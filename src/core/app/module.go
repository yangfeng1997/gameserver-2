package app

import "context"

// Module is the lifecycle unit managed by App.
type Module interface {
	Name() string
	Set(app *App)
	Init() error
	AfterInit() error
	BeforeShutdown()
	Shutdown()
	DependsOn() []string
}

// DefaultModule provides empty lifecycle hooks for embedding.
type DefaultModule struct {
	app *App
}

func (m *DefaultModule) Name() string        { return "" }
func (m *DefaultModule) Set(app *App)        { m.app = app }
func (m *DefaultModule) Init() error         { return nil }
func (m *DefaultModule) AfterInit() error    { return nil }
func (m *DefaultModule) BeforeShutdown()      {}
func (m *DefaultModule) Shutdown()           {}
func (m *DefaultModule) DependsOn() []string { return nil }
func (m *DefaultModule) App() *App           { return m.app }

// Poster represents a serializer that executes functions on a dedicated goroutine.
type Poster interface {
	Post(fn func())
}

// ReadyWaiter waits until asynchronous initialization completes.
type ReadyWaiter interface {
	WaitReady(ctx context.Context) error
}

// MetricsProvider exposes counters for the optional /metrics endpoint.
type MetricsProvider interface {
	MetricsSnapshot() map[string]int64
}
