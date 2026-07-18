//go:build linux

package network

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Server 高层封装：mainLoop 跑 Acceptor，EventLoopGroup 分发新连接到 subLoop，
// 维护连接表。支持多地址绑定（TCP/Unix）与 SO_REUSEPORT 模式。
type Server struct {
	opts      Options
	logger    Logger
	base      *EventLoop
	loops     *EventLoopGroup
	ownsLoops bool // 是否自建 loop 池+base（注入的归所有者管，Run 不启 Stop 不停）
	acceptors []*Acceptor
	specs     []listenSpec
	conns     sync.Map // string → *Conn
	nextID    atomic.Uint64
	started   atomic.Bool
	stopping  bool // 关停中：拒绝新 acceptor 创建与新监听启动

	onConnect       ConnCallback
	onMessage       MessageCallback
	onWriteComplete ConnCallback
	onClose         ConnCallback

	mu sync.Mutex // 保护 acceptors 切片
}

type listenSpec struct {
	network string
	address string
}

// NewServer 构造服务器，应用函数式选项。Run 前需 AddAddress 至少一个监听地址。
func NewServer(opts ...Option) (*Server, error) {
	o := Apply(opts)
	base, err := NewEventLoop(o)
	if err != nil {
		return nil, err
	}
	return &Server{
		opts:      o,
		logger:    o.Logger,
		base:      base,
		loops:     NewEventLoopGroup(base, o),
		ownsLoops: true,
	}, nil
}

// NewServerWithLoops 构造复用已有 EventLoopGroup 的服务器（与 Client.NewClientWithLoops 对偶），
// 使玩家 conn 与后端 conn（经 NewClientWithLoops 注入同一 group）落在同一套 loop 上，
// 配合 loop.Dial 可实现 per-loop in-loop 零 marshal 转发。
//
// base（跑 Acceptor 的主 loop）取 loops.BaseLoop()；normal 模式（非 ReusePort）须非 nil，
// 否则返回错误。ReusePort 模式 acceptor 跑在各 sub loop 上，base 可为 nil。
//
// 被注入的 loop 池与 base 生命周期由所有者管理：Run 不启动它们，Stop 不停止它们。
// 因此注入模式下 Run 创建完 acceptor 即返回（不阻塞），Stop 只关本 Server 创建的 acceptor
// 与连接 fd，不停 loop——loop 由所有者统一停。
func NewServerWithLoops(loops *EventLoopGroup, opts ...Option) (*Server, error) {
	o := Apply(opts)
	base := loops.BaseLoop()
	if base == nil && !o.ReusePort {
		return nil, fmt.Errorf("network: NewServerWithLoops normal 模式需 loops.BaseLoop() 非空")
	}
	return &Server{
		opts:      o,
		logger:    o.Logger,
		base:      base,
		loops:     loops,
		ownsLoops: false,
	}, nil
}

// Loops 暴露 loop 池（供 NewClientWithLoops 注入，使后端 conn 与玩家 conn 共用 loop）。
func (s *Server) Loops() *EventLoopGroup { return s.loops }

// AddAddress 追加一个监听地址。network: tcp/tcp4/tcp6/unix。
func (s *Server) AddAddress(network, address string) error {
	if network != "tcp" && network != "tcp4" && network != "tcp6" && network != "unix" {
		return fmt.Errorf("%w: %s", ErrInvalidNetwork, network)
	}
	if _, _, err := ParseAddress(network, address); err != nil {
		return err
	}
	s.specs = append(s.specs, listenSpec{network, address})
	return nil
}

// SetOnConnect / SetOnMessage / SetOnWriteComplete / SetOnClose 设置用户回调。
func (s *Server) SetOnConnect(cb ConnCallback)       { s.onConnect = cb }
func (s *Server) SetOnMessage(cb MessageCallback)     { s.onMessage = cb }
func (s *Server) SetOnWriteComplete(cb ConnCallback)  { s.onWriteComplete = cb }
func (s *Server) SetOnClose(cb ConnCallback)          { s.onClose = cb }

// CountConnections 当前活跃连接数（遍历估算，非精确瞬时）。
func (s *Server) CountConnections() int {
	n := 0
	s.conns.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

// Run 启动 sub loop、创建 acceptor，并在本 goroutine 阻塞跑 mainLoop 直至 Stop。
// 注入模式（!ownsLoops）不启 loop、不阻塞：创建完 acceptor 即返回，loop 由所有者在跑。
func (s *Server) Run() {
	if !s.started.CompareAndSwap(false, true) {
		return
	}
	if s.ownsLoops {
		s.loops.Start()
	}
	for _, sp := range s.specs {
		if s.opts.ReusePort && len(s.loops.Loops()) > 0 {
			// 每 subLoop 各自 bind 同端口，内核分发 accept。
			for _, l := range s.loops.Loops() {
				s.createAcceptor(l, sp)
			}
		} else {
			// 单 acceptor 跑在 mainLoop，分发到 subLoop。
			s.createAcceptor(s.base, sp)
		}
	}
	if s.ownsLoops {
		// 自有 base loop：阻塞本 goroutine 至 Stop。
		s.base.Loop()
	}
}

// createAcceptor 在指定 loop 上创建监听并启用 accept（投递到该 loop，epoll_ctl 在属主线程）。
func (s *Server) createAcceptor(loop *EventLoop, sp listenSpec) {
	loop.RunInLoop(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.stopping {
			return
		}
		acc, err := NewAcceptor(loop, sp.network, sp.address, s.opts)
		if err != nil {
			s.logger.Fatalf("server: create acceptor %s://%s: %v", sp.network, sp.address, err)
		}
		acc.SetNewConnectionCallback(func(fd int, local, peer Address) {
			s.newConnection(fd, local, peer)
		})
		acc.Listen()
		s.acceptors = append(s.acceptors, acc)
	})
}

// newConnection 在 acceptor 所在 loop 触发，选目标 subLoop 建 Conn（投递到目标 loop）。
func (s *Server) newConnection(fd int, local, peer Address) {
	target := s.loops.GetNextLoop(peer)
	name := fmt.Sprintf("s-%s-%d", peer.String(), s.nextID.Add(1))
	target.RunInLoop(func() {
		conn := NewConn(target, fd, name, local, peer)
		conn.SetConnectCallback(s.onConnect)
		conn.SetMessageCallback(s.onMessage)
		conn.SetWriteCompleteCallback(s.onWriteComplete)
		conn.SetCloseCallback(s.removeConnection)
		s.conns.Store(name, conn)
		conn.connectEstablished()
	})
}

// removeConnection 连接关闭时从表中移除并触发用户 onClose。
func (s *Server) removeConnection(c *Conn) {
	s.conns.Delete(c.Name())
	if s.onClose != nil {
		s.onClose(c)
	}
}

// StopAccepting 停止所有监听（关闭 listen fd、UDS 删文件），不再接受新连接，
// 但**不动现有连接**——它们继续在各自 subLoop 上收发，直至对端关闭或显式 Close。
//
// 优雅停机范式（业务编排，库只提供原语）：
//
//	srv.StopAccepting()                       // 1. 停入口，留现有 conn
//	sessionMgr.Each(func(s){ s.Conn.AsyncSend(kickPacket) }) // 2. 可选：通知客户端
//	// 3. 等排空或超时（OnClose→SessionMgr.Remove→计数；或 srv.CountConnections()==0）
//	for sessionMgr.Count() > 0 && !timeExpired { time.Sleep(50*time.Millisecond) }
//	srv.Stop()                               // 4. ForceClose 残留 + quit loops
//
// drain 判据（在飞 RPC/登录中/对局中）与 kick 包内容属业务/协议范畴，库不可见，
// 故 graceful 全流程在业务层；此方法只暴露"停 accept 不杀 conn"这一传输原语。
// 返回时所有已登记 acceptor 都已在各自所属 loop 上同步停止。
// 幂等，可在 Run 后任意时刻调用。
func (s *Server) StopAccepting() {
	s.mu.Lock()
	s.stopping = true
	accs := s.acceptors
	s.acceptors = nil
	s.mu.Unlock()
	// 在各 acceptor 所属 loop 上同步停止监听并关闭 fd（epoll_ctl 在属主线程）。
	for _, acc := range accs {
		acc.loop.runInLoopSync(acc.Stop)
	}
}

// Stop 停止 acceptor、强制关闭所有连接、退出 mainLoop 与 subLoop。
// 硬停（无 drain、无超时）：先停 accept、再 ForceClose 全部 conn、最后退出 loop。
// 优雅停机请先 StopAccepting + 业务排空后再 Stop；此方法幂等。
func (s *Server) Stop() {
	if !s.started.CompareAndSwap(true, false) {
		return
	}
	s.StopAccepting()
	// 强制关闭所有连接。
	s.conns.Range(func(_, v any) bool {
		v.(*Conn).ForceClose()
		return true
	})
	if s.ownsLoops {
		s.base.Quit()
		s.loops.Stop()
	}
	// 注入模式：loop 归所有者停；本 Server 仅关 acceptor fd 与连接 fd。
}
