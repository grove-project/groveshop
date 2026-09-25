# Grove Shop

Grove Shop is the standalone reference application for
[Grove](https://github.com/grove-project/grove). It lives in its own Go module
so its service code, registration, configuration, application actions, and Web
UI consume Grove exactly as another application would.

```bash
go test ./...
```

The application owns ordinary Go services and explicitly connects them to
Grove:

```go
inventory := &groveshop.Inventory{}
registry := &grove.Registry{}

if err := groveshop.RegisterInventory(registry, inventory); err != nil {
    log.Fatal(err)
}
```

The repository contains the complete extraction:

- Grove Shop business services and deterministic order flow;
- explicit Grove service and method registration;
- the application-owned console action;
- strict customer configuration compilation and validation;
- the embedded Orders and Cluster Status Web UI;
- good and intentionally broken demo configurations;
- unit and integration tests running against Grove as a versioned dependency;
- `runtimeapp`, the explicit composition boundary that connects this
  application to Grove's public `runtime` package;
- `cmd/groveshop`, the executable that starts Grove Shop through Grove;
- the complete target demo contract under [`docs/`](docs/DEMO_FLOW.md).

Grove Shop consumes Grove entirely through public packages
(`github.com/grove-project/grove`, `.../console`, `.../runtime`) — no
Grove-internal package, type, or business assumption is required. Build and
run the same binary Grove starts in a cluster:

```bash
go build -o ./bin/groveshop ./cmd/groveshop
./bin/groveshop
```

See [`docs/MIGRATION.md`](docs/MIGRATION.md) for how this boundary was
proven from the Grove side.

## Demo contract

The completed human flow remains one application binary:

```text
run artifact -> bootstrap cluster
run same artifact -> join
run same artifact -> join

kill node -> watch shared Cluster views -> service recovers

run new artifact -> rollout suggested
browser stays on the same endpoint

run artifact with broken config -> failed candidate -> automatic rollback
```

See [the full demo flow](docs/DEMO_FLOW.md),
[architecture](docs/ARCHITECTURE.md), and
[debugging walkthrough](docs/DEBUGGING_DEMO.md).
