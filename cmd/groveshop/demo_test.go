package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grove-project/grove/grovetest"
	"github.com/grove-project/groveshop"
)

// TestGroveShopDemoFlow is Grove Shop's own end-to-end acceptance of the demo
// in docs/DEMO_FLOW.md, driven against the real configured artifact:
//
//	run artifact -> bootstrap cluster
//	run same artifact -> join (twice)
//	open the Web ingress, place an order through every service
//	kill Inventory's node -> services recover -> order succeeds on the same endpoint
//	restart that node -> cluster converges -> order succeeds on the same endpoint
//
// Every wait is condition-based; there are no fixed sleeps.
func TestGroveShopDemoFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 240*time.Second)
	defer cancel()

	artifact := buildConfiguredGroveshop(ctx, t, filepath.Join("..", "..", "configs", "acme.yaml"))

	ports := reservePorts(t, 4)
	webAddress := "127.0.0.1:" + strconv.Itoa(ports[3])
	baseURL := "http://" + webAddress

	// 1. Run the artifact: node-1 bootstraps the cluster and owns ingress.
	nodes := []*grovetest.Node{startDemoNode(t, artifact, "node-1", ports[0], 0, webAddress)}
	// 2. Run the same artifact twice more: node-2 and node-3 join.
	nodes = append(nodes,
		startDemoNode(t, artifact, "node-2", ports[1], ports[0], ""),
		startDemoNode(t, artifact, "node-3", ports[2], ports[0], ""),
	)
	nodeIDs := []string{"node-1", "node-2", "node-3"}
	for i, node := range nodes {
		if err := node.WaitReady(ctx); err != nil {
			t.Fatalf("bootstrap cluster: %s: %v\n%s", nodeIDs[i], err, dumpLogs(nodes))
		}
	}
	status := waitForDemoStatus(t, ctx, baseURL, nodes, "three healthy nodes with every service placed", func(s demoStatus) bool {
		return s.healthy() && s.nodeIDs() == "node-1,node-2,node-3"
	})
	t.Logf("bootstrapped: nodes=%s placements=%s", status.nodeIDs(), status.placementSummary())

	// 3. Open Grove Shop through the Grove-managed ingress.
	assertDemoWeb(t, ctx, baseURL)
	placeDemoOrder(t, ctx, baseURL, "demo-bootstrap", nodes)

	// 4. Terminate the node hosting Inventory, the service the demo's recovery
	// scenario follows. The cluster relocates its services to survivors (the
	// ingress too, if it was there) and the same endpoint keeps serving orders.
	victimID := status.placementNode("Inventory")
	victimIndex := slices.Index(nodeIDs, victimID)
	if victimIndex < 0 {
		t.Fatalf("Inventory is not placed on a cluster node: %s", status.placementSummary())
	}
	victim := nodes[victimIndex]
	if err := victim.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("killed %s, which hosted Inventory", victimID)
	status = waitForDemoStatus(t, ctx, baseURL, nodes, "services relocated off killed "+victimID, func(s demoStatus) bool {
		return s.everyServicePlacedHealthyOff(victimID)
	})
	t.Logf("after kill: health=%s placements=%s", status.Health, status.placementSummary())
	placeDemoOrder(t, ctx, baseURL, "demo-after-kill", nodes)

	// Restore the killed node and verify every view converges on three
	// healthy nodes.
	if err := victim.Restart(); err != nil {
		t.Fatal(err)
	}
	if err := victim.WaitReady(ctx); err != nil {
		t.Fatalf("restart %s: %v\n%s", victimID, err, dumpLogs(nodes))
	}
	status = waitForDemoStatus(t, ctx, baseURL, nodes, "three healthy nodes after "+victimID+" rejoined", func(s demoStatus) bool {
		return s.healthy() && s.nodeIDs() == "node-1,node-2,node-3"
	})
	t.Logf("after rejoin: nodes=%s placements=%s", status.nodeIDs(), status.placementSummary())
	placeDemoOrder(t, ctx, baseURL, "demo-after-rejoin", nodes)
}

// buildConfiguredGroveshop produces the demo artifact the way `make build`
// does: compile Grove Shop, then embed configPath with the Grove CLI.
func buildConfiguredGroveshop(ctx context.Context, t *testing.T, configPath string) string {
	t.Helper()
	unconfigured := buildGroveshop(ctx, t)
	dir := t.TempDir()
	groveCLI := filepath.Join(dir, "grove")
	build := exec.CommandContext(ctx, "go", "build", "-o", groveCLI, "github.com/grove-project/grove/cmd/grove")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Grove CLI: %v\n%s", err, output)
	}
	artifact := filepath.Join(dir, "groveshop")
	embed := exec.CommandContext(ctx, groveCLI, "config", "embed",
		"--binary", unconfigured, "--config", configPath, "--output", artifact)
	if output, err := embed.CombinedOutput(); err != nil {
		t.Fatalf("embed %s: %v\n%s", configPath, err, output)
	}
	return artifact
}

// startDemoNode runs the artifact as one cluster node. A seedPort of 0 founds
// the cluster; ingressAddress is set only on the founder.
func startDemoNode(t *testing.T, artifact, nodeID string, port, seedPort int, ingressAddress string) *grovetest.Node {
	t.Helper()
	args := []string{
		"--node-id", nodeID,
		"--advertise-endpoint", "nats-subject://system/" + nodeID,
		"--system-nats-listen", "127.0.0.1:0",
		"--system-nats-route-listen", "127.0.0.1:" + strconv.Itoa(port),
		"--system-nats-membership", "--system-nats-recovery", "--system-nats-retire-on-stop",
		"--system-nats-subject", "_GROVE.system.cli." + nodeID,
	}
	if seedPort != 0 {
		args = append(args, "--system-nats-seed", "nats-route://127.0.0.1:"+strconv.Itoa(seedPort))
	}
	if ingressAddress != "" {
		args = append(args, "--ingress-address", ingressAddress)
	}
	node, err := grovetest.StartNode(artifact, args...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Cleanup() })
	return node
}

type demoStatus struct {
	Health string `json:"health"`
	Ready  bool   `json:"ready"`
	Nodes  []struct {
		NodeID string `json:"node_id"`
		Health string `json:"health"`
	} `json:"nodes"`
	Placements []struct {
		Name   string `json:"name"`
		NodeID string `json:"node_id"`
		Health string `json:"health"`
	} `json:"placements"`
}

// demoServices are the services a healthy Grove Shop cluster must place.
var demoServices = []string{"Orders", "Inventory", "Payment", "Shipping", "Web", "LoadGen"}

func (s demoStatus) healthy() bool {
	return s.Ready && s.Health == "healthy" && s.everyServicePlacedHealthyOff("")
}

func (s demoStatus) everyServicePlacedHealthyOff(excludedNode string) bool {
	if !s.Ready {
		return false
	}
	for _, service := range demoServices {
		placed := false
		for _, placement := range s.Placements {
			if placement.Name != service {
				continue
			}
			if placement.NodeID == excludedNode || placement.Health != "healthy" {
				return false
			}
			placed = true
		}
		if !placed {
			return false
		}
	}
	return true
}

func (s demoStatus) placementNode(service string) string {
	for _, placement := range s.Placements {
		if placement.Name == service {
			return placement.NodeID
		}
	}
	return ""
}

func (s demoStatus) nodeIDs() string {
	ids := make([]string, 0, len(s.Nodes))
	for _, node := range s.Nodes {
		if node.Health == "healthy" {
			ids = append(ids, node.NodeID)
		}
	}
	slices.Sort(ids)
	return strings.Join(ids, ",")
}

func (s demoStatus) placementSummary() string {
	parts := make([]string, 0, len(s.Placements))
	for _, placement := range s.Placements {
		parts = append(parts, fmt.Sprintf("%s@%s(%s)", placement.Name, placement.NodeID, placement.Health))
	}
	return strings.Join(parts, " ")
}

func waitForDemoStatus(
	t *testing.T, ctx context.Context, baseURL string, nodes []*grovetest.Node, want string, done func(demoStatus) bool,
) demoStatus {
	t.Helper()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var last demoStatus
	var lastErr error
	for {
		var status demoStatus
		// Bound each poll so one request stuck on a dead node cannot consume
		// the whole wait.
		requestCtx, requestCancel := context.WithTimeout(waitCtx, 3*time.Second)
		lastErr = getJSON(requestCtx, baseURL+"/grove/status", &status)
		requestCancel()
		if lastErr == nil {
			last = status
			if done(status) {
				return status
			}
		}
		select {
		case <-ticker.C:
		case <-waitCtx.Done():
			t.Fatalf("wait for %s: %v (last error %v)\nlast status: health=%s ready=%v nodes=%s placements=%s\n%s",
				want, waitCtx.Err(), lastErr, last.Health, last.Ready, last.nodeIDs(), last.placementSummary(), dumpLogs(nodes))
		}
	}
}

func assertDemoWeb(t *testing.T, ctx context.Context, baseURL string) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET Grove Shop UI: %v", err)
	}
	defer response.Body.Close()
	page, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	want, err := groveshop.WebAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Equal(page, want) {
		t.Errorf("GET Grove Shop UI = %s; want the embedded index.html", response.Status)
	}

	var configuration groveshop.RuntimeConfigurationView
	if err := getJSON(ctx, baseURL+"/grove/config", &configuration); err != nil {
		t.Fatalf("read Grove Shop runtime configuration: %v", err)
	}
	if configuration.Revision != "acme-r42" || configuration.CustomerName != "Acme Retail" ||
		configuration.ClusterName != "acme-local" || configuration.ReservationBuffer != 100 {
		t.Errorf("runtime configuration = %+v; want the embedded configs/acme.yaml", configuration)
	}
}

// placeDemoOrder creates one order through the ingress and requires it to pass
// through every business stage. Orders can briefly fail while placement
// settles after a membership change, so it retries until the wait expires.
func placeDemoOrder(t *testing.T, ctx context.Context, baseURL, orderID string, nodes []*grovetest.Node) groveshop.Order {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for attempt := 1; ; attempt++ {
		// A failed attempt may have created part of its order, so each attempt
		// uses a fresh order ID.
		body, err := json.Marshal(groveshop.CreateOrderRequest{
			OrderID: fmt.Sprintf("%s-%d", orderID, attempt), SKU: "sku-grove-mug", Quantity: 1,
			AmountCents: 1500, ShippingAddress: "1 Demo Way",
		})
		if err != nil {
			t.Fatal(err)
		}
		requestCtx, requestCancel := context.WithTimeout(waitCtx, 5*time.Second)
		order, err := postDemoOrder(requestCtx, baseURL, body)
		requestCancel()
		if err == nil {
			want := []groveshop.OrderStatus{
				groveshop.OrderCreated, groveshop.OrderReserved, groveshop.OrderPaid, groveshop.OrderShipping, groveshop.OrderCompleted,
			}
			if order.Status != groveshop.OrderCompleted || !slices.Equal(order.History, want) {
				t.Fatalf("order %s = status %s history %v; want %v", orderID, order.Status, order.History, want)
			}
			t.Logf("order %s completed via Orders on %s after %d attempt(s)", order.ID, order.Node, attempt)
			return order
		}
		lastErr = err
		select {
		case <-ticker.C:
		case <-waitCtx.Done():
			t.Fatalf("place order %s: %v\n%s", orderID, lastErr, dumpLogs(nodes))
		}
	}
}

func postDemoOrder(ctx context.Context, baseURL string, body []byte) (groveshop.Order, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/orders", bytes.NewReader(body))
	if err != nil {
		return groveshop.Order{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return groveshop.Order{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		message, _ := io.ReadAll(response.Body)
		return groveshop.Order{}, fmt.Errorf("status %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var order groveshop.Order
	if err := json.NewDecoder(response.Body).Decode(&order); err != nil {
		return groveshop.Order{}, err
	}
	return order, nil
}
