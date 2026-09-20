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

The repository currently contains the first migration slice:

- Grove Shop business services and deterministic order flow;
- explicit Grove service and method registration;
- the application-owned console action;
- strict customer configuration compilation and validation;
- the embedded Orders and Cluster Status Web UI;
- good and intentionally broken demo configurations;
- unit and integration tests running against Grove as a versioned dependency;
- the complete target demo contract under [`docs/`](docs/DEMO_FLOW.md).

The executable runtime is the remaining extraction boundary. Today it is
implemented in Grove's `cmd/grovlet` command and imports Grove-private
`internal/*` packages, so it cannot yet be built by this external module. See
[`docs/MIGRATION.md`](docs/MIGRATION.md) for the boundary and next steps. The
target bootstrap, join, rollout, rollback, stable-ingress, recovery, and native
debugging flow remains unchanged.

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
