package app

import (
	"testing"
	"time"
)

func TestPosterQueue(t *testing.T) {
	q := NewPosterQueue(2)
	q.Start()

	var got int
	q.Post(func() { got++ })
	q.Post(func() { got++ })

	for i := 0; i < 100; i++ {
		if got == 2 {
			break
		}
		select {
		case <-time.After(5 * time.Millisecond):
		}
	}
	q.Stop()
	if got != 2 {
		t.Fatalf("got = %d after stop, want 2", got)
	}
}
