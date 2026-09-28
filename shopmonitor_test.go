package groveshop_test

import (
	"context"
	"errors"
	"testing"

	"github.com/grove-project/groveshop"
)

// The monitor keeps one activity stream and history across Shop relocations
// and hands the new Shop the old one's business state.
func TestShopMonitorCarriesStateAcrossRelocation(t *testing.T) {
	first := groveshop.NewShop(groveshop.ShopDeps{Node: "node-1"})
	first.Open()
	first.Restore(groveshop.ShopCarryover{Served: 42})
	second := groveshop.NewShop(groveshop.ShopDeps{Node: "node-2"})
	second.Open()

	current := first
	var unreachable bool
	monitor := groveshop.NewShopMonitor(func(_ context.Context, request groveshop.ShopRequest) (groveshop.ShopSnapshot, error) {
		if unreachable {
			return groveshop.ShopSnapshot{}, errors.New("no owner")
		}
		if request.Restore != nil {
			current.Restore(*request.Restore)
		}
		return current.Snapshot(request.SinceEvent, request.SinceHistory), nil
	})

	monitor.Poll(t.Context())
	if view := monitor.View(); !view.Available || view.Node != "node-1" || view.Metrics.Served != 42 {
		t.Fatalf("first view = %+v", view.Metrics)
	}
	rush := groveshop.ShopDemand{Mode: groveshop.DemandRush, PerMinute: 90}
	if err := first.SetDemand(rush); err != nil {
		t.Fatal(err)
	}
	monitor.Poll(t.Context())
	events := len(monitor.View().Events)

	unreachable = true
	monitor.Poll(t.Context())
	if view := monitor.View(); view.Available || view.Error == "" {
		t.Fatalf("view while relocating = available %v error %q", view.Available, view.Error)
	}

	unreachable, current = false, second
	monitor.Poll(t.Context())
	view := monitor.View()
	if !view.Available || view.Node != "node-2" || view.Metrics.Served != 42 {
		t.Fatalf("relocated view = node %s %+v", view.Node, view.Metrics)
	}
	if view.Demand != rush {
		t.Fatalf("customer flow after relocation = %+v; want %+v", view.Demand, rush)
	}
	if len(view.Events) <= events || !hasEvent(view.Events, "relocated", "node-2") || !hasEvent(view.Events, "open", "node-1") {
		t.Fatalf("activity stream after relocation = %+v", view.Events)
	}
}
