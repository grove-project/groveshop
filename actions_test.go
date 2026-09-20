package groveshop_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/grove-project/grove/console"
	"github.com/grove-project/groveshop"
)

func TestRegisterActionsExposesOrderIntegrity(t *testing.T) {
	var registry console.Registry
	if err := groveshop.RegisterActions(&registry); err != nil {
		t.Fatal(err)
	}
	result, err := registry.Invoke(t.Context(), groveshop.ActionVerifyOrders, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := groveshop.IntegrityResult{
		Service: "orders",
		Status:  "healthy",
		Workflow: []groveshop.OrderStatus{
			groveshop.OrderCreated,
			groveshop.OrderReserved,
			groveshop.OrderPaid,
			groveshop.OrderShipping,
			groveshop.OrderCompleted,
		},
	}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("order integrity action = %#v; want %#v", result, want)
	}
	if _, err := registry.Invoke(t.Context(), groveshop.ActionVerifyOrders, []string{"extra"}); !errors.Is(err, groveshop.ErrActionArguments) {
		t.Errorf("order integrity arguments error = %v; want %v", err, groveshop.ErrActionArguments)
	}
}
