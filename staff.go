package groveshop

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/grove-project/grove"
)

// Station is one kind of coffee-shop work. Each station is a Grove handler
// running on every node, so its staff are Grove execution capacity.
type Station string

const (
	// StationCashier takes customers' orders (Cashier.TakeOrder).
	StationCashier Station = "cashier"
	// StationBarista prepares drinks (Barista.MakeDrink).
	StationBarista Station = "barista"
	// StationKitchen prepares food (Kitchen.PrepareFood).
	StationKitchen Station = "kitchen"
)

// Stations lists every station in the order work flows through the shop.
var Stations = []Station{StationCashier, StationBarista, StationKitchen}

const (
	// StaffPerNode is how many pieces of work one Grove node executes at the
	// same time, shared across its Cashier, Barista and Kitchen handlers. The
	// shop's staff are exactly these execution slots, so adding a node adds
	// StaffPerNode staff and losing one removes them.
	StaffPerNode = 3
	// MaxWorkDuration bounds one piece of station work, keeping every Grove
	// call well inside Grove's per-request timeout.
	MaxWorkDuration = 4 * time.Second
	// staffWait is how long a node lets work wait for one of its staff before
	// reporting that it is fully busy.
	staffWait = 300 * time.Millisecond
)

var (
	// ErrStaffBusy is returned when every staff member on the node that
	// received the work is already busy; the shop retries it elsewhere.
	ErrStaffBusy = errors.New("all staff on this node are busy")
	// ErrWorkInvalid is returned for work with an unknown station or an
	// out-of-range duration.
	ErrWorkInvalid = errors.New("invalid station work")
)

// WorkRequest is one piece of station work: taking one customer's order or
// preparing one item.
type WorkRequest struct {
	Station Station
	OrderID int64
	Item    string
	// DurationMillis is how long the work takes; the shop draws it at random.
	DurationMillis int
}

// WorkResult reports which Grove node's staff did the work.
type WorkResult struct {
	Station Station
	OrderID int64
	Item    string
	Node    string
}

// Crew is one Grove node's staff: a fixed number of execution slots shared by
// every station handler on that node.
type Crew struct {
	node  string
	slots chan struct{}
}

// NewCrew creates a crew of size staff on node.
func NewCrew(node string, size int) *Crew {
	return &Crew{node: node, slots: make(chan struct{}, size)}
}

var (
	crewsMu sync.Mutex
	crews   = map[string]*Crew{}
)

// NodeCrew returns the shared crew of node, creating it on first use, so the
// Cashier, Barista and Kitchen components of one node draw on the same staff.
// One process can host several nodes, so crews are keyed by node ID.
func NodeCrew(node string) *Crew {
	crewsMu.Lock()
	defer crewsMu.Unlock()
	crew, ok := crews[node]
	if !ok {
		crew = NewCrew(node, StaffPerNode)
		crews[node] = crew
	}
	return crew
}

// Work performs req with one of the crew's staff, waiting briefly for one to
// become free.
func (c *Crew) Work(ctx context.Context, req WorkRequest) (WorkResult, error) {
	if !validStation(req.Station) || req.DurationMillis <= 0 ||
		time.Duration(req.DurationMillis)*time.Millisecond > MaxWorkDuration {
		return WorkResult{}, fmt.Errorf("%w: station %q for %dms", ErrWorkInvalid, req.Station, req.DurationMillis)
	}
	wait := time.NewTimer(staffWait)
	defer wait.Stop()
	select {
	case c.slots <- struct{}{}:
	case <-wait.C:
		return WorkResult{}, ErrStaffBusy
	case <-ctx.Done():
		return WorkResult{}, ctx.Err()
	}
	defer func() { <-c.slots }()
	work := time.NewTimer(time.Duration(req.DurationMillis) * time.Millisecond)
	defer work.Stop()
	select {
	case <-work.C:
	case <-ctx.Done():
		return WorkResult{}, ctx.Err()
	}
	return WorkResult{Station: req.Station, OrderID: req.OrderID, Item: req.Item, Node: c.node}, nil
}

func validStation(station Station) bool {
	for _, known := range Stations {
		if station == known {
			return true
		}
	}
	return false
}

// StationService returns the Grove service and method of station's handler.
func StationService(station Station) (grove.ServiceID, grove.MethodID) {
	switch station {
	case StationCashier:
		return ServiceCashier, MethodTakeOrder
	case StationBarista:
		return ServiceBarista, MethodMakeDrink
	case StationKitchen:
		return ServiceKitchen, MethodPrepareFood
	}
	return 0, 0
}

// RegisterStation associates station's handler with its Grove IDs, doing the
// work with crew. The handler rejects work meant for another station.
func RegisterStation(registry *grove.Registry, station Station, crew *Crew) error {
	if registry == nil {
		return ErrRegistryRequired
	}
	if crew == nil {
		return ErrServiceRequired
	}
	service, method := StationService(station)
	if service == 0 {
		return fmt.Errorf("%w: unknown station %q", ErrWorkInvalid, station)
	}
	return registry.Register(service, method, func(ctx context.Context, payload []byte) ([]byte, error) {
		var req WorkRequest
		if err := grove.Decode(payload, &req); err != nil {
			return nil, err
		}
		if req.Station != station {
			return nil, fmt.Errorf("%w: %s handler received %q work", ErrWorkInvalid, station, req.Station)
		}
		result, err := crew.Work(ctx, req)
		if err != nil {
			return nil, err
		}
		return grove.Encode(result)
	})
}
