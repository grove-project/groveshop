GO ?= go
# Application YAML embedded into the built artifact.
CONFIG ?= configs/acme.yaml
BIN := ./bin
GROVE_CLI := $(BIN)/grove

.PHONY: test
test:
	$(GO) test ./...

# build compiles Grove Shop and embeds CONFIG, producing the runnable
# artifact ./bin/groveshop. Override the configuration with CONFIG=<yaml>.
.PHONY: build
build:
	mkdir -p $(BIN)
	$(GO) build -o $(BIN)/groveshop.unconfigured ./cmd/groveshop
	$(GO) build -o $(GROVE_CLI) github.com/grove-project/grove/cmd/grove
	rm -f $(BIN)/groveshop
	$(GROVE_CLI) config embed --binary $(BIN)/groveshop.unconfigured --config $(CONFIG) --output $(BIN)/groveshop
	rm -f $(BIN)/groveshop.unconfigured
