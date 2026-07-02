// Package async provides Group, a tiny goroutine tracker for the
// fire-and-forget provisioning goroutines the HTTP handlers launch.
//
// Why it exists: handlers return 202 and provision in a detached goroutine
// (deliberately on context.Background() so an HTTP disconnect doesn't cancel
// provisioning). Without tracking, main.go exits right after srv.Shutdown and
// a rolling deploy kills those goroutines mid-flight — stranding PENDING rows
// with no backend_uid (which only the reconciler's orphan sweep can recover).
// Group lets main.go drain them, bounded, before exiting.
package async

import (
	"context"
	"sync"
)

// Group tracks detached goroutines so shutdown can wait (bounded) for them.
//
// The zero value is ready to use. A nil *Group is also valid: Go falls back
// to a plain detached goroutine and Wait reports drained immediately, so
// tests and callers that don't inject one keep working unchanged.
//
// Unlike a raw sync.WaitGroup, Go may be called concurrently with Wait in any
// interleaving: if srv.Shutdown times out with requests still in flight, a
// handler can legitimately call Go while main.go is already draining, which
// would be a WaitGroup misuse panic. Group uses a counter + broadcast channel
// instead, and Wait simply re-checks until the count reaches zero or ctx
// expires.
type Group struct {
	mu    sync.Mutex
	count int
	idle  chan struct{} // closed when count drops to 0; nil until a waiter needs it
}

// Go runs fn in a new goroutine tracked by the group. On a nil receiver it
// degrades to a plain `go fn()`.
func (g *Group) Go(fn func()) {
	if g == nil {
		go fn()
		return
	}
	g.mu.Lock()
	g.count++
	g.mu.Unlock()
	go func() {
		defer func() {
			g.mu.Lock()
			g.count--
			if g.count == 0 && g.idle != nil {
				close(g.idle)
				g.idle = nil
			}
			g.mu.Unlock()
		}()
		fn()
	}()
}

// Wait blocks until every tracked goroutine has finished, or ctx expires.
// Returns true when fully drained, false when ctx expired first (some tasks
// were abandoned). Safe on a nil receiver (trivially drained). A task started
// while Wait is blocked extends the drain — the caller's ctx bounds it.
func (g *Group) Wait(ctx context.Context) bool {
	if g == nil {
		return true
	}
	for {
		g.mu.Lock()
		if g.count == 0 {
			g.mu.Unlock()
			return true
		}
		if g.idle == nil {
			g.idle = make(chan struct{})
		}
		idle := g.idle
		g.mu.Unlock()

		select {
		case <-idle:
			// Count hit zero, but a new task may have started since — re-check.
		case <-ctx.Done():
			return false
		}
	}
}
