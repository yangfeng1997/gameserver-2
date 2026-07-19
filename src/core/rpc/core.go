package rpc

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"project/src/core/errcode"
)

// Transport 底层传输层接口。
// SendFrame 发送一帧 RPC 请求/通知到目标。
type Transport interface {
	SendFrame(target Target, header Header, body []byte) error
}

type inflight struct {
	onResult func([]byte, errcode.ErrCode)
	deadline time.Time
	span     Span
}

var inflightPool = sync.Pool{
	New: func() any { return new(inflight) },
}

func acquireInflight(on func([]byte, errcode.ErrCode), span Span, deadline time.Time) *inflight {
	f := inflightPool.Get().(*inflight)
	f.onResult = on
	f.span = span
	f.deadline = deadline
	return f
}

func releaseInflight(f *inflight) {
	if f == nil {
		return
	}
	f.onResult = nil
	f.deadline = time.Time{}
	f.span = nil
	inflightPool.Put(f)
}

const coreShards = 16

type coreShard struct {
	mu      sync.Mutex
	pending map[uint64]*inflight
}

// Core RPC 引擎：管理 seq 分配、in-flight 登记、超时扫描。
type Core struct {
	transport    Transport
	poster       Poster
	timeout      time.Duration
	scanInterval time.Duration
	seq          atomic.Uint64
	shards       [coreShards]*coreShard
	stopCh       chan struct{}
	scanWG       sync.WaitGroup
	closeOnce    sync.Once
}

func (c *Core) shard(seq uint64) *coreShard { return c.shards[seq%coreShards] }

// Option Core 配置选项。
type Option func(*Core)

// WithPoster 设置回调投递器。
func WithPoster(p Poster) Option {
	return func(c *Core) { c.poster = p }
}

// WithDefaultTimeout 设置默认超时。
func WithDefaultTimeout(d time.Duration) Option {
	return func(c *Core) { c.timeout = d }
}

// WithScanInterval 设置超时扫描间隔。
func WithScanInterval(d time.Duration) Option {
	return func(c *Core) { c.scanInterval = d }
}

// New 创建 RPC 引擎。
func New(transport Transport, opts ...Option) *Core {
	c := &Core{
		transport:    transport,
		timeout:      3 * time.Second,
		scanInterval: 100 * time.Millisecond,
		stopCh:       make(chan struct{}),
	}
	for i := range c.shards {
		c.shards[i] = &coreShard{pending: make(map[uint64]*inflight)}
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.scanInterval <= 0 {
		c.scanInterval = 100 * time.Millisecond
	}
	c.scanWG.Add(1)
	go c.scanLoop()
	return c
}

// Call 发起请求并登记回调。
func (c *Core) Call(t Target, route string, body []byte, ctx Ctx, on func([]byte, errcode.ErrCode)) {
	if on == nil {
		return
	}
	seq := c.seq.Add(1)
	span := ctx.Span().Child(route)
	reqTimeout := ctx.Remaining()
	if reqTimeout <= 0 {
		reqTimeout = c.timeout
	}
	if t.Deadline > 0 && t.Deadline < reqTimeout {
		reqTimeout = t.Deadline
	}
	deadline := time.Time{}
	if reqTimeout > 0 {
		deadline = time.Now().Add(reqTimeout)
	}
	f := acquireInflight(on, span, deadline)
	s := c.shard(seq)
	s.mu.Lock()
	s.pending[seq] = f
	s.mu.Unlock()

	head := Header{
		SeqID:       seq,
		Route:       route,
		DeadlineMs:  int64(reqTimeout / time.Millisecond),
		SrcNodeID:   ctx.FromNodeID(),
		RoutingMode: t.Mode,
		RoutingKey:  t.Key,
		ServerType:  t.ServerType,
	}
	if err := c.transport.SendFrame(t, head, body); err != nil {
		s.mu.Lock()
		f := s.pending[seq]
		if f != nil {
			delete(s.pending, seq)
		}
		s.mu.Unlock()
		if f == nil {
			return
		}
		if f.span != nil {
			f.span.Finish()
		}
		code := errcode.CodeOf(err)
		if code == errcode.OK {
			code = errcode.ERR_INTERNAL
		}
		onResult := f.onResult
		releaseInflight(f)
		c.dispatch(func() { onResult(nil, code) })
	}
}

// Send 发起单向通知。
func (c *Core) Send(t Target, route string, body []byte, ctx Ctx) {
	head := Header{
		Route:       route,
		SrcNodeID:   ctx.FromNodeID(),
		RoutingMode: t.Mode,
		RoutingKey:  t.Key,
		ServerType:  t.ServerType,
	}
	_ = c.transport.SendFrame(t, head, body)
}

// OnResponse 处理回包。
func (c *Core) OnResponse(seq uint64, payload []byte, code errcode.ErrCode) {
	c.OnResponseWithRelease(seq, payload, code, nil)
}

// OnResponseWithRelease 处理回包并在回调完成后释放 payload 所属资源。
func (c *Core) OnResponseWithRelease(seq uint64, payload []byte, code errcode.ErrCode, release func()) {
	s := c.shard(seq)
	s.mu.Lock()
	f := s.pending[seq]
	if f != nil {
		delete(s.pending, seq)
	}
	s.mu.Unlock()
	if f == nil {
		if release != nil {
			release()
		}
		return
	}
	if f.span != nil {
		f.span.Finish()
	}
	onResult := f.onResult
	releaseInflight(f)
	c.dispatch(func() {
		if release != nil {
			defer release()
		}
		onResult(payload, code)
	})
}

func (c *Core) scanLoop() {
	defer c.scanWG.Done()
	ticker := time.NewTicker(c.scanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.scanExpired()
		}
	}
}

func (c *Core) scanExpired() {
	now := time.Now()
	var expired []*inflight
	for _, s := range c.shards {
		s.mu.Lock()
		for seq, f := range s.pending {
			if !f.deadline.IsZero() && now.After(f.deadline) {
				delete(s.pending, seq)
				expired = append(expired, f)
			}
		}
		s.mu.Unlock()
	}
	for _, f := range expired {
		if f.span != nil {
			f.span.Finish()
		}
		onResult := f.onResult
		releaseInflight(f)
		c.dispatch(func() { onResult(nil, errcode.ERR_TIMEOUT) })
	}
}

// Close 停止超时扫描 goroutine。
func (c *Core) Close() {
	c.closeOnce.Do(func() {
		close(c.stopCh)
		c.scanWG.Wait()
	})
}

func (c *Core) dispatch(fn func()) {
	if c.poster != nil {
		c.poster.Post(fn)
		return
	}
	fn()
}

// PendingLen 返回在途请求数。
func (c *Core) PendingLen() int {
	n := 0
	for _, s := range c.shards {
		s.mu.Lock()
		n += len(s.pending)
		s.mu.Unlock()
	}
	return n
}

// String 返回调试信息。
func (c *Core) String() string {
	return fmt.Sprintf("rpc.Core{pending=%d}", c.PendingLen())
}
