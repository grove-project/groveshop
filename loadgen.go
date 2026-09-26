package groveshop

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/grove-project/grove"
)

// LoadGenCapability is the exclusive cluster-wide capability owned by the one
// active load generator.
const LoadGenCapability = "groveshop/load-generator"

const (
	loadTick          = 500 * time.Millisecond
	loadOrderTimeout  = 5 * time.Second
	loadMaxSamples    = 1200
	loadMinWorkers    = 1
	loadMaxWorkers    = 512
	loadProbeInterval = 6
)

// LoadSample is one measured interval of generated application traffic.
type LoadSample struct {
	// UnixMilli is the end of the measured interval.
	UnixMilli int64 `json:"t"`
	// Throughput is completed orders per second during the interval.
	Throughput float64 `json:"throughput"`
	// P50Millis and P95Millis are order latency percentiles in milliseconds.
	P50Millis float64 `json:"p50_ms"`
	P95Millis float64 `json:"p95_ms"`
	// InFlight is the number of orders in progress when the interval ended.
	InFlight int `json:"in_flight"`
	// Concurrency is the generated load level in effect for the interval.
	Concurrency int `json:"concurrency"`
	// Failed is the number of orders that failed during the interval.
	Failed int `json:"failed"`
	// Running reports whether the generator was on.
	Running bool `json:"running"`
}

// LoadSnapshot is the current state of the cluster-wide load generator.
type LoadSnapshot struct {
	// Instance identifies this generator process incarnation. A change means
	// Grove relocated the generator.
	Instance string `json:"instance"`
	// Node is the Grove node hosting the owning generator.
	Node string `json:"node"`
	// Running reports whether generated load is ON.
	Running     bool    `json:"running"`
	Concurrency int     `json:"concurrency"`
	InFlight    int     `json:"in_flight"`
	Completed   int64   `json:"completed"`
	Failed      int64   `json:"failed"`
	Throughput  float64 `json:"throughput"`
	P50Millis   float64 `json:"p50_ms"`
	P95Millis   float64 `json:"p95_ms"`
	// ErrorRate is failed / (completed + failed) since the generator started.
	ErrorRate float64 `json:"error_rate"`
	// Distribution counts completed orders by workflow stage and the Grove
	// node that handled that stage: stage -> node -> orders.
	Distribution map[string]map[string]int64 `json:"distribution"`
	// Samples is the recent measurement history, oldest first.
	Samples []LoadSample `json:"samples"`
}

// LoadRequest is the single call served by the exclusive load-generator
// handler. When Apply is set it first turns load ON or OFF per Running; it
// always returns the snapshot with samples newer than SinceUnixMilli.
type LoadRequest struct {
	Apply          bool
	Running        bool
	SinceUnixMilli int64
}

// OrderSubmitter runs one complete order through the normal Groveshop path.
type OrderSubmitter func(context.Context, CreateOrderRequest) (Order, error)

// LoadGenerator is the Groveshop backend load generator. It drives complete
// order flows through the normal Orders service and adapts the number of
// concurrent flows to saturate whatever cluster capacity is available. Run it
// as a single Grove component so exactly one instance exists cluster-wide.
type LoadGenerator struct {
	submit   OrderSubmitter
	instance string
	node     string
	tick     time.Duration

	mu         sync.Mutex
	enabled    bool
	running    bool
	target     int
	workers    int
	inFlight   int
	completed  int64
	failed     int64
	seq        int64
	tickDone   int
	tickFailed int
	latencies  []time.Duration
	last       LoadSample
	samples    []LoadSample
	control    controller
	dist       map[string]map[string]int64
}

// NewLoadGenerator creates a stopped generator that submits orders with submit.
// node labels the hosting Grove node in snapshots.
func NewLoadGenerator(node string, submit OrderSubmitter) *LoadGenerator {
	return &LoadGenerator{
		submit:   submit,
		node:     node,
		enabled:  true,
		instance: fmt.Sprintf("%d", time.Now().UnixNano()),
		tick:     loadTick,
		control:  newController(),
		dist:     map[string]map[string]int64{},
	}
}

// Run measures and adapts load until ctx ends.
func (g *LoadGenerator) Run(ctx context.Context) {
	ticker := time.NewTicker(g.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			g.step(ctx, now)
		}
	}
}

// SetEnabled records whether this instance currently owns the cluster-wide
// load-generator capability. Losing ownership stops generation immediately, so
// a stale owner never keeps driving load; the new owner starts stopped.
func (g *LoadGenerator) SetEnabled(enabled bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.enabled = enabled
	if !enabled {
		g.running = false
		g.target = 0
	}
}

// Enabled reports whether this instance currently owns the capability.
func (g *LoadGenerator) Enabled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.enabled
}

// RunOwned claims the exclusive load-generator capability through Grove and
// keeps the generator enabled only while this instance holds it. It blocks
// until ctx ends.
func (g *LoadGenerator) RunOwned(ctx context.Context) {
	for ctx.Err() == nil {
		ownership := grove.Exclusive(ctx, LoadGenCapability)
		g.SetEnabled(ownership.Enabled())
		for ownership.Enabled() {
			sleepContext(ctx, 50*time.Millisecond)
		}
		g.SetEnabled(false)
		ownership.Release()
		sleepContext(ctx, 100*time.Millisecond)
	}
}

func sleepContext(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// SetRunning turns generated load ON or OFF. It has no effect unless this
// instance owns the capability.
func (g *LoadGenerator) SetRunning(ctx context.Context, on bool) {
	g.mu.Lock()
	if !g.enabled || on == g.running {
		g.mu.Unlock()
		return
	}
	g.running = on
	if on {
		g.control = newController()
		g.target = g.control.level
	} else {
		g.target = 0
	}
	g.spawnLocked(ctx)
	g.mu.Unlock()
}

// Snapshot returns current state and samples newer than since (unix ms).
func (g *LoadGenerator) Snapshot(since int64) LoadSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	snapshot := LoadSnapshot{
		Instance:    g.instance,
		Node:        g.node,
		Running:     g.running,
		Concurrency: g.target,
		InFlight:    g.inFlight,
		Completed:   g.completed,
		Failed:      g.failed,
		Throughput:  g.last.Throughput,
		P50Millis:   g.last.P50Millis,
		P95Millis:   g.last.P95Millis,
	}
	if total := g.completed + g.failed; total > 0 {
		snapshot.ErrorRate = float64(g.failed) / float64(total)
	}
	snapshot.Distribution = make(map[string]map[string]int64, len(g.dist))
	for stage, nodes := range g.dist {
		snapshot.Distribution[stage] = maps.Clone(nodes)
	}
	for _, sample := range g.samples {
		if sample.UnixMilli > since {
			snapshot.Samples = append(snapshot.Samples, sample)
		}
	}
	return snapshot
}

func (g *LoadGenerator) step(ctx context.Context, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	seconds := g.tick.Seconds()
	latencies := g.latencies
	g.latencies = nil
	sample := LoadSample{
		UnixMilli:   now.UnixMilli(),
		Throughput:  float64(g.tickDone) / seconds,
		P50Millis:   percentileMillis(latencies, 0.50),
		P95Millis:   percentileMillis(latencies, 0.95),
		InFlight:    g.inFlight,
		Concurrency: g.target,
		Failed:      g.tickFailed,
		Running:     g.running,
	}
	done, failed := g.tickDone, g.tickFailed
	g.tickDone, g.tickFailed = 0, 0
	g.last = sample
	g.samples = append(g.samples, sample)
	if len(g.samples) > loadMaxSamples {
		g.samples = slices.Clone(g.samples[len(g.samples)-loadMaxSamples:])
	}
	if !g.running {
		return
	}
	g.target = g.control.next(observation{
		throughput: sample.Throughput,
		p50:        sample.P50Millis,
		p95:        sample.P95Millis,
		completed:  done,
		failed:     failed,
	})
	g.spawnLocked(ctx)
}

func (g *LoadGenerator) spawnLocked(ctx context.Context) {
	for g.workers < g.target {
		g.workers++
		go g.worker(ctx)
	}
}

func (g *LoadGenerator) worker(ctx context.Context) {
	for {
		g.mu.Lock()
		if !g.running || !g.enabled || g.workers > g.target || ctx.Err() != nil {
			g.workers--
			g.mu.Unlock()
			return
		}
		g.seq++
		request := loadOrder(g.instance, g.seq)
		g.inFlight++
		g.mu.Unlock()

		started := time.Now()
		callCtx, cancel := context.WithTimeout(ctx, loadOrderTimeout)
		order, err := g.submit(callCtx, request)
		cancel()
		elapsed := time.Since(started)

		g.mu.Lock()
		g.inFlight--
		if err != nil || order.Status != OrderCompleted {
			g.failed++
			g.tickFailed++
			g.mu.Unlock()
			// Back off briefly so a dead cluster is not spun against.
			select {
			case <-time.After(50 * time.Millisecond):
			case <-ctx.Done():
			}
			continue
		}
		g.completed++
		g.tickDone++
		g.countStage("orders", order.Node)
		g.countStage("inventory", order.Reservation.Node)
		g.countStage("payment", order.Payment.Node)
		g.countStage("shipping", order.Shipment.Node)
		g.latencies = append(g.latencies, elapsed)
		g.mu.Unlock()
	}
}

// countStage records which node handled a stage; g.mu must be held.
func (g *LoadGenerator) countStage(stage, node string) {
	if node == "" {
		node = "unknown"
	}
	if g.dist[stage] == nil {
		g.dist[stage] = map[string]int64{}
	}
	g.dist[stage][node]++
}

var loadSKUs = []string{"coffee-beans", "tea-leaves", "cocoa-nibs", "oat-milk"}

func loadOrder(instance string, seq int64) CreateOrderRequest {
	return CreateOrderRequest{
		OrderID:         fmt.Sprintf("load-%s-%d", instance, seq),
		SKU:             loadSKUs[seq%int64(len(loadSKUs))],
		Quantity:        1 + int(seq%3),
		AmountCents:     500 + int(seq%20)*100,
		ShippingAddress: "31 Grove Lane",
	}
}

func percentileMillis(values []time.Duration, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	index := int(quantile * float64(len(sorted)-1))
	return float64(sorted[index]) / float64(time.Millisecond)
}

type observation struct {
	throughput float64
	p50, p95   float64
	completed  int
	failed     int
}

// controller is a deterministic hill-climbing controller over the number of
// concurrent order flows. It climbs while added concurrency yields more
// throughput, holds at the knee, backs off when latency or errors show
// queueing or capacity loss, and periodically probes upward so newly added
// capacity is discovered without any configured target.
type controller struct {
	level      int
	prevLevel  int
	lastThr    float64
	climbing   bool
	holdTicks  int
	minP50     float64
	settleTick int
}

func newController() controller {
	return controller{level: 2, prevLevel: 2, climbing: true}
}

func (c *controller) next(o observation) int {
	// minP50 is the uncongested latency; it drifts up slowly so a permanent
	// latency shift (fewer nodes, more hops) is not mistaken for queueing.
	c.minP50 *= 1.003
	if o.completed > 0 && (c.minP50 == 0 || o.p50 < c.minP50) {
		c.minP50 = o.p50
	}
	// Ignore the first interval after a level change: workers are still
	// ramping and the measurement does not reflect the new level.
	if c.settleTick > 0 {
		c.settleTick--
		return c.level
	}
	errorRate := 0.0
	if total := o.completed + o.failed; total > 0 {
		errorRate = float64(o.failed) / float64(total)
	}
	limit := max(c.minP50*8, 50)
	if errorRate > 0.05 || (o.completed > 0 && o.p95 > limit) {
		return c.set(max(loadMinWorkers, c.level*7/10), false, o.throughput)
	}
	if o.completed > 0 && o.p50 > c.minP50*1.25 && c.level > loadMinWorkers {
		// Latency well above uncongested means work is queueing: there is
		// more concurrency than capacity, so ease off.
		return c.set(max(loadMinWorkers, c.level*85/100), false, o.throughput)
	}
	if c.climbing {
		if c.lastThr == 0 || o.throughput >= c.lastThr*1.05 {
			return c.set(min(loadMaxWorkers, c.level+max(1, c.level/4)), true, o.throughput)
		}
		return c.set(c.prevLevel, false, o.throughput)
	}
	c.holdTicks++
	if c.holdTicks >= loadProbeInterval {
		return c.set(min(loadMaxWorkers, c.level+max(1, c.level/10)), true, o.throughput)
	}
	if o.throughput < c.lastThr*0.8 {
		// Capacity dropped without latency or errors: re-baseline and probe.
		c.lastThr = o.throughput
	}
	return c.level
}

func (c *controller) set(level int, climbing bool, throughput float64) int {
	if level != c.level {
		c.prevLevel = c.level
		c.settleTick = 1
	}
	c.level = level
	c.climbing = climbing
	c.holdTicks = 0
	c.lastThr = throughput
	return c.level
}
