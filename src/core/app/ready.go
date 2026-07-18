package app

import (
	"context"
	"sync"
)

// Ready is a one-shot readiness primitive.
type Ready struct {
	once sync.Once
	ch   chan struct{}
	err  error
}

// NewReady creates a readiness primitive.
func NewReady() *Ready {
	return &Ready{ch: make(chan struct{})}
}

// Done marks the module ready.
func (r *Ready) Done() {
	r.once.Do(func() { close(r.ch) })
}

// Fail marks the module ready with an error.
func (r *Ready) Fail(err error) {
	r.once.Do(func() {
		r.err = err
		close(r.ch)
	})
}

// WaitReady waits until Done, Fail, or context cancellation.
func (r *Ready) WaitReady(ctx context.Context) error {
	select {
	case <-r.ch:
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
