package groveshop

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"
)

const (
	loadMonitorInterval = time.Second
	loadMaxHistory      = 1800
	loadMaxEvents       = 100
)

// LoadClient reaches the cluster-wide load generator through Grove.
type LoadClient struct {
	// Set turns generated load ON or OFF.
	Set func(ctx context.Context, running bool) (LoadSnapshot, error)
	// Snapshot returns generator state and samples newer than since (unix ms).
	Snapshot func(ctx context.Context, since int64) (LoadSnapshot, error)
}

// TimelineEvent is a cluster or application event annotated on the timeline.
type TimelineEvent struct {
	UnixMilli int64  `json:"t"`
	Kind      string `json:"kind"`
	Message   string `json:"message"`
}

// LoadPoint is one timeline sample together with the cluster size at the time.
type LoadPoint struct {
	LoadSample
	Nodes int `json:"nodes"`
}

// LoadView is the browser's read model for the load/scaling/recovery demo.
type LoadView struct {
	// Desired is the presenter's Load ON/OFF choice; the monitor re-applies it
	// to a relocated generator.
	Desired bool `json:"desired"`
	// GeneratorAvailable is false while the generator is unreachable, for
	// example during relocation.
	GeneratorAvailable bool `json:"generator_available"`
	// GeneratorNode is the node currently hosting the generator.
	GeneratorNode string       `json:"generator_node"`
	Nodes         int          `json:"nodes"`
	Current       LoadSnapshot `json:"current"`
	// CompletionRate is completed / (completed + failed) since generator start.
	CompletionRate float64 `json:"completion_rate"`
	// RecoveryMillis is the duration of the most recent node-loss recovery.
	RecoveryMillis int64           `json:"recovery_ms"`
	History        []LoadPoint     `json:"history"`
	Events         []TimelineEvent `json:"events"`
	Error          string          `json:"error,omitempty"`
}

// LoadMonitor keeps one continuous performance timeline across topology
// changes. It polls the load generator and the Grove status read model,
// annotates node and generator events, and re-applies the presenter's Load
// ON/OFF choice when the generator is relocated.
type LoadMonitor struct {
	load       LoadClient
	readStatus StatusReader
	now        func() time.Time

	mu           sync.Mutex
	desired      bool
	seenSnapshot bool
	instance     string
	lastSample   int64
	nodes        map[string]bool
	nodesKnown   bool
	generator    string
	lostAt       time.Time
	preLoss      float64
	recoveryMS   int64
	view         LoadView
}

// NewLoadMonitor creates a monitor over load and readStatus.
func NewLoadMonitor(load LoadClient, readStatus StatusReader) *LoadMonitor {
	return &LoadMonitor{load: load, readStatus: readStatus, now: time.Now}
}

// Run polls until ctx ends.
func (m *LoadMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(loadMonitorInterval)
	defer ticker.Stop()
	for {
		m.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SetDesired records and applies the presenter's Load ON/OFF choice.
func (m *LoadMonitor) SetDesired(ctx context.Context, running bool) (LoadView, error) {
	m.mu.Lock()
	m.desired = running
	m.view.Desired = running
	m.mu.Unlock()
	if _, err := m.load.Set(ctx, running); err != nil {
		return m.View(), err
	}
	m.Poll(ctx)
	return m.View(), nil
}

// View returns a copy of the current read model.
func (m *LoadMonitor) View() LoadView {
	m.mu.Lock()
	defer m.mu.Unlock()
	view := m.view
	view.History = slices.Clone(view.History)
	view.Events = slices.Clone(view.Events)
	return view
}

// Poll refreshes topology, generator state, and timeline once.
func (m *LoadMonitor) Poll(ctx context.Context) {
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status, statusErr := m.readStatus(callCtx)

	m.mu.Lock()
	since := m.lastSample
	m.mu.Unlock()
	snapshot, snapshotErr := m.load.Snapshot(callCtx, since)

	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.view.Error = ""
	if statusErr == nil {
		m.observeTopology(now, status)
	} else {
		m.view.Error = "cluster status: " + statusErr.Error()
	}
	m.view.GeneratorAvailable = snapshotErr == nil
	if snapshotErr != nil {
		m.view.Error = "load generator unavailable: " + snapshotErr.Error()
		m.view.Desired = m.desired
		return
	}
	m.observeGenerator(now, snapshot)
	if m.desired && !snapshot.Running {
		// Re-apply ON to a relocated generator; a failure is retried next poll.
		m.mu.Unlock()
		if applied, err := m.load.Set(callCtx, true); err == nil {
			snapshot = applied
		}
		m.mu.Lock()
	}
	m.view.Desired = m.desired
	m.view.Current = snapshot
	m.view.Current.Samples = nil
	m.view.Nodes = m.healthyCount()
	m.view.GeneratorNode = m.generator
	m.view.RecoveryMillis = m.recoveryMS
	if total := snapshot.Completed + snapshot.Failed; total > 0 {
		m.view.CompletionRate = float64(snapshot.Completed) / float64(total)
	}
	for _, sample := range snapshot.Samples {
		if sample.UnixMilli <= m.lastSample {
			continue
		}
		m.lastSample = sample.UnixMilli
		m.view.History = append(m.view.History, LoadPoint{LoadSample: sample, Nodes: m.healthyCount()})
	}
	if len(m.view.History) > loadMaxHistory {
		m.view.History = slices.Clone(m.view.History[len(m.view.History)-loadMaxHistory:])
	}
	m.checkRecovered(now, status, statusErr)
}

func (m *LoadMonitor) healthyCount() int {
	count := 0
	for _, healthy := range m.nodes {
		if healthy {
			count++
		}
	}
	return count
}

func (m *LoadMonitor) observeTopology(now time.Time, status ClusterStatusView) {
	current := make(map[string]bool, len(status.Nodes))
	for _, node := range status.Nodes {
		current[node.NodeID] = node.Health == "healthy"
	}
	if m.nodesKnown {
		for _, id := range sortedKeys(current) {
			if current[id] && !m.nodes[id] {
				m.addEvent(now, "node_joined", id+" joined the cluster")
			}
		}
		for _, id := range sortedKeys(m.nodes) {
			if m.nodes[id] && !current[id] {
				m.addEvent(now, "node_lost", id+" left or was lost")
				if m.lostAt.IsZero() {
					m.lostAt = now
					m.preLoss = m.view.Current.Throughput
				}
			}
		}
	}
	m.nodes, m.nodesKnown = current, true
}

func (m *LoadMonitor) observeGenerator(now time.Time, snapshot LoadSnapshot) {
	if !m.seenSnapshot {
		m.seenSnapshot = true
		m.desired = m.desired || snapshot.Running
	} else if snapshot.Instance != m.instance {
		where := snapshot.Node
		if where == "" {
			where = "another node"
		}
		m.addEvent(now, "generator_relocated", "load generator relocated to "+where)
		m.lastSample = 0
	}
	m.instance = snapshot.Instance
	m.generator = snapshot.Node
}

// checkRecovered closes an open node-loss window once the control plane is
// ready again and, when load is on, orders are completing at no less than half
// the pre-loss rate. It deliberately does not wait for the cluster to report
// healthy: a cluster that lost a node stays degraded until the node returns.
func (m *LoadMonitor) checkRecovered(now time.Time, status ClusterStatusView, statusErr error) {
	if m.lostAt.IsZero() || statusErr != nil || !status.Ready {
		return
	}
	if m.desired && (m.view.Current.Throughput <= 0 || m.view.Current.Throughput < m.preLoss/2) {
		return
	}
	m.recoveryMS = now.Sub(m.lostAt).Milliseconds()
	m.view.RecoveryMillis = m.recoveryMS
	m.addEvent(now, "recovered", fmt.Sprintf("workload recovered after %.1fs", now.Sub(m.lostAt).Seconds()))
	m.lostAt = time.Time{}
}

func (m *LoadMonitor) addEvent(now time.Time, kind, message string) {
	m.view.Events = append(m.view.Events, TimelineEvent{UnixMilli: now.UnixMilli(), Kind: kind, Message: message})
	if len(m.view.Events) > loadMaxEvents {
		m.view.Events = slices.Clone(m.view.Events[len(m.view.Events)-loadMaxEvents:])
	}
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
