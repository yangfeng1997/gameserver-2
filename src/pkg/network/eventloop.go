//go:build linux

package network

import (
	"runtime"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// EventLoop 单线程事件循环：一个 goroutine 独占跑 epoll_wait，串行处理 I/O 与回调。
// 跨线程交互经 wakeup（eventfd + 无锁队列）投递；定时器经 timerfd + 时间轮。
type EventLoop struct {
	poller       *Poller
	wakeup       *loopWakeup
	timers       *TimerQueue
	wakeupCh     *Channel // eventfd 的 Channel
	timerCh      *Channel // timerfd 的 Channel
	opts         Options
	logger       Logger
	tid          atomic.Int32 // loop goroutine 绑定的 OS 线程 id（LockOSThread 时有效）；atomic 防跨 loop RunInLoop 读/Loop 写竞态
	lockOSThread bool
	running      atomic.Bool
	quit         atomic.Bool
	connCount    atomic.Int32 // 该 loop 上活跃连接数，供 least-connections 分发
	// per-loop 复用读辅助缓冲与 iovec，所有连接共享，热路径零分配、零 per-conn 内存。
	readExtra [extraBufSize]byte
	readIov   [2][]byte
	// per-loop 用户上下文（loop 内访问，非线程安全）。挂后端池/统计/缓存等 loop 局部状态。
	ctx any
}

// SetContext 设置 per-loop 上下文（须在 loop 线程内调用，如 OnConnect/OnMessage）。
func (el *EventLoop) SetContext(v any) { el.ctx = v }

// Context 取 per-loop 上下文。未设置返回 nil。
func (el *EventLoop) Context() any { return el.ctx }

// AddConn / SubConn 连接计数维护，由 Conn 生命周期调用。
func (el *EventLoop) AddConn() { el.connCount.Add(1) }
func (el *EventLoop) SubConn() { el.connCount.Add(-1) }

// ConnCount 当前 loop 活跃连接数。
func (el *EventLoop) ConnCount() int32 { return el.connCount.Load() }

// NewEventLoop 构造事件循环（尚未启动）。
func NewEventLoop(o Options) (*EventLoop, error) {
	poller, err := NewPoller(o.EdgeTriggeredIO)
	if err != nil {
		return nil, err
	}
	wk, err := newWakeup()
	if err != nil {
		_ = poller.Close()
		return nil, err
	}
	tq, err := newTimerQueue(o.TimerTick, o.TimerSlots)
	if err != nil {
		_ = wk.Close()
		_ = poller.Close()
		return nil, err
	}
	return &EventLoop{
		poller:       poller,
		wakeup:       wk,
		timers:       tq,
		opts:        o,
		logger:       o.Logger,
		lockOSThread: o.LockOSThread,
	}, nil
}

// Poller 暴露底层 Poller（Conn/Acceptor 注册 Channel 用）。
func (el *EventLoop) Poller() *Poller { return el.poller }

// Opts 暴露配置（Conn/Acceptor 读缓冲容量等）。
func (el *EventLoop) Opts() Options { return el.opts }

// Logger 暴露日志。
func (el *EventLoop) Logger() Logger { return el.logger }

// Loop 启动事件循环，阻塞直至 Quit。必须在其所属 goroutine 调用。
func (el *EventLoop) Loop() {
	if el.running.Swap(true) {
		// 已在运行。
		return
	}

	if el.lockOSThread {
		runtime.LockOSThread()
		el.tid.Store(int32(unix.Gettid()))
	}

	// 注册 eventfd / timerfd 的 Channel（在 loop 线程上 epoll_ctl）。
	el.wakeupCh = NewChannel(el.wakeup.Fd(), el.poller)
	el.wakeupCh.SetReadCallback(el.handleWakeupRead)
	el.wakeupCh.EnableReading()

	el.timerCh = NewChannel(el.timers.fd, el.poller)
	el.timerCh.SetReadCallback(el.timers.handleTick)
	el.timerCh.EnableReading()

	for !el.quit.Load() {
		active, err := el.poller.Poll(-1)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			el.logger.Errorf("eventloop poll: %v", err)
			continue
		}
		for _, ch := range active {
			ch.HandleEvent()
		}
	}

	// 优雅关闭本 loop 上所有业务连接（wakeup/timer 的 closeCb 为 nil，自动跳过）。
	// teardown 走 channel.Remove + 关 fd + 触发 OnClose；快照避免遍历时改 map。
	// 关停中（Client.Stop 已置 IsStopping）的客户端 OnClose 据此跳过重连。
	for _, ch := range el.poller.snapshotChannels() {
		if ch.closeCb != nil {
			ch.closeCb()
		}
	}

	// 清理内部 Channel 与 fd。
	el.wakeupCh.DisableAll()
	el.wakeupCh.Remove()
	el.timerCh.DisableAll()
	el.timerCh.Remove()
	_ = el.wakeup.Close()
	_ = el.timers.Close()
	_ = el.poller.Close()
	el.running.Store(false)
}

// IsInLoopThread 当前是否在事件循环线程。
// LockOSThread 时用 gettid 精确判定；未开则保守返回 false（跨线程处理，安全但稍慢）。
func (el *EventLoop) IsInLoopThread() bool {
	if !el.lockOSThread {
		return false
	}
	return int32(unix.Gettid()) == el.tid.Load()
}

// Quit 请求停止循环并唤醒（线程安全）。
func (el *EventLoop) Quit() {
	if el.quit.CompareAndSwap(false, true) {
		el.wakeup.Wake()
	}
}

// RunInLoop 若在 loop 线程则立即执行，否则投递到 loop 下一轮执行。
func (el *EventLoop) RunInLoop(fn func()) {
	if el.IsInLoopThread() {
		fn()
		return
	}
	el.QueueInLoop(fn)
}

// QueueInLoop 总是投递到 loop 下一轮执行（即便当前在 loop 线程也延后到下轮）。
func (el *EventLoop) QueueInLoop(fn func()) {
	el.wakeup.Run(fn)
}

// QueueSend 跨线程类型化投递发送任务（零闭包）。在 loop 下一轮执行 conn.sendInLoop。
func (el *EventLoop) QueueSend(c *Conn, data []byte) {
	el.wakeup.pushSend(c, data)
	el.wakeup.Wake()
}

// RunAfter 在 d 后执行 cb，返回可取消定时器 id。
func (el *EventLoop) RunAfter(d time.Duration, cb func()) uint64 {
	return el.timers.AddTimer(d, cb)
}

// RunEvery 每 d 执行 cb，返回可取消定时器 id。
func (el *EventLoop) RunEvery(d time.Duration, cb func()) uint64 {
	return el.timers.AddTimerEvery(d, cb)
}

// CancelTimer 取消定时器。
func (el *EventLoop) CancelTimer(id uint64) {
	el.timers.Cancel(id)
}

// handleWakeupRead eventfd 可读：清计数、分发所有待执行任务、rearm 防丢唤醒。
// taskSend 走类型化零闭包路径直接 sendInLoop，省去闭包分配。
func (el *EventLoop) handleWakeupRead() {
	el.wakeup.Read()
	for {
		t, ok := el.wakeup.queue.pop()
		if !ok {
			break
		}
		switch t.kind {
		case taskFunc:
			if t.fn != nil {
				t.fn()
			}
		case taskSend:
			t.conn.sendInLoop(t.data)
		}
	}
	el.wakeup.rearm()
}
