//go:build linux

package network

import (
	"fmt"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// Client 独立客户端（gnet 风格）：拥有一组 sub loop，同步拨号成功后把 fd 移交目标 loop 建 Conn。
// 支持自定义连接级 TCP 选项与 UDS 拨号。
type Client struct {
	opts     Options
	logger   Logger
	loops    *EventLoopGroup
	started  atomic.Bool
	stopping atomic.Bool // 关停中标志，供回调判断是否跳过重连等"启动新工作"逻辑
	ownsLoops bool       // 是否自建 loop 池（注入的归所有者管，Stop 不停）
	nextID   atomic.Uint64

	onConnect       ConnCallback
	onMessage       MessageCallback
	onWriteComplete ConnCallback
	onClose         ConnCallback
	conn            *Conn // 最近 dial 的 conn（单连接场景下 cli.Conn().Send 一句柄；清理靠 loop 退出 teardown）
}

// NewClient 构造客户端，应用函数式选项。loop 池自建。
func NewClient(opts ...Option) *Client {
	o := Apply(opts)
	return &Client{
		opts:      o,
		logger:    o.Logger,
		loops:     NewEventLoopGroup(nil, o),
		ownsLoops: true,
	}
}

// NewClientWithLoops 构造客户端但复用已有 EventLoopGroup（典型为 Server 的 loop 池），
// 使后端 conn 与玩家 conn 落在同一套 loop 上，配合 loop.Dial 可实现 per-loop in-loop 转发。
// 注意：被注入的 loop 池生命周期由所有者（如 Server）管理，Client.Stop 不会停它。
func NewClientWithLoops(loops *EventLoopGroup, opts ...Option) *Client {
	o := Apply(opts)
	return &Client{
		opts:   o,
		logger: o.Logger,
		loops:  loops,
	}
}

// Loops 暴露 loop 池（供业务取指定 loop 做 loop.Dial 等）。
func (c *Client) Loops() *EventLoopGroup { return c.loops }

// SetOnConnect / SetOnMessage / SetOnWriteComplete / SetOnClose 设置连接回调。
func (c *Client) SetOnConnect(cb ConnCallback)         { c.onConnect = cb }
func (c *Client) SetOnMessage(cb MessageCallback)       { c.onMessage = cb }
func (c *Client) SetOnWriteComplete(cb ConnCallback)    { c.onWriteComplete = cb }
func (c *Client) SetOnClose(cb ConnCallback)            { c.onClose = cb }

// Start 启动 sub loop 池（仅自建池；注入的由所有者管理）。Dial 前未调用会自动启动。
func (c *Client) Start() {
	if !c.ownsLoops {
		return // 注入的 loop 池归所有者管。
	}
	if !c.started.CompareAndSwap(false, true) {
		return
	}
	c.loops.Start()
}

// Stop 停止 sub loop 池并等待退出（仅自建池）。置 stopping 标志，回调可据此跳过重连。
func (c *Client) Stop() {
	if !c.ownsLoops {
		c.stopping.Store(true)
		return // 注入的 loop 池由所有者（如 Server）停。
	}
	if !c.started.CompareAndSwap(true, false) {
		return
	}
	c.stopping.Store(true)
	c.loops.Stop()
}

// IsStopping 是否处于关停中（线程安全）。典型在 OnClose 等回调里判断，主动关停时跳过重连。
func (c *Client) IsStopping() bool { return c.stopping.Load() }

// Dial 同步拨号：非阻塞 connect + 等待可写（DialTimeout），成功后把 fd 移交目标 loop 建 Conn。
// 返回的 Conn 已置 Connected，可立即 Send；可读注册在目标 loop 上异步完成。
// loop 由 c.loops.GetNextLoop(peer) 选定；要指定 loop 用 DialOn（per-loop 亲和）。
func (c *Client) Dial(network, address string) (*Conn, error) {
	fd, local, peer, err := c.dialFd(network, address)
	if err != nil {
		return nil, err
	}
	target := c.loops.GetNextLoop(peer)
	return c.buildConn(target, fd, local, peer), nil
}

// DialOn 在指定 loop 上同步拨号（per-loop 亲和原语）：conn 落在调用方给的 loop，
// 用于"玩家 conn 在 L，后端 conn 也在 L"的 in-loop 零 marshal 转发。其余同 Dial。
func (c *Client) DialOn(loop *EventLoop, network, address string) (*Conn, error) {
	fd, local, peer, err := c.dialFd(network, address)
	if err != nil {
		return nil, err
	}
	return c.buildConn(loop, fd, local, peer), nil
}

// dialFd 同步建非阻塞 fd 并连上，返回 fd 与本地/对端地址（loop 选择前的共用步骤）。
func (c *Client) dialFd(network, address string) (fd int, local, peer Address, err error) {
	if !c.started.Load() {
		c.Start()
	}
	_, addr, perr := ParseAddress(network, address)
	if perr != nil {
		return -1, Address{}, Address{}, perr
	}
	fd, err = createConnector(addr, c.opts)
	if err != nil && err != unix.EINPROGRESS {
		return -1, Address{}, Address{}, err
	}
	if err == unix.EINPROGRESS {
		if werr := waitWritable(fd, c.opts.DialTimeout); werr != nil {
			_ = unix.Close(fd)
			return -1, Address{}, Address{}, werr
		}
	}
	if cerr := connectError(fd); cerr != nil {
		_ = unix.Close(fd)
		return -1, Address{}, Address{}, cerr
	}
	local, _ = sockName(fd)
	peer, _ = peerName(fd)
	return fd, local, peer, nil
}

// Conn 返回最近一次 Dial 的连接（单连接场景下业务只持 Client 一个句柄：cli.Conn().Send）。
// 多次 Dial 会覆盖；conn 的生命周期由 loop 退出 teardown 兜底关闭，无需业务关。
//
// 注意：返回的 Conn 跨线程（如拨号 goroutine）只能调 AsyncSend/AsyncSendString；
// Send/SendString 是 in-loop 快路径（gnet 式，无同线程检测），跨线程调用会与
// loop 的写路径竞态。仅在 OnMessage/OnConnect 等 loop 线程回调内可直接用 Send。
func (c *Client) Conn() *Conn { return c.conn }

// buildConn 把已连 fd 在 target loop 上建 Conn，注入 Client 回调并 connectEstablished。
func (c *Client) buildConn(target *EventLoop, fd int, local, peer Address) *Conn {
	name := fmt.Sprintf("c-%s-%d", peer.String(), c.nextID.Add(1))
	conn := NewConn(target, fd, name, local, peer)
	conn.SetConnectCallback(c.onConnect)
	conn.SetMessageCallback(c.onMessage)
	conn.SetWriteCompleteCallback(c.onWriteComplete)
	conn.SetCloseCallback(c.onClose)
	conn.setConnected() // fd 已连，预置 Connected 让调用方可立即 Send
	c.conn = conn // 记最近 dial 的 conn，供 Conn() 访问
	target.RunInLoop(func() {
		conn.channel.EnableReading()
		conn.loop.AddConn()
		if conn.onConnect != nil {
			conn.onConnect(conn)
		}
	})
	return conn
}

// NewConnector 构造异步拨号器（带重试/重连），由调用方在指定 loop 上驱动。
func (c *Client) NewConnector(network, address string, loop *EventLoop) (*Connector, error) {
	return NewConnector(loop, network, address, c.opts)
}
