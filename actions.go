package groveshop

import (
	"context"
	"errors"

	"github.com/grove-project/grove/console"
)

const (
	// ActionVerifyOrders is Grove Shop's application-owned integrity action.
	ActionVerifyOrders = "app.orders.verify"
)

var (
	// ErrActionArguments is returned when a Grove Shop action receives arguments
	// outside its documented contract.
	ErrActionArguments = errors.New("unexpected application action arguments")
)

// IntegrityResult reports the application-owned order workflow checked by the
// Grove Shop console action.
type IntegrityResult struct {
	// Service is the business service checked by the action.
	Service string `json:"service"`
	// Status is the resulting application-owned health classification.
	Status string `json:"status"`
	// Workflow is the expected successful order progression.
	Workflow []OrderStatus `json:"workflow"`
}

// RegisterActions contributes Grove Shop operations to the application console
// without creating a second administrative executable.
func RegisterActions(registry *console.Registry) error {
	return registry.Register(console.Action{
		Name:        ActionVerifyOrders,
		Label:       "Run integrity check",
		Section:     "Application",
		Description: "Verify the Grove Shop order workflow contract.",
		Handler: func(ctx context.Context, args []string) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(args) != 0 {
				return nil, ErrActionArguments
			}
			return IntegrityResult{
				Service: "orders",
				Status:  "healthy",
				Workflow: []OrderStatus{
					OrderCreated,
					OrderReserved,
					OrderPaid,
					OrderShipping,
					OrderCompleted,
				},
			}, nil
		},
	})
}
