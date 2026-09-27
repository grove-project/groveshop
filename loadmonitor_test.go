package groveshop

import (
	"context"
	"testing"
	"time"
)

type fakeCluster struct {
	status   ClusterStatusView
	snapshot LoadSnapshot
	setCalls []bool
}

func (f *fakeCluster) client() LoadClient {
	return LoadClient{
		Set: func(_ context.Context, running bool) (LoadSnapshot, error) {
			f.setCalls = append(f.setCalls, running)
			f.snapshot.Running = running
			return f.snapshot, nil
		},
		Snapshot: func(_ context.Context, since int64) (LoadSnapshot, error) {
			snapshot := f.snapshot
			snapshot.Samples = nil
			for _, sample := range f.snapshot.Samples {
				if sample.UnixMilli > since {
					snapshot.Samples = append(snapshot.Samples, sample)
				}
			}
			return snapshot, nil
		},
	}
}

func clusterOf(health string, nodes ...string) ClusterStatusView {
	status := ClusterStatusView{Health: health, Ready: true}
	for _, id := range nodes {
		status.Nodes = append(status.Nodes, NodeStatusView{NodeID: id, Health: "healthy"})
	}
	return status
}

func eventKinds(view LoadView) []string {
	kinds := make([]string, len(view.Events))
	for i, event := range view.Events {
		kinds[i] = event.Kind
	}
	return kinds
}

func TestLoadMonitorTimelineSpansTopologyChanges(t *testing.T) {
	cluster := &fakeCluster{status: clusterOf("healthy", "node-1"), snapshot: LoadSnapshot{Instance: "a"}}
	monitor := NewLoadMonitor(cluster.client(), func(context.Context) (ClusterStatusView, error) { return cluster.status, nil })
	clock := time.Unix(1000, 0)
	monitor.now = func() time.Time { return clock }
	poll := func(sampleAt int64, throughput float64) {
		clock = clock.Add(time.Second)
		cluster.snapshot.Samples = append(cluster.snapshot.Samples, LoadSample{UnixMilli: sampleAt, Throughput: throughput})
		cluster.snapshot.Throughput = throughput
		monitor.Poll(context.Background())
	}

	poll(1, 100)
	if _, err := monitor.SetDesired(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	cluster.status = clusterOf("healthy", "node-1", "node-2")
	poll(2, 190)
	cluster.status = clusterOf("degraded", "node-1")
	poll(3, 0)
	cluster.snapshot.Instance = "b" // generator relocated
	cluster.status = clusterOf("healthy", "node-1")
	poll(4, 95)

	view := monitor.View()
	want := []string{"node_joined", "node_lost", "generator_relocated", "recovered"}
	got := eventKinds(view)
	if len(got) != len(want) {
		t.Fatalf("events = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v; want %v", got, want)
		}
	}
	if len(view.History) != 4 || view.History[0].Nodes != 1 || view.History[1].Nodes != 2 || view.History[3].Nodes != 1 {
		t.Fatalf("history did not span topology changes: %+v", view.History)
	}
	if view.RecoveryMillis <= 0 {
		t.Fatalf("recovery duration = %d", view.RecoveryMillis)
	}
}

func TestLoadMonitorReappliesLoadToRelocatedGenerator(t *testing.T) {
	cluster := &fakeCluster{status: clusterOf("healthy", "node-1"), snapshot: LoadSnapshot{Instance: "a"}}
	monitor := NewLoadMonitor(cluster.client(), func(context.Context) (ClusterStatusView, error) { return cluster.status, nil })
	if _, err := monitor.SetDesired(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	cluster.setCalls = nil
	cluster.snapshot = LoadSnapshot{Instance: "b"} // fresh, stopped generator
	monitor.Poll(context.Background())
	if len(cluster.setCalls) != 1 || !cluster.setCalls[0] || !monitor.View().Current.Running {
		t.Fatalf("Set calls = %v; want one ON after relocation", cluster.setCalls)
	}
}
