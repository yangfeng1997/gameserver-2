package network

import (
	"time"
)

// Balancing 连接分发策略。
type Balancing int

const (
	// RoundRobin 轮询（默认）。原子计数取模，均匀且 O(1)。
	RoundRobin Balancing = iota
	// LeastConnections 最少连接数。遍历选最小，适合长连接不均场景，O(n)。
	LeastConnections
	// SourceAddrHash 源地址哈希。同源固定到同一 loop，便于会话亲和。
	SourceAddrHash
)

// TCPSocketOpt Nagle 算法开关。
type TCPSocketOpt int

const (
	// TCPNoDelay 关闭 Nagle，小包低延迟（默认）。
	TCPNoDelay TCPSocketOpt = iota
	// TCPDelay 开启 Nagle，合并小包提吞吐。
	TCPDelay
)

// Options 服务器与客户端共享的运行期配置。零值即可用（单 loop、轮询、TCPNoDelay）。
type Options struct {
	// LB 连接分发策略，默认 RoundRobin。
	LB Balancing
	// ReuseAddr 设置 SO_REUSEADDR，默认 false。
	ReuseAddr bool
	// ReusePort 设置 SO_REUSEPORT，每 loop 各自 bind 同端口由内核分发 accept，默认 false。
	ReusePort bool
	// Multicore 为 true 且 NumEventLoop<=0 时，loop 数取 runtime.NumCPU()，默认 false。
	Multicore bool
	// NumEventLoop 显式指定 loop 数；<=0 时由 Multicore 决定；上限 256。
	NumEventLoop int
	// ReadBufferCap 每 loop 复用读缓冲容量，默认 64KiB，向上取 2 的幂，最小 1KiB。
	ReadBufferCap int
	// WriteBufferCap 连接出站 buffer 初始容量，默认 64KiB，向上取 2 的幂，最小 1KiB。
	WriteBufferCap int
	// LockOSThread 每个 loop goroutine 绑定独立 OS 线程，阻塞 epoll_wait 不影响 runtime，默认 true。
	LockOSThread bool
	// TCPKeepAlive 开启 TCP keepalive，>0 启用并设空闲时长，0 关闭，默认 0。
	TCPKeepAlive time.Duration
	// TCPKeepInterval keepalive 探测间隔，默认 TCPKeepAlive/5，下限 1s。
	TCPKeepInterval time.Duration
	// TCPKeepCount 探测次数，默认 5。
	TCPKeepCount int
	// TCPNoDelay Nagle 开关，默认 TCPNoDelay（关闭 Nagle）。
	TCPNoDelay TCPSocketOpt
	// SocketRecvBuffer 内核 SO_RCVBUF，<=0 不设置。
	SocketRecvBuffer int
	// SocketSendBuffer 内核 SO_SNDBUF，<=0 不设置。
	SocketSendBuffer int
	// EdgeTriggeredIO 启用边沿触发，读写循环到 EAGAIN，默认 false（水平触发）。
	EdgeTriggeredIO bool
	// EdgeTriggeredIOChunk ET 模式单轮最大读写字节，<=0 自动启用 ET 并取 1MiB。
	EdgeTriggeredIOChunk int
	// TimerTick 时间轮 tick 周期，默认 100ms。
	TimerTick time.Duration
	// TimerSlots 时间轮槽数，默认 256，向上取 2 的幂。
	TimerSlots int
	// DialTimeout 客户端同步拨号等待时长，<=0 取 5s。
	DialTimeout time.Duration
	// Logger 日志实现，nil 使用包级默认（stderr + Info 级别）。
	Logger Logger
}

// defaultOptions 返回带合理默认值的 Options 副本。未设置字段补默认。
func defaultOptions() Options {
	return Options{
		LB:                   RoundRobin,
		NumEventLoop:         0,
		ReadBufferCap:        64 << 10,
		WriteBufferCap:       64 << 10,
		LockOSThread:         true,
		TCPNoDelay:           TCPNoDelay,
		TCPKeepCount:         5,
		EdgeTriggeredIOChunk: 1 << 20,
		TimerTick:            100 * time.Millisecond,
		TimerSlots:           256,
		DialTimeout:          5 * time.Second,
		Logger:               defaultLogger,
	}
}

// Option 函数式选项。
type Option func(*Options)

// Apply 将可变参数选项合并到默认值上。导出供注入式构造
// （NewServerWithLoops / NewClientWithLoops 的调用方手建 EventLoopGroup 时）复用同一份默认值。
func Apply(opts []Option) Options {
	o := defaultOptions()
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	// 若用户注入 nil Logger，回退默认。
	if o.Logger == nil {
		o.Logger = defaultLogger
	}
	// ReadBufferCap/WriteBufferCap 向上取 2 的幂，最小 1KiB。
	o.ReadBufferCap = ceilPow2Min(o.ReadBufferCap, 1<<10)
	o.WriteBufferCap = ceilPow2Min(o.WriteBufferCap, 1<<10)
	// TimerSlots 向上取 2 的幂，最小 8。
	o.TimerSlots = ceilPow2Min(o.TimerSlots, 8)
	// EdgeTriggeredIOChunk>0 视为启用 ET。
	if o.EdgeTriggeredIOChunk > 0 {
		o.EdgeTriggeredIO = true
	}
	// TCPKeepInterval 兜底。
	if o.TCPKeepAlive > 0 && o.TCPKeepInterval <= 0 {
		o.TCPKeepInterval = max(o.TCPKeepAlive/5, time.Second)
	}
	return o
}

// ceilPow2Min 返回 max(v, minimum) 后向上取整到 2 的幂。
func ceilPow2Min(v, minimum int) int {
	if v <= minimum {
		return minimum
	}
	// 向上取 2 的幂。
	v--
	v |= v >> 1
	v |= v >> 2
	v |= v >> 4
	v |= v >> 8
	v |= v >> 16
	v |= v >> 32
	v++
	return v
}

// --- WithXxx 选项构造器 ---

// WithBalancing 设置连接分发策略。
func WithBalancing(lb Balancing) Option { return func(o *Options) { o.LB = lb } }

// WithReuseAddr 启用 SO_REUSEADDR。
func WithReuseAddr(b bool) Option { return func(o *Options) { o.ReuseAddr = b } }

// WithReusePort 启用 SO_REUSEPORT。
func WithReusePort(b bool) Option { return func(o *Options) { o.ReusePort = b } }

// WithMulticore loop 数取 runtime.NumCPU()。
func WithMulticore(b bool) Option { return func(o *Options) { o.Multicore = b } }

// WithNumEventLoop 显式指定 loop 数。
func WithNumEventLoop(n int) Option { return func(o *Options) { o.NumEventLoop = n } }

// WithReadBufferCap 设置每 loop 读缓冲容量。
func WithReadBufferCap(n int) Option { return func(o *Options) { o.ReadBufferCap = n } }

// WithWriteBufferCap 设置连接出站初始容量。
func WithWriteBufferCap(n int) Option { return func(o *Options) { o.WriteBufferCap = n } }

// WithLockOSThread 设置 loop goroutine 是否绑定 OS 线程。
func WithLockOSThread(b bool) Option { return func(o *Options) { o.LockOSThread = b } }

// WithTCPKeepAlive 启用 TCP keepalive 并设空闲时长。
func WithTCPKeepAlive(d time.Duration) Option { return func(o *Options) { o.TCPKeepAlive = d } }

// WithTCPKeepInterval 设置 keepalive 探测间隔。
func WithTCPKeepInterval(d time.Duration) Option { return func(o *Options) { o.TCPKeepInterval = d } }

// WithTCPKeepCount 设置 keepalive 探测次数。
func WithTCPKeepCount(n int) Option { return func(o *Options) { o.TCPKeepCount = n } }

// WithTCPNoDelay 设置 Nagle 开关；true=关 Nagle（低延迟），false=开 Nagle。
func WithTCPNoDelay(b bool) Option {
	opt := TCPNoDelay
	if !b {
		opt = TCPDelay
	}
	return func(o *Options) { o.TCPNoDelay = opt }
}

// WithSocketRecvBuffer 设置内核 SO_RCVBUF。
func WithSocketRecvBuffer(n int) Option { return func(o *Options) { o.SocketRecvBuffer = n } }

// WithSocketSendBuffer 设置内核 SO_SNDBUF。
func WithSocketSendBuffer(n int) Option { return func(o *Options) { o.SocketSendBuffer = n } }

// WithEdgeTriggeredIO 启用/关闭边沿触发。
func WithEdgeTriggeredIO(b bool) Option { return func(o *Options) { o.EdgeTriggeredIO = b } }

// WithEdgeTriggeredIOChunk 设置 ET 单轮最大读写字节（>0 自动启用 ET）。
func WithEdgeTriggeredIOChunk(n int) Option {
	return func(o *Options) { o.EdgeTriggeredIOChunk = n }
}

// WithTimerTick 设置时间轮 tick 周期。
func WithTimerTick(d time.Duration) Option { return func(o *Options) { o.TimerTick = d } }

// WithTimerSlots 设置时间轮槽数。
func WithTimerSlots(n int) Option { return func(o *Options) { o.TimerSlots = n } }

// WithLogger 注入自定义日志实现。
func WithLogger(l Logger) Option { return func(o *Options) { o.Logger = l } }

// WithDialTimeout 设置客户端同步拨号等待时长。
func WithDialTimeout(d time.Duration) Option { return func(o *Options) { o.DialTimeout = d } }
