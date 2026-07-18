//go:build linux

package network

import (
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Timer 定时器：cb 在到期时于事件循环线程执行。
type Timer struct {
	id        uint64
	cb        func()
	deadline  uint64 // 绝对到期 tick（以启动后 tick 计）
	interval  uint64 // 周期间隔 tick；0=一次性
}

// TimerQueue 时间轮定时器队列。
//
// 设计：timerfd 按 tick 周期唤醒事件循环，每次唤醒读取溢出 tick 数 n，推进 n 个槽。
// 槽位按 (deadline % slots) 哈希；处理某槽时遍历链表，到期则触发回调，
// 周期性定时器按新 deadline 重新入槽，未到期的（跨轮长延时）原槽保留待下轮。
//
// 仅在所属事件循环线程调用 AddTimer/AddTimerEvery/Cancel/handleTick，
// 故内部无锁。跨线程需经 EventLoop.RunInLoop 投递。
type TimerQueue struct {
	fd       int
	tick     time.Duration
	slots    uint64
	slotMask uint64
	wheel    [][]*Timer
	cur      uint64 // 当前 tick 计数
	nextID   atomic.Uint64
	pending  map[uint64]*Timer // id→Timer，供取消与防悬挂
}

// newTimerQueue 创建时间轮与 timerfd。tick 为最小粒度，slots 为槽数（向上取 2 的幂）。
func newTimerQueue(tick time.Duration, slots int) (*TimerQueue, error) {
	if tick <= 0 {
		tick = 100 * time.Millisecond
	}
	if slots <= 0 {
		slots = 256
	}
	slots = ceilPow2Min(slots, 8)

	fd, err := unix.TimerfdCreate(unix.CLOCK_MONOTONIC, unix.TFD_CLOEXEC|unix.TFD_NONBLOCK)
	if err != nil {
		return nil, err
	}
	spec := unix.ItimerSpec{
		Value:    unix.NsecToTimespec(int64(tick)),
		Interval: unix.NsecToTimespec(int64(tick)),
	}
	if err = unix.TimerfdSettime(fd, 0, &spec, nil); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	tq := &TimerQueue{
		fd:       fd,
		tick:     tick,
		slots:    uint64(slots),
		slotMask: uint64(slots) - 1,
		wheel:    make([][]*Timer, slots),
		pending:  make(map[uint64]*Timer),
	}
	return tq, nil
}

// Fd 返回 timerfd，供 EventLoop 注册 Channel。
func (tq *TimerQueue) Fd() int { return tq.fd }

// ticks 把时长换算为 tick 数，不足一个 tick 取 1。
func (tq *TimerQueue) ticks(d time.Duration) uint64 {
	n := uint64(d / tq.tick)
	if n == 0 {
		n = 1
	}
	return n
}

// insert 把定时器放入对应到期槽。
func (tq *TimerQueue) insert(tm *Timer) {
	slot := tm.deadline & tq.slotMask
	tq.wheel[slot] = append(tq.wheel[slot], tm)
}

// AddTimer 添加一次性定时器，d 后在事件循环线程执行 cb。返回可取消 id。
func (tq *TimerQueue) AddTimer(d time.Duration, cb func()) uint64 {
	id := tq.nextID.Add(1)
	tm := &Timer{
		id:       id,
		cb:       cb,
		deadline: tq.cur + tq.ticks(d),
	}
	tq.pending[id] = tm
	tq.insert(tm)
	return id
}

// AddTimerEvery 添加周期定时器，每 d 执行一次 cb，返回可取消 id。
func (tq *TimerQueue) AddTimerEvery(d time.Duration, cb func()) uint64 {
	id := tq.nextID.Add(1)
	interval := tq.ticks(d)
	tm := &Timer{
		id:        id,
		cb:        cb,
		deadline:  tq.cur + interval,
		interval:  interval,
	}
	tq.pending[id] = tm
	tq.insert(tm)
	return id
}

// Cancel 取消定时器。仅从 pending 移除标记；已在槽链表中的节点到槽时被跳过。
func (tq *TimerQueue) Cancel(id uint64) {
	delete(tq.pending, id)
}

// handleTick 读 timerfd 溢出计数，推进相应 tick 数并触发到期回调。
// 由 EventLoop 在 timerfd 可读时调用（事件循环线程）。
func (tq *TimerQueue) handleTick() {
	n := tq.readCount()
	for range n {
		tq.cur++
		tq.processSlot(tq.cur & tq.slotMask)
	}
}

// processSlot 处理某槽：到期触发，周期重排，未到期的原槽保留。
func (tq *TimerQueue) processSlot(slot uint64) {
	list := tq.wheel[slot]
	tq.wheel[slot] = nil
	for _, tm := range list {
		if _, ok := tq.pending[tm.id]; !ok {
			continue // 已取消，丢弃。
		}
		if tm.deadline <= tq.cur {
			tm.cb()
			if tm.interval > 0 {
				tm.deadline = tq.cur + tm.interval
				tq.insert(tm) // 重排到新槽
			} else {
				delete(tq.pending, tm.id)
			}
		} else {
			tq.insert(tm) // 跨轮长延时，原槽保留待下轮
		}
	}
}

// readCount 读 timerfd 的 8 字节溢出计数（原生字节序）。
func (tq *TimerQueue) readCount() uint64 {
	var buf [8]byte
	if _, err := unix.Read(tq.fd, buf[:]); err != nil {
		return 0 // EAGAIN：无溢出。
	}
	return uint64(*(*uint64)(unsafe.Pointer(&buf[0])))
}

// Close 关闭 timerfd。
func (tq *TimerQueue) Close() error {
	if tq.fd > 0 {
		err := unix.Close(tq.fd)
		tq.fd = -1
		return err
	}
	return nil
}
