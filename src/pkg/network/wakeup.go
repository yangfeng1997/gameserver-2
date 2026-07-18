//go:build linux

package network

import (
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// taskKind 任务类型：通用闭包或类型化发送（后者零闭包分配）。
type taskKind int

const (
	taskFunc taskKind = iota
	taskSend
)

// taskNode 无锁任务队列节点。
type taskNode struct {
	next atomic.Pointer[taskNode]
	kind taskKind
	fn   func()   // taskFunc 用
	conn *Conn    // taskSend 用
	data []byte   // taskSend 用
}

// task 值形式任务，pop 时拷出供分发（避免返回节点指针）。
type task struct {
	kind taskKind
	fn   func()
	conn *Conn
	data []byte
}

// mpscQueue 单消费者多生产者无锁 FIFO 队列（Vyukov 算法）。
//
// 生产端用 atomic Swap 接力链接节点（无 CAS 重试），消费端单线程沿 next 链摘取。
// 借助 eventfd 唤醒兜底"生产者已 Swap tail 但 prev.next 尚未链接"的瞬时窗口：
// 生产者先 Store 链接、再 Wake，故消费端被唤醒时链接必已完成。
type mpscQueue struct {
	stub taskNode
	head atomic.Pointer[taskNode] // 消费端：当前哨兵
	tail atomic.Pointer[taskNode] // 生产端：最后一个节点
	pool sync.Pool                // 节点复用，压分配
}

func newMPSCQueue() *mpscQueue {
	q := &mpscQueue{
		pool: sync.Pool{New: func() any { return new(taskNode) }},
	}
	q.head.Store(&q.stub)
	q.tail.Store(&q.stub)
	return q
}

func (q *mpscQueue) acquire() *taskNode {
	n := q.pool.Get().(*taskNode)
	n.next.Store(nil)
	return n
}

func (q *mpscQueue) release(n *taskNode) {
	n.kind = taskFunc
	n.fn = nil
	n.conn = nil
	n.data = nil
	n.next.Store(nil)
	q.pool.Put(n)
}

// push 通用闭包任务（生产端，线程安全）。
func (q *mpscQueue) push(fn func()) {
	n := q.acquire()
	n.kind = taskFunc
	n.fn = fn
	prev := q.tail.Swap(n)
	prev.next.Store(n)
}

// pushSend 类型化发送任务（零闭包，生产端线程安全）。
func (q *mpscQueue) pushSend(c *Conn, data []byte) {
	n := q.acquire()
	n.kind = taskSend
	n.conn = c
	n.data = data
	prev := q.tail.Swap(n)
	prev.next.Store(n)
}

// pop 消费端：仅事件循环单线程调用。空返回 ok=false。
func (q *mpscQueue) pop() (task, bool) {
	head := q.head.Load()
	next := head.next.Load()
	if next == nil {
		return task{}, false
	}
	t := task{kind: next.kind, fn: next.fn, conn: next.conn, data: next.data}
	next.kind = taskFunc
	next.fn = nil
	next.conn = nil
	next.data = nil
	q.head.Store(next)
	if head != &q.stub {
		q.release(head)
	}
	return t, true
}

// nonEmpty 队列是否非空（消费端 rearm 用，可能有瞬时窗口，配合生产者 push-then-Wake 顺序安全）。
func (q *mpscQueue) nonEmpty() bool {
	return q.head.Load().next.Load() != nil
}

// loopWakeup 封装 eventfd + 任务队列，供 EventLoop 跨线程唤醒与投递任务。
type loopWakeup struct {
	fd      atomic.Int32 // eventfd；Close 用 Swap(-1) 抢占，Wake 跨线程 Load 读，避免与 teardown Close 的 fd 竞态
	queue   *mpscQueue
	pending atomic.Bool // 是否已挂起未消费的唤醒，CAS 合并写 eventfd
}

func newWakeup() (*loopWakeup, error) {
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return nil, err
	}
	w := &loopWakeup{queue: newMPSCQueue()}
	w.fd.Store(int32(fd))
	return w, nil
}

func (w *loopWakeup) Fd() int { return int(w.fd.Load()) }

// Run 投递通用闭包任务并唤醒（生产端，线程安全）。
func (w *loopWakeup) Run(fn func()) {
	w.queue.push(fn)
	w.Wake()
}

// pushSend 投递类型化发送任务（零闭包），由 EventLoop.QueueSend 调用。
func (w *loopWakeup) pushSend(c *Conn, data []byte) {
	w.queue.pushSend(c, data)
}

// Wake 合并写 eventfd：仅当 pending 由 false→true 时写一次，burst 内多次调用只唤醒一次。
func (w *loopWakeup) Wake() {
	if !w.pending.CompareAndSwap(false, true) {
		return
	}
	w.writeEventfd()
}

func (w *loopWakeup) writeEventfd() {
	fd := w.fd.Load()
	if fd < 0 {
		return // 已 Close，teardown 与跨线程 Quit.Wake 竞争时静默丢弃。
	}
	var buf [8]byte
	buf[0] = 1
	_, _ = unix.Write(int(fd), buf[:])
}

// Read 读掉 eventfd 计数，消除可读状态。
func (w *loopWakeup) Read() {
	var buf [8]byte
	for {
		if _, err := unix.Read(int(w.fd.Load()), buf[:]); err != nil {
			break // EAGAIN 已读空。
		}
	}
}

// rearm 消费完一批任务后重置 pending；若期间又有任务入队则补唤醒，防丢唤醒。
// 生产者 push(链接) 在 Wake 之前，故 pending 期间到达的任务其链接必可见。
func (w *loopWakeup) rearm() {
	w.pending.Store(false)
	if w.queue.nonEmpty() && w.pending.CompareAndSwap(false, true) {
		w.writeEventfd()
	}
}

func (w *loopWakeup) Close() error {
	if fd := w.fd.Swap(-1); fd > 0 {
		return unix.Close(int(fd))
	}
	return nil
}
