package groveshop

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/grove-project/grove"
)

// ShopCapability is the exclusive cluster-wide capability owned by the one
// node running the coffee-shop simulation.
const ShopCapability = "groveshop/shop"

const (
	shopTick           = 100 * time.Millisecond
	shopCapacityEvery  = time.Second
	shopShiftEvery     = 1500 * time.Millisecond
	shopMaxEvents      = 300
	shopMaxHistory     = 600
	shopMaxOrdersShown = 40
	shopRateWindow     = time.Minute
	shopMixWindow      = 5 * time.Minute
	shopWaitWindow     = 2 * time.Minute
	shopRetryBackoff   = 250 * time.Millisecond
	shopCarryoverLimit = time.Minute
	shopResumeWithin   = 30 * time.Second
)

// ItemKind separates drinks, made by baristas, from food, made in the kitchen.
type ItemKind string

const (
	// KindDrink items are prepared at the Barista station.
	KindDrink ItemKind = "drink"
	// KindFood items are prepared at the Kitchen station.
	KindFood ItemKind = "food"
)

// MenuItem is one product customers can order.
type MenuItem struct {
	Name   string
	Kind   ItemKind
	Emoji  string
	Uses   map[string]int
	Weight float64
	// MinMillis and MaxMillis bound the random preparation time.
	MinMillis, MaxMillis int
}

// Menu is everything Grove Coffee sells.
var Menu = []MenuItem{
	{Name: "Espresso", Kind: KindDrink, Emoji: "☕", Uses: map[string]int{"beans": 1}, Weight: 10, MinMillis: 1200, MaxMillis: 2000},
	{Name: "Americano", Kind: KindDrink, Emoji: "☕", Uses: map[string]int{"beans": 1}, Weight: 15, MinMillis: 1400, MaxMillis: 2400},
	{Name: "Latte", Kind: KindDrink, Emoji: "☕", Uses: map[string]int{"beans": 1, "milk": 1}, Weight: 25, MinMillis: 1900, MaxMillis: 3200},
	{Name: "Cappuccino", Kind: KindDrink, Emoji: "☕", Uses: map[string]int{"beans": 1, "milk": 1}, Weight: 20, MinMillis: 1900, MaxMillis: 3200},
	{Name: "Flat White", Kind: KindDrink, Emoji: "☕", Uses: map[string]int{"beans": 2, "milk": 1}, Weight: 12, MinMillis: 1800, MaxMillis: 3000},
	{Name: "Oat Latte", Kind: KindDrink, Emoji: "☕", Uses: map[string]int{"beans": 1, "oat_milk": 1}, Weight: 12, MinMillis: 1900, MaxMillis: 3200},
	{Name: "Croissant", Kind: KindFood, Emoji: "🥐", Uses: map[string]int{"pastries": 1}, Weight: 30, MinMillis: 1500, MaxMillis: 2600},
	{Name: "Muffin", Kind: KindFood, Emoji: "🧁", Uses: map[string]int{"pastries": 1}, Weight: 20, MinMillis: 1400, MaxMillis: 2400},
	{Name: "Sandwich", Kind: KindFood, Emoji: "🥪", Uses: map[string]int{"sandwiches": 1}, Weight: 30, MinMillis: 2600, MaxMillis: 3800},
	{Name: "Toastie", Kind: KindFood, Emoji: "🥪", Uses: map[string]int{"sandwiches": 1}, Weight: 20, MinMillis: 2400, MaxMillis: 3600},
}

const cashierMinMillis, cashierMaxMillis = 700, 1500

type stockSpec struct {
	id, label string
	capacity  int
}

var stockSpecs = []stockSpec{
	{"beans", "Coffee beans", 1400},
	{"milk", "Milk", 330},
	{"oat_milk", "Oat milk", 150},
	{"pastries", "Pastries", 140},
	{"sandwiches", "Sandwiches", 110},
}

type supplierSpec struct {
	name     string
	products []string
}

var supplierSpecs = []supplierSpec{
	{"Roastery", []string{"beans"}},
	{"Dairy Co.", []string{"milk", "oat_milk"}},
	{"Fresh Foods", []string{"pastries", "sandwiches"}},
}

const (
	// reorderBelow is the stock fraction at which a supplier is dispatched.
	reorderBelow  = 0.4
	lowBelow      = 0.25
	criticalBelow = 0.10
)

// ShopDeps connects the simulation to Grove.
type ShopDeps struct {
	// Node is the Grove node hosting this Shop instance.
	Node string
	// Work runs one piece of station work through Grove, which picks the
	// node whose staff does it.
	Work func(context.Context, WorkRequest) (WorkResult, error)
	// Capacity returns the healthy Grove nodes hosting the station handlers.
	// Every such node contributes StaffPerNode staff.
	Capacity func(context.Context) ([]string, error)
	// Now and Rand default to wall-clock time and a random seed.
	Now  func() time.Time
	Rand *rand.Rand
}

// ShopRequest is the single call served by the exclusive Shop handler.
type ShopRequest struct {
	// SinceEvent and SinceHistory select events and history newer than the
	// caller already has.
	SinceEvent   int64
	SinceHistory int64
	// Restore, when set, hands a freshly opened Shop the state of the Shop
	// it replaces after Grove relocated it.
	Restore *ShopCarryover
}

// ShopCarryover is the business state that survives the Shop moving to
// another node. Customers in the shop at the time are lost.
type ShopCarryover struct {
	Served      int64          `json:"served"`
	Left        int64          `json:"left"`
	CustomerSeq int64          `json:"customer_seq"`
	OrderSeq    int64          `json:"order_seq"`
	Inventory   map[string]int `json:"inventory"`
	Nodes       []string       `json:"nodes"`
}

// ShopEvent is one line in the live activity stream.
type ShopEvent struct {
	Seq       int64 `json:"seq"`
	UnixMilli int64 `json:"t"`
	// Level is quiet for routine activity, notice for operationally
	// interesting moments, good for recoveries and alert for losses.
	Level string `json:"level"`
	// Kind tags events the UI reacts to, such as node_joined and node_lost.
	Kind string `json:"kind,omitempty"`
	Icon string `json:"icon"`
	Text string `json:"text"`
}

// ShopHistoryPoint is one per-second sample of the shop.
type ShopHistoryPoint struct {
	UnixMilli     int64   `json:"t"`
	CashierQueue  int     `json:"cashier_q"`
	BaristaQueue  int     `json:"barista_q"`
	KitchenQueue  int     `json:"kitchen_q"`
	AvgWaitMillis int64   `json:"avg_wait_ms"`
	ArrivalsMin   float64 `json:"arrivals_min"`
	Staff         int     `json:"staff"`
	Nodes         int     `json:"nodes"`
}

// StationView is one station card.
type StationView struct {
	ID          Station `json:"id"`
	Staff       int     `json:"staff"`
	Busy        int     `json:"busy"`
	Queue       int     `json:"queue"`
	OldestWait  int64   `json:"oldest_wait_ms"`
	Utilization float64 `json:"utilization"`
	PerMinute   float64 `json:"per_min"`
	Congested   bool    `json:"congested"`
	// ByNode counts the station's work completed in the last minute by the
	// Grove node whose staff did it.
	ByNode map[string]int `json:"by_node"`
}

// InventoryView is one stock bar.
type InventoryView struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Level    int    `json:"level"`
	Capacity int    `json:"capacity"`
	// State is ok, low, critical or out.
	State string `json:"state"`
	// DepletionMillis estimates when stock runs out at the recent rate; 0
	// means not depleting.
	DepletionMillis int64 `json:"depletion_ms"`
}

// SupplierView is one supplier's delivery status.
type SupplierView struct {
	Name     string         `json:"name"`
	State    string         `json:"state"`
	ETAMilli int64          `json:"eta_ms,omitempty"`
	Delayed  bool           `json:"delayed"`
	DelayMs  int64          `json:"delay_ms,omitempty"`
	Bringing map[string]int `json:"bringing,omitempty"`
}

// OrderItemView is one item of an active order.
type OrderItemView struct {
	Name  string   `json:"name"`
	Kind  ItemKind `json:"kind"`
	Emoji string   `json:"emoji"`
	// State is queued, preparing or ready.
	State    string  `json:"state"`
	Progress float64 `json:"progress"`
	Node     string  `json:"node,omitempty"`
}

// OrderView is one active order.
type OrderView struct {
	ID       int64           `json:"id"`
	Customer int64           `json:"customer"`
	Stage    string          `json:"stage"`
	AgeMs    int64           `json:"age_ms"`
	Items    []OrderItemView `json:"items"`
}

// ShopMetrics are the business-level numbers of the shop.
type ShopMetrics struct {
	CustomersInside int   `json:"customers_inside"`
	ActiveOrders    int   `json:"active_orders"`
	AvgWaitMillis   int64 `json:"avg_wait_ms"`
	LongestWait     int64 `json:"longest_wait_ms"`
	Served          int64 `json:"served"`
	// WorkDone counts every piece of station work completed since the shop
	// opened.
	WorkDone       int64   `json:"work_done"`
	Left           int64   `json:"left"`
	ArrivalsPerMin float64 `json:"arrivals_min"`
	DrinksPerMin   float64 `json:"drinks_min"`
	FoodPerMin     float64 `json:"food_min"`
	// DrinkShare is the share of items ordered in the last five minutes that
	// were drinks.
	DrinkShare float64 `json:"drink_share"`
	Rush       bool    `json:"rush"`
}

// ShopSnapshot is the complete observable state of the shop.
type ShopSnapshot struct {
	Instance string `json:"instance"`
	// Paused is set while the shop waits to confirm it still owns the
	// capability.
	Paused       bool               `json:"paused"`
	Node         string             `json:"node"`
	OpenedMilli  int64              `json:"opened_ms"`
	NowMilli     int64              `json:"now_ms"`
	Nodes        []string           `json:"nodes"`
	StaffPerNode int                `json:"staff_per_node"`
	StaffTotal   int                `json:"staff_total"`
	Stations     []StationView      `json:"stations"`
	Bottleneck   Station            `json:"bottleneck,omitempty"`
	Metrics      ShopMetrics        `json:"metrics"`
	Inventory    []InventoryView    `json:"inventory"`
	Unavailable  []string           `json:"unavailable"`
	Suppliers    []SupplierView     `json:"suppliers"`
	Orders       []OrderView        `json:"orders"`
	Carryover    ShopCarryover      `json:"carryover"`
	Events       []ShopEvent        `json:"events"`
	History      []ShopHistoryPoint `json:"history"`
}

type shopItem struct {
	menu     *MenuItem
	state    string
	started  time.Time
	duration time.Duration
	node     string
}

type shopOrder struct {
	id       int64
	customer int64
	arrived  time.Time
	patience time.Duration
	wants    []*MenuItem
	stage    string // queued, ordering, preparing, pickup
	items    []*shopItem
	pickupAt time.Time
}

type shopTask struct {
	order    *shopOrder
	item     *shopItem
	enqueued time.Time
	notUntil time.Time
}

type shopStock struct {
	spec  stockSpec
	level int
	state string
}

type supplier struct {
	spec     supplierSpec
	state    string // idle, en_route
	eta      time.Time
	bringing map[string]int
	delayAt  time.Time
	delay    time.Duration
	delayed  bool
}

type stamped struct {
	t    time.Time
	name string
}

// Shop is the Grove Coffee simulation. Customers arrive at random, cashiers
// take their orders, baristas and kitchen staff prepare the items
// concurrently, and orders wait at pickup until every item is ready. Every
// piece of station work is a Grove call, so the staff are Grove execution
// capacity: the number of healthy nodes times StaffPerNode. A shift manager
// assigns that capacity to the stations that need it most.
//
// Run it as a single Grove component instance cluster-wide; RunOwned claims
// the exclusive capability.
type Shop struct {
	deps     ShopDeps
	instance string

	mu      sync.Mutex
	rng     *rand.Rand
	enabled bool
	open    bool
	// paused is set while this instance has briefly lost the capability,
	// for example while Grove re-establishes leases after a node loss.
	paused   bool
	pausedAt time.Time
	gen      int64
	opened   time.Time
	restored bool

	nodes          []string
	nodesKnown     bool
	staffTotal     int
	alloc          map[Station]int
	active         map[Station]int
	queues         map[Station][]*shopTask
	busyAvg        map[Station]float64
	congested      map[Station]bool
	lastShift      time.Time
	lastShiftEvent time.Time
	lastFailEvent  time.Time

	orders      map[int64]*shopOrder
	customerSeq int64
	orderSeq    int64
	served      int64
	left        int64
	workDone    int64

	stock     map[string]*shopStock
	suppliers []*supplier

	arrivals    []time.Time
	completions map[Station][]stamped
	ordered     []stamped
	consumed    map[string][]stamped
	// waits are pickup times, aligned with waitMillis.
	waits      []stamped
	waitMillis []int64

	// Demand model.
	baseRate    float64
	drinkShare  float64
	foodShare   float64
	drinkTarget float64
	foodTarget  float64
	nextRegime  time.Time
	rushUntil   time.Time
	rushFactor  float64
	nextRush    time.Time
	lastSecond  time.Time
	lastStep    time.Time

	eventSeq int64
	events   []ShopEvent
	history  []ShopHistoryPoint
}

// NewShop creates a closed Shop; it opens when it owns the capability (or
// when Open is called directly).
func NewShop(deps ShopDeps) *Shop {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	rng := deps.Rand
	if rng == nil {
		seed := uint64(time.Now().UnixNano())
		rng = rand.New(rand.NewPCG(seed, seed>>17|1))
	}
	return &Shop{
		deps:     deps,
		rng:      rng,
		instance: fmt.Sprintf("%s-%d", deps.Node, time.Now().UnixNano()),
		enabled:  true,
	}
}

// Enabled reports whether this instance owns the shop capability.
func (s *Shop) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// SetEnabled records ownership. Losing it pauses the shop, so a stale owner
// never keeps running customers; regaining it soon after resumes the same
// shop, and otherwise opens a fresh one. Grove's lease can lapse for a few
// seconds while the cluster recovers from a node loss, and a pause keeps
// that from wiping the shop.
func (s *Shop) SetEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.deps.Now()
	s.enabled = enabled
	switch {
	case enabled && s.open && s.paused && now.Sub(s.pausedAt) <= shopResumeWithin:
		s.paused = false
		s.addEvent(now, "good", "resumed", "▶", fmt.Sprintf("Shop resumed after a %s pause", clock(now.Sub(s.pausedAt))))
	case enabled && (!s.open || s.paused):
		s.openLocked(now)
	case !enabled && s.open && !s.paused:
		s.paused, s.pausedAt = true, now
		s.addEvent(now, "notice", "paused", "⏸", "Shop paused while Grove confirms which node runs it")
	}
}

// Serving reports whether this instance has a shop to show: open, even if
// paused.
func (s *Shop) Serving() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open
}

// Open starts a fresh simulation immediately, without claiming ownership.
func (s *Shop) Open() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = true
	s.openLocked(s.deps.Now())
}

// RunOwned claims the exclusive shop capability through Grove and keeps the
// shop open only while this instance holds it. It blocks until ctx ends.
func (s *Shop) RunOwned(ctx context.Context) {
	for ctx.Err() == nil {
		ownership := grove.Exclusive(ctx, ShopCapability)
		s.SetEnabled(ownership.Enabled())
		for ownership.Enabled() {
			sleepContext(ctx, 50*time.Millisecond)
		}
		s.SetEnabled(false)
		ownership.Release()
		sleepContext(ctx, 100*time.Millisecond)
	}
}

// Run advances the simulation and refreshes Grove capacity until ctx ends.
func (s *Shop) Run(ctx context.Context) {
	go s.runCapacity(ctx)
	ticker := time.NewTicker(shopTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Step(ctx)
		}
	}
}

func (s *Shop) runCapacity(ctx context.Context) {
	ticker := time.NewTicker(shopCapacityEvery)
	defer ticker.Stop()
	for {
		s.RefreshCapacity(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RefreshCapacity reads the Grove nodes that can do station work. A failed
// read keeps the last known capacity.
func (s *Shop) RefreshCapacity(ctx context.Context) {
	if s.deps.Capacity == nil || !s.Enabled() {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	nodes, err := s.deps.Capacity(callCtx)
	cancel()
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setNodesLocked(s.deps.Now(), nodes)
}

func sleepContext(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

func (s *Shop) openLocked(now time.Time) {
	s.open, s.paused = true, false
	s.gen++
	s.instance = fmt.Sprintf("%s-%d", s.deps.Node, time.Now().UnixNano())
	s.eventSeq, s.events, s.history = 0, nil, nil
	s.opened = now
	s.restored = false
	s.nodes, s.nodesKnown, s.staffTotal = nil, false, 0
	s.alloc = map[Station]int{}
	s.active = map[Station]int{}
	s.queues = map[Station][]*shopTask{}
	s.busyAvg = map[Station]float64{}
	s.congested = map[Station]bool{}
	s.completions = map[Station][]stamped{}
	s.consumed = map[string][]stamped{}
	s.orders = map[int64]*shopOrder{}
	s.customerSeq, s.orderSeq, s.served, s.left, s.workDone = 0, 0, 0, 0, 0
	s.arrivals, s.ordered, s.waits, s.waitMillis = nil, nil, nil, nil
	s.stock = map[string]*shopStock{}
	for _, spec := range stockSpecs {
		// Start the day with a little variety in what is on the shelves.
		level := int(float64(spec.capacity) * (0.6 + 0.4*s.rng.Float64()))
		s.stock[spec.id] = &shopStock{spec: spec, level: level, state: "ok"}
	}
	s.suppliers = nil
	for _, spec := range supplierSpecs {
		s.suppliers = append(s.suppliers, &supplier{spec: spec, state: "idle"})
	}
	s.baseRate = 1.3 + 0.4*s.rng.Float64()
	s.drinkShare, s.foodShare = 0.8, 0.45
	s.drinkTarget, s.foodTarget = s.drinkShare, s.foodShare
	s.nextRegime = now.Add(s.uniformDuration(60*time.Second, 150*time.Second))
	s.rushUntil, s.rushFactor = time.Time{}, 1
	s.nextRush = now.Add(s.uniformDuration(25*time.Second, 70*time.Second))
	s.lastSecond, s.lastStep = now, now
	s.addEvent(now, "good", "open", "☕", "Grove Coffee is open on "+s.deps.Node)
}

// Restore applies the carryover of the Shop this one replaces. It only
// applies to a Shop opened recently that has not been restored already.
func (s *Shop) Restore(carry ShopCarryover) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.deps.Now()
	if !s.open || s.restored || now.Sub(s.opened) > shopCarryoverLimit {
		return
	}
	s.restored = true
	s.served += carry.Served
	s.left += carry.Left
	s.customerSeq = max(s.customerSeq, carry.CustomerSeq)
	s.orderSeq = max(s.orderSeq, carry.OrderSeq)
	for id, level := range carry.Inventory {
		if stock, ok := s.stock[id]; ok {
			stock.level = min(max(level, 0), stock.spec.capacity)
			stock.state = stockState(stock)
		}
	}
	if len(carry.Nodes) > 0 {
		previous := slices.Clone(carry.Nodes)
		slices.Sort(previous)
		if s.nodesKnown {
			// Report nodes lost while the shop was moving.
			for _, node := range previous {
				if !slices.Contains(s.nodes, node) {
					s.addEvent(now, "alert", "node_lost", "🔴", node+" lost")
				}
			}
		} else {
			s.nodes, s.nodesKnown = previous, true
		}
	}
	s.addEvent(now, "notice", "relocated", "↻", "Shop moved to "+s.deps.Node+"; sales and inventory carried over")
}

// Step advances the simulation by one tick.
func (s *Shop) Step(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open || s.paused {
		return
	}
	now := s.deps.Now()
	s.demandLocked(now)
	s.arriveLocked(now)
	s.abandonLocked(now)
	s.pickupLocked(now)
	s.suppliersLocked(now)
	if now.Sub(s.lastShift) >= shopShiftEvery {
		s.lastShift = now
		s.shiftLocked(now)
	}
	s.utilizationLocked()
	s.congestionLocked(now)
	if now.Sub(s.lastSecond) >= time.Second {
		s.lastSecond = now
		s.secondLocked(now)
	}
	s.dispatchLocked(ctx, now)
}

// arrivalRate is customers per second right now.
func (s *Shop) arrivalRate(now time.Time) float64 {
	wave := 1 + 0.25*math.Sin(2*math.Pi*float64(now.Sub(s.opened))/float64(3*time.Minute))
	rate := s.baseRate * wave
	if now.Before(s.rushUntil) {
		rate *= s.rushFactor
	}
	return rate
}

// demandLocked evolves the demand model: occasional rushes, and a product mix
// that drifts between drink-heavy and food-heavy regimes.
func (s *Shop) demandLocked(now time.Time) {
	if !s.nextRush.After(now) {
		s.rushFactor = 1.7 + 0.9*s.rng.Float64()
		s.rushUntil = now.Add(s.uniformDuration(25*time.Second, 55*time.Second))
		s.nextRush = s.rushUntil.Add(90*time.Second + s.expDuration(120*time.Second))
		s.addEvent(now, "notice", "rush", "📈", "Customer rush: arrivals picking up")
	} else if !s.rushUntil.IsZero() && !now.Before(s.rushUntil) {
		s.rushUntil = time.Time{}
		s.addEvent(now, "quiet", "", "📉", "Rush easing")
	}
	if !s.nextRegime.After(now) {
		s.nextRegime = now.Add(s.uniformDuration(90*time.Second, 200*time.Second))
		switch s.rng.IntN(3) {
		case 0: // drink-heavy
			s.drinkTarget, s.foodTarget = 0.95, 0.25
		case 1: // balanced
			s.drinkTarget, s.foodTarget = 0.75, 0.5
		default: // food-heavy
			s.drinkTarget, s.foodTarget = 0.6, 0.8
		}
	}
}

func (s *Shop) arriveLocked(now time.Time) {
	// Customers arrive in small groups; groups follow a Poisson process.
	const meanGroup = 1.38
	elapsed := now.Sub(s.lastStep)
	s.lastStep = now
	if elapsed <= 0 {
		return
	}
	expected := s.arrivalRate(now) / meanGroup * min(elapsed, time.Second).Seconds()
	groups := s.poisson(expected)
	for range groups {
		size := 1
		if r := s.rng.Float64(); r > 0.92 {
			size = 3
		} else if r > 0.70 {
			size = 2
		}
		for range size {
			s.customerSeq++
			s.arrivals = append(s.arrivals, now)
			order := &shopOrder{
				id:       0,
				customer: s.customerSeq,
				arrived:  now,
				patience: s.uniformDuration(45*time.Second, 120*time.Second),
				wants:    s.chooseLocked(),
				stage:    "queued",
			}
			s.orders[-order.customer] = order // keyed by customer until ordered
			s.enqueueLocked(StationCashier, &shopTask{order: order, enqueued: now})
		}
		if size == 1 {
			s.addEvent(now, "quiet", "", "👤", fmt.Sprintf("Customer #%d entered", s.customerSeq))
		} else {
			s.addEvent(now, "quiet", "", strings.Repeat("👤", size), fmt.Sprintf("%d customers entered", size))
		}
	}
}

// chooseLocked draws what a new customer wants from the current product mix.
func (s *Shop) chooseLocked() []*MenuItem {
	wantsDrink := s.rng.Float64() < s.drinkShare
	wantsFood := s.rng.Float64() < s.foodShare
	if !wantsDrink && !wantsFood {
		wantsDrink = true
	}
	var wants []*MenuItem
	if wantsDrink {
		wants = append(wants, s.pickLocked(KindDrink, nil))
		if s.rng.Float64() < 0.12 {
			wants = append(wants, s.pickLocked(KindDrink, nil))
		}
	}
	if wantsFood {
		wants = append(wants, s.pickLocked(KindFood, nil))
	}
	return wants
}

// pickLocked draws a menu item of kind by weight, among those allowed (nil
// allows all). It returns nil when nothing qualifies.
func (s *Shop) pickLocked(kind ItemKind, allowed func(*MenuItem) bool) *MenuItem {
	total := 0.0
	for i := range Menu {
		if Menu[i].Kind == kind && (allowed == nil || allowed(&Menu[i])) {
			total += Menu[i].Weight
		}
	}
	if total == 0 {
		return nil
	}
	r := s.rng.Float64() * total
	var last *MenuItem
	for i := range Menu {
		if Menu[i].Kind != kind || (allowed != nil && !allowed(&Menu[i])) {
			continue
		}
		last = &Menu[i]
		if r < Menu[i].Weight {
			return last
		}
		r -= Menu[i].Weight
	}
	return last
}

func (s *Shop) available(item *MenuItem) bool {
	for id, n := range item.Uses {
		if stock, ok := s.stock[id]; !ok || stock.level < n {
			return false
		}
	}
	return true
}

func (s *Shop) enqueueLocked(station Station, task *shopTask) {
	s.queues[station] = append(s.queues[station], task)
}

// abandonLocked makes customers who have waited in line past their patience
// leave before ordering.
func (s *Shop) abandonLocked(now time.Time) {
	queue := s.queues[StationCashier]
	kept := queue[:0]
	for _, task := range queue {
		if waited := now.Sub(task.order.arrived); waited > task.order.patience {
			s.left++
			delete(s.orders, -task.order.customer)
			s.addEvent(now, "notice", "left", "😠", fmt.Sprintf("Customer #%d left after %s in line", task.order.customer, clock(waited)))
			continue
		}
		kept = append(kept, task)
	}
	clear(queue[len(kept):])
	s.queues[StationCashier] = kept
}

func (s *Shop) pickupLocked(now time.Time) {
	for key, order := range s.orders {
		if order.stage != "pickup" || now.Before(order.pickupAt) {
			continue
		}
		delete(s.orders, key)
		s.served++
		wait := now.Sub(order.arrived)
		s.waits = append(s.waits, stamped{t: now})
		s.waitMillis = append(s.waitMillis, wait.Milliseconds())
		s.addEvent(now, "quiet", "", "✓", fmt.Sprintf("Order #%d picked up after %s", order.id, clock(wait)))
	}
}

func (s *Shop) suppliersLocked(now time.Time) {
	for _, sup := range s.suppliers {
		switch sup.state {
		case "idle":
			need := false
			for _, id := range sup.spec.products {
				stock := s.stock[id]
				if float64(stock.level) < reorderBelow*float64(stock.spec.capacity) {
					need = true
				}
			}
			if !need {
				continue
			}
			sup.state = "en_route"
			trip := s.uniformDuration(35*time.Second, 70*time.Second)
			sup.eta = now.Add(trip)
			sup.bringing = map[string]int{}
			for _, id := range sup.spec.products {
				stock := s.stock[id]
				// Order enough to fill the shelf as it is expected to be on arrival.
				sup.bringing[id] = max(stock.spec.capacity-stock.level, stock.spec.capacity/4)
			}
			sup.delayed, sup.delay = false, 0
			sup.delayAt = time.Time{}
			if s.rng.Float64() < 0.3 {
				sup.delayAt = now.Add(time.Duration(float64(trip) * (0.3 + 0.5*s.rng.Float64())))
				sup.delay = s.uniformDuration(20*time.Second, 50*time.Second)
			}
			s.addEvent(now, "notice", "supplier", "🚚", fmt.Sprintf("%s dispatched: %s (ETA %s)", sup.spec.name, describeDelivery(sup.bringing), clock(trip)))
		case "en_route":
			if !sup.delayAt.IsZero() && !sup.delayed && !now.Before(sup.delayAt) {
				sup.delayed = true
				sup.eta = sup.eta.Add(sup.delay)
				s.addEvent(now, "notice", "supplier_delayed", "🚚", fmt.Sprintf("%s delayed by %s", sup.spec.name, clock(sup.delay)))
			}
			if now.Before(sup.eta) {
				continue
			}
			sup.state = "idle"
			s.addEvent(now, "good", "supplier_arrived", "📦", fmt.Sprintf("%s delivered %s", sup.spec.name, describeDelivery(sup.bringing)))
			before := s.unavailableLocked()
			for id, n := range sup.bringing {
				stock := s.stock[id]
				stock.level = min(stock.spec.capacity, stock.level+n)
			}
			sup.bringing = nil
			s.stockChangedLocked(now, before)
		}
	}
}

func describeDelivery(bringing map[string]int) string {
	ids := make([]string, 0, len(bringing))
	for id := range bringing {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%s ×%d", stockLabel(id), bringing[id]))
	}
	return strings.Join(parts, ", ")
}

func stockLabel(id string) string {
	for _, spec := range stockSpecs {
		if spec.id == id {
			return strings.ToLower(spec.label)
		}
	}
	return id
}

func stockState(stock *shopStock) string {
	fraction := float64(stock.level) / float64(stock.spec.capacity)
	switch {
	case stock.level <= 0:
		return "out"
	case fraction < criticalBelow:
		return "critical"
	case fraction < lowBelow:
		return "low"
	}
	return "ok"
}

// unavailableLocked lists menu items that cannot currently be made.
func (s *Shop) unavailableLocked() []string {
	var names []string
	for i := range Menu {
		if !s.available(&Menu[i]) {
			names = append(names, Menu[i].Name)
		}
	}
	return names
}

// stockChangedLocked reports stock state transitions and menu availability
// changes since before.
func (s *Shop) stockChangedLocked(now time.Time, before []string) {
	for _, spec := range stockSpecs {
		stock := s.stock[spec.id]
		state := stockState(stock)
		if state == stock.state {
			continue
		}
		worse := stateRank(state) > stateRank(stock.state)
		stock.state = state
		if !worse {
			continue
		}
		switch state {
		case "low":
			s.addEvent(now, "notice", "stock_low", "⚠", spec.label+" inventory low")
		case "critical":
			s.addEvent(now, "notice", "stock_critical", "🔥", spec.label+" critical")
		case "out":
			s.addEvent(now, "alert", "stock_out", "⛔", spec.label+" ran out")
		}
	}
	after := s.unavailableLocked()
	for _, name := range after {
		if !slices.Contains(before, name) {
			s.addEvent(now, "notice", "unavailable", "⛔", name+" temporarily unavailable")
		}
	}
	for _, name := range before {
		if !slices.Contains(after, name) {
			s.addEvent(now, "good", "available", "✓", name+" available again")
		}
	}
}

func stateRank(state string) int {
	switch state {
	case "low":
		return 1
	case "critical":
		return 2
	case "out":
		return 3
	}
	return 0
}

// setNodesLocked records the Grove nodes providing staff and adjusts the
// staff available to the shift manager.
func (s *Shop) setNodesLocked(now time.Time, nodes []string) {
	nodes = slices.Clone(nodes)
	slices.Sort(nodes)
	if s.nodesKnown {
		for _, node := range nodes {
			if !slices.Contains(s.nodes, node) {
				s.addEvent(now, "good", "node_joined", "🟢", node+" joined: +"+fmt.Sprint(StaffPerNode)+" staff available")
			}
		}
		for _, node := range s.nodes {
			if !slices.Contains(nodes, node) {
				s.addEvent(now, "alert", "node_lost", "🔴", node+" lost: shop capacity reduced")
			}
		}
	}
	s.nodes, s.nodesKnown = nodes, true
	total := len(nodes) * StaffPerNode
	if total == s.staffTotal {
		return
	}
	s.staffTotal = total
	s.fitAllocationLocked(now)
}

// fitAllocationLocked makes the station allocation add up to the staff total
// after capacity changed: new staff go where they are needed most, and lost
// staff come off the stations that can spare them best.
func (s *Shop) fitAllocationLocked(now time.Time) {
	assigned := 0
	for _, station := range Stations {
		assigned += s.alloc[station]
	}
	target := s.targetAllocationLocked()
	for assigned < s.staffTotal {
		station := s.mostShortLocked(target)
		s.alloc[station]++
		assigned++
	}
	for assigned > s.staffTotal {
		station := s.mostSurplusLocked(target)
		s.alloc[station]--
		assigned--
	}
}

// pressure is the outstanding work at station in staff-seconds.
func (s *Shop) pressure(station Station) float64 {
	return float64(len(s.queues[station])+s.active[station]) * meanWorkSeconds(station)
}

func meanWorkSeconds(station Station) float64 {
	switch station {
	case StationCashier:
		return float64(cashierMinMillis+cashierMaxMillis) / 2000
	case StationBarista:
		return 2.4
	default:
		return 2.7
	}
}

// targetAllocationLocked splits the staff total across stations in
// proportion to their pressure, with at least one person per station when
// there are enough staff.
func (s *Shop) targetAllocationLocked() map[Station]int {
	target := map[Station]int{}
	total := s.staffTotal
	if total <= 0 {
		return target
	}
	floor := 0
	if total >= len(Stations) {
		floor = 1
	}
	remaining := total - floor*len(Stations)
	pressures := map[Station]float64{}
	sum := 0.0
	for _, station := range Stations {
		// A small baseline keeps idle stations staffed in proportion to the
		// work they usually get.
		p := s.pressure(station) + 0.5*meanWorkSeconds(station)
		pressures[station] = p
		sum += p
	}
	type share struct {
		station Station
		frac    float64
	}
	var shares []share
	given := 0
	for _, station := range Stations {
		exact := float64(remaining) * pressures[station] / sum
		whole := int(exact)
		target[station] = floor + whole
		given += whole
		shares = append(shares, share{station, exact - float64(whole)})
	}
	sort.SliceStable(shares, func(i, j int) bool { return shares[i].frac > shares[j].frac })
	for i := 0; given < remaining; i++ {
		target[shares[i%len(shares)].station]++
		given++
	}
	return target
}

func (s *Shop) mostShortLocked(target map[Station]int) Station {
	best, bestGap := Stations[0], math.MinInt
	for _, station := range Stations {
		if gap := target[station] - s.alloc[station]; gap > bestGap {
			best, bestGap = station, gap
		}
	}
	return best
}

func (s *Shop) mostSurplusLocked(target map[Station]int) Station {
	best, bestGap := Station(""), math.MinInt
	for _, station := range Stations {
		if s.alloc[station] == 0 {
			continue
		}
		if gap := s.alloc[station] - target[station]; gap > bestGap {
			best, bestGap = station, gap
		}
	}
	return best
}

// shiftLocked is the shift manager: it moves one staff member at a time from
// the station that can best spare one to the station under most pressure.
func (s *Shop) shiftLocked(now time.Time) {
	if s.staffTotal < len(Stations) {
		return
	}
	target := s.targetAllocationLocked()
	to := s.mostShortLocked(target)
	from := s.mostSurplusLocked(target)
	if from == "" || from == to || target[to]-s.alloc[to] <= 0 || s.alloc[from]-target[from] <= 0 || s.alloc[from] <= 1 {
		return
	}
	s.alloc[from]--
	s.alloc[to]++
	if now.Sub(s.lastShiftEvent) >= 10*time.Second {
		s.lastShiftEvent = now
		s.addEvent(now, "notice", "rebalanced", "↻", "Capacity shifted toward "+stationWork(to))
	}
}

func stationWork(station Station) string {
	switch station {
	case StationCashier:
		return "taking orders"
	case StationBarista:
		return "drink preparation"
	default:
		return "food preparation"
	}
}

func stationQueueName(station Station) string {
	switch station {
	case StationCashier:
		return "Cashier line"
	case StationBarista:
		return "Drink queue"
	default:
		return "Food queue"
	}
}

func (s *Shop) utilizationLocked() {
	for _, station := range Stations {
		busy := 0.0
		if s.alloc[station] > 0 {
			busy = min(1, float64(s.active[station])/float64(s.alloc[station]))
		}
		s.busyAvg[station] = 0.9*s.busyAvg[station] + 0.1*busy
	}
}

// congestionLocked reports stations whose queue grows well beyond what their
// staff can clear, with hysteresis so the stream is not flooded.
func (s *Shop) congestionLocked(now time.Time) {
	for _, station := range Stations {
		queue := len(s.queues[station])
		staff := max(1, s.alloc[station])
		if !s.congested[station] && queue >= 8 && float64(queue) >= 2.5*float64(staff) {
			s.congested[station] = true
			s.addEvent(now, "notice", "congested", "🔥", fmt.Sprintf("%s congested: %d waiting", stationQueueName(station), queue))
		} else if s.congested[station] && float64(queue) <= float64(staff) {
			s.congested[station] = false
			s.addEvent(now, "good", "cleared", "✓", stationQueueName(station)+" back to normal")
		}
	}
}

// secondLocked runs once a second: product-mix drift, window trimming and a
// history sample.
func (s *Shop) secondLocked(now time.Time) {
	s.drinkShare = clamp(s.drinkShare+(s.drinkTarget-s.drinkShare)*0.03+0.015*s.rng.NormFloat64(), 0.5, 0.98)
	s.foodShare = clamp(s.foodShare+(s.foodTarget-s.foodShare)*0.03+0.015*s.rng.NormFloat64(), 0.15, 0.85)

	s.arrivals = trimTimes(s.arrivals, now.Add(-shopRateWindow))
	for _, station := range Stations {
		s.completions[station] = trimStamped(s.completions[station], now.Add(-shopRateWindow))
	}
	s.ordered = trimStamped(s.ordered, now.Add(-shopMixWindow))
	for id, uses := range s.consumed {
		s.consumed[id] = trimStamped(uses, now.Add(-shopRateWindow))
	}
	cut := 0
	for cut < len(s.waits) && s.waits[cut].t.Before(now.Add(-shopWaitWindow)) {
		cut++
	}
	s.waits, s.waitMillis = s.waits[cut:], s.waitMillis[cut:]

	s.history = append(s.history, ShopHistoryPoint{
		UnixMilli:     now.UnixMilli(),
		CashierQueue:  len(s.queues[StationCashier]),
		BaristaQueue:  len(s.queues[StationBarista]),
		KitchenQueue:  len(s.queues[StationKitchen]),
		AvgWaitMillis: s.avgWaitLocked(),
		ArrivalsMin:   float64(len(s.arrivals)) * float64(time.Minute) / float64(shopRateWindow),
		Staff:         s.staffTotal,
		Nodes:         len(s.nodes),
	})
	if len(s.history) > shopMaxHistory {
		s.history = slices.Clone(s.history[len(s.history)-shopMaxHistory:])
	}
}

func (s *Shop) avgWaitLocked() int64 {
	if len(s.waitMillis) == 0 {
		return 0
	}
	var sum int64
	for _, w := range s.waitMillis {
		sum += w
	}
	return sum / int64(len(s.waitMillis))
}

// dispatchLocked starts queued work while stations have free staff.
func (s *Shop) dispatchLocked(ctx context.Context, now time.Time) {
	if s.deps.Work == nil || s.paused {
		return
	}
	for _, station := range Stations {
		for s.active[station] < s.alloc[station] {
			index := slices.IndexFunc(s.queues[station], func(task *shopTask) bool { return !now.Before(task.notUntil) })
			if index < 0 {
				break
			}
			task := s.queues[station][index]
			s.queues[station] = slices.Delete(s.queues[station], index, index+1)
			s.startLocked(ctx, now, station, task)
		}
	}
}

func (s *Shop) startLocked(ctx context.Context, now time.Time, station Station, task *shopTask) {
	s.active[station]++
	request := WorkRequest{Station: station}
	if station == StationCashier {
		task.order.stage = "ordering"
		request.DurationMillis = s.uniformMillis(cashierMinMillis, cashierMaxMillis)
		request.Item = "order"
	} else {
		item := task.item
		item.state = "preparing"
		item.started = now
		item.duration = time.Duration(s.uniformMillis(item.menu.MinMillis, item.menu.MaxMillis)) * time.Millisecond
		request.DurationMillis = int(item.duration.Milliseconds())
		request.Item = item.menu.Name
		request.OrderID = task.order.id
	}
	gen := s.gen
	go func() {
		// No call deadline: Grove splits a caller's deadline across the
		// handler's nodes and hands that share to the handler, which would cut
		// short work longer than the share. Grove already bounds each attempt
		// by its own request timeout, which is longer than MaxWorkDuration.
		result, err := s.deps.Work(ctx, request)
		s.finish(ctx, gen, station, task, result, err)
	}()
}

func (s *Shop) finish(ctx context.Context, gen int64, station Station, task *shopTask, result WorkResult, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen || !s.open {
		return // this shop closed or reopened while the work ran
	}
	now := s.deps.Now()
	s.active[station]--
	if err != nil {
		// The staff member's node was busy or lost; the work goes back to the
		// front of the line and is retried shortly.
		task.notUntil = now.Add(shopRetryBackoff)
		if station == StationCashier {
			task.order.stage = "queued"
		} else {
			task.item.state = "queued"
			task.item.node = ""
		}
		s.queues[station] = append([]*shopTask{task}, s.queues[station]...)
		if !strings.Contains(err.Error(), ErrStaffBusy.Error()) && now.Sub(s.lastFailEvent) >= 5*time.Second {
			s.lastFailEvent = now
			s.addEvent(now, "notice", "retry", "⚠", stationWork(station)+" interrupted; retrying with other staff")
		}
		s.dispatchLocked(ctx, now)
		return
	}
	s.workDone++
	s.completions[station] = append(s.completions[station], stamped{t: now, name: result.Node})
	if station == StationCashier {
		s.orderTakenLocked(now, task.order)
	} else {
		task.item.state = "ready"
		task.item.node = result.Node
		s.addEvent(now, "quiet", "", task.item.menu.Emoji, fmt.Sprintf("%s #%d ready", task.item.menu.Name, task.order.id))
		s.maybeReadyLocked(now, task.order)
	}
	s.dispatchLocked(ctx, now)
}

// orderTakenLocked turns what the customer wanted into an order of available
// items, consumes inventory and fans the items out to the stations.
func (s *Shop) orderTakenLocked(now time.Time, order *shopOrder) {
	delete(s.orders, -order.customer)
	before := s.unavailableLocked()
	var items []*shopItem
	for _, want := range order.wants {
		choice := want
		if !s.available(choice) {
			// Pick something else of the same kind that can be made.
			choice = s.pickLocked(want.Kind, s.available)
		}
		if choice == nil {
			continue
		}
		for id, n := range choice.Uses {
			s.stock[id].level -= n
			for range n {
				s.consumed[id] = append(s.consumed[id], stamped{t: now})
			}
		}
		items = append(items, &shopItem{menu: choice, state: "queued"})
		s.ordered = append(s.ordered, stamped{t: now, name: string(choice.Kind)})
	}
	s.stockChangedLocked(now, before)
	if len(items) == 0 {
		s.left++
		s.addEvent(now, "quiet", "left", "😞", fmt.Sprintf("Customer #%d left: nothing they wanted was available", order.customer))
		return
	}
	s.orderSeq++
	order.id = s.orderSeq
	order.items = items
	order.stage = "preparing"
	s.orders[order.id] = order
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = item.menu.Name
		station := StationBarista
		if item.menu.Kind == KindFood {
			station = StationKitchen
		}
		s.enqueueLocked(station, &shopTask{order: order, item: item, enqueued: now})
	}
	s.addEvent(now, "quiet", "", "🧾", fmt.Sprintf("Order #%d — %s", order.id, strings.Join(names, " + ")))
}

func (s *Shop) maybeReadyLocked(now time.Time, order *shopOrder) {
	for _, item := range order.items {
		if item.state != "ready" {
			return
		}
	}
	order.stage = "pickup"
	order.pickupAt = now.Add(time.Duration(s.uniformMillis(800, 2500)) * time.Millisecond)
}

// Snapshot returns the shop's observable state, with events and history newer
// than the given sequence number and time.
func (s *Shop) Snapshot(sinceEvent, sinceHistory int64) ShopSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.deps.Now()
	snapshot := ShopSnapshot{
		Instance:     s.instance,
		Node:         s.deps.Node,
		OpenedMilli:  s.opened.UnixMilli(),
		NowMilli:     now.UnixMilli(),
		Paused:       s.paused,
		Nodes:        slices.Clone(s.nodes),
		StaffPerNode: StaffPerNode,
		StaffTotal:   s.staffTotal,
		Unavailable:  []string{},
		Orders:       []OrderView{},
		Events:       []ShopEvent{},
		History:      []ShopHistoryPoint{},
	}
	if !s.open {
		return snapshot
	}
	worst, worstScore := Station(""), 0.0
	for _, station := range Stations {
		view := StationView{
			ID:          station,
			Staff:       s.alloc[station],
			Busy:        s.active[station],
			Queue:       len(s.queues[station]),
			Utilization: s.busyAvg[station],
			PerMinute:   float64(len(s.completions[station])) * float64(time.Minute) / float64(shopRateWindow),
			Congested:   s.congested[station],
			ByNode:      map[string]int{},
		}
		if len(s.queues[station]) > 0 {
			oldest := s.queues[station][0].enqueued
			for _, task := range s.queues[station] {
				if task.enqueued.Before(oldest) {
					oldest = task.enqueued
				}
			}
			view.OldestWait = now.Sub(oldest).Milliseconds()
		}
		for _, done := range s.completions[station] {
			view.ByNode[done.name]++
		}
		snapshot.Stations = append(snapshot.Stations, view)
		// The bottleneck is the station whose backlog would take longest to
		// clear with its current staff.
		score := float64(view.Queue) * meanWorkSeconds(station) / float64(max(1, view.Staff))
		if view.Queue >= 4 && score > worstScore {
			worst, worstScore = station, score
		}
	}
	snapshot.Bottleneck = worst

	metrics := ShopMetrics{
		Served:         s.served,
		WorkDone:       s.workDone,
		Left:           s.left,
		AvgWaitMillis:  s.avgWaitLocked(),
		ArrivalsPerMin: float64(len(s.arrivals)) * float64(time.Minute) / float64(shopRateWindow),
		Rush:           now.Before(s.rushUntil),
	}
	drinks, food := 0, 0
	for _, o := range s.ordered {
		if o.name == string(KindDrink) {
			drinks++
		} else {
			food++
		}
	}
	if drinks+food > 0 {
		metrics.DrinkShare = float64(drinks) / float64(drinks+food)
	}
	var active []*shopOrder
	for _, order := range s.orders {
		metrics.CustomersInside++
		metrics.LongestWait = max(metrics.LongestWait, now.Sub(order.arrived).Milliseconds())
		if order.id > 0 {
			metrics.ActiveOrders++
			active = append(active, order)
		}
	}
	perMinute := float64(time.Minute) / float64(shopRateWindow)
	metrics.DrinksPerMin = float64(len(s.completions[StationBarista])) * perMinute
	metrics.FoodPerMin = float64(len(s.completions[StationKitchen])) * perMinute
	snapshot.Metrics = metrics

	sort.Slice(active, func(i, j int) bool { return active[i].id < active[j].id })
	if len(active) > shopMaxOrdersShown {
		active = active[:shopMaxOrdersShown]
	}
	for _, order := range active {
		view := OrderView{ID: order.id, Customer: order.customer, Stage: order.stage, AgeMs: now.Sub(order.arrived).Milliseconds()}
		for _, item := range order.items {
			itemView := OrderItemView{Name: item.menu.Name, Kind: item.menu.Kind, Emoji: item.menu.Emoji, State: item.state, Node: item.node}
			switch item.state {
			case "ready":
				itemView.Progress = 1
			case "preparing":
				if item.duration > 0 {
					itemView.Progress = min(0.99, float64(now.Sub(item.started))/float64(item.duration))
				}
			}
			view.Items = append(view.Items, itemView)
		}
		snapshot.Orders = append(snapshot.Orders, view)
	}

	for _, spec := range stockSpecs {
		stock := s.stock[spec.id]
		view := InventoryView{ID: spec.id, Label: spec.label, Level: stock.level, Capacity: spec.capacity, State: stockState(stock)}
		if used := len(s.consumed[spec.id]); used > 0 && stock.level > 0 {
			perMilli := float64(used) / float64(shopRateWindow.Milliseconds())
			view.DepletionMillis = int64(float64(stock.level) / perMilli)
		}
		snapshot.Inventory = append(snapshot.Inventory, view)
	}
	snapshot.Unavailable = append(snapshot.Unavailable, s.unavailableLocked()...)
	for _, sup := range s.suppliers {
		view := SupplierView{Name: sup.spec.name, State: sup.state, Delayed: sup.delayed}
		if sup.state == "en_route" {
			view.ETAMilli = max(0, sup.eta.Sub(now).Milliseconds())
			view.Bringing = sup.bringing
			if sup.delayed {
				view.DelayMs = sup.delay.Milliseconds()
			}
		}
		snapshot.Suppliers = append(snapshot.Suppliers, view)
	}
	snapshot.Carryover = ShopCarryover{
		Served:      s.served,
		Left:        s.left,
		CustomerSeq: s.customerSeq,
		OrderSeq:    s.orderSeq,
		Inventory:   map[string]int{},
		Nodes:       slices.Clone(s.nodes),
	}
	for id, stock := range s.stock {
		snapshot.Carryover.Inventory[id] = stock.level
	}
	for _, event := range s.events {
		if event.Seq > sinceEvent {
			snapshot.Events = append(snapshot.Events, event)
		}
	}
	for _, point := range s.history {
		if point.UnixMilli > sinceHistory {
			snapshot.History = append(snapshot.History, point)
		}
	}
	return snapshot
}

func (s *Shop) addEvent(now time.Time, level, kind, icon, text string) {
	s.eventSeq++
	s.events = append(s.events, ShopEvent{Seq: s.eventSeq, UnixMilli: now.UnixMilli(), Level: level, Kind: kind, Icon: icon, Text: text})
	if len(s.events) > shopMaxEvents {
		s.events = slices.Clone(s.events[len(s.events)-shopMaxEvents:])
	}
}

func (s *Shop) uniformDuration(lo, hi time.Duration) time.Duration {
	return lo + time.Duration(s.rng.Int64N(int64(hi-lo)+1))
}

func (s *Shop) expDuration(mean time.Duration) time.Duration {
	return time.Duration(s.rng.ExpFloat64() * float64(mean))
}

func (s *Shop) uniformMillis(lo, hi int) int {
	return lo + s.rng.IntN(hi-lo+1)
}

// poisson draws a Poisson-distributed count with mean lambda (Knuth).
func (s *Shop) poisson(lambda float64) int {
	if lambda <= 0 {
		return 0
	}
	limit, product, count := math.Exp(-lambda), s.rng.Float64(), 0
	for product > limit {
		count++
		product *= s.rng.Float64()
	}
	return count
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func trimTimes(times []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(times) && times[i].Before(cutoff) {
		i++
	}
	return times[i:]
}

func trimStamped(values []stamped, cutoff time.Time) []stamped {
	i := 0
	for i < len(values) && values[i].t.Before(cutoff) {
		i++
	}
	return values[i:]
}

// clock formats d as m:ss.
func clock(d time.Duration) string {
	seconds := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}
