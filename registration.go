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
	// ServiceShop identifies the cluster-wide coffee-shop simulation.
	ServiceShop grove.ServiceID = 6
	// ServiceCashier identifies the Cashier station.
	ServiceCashier grove.ServiceID = 7
	// ServiceBarista identifies the Barista station.
	ServiceBarista grove.ServiceID = 8
	// ServiceKitchen identifies the Kitchen station.
	ServiceKitchen grove.ServiceID = 9
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
	// MethodShop identifies the exclusive Shop handler.
	MethodShop grove.MethodID = 1
	// MethodTakeOrder identifies Cashier.TakeOrder.
	MethodTakeOrder grove.MethodID = 1
	// MethodMakeDrink identifies Barista.MakeDrink.
	MethodMakeDrink grove.MethodID = 1
	// MethodPrepareFood identifies Kitchen.PrepareFood.
	MethodPrepareFood grove.MethodID = 1
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

// ErrNotShopOwner is returned when a call reaches a Shop that does not
// currently own the exclusive capability.
var ErrNotShopOwner = errors.New("shop is not the current owner")

// RegisterShop associates the exclusive Shop handler with the Grove Shop IDs.
// Every node hosting Shop registers it; Grove routes calls only to the current
// capability owner, which runs the simulation.
func RegisterShop(registry *grove.Registry, shop *Shop) error {
	if registry == nil {
		return ErrRegistryRequired
	}
	if shop == nil {
		return ErrServiceRequired
	}
	return registry.Register(
		ServiceShop,
		MethodShop,
		func(_ context.Context, payload []byte) ([]byte, error) {
			var req ShopRequest
			if err := grove.Decode(payload, &req); err != nil {
				return nil, err
			}
			// A paused former owner still shows its shop: Grove only routes
			// here while it places the capability on this node.
			if !shop.Serving() {
				return nil, ErrNotShopOwner
			}
			if req.Restore != nil {
				shop.Restore(*req.Restore)
			}
			return grove.Encode(shop.Snapshot(req.SinceEvent, req.SinceHistory))
		},
	)
}
