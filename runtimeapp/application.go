package runtimeapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/grove-project/grove"
	"github.com/grove-project/groveshop"
	groveruntime "github.com/grove-project/grove/runtime"
	"go.yaml.in/yaml/v3"
)

const artifactManifest = `{"format_version":1,"application_id":"grove-shop","code_version":"v0.1.0-dev","components":[{"service_id":1,"name":"Orders","runtime":"process","entrypoint":["worker","--component","orders"]},{"service_id":2,"name":"Inventory","runtime":"process","entrypoint":["worker","--component","inventory"]},{"service_id":3,"name":"Payment","runtime":"process","entrypoint":["worker","--component","payment"]},{"service_id":4,"name":"Shipping","runtime":"process","entrypoint":["worker","--component","shipping"]},{"service_id":5,"name":"Web","runtime":"process","entrypoint":["worker","--component","web"]},{"service_id":6,"name":"LoadGen","runtime":"process","entrypoint":["worker","--component","loadgen"]}],"ui_assets":["web/index.html"],"config_region":{"format_version":1,"capacity":4096}}`

var embeddedArtifact = groveruntime.ArtifactManifestPrefix + artifactManifest + groveruntime.ArtifactManifestSuffix +
	groveruntime.ArtifactConfigPrefix + groveruntime.BlankConfigRegion + groveruntime.ArtifactConfigSuffix

// RuntimeDefinition composes Grove Shop with Grove's public application
// runtime. All business implementations, IDs, configuration, Web behavior,
// and application actions remain owned by this package.
func RuntimeDefinition() groveruntime.Definition {
	return groveruntime.Definition{
		Name:             "Grove Shop",
		ApplicationID:    "grove-shop",
		EmbeddedArtifact: &embeddedArtifact,
		Configuration: groveruntime.ConfigurationProvider{
			Default: runtimeDefaultConfiguration,
			Compile: runtimeCompileConfiguration,
			Decode:  runtimeDecodeConfiguration,
			Failure: runtimeConfigurationFailure,
		},
		Components: []groveruntime.Component{
			{ServiceID: groveshop.ServiceOrders, Name: "Orders", Kind: "orders", Register: registerRuntimeOrders,
				Handlers: []groveruntime.HandlerSpec{{Method: groveshop.MethodCreateOrder, Name: "Create"}}},
			{ServiceID: groveshop.ServiceInventory, Name: "Inventory", Kind: "inventory", Register: registerRuntimeInventory,
				Handlers: []groveruntime.HandlerSpec{{Method: groveshop.MethodReserve, Name: "Reserve"}}},
			{ServiceID: groveshop.ServicePayment, Name: "Payment", Kind: "payment", Register: registerRuntimePayment,
				Handlers: []groveruntime.HandlerSpec{{Method: groveshop.MethodCharge, Name: "Charge"}}},
			{ServiceID: groveshop.ServiceShipping, Name: "Shipping", Kind: "shipping", Register: registerRuntimeShipping,
				Handlers: []groveruntime.HandlerSpec{{Method: groveshop.MethodArrangeShipping, Name: "Arrange"}}},
			{ServiceID: groveshop.ServiceWeb, Name: "Web", Kind: "web", HTTPHandler: runtimeWebHandler},
			{ServiceID: groveshop.ServiceLoadGen, Name: "LoadGen", Kind: "loadgen", Register: registerRuntimeLoadGen,
				Handlers: []groveruntime.HandlerSpec{{Method: groveshop.MethodLoad, Name: "Load", Exclusive: true, Capability: groveshop.LoadGenCapability}}},
		},
		RegisterActions: groveshop.RegisterActions,
		IntegrityAction: groveshop.ActionVerifyOrders,
		Scenario: &groveruntime.Scenario{
			NodeCount:      3,
			DebugNodeCount: 5,
			InitialPlacements: []groveruntime.ScenarioPlacement{
				{ServiceID: groveshop.ServiceOrders, NodeID: "node-1"},
				{ServiceID: groveshop.ServiceInventory, NodeID: "node-2"},
				{ServiceID: groveshop.ServiceWeb, NodeID: "node-1"},
				{ServiceID: groveshop.ServiceLoadGen, NodeID: "node-1"},
			},
			StartupComponents: startupComponents(),
			DebugPlacements: []groveruntime.ScenarioPlacement{
				{ServiceID: groveshop.ServiceWeb, NodeID: "node-1"},
				{ServiceID: groveshop.ServiceOrders, NodeID: "node-2", Options: []string{"distributed"}},
				{ServiceID: groveshop.ServiceInventory, NodeID: "node-3"},
				{ServiceID: groveshop.ServicePayment, NodeID: "node-4"},
				{ServiceID: groveshop.ServiceShipping, NodeID: "node-5"},
			},
			RecoveryServiceID: groveshop.ServiceInventory,
			Probe:             runtimeOrderProbe,
			ProbeHealthy:      runtimeOrderHealthy,
			ProbeSummary:      runtimeOrderSummary,
			InvalidConfig:     runtimeInvalidConfiguration,
		},
	}
}

// startupComponents hosts every processing component on every node, so each
// node that joins adds a placement for every ordinary handler. Web stays on
// node-1 as the ingress, and LoadGen competes for its exclusive capability from
// every node.
func startupComponents() []groveruntime.ScenarioPlacement {
	var placements []groveruntime.ScenarioPlacement
	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		label := "node=" + nodeID
		placements = append(placements,
			groveruntime.ScenarioPlacement{ServiceID: groveshop.ServiceOrders, NodeID: nodeID, Options: []string{"distributed", label}},
			groveruntime.ScenarioPlacement{ServiceID: groveshop.ServiceInventory, NodeID: nodeID, Options: []string{label}},
			groveruntime.ScenarioPlacement{ServiceID: groveshop.ServicePayment, NodeID: nodeID, Options: []string{label}},
			groveruntime.ScenarioPlacement{ServiceID: groveshop.ServiceShipping, NodeID: nodeID, Options: []string{label}},
			groveruntime.ScenarioPlacement{ServiceID: groveshop.ServiceLoadGen, NodeID: nodeID, Options: []string{label}},
		)
		if nodeID == "node-1" {
			placements = append(placements, groveruntime.ScenarioPlacement{ServiceID: groveshop.ServiceWeb, NodeID: nodeID})
		}
	}
	return placements
}

func runtimeDefaultConfiguration() (groveruntime.Configuration, error) {
	configuration := groveshop.DefaultConfiguration()
	payload, err := groveshop.EncodeConfiguration(configuration)
	if err != nil {
		return groveruntime.Configuration{}, err
	}
	return runtimeConfiguration(configuration, payload, nil), nil
}

func runtimeCompileConfiguration(source []byte) (groveruntime.Configuration, error) {
	configuration, canonical, err := groveshop.CompileConfigurationYAML(source)
	if err != nil {
		return groveruntime.Configuration{}, err
	}
	payload, err := groveshop.EncodeConfiguration(configuration)
	if err != nil {
		return groveruntime.Configuration{}, err
	}
	return runtimeConfiguration(configuration, payload, canonical), nil
}

func runtimeDecodeConfiguration(payload []byte) (groveruntime.Configuration, error) {
	configuration, err := groveshop.DecodeConfiguration(payload)
	if err != nil {
		return groveruntime.Configuration{}, err
	}
	return runtimeConfiguration(configuration, payload, nil), nil
}

func runtimeConfiguration(configuration groveshop.Configuration, payload, canonical []byte) groveruntime.Configuration {
	return groveruntime.Configuration{
		Value:         configuration,
		Revision:      configuration.Revision,
		Encoding:      "gob",
		Payload:       payload,
		CanonicalYAML: canonical,
		Facts: map[string]string{
			"cluster.name": configuration.Cluster.Name,
			"node.zone":    configuration.Node.Zone,
		},
	}
}

func runtimeConfigurationFailure(err error) (string, string) {
	var validation *groveshop.ConfigurationError
	if errors.As(err, &validation) {
		return validation.Field, validation.Message
	}
	return "", err.Error()
}

// nodeOption returns the "node=<id>" label the startup layout gives every
// component, or "unknown".
func nodeOption(options []string) string {
	for _, option := range options {
		if id, ok := strings.CutPrefix(option, "node="); ok {
			return id
		}
	}
	return "unknown"
}

func registerRuntimeOrders(ctx groveruntime.ComponentContext) error {
	var orders *groveshop.Orders
	if ctx.Client == nil {
		orders = groveshop.NewOrders(&groveshop.Inventory{}, &groveshop.Payment{}, &groveshop.Shipping{})
	} else if slices.Contains(ctx.Options, "distributed") {
		orders = groveshop.NewDistributedOrders(ctx.Client)
	} else {
		orders = groveshop.NewGroveOrders(ctx.Client, &groveshop.Payment{}, &groveshop.Shipping{})
	}
	orders.Node = nodeOption(ctx.Options)
	return groveshop.RegisterOrders(ctx.Registry, orders)
}

// registerRuntimeLoadGen hosts the single cluster-wide load generator. It
// drives orders through the same Orders service the Web component calls.
func registerRuntimeLoadGen(ctx groveruntime.ComponentContext) error {
	if ctx.Client == nil {
		return errors.New("Grove Shop load generator requires a Grove client")
	}
	generator := groveshop.NewLoadGenerator(nodeOption(ctx.Options), func(callCtx context.Context, request groveshop.CreateOrderRequest) (groveshop.Order, error) {
		return grove.Call[groveshop.CreateOrderRequest, groveshop.Order](callCtx, ctx.Client, groveshop.ServiceOrders, groveshop.MethodCreateOrder, request)
	})
	// Every node hosting LoadGen competes for the exclusive capability; only
	// the owner generates load, and ownership moves if its node dies.
	generator.SetEnabled(false)
	go generator.Run(ctx.Context)
	go generator.RunOwned(ctx.Context)
	return groveshop.RegisterLoadGen(ctx.Context, ctx.Registry, generator)
}

func registerRuntimeInventory(ctx groveruntime.ComponentContext) error {
	configuration, ok := ctx.Configuration.(groveshop.Configuration)
	if !ok {
		return errors.New("Grove Shop runtime configuration has an unexpected type")
	}
	inventory := groveshop.NewInventory(configuration.Inventory.ReservationBuffer)
	inventory.Node = nodeOption(ctx.Options)
	return groveshop.RegisterInventory(ctx.Registry, inventory)
}

func registerRuntimePayment(ctx groveruntime.ComponentContext) error {
	return groveshop.RegisterPayment(ctx.Registry, &groveshop.Payment{Node: nodeOption(ctx.Options)})
}

func registerRuntimeShipping(ctx groveruntime.ComponentContext) error {
	return groveshop.RegisterShipping(ctx.Registry, &groveshop.Shipping{Node: nodeOption(ctx.Options)})
}

func runtimeWebHandler(ctx groveruntime.ComponentContext) (http.Handler, error) {
	configuration, ok := ctx.Configuration.(groveshop.Configuration)
	if !ok {
		return nil, errors.New("Grove Shop runtime configuration has an unexpected type")
	}
	readStatus := func(callCtx context.Context) (groveshop.ClusterStatusView, error) {
		status, err := ctx.ReadStatus(callCtx)
		if err != nil {
			return groveshop.ClusterStatusView{}, err
		}
		return groveShopStatus(status), nil
	}
	createOrder := func(callCtx context.Context, request groveshop.CreateOrderRequest) (groveshop.Order, error) {
		return grove.Call[groveshop.CreateOrderRequest, groveshop.Order](callCtx, ctx.Client, groveshop.ServiceOrders, groveshop.MethodCreateOrder, request)
	}
	callLoad := func(callCtx context.Context, request groveshop.LoadRequest) (groveshop.LoadSnapshot, error) {
		return grove.Call[groveshop.LoadRequest, groveshop.LoadSnapshot](callCtx, ctx.Client, groveshop.ServiceLoadGen, groveshop.MethodLoad, request)
	}
	load := groveshop.LoadClient{
		Set: func(callCtx context.Context, running bool) (groveshop.LoadSnapshot, error) {
			return callLoad(callCtx, groveshop.LoadRequest{Apply: true, Running: running})
		},
		Snapshot: func(callCtx context.Context, since int64) (groveshop.LoadSnapshot, error) {
			return callLoad(callCtx, groveshop.LoadRequest{SinceUnixMilli: since})
		},
	}
	monitor := groveshop.NewLoadMonitor(load, readStatus)
	go monitor.Run(ctx.Context)
	return groveshop.WebHandlerWithLoad(configuration, ctx.ConfigDigest, readStatus, createOrder, monitor), nil
}

func groveShopStatus(status groveruntime.ClusterStatus) groveshop.ClusterStatusView {
	view := groveshop.ClusterStatusView{Health: status.Health, Ready: status.Ready}
	view.Nodes = make([]groveshop.NodeStatusView, 0, len(status.Nodes))
	for _, node := range status.Nodes {
		converted := groveshop.NodeStatusView{NodeID: node.NodeID, Health: node.Health, Error: node.Error}
		for _, component := range node.Components {
			converted.Components = append(converted.Components, groveshop.ComponentStatusView{
				ServiceID: component.ServiceID, Name: component.Name, WorkerID: component.WorkerID,
				State: component.State, Error: component.Error,
			})
		}
		view.Nodes = append(view.Nodes, converted)
	}
	for _, placement := range status.Placements {
		view.Placements = append(view.Placements, groveshop.PlacementStatusView{
			ServiceID: placement.ServiceID, Name: placement.Name, NodeID: placement.NodeID,
			InvocationSubject: placement.InvocationSubject, ArtifactDigest: placement.ArtifactDigest, Health: placement.Health,
		})
	}
	view.ActiveArtifact = groveShopArtifactStatus(status.ActiveArtifact)
	view.CandidateArtifact = groveShopArtifactStatus(status.CandidateArtifact)
	if status.Rollout != nil {
		view.Rollout = &groveshop.RolloutStatusView{Generation: status.Rollout.Generation, Phase: status.Rollout.Phase}
		if status.Rollout.Failure != nil {
			view.Rollout.Failure = &groveshop.RolloutFailureView{
				Code: status.Rollout.Failure.Code, Component: status.Rollout.Failure.Component,
				Field: status.Rollout.Failure.Field, Message: status.Rollout.Failure.Message,
			}
		}
	}
	return view
}

func groveShopArtifactStatus(status *groveruntime.ArtifactStatus) *groveshop.ArtifactStatusView {
	if status == nil {
		return nil
	}
	return &groveshop.ArtifactStatusView{
		ApplicationID: status.ApplicationID, CodeVersion: status.CodeVersion,
		ArtifactDigest: status.ArtifactDigest, ConfigRevision: status.ConfigRevision, ConfigDigest: status.ConfigDigest,
	}
}

func runtimeOrderProbe(ctx context.Context, webAddress, orderID string) (any, error) {
	requestBody, err := json.Marshal(groveshop.CreateOrderRequest{
		OrderID: orderID, SKU: "coffee-beans", Quantity: 1,
		AmountCents: 1200, ShippingAddress: "31 Grove Lane",
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+webAddress+"/api/orders", bytes.NewReader(requestBody))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		return nil, fmt.Errorf("POST /api/orders: %s: %s", response.Status, body)
	}
	var order groveshop.Order
	if err := json.NewDecoder(response.Body).Decode(&order); err != nil {
		return nil, err
	}
	return order, nil
}

func runtimeOrderHealthy(value any) bool {
	order, ok := value.(groveshop.Order)
	return ok && order.Status == groveshop.OrderCompleted && slices.Equal(order.History, []groveshop.OrderStatus{
		groveshop.OrderCreated, groveshop.OrderReserved, groveshop.OrderPaid, groveshop.OrderShipping, groveshop.OrderCompleted,
	})
}

func runtimeOrderSummary(value any) string {
	if order, ok := value.(groveshop.Order); ok {
		return string(order.Status)
	}
	return fmt.Sprintf("%v", value)
}

func runtimeInvalidConfiguration(source []byte) (groveruntime.Configuration, string, string, error) {
	configuration := groveshop.DefaultConfiguration()
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	decoder.KnownFields(true)
	if err := decoder.Decode(&configuration); err != nil {
		return groveruntime.Configuration{}, "", "", fmt.Errorf("decode candidate configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return groveruntime.Configuration{}, "", "", errors.New("candidate configuration contains multiple YAML documents")
	}
	validationErr := groveshop.ValidateConfiguration(configuration)
	var validation *groveshop.ConfigurationError
	if validationErr == nil || !errors.As(validationErr, &validation) {
		return groveruntime.Configuration{}, "", "", errors.New("candidate configuration must violate Grove Shop validation")
	}
	payload, err := grove.Encode(configuration)
	if err != nil {
		return groveruntime.Configuration{}, "", "", fmt.Errorf("encode invalid candidate configuration: %w", err)
	}
	return runtimeConfiguration(configuration, payload, source), validation.Field, validation.Message, nil
}
