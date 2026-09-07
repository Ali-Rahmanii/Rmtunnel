package main

import (
	"context"
	"sync"
	"time"
)

// notifier is a broadcast wakeup: any number of goroutines can wait() for the
// next broadcast() without missing it or racing each other, using the
// standard "close a channel, replace it" idiom instead of a sync.Cond (which
// does not compose with select/context cancellation).
type notifier struct {
	mu sync.Mutex
	ch chan struct{}
}

func newNotifier() *notifier { return &notifier{ch: make(chan struct{})} }

func (n *notifier) wait() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ch
}

func (n *notifier) broadcast() {
	n.mu.Lock()
	defer n.mu.Unlock()
	close(n.ch)
	n.ch = make(chan struct{})
}

// backoff grows delays geometrically between min and max, and resets after a
// connection that stayed up for a while — a brief blip and a long outage are
// both common, and this keeps neither drumming a peer nor stalling recovery.
type backoff struct {
	min, max time.Duration
	cur      time.Duration
}

func newBackoff(min, max time.Duration) *backoff {
	if min <= 0 {
		min = time.Second
	}
	if max < min {
		max = min
	}
	return &backoff{min: min, max: max, cur: min}
}

func (b *backoff) wait(ctx context.Context) {
	t := time.NewTimer(b.cur)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
	b.cur *= 2
	if b.cur > b.max {
		b.cur = b.max
	}
}

func (b *backoff) reset() { b.cur = b.min }
