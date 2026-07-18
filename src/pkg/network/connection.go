//go:build linux

package network

import (
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// ConnState 连接状态机。
type ConnState int32

const (
	StateDisconnected ConnState = iota
	StateConnecting
	StateConnected
	StateDisconnecting
)

// ConnCallback 连接级回调（建立/写完成/关闭）。
type ConnCallback func(c *Conn)

// MessageCallback 消息到达回调，in 为连接入站缓冲，用户 Peek/Next/Read 解析协议。
type MessageCallback func(c *Conn, in *Buffer)

// HighWaterCallback 出站累积越过高水位时触发，buffered 为当前出站排队字节数。
// 典型用途：对端慢消费时主动限速/丢弃，避免出站 buffer 堆爆。muduo highWaterMarkCallback 对偶。
type HighWaterCallback func(c *Conn, buffered int)

// Conn 连接级状态机，封装一个已连接 fd + input/output Buffer + Channel + 用户回调。
// TCP 与 stream UDS 通用。所有 I/O 与回调在所属 loop 线程串行执行；
// 跨线程发数据经 loop.RunInLoop 投递（同线程则直接 sendInLoop，零投递开销）。
type Conn struct {
	loop    *EventLoop
	channel *Channel
	fd      int
	name    string
	opts    Options
	logger  Logger
	local   Address
	peer    Address

	state  atomic.Int32
	input  *Buffer
	output *Buffer

	// per-conn 用户上下文：ctx 仅 loop 内用；safeCtx 跨线程用（atomic.Pointer）。
	ctx     any
	safeCtx atomic.Pointer[any]

	// 由 Server 设置的回调。
	onConnect       ConnCallback
	onMessage       MessageCallback
	onWriteComplete ConnCallback
	onClose         ConnCallback
	highWater       HighWaterCallback

	highWaterMark  int  // 高水位阈值（字节）；0 表禁用
	aboveHighWater bool // 当前是否已处于高水位（去抖，仅 below→above 触发一次）
	outbound       atomic.Int64 // 出站排队字节镜像（线程安全供 OutboundBuffered 跨线程读）

	etRead int // ET 模式累计读字节，达阈值防饿死
}

// NewConn 构造（尚未 connectEstablished）。fd 须为非阻塞已连接 fd。
func NewConn(loop *EventLoop, fd int, name string, local, peer Address) *Conn {
	o := loop.Opts()
	c := &Conn{
		loop:    loop,
		channel: NewChannel(fd, loop.Poller()),
		fd:      fd,
		name:    name,
		opts:    o,
		logger:  loop.Logger(),
		local:   local,
		peer:    peer,
		input:   NewBuffer(o.ReadBufferCap),
		output:  NewBuffer(o.WriteBufferCap),
	}
	c.state.Store(int32(StateDisconnected))
	c.channel.SetReadCallback(c.handleRead)
	c.channel.SetWriteCallback(c.handleWrite)
	c.channel.SetCloseCallback(c.handleClose)
	c.channel.SetErrorCallback(c.handleError)
	return c
}

// --- 基本访问 ---

func (c *Conn) Fd() int          { return c.fd }
func (c *Conn) Name() string     { return c.name }
func (c *Conn) LocalAddr() Address { return c.local }
func (c *Conn) PeerAddr() Address { return c.peer }
func (c *Conn) EventLoop() *EventLoop { return c.loop }
func (c *Conn) Logger() Logger       { return c.logger }
func (c *Conn) State() ConnState { return ConnState(c.state.Load()) }
func (c *Conn) IsConnected() bool { return c.State() == StateConnected }
func (c *Conn) IsDisconnected() bool { return c.State() == StateDisconnected }

// Context / SetContext 用户上下文（loop 内访问，非线程安全）。
func (c *Conn) Context() any     { return c.ctx }
func (c *Conn) SetContext(v any) { c.ctx = v }

// SafeContext / SetSafeContext 线程安全上下文（跨线程读写）。
func (c *Conn) SafeContext() any {
	p := c.safeCtx.Load()
	if p == nil {
		return nil
	}
	return *p
}
func (c *Conn) SetSafeContext(v any) { c.safeCtx.Store(&v) }

// --- 回调注入（Server 设置） ---

func (c *Conn) SetConnectCallback(cb ConnCallback)         { c.onConnect = cb }
func (c *Conn) SetMessageCallback(cb MessageCallback)       { c.onMessage = cb }
func (c *Conn) SetWriteCompleteCallback(cb ConnCallback)   { c.onWriteComplete = cb }
func (c *Conn) SetCloseCallback(cb ConnCallback)            { c.onClose = cb }

// SetHighWaterMark 设置出站高水位：当出站排队字节从阈值之下越至之上时触发 cb
// （仅 below→above 一次，排空回落后再堆积可再次触发）。mark=0 禁用。
// 在 loop 线程内发送（sendInLoop）时判定，零热路径额外开销（仅 EAGAIN 余量入队分支）。
func (c *Conn) SetHighWaterMark(mark int, cb HighWaterCallback) {
	c.highWaterMark = mark
	c.highWater = cb
}

// OutboundBuffered 当前出站排队字节数（未发出的 output buffer）。
// 经原子镜像，跨线程读安全；loop 线程内读与 output 同步更新，精确。
func (c *Conn) OutboundBuffered() int { return int(c.outbound.Load()) }

// --- 生命周期 ---

// connectEstablished 建立：置 Connected、注册可读、计数、触发 onConnect。须在 loop 线程调用。
func (c *Conn) connectEstablished() {
	c.state.Store(int32(StateConnected))
	c.channel.EnableReading()
	c.loop.AddConn()
	if c.onConnect != nil {
		c.onConnect(c)
	}
}

// setConnected 仅置 Connected 状态（fd 已连但尚未在 loop 注册可读），供客户端同步 Dial 返回前预置，
// 使调用方可立即 Send（写路径不依赖可读注册）。可读注册仍由后续 connectEstablished 完成。
func (c *Conn) setConnected() { c.state.Store(int32(StateConnected)) }

// teardown 销毁：幂等。置 Disconnected、移除 Channel、关闭 fd、计数减、触发 onClose。
func (c *Conn) teardown() {
	if !c.state.CompareAndSwap(int32(StateConnected), int32(StateDisconnected)) &&
		!c.state.CompareAndSwap(int32(StateDisconnecting), int32(StateDisconnected)) {
		return
	}
	c.channel.DisableAll()
	c.channel.Remove()
	_ = unix.Close(c.fd)
	c.loop.SubConn()
	if c.onClose != nil {
		c.onClose(c)
	}
}

// --- 发送（muduo sendInLoop 快路径） ---

// Send 直接发送（gnet 式，无同线程检测，零 syscall 开销）。
// 必须在 loop 线程调用（典型为 OnMessage 回调内）；跨线程请用 AsyncSend。
// 直接 unix.Write，EAGAIN 余量入 output 并 enableWriting。
func (c *Conn) Send(data []byte) {
	if c.state.Load() == int32(StateDisconnected) || len(data) == 0 {
		return
	}
	c.sendInLoop(data)
}

// AsyncSend 线程安全发送：跨线程投递到 loop 执行（类型化池化 task，零闭包分配）。
func (c *Conn) AsyncSend(data []byte) {
	if c.state.Load() == int32(StateDisconnected) || len(data) == 0 {
		return
	}
	c.loop.QueueSend(c, data)
}

// SendString 零拷贝发送字符串（in-loop）。
func (c *Conn) SendString(s string) {
	if len(s) == 0 {
		return
	}
	c.Send(stringToBytes(s))
}

// AsyncSendString 线程安全发送字符串。
func (c *Conn) AsyncSendString(s string) {
	if len(s) == 0 {
		return
	}
	c.AsyncSend(stringToBytes(s))
}

// sendInLoop 在 loop 线程执行写入：未注册 EPOLLOUT 时直接写，
// EAGAIN 余量入 output buffer 并 enableWriting；全发完触发 onWriteComplete。
func (c *Conn) sendInLoop(data []byte) {
	if c.state.Load() == int32(StateDisconnected) {
		return
	}
	total := len(data)
	nWrote := 0
	wroteAll := false

	if !c.channel.IsWriting() {
		n, err := unix.Write(c.fd, data)
		switch err {
		case nil:
			nWrote = n
			if nWrote >= total {
				wroteAll = true
			}
		case unix.EAGAIN: // Linux 上 EWOULDBLOCK 同值
			nWrote = 0
		default:
			c.logger.Errorf("conn send: %v", err)
			c.teardown()
			return
		}
	}

	if wroteAll {
		if c.onWriteComplete != nil {
			c.onWriteComplete(c)
		}
		return
	}

	// 余量入队，注册可写。
	c.output.Append(data[nWrote:])
	c.outbound.Add(int64(len(data) - nWrote))
	c.maybeHighWater()
	if !c.channel.IsWriting() {
		c.channel.EnableWriting()
	}
}

// maybeHighWater 出站排队越过阈值（且尚未标记）时触发一次高水位回调。loop 线程调用。
func (c *Conn) maybeHighWater() {
	if c.highWaterMark <= 0 || c.highWater == nil {
		return
	}
	n := c.output.ReadableBytes()
	if n >= c.highWaterMark && !c.aboveHighWater {
		c.aboveHighWater = true
		c.highWater(c, n)
	}
}

// Shutdown 优雅半关：排空 output 后 SHUT_WR，等对端关闭再彻底销毁。
func (c *Conn) Shutdown() {
	if c.state.CompareAndSwap(int32(StateConnected), int32(StateDisconnecting)) {
		c.loop.RunInLoop(c.shutdownInLoop)
	}
}

func (c *Conn) shutdownInLoop() {
	if c.state.Load() != int32(StateDisconnecting) {
		return
	}
	if c.output.ReadableBytes() == 0 {
		// 已排空，关闭写端；对端读到 EOF 后会回 FIN，触发 handleRead 的 n==0 → teardown。
		_ = unix.Shutdown(c.fd, unix.SHUT_WR)
	}
	// 否则等 output 排空：handleWrite 在排空时会再次调用 shutdownInLoop。
}

// ForceClose 强制关闭连接（不等排空）。
func (c *Conn) ForceClose() {
	if c.state.CompareAndSwap(int32(StateConnected), int32(StateDisconnecting)) ||
		c.state.CompareAndSwap(int32(StateConnecting), int32(StateDisconnecting)) {
		c.loop.RunInLoop(c.teardown)
	}
}

// Close 等价 ForceClose（语义清晰别名）。
func (c *Conn) Close() { c.ForceClose() }

// --- 事件处理（loop 线程） ---

// handleRead 可读就绪：读入 input buffer，触发 onMessage；n==0 视为对端关闭。
// LT 模式单次读取（可读则 epoll 再触发）；ET 模式循环至 EAGAIN，带阈值防饿死。
func (c *Conn) handleRead() {
	for {
		n, err := c.input.ReadFd(c.fd, c.loop.readExtra[:], c.loop.readIov[:])
		if n > 0 {
			if c.onMessage != nil {
				c.onMessage(c, c.input)
			}
			if !c.opts.EdgeTriggeredIO {
				return // LT：一次读取，可读则下次 epoll 再触发。
			}
			// ET：累计字节达阈值则延后继续，避免饿死同 loop 其他连接。
			c.etRead += n
			if c.etRead >= c.opts.EdgeTriggeredIOChunk {
				c.etRead = 0
				c.loop.QueueInLoop(c.handleRead)
				return
			}
			continue
		}
		if err != nil {
			switch err {
			case unix.EAGAIN: // Linux 上 EWOULDBLOCK 同值
				c.etRead = 0
				return // 读空（ET 排空）或 LT 无数据。
			default:
				c.logger.Errorf("conn read err: %v", err)
				c.teardown()
				return
			}
		}
		// n==0, err==nil → 对端 FIN。
		c.teardown()
		return
	}
}

// handleWrite 可写就绪：排空 output buffer，全排空则取消可写并触发 onWriteComplete。
func (c *Conn) handleWrite() {
	for {
		view := c.output.Peek()
		if len(view) == 0 {
			c.channel.DisableWriting()
			if c.state.Load() == int32(StateDisconnecting) {
				c.shutdownInLoop()
			}
			return
		}
		n, err := unix.Write(c.fd, view)
		if n > 0 {
			c.output.Retrieve(n)
			c.outbound.Add(-int64(n))
			// 排空回阈值之下则复位高水位标记，下次再堆积可再次触发。
			if c.aboveHighWater && c.output.ReadableBytes() < c.highWaterMark {
				c.aboveHighWater = false
			}
			if c.output.ReadableBytes() == 0 {
				c.channel.DisableWriting()
				if c.onWriteComplete != nil {
					c.onWriteComplete(c)
				}
				if c.state.Load() == int32(StateDisconnecting) {
					c.shutdownInLoop()
				}
				return
			}
			if !c.opts.EdgeTriggeredIO {
				return // LT：下次 epoll 再触发。
			}
			continue
		}
		if err != nil {
			switch err {
			case unix.EAGAIN: // Linux 上 EWOULDBLOCK 同值
				return // 仍注册 EPOLLOUT，下次再触发。
			default:
				c.logger.Errorf("conn write err: %v", err)
				c.teardown()
				return
			}
		}
		// n==0, err==nil：不应发生，避免死循环退出。
		return
	}
}

// handleError 错误就绪：记日志后按关闭处理。
func (c *Conn) handleError() {
	c.logger.Errorf("conn error: %s", c.name)
	c.teardown()
}

// handleClose 对端关闭/连接断开：销毁。
func (c *Conn) handleClose() {
	c.teardown()
}
