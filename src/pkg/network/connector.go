//go:build linux

package network

import (
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// ConnectorState 拨号状态。
type ConnectorState int32

const (
	csDisconnected ConnectorState = iota
	csConnecting
	csConnected
)

// OnConnectedCallback 拨号成功回调，传递已连 fd 与本地/对端地址，由调用方在目标 loop 建 Conn。
type OnConnectedCallback func(fd int, local, peer Address)

const (
	retryInitial = 500 * time.Millisecond
	retryMax      = 30 * time.Second
)

// Connector 异步拨号器（muduo Connector）：非阻塞 connect + EPOLLOUT 就绪判定，
// 支持指数退避重试。供客户端重连等场景使用。
type Connector struct {
	loop        *EventLoop
	opts        Options
	logger      Logger
	network     string
	addr        Address
	fd          int
	channel     *Channel
	state       atomic.Int32
	retryDelay  time.Duration
	timerID     uint64
	retry       bool
	onConnected OnConnectedCallback
}

// NewConnector 构造拨号器，绑定指定 loop 驱动。
func NewConnector(loop *EventLoop, network, address string, o Options) (*Connector, error) {
	_, addr, err := ParseAddress(network, address)
	if err != nil {
		return nil, err
	}
	return &Connector{
		loop: loop, opts: o, logger: loop.Logger(),
		network: network, addr: addr,
		retryDelay: retryInitial,
	}, nil
}

// SetOnConnected 设置成功回调。
func (cr *Connector) SetOnConnected(cb OnConnectedCallback) { cr.onConnected = cb }

// SetRetry 是否开启失败重试（指数退避至上限）。
func (cr *Connector) SetRetry(b bool) { cr.retry = b }

// Start 启动拨号（投递到 loop）。
func (cr *Connector) Start() { cr.loop.RunInLoop(cr.startInLoop) }

func (cr *Connector) startInLoop() {
	if cr.state.Load() != int32(csDisconnected) {
		return
	}
	cr.state.Store(int32(csConnecting))
	fd, err := createConnector(cr.addr, cr.opts)
	if err == nil {
		// 立即连上。
		cr.handleConnectSuccess(fd)
		return
	}
	if err != unix.EINPROGRESS {
		cr.handleErr(err)
		return
	}
	// EINPROGRESS：注册可写，等就绪后校验 SO_ERROR。
	cr.fd = fd
	cr.channel = NewChannel(fd, cr.loop.Poller())
	cr.channel.SetWriteCallback(cr.handleWrite)
	cr.channel.EnableWriting()
}

func (cr *Connector) handleWrite() {
	if cr.state.Load() != int32(csConnecting) {
		return
	}
	if cerr := connectError(cr.fd); cerr != nil {
		cr.handleErr(cerr)
		return
	}
	cr.handleConnectSuccess(cr.fd)
}

func (cr *Connector) handleConnectSuccess(fd int) {
	cr.state.Store(int32(csConnected))
	if cr.channel != nil {
		cr.channel.DisableAll()
		cr.channel.Remove()
		cr.channel = nil
	}
	local, _ := sockName(fd)
	peer, _ := peerName(fd)
	if cr.onConnected != nil {
		cr.onConnected(fd, local, peer)
	}
}

func (cr *Connector) handleErr(err error) {
	cr.logger.Warnf("connector connect err: %v", err)
	if cr.channel != nil {
		cr.channel.DisableAll()
		cr.channel.Remove()
		cr.channel = nil
	}
	if cr.fd > 0 {
		_ = unix.Close(cr.fd)
		cr.fd = -1
	}
	cr.state.Store(int32(csDisconnected))
	if cr.retry {
		cr.retryDelay *= 2
		if cr.retryDelay > retryMax {
			cr.retryDelay = retryMax
		}
		cr.timerID = cr.loop.RunAfter(cr.retryDelay, cr.startInLoop)
	}
}

// Stop 停止拨号并取消重试定时器（投递到 loop）。
func (cr *Connector) Stop() { cr.loop.RunInLoop(cr.stopInLoop) }

func (cr *Connector) stopInLoop() {
	cr.state.Store(int32(csDisconnected))
	if cr.channel != nil {
		cr.channel.DisableAll()
		cr.channel.Remove()
		cr.channel = nil
	}
	if cr.fd > 0 {
		_ = unix.Close(cr.fd)
		cr.fd = -1
	}
	if cr.timerID != 0 {
		cr.loop.CancelTimer(cr.timerID)
		cr.timerID = 0
	}
}

// waitWritable 同步等待 fd 可写或超时，供客户端同步 Dial 路径使用。
// 直接 syscall（unix.Poll），不经 runtime netpoller，与 loop poller 不冲突。
func waitWritable(fd int, timeout time.Duration) error {
	ms := int(timeout / time.Millisecond)
	if ms <= 0 {
		ms = 5000
	}
	pf := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
	for {
		n, err := unix.Poll(pf, ms)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return err
		}
		if n == 0 {
			return ErrConnectionRefused
		}
		return nil
	}
}
