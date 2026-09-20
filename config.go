package groveshop

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/grove-project/grove"
	"go.yaml.in/yaml/v3"
)

const (
	// DefaultConfigRevision identifies configuration produced without an
	// application-supplied revision.
	DefaultConfigRevision = "default"
	// DefaultReservationBuffer is the Inventory capacity used when the YAML
	// omits inventory.reservation_buffer.
	DefaultReservationBuffer = 100
)

var (
	// ErrConfigurationInvalid is returned when Grove Shop configuration cannot
	// be parsed or violates application-owned rules.
	ErrConfigurationInvalid = errors.New("grove shop configuration is invalid")
)

// Configuration is the immutable runtime representation owned by the Grove
// Shop artifact that compiled it.
type Configuration struct {
	Revision  string                 `yaml:"revision"`
	Customer  CustomerConfiguration  `yaml:"customer"`
	Cluster   ClusterConfiguration   `yaml:"cluster"`
	Node      NodeConfiguration      `yaml:"node"`
	Inventory InventoryConfiguration `yaml:"inventory"`
}

// CustomerConfiguration contains customer-visible Grove Shop identity.
type CustomerConfiguration struct {
	Name string `yaml:"name"`
}

// ClusterConfiguration contains immutable cluster facts.
type ClusterConfiguration struct {
	Name string `yaml:"name"`
}

// NodeConfiguration contains immutable local placement facts.
type NodeConfiguration struct {
	Zone string `yaml:"zone"`
}

// InventoryConfiguration controls deterministic Inventory behavior.
type InventoryConfiguration struct {
	ReservationBuffer int `yaml:"reservation_buffer"`
}

// ConfigurationError is a structured application-owned validation failure.
type ConfigurationError struct {
	Field   string
	Message string
}

func (e *ConfigurationError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%v: %s", ErrConfigurationInvalid, e.Message)
	}
	return fmt.Sprintf("%v: %s: %s", ErrConfigurationInvalid, e.Field, e.Message)
}

func (e *ConfigurationError) Unwrap() error {
	return ErrConfigurationInvalid
}

// DefaultConfiguration returns the complete defaults applied by the Grove
// Shop target binary before it decodes customer YAML.
func DefaultConfiguration() Configuration {
	return Configuration{
		Revision: DefaultConfigRevision,
		Customer: CustomerConfiguration{Name: "Grove Shop"},
		Cluster:  ClusterConfiguration{Name: "local"},
		Node:     NodeConfiguration{Zone: "local"},
		Inventory: InventoryConfiguration{
			ReservationBuffer: DefaultReservationBuffer,
		},
	}
}

// CompileConfigurationYAML strictly decodes YAML with this binary version's
// schema, applies its defaults, validates it, and returns canonical YAML.
func CompileConfigurationYAML(source []byte) (Configuration, []byte, error) {
	configuration := DefaultConfiguration()
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	decoder.KnownFields(true)
	if err := decoder.Decode(&configuration); err != nil && !errors.Is(err, io.EOF) {
		return Configuration{}, nil, &ConfigurationError{Message: "decode YAML: " + err.Error()}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple YAML documents are not supported")
		}
		return Configuration{}, nil, &ConfigurationError{Message: err.Error()}
	}
	normalizeConfiguration(&configuration)
	if err := ValidateConfiguration(configuration); err != nil {
		return Configuration{}, nil, err
	}
	canonical, err := yaml.Marshal(configuration)
	if err != nil {
		return Configuration{}, nil, fmt.Errorf("encode canonical Grove Shop configuration: %w", err)
	}
	return configuration, canonical, nil
}

// ValidateConfiguration applies the same semantic checks to compiled runtime
// data that the target binary applies during YAML compilation.
func ValidateConfiguration(configuration Configuration) error {
	checks := []struct {
		field string
		value string
	}{
		{field: "revision", value: configuration.Revision},
		{field: "customer.name", value: configuration.Customer.Name},
		{field: "cluster.name", value: configuration.Cluster.Name},
		{field: "node.zone", value: configuration.Node.Zone},
	}
	for _, check := range checks {
		if check.value == "" {
			return &ConfigurationError{Field: check.field, Message: "must not be empty"}
		}
	}
	if configuration.Inventory.ReservationBuffer < 0 {
		return &ConfigurationError{Field: "inventory.reservation_buffer", Message: "must be zero or greater"}
	}
	return nil
}

// EncodeConfiguration serializes the validated target-owned runtime form.
func EncodeConfiguration(configuration Configuration) ([]byte, error) {
	if err := ValidateConfiguration(configuration); err != nil {
		return nil, err
	}
	return grove.Encode(configuration)
}

// DecodeConfiguration restores and defensively validates the target-owned
// runtime form embedded in this artifact version.
func DecodeConfiguration(encoded []byte) (Configuration, error) {
	var configuration Configuration
	if err := grove.Decode(encoded, &configuration); err != nil {
		return Configuration{}, fmt.Errorf("decode compiled Grove Shop configuration: %w", err)
	}
	normalizeConfiguration(&configuration)
	if err := ValidateConfiguration(configuration); err != nil {
		return Configuration{}, err
	}
	return configuration, nil
}

func normalizeConfiguration(configuration *Configuration) {
	configuration.Revision = strings.TrimSpace(configuration.Revision)
	configuration.Customer.Name = strings.TrimSpace(configuration.Customer.Name)
	configuration.Cluster.Name = strings.TrimSpace(configuration.Cluster.Name)
	configuration.Node.Zone = strings.TrimSpace(configuration.Node.Zone)
}
