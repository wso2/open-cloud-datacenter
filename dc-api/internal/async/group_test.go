package async

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestGroup_WaitDrains proves Wait returns true only after every Go'd task
// has finished, and that the tasks actually ran.
func TestGroup_WaitDrains(t *testing.T) {
	g := &Group{}
	var ran atomic.Int32
	for i := 0; i < 5; i++ {
		g.Go(func() {
			time.Sleep(10 * time.Millisecond)
			ran.Add(1)
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !g.Wait(ctx) {
		t.Fatal("Wait returned false, expected a full drain well within the deadline")
	}
	if got := ran.Load(); got != 5 {
		t.Fatalf("expected 5 tasks to have run, got %d", got)
	}
}

// TestGroup_WaitTimesOut proves Wait returns false when a task outlives the
// context deadline.
func TestGroup_WaitTimesOut(t *testing.T) {
	g := &Group{}
	release := make(chan struct{})
	g.Go(func() { <-release })
	defer close(release) // let the goroutine exit after the test

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if g.Wait(ctx) {
		t.Fatal("Wait returned true, expected false: the task outlives the context")
	}
}

// TestGroup_NilReceiver proves the nil-Group fallbacks: Go still runs the
// task (plain detached goroutine, no panic) and Wait reports drained.
func TestGroup_NilReceiver(t *testing.T) {
	var g *Group
	done := make(chan struct{})
	g.Go(func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("nil-Group Go never ran the task")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !g.Wait(ctx) {
		t.Fatal("nil-Group Wait must report drained")
	}
}
