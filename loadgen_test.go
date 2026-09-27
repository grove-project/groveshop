package groveshop

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// simulate feeds the controller a closed-loop system with the given capacity:
// throughput saturates at capacity and latency grows once concurrency exceeds it.
// It returns the mean level over the final 30 ticks, since holding at the knee
// is a small sawtooth of upward probes and backoffs.
func simulate(c *controller, capacity float64, ticks int) float64 {
	sum := 0
	for i := range ticks {
		level := float64(c.level)
		thr := min(level, capacity) * 10
		p50 := 10 * max(1, level/capacity)
		c.next(observation{throughput: thr, p50: p50, p95: p50 * 1.2, completed: int(thr / 2)})
		if i >= ticks-30 {
			sum += c.level
		}
	}
	return float64(sum) / 30
}

func TestControllerTracksCapacityUpAndDown(t *testing.T) {
	c := newController()
	one := simulate(&c, 10, 80)
	if one < 8 || one > 15 {
		t.Fatalf("level at capacity 10 = %.1f; want near 10", one)
	}
	three := simulate(&c, 30, 120)
	if three <= one*2 || three < 24 || three > 45 {
		t.Fatalf("level after capacity 30 = %.1f (was %.1f); want to rise near 30", three, one)
	}
	two := simulate(&c, 20, 120)
	if two >= three || two < 16 || two > 30 {
		t.Fatalf("level after capacity 20 = %.1f (was %.1f); want to fall near 20", two, three)
	}
}

func TestControllerIsDeterministic(t *testing.T) {
	a, b := newController(), newController()
	if simulate(&a, 17, 50) != simulate(&b, 17, 50) {
		t.Fatal("identical inputs produced different levels")
	}
}

func TestControllerBacksOffOnErrors(t *testing.T) {
	c := newController()
	simulate(&c, 30, 40)
	before := c.level
	c.next(observation{throughput: 0, completed: 1, failed: 50})
	if c.level >= before {
		t.Fatalf("level = %d after errors; want below %d", c.level, before)
	}
}

func TestLoadGeneratorDrivesOrdersAndStops(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	submit := func(_ context.Context, req CreateOrderRequest) (Order, error) {
		mu.Lock()
		defer mu.Unlock()
		if seen[req.OrderID] {
			return Order{}, errors.New("duplicate order ID " + req.OrderID)
		}
		seen[req.OrderID] = true
		time.Sleep(time.Millisecond)
		return Order{ID: req.OrderID, Status: OrderCompleted}, nil
	}
	generator := NewLoadGenerator("node-1", submit)
	generator.tick = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go generator.Run(ctx)

	if generator.Snapshot(0).Running {
		t.Fatal("generator must start stopped")
	}
	generator.SetRunning(ctx, true)
	deadline := time.Now().Add(3 * time.Second)
	for generator.Snapshot(0).Completed < 100 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	on := generator.Snapshot(0)
	if on.Completed < 100 || on.Failed != 0 || !on.Running || on.Concurrency < 2 || len(on.Samples) == 0 {
		t.Fatalf("snapshot while ON = %+v", on)
	}
	if on.Throughput == 0 && on.Samples[len(on.Samples)-1].Throughput == 0 {
		t.Fatal("no throughput measured")
	}

	generator.SetRunning(ctx, false)
	time.Sleep(100 * time.Millisecond)
	stopped := generator.Snapshot(0)
	time.Sleep(100 * time.Millisecond)
	after := generator.Snapshot(0)
	if after.Running || after.Completed != stopped.Completed || after.InFlight != 0 {
		t.Fatalf("generator kept working while OFF: %+v -> %+v", stopped, after)
	}
	since := on.Samples[len(on.Samples)-1].UnixMilli
	for _, sample := range generator.Snapshot(since).Samples {
		if sample.UnixMilli <= since {
			t.Fatalf("sample %d is not newer than %d", sample.UnixMilli, since)
		}
	}
}

func TestLoadGeneratorCountsFailures(t *testing.T) {
	generator := NewLoadGenerator("node-1", func(context.Context, CreateOrderRequest) (Order, error) {
		return Order{}, errors.New("node lost")
	})
	generator.tick = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go generator.Run(ctx)
	generator.SetRunning(ctx, true)
	deadline := time.Now().Add(3 * time.Second)
	for generator.Snapshot(0).Failed == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	snapshot := generator.Snapshot(0)
	if snapshot.Failed == 0 || snapshot.Completed != 0 || snapshot.ErrorRate != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestLoadGeneratorStopsWhenOwnershipIsLost(t *testing.T) {
	generator := NewLoadGenerator("node-1", func(context.Context, CreateOrderRequest) (Order, error) {
		time.Sleep(time.Millisecond)
		return Order{Status: OrderCompleted}, nil
	})
	generator.tick = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go generator.Run(ctx)
	generator.SetRunning(ctx, true)
	time.Sleep(100 * time.Millisecond)

	generator.SetEnabled(false)
	time.Sleep(100 * time.Millisecond)
	stopped := generator.Snapshot(0)
	time.Sleep(100 * time.Millisecond)
	after := generator.Snapshot(0)
	if after.Running || after.Completed != stopped.Completed || after.InFlight != 0 {
		t.Fatalf("stale owner kept generating: %+v -> %+v", stopped, after)
	}
	generator.SetRunning(ctx, true)
	if generator.Snapshot(0).Running {
		t.Fatal("a non-owner accepted Load ON")
	}
	generator.SetEnabled(true)
	if generator.Snapshot(0).Running {
		t.Fatal("a new owner must start stopped until the monitor re-applies Load ON")
	}
}

func TestRunOwnedWithoutProviderOwnsCapability(t *testing.T) {
	generator := NewLoadGenerator("node-1", nil)
	generator.SetEnabled(false)
	ctx, cancel := context.WithCancel(context.Background())
	go generator.RunOwned(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for !generator.Enabled() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !generator.Enabled() {
		t.Fatal("sole owner was not enabled")
	}
	cancel()
	deadline = time.Now().Add(2 * time.Second)
	for generator.Enabled() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if generator.Enabled() {
		t.Fatal("generator stayed enabled after its context ended")
	}
}
