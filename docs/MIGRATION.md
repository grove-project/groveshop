# Grove Shop extraction

## Goal

Keep Grove Shop's existing demo experience while making the application a real
external consumer of `github.com/grove-project/grove`.

## Migrated

The application-owned layer has moved from `grove/demo/groveshop` into this
module:

```text
Grove Shop
├── ordinary Go business services
├── Grove service and method IDs
├── explicit registration adapters
├── application console action
├── customer configuration compiler
├── embedded Web UI and HTTP API
├── demo configurations
└── tests
```

The module pins Grove's current main revision as a normal versioned dependency.
It intentionally has no local `replace` directive. This makes Go enforce the
same public-package boundary that any other Grove application sees.

## Runtime boundary

Grove now exposes a public `github.com/grove-project/grove/runtime` package:
an explicit `Definition`/`Component`/`Scenario` composition boundary that owns
infrastructure (discovery, node bootstrap/join, artifact identity, the shared
cluster read model, stable ingress, rollout/rollback, recovery, worker
supervision, and debug target resolution) while accepting application-owned
hooks for identity, service registration, configuration compile/decode, the
embedded Web handler, and console actions.

`runtimeapp/application.go` in this module is that composition: it builds a
`runtime.Definition` from this package's ordinary business services and
registers them, and `cmd/groveshop/main.go` is the resulting executable:

```go
func main() {
	groveruntime.Main(runtimeapp.RuntimeDefinition())
}
```

Grove's own `internal/*` packages remain invisible to this module — Go's
internal-package visibility rule enforces that regardless of how this module
depends on Grove, which is what makes this a real (not merely nominal)
extraction. Grove's own repository proves the same boundary from its side with
a dependency-boundary test and an external-module build smoke test.

## Source baseline

Application code and demo contracts were migrated from Grove `origin/main` at
`822270f161e9d83fd6f15ad08ce0a2ebae002ad6`; the runtime boundary above was
migrated once Grove's `runtime` package existed to support it.
