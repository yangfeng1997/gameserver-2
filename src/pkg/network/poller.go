//go:build linux

package network

import (
	"golang.org/x/sys/unix"
)

// Poller 封装 epoll 实例，负责 fd 的注册/修改/移除与就绪事件轮询。
// 每个 EventLoop 拥有一个 Poller，在其 loop goroutine 上独占调用。
type Poller struct {
	epfd    int                 // epoll fd
	edge    bool                // 是否边沿触发（追加 EPOLLET）
	channels map[int]*Channel   // fd → Channel
	events  []unix.EpollEvent   // epoll_wait 复用缓冲
	active  []*Channel          // 就绪 Channel 复用切片
}

// NewPoller 创建 epoll 实例。edge 为 true 时对所有 fd 追加 EPOLLET。
func NewPoller(edge bool) (*Poller, error) {
	epfd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		return nil, err
	}
	return &Poller{
		epfd:     epfd,
		edge:     edge,
		channels: make(map[int]*Channel),
		events:   make([]unix.EpollEvent, 0, 128),
		active:   make([]*Channel, 0, 128),
	}, nil
}

// Close 关闭 epoll fd。
func (p *Poller) Close() error {
	if p.epfd > 0 {
		err := unix.Close(p.epfd)
		p.epfd = -1
		return err
	}
	return nil
}

// HasChannel 是否已注册该 fd。
func (p *Poller) HasChannel(fd int) bool {
	_, ok := p.channels[fd]
	return ok
}

// snapshotChannels 返回当前所有 Channel 的快照切片，供 loop 退出时逐个清理
// （teardown 会 channel.Remove 改 map，故先快照再遍历）。
func (p *Poller) snapshotChannels() []*Channel {
	out := make([]*Channel, 0, len(p.channels))
	for _, ch := range p.channels {
		out = append(out, ch)
	}
	return out
}

// mask 组装 epoll 事件掩码：逻辑 events + 可选 EPOLLET。
func (p *Poller) mask(events uint32) uint32 {
	if p.edge {
		events |= unix.EPOLLET
	}
	return events
}

// UpdateChannel 同步 Channel 关心事件到 epoll。
// 状态机（muduo）：
//   - new → ADD，index=added
//   - added → 无事件则 DEL（index=deleted），否则 MOD
//   - deleted → 有事件则 ADD（index=added），无事件不动
func (p *Poller) UpdateChannel(ch *Channel) {
	fd := ch.fd
	if ch.index == chanNew || ch.index == chanDeleted {
		// 需要新增/重新加入。
		if ch.events != 0 {
			ev := unix.EpollEvent{Events: p.mask(ch.events), Fd: int32(fd)}
			if err := unix.EpollCtl(p.epfd, unix.EPOLL_CTL_ADD, fd, &ev); err != nil {
				// 已存在则改 MOD，避免 EINVAL。
				if err == unix.EEXIST {
					_ = unix.EpollCtl(p.epfd, unix.EPOLL_CTL_MOD, fd, &ev)
				}
			}
			ch.index = chanAdded
			p.channels[fd] = ch
		}
		return
	}
	// index == chanAdded
	if ch.events == 0 {
		// 取消所有 → DEL。
		_ = unix.EpollCtl(p.epfd, unix.EPOLL_CTL_DEL, fd, nil)
		ch.index = chanDeleted
	} else {
		ev := unix.EpollEvent{Events: p.mask(ch.events), Fd: int32(fd)}
		_ = unix.EpollCtl(p.epfd, unix.EPOLL_CTL_MOD, fd, &ev)
	}
}

// RemoveChannel 从 epoll 与 map 移除。
func (p *Poller) RemoveChannel(ch *Channel) {
	fd := ch.fd
	if ch.index == chanAdded {
		_ = unix.EpollCtl(p.epfd, unix.EPOLL_CTL_DEL, fd, nil)
	}
	ch.index = chanNew
	delete(p.channels, fd)
}

// Poll 阻塞至多 timeoutMs 毫秒，返回就绪 Channel 列表（复用内部切片）。
// timeoutMs=-1 永久阻塞直至有事件。EINTR 自动重试。
// 仅当返回事件数等于当前容量（可能还有更多就绪）时扩容，避免无谓分配。
func (p *Poller) Poll(timeoutMs int) ([]*Channel, error) {
	if cap(p.events) == 0 {
		p.events = make([]unix.EpollEvent, 128)
	}
	events := p.events[:cap(p.events)]

	for {
		n, err := unix.EpollWait(p.epfd, events, timeoutMs)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return nil, err
		}
		// 收集就绪 Channel，复用 active 切片。
		active := p.active[:0]
		for i := range n {
			fd := int(events[i].Fd)
			ch, ok := p.channels[fd]
			if !ok {
				continue
			}
			ch.setRevents(events[i].Events)
			active = append(active, ch)
		}
		p.active = active
		// 满载才扩容（muduo 原意：返回了上限说明可能还有未取走的就绪事件）。
		if n == len(events) && cap(p.events) < 1024 {
			p.events = make([]unix.EpollEvent, cap(p.events)*2)
		}
		return active, nil
	}
}
