package groveshop_test

import (
	"errors"
	"testing"

	"github.com/grove-project/grove"
	"github.com/grove-project/groveshop"
)

// Application registrations must dispatch each stable ID pair to the intended
// concrete Grove Shop implementation through explicit serialization adapters.
func TestGroveShopRegistration(t *testing.T) {
	registry := &grove.Registry{}
	inventory := &groveshop.Inventory{}
	payment := &groveshop.Payment{}
	shipping := &groveshop.Shipping{}
	orders := groveshop.NewOrders(inventory, payment, shipping)

	for _, register := range []func() error{
		func() error { return groveshop.RegisterOrders(registry, orders) },
		func() error { return groveshop.RegisterInventory(registry, inventory) },
		func() error { return groveshop.RegisterPayment(registry, payment) },
		func() error { return groveshop.RegisterShipping(registry, shipping) },
	} {
		if err := register(); err != nil {
			t.Fatal(err)
		}
	}
	client, err := grove.NewClient(registry)
	if err != nil {
		t.Fatal(err)
	}

	reserved, err := grove.Call[groveshop.ReserveRequest, groveshop.Reservation](
		t.Context(),
		client,
		groveshop.ServiceInventory,
		groveshop.MethodReserve,
		groveshop.ReserveRequest{OrderID: "order-1", SKU: "coffee-beans", Quantity: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.ID != "reservation-order-1" {
		t.Errorf("Inventory handler reservation ID = %q; want reservation-order-1", reserved.ID)
	}

	charged, err := grove.Call[groveshop.ChargeRequest, groveshop.PaymentResult](
		t.Context(),
		client,
		groveshop.ServicePayment,
		groveshop.MethodCharge,
		groveshop.ChargeRequest{OrderID: "order-1", AmountCents: 2400},
	)
	if err != nil {
		t.Fatal(err)
	}
	if charged.ID != "payment-order-1" {
		t.Errorf("Payment handler payment ID = %q; want payment-order-1", charged.ID)
	}

	shipped, err := grove.Call[groveshop.ShippingRequest, groveshop.Shipment](
		t.Context(),
		client,
		groveshop.ServiceShipping,
		groveshop.MethodArrangeShipping,
		groveshop.ShippingRequest{OrderID: "order-1", Address: "12 Grove Lane"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if shipped.ID != "shipment-order-1" {
		t.Errorf("Shipping handler shipment ID = %q; want shipment-order-1", shipped.ID)
	}

	created, err := grove.Call[groveshop.CreateOrderRequest, groveshop.Order](
		t.Context(),
		client,
		groveshop.ServiceOrders,
		groveshop.MethodCreateOrder,
		groveshop.CreateOrderRequest{
			OrderID:         "order-1",
			SKU:             "coffee-beans",
			Quantity:        2,
			AmountCents:     2400,
			ShippingAddress: "12 Grove Lane",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != groveshop.OrderCompleted {
		t.Errorf("Orders handler status = %q; want %q", created.Status, groveshop.OrderCompleted)
	}

	reserve, err := registry.Resolve(groveshop.ServiceInventory, groveshop.MethodReserve)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reserve(t.Context(), []byte("not Gob")); err == nil {
		t.Error("Inventory handler accepted malformed request payload")
	} else {
		var codecErr *grove.CodecError
		if !errors.As(err, &codecErr) || codecErr.Operation != grove.CodecDecode {
			t.Errorf("Inventory handler malformed request error = %v; want decode CodecError", err)
		}
	}
}
