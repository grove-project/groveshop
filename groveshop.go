// Package groveshop implements the deterministic Grove Shop reference
// application as ordinary Go business services.
package groveshop

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/grove-project/grove"
)

var (
	// ErrOrderIDRequired is returned when an order ID is empty.
	ErrOrderIDRequired = errors.New("order ID is required")
	// ErrOrderExists is returned when an order ID has already been created.
	ErrOrderExists = errors.New("order already exists")
	// ErrOrderNotFound is returned when an order ID has not been created.
	ErrOrderNotFound = errors.New("order not found")
	// ErrSKURequired is returned when an inventory SKU is empty.
	ErrSKURequired = errors.New("sku is required")
	// ErrQuantityPositive is returned when an inventory quantity is not positive.
	ErrQuantityPositive = errors.New("quantity must be positive")
	// ErrReservationBufferExceeded means Inventory cannot reserve the requested
	// quantity within its immutable configured buffer.
	ErrReservationBufferExceeded = errors.New("inventory reservation buffer exceeded")
	// ErrAmountPositive is returned when a payment amount is not positive.
	ErrAmountPositive = errors.New("amount must be positive")
	// ErrShippingAddressRequired is returned when a shipping address is empty.
	ErrShippingAddressRequired = errors.New("shipping address is required")
)

// OrderStatus is one completed stage in the Grove Shop order workflow.
type OrderStatus string

const (
	// OrderCreated means Orders accepted the request.
	OrderCreated OrderStatus = "Created"
	// OrderReserved means Inventory reserved the requested quantity.
	OrderReserved OrderStatus = "Reserved"
	// OrderPaid means Payment charged the order amount.
	OrderPaid OrderStatus = "Paid"
	// OrderShipping means Shipping arranged delivery.
	OrderShipping OrderStatus = "Shipping"
	// OrderCompleted means every business stage succeeded.
	OrderCompleted OrderStatus = "Completed"
)

// ReserveRequest describes inventory requested for one order.
type ReserveRequest struct {
	// OrderID identifies the order that owns the reservation.
	OrderID string
	// SKU identifies the product to reserve.
	SKU string
	// Quantity is the number of units to reserve.
	Quantity int
}

// Reservation is the deterministic result of a successful inventory request.
type Reservation struct {
	// ID identifies the reservation.
	ID string
	// OrderID identifies the associated order.
	OrderID string
	// SKU identifies the reserved product.
	SKU string
	// Quantity is the number of reserved units.
	Quantity int
}

// Inventory reserves products for Grove Shop orders.
type Inventory struct {
	reservationBuffer int
	configured        bool
}

// NewInventory creates Inventory with an immutable reservation buffer from the
// compiled Grove Shop configuration.
func NewInventory(reservationBuffer int) *Inventory {
	return &Inventory{reservationBuffer: reservationBuffer, configured: true}
}

// Reserve validates req and returns a deterministic reservation.
func (s *Inventory) Reserve(ctx context.Context, req ReserveRequest) (Reservation, error) {
	if err := ctx.Err(); err != nil {
		return Reservation{}, err
	}
	if req.OrderID == "" {
		return Reservation{}, ErrOrderIDRequired
	}
	if req.SKU == "" {
		return Reservation{}, ErrSKURequired
	}
	if req.Quantity <= 0 {
		return Reservation{}, ErrQuantityPositive
	}
	reservationBuffer := DefaultReservationBuffer
	if s.configured {
		reservationBuffer = s.reservationBuffer
	}
	if req.Quantity > reservationBuffer {
		return Reservation{}, ErrReservationBufferExceeded
	}
	return Reservation{
		ID:       "reservation-" + req.OrderID,
		OrderID:  req.OrderID,
		SKU:      req.SKU,
		Quantity: req.Quantity,
	}, nil
}

// ChargeRequest describes a payment requested for one order.
type ChargeRequest struct {
	// OrderID identifies the order to charge.
	OrderID string
	// AmountCents is the whole-cent amount to charge.
	AmountCents int
}

// PaymentResult is the deterministic result of a successful charge.
type PaymentResult struct {
	// ID identifies the payment.
	ID string
	// OrderID identifies the associated order.
	OrderID string
	// AmountCents is the charged whole-cent amount.
	AmountCents int
}

// Payment charges Grove Shop orders.
type Payment struct{}

// Charge validates req and returns a deterministic successful payment.
func (s *Payment) Charge(ctx context.Context, req ChargeRequest) (PaymentResult, error) {
	if err := ctx.Err(); err != nil {
		return PaymentResult{}, err
	}
	if req.OrderID == "" {
		return PaymentResult{}, ErrOrderIDRequired
	}
	if req.AmountCents <= 0 {
		return PaymentResult{}, ErrAmountPositive
	}
	return PaymentResult{
		ID:          "payment-" + req.OrderID,
		OrderID:     req.OrderID,
		AmountCents: req.AmountCents,
	}, nil
}

// ShippingRequest describes delivery requested for one order.
type ShippingRequest struct {
	// OrderID identifies the order to deliver.
	OrderID string
	// Address is the destination for the order.
	Address string
}

// Shipment is the deterministic result of successfully arranging delivery.
type Shipment struct {
	// ID identifies the shipment.
	ID string
	// OrderID identifies the associated order.
	OrderID string
	// Address is the shipment destination.
	Address string
}

// Shipping arranges delivery for Grove Shop orders.
type Shipping struct{}

// Arrange validates req and returns a deterministic shipment.
func (s *Shipping) Arrange(ctx context.Context, req ShippingRequest) (Shipment, error) {
	if err := ctx.Err(); err != nil {
		return Shipment{}, err
	}
	if req.OrderID == "" {
		return Shipment{}, ErrOrderIDRequired
	}
	if req.Address == "" {
		return Shipment{}, ErrShippingAddressRequired
	}
	return Shipment{
		ID:      "shipment-" + req.OrderID,
		OrderID: req.OrderID,
		Address: req.Address,
	}, nil
}

// CreateOrderRequest contains the business inputs needed to complete an order.
type CreateOrderRequest struct {
	// OrderID is the application-owned stable order identifier.
	OrderID string
	// SKU identifies the product to order.
	SKU string
	// Quantity is the number of units to order.
	Quantity int
	// AmountCents is the whole-cent amount to charge.
	AmountCents int
	// ShippingAddress is the order's delivery destination.
	ShippingAddress string
}

// Order is a completed Grove Shop order and its business progression.
type Order struct {
	// ID identifies the order.
	ID string
	// SKU identifies the ordered product.
	SKU string
	// Quantity is the number of ordered units.
	Quantity int
	// AmountCents is the charged whole-cent amount.
	AmountCents int
	// ShippingAddress is the delivery destination.
	ShippingAddress string
	// Status is the current workflow status.
	Status OrderStatus
	// History contains every completed status in workflow order.
	History []OrderStatus
	// Reservation is the inventory result.
	Reservation Reservation
	// Payment is the payment result.
	Payment PaymentResult
	// Shipment is the shipping result.
	Shipment Shipment
}

// Orders creates and retains Grove Shop orders using concrete business
// services.
type Orders struct {
	mu        sync.RWMutex
	inventory *Inventory
	client    *grove.Client
	payment   *Payment
	shipping  *Shipping
	orders    map[string]Order
	orderIDs  []string
}

// NewDistributedOrders creates an Orders service that invokes Inventory,
// Payment, and Shipping through Grove. It panics if client is nil.
func NewDistributedOrders(client *grove.Client) *Orders {
	if client == nil {
		panic("groveshop: distributed Orders client must be non-nil")
	}
	return &Orders{
		client: client,
		orders: make(map[string]Order),
	}
}

// NewGroveOrders creates an Orders service that invokes Inventory through
// Grove. It panics if client, payment, or shipping is nil.
func NewGroveOrders(client *grove.Client, payment *Payment, shipping *Shipping) *Orders {
	if client == nil || payment == nil || shipping == nil {
		panic("groveshop: Grove Orders dependencies must be non-nil")
	}
	return &Orders{
		client:   client,
		payment:  payment,
		shipping: shipping,
		orders:   make(map[string]Order),
	}
}

// NewOrders creates an Orders service backed by the supplied concrete
// Inventory, Payment, and Shipping services. It panics if any service is nil.
func NewOrders(inventory *Inventory, payment *Payment, shipping *Shipping) *Orders {
	if inventory == nil || payment == nil || shipping == nil {
		panic("groveshop: orders services must be non-nil")
	}
	return &Orders{
		inventory: inventory,
		payment:   payment,
		shipping:  shipping,
		orders:    make(map[string]Order),
	}
}

// Create runs the complete order workflow and retains the result for later
// inspection. Component errors are wrapped with their workflow operation.
func (s *Orders) Create(ctx context.Context, req CreateOrderRequest) (Order, error) {
	if err := ctx.Err(); err != nil {
		return Order{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if req.OrderID == "" {
		return Order{}, ErrOrderIDRequired
	}
	if _, exists := s.orders[req.OrderID]; exists {
		return Order{}, fmt.Errorf("create order %q: %w", req.OrderID, ErrOrderExists)
	}

	order := Order{
		ID:              req.OrderID,
		SKU:             req.SKU,
		Quantity:        req.Quantity,
		AmountCents:     req.AmountCents,
		ShippingAddress: req.ShippingAddress,
	}
	advance(&order, OrderCreated)

	reserveRequest := ReserveRequest{
		OrderID:  req.OrderID,
		SKU:      req.SKU,
		Quantity: req.Quantity,
	}
	var reservation Reservation
	var err error
	if s.client != nil {
		reservation, err = grove.Call[ReserveRequest, Reservation](
			ctx,
			s.client,
			ServiceInventory,
			MethodReserve,
			reserveRequest,
		)
	} else {
		reservation, err = s.inventory.Reserve(ctx, reserveRequest)
	}
	if err != nil {
		return Order{}, fmt.Errorf("reserve inventory: %w", err)
	}
	order.Reservation = reservation
	advance(&order, OrderReserved)

	chargeRequest := ChargeRequest{
		OrderID:     req.OrderID,
		AmountCents: req.AmountCents,
	}
	var payment PaymentResult
	if s.payment == nil {
		payment, err = grove.Call[ChargeRequest, PaymentResult](
			ctx,
			s.client,
			ServicePayment,
			MethodCharge,
			chargeRequest,
		)
	} else {
		payment, err = s.payment.Charge(ctx, chargeRequest)
	}
	if err != nil {
		return Order{}, fmt.Errorf("charge payment: %w", err)
	}
	order.Payment = payment
	advance(&order, OrderPaid)

	shippingRequest := ShippingRequest{
		OrderID: req.OrderID,
		Address: req.ShippingAddress,
	}
	var shipment Shipment
	if s.shipping == nil {
		shipment, err = grove.Call[ShippingRequest, Shipment](
			ctx,
			s.client,
			ServiceShipping,
			MethodArrangeShipping,
			shippingRequest,
		)
	} else {
		shipment, err = s.shipping.Arrange(ctx, shippingRequest)
	}
	if err != nil {
		return Order{}, fmt.Errorf("arrange shipping: %w", err)
	}
	order.Shipment = shipment
	advance(&order, OrderShipping)
	advance(&order, OrderCompleted)

	s.orders[order.ID] = cloneOrder(order)
	s.orderIDs = append(s.orderIDs, order.ID)
	return cloneOrder(order), nil
}

// Get returns a snapshot of a retained order.
func (s *Orders) Get(ctx context.Context, orderID string) (Order, error) {
	if err := ctx.Err(); err != nil {
		return Order{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	order, ok := s.orders[orderID]
	if !ok {
		return Order{}, fmt.Errorf("get order %q: %w", orderID, ErrOrderNotFound)
	}
	return cloneOrder(order), nil
}

// List returns snapshots of all retained orders in creation order.
func (s *Orders) List(ctx context.Context) ([]Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	orders := make([]Order, 0, len(s.orderIDs))
	for _, orderID := range s.orderIDs {
		orders = append(orders, cloneOrder(s.orders[orderID]))
	}
	return orders, nil
}

func advance(order *Order, status OrderStatus) {
	order.Status = status
	order.History = append(order.History, status)
}

func cloneOrder(order Order) Order {
	order.History = append([]OrderStatus(nil), order.History...)
	return order
}

// Web is the Web-facing Grove Shop business API. HTTP and UI serving are
// intentionally layered on top in later tasks.
type Web struct {
	orders *Orders
}

// NewWeb creates a Web business API backed by orders. It panics if orders is
// nil.
func NewWeb(orders *Orders) *Web {
	if orders == nil {
		panic("groveshop: orders must be non-nil")
	}
	return &Web{orders: orders}
}

// CreateOrder creates and retains an order through the Orders workflow.
func (w *Web) CreateOrder(ctx context.Context, req CreateOrderRequest) (Order, error) {
	return w.orders.Create(ctx, req)
}

// GetOrder returns one retained order for inspection.
func (w *Web) GetOrder(ctx context.Context, orderID string) (Order, error) {
	return w.orders.Get(ctx, orderID)
}

// ListOrders returns all retained orders in creation order.
func (w *Web) ListOrders(ctx context.Context) ([]Order, error) {
	return w.orders.List(ctx)
}
