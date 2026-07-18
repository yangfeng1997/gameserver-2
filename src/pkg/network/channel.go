//go:build linux

package network

import "golang.org/x/sys/unix"

// Channel 是 fd + 关心事件 + 就绪事件 + 各类回调的集合，epoll 的操作单位（muduo 命名）。
// 一个 fd 对应一个 Channel：Conn 与 Acceptor 的 listen fd 各持一个。
//
// 设计要点：
//   - 持有 poller 指针而非 loop，仅做 epoll_ctl 更新/移除，与 EventLoop 解耦。
//   - events 存逻辑关心事件（IN/OUT/RDHUP），EPOLLET 由 Poller 按全局配置统一追加。
//   - index 标记在 poller 中的状态（new/added/deleted），决定 ADD/MOD/DEL。
//   - 无 tie_：Go GC + Conn 由 EventLoop.connMap 持有，handleEvent 期间不会被回收。
type Channel struct {
	fd      int
	poller  *Poller
	events  uint32 // 关心事件
	revents uint32 // 就绪事件
	index   int    // poller 状态：chanNew/chanAdded/chanDeleted

	readCb  func()
	writeCb func()
	closeCb func()
	errorCb func()
}

// Channel 在 poller 中的状态。
const (
	chanNew = iota // 未加入 poller
	chanAdded      // 已 ADD
	chanDeleted    // 已 DEL（但 Channel 对象仍在）
)

// NewChannel 构造，归属指定 poller。
func NewChannel(fd int, p *Poller) *Channel {
	return &Channel{fd: fd, poller: p, index: chanNew}
}

// Fd 返回底层 fd。
func (c *Channel) Fd() int { return c.fd }

// IsNoneEvent 当前无任何关心事件。
func (c *Channel) IsNoneEvent() bool { return c.events == 0 }

// IsWriting 关心可写事件。
func (c *Channel) IsWriting() bool { return c.events&unix.EPOLLOUT != 0 }

// IsReading 关心可读事件。
func (c *Channel) IsReading() bool { return c.events&unix.EPOLLIN != 0 }

// EnableReading 关心可读 + 对端关闭（RDHUP），并更新 epoll。
func (c *Channel) EnableReading() {
	c.events |= unix.EPOLLIN | unix.EPOLLRDHUP
	c.update()
}

// DisableReading 取消可读。
func (c *Channel) DisableReading() {
	c.events &^= unix.EPOLLIN
	c.update()
}

// EnableWriting 关心可写。
func (c *Channel) EnableWriting() {
	c.events |= unix.EPOLLOUT
	c.update()
}

// DisableWriting 取消可写。
func (c *Channel) DisableWriting() {
	c.events &^= unix.EPOLLOUT
	c.update()
}

// DisableAll 取消所有关心事件。
func (c *Channel) DisableAll() {
	c.events = 0
	c.update()
}

// update 通知 poller 同步 epoll_ctl。必须在所属 loop 线程调用。
func (c *Channel) update() {
	c.poller.UpdateChannel(c)
}

// Remove 从 poller 移除（EPOLL_CTL_DEL 或清理状态）。
func (c *Channel) Remove() {
	c.poller.RemoveChannel(c)
}

// SetReadCallback 设置可读就绪回调。
func (c *Channel) SetReadCallback(cb func()) { c.readCb = cb }

// SetWriteCallback 设置可写就绪回调。
func (c *Channel) SetWriteCallback(cb func()) { c.writeCb = cb }

// SetCloseCallback 设置对端关闭/连接断开回调。
func (c *Channel) SetCloseCallback(cb func()) { c.closeCb = cb }

// SetErrorCallback 设置错误就绪回调。
func (c *Channel) SetErrorCallback(cb func()) { c.errorCb = cb }

// HandleEvent 按 revents 分发到对应回调。由 EventLoop 在 Poll 后调用。
//
// 处理顺序遵循 muduo：
//   - EPOLLHUP 且非可写 → 对端关闭，触发 closeCb；
//   - EPOLLERR → 错误回调；
//   - EPOLLIN/EPOLLPRI → 可读回调；
//   - EPOLLOUT → 可写回调；
//   - HUP 且无读 → closeCb（兜底）。
func (c *Channel) HandleEvent() {
	if c.revents&(unix.EPOLLHUP|unix.EPOLLRDHUP) != 0 && c.revents&unix.EPOLLOUT == 0 {
		if c.closeCb != nil {
			c.closeCb()
		}
		// 对端关闭后不再分发后续事件。
		return
	}
	if c.revents&unix.EPOLLERR != 0 {
		if c.errorCb != nil {
			c.errorCb()
		}
	}
	if c.revents&(unix.EPOLLIN|unix.EPOLLPRI) != 0 {
		if c.readCb != nil {
			c.readCb()
		}
	}
	if c.revents&unix.EPOLLOUT != 0 {
		if c.writeCb != nil {
			c.writeCb()
		}
	}
	if c.revents&unix.EPOLLHUP != 0 && c.revents&(unix.EPOLLIN|unix.EPOLLOUT) == 0 {
		if c.closeCb != nil {
			c.closeCb()
		}
	}
}

// setRevents 由 Poller.Poll 设置就绪事件。
func (c *Channel) setRevents(ev uint32) { c.revents = ev }
