package groveshop_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/grove-project/groveshop"
)

func TestCompileConfigurationYAML(t *testing.T) {
	source := []byte("revision: acme-r42\ncustomer:\n  name: Acme Retail\nnode:\n  zone: cloud\ninventory:\n  reservation_buffer: 7\n")
	configuration, canonical, err := groveshop.CompileConfigurationYAML(source)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Revision != "acme-r42" || configuration.Customer.Name != "Acme Retail" || configuration.Node.Zone != "cloud" || configuration.Inventory.ReservationBuffer != 7 {
		t.Errorf("compiled configuration = %#v", configuration)
	}
	if configuration.Cluster.Name != "local" {
		t.Errorf("default cluster name = %q; want local", configuration.Cluster.Name)
	}
	if !strings.Contains(string(canonical), "name: local") || !strings.Contains(string(canonical), "reservation_buffer: 7") {
		t.Errorf("canonical YAML = %q", canonical)
	}
	encoded, err := groveshop.EncodeConfiguration(configuration)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := groveshop.DecodeConfiguration(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != configuration {
		t.Errorf("decoded configuration = %#v; want %#v", decoded, configuration)
	}
}

func TestCompileConfigurationYAMLAppliesDefaults(t *testing.T) {
	configuration, canonical, err := groveshop.CompileConfigurationYAML(nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := groveshop.DefaultConfiguration(); configuration != want {
		t.Errorf("empty YAML configuration = %#v; want %#v", configuration, want)
	}
	if len(canonical) == 0 {
		t.Fatal("canonical default YAML is empty")
	}
}

func TestCompileConfigurationYAMLRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		source string
		field  string
	}{
		{name: "negative buffer", source: "inventory:\n  reservation_buffer: -1\n", field: "inventory.reservation_buffer"},
		{name: "empty zone", source: "node:\n  zone: '  '\n", field: "node.zone"},
		{name: "unknown field", source: "inventory:\n  capacity: 10\n", field: "field capacity not found"},
		{name: "multiple documents", source: "---\nrevision: one\n---\nrevision: two\n", field: "multiple YAML documents"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := groveshop.CompileConfigurationYAML([]byte(test.source))
			if !errors.Is(err, groveshop.ErrConfigurationInvalid) {
				t.Fatalf("CompileConfigurationYAML() error = %v; want %v", err, groveshop.ErrConfigurationInvalid)
			}
			if !strings.Contains(err.Error(), test.field) {
				t.Errorf("CompileConfigurationYAML() error = %q; want field %q", err, test.field)
			}
		})
	}
}

func TestDecodeConfigurationRejectsInvalidRuntimeData(t *testing.T) {
	if _, err := groveshop.DecodeConfiguration([]byte("not gob")); err == nil {
		t.Fatal("malformed compiled configuration returned nil error")
	}
	configuration := groveshop.DefaultConfiguration()
	configuration.Inventory.ReservationBuffer = -1
	if _, err := groveshop.EncodeConfiguration(configuration); !errors.Is(err, groveshop.ErrConfigurationInvalid) {
		t.Errorf("EncodeConfiguration() error = %v; want %v", err, groveshop.ErrConfigurationInvalid)
	}
}
