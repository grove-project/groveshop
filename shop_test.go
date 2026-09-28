package groveshop_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grove-project/groveshop"
)

// shopHarness drives a Shop on a fake clock with instant station work.
type shopHarness struct {
	t     *testing.T
	shop  *groveshop.Shop
	mu    sync.Mutex
	now   time.Time
	nodes []string
	// fail, when set, decides whether one piece of work fails.
	fail func(groveshop.WorkRequest) bool
	work map[groveshop.Station]int
	// seen accumulates the activity stream, which the shop itself bounds.
	seen    []groveshop.ShopEvent
	seenSeq int64
}

func newShopHarness(t *testing.T, nodes ...string) *shopHarness {
	t.Helper()
	h := &shopHarness{t: t, now: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC), nodes: nodes, work: map[groveshop.Station]int{}}
	h.shop = groveshop.NewShop(groveshop.ShopDeps{
		Node: "node-1",
		Now:  h.clock,
		Rand: rand.New(rand.NewPCG(7, 11)),
		Work: func(_ context.Context, req groveshop.WorkRequest) (groveshop.WorkResult, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.fail != nil && h.fail(req) {
				return groveshop.WorkResult{}, errors.New("node lost")
			}
			h.work[req.Station]++
			node := h.nodes[h.work[req.Station]%len(h.nodes)]
			return groveshop.WorkResult{Station: req.Station, OrderID: req.OrderID, Item: req.Item, Node: node}, nil
		},
		Capacity: func(context.Context) ([]string, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return append([]string(nil), h.nodes...), nil
		},
	})
	h.shop.Open()
	h.shop.RefreshCapacity(t.Context())
	return h
}

func (h *shopHarness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *shopHarness) setNodes(nodes ...string) {
	h.mu.Lock()
	h.nodes = nodes
	h.mu.Unlock()
	h.shop.RefreshCapacity(h.t.Context())
}

// run advances the fake clock by d in 100ms steps, letting the instant work
// started by each step finish before the next.
func (h *shopHarness) run(d time.Duration) {
	h.t.Helper()
	for elapsed := time.Duration(0); elapsed < d; elapsed += 100 * time.Millisecond {
		h.mu.Lock()
		h.now = h.now.Add(100 * time.Millisecond)
		h.mu.Unlock()
		h.shop.Step(h.t.Context())
		h.waitIdle()
		h.collect()
	}
}

func (h *shopHarness) collect() {
	for _, event := range h.shop.Snapshot(h.seenSeq, 0).Events {
		h.seen = append(h.seen, event)
		h.seenSeq = event.Seq
	}
}

func (h *shopHarness) waitIdle() {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		busy := 0
		for _, station := range h.shop.Snapshot(0, 0).Stations {
			busy += station.Busy
		}
		if busy == 0 {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatal("station work did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *shopHarness) events() []groveshop.ShopEvent {
	h.collect()
	return h.seen
}

func hasEvent(events []groveshop.ShopEvent, kind, text string) bool {
	for _, event := range events {
		if (kind == "" || event.Kind == kind) && strings.Contains(event.Text, text) {
			return true
		}
	}
	return false
}

func station(snapshot groveshop.ShopSnapshot, id groveshop.Station) groveshop.StationView {
	for _, view := range snapshot.Stations {
		if view.ID == id {
			return view
		}
	}
	return groveshop.StationView{}
}

// The shop opens with no setup: customers arrive on their own, cashiers take
// orders, drinks and food are made at their own stations, and customers pick
// up complete orders.
func TestShopRunsCustomersThroughEveryStation(t *testing.T) {
	h := newShopHarness(t, "node-1", "node-2", "node-3")
	h.run(90 * time.Second)

	snapshot := h.shop.Snapshot(0, 0)
	if snapshot.Metrics.Served == 0 {
		t.Fatalf("no customers served after 90s: %+v", snapshot.Metrics)
	}
	for _, id := range groveshop.Stations {
		if h.work[id] == 0 {
			t.Errorf("station %s did no work", id)
		}
	}
	if snapshot.Metrics.WorkDone != int64(h.work[groveshop.StationCashier]+h.work[groveshop.StationBarista]+h.work[groveshop.StationKitchen]) {
		t.Errorf("work done = %d; want the %v pieces of station work", snapshot.Metrics.WorkDone, h.work)
	}
	if !hasEvent(h.events(), "", "picked up") || !hasEvent(h.events(), "", "entered") {
		t.Errorf("activity stream lacks arrivals or pickups: %+v", snapshot.Events)
	}
	if len(snapshot.History) < 80 {
		t.Errorf("history has %d points after 90s; want one per second", len(snapshot.History))
	}
	for _, id := range groveshop.Stations {
		if len(station(snapshot, id).ByNode) == 0 {
			t.Errorf("station %s reports no work by node", id)
		}
	}
}

// Staff are Grove capacity: StaffPerNode per healthy node, all assigned to a
// station, and a node joining or leaving changes them and is reported.
func TestShopStaffFollowGroveCapacity(t *testing.T) {
	h := newShopHarness(t, "node-1", "node-2", "node-3")
	assertStaff := func(want int) {
		t.Helper()
		snapshot := h.shop.Snapshot(0, 0)
		assigned := 0
		for _, view := range snapshot.Stations {
			assigned += view.Staff
			if view.Staff < 1 {
				t.Errorf("station %s has no staff", view.ID)
			}
		}
		if snapshot.StaffTotal != want || assigned != want {
			t.Fatalf("staff total %d, assigned %d; want %d", snapshot.StaffTotal, assigned, want)
		}
	}
	assertStaff(3 * groveshop.StaffPerNode)

	h.setNodes("node-1", "node-2", "node-3", "node-4")
	assertStaff(4 * groveshop.StaffPerNode)
	if !hasEvent(h.events(), "node_joined", "node-4") {
		t.Error("node-4 joining was not reported")
	}

	h.setNodes("node-1", "node-3", "node-4")
	assertStaff(3 * groveshop.StaffPerNode)
	if !hasEvent(h.events(), "node_lost", "node-2") {
		t.Error("node-2 loss was not reported")
	}
}

// The shift manager moves staff toward the station under most pressure
// instead of scaling every station equally.
func TestShopShiftsStaffTowardTheBottleneck(t *testing.T) {
	h := newShopHarness(t, "node-1", "node-2", "node-3")
	// Baristas and cashiers keep dropping their work (as if their node were
	// struggling) so drink and order queues build while the kitchen is idle.
	var drinks atomic.Bool
	drinks.Store(true)
	h.fail = func(req groveshop.WorkRequest) bool { return drinks.Load() && req.Station == groveshop.StationBarista }
	h.run(60 * time.Second)
	before := h.shop.Snapshot(0, 0)
	if station(before, groveshop.StationBarista).Queue == 0 {
		t.Fatal("no drink queue built up")
	}
	barista, kitchen := station(before, groveshop.StationBarista), station(before, groveshop.StationKitchen)
	if barista.Staff <= kitchen.Staff {
		t.Fatalf("baristas %d not favoured over idle kitchen %d with %d drinks waiting", barista.Staff, kitchen.Staff, barista.Queue)
	}
	if !hasEvent(h.events(), "rebalanced", "drink preparation") {
		t.Error("capacity shift toward drinks was not reported")
	}
}

// Work that fails because a node was lost goes back to the queue and is done
// by other staff.
func TestShopRetriesFailedWork(t *testing.T) {
	h := newShopHarness(t, "node-1", "node-2", "node-3")
	var failures atomic.Int32
	h.fail = func(req groveshop.WorkRequest) bool {
		return req.Station == groveshop.StationKitchen && failures.Add(1) <= 3
	}
	h.run(60 * time.Second)
	if failures.Load() <= 3 {
		t.Fatal("kitchen was never asked to retry")
	}
	if h.work[groveshop.StationKitchen] == 0 {
		t.Fatal("kitchen work was never completed after failures")
	}
	if !hasEvent(h.events(), "retry", "food preparation") {
		t.Error("interrupted work was not reported")
	}
}

// Running out of an ingredient makes products unavailable, dispatches the
// supplier, and a delivery brings the products back.
func TestShopInventoryAndSuppliers(t *testing.T) {
	h := newShopHarness(t, "node-1", "node-2", "node-3")
	h.shop.Restore(groveshop.ShopCarryover{Inventory: map[string]int{"milk": 0}})
	h.run(time.Second)
	snapshot := h.shop.Snapshot(0, 0)
	for _, product := range []string{"Latte", "Cappuccino", "Flat White"} {
		if !strings.Contains(strings.Join(snapshot.Unavailable, ","), product) {
			t.Errorf("%s is available with no milk: %v", product, snapshot.Unavailable)
		}
	}
	if !hasEvent(h.events(), "supplier", "Dairy Co.") {
		t.Fatal("dairy supplier was not dispatched when milk ran out")
	}
	h.run(3 * time.Minute)
	snapshot = h.shop.Snapshot(0, 0)
	if !hasEvent(h.events(), "supplier_arrived", "Dairy Co.") || !hasEvent(h.events(), "available", "Latte") {
		t.Fatalf("dairy delivery did not bring milk drinks back: %v", snapshot.Unavailable)
	}
}

// With no staff at all, waiting customers eventually give up.
func TestShopCustomersLeaveWhenNobodyServesThem(t *testing.T) {
	h := newShopHarness(t)
	h.run(3 * time.Minute)
	snapshot := h.shop.Snapshot(0, 0)
	if snapshot.Metrics.Left == 0 || snapshot.Metrics.Served != 0 {
		t.Fatalf("metrics = %+v; want customers leaving and none served", snapshot.Metrics)
	}
}

// A relocated shop takes over the business state of the one it replaced, and
// only once.
func TestShopRestoreCarriesOverOnce(t *testing.T) {
	h := newShopHarness(t, "node-1", "node-3")
	h.shop.Restore(groveshop.ShopCarryover{
		Served: 400, Left: 3, CustomerSeq: 450, OrderSeq: 420,
		Inventory: map[string]int{"beans": 10}, Nodes: []string{"node-1", "node-2", "node-3"},
	})
	h.shop.Restore(groveshop.ShopCarryover{Served: 1000})
	snapshot := h.shop.Snapshot(0, 0)
	if snapshot.Metrics.Served != 400 || snapshot.Metrics.Left != 3 {
		t.Errorf("metrics after restore = %+v", snapshot.Metrics)
	}
	if snapshot.Carryover.Inventory["beans"] != 10 {
		t.Errorf("beans after restore = %d; want 10", snapshot.Carryover.Inventory["beans"])
	}
	if !hasEvent(snapshot.Events, "node_lost", "node-2") || !hasEvent(snapshot.Events, "relocated", "carried over") {
		t.Errorf("restore events = %+v", snapshot.Events)
	}
}

// Losing the capability pauses the shop; regaining it soon resumes the same
// shop, and regaining it much later opens a fresh one.
func TestShopOwnership(t *testing.T) {
	h := newShopHarness(t, "node-1")
	h.run(40 * time.Second)
	served := h.shop.Snapshot(0, 0).Metrics.Served
	if served == 0 {
		t.Fatal("nothing served before the pause")
	}

	h.shop.SetEnabled(false)
	if h.shop.Enabled() || !h.shop.Serving() || !h.shop.Snapshot(0, 0).Paused {
		t.Fatal("losing ownership must pause, not close, the shop")
	}
	work := h.shop.Snapshot(0, 0).Metrics.WorkDone
	h.run(5 * time.Second)
	if got := h.shop.Snapshot(0, 0).Metrics.WorkDone; got != work {
		t.Fatalf("paused shop kept working: %d -> %d", work, got)
	}
	h.shop.SetEnabled(true)
	if snapshot := h.shop.Snapshot(0, 0); snapshot.Paused || snapshot.Metrics.Served < served || !hasEvent(h.events(), "resumed", "pause") {
		t.Fatalf("shop did not resume: paused=%v served=%d", snapshot.Paused, snapshot.Metrics.Served)
	}

	h.shop.SetEnabled(false)
	h.run(time.Minute)
	h.shop.SetEnabled(true)
	if snapshot := h.shop.Snapshot(0, 0); snapshot.Paused || snapshot.Metrics.Served != 0 || len(snapshot.Events) != 1 {
		t.Fatalf("shop regained after a long pause is not fresh: %+v", snapshot.Metrics)
	}
}
