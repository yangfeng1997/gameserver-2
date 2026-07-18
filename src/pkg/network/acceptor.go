//go:build linux

package network

import (
	"os"

	"golang.org/x/sys/unix"
)

// NewConnectionCallback Acceptor 在 accept 成功后回调上层，传递新 fd 与本地/对端地址。
type NewConnectionCallback func(fd int, local, peer Address)

// Acceptor 监听 fd 的封装：accept 循环到 EAGAIN，每条新连接回调上层建立 Conn。
type Acceptor struct {
	loop             *EventLoop
	channel          *Channel
	fd               int
	localAddr        Address // 监听地址，用作新连接 local（免 per-accept getsockname）
	opts             Options
	logger           Logger
	onNewConnection NewConnectionCallback
}

// NewAcceptor 在指定 loop 上创建监听 fd 并 bind/listen。尚未注册到 epoll。
func NewAcceptor(loop *EventLoop, network, address string, o Options) (*Acceptor, error) {
	_, addr, err := ParseAddress(network, address)
	if err != nil {
		return nil, err
	}
	fd, err := createListener(network, addr, o)
	if err != nil {
		return nil, err
	}
	a := &Acceptor{
		loop:      loop,
		fd:        fd,
		localAddr: addr,
		opts:      o,
		logger:    loop.Logger(),
	}
	a.channel = NewChannel(fd, loop.Poller())
	a.channel.SetReadCallback(a.handleRead)
	return a, nil
}

// Fd 返回监听 fd。
func (a *Acceptor) Fd() int { return a.fd }

// LocalAddr 返回监听本地地址。
func (a *Acceptor) LocalAddr() Address { return a.localAddr }

// SetNewConnectionCallback 注册新连接回调。
func (a *Acceptor) SetNewConnectionCallback(cb NewConnectionCallback) {
	a.onNewConnection = cb
}

// Listen 注册可读，开始接受连接。
func (a *Acceptor) Listen() {
	a.channel.EnableReading()
}

// Stop 停止监听并关闭 fd（Unix 路径随之 unlink）。
func (a *Acceptor) Stop() {
	a.channel.DisableAll()
	a.channel.Remove()
	_ = unix.Close(a.fd)
	if a.localAddr.IsUnix() {
		_ = os.Remove(a.localAddr.path)
	}
}

// handleRead 可读就绪：循环 accept 到 EAGAIN，每条新连接应用 TCP 选项并回调上层。
func (a *Acceptor) handleRead() {
	for {
		nfd, peer, err := acceptConnection(a.fd)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				return
			}
			if err == unix.EINTR {
				continue
			}
			// EMFILE/ENFILE：fd 耗尽，记日志后退出（v1 不做 idle fd 技巧）。
			a.logger.Errorf("accept err: %v", err)
			return
		}
		applyConnOpts(nfd, a.opts)
		if a.onNewConnection != nil {
			a.onNewConnection(nfd, a.localAddr, peer)
		}
	}
}
