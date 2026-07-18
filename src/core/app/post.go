package app

import (
	"fmt"
	"sync"
	"sync/atomic"
)

const postQueueBuffer = 1024

// PosterQueue serializes functions on a single goroutine.
type PosterQueue struct {
	ch      chan func()
	closed  atomic.Bool
	started atomic.Bool
	wg      sync.WaitGroup
}

// NewPosterQueue creates a serializer queue.
func NewPosterQueue(buffer int) *PosterQueue {
	if buffer <= 0 {
		buffer = postQueueBuffer
	}
	return &PosterQueue{ch: make(chan func(), buffer)}
}

// Start runs the draining goroutine once.
func (p *PosterQueue) Start() {
	if !p.started.CompareAndSwap(false, true) {
		return
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for fn := range p.ch {
			if fn != nil {
				fn()
			}
		}
	}()
}

// Post schedules a function for serialized execution.
func (p *PosterQueue) Post(fn func()) {
	if fn == nil || p.closed.Load() {
		return
	}
	defer func() {
		_ = recover()
	}()
	p.ch <- fn
}

// Stop closes the queue and waits for the drain goroutine to exit.
func (p *PosterQueue) Stop() {
	if p.closed.CompareAndSwap(false, true) {
		close(p.ch)
		p.wg.Wait()
	}
}

func (p *PosterQueue) String() string {
	return fmt.Sprintf("PosterQueue(len=%d)", len(p.ch))
}
