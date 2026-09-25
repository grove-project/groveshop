GO ?= go

.PHONY: test
test:
	$(GO) test ./...

.PHONY: build
build:
	$(GO) build -o ./bin/groveshop ./cmd/groveshop
