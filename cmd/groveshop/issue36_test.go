package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grove-project/grove/grovetest"
)

// buildGroveshop builds the Grove Shop artifact from this module (grovetest's
// own BuildGrovlet builds relative to the Grove module, not an application
// module that merely depends on it).
func buildGroveshop(ctx context.Context, t *testing.T) string {
	t.Helper()
	binaryPath := filepath.Join(t.TempDir(), "groveshop")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, "./")
	cmd.Dir = "."
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build Grove Shop: %v\n%s", err, output)
	}
	return binaryPath
}

// A replacement node joining a cluster still degraded from a recent abrupt
// node kill must not collapse cluster-wide throughput: grove#36. Reproduces
// the manual repro from that issue against a real 3-node Grove Shop cluster
// with load running, then verifies throughput recovers once Grove is built
// against the fix (grove#37 / grove main 749aef0 and later).
func TestReplacementJoinDuringDegradedClusterRecovers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()

	binary := buildGroveshop(ctx, t)

	const bootstrapNodes = 3
	// +1 for the web ingress, +1 for the replacement node's own route port.
	ports := reservePorts(t, bootstrapNodes+2)
	webAddress := "127.0.0.1:" + strconv.Itoa(ports[bootstrapNodes])
	replacementPort := ports[bootstrapNodes+1]

	nodes := make([]*grovetest.Node, 0, bootstrapNodes)
	startJoiner := func(nodeID string, port int, seedPort int) *grovetest.Node {
		t.Helper()
		args := []string{
			"--node-id", nodeID,
			"--advertise-endpoint", "nats-subject://system/" + nodeID,
			"--system-nats-listen", "127.0.0.1:0",
			"--system-nats-route-listen", "127.0.0.1:" + strconv.Itoa(port),
			"--system-nats-seed", "nats-route://127.0.0.1:" + strconv.Itoa(seedPort),
			"--system-nats-membership", "--system-nats-recovery", "--system-nats-retire-on-stop",
			"--system-nats-subject", "_GROVE.system.cli." + nodeID,
		}
		node, err := grovetest.StartNode(binary, args...)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = node.Cleanup() })
		return node
	}

	// node-1 founds the cluster and hosts the Web ingress, exactly as in the
	// issue's manual repro ("Node-1 founds the cluster with --ingress-address").
	founder, err := grovetest.StartNode(binary,
		"--node-id", "node-1",
		"--advertise-endpoint", "nats-subject://system/node-1",
		"--system-nats-listen", "127.0.0.1:0",
		"--system-nats-route-listen", "127.0.0.1:"+strconv.Itoa(ports[0]),
		"--system-nats-membership", "--system-nats-recovery", "--system-nats-retire-on-stop",
		"--system-nats-subject", "_GROVE.system.cli.node-1",
		"--ingress-address", webAddress,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = founder.Cleanup() })
	nodes = append(nodes, founder)

	// node-2 and node-3 join with no --component flags: the runtime decides
	// placement (grove#34).
	nodes = append(nodes, startJoiner("node-2", ports[1], ports[0]))
	nodes = append(nodes, startJoiner("node-3", ports[2], ports[0]))

	for _, node := range nodes {
		if err := node.WaitReady(ctx); err != nil {
			t.Fatalf("wait for bootstrap cluster: %v\n%s", err, dumpLogs(nodes))
		}
	}

	baseURL := "http://" + webAddress
	waitForStatusReady(t, ctx, baseURL, nodes)

	// Turn load ON and let it ramp to a stable baseline.
	setLoad(t, ctx, baseURL, true)
	baseline := waitForStableThroughput(t, ctx, baseURL, nodes)
	t.Logf("baseline throughput = %.1f orders/s", baseline)
	if baseline <= 0 {
		t.Fatalf("baseline throughput is not positive\n%s", dumpLogs(nodes))
	}

	// Abruptly kill node-2, exactly like the issue's kill -9 scenario.
	if err := nodes[1].Kill(ctx); err != nil {
		t.Fatal(err)
	}
	killedAt := time.Now()
	t.Logf("killed node-2 at t=0")

	// Join a replacement node while the cluster is still degraded (0s gap,
	// the more severe of the two timings the issue reports).
	replacement := startJoiner("node-4", replacementPort, ports[0])
	nodes = append(nodes, replacement)
	if err := replacement.WaitReady(ctx); err != nil {
		t.Fatalf("replacement node did not become ready: %v\n%s", err, dumpLogs(nodes))
	}
	t.Logf("node-4 joined %.1fs after the kill", time.Since(killedAt).Seconds())

	// The issue's acceptance criterion: throughput returns to at least half of
	// baseline within a bounded time (a kill alone recovers within a couple of
	// seconds; this allows generously more for the join to settle too).
	recovered, elapsed, last := waitForRecovery(t, ctx, baseURL, baseline/2, 30*time.Second)
	if !recovered {
		t.Fatalf(
			"throughput did not recover to >= half of baseline (%.1f) within 30s of the kill; last throughput = %.1f\n%s",
			baseline/2, last, dumpLogs(nodes),
		)
	}
	t.Logf("throughput recovered to %.1f orders/s after %.1fs", last, elapsed.Seconds())
}

func reservePorts(t *testing.T, count int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, count)
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	ports := make([]int, 0, count)
	for range count {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		_, portText, err := net.SplitHostPort(listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			t.Fatal(err)
		}
		ports = append(ports, port)
	}
	return ports
}

type clusterStatus struct {
	Ready bool `json:"ready"`
}

type loadView struct {
	GeneratorAvailable bool `json:"generator_available"`
	Current            struct {
		Running    bool    `json:"running"`
		Completed  int64   `json:"completed"`
		Failed     int64   `json:"failed"`
		Throughput float64 `json:"throughput"`
	} `json:"current"`
}

func getJSON(ctx context.Context, url string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d", url, response.StatusCode)
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func waitForStatusReady(t *testing.T, ctx context.Context, baseURL string, nodes []*grovetest.Node) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var status clusterStatus
		if err := getJSON(ctx, baseURL+"/grove/status", &status); err == nil && status.Ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cluster status did not become ready\n%s", dumpLogs(nodes))
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("cluster status did not become ready: %v\n%s", ctx.Err(), dumpLogs(nodes))
		}
	}
}

func setLoad(t *testing.T, ctx context.Context, baseURL string, running bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastStatus int
	for {
		body := strings.NewReader(fmt.Sprintf(`{"running":%t}`, running))
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/load", body)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			lastStatus = response.StatusCode
			response.Body.Close()
			if lastStatus == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("set load running=%v: status %d (err=%v)", running, lastStatus, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitForStableThroughput polls until the generator is running and its
// throughput has stopped ramping (two consecutive positive readings within
// 25% of each other), returning the stabilized value.
func waitForStableThroughput(t *testing.T, ctx context.Context, baseURL string, nodes []*grovetest.Node) float64 {
	t.Helper()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(30 * time.Second)
	var previous float64
	for {
		var view loadView
		if err := getJSON(ctx, baseURL+"/api/load", &view); err == nil &&
			view.GeneratorAvailable && view.Current.Running && view.Current.Throughput > 0 {
			current := view.Current.Throughput
			if previous > 0 {
				ratio := current / previous
				if ratio > 0.75 && ratio < 1.25 {
					return current
				}
			}
			previous = current
		}
		if time.Now().After(deadline) {
			t.Fatalf("load did not ramp to a stable baseline\n%s", dumpLogs(nodes))
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("load did not ramp to a stable baseline: %v\n%s", ctx.Err(), dumpLogs(nodes))
		}
	}
}

// waitForRecovery polls /api/load until throughput is at least floor, or
// timeout elapses. It returns the last observed throughput either way.
func waitForRecovery(
	t *testing.T, ctx context.Context, baseURL string, floor float64, timeout time.Duration,
) (recovered bool, elapsed time.Duration, last float64) {
	t.Helper()
	start := time.Now()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	deadline := start.Add(timeout)
	for {
		var view loadView
		if err := getJSON(ctx, baseURL+"/api/load", &view); err == nil {
			last = view.Current.Throughput
			t.Logf(
				"t=%.1fs throughput=%.1f completed=%d failed=%d",
				time.Since(start).Seconds(), view.Current.Throughput, view.Current.Completed, view.Current.Failed,
			)
			if view.Current.Throughput >= floor {
				return true, time.Since(start), last
			}
		}
		if time.Now().After(deadline) {
			return false, time.Since(start), last
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return false, time.Since(start), last
		}
	}
}

func dumpLogs(nodes []*grovetest.Node) string {
	var builder strings.Builder
	for _, node := range nodes {
		fmt.Fprintf(&builder, "node %s logs:\n%s\n", node.ID(), node.Logs())
	}
	return builder.String()
}
