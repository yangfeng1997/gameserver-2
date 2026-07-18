//go:build linux

package network

import (
	"hash/fnv"
	"runtime"
	"sync"
	"sync/atomic"
)

// EventLoopGroup sub-reactor 线程池：一组各跑独立 goroutine 的 EventLoop，
// 按 LB 策略把新连接分发到某一 loop。主 loop（跑 Acceptor）由上层单独持有。
type EventLoopGroup struct {
	baseLoop *EventLoop // 主 loop，分发用；可为 nil（ReusePort 模式）
	loops    []*EventLoop
	next     atomic.Uint64 // RR 计数
	strategy Balancing
	opts     Options
	logger   Logger
	started  atomic.Bool
	wg       sync.WaitGroup
}

// NewEventLoopGroup 构造。baseLoop 为跑 Acceptor 的主 loop。
func NewEventLoopGroup(base *EventLoop, o Options) *EventLoopGroup {
	return &EventLoopGroup{
		baseLoop: base,
		strategy: o.LB,
		opts:     o,
		logger:   o.Logger,
	}
}

// determineNumLoops 解析 sub loop 数量。
func (g *EventLoopGroup) determineNumLoops() int {
	n := g.opts.NumEventLoop
	if n > 0 {
		return min(n, 256)
	}
	if g.opts.Multicore {
		return min(runtime.NumCPU(), 256)
	}
	return 1
}

// Start 创建并启动所有 sub loop。返回后各 loop 已进入 epoll_wait。
func (g *EventLoopGroup) Start() {
	if !g.started.CompareAndSwap(false, true) {
		return
	}
	n := g.determineNumLoops()
	g.loops = make([]*EventLoop, 0, n)
	for range n {
		loop, err := NewEventLoop(g.opts)
		if err != nil {
			g.logger.Fatalf("eventloopgroup: new loop: %v", err)
		}
		g.loops = append(g.loops, loop)
		g.wg.Add(1)
		go func(l *EventLoop) {
			defer g.wg.Done()
			l.Loop()
		}(loop)
	}
}

// GetNextLoop 按策略选下一个 sub loop。remoteAddr 用于哈希亲和。
// 无 sub loop 时回退 baseLoop。
func (g *EventLoopGroup) GetNextLoop(remoteAddr Address) *EventLoop {
	if len(g.loops) == 0 {
		return g.baseLoop
	}
	switch g.strategy {
	case LeastConnections:
		return g.leastConnections()
	case SourceAddrHash:
		return g.sourceAddrHash(remoteAddr)
	default: // RoundRobin
		idx := g.next.Add(1)
		return g.loops[int(idx%uint64(len(g.loops)))]
	}
}

// leastConnections 选当前连接数最少的 loop。
func (g *EventLoopGroup) leastConnections() *EventLoop {
	best := g.loops[0]
	min := best.ConnCount()
	for _, l := range g.loops[1:] {
		if c := l.ConnCount(); c < min {
			min = c
			best = l
		}
	}
	return best
}

// sourceAddrHash 按对端地址哈希选 loop，保证同源亲和。
func (g *EventLoopGroup) sourceAddrHash(addr Address) *EventLoop {
	h := fnv.New32a()
	switch addr.fam {
	case famIPv4:
		_, _ = h.Write(addr.ip4[:])
	case famIPv6:
		_, _ = h.Write(addr.ip6[:])
	case famUnix:
		_, _ = h.Write([]byte(addr.path))
	}
	return g.loops[int(h.Sum32()%uint32(len(g.loops)))]
}

// Loops 返回所有 sub loop 切片。
func (g *EventLoopGroup) Loops() []*EventLoop { return g.loops }

// Len sub loop 数量。
func (g *EventLoopGroup) Len() int { return len(g.loops) }

// BaseLoop 返回主 loop。
func (g *EventLoopGroup) BaseLoop() *EventLoop { return g.baseLoop }

// Stop 停止所有 sub loop 并等待退出。
func (g *EventLoopGroup) Stop() {
	for _, l := range g.loops {
		l.Quit()
	}
	g.wg.Wait()
	g.started.Store(false)
}

// Iterate 遍历所有 sub loop。
func (g *EventLoopGroup) Iterate(fn func(*EventLoop)) {
	for _, l := range g.loops {
		fn(l)
	}
}
