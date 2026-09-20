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

## Remaining boundary

The current Grove Shop executable is still implemented by
`grove/cmd/grovlet`. Its console/controller, artifact lifecycle, cluster
bootstrap, placement, rollout, recovery, and debugging code import:

```text
github.com/grove-project/grove/internal/artifact
github.com/grove-project/grove/internal/bootstrap
github.com/grove-project/grove/internal/debuggateway
github.com/grove-project/grove/internal/systemnats
```

Go correctly prevents this module from importing those packages. Copying the
runtime implementation here would duplicate Grove inside its example and would
not demonstrate Grove as a package.

## Next extraction slice

Grove needs a public application-runtime boundary that owns the infrastructure
and accepts application-owned hooks. The smallest useful surface must let Grove
Shop provide:

- its stable application identity and build metadata;
- service registrations and component factories;
- configuration compile/decode hooks;
- the embedded Web handler;
- application-specific console actions.

Grove should continue to own discovery, node bootstrap/join, artifact identity,
the shared cluster read model, stable ingress, rollout/rollback, recovery,
worker supervision, and debug target resolution.

Once that boundary exists, this repository can add `cmd/groveshop` as thin
composition code and migrate the existing command/E2E tests without changing
the user flow documented in `DEMO_FLOW.md`.

## Source baseline

Application code and demo contracts were migrated from Grove `origin/main` at
`822270f161e9d83fd6f15ad08ce0a2ebae002ad6`.
