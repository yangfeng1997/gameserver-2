package app

type Option func(*options)

type options struct {
	name      string
	nodeID    string
	pprof     bool
	pprofAddr string
	postBuf   int
}

func WithName(name string) Option {
	return func(o *options) { o.name = name }
}

func WithNodeID(nodeID string) Option {
	return func(o *options) { o.nodeID = nodeID }
}

func WithPprof(enabled bool, addr string) Option {
	return func(o *options) {
		o.pprof = enabled
		o.pprofAddr = addr
	}
}

func WithPostBuffer(size int) Option {
	return func(o *options) { o.postBuf = size }
}
