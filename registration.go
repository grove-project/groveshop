package groveshop

import (
	"context"
	"errors"

	"github.com/grove-project/grove"
)

const (
	// ServiceOrders identifies the Grove Shop Orders service.
	ServiceOrders grove.ServiceID = 1
	// ServiceInventory identifies the Grove Shop Inventory service.
	ServiceInventory grove.ServiceID = 2
	// ServicePayment identifies the Grove Shop Payment service.
	ServicePayment grove.ServiceID = 3
	// ServiceShipping identifies the Grove Shop Shipping service.
	ServiceShipping grove.ServiceID = 4
	// ServiceWeb identifies the Grove Shop Web component.
	ServiceWeb grove.ServiceID = 5
	// ServiceLoadGen identifies the cluster-wide Grove Shop load generator.
	ServiceLoadGen grove.ServiceID = 6
)

const (
	// MethodCreateOrder identifies Orders.Create.
	MethodCreateOrder grove.MethodID = 1
	// MethodReserve identifies Inventory.Reserve.
	MethodReserve grove.MethodID = 1
	// MethodCharge identifies Payment.Charge.
	MethodCharge grove.MethodID = 1
	// MethodArrangeShipping identifies Shipping.Arrange.
	MethodArrangeShipping grove.MethodID = 1
	// MethodLoad identifies the exclusive LoadGenerator handler.
	MethodLoad grove.MethodID = 1
)

var (
	// ErrRegistryRequired is returned when registration receives a nil Registry.
	ErrRegistryRequired = errors.New("registry is required")
	// ErrServiceRequired is returned when registration receives a nil concrete
	// service implementation.
	ErrServiceRequired = errors.New("service implementation is required")
)

// RegisterOrders explicitly associates Orders.Create with the Grove Shop IDs.
func RegisterOrders(registry *grove.Registry, orders *Orders) error {
	if registry == nil {
		return ErrRegistryRequired
	}
	if orders == nil {
		return ErrServiceRequired
	}
	return registry.Register(
		ServiceOrders,
		MethodCreateOrder,
		func(ctx context.Context, payload []byte) ([]byte, error) {
			var req CreateOrderRequest
			if err := grove.Decode(payload, &req); err != nil {
				return nil, err
			}
			response, err := orders.Create(ctx, req)
			if err != nil {
				return nil, err
			}
			return grove.Encode(response)
		},
	)
}

// RegisterInventory explicitly associates Inventory.Reserve with the Grove
// Shop IDs.
func RegisterInventory(registry *grove.Registry, inventory *Inventory) error {
	if registry == nil {
		return ErrRegistryRequired
	}
	if inventory == nil {
		return ErrServiceRequired
	}
	return registry.Register(
		ServiceInventory,
		MethodReserve,
		func(ctx context.Context, payload []byte) ([]byte, error) {
			var req ReserveRequest
			if err := grove.Decode(payload, &req); err != nil {
				return nil, err
			}
			response, err := inventory.Reserve(ctx, req)
			if err != nil {
				return nil, err
			}
			return grove.Encode(response)
		},
	)
}

// RegisterPayment explicitly associates Payment.Charge with the Grove Shop
// IDs.
func RegisterPayment(registry *grove.Registry, payment *Payment) error {
	if registry == nil {
		return ErrRegistryRequired
	}
	if payment == nil {
		return ErrServiceRequired
	}
	return registry.Register(
		ServicePayment,
		MethodCharge,
		func(ctx context.Context, payload []byte) ([]byte, error) {
			var req ChargeRequest
			if err := grove.Decode(payload, &req); err != nil {
				return nil, err
			}
			response, err := payment.Charge(ctx, req)
			if err != nil {
				return nil, err
			}
			return grove.Encode(response)
		},
	)
}

// RegisterShipping explicitly associates Shipping.Arrange with the Grove Shop
// IDs.
func RegisterShipping(registry *grove.Registry, shipping *Shipping) error {
	if registry == nil {
		return ErrRegistryRequired
	}
	if shipping == nil {
		return ErrServiceRequired
	}
	return registry.Register(
		ServiceShipping,
		MethodArrangeShipping,
		func(ctx context.Context, payload []byte) ([]byte, error) {
			var req ShippingRequest
			if err := grove.Decode(payload, &req); err != nil {
				return nil, err
			}
			response, err := shipping.Arrange(ctx, req)
			if err != nil {
				return nil, err
			}
			return grove.Encode(response)
		},
	)
}

// ErrNotLoadOwner is returned when a call reaches a load generator that does
// not currently own the exclusive capability.
var ErrNotLoadOwner = errors.New("load generator is not the current owner")

// RegisterLoadGen associates the exclusive load-generator handler with the
// Grove Shop IDs. Every node hosting LoadGen registers it; Grove routes calls
// only to the current capability owner.
func RegisterLoadGen(ctx context.Context, registry *grove.Registry, generator *LoadGenerator) error {
	if registry == nil {
		return ErrRegistryRequired
	}
	if generator == nil {
		return ErrServiceRequired
	}
	return registry.Register(
		ServiceLoadGen,
		MethodLoad,
		func(_ context.Context, payload []byte) ([]byte, error) {
			var req LoadRequest
			if err := grove.Decode(payload, &req); err != nil {
				return nil, err
			}
			if !generator.Enabled() {
				return nil, ErrNotLoadOwner
			}
			if req.Apply {
				// The generator outlives the request, so it runs on the
				// component's context rather than the call's.
				generator.SetRunning(ctx, req.Running)
			}
			return grove.Encode(generator.Snapshot(req.SinceUnixMilli))
		},
	)
}
