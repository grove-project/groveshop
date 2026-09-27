package groveshop

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"sync"

	"github.com/grove-project/grove"
)

// webFiles contains the browser application shipped in the Grove Shop
// deployment artifact.
//
//go:embed web/index.html
var webFiles embed.FS

// WebHandler serves the Grove Shop browser assets without runtime files or a
// separate frontend deployment.
func WebHandler() http.Handler {
	return WebHandlerWithConfiguration(DefaultConfiguration(), "")
}

// RuntimeConfigurationView is the non-secret immutable configuration exposed
// by Grove Shop for operators and application verification.
type RuntimeConfigurationView struct {
	Revision          string `json:"revision"`
	ConfigDigest      string `json:"config_digest,omitempty"`
	CustomerName      string `json:"customer_name"`
	ClusterName       string `json:"cluster_name"`
	NodeZone          string `json:"node_zone"`
	ReservationBuffer int    `json:"reservation_buffer"`
}

// ClusterStatusView is the structured Grove read model polled by the embedded
// browser application.
type ClusterStatusView struct {
	Health            string                `json:"health"`
	Ready             bool                  `json:"ready"`
	Nodes             []NodeStatusView      `json:"nodes"`
	Placements        []PlacementStatusView `json:"placements"`
	ActiveArtifact    *ArtifactStatusView   `json:"active_artifact,omitempty"`
	CandidateArtifact *ArtifactStatusView   `json:"candidate_artifact,omitempty"`
	Rollout           *RolloutStatusView    `json:"rollout,omitempty"`
}

// NodeStatusView reports one logical Grovlet and its locally observed
// components.
type NodeStatusView struct {
	NodeID     string                `json:"node_id"`
	Health     string                `json:"health"`
	Components []ComponentStatusView `json:"components"`
	Error      string                `json:"error,omitempty"`
}

// ComponentStatusView reports one Grove-managed component process.
type ComponentStatusView struct {
	ServiceID grove.ServiceID `json:"service_id"`
	Name      string          `json:"name"`
	WorkerID  string          `json:"worker_id"`
	State     string          `json:"state"`
	Error     string          `json:"error,omitempty"`
}

// PlacementStatusView reports the authoritative destination and observed
// health of one application service.
type PlacementStatusView struct {
	ServiceID         grove.ServiceID `json:"service_id"`
	Name              string          `json:"name"`
	NodeID            string          `json:"node_id"`
	InvocationSubject string          `json:"invocation_subject"`
	ArtifactDigest    string          `json:"artifact_digest"`
	Health            string          `json:"health"`
}

// ArtifactStatusView is the immutable code and configuration identity shown
// for the active or candidate deployment.
type ArtifactStatusView struct {
	ApplicationID  string `json:"application_id"`
	CodeVersion    string `json:"code_version"`
	ArtifactDigest string `json:"artifact_digest"`
	ConfigRevision string `json:"config_revision"`
	ConfigDigest   string `json:"config_digest"`
}

// RolloutStatusView is the latest durable rollout generation.
type RolloutStatusView struct {
	Generation uint64              `json:"generation"`
	Phase      string              `json:"phase"`
	Failure    *RolloutFailureView `json:"failure,omitempty"`
}

// RolloutFailureView is the machine-readable reason a candidate was rejected.
type RolloutFailureView struct {
	Code      string `json:"code"`
	Component string `json:"component,omitempty"`
	Field     string `json:"field,omitempty"`
	Message   string `json:"message"`
}

// StatusReader returns the latest Grove control-plane read model.
type StatusReader func(context.Context) (ClusterStatusView, error)

// OrderCreator runs one Grove Shop order through the explicit Grove call path.
type OrderCreator func(context.Context, CreateOrderRequest) (Order, error)

// WebHandlerWithConfiguration serves embedded assets and a read-only view of
// the compiled configuration observed by this exact artifact version.
func WebHandlerWithConfiguration(configuration Configuration, configDigest string) http.Handler {
	return WebHandlerWithRuntime(configuration, configDigest, nil, nil)
}

// WebHandlerWithRuntime serves the embedded application, its order API, and
// the read-only Grove status model supplied by the hosting Grovlet.
func WebHandlerWithRuntime(
	configuration Configuration,
	configDigest string,
	readStatus StatusReader,
	createOrder OrderCreator,
) http.Handler {
	return WebHandlerWithLoad(configuration, configDigest, readStatus, createOrder, nil)
}

// WebHandlerWithLoad additionally serves the load/scaling/recovery demo API
// backed by monitor. A nil monitor disables it.
func WebHandlerWithLoad(
	configuration Configuration,
	configDigest string,
	readStatus StatusReader,
	createOrder OrderCreator,
	monitor *LoadMonitor,
) http.Handler {
	root, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	view := RuntimeConfigurationView{
		Revision:          configuration.Revision,
		ConfigDigest:      configDigest,
		CustomerName:      configuration.Customer.Name,
		ClusterName:       configuration.Cluster.Name,
		NodeZone:          configuration.Node.Zone,
		ReservationBuffer: configuration.Inventory.ReservationBuffer,
	}
	var (
		ordersMu sync.RWMutex
		orders   []Order
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /grove/config", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, view)
	})
	mux.HandleFunc("GET /grove/status", func(response http.ResponseWriter, request *http.Request) {
		if readStatus == nil {
			http.Error(response, "Grove status is unavailable", http.StatusServiceUnavailable)
			return
		}
		status, err := readStatus(request.Context())
		if err != nil {
			http.Error(response, "read Grove status: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		writeJSON(response, http.StatusOK, status)
	})
	mux.HandleFunc("GET /api/orders", func(response http.ResponseWriter, _ *http.Request) {
		ordersMu.RLock()
		view := make([]Order, len(orders))
		for i, order := range orders {
			view[i] = cloneOrder(order)
		}
		ordersMu.RUnlock()
		writeJSON(response, http.StatusOK, view)
	})
	mux.HandleFunc("POST /api/orders", func(response http.ResponseWriter, request *http.Request) {
		if createOrder == nil {
			http.Error(response, "Grove Shop orders are unavailable", http.StatusServiceUnavailable)
			return
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var input CreateOrderRequest
		if err := decoder.Decode(&input); err != nil {
			http.Error(response, "decode order: "+err.Error(), http.StatusBadRequest)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			http.Error(response, "decode order: trailing JSON content", http.StatusBadRequest)
			return
		}
		order, err := createOrder(request.Context(), input)
		if err != nil {
			http.Error(response, "create order: "+err.Error(), http.StatusBadGateway)
			return
		}
		ordersMu.Lock()
		orders = append(orders, cloneOrder(order))
		ordersMu.Unlock()
		writeJSON(response, http.StatusCreated, order)
	})
	mux.HandleFunc("GET /api/load", func(response http.ResponseWriter, _ *http.Request) {
		if monitor == nil {
			http.Error(response, "load generator is unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(response, http.StatusOK, monitor.View())
	})
	mux.HandleFunc("POST /api/load", func(response http.ResponseWriter, request *http.Request) {
		if monitor == nil {
			http.Error(response, "load generator is unavailable", http.StatusServiceUnavailable)
			return
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var input struct {
			Running bool `json:"running"`
		}
		if err := decoder.Decode(&input); err != nil {
			http.Error(response, "decode load request: "+err.Error(), http.StatusBadRequest)
			return
		}
		view, err := monitor.SetDesired(request.Context(), input.Running)
		if err != nil {
			http.Error(response, "set load: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(response, http.StatusOK, view)
	})
	mux.Handle("GET /", http.FileServer(http.FS(root)))
	return mux
}

// WebAsset returns one embedded Grove Shop browser asset for artifact tests and
// tooling.
func WebAsset(name string) ([]byte, error) {
	return webFiles.ReadFile("web/" + name)
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
