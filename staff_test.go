package groveshop_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/grove-project/grove"
	"github.com/grove-project/groveshop"
)

// A crew never runs more work at once than it has staff.
func TestCrewLimitsConcurrentWork(t *testing.T) {
	crew := groveshop.NewCrew("node-1", 1)
	started := make(chan struct{})
	done := make(chan error)
	go func() {
		close(started)
		_, err := crew.Work(t.Context(), groveshop.WorkRequest{Station: groveshop.StationBarista, DurationMillis: 1000})
		done <- err
	}()
	<-started
	time.Sleep(50 * time.Millisecond)
	if _, err := crew.Work(t.Context(), groveshop.WorkRequest{Station: groveshop.StationKitchen, DurationMillis: 10}); !errors.Is(err, groveshop.ErrStaffBusy) {
		t.Fatalf("second concurrent work error = %v; want ErrStaffBusy", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	result, err := crew.Work(t.Context(), groveshop.WorkRequest{Station: groveshop.StationKitchen, DurationMillis: 10, Item: "Muffin"})
	if err != nil || result.Node != "node-1" || result.Item != "Muffin" {
		t.Fatalf("work after staff freed = %+v, %v", result, err)
	}
}

func TestCrewRejectsInvalidWork(t *testing.T) {
	crew := groveshop.NewCrew("node-1", 1)
	for _, req := range []groveshop.WorkRequest{
		{Station: "bakery", DurationMillis: 10},
		{Station: groveshop.StationBarista},
		{Station: groveshop.StationBarista, DurationMillis: int(groveshop.MaxWorkDuration.Milliseconds()) + 1},
	} {
		if _, err := crew.Work(t.Context(), req); !errors.Is(err, groveshop.ErrWorkInvalid) {
			t.Errorf("Work(%+v) error = %v; want ErrWorkInvalid", req, err)
		}
	}
}

// Nodes in one process keep separate crews.
func TestNodeCrewIsPerNode(t *testing.T) {
	if groveshop.NodeCrew("crew-a") != groveshop.NodeCrew("crew-a") || groveshop.NodeCrew("crew-a") == groveshop.NodeCrew("crew-b") {
		t.Fatal("NodeCrew must return one crew per node")
	}
}

// Station handlers are ordinary Grove handlers under their own IDs.
func TestRegisterStationDispatchesThroughGrove(t *testing.T) {
	registry := &grove.Registry{}
	crew := groveshop.NewCrew("node-7", 2)
	for _, station := range groveshop.Stations {
		if err := groveshop.RegisterStation(registry, station, crew); err != nil {
			t.Fatal(err)
		}
	}
	client, err := grove.NewClient(registry)
	if err != nil {
		t.Fatal(err)
	}
	for _, station := range groveshop.Stations {
		service, method := groveshop.StationService(station)
		result, err := grove.Call[groveshop.WorkRequest, groveshop.WorkResult](t.Context(), client, service, method,
			groveshop.WorkRequest{Station: station, OrderID: 9, Item: "x", DurationMillis: 5})
		if err != nil || result.Node != "node-7" || result.Station != station {
			t.Fatalf("%s call = %+v, %v", station, result, err)
		}
	}
	service, method := groveshop.StationService(groveshop.StationBarista)
	_, err = grove.Call[groveshop.WorkRequest, groveshop.WorkResult](context.Background(), client, service, method,
		groveshop.WorkRequest{Station: groveshop.StationKitchen, DurationMillis: 5})
	if err == nil || !strings.Contains(err.Error(), "barista handler received") {
		t.Fatalf("misrouted work error = %v", err)
	}
	if err := groveshop.RegisterStation(nil, groveshop.StationBarista, crew); !errors.Is(err, groveshop.ErrRegistryRequired) {
		t.Errorf("nil registry error = %v", err)
	}
	if err := groveshop.RegisterStation(registry, groveshop.StationBarista, nil); !errors.Is(err, groveshop.ErrServiceRequired) {
		t.Errorf("nil crew error = %v", err)
	}
}

// Only the capability owner serves the Shop handler.
func TestRegisterShopServesOnlyTheOwner(t *testing.T) {
	registry := &grove.Registry{}
	shop := groveshop.NewShop(groveshop.ShopDeps{Node: "node-1"})
	if err := groveshop.RegisterShop(registry, shop); err != nil {
		t.Fatal(err)
	}
	client, err := grove.NewClient(registry)
	if err != nil {
		t.Fatal(err)
	}
	shop.SetEnabled(false)
	if _, err := grove.Call[groveshop.ShopRequest, groveshop.ShopSnapshot](t.Context(), client, groveshop.ServiceShop, groveshop.MethodShop, groveshop.ShopRequest{}); err == nil || !strings.Contains(err.Error(), groveshop.ErrNotShopOwner.Error()) {
		t.Fatalf("non-owner call error = %v", err)
	}
	shop.SetEnabled(true)
	snapshot, err := grove.Call[groveshop.ShopRequest, groveshop.ShopSnapshot](t.Context(), client, groveshop.ServiceShop, groveshop.MethodShop,
		groveshop.ShopRequest{Restore: &groveshop.ShopCarryover{Served: 12}})
	if err != nil || snapshot.Node != "node-1" || snapshot.Metrics.Served != 12 {
		t.Fatalf("owner call = %+v, %v", snapshot.Metrics, err)
	}
}
