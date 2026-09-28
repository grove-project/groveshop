package groveshop_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/grove-project/grove"
	"github.com/grove-project/groveshop"
)

// simCluster stands in for Grove in pure-Go simulation tests. Each node has
// the real Crew the station handlers use, calls are round-robined over live
// nodes as Grove's placement does, a killed node's in-flight work fails, and
// capacity reports membership changes after a detection delay, as Grove
// status does. Run inside a synctest bubble, hours of shop time take seconds.
type simCluster struct {
	mu     sync.Mutex
	nodes  []*simNode
	next   int
	detect time.Duration
	// lastLoss is when a node was last killed.
	lastLoss time.Time
}

type simNode struct {
	id       string
	crew     *groveshop.Crew
	ctx      context.Context
	cancel   context.CancelFunc
	alive    bool
	changeAt time.Time // when capacity starts reporting the node's state
}

const simDetectDelay = 3 * time.Second

var rushDemand = groveshop.ShopDemand{Mode: groveshop.DemandRush, PerMinute: 63}

func newSimCluster(nodes int) *simCluster {
	c := &simCluster{detect: simDetectDelay}
	for range nodes {
		c.join()
	}
	return c
}

// join adds a node; it returns the node's ID.
func (c *simCluster) join() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := fmt.Sprintf("node-%d", len(c.nodes)+1)
	ctx, cancel := context.WithCancel(context.Background())
	c.nodes = append(c.nodes, &simNode{id: id, crew: groveshop.NewCrew(id, groveshop.StaffPerNode), ctx: ctx, cancel: cancel, alive: true, changeAt: time.Now()})
	return id
}

// kill stops a node: its in-flight work fails at once, and capacity notices
// after the detection delay.
func (c *simCluster) kill(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, node := range c.nodes {
		if node.id == id && node.alive {
			node.alive = false
			c.lastLoss = time.Now()
			node.changeAt = time.Now().Add(c.detect)
			node.cancel()
		}
	}
}

func (c *simCluster) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, node := range c.nodes {
		node.cancel()
	}
}

func (c *simCluster) work(ctx context.Context, req groveshop.WorkRequest) (groveshop.WorkResult, error) {
	c.mu.Lock()
	var live []*simNode
	for _, node := range c.nodes {
		if node.alive {
			live = append(live, node)
		}
	}
	if len(live) == 0 {
		c.mu.Unlock()
		return groveshop.WorkResult{}, errors.New("no node serves the handler")
	}
	node := live[c.next%len(live)]
	c.next++
	c.mu.Unlock()
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		select {
		case <-node.ctx.Done():
			stop()
		case <-ctx.Done():
		}
	}()
	result, err := node.crew.Work(ctx, req)
	if err != nil && node.ctx.Err() != nil {
		return groveshop.WorkResult{}, fmt.Errorf("node %s lost: %w", node.id, err)
	}
	return result, err
}

func (c *simCluster) capacity(context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	var nodes []string
	for _, node := range c.nodes {
		// A node counts from when it joined until its loss is detected.
		if node.alive && !now.Before(node.changeAt) || !node.alive && now.Before(node.changeAt) {
			nodes = append(nodes, node.id)
		}
	}
	return nodes, nil
}

// simShop runs a real Shop, with its real run loops, against a simCluster.
type simShop struct {
	t       *testing.T
	cluster *simCluster
	shop    *groveshop.Shop
	seen    []groveshop.ShopEvent
	seenSeq int64
	history []groveshop.ShopHistoryPoint
	histSeq int64
	stats   simStats
}

// simStats are invariants and extremes checked on every sampled second.
type simStats struct {
	samples      int
	maxBusy      map[groveshop.Station]int
	maxQueue     map[groveshop.Station]int
	maxWaitMilli int64
}

func startSimShop(t *testing.T, nodes int, seed uint64) *simShop {
	t.Helper()
	cluster := newSimCluster(nodes)
	s := &simShop{t: t, cluster: cluster, stats: simStats{maxBusy: map[groveshop.Station]int{}, maxQueue: map[groveshop.Station]int{}}}
	s.shop = groveshop.NewShop(groveshop.ShopDeps{
		Node:     "node-1",
		Work:     cluster.work,
		Capacity: cluster.capacity,
		Rand:     rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
	})
	// Most scenarios run on the random rush flow at the load the demo had
	// before the flow became configurable.
	if err := s.shop.SetDemand(rushDemand); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.shop.Open()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.shop.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		cluster.stop()
		<-done
		synctest.Wait()
	})
	return s
}

// run advances shop time by d, sampling the shop every second.
func (s *simShop) run(d time.Duration) groveshop.ShopSnapshot {
	s.t.Helper()
	var snapshot groveshop.ShopSnapshot
	for end := time.Now().Add(d); time.Now().Before(end); {
		time.Sleep(time.Second)
		synctest.Wait()
		snapshot = s.sample()
	}
	return snapshot
}

func (s *simShop) sample() groveshop.ShopSnapshot {
	s.t.Helper()
	snapshot := s.shop.Snapshot(s.seenSeq, s.histSeq)
	for _, event := range snapshot.Events {
		s.seen = append(s.seen, event)
		s.seenSeq = event.Seq
	}
	s.history = append(s.history, snapshot.History...)
	if n := len(snapshot.History); n > 0 {
		s.histSeq = snapshot.History[n-1].UnixMilli
	}
	s.stats.samples++
	s.check(snapshot)
	return snapshot
}

// check asserts what must hold at every moment of the business.
func (s *simShop) check(snapshot groveshop.ShopSnapshot) {
	s.t.Helper()
	staff, busy := 0, 0
	for _, station := range snapshot.Stations {
		if station.Staff < 0 || station.Busy < 0 || station.Queue < 0 {
			s.t.Fatalf("%s: negative station state %+v", simClock(), station)
		}
		staff += station.Staff
		busy += station.Busy
		s.stats.maxBusy[station.ID] = max(s.stats.maxBusy[station.ID], station.Busy)
		s.stats.maxQueue[station.ID] = max(s.stats.maxQueue[station.ID], station.Queue)
	}
	if staff != snapshot.StaffTotal {
		s.t.Fatalf("%s: stations have %d staff; shop has %d", simClock(), staff, snapshot.StaffTotal)
	}
	if snapshot.StaffTotal != len(snapshot.Nodes)*groveshop.StaffPerNode {
		s.t.Fatalf("%s: %d staff for %d nodes", simClock(), snapshot.StaffTotal, len(snapshot.Nodes))
	}
	// Until a lost node is noticed, its interrupted work is retried on the
	// remaining staff while the lost staff still count.
	s.cluster.mu.Lock()
	settling := time.Since(s.cluster.lastLoss) < s.cluster.detect+groveshop.MaxWorkDuration+time.Second
	s.cluster.mu.Unlock()
	if busy > snapshot.StaffTotal && !settling {
		s.t.Fatalf("%s: %d busy with %d staff", simClock(), busy, snapshot.StaffTotal)
	}
	for _, stock := range snapshot.Inventory {
		if stock.Level < 0 || stock.Level > stock.Capacity {
			s.t.Fatalf("%s: stock %s at %d of %d", simClock(), stock.ID, stock.Level, stock.Capacity)
		}
	}
	m := snapshot.Metrics
	if arrived := snapshot.Carryover.CustomerSeq; m.Served+m.Left+int64(m.CustomersInside) != arrived {
		s.t.Fatalf("%s: served %d + left %d + inside %d != arrived %d", simClock(), m.Served, m.Left, m.CustomersInside, arrived)
	}
	s.stats.maxWaitMilli = max(s.stats.maxWaitMilli, m.LongestWait)
}

func (s *simShop) count(kind string) int {
	n := 0
	for _, event := range s.seen {
		if event.Kind == kind {
			n++
		}
	}
	return n
}

// simClock is shop time; a synctest bubble starts at midnight.
func simClock() string { return time.Now().Format("15:04:05") }

func (s *simShop) logSummary(label string, snapshot groveshop.ShopSnapshot) {
	m := snapshot.Metrics
	s.t.Logf("%s: nodes=%d staff=%d served=%d left=%d inside=%d avgWait=%.1fs longest=%.1fs work=%d stations=%v",
		label, len(snapshot.Nodes), snapshot.StaffTotal, m.Served, m.Left, m.CustomersInside,
		float64(m.AvgWaitMillis)/1000, float64(m.LongestWait)/1000, m.WorkDone, simStations(snapshot))
}

func simStations(snapshot groveshop.ShopSnapshot) string {
	out := ""
	for _, station := range snapshot.Stations {
		out += fmt.Sprintf(" %s:staff=%d,busy=%d,q=%d", station.ID, station.Staff, station.Busy, station.Queue)
	}
	return out
}

// A full shift on three nodes: every customer is accounted for, staff and
// stock stay consistent, and most customers are served.
func TestShopSimulationSteadyShift(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3} {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sim := startSimShop(t, 3, seed)
				snapshot := sim.run(30 * time.Minute)
				sim.logSummary("after 30m", snapshot)
				m := snapshot.Metrics
				if m.Served < 1000 {
					t.Errorf("served %d customers in 30 minutes", m.Served)
				}
				if left := float64(m.Left) / float64(m.Served+m.Left); left > 0.02 {
					t.Errorf("%.1f%% of customers left unhappy", left*100)
				}
				var peak int64
				for _, point := range sim.history {
					peak = max(peak, point.AvgWaitMillis)
				}
				if peak > (45 * time.Second).Milliseconds() {
					t.Errorf("average wait peaked at %s", time.Duration(peak)*time.Millisecond)
				}
				// Three nodes are enough for normal demand but not for every
				// rush, which is what joining a node in the demo fixes.
				if sim.count("rush") == 0 || sim.count("congested") == 0 {
					t.Errorf("rushes=%d congested=%d; the demo needs visible pressure", sim.count("rush"), sim.count("congested"))
				}
				for _, station := range groveshop.Stations {
					if sim.stats.maxBusy[station] == 0 {
						t.Errorf("%s never worked", station)
					}
				}
				if sim.count("supplier_arrived") == 0 {
					t.Error("no supplier delivered in 30 minutes")
				}
				if sim.count("retry") != 0 {
					t.Errorf("%d retries with no node loss", sim.count("retry"))
				}
			})
		})
	}
}

// The default steady flow is the demo's bottleneck: three nodes fall
// further behind every minute, and joining nodes clears the backlog, faster
// with each node.
func TestShopSimulationSteadyFlow(t *testing.T) {
	cleared := map[int]time.Duration{}
	for _, join := range []int{1, 2} {
		synctest.Test(t, func(t *testing.T) {
			sim := startSimShop(t, 3, 11)
			if err := sim.shop.SetDemand(groveshop.DefaultDemand()); err != nil {
				t.Fatal(err)
			}
			early := sim.run(2 * time.Minute)
			behind := sim.run(4 * time.Minute)
			sim.logSummary("3 nodes", behind)
			if got := behind.Metrics.ArrivalsPerMin; got < groveshop.DefaultArrivalsPerMin-1 || got > groveshop.DefaultArrivalsPerMin+1 {
				t.Errorf("steady flow measured %.1f / min, set %d", got, groveshop.DefaultArrivalsPerMin)
			}
			if sim.count("rush") != 0 {
				t.Errorf("%d rushes in steady flow", sim.count("rush"))
			}
			line := func(s groveshop.ShopSnapshot) int { return s.Stations[0].Queue }
			if line(behind) < 25 || line(behind) <= line(early) {
				t.Errorf("3 nodes kept up: cashier line %d after 2m, %d after 6m", line(early), line(behind))
			}

			for range join {
				sim.cluster.join()
			}
			for elapsed := time.Duration(0); elapsed < 5*time.Minute; elapsed += time.Second {
				if line(sim.run(time.Second)) < 3 && elapsed > 10*time.Second {
					cleared[join] = elapsed
					break
				}
			}
			after := sim.run(3 * time.Minute)
			sim.logSummary(fmt.Sprintf("%d nodes", 3+join), after)
			if cleared[join] == 0 {
				t.Fatalf("%d nodes did not clear the cashier line in 5 minutes", 3+join)
			}
			if line(after) > 5 || after.Metrics.AvgWaitMillis > (15*time.Second).Milliseconds() {
				t.Errorf("%d nodes: line %d, avg wait %s after clearing", 3+join, line(after), time.Duration(after.Metrics.AvgWaitMillis)*time.Millisecond)
			}
		})
	}
	t.Logf("cashier line cleared after: %v", cleared)
	if cleared[2] >= cleared[1] {
		t.Errorf("a fifth node did not clear the line faster: %v", cleared)
	}
}

// Losing a node drops staff and interrupts work, which is retried; nothing
// is stuck, and a replacement node brings the staff back.
func TestShopSimulationNodeLossAndRejoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sim := startSimShop(t, 3, 5)
		before := sim.run(5 * time.Minute)
		sim.logSummary("before loss", before)

		sim.cluster.kill("node-2")
		lost := sim.run(2 * time.Minute)
		sim.logSummary("after loss", lost)
		if lost.StaffTotal != 2*groveshop.StaffPerNode {
			t.Fatalf("staff after node loss = %d", lost.StaffTotal)
		}
		if sim.count("node_lost") != 1 {
			t.Errorf("node_lost events = %d", sim.count("node_lost"))
		}
		if lost.Metrics.Served <= before.Metrics.Served {
			t.Error("shop stopped serving after losing a node")
		}

		sim.cluster.join()
		back := sim.run(3 * time.Minute)
		sim.logSummary("after rejoin", back)
		if back.StaffTotal != 3*groveshop.StaffPerNode {
			t.Fatalf("staff after rejoin = %d", back.StaffTotal)
		}
		if sim.count("node_joined") == 0 {
			t.Error("no node_joined event")
		}
		for _, order := range back.Orders {
			if order.AgeMs > (3 * time.Minute).Milliseconds() {
				t.Errorf("order #%d stuck in %s for %s", order.ID, order.Stage, time.Duration(order.AgeMs)*time.Millisecond)
			}
		}
	})
}

// More nodes mean more staff and shorter waits for the same demand.
func TestShopSimulationScalesWithNodes(t *testing.T) {
	waits := map[int]float64{}
	lefts := map[int]int64{}
	for _, nodes := range []int{1, 2, 4} {
		synctest.Test(t, func(t *testing.T) {
			sim := startSimShop(t, nodes, 9)
			snapshot := sim.run(15 * time.Minute)
			sim.logSummary(fmt.Sprintf("%d nodes", nodes), snapshot)
			var sum float64
			for _, point := range sim.history {
				sum += float64(point.AvgWaitMillis)
			}
			waits[nodes] = sum / float64(max(1, len(sim.history))) / 1000
			lefts[nodes] = snapshot.Metrics.Left
		})
	}
	t.Logf("mean avg wait by nodes: %v; left: %v", waits, lefts)
	if !(waits[1] > waits[2] && waits[2] >= waits[4]) {
		t.Errorf("waits do not shrink with nodes: %v", waits)
	}
	if lefts[1] <= lefts[4] {
		t.Errorf("one node lost no more customers than four: %v", lefts)
	}
}

// With every node gone the shop keeps its customers accounted for: nobody
// is served, customers eventually leave, and service resumes on rejoin.
func TestShopSimulationAllNodesLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sim := startSimShop(t, 2, 13)
		sim.run(2 * time.Minute)
		sim.cluster.kill("node-1")
		sim.cluster.kill("node-2")
		dark := sim.run(3 * time.Minute)
		sim.logSummary("no nodes", dark)
		if dark.StaffTotal != 0 {
			t.Fatalf("staff with no nodes = %d", dark.StaffTotal)
		}
		sim.cluster.join()
		back := sim.run(3 * time.Minute)
		sim.logSummary("rejoined", back)
		if back.Metrics.Served <= dark.Metrics.Served {
			t.Error("shop did not resume serving after a node rejoined")
		}
	})
}

// simLease is a Grove lease whose renewal the test controls.
type simLease struct {
	held     atomic.Bool
	released atomic.Bool
}

func (l *simLease) Held() bool { return l.held.Load() && !l.released.Load() }
func (l *simLease) Release()   { l.released.Store(true) }

type simLeases struct {
	mu     sync.Mutex
	leases []*simLease
	// pending delays the next claim, as Grove waits out a lapsed lease.
	pending time.Duration
}

func (p *simLeases) AcquireExclusive(ctx context.Context, _ string) (grove.Lease, error) {
	p.mu.Lock()
	wait := p.pending
	p.pending = 0
	p.mu.Unlock()
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	lease := &simLease{}
	lease.held.Store(true)
	p.leases = append(p.leases, lease)
	return lease, nil
}

func (p *simLeases) latest() (*simLease, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.leases[len(p.leases)-1], len(p.leases)
}

// When Grove stops renewing the lease, the shop pauses, claims the
// capability again and resumes where it left off.
func TestShopSimulationLeaseLapses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sim := startSimShop(t, 3, 21)
		leases := &simLeases{}
		ctx, cancel := context.WithCancel(grove.WithExclusiveProvider(context.Background(), leases))
		done := make(chan struct{})
		go func() {
			defer close(done)
			sim.shop.RunOwned(ctx)
		}()
		defer func() { cancel(); <-done }()

		opened := sim.run(time.Minute)
		lease, claims := leases.latest()
		if opened.Paused || opened.Metrics.Served == 0 || claims != 1 {
			t.Fatalf("shop not serving on its claim: paused=%v served=%d claims=%d", opened.Paused, opened.Metrics.Served, claims)
		}
		leases.mu.Lock()
		leases.pending = 3 * time.Second // the lapsed lease runs out before a new claim
		leases.mu.Unlock()
		lease.held.Store(false)
		paused := sim.run(2 * time.Second)
		if !paused.Paused || !lease.released.Load() {
			t.Fatalf("lapsed lease: paused=%v released=%v", paused.Paused, lease.released.Load())
		}
		back := sim.run(5 * time.Second)
		if _, claims := leases.latest(); claims != 2 || back.Paused || back.Instance != opened.Instance || back.Metrics.Served < opened.Metrics.Served {
			t.Fatalf("shop not resumed on a new claim: claims=%d paused=%v instance=%s", claims, back.Paused, back.Instance)
		}
		if sim.count("paused") != 1 || sim.count("resumed") != 1 {
			t.Errorf("paused=%d resumed=%d events", sim.count("paused"), sim.count("resumed"))
		}
	})
}
