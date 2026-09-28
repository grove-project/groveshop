package groveshop

import (
	"context"
	"slices"
	"sync"
	"time"
)

const (
	shopMonitorInterval = 500 * time.Millisecond
	shopViewEvents      = 200
	shopViewHistory     = 600
)

// ShopClient reaches the cluster-wide Shop through Grove.
type ShopClient func(context.Context, ShopRequest) (ShopSnapshot, error)

// ShopView is the browser's read model of the coffee shop.
type ShopView struct {
	ShopSnapshot
	// Available is false while the Shop cannot be reached, for example
	// while Grove relocates it after its node was lost.
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

// ShopMonitor keeps one continuous view of the shop for the browser. It
// polls the Shop, accumulates its activity stream and history across Shop
// relocations, and hands a relocated Shop the carryover of the one it
// replaced.
type ShopMonitor struct {
	call ShopClient

	mu        sync.Mutex
	instance  string
	sinceSeq  int64
	sinceHist int64
	carryover *ShopCarryover
	events    []ShopEvent
	history   []ShopHistoryPoint
	view      ShopView
}

// NewShopMonitor creates a monitor over call.
func NewShopMonitor(call ShopClient) *ShopMonitor {
	return &ShopMonitor{call: call}
}

// Run polls until ctx ends.
func (m *ShopMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(shopMonitorInterval)
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

// View returns a copy of the current read model.
func (m *ShopMonitor) View() ShopView {
	m.mu.Lock()
	defer m.mu.Unlock()
	view := m.view
	view.Events = slices.Clone(m.events)
	view.History = slices.Clone(m.history)
	if view.Events == nil {
		view.Events = []ShopEvent{}
	}
	if view.History == nil {
		view.History = []ShopHistoryPoint{}
	}
	return view
}

// Poll refreshes the view once.
func (m *ShopMonitor) Poll(ctx context.Context) {
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	m.mu.Lock()
	request := ShopRequest{SinceEvent: m.sinceSeq, SinceHistory: m.sinceHist}
	instance := m.instance
	m.mu.Unlock()

	snapshot, err := m.call(callCtx, request)
	if err == nil && instance != "" && snapshot.Instance != instance {
		// Grove moved the Shop: read the new one from the start and hand it
		// the business state of the one it replaced.
		m.mu.Lock()
		carryover := m.carryover
		m.mu.Unlock()
		snapshot, err = m.call(callCtx, ShopRequest{Restore: carryover})
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.view.Available = false
		m.view.Error = "shop unavailable: " + err.Error()
		return
	}
	if snapshot.Instance != m.instance {
		m.instance = snapshot.Instance
		m.sinceSeq = 0
	}
	for _, event := range snapshot.Events {
		if event.Seq > m.sinceSeq {
			m.sinceSeq = event.Seq
		}
		m.events = append(m.events, event)
	}
	if len(m.events) > shopViewEvents {
		m.events = slices.Clone(m.events[len(m.events)-shopViewEvents:])
	}
	for _, point := range snapshot.History {
		if point.UnixMilli <= m.sinceHist {
			continue
		}
		m.sinceHist = point.UnixMilli
		m.history = append(m.history, point)
	}
	if len(m.history) > shopViewHistory {
		m.history = slices.Clone(m.history[len(m.history)-shopViewHistory:])
	}
	carryover := snapshot.Carryover
	m.carryover = &carryover
	snapshot.Events, snapshot.History = nil, nil
	m.view = ShopView{ShopSnapshot: snapshot, Available: true}
}
