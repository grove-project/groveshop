package groveshop_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/grove-project/groveshop"
)

func TestWebHandlerServesEmbeddedIndex(t *testing.T) {
	server := httptest.NewServer(groveshop.WebHandler())
	defer server.Close()
	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Errorf("GET / status = %d; want %d", response.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(body), `data-grove-artifact="grove-shop-ui-v1"`) {
		t.Errorf("GET / body does not contain Grove Shop UI fingerprint: %q", body)
	}
}

func TestWebAsset(t *testing.T) {
	asset, err := groveshop.WebAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(asset), "Grove Cluster Status") {
		t.Errorf("embedded index = %q", asset)
	}
	for _, contract := range []string{`fetch("/grove/status"`, `fetch("/api/orders"`, "statusIntervalMilliseconds = 750"} {
		if !strings.Contains(string(asset), contract) {
			t.Errorf("embedded index does not contain %q", contract)
		}
	}
	if _, err := groveshop.WebAsset("missing.html"); err == nil {
		t.Fatal("missing embedded asset returned nil error")
	}
}

func TestWebHandlerExposesOrdersAndClusterStatus(t *testing.T) {
	status := groveshop.ClusterStatusView{
		Health: "healthy",
		Ready:  true,
		Nodes:  []groveshop.NodeStatusView{{NodeID: "node-1", Health: "healthy", Components: []groveshop.ComponentStatusView{}}},
		Placements: []groveshop.PlacementStatusView{{
			ServiceID: 1, Name: "Orders", NodeID: "node-1", ArtifactDigest: "sha256:artifact", Health: "healthy",
		}},
		ActiveArtifact: &groveshop.ArtifactStatusView{ApplicationID: "grove-shop", ConfigRevision: "acme-r42"},
		Rollout:        &groveshop.RolloutStatusView{Generation: 1, Phase: "active"},
	}
	wantOrder := groveshop.Order{
		ID: "web-order", Status: groveshop.OrderCompleted,
		History: []groveshop.OrderStatus{groveshop.OrderCreated, groveshop.OrderReserved, groveshop.OrderPaid, groveshop.OrderShipping, groveshop.OrderCompleted},
	}
	handler := groveshop.WebHandlerWithRuntime(
		groveshop.DefaultConfiguration(),
		"sha256:config",
		func(_ context.Context) (groveshop.ClusterStatusView, error) { return status, nil },
		func(_ context.Context, request groveshop.CreateOrderRequest) (groveshop.Order, error) {
			if request.OrderID != wantOrder.ID {
				t.Errorf("order request = %#v", request)
			}
			return wantOrder, nil
		},
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Get(server.URL + "/grove/status")
	if err != nil {
		t.Fatal(err)
	}
	var gotStatus groveshop.ClusterStatusView
	if err := json.NewDecoder(response.Body).Decode(&gotStatus); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !reflect.DeepEqual(gotStatus, status) {
		t.Errorf("GET /grove/status = %d %#v; want %#v", response.StatusCode, gotStatus, status)
	}

	response, err = http.Post(server.URL+"/api/orders", "application/json", strings.NewReader(`{"OrderID":"web-order","SKU":"coffee-beans","Quantity":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var created groveshop.Order
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated || !reflect.DeepEqual(created, wantOrder) {
		t.Errorf("POST /api/orders = %d %#v; want %#v", response.StatusCode, created, wantOrder)
	}

	response, err = http.Get(server.URL + "/api/orders")
	if err != nil {
		t.Fatal(err)
	}
	var orders []groveshop.Order
	if err := json.NewDecoder(response.Body).Decode(&orders); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !reflect.DeepEqual(orders, []groveshop.Order{wantOrder}) {
		t.Errorf("GET /api/orders = %#v; want %#v", orders, []groveshop.Order{wantOrder})
	}
}

func TestWebHandlerExposesReadOnlyRuntimeConfiguration(t *testing.T) {
	configuration := groveshop.DefaultConfiguration()
	configuration.Revision = "acme-r42"
	configuration.Customer.Name = "Acme Retail"
	configuration.Cluster.Name = "production"
	configuration.Node.Zone = "edge"
	configuration.Inventory.ReservationBuffer = 7
	server := httptest.NewServer(groveshop.WebHandlerWithConfiguration(configuration, "sha256:config"))
	defer server.Close()
	response, err := http.Get(server.URL + "/grove/config")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var view groveshop.RuntimeConfigurationView
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.Revision != "acme-r42" || view.ConfigDigest != "sha256:config" || view.CustomerName != "Acme Retail" || view.ClusterName != "production" || view.NodeZone != "edge" || view.ReservationBuffer != 7 {
		t.Errorf("runtime configuration view = %#v", view)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/grove/config", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /grove/config status = %d; want %d", response.StatusCode, http.StatusMethodNotAllowed)
	}
}

func TestWebHandlerLoadAPI(t *testing.T) {
	var running bool
	load := groveshop.LoadClient{
		Set: func(_ context.Context, on bool) (groveshop.LoadSnapshot, error) {
			running = on
			return groveshop.LoadSnapshot{Instance: "a", Running: on}, nil
		},
		Snapshot: func(context.Context, int64) (groveshop.LoadSnapshot, error) {
			return groveshop.LoadSnapshot{Instance: "a", Running: running}, nil
		},
	}
	status := func(context.Context) (groveshop.ClusterStatusView, error) {
		return groveshop.ClusterStatusView{Health: "healthy", Ready: true}, nil
	}
	monitor := groveshop.NewLoadMonitor(load, status)
	server := httptest.NewServer(groveshop.WebHandlerWithLoad(groveshop.DefaultConfiguration(), "", status, nil, monitor))
	defer server.Close()

	response, err := http.Post(server.URL+"/api/load", "application/json", strings.NewReader(`{"running":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var view groveshop.LoadView
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !view.Desired || !running || !view.Current.Running {
		t.Fatalf("POST /api/load = %d %+v (running=%v)", response.StatusCode, view, running)
	}

	get, err := http.Get(server.URL + "/api/load")
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/load status = %d", get.StatusCode)
	}

	bad, err := http.Post(server.URL+"/api/load", "application/json", strings.NewReader(`{"bogus":1}`))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d; want 400", bad.StatusCode)
	}

	disabled := httptest.NewServer(groveshop.WebHandler())
	defer disabled.Close()
	off, err := http.Get(disabled.URL + "/api/load")
	if err != nil {
		t.Fatal(err)
	}
	off.Body.Close()
	if off.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/load without monitor = %d; want 503", off.StatusCode)
	}
}
