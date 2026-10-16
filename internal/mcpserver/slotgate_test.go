package mcpserver

import (
	"context"
	"errors"
	"testing"
	"time"
)

// waitQueued waits until n calls wait on g.
func waitQueued(t *testing.T, g *slotGate, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		g.mu.Lock()
		got := len(g.waiters)
		g.mu.Unlock()
		if got == n {
			return
		}
	}
	t.Fatalf("%d calls never queued", n)
}

// TestSlotGateServesWaitersInArrivalOrder: the slot goes to the call that
// waited longest.
func TestSlotGateServesWaitersInArrivalOrder(t *testing.T) {
	g := newSlotGate(1, 3)
	if err := g.acquire(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	order := make(chan int, 3)
	for i := range 3 {
		go func() {
			if err := g.acquire(t.Context(), nil); err != nil {
				t.Error(err)
			}
			order <- i
			g.release()
		}()
		waitQueued(t, g, i+1)
	}
	if err := g.acquire(t.Context(), nil); !errors.Is(err, errGateFull) {
		t.Fatalf("beyond the queue: %v, want errGateFull", err)
	}
	g.release()
	for want := range 3 {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("call %d got the slot, want call %d", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("call %d never got the slot", want)
		}
	}
}

// TestSlotGateCancelledWaiterLeavesTheQueue: a waiter whose context ends
// leaves the queue with the context's error and the slot is not lost.
func TestSlotGateCancelledWaiterLeavesTheQueue(t *testing.T) {
	g := newSlotGate(1, 1)
	if err := g.acquire(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- g.acquire(ctx, nil) }()
	waitQueued(t, g, 1)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	waitQueued(t, g, 0)
	g.release()
	if err := g.acquire(t.Context(), nil); err != nil {
		t.Fatalf("the slot was lost: %v", err)
	}
}
