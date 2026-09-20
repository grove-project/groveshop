# Grove Shop Demo Architecture

## Application shape

```text
Browser
   |
   v
Stable Grove Ingress
   |
   v
Web
 |
 v
Orders
 |-- Inventory
 |-- Payment
 `-- Shipping
```

The business logic stays deterministic and small. The value is the Grove lifecycle around it.

## Components
### Web
- Serves embedded HTML/CSS/JavaScript assets.
- Exposes the Orders HTTP API.
- Exposes/read-proxies the Grove status endpoint.
- Runs as a Grove-managed component.

### Orders
Creates orders and coordinates Inventory, Payment, and Shipping through Grove's explicit invocation model.

### Inventory
Reserves inventory and owns the configuration value used by the intentional bad-config scenario.

### Payment
Produces a deterministic successful payment result.

### Shipping
Produces a deterministic shipping result.

## Immutable artifact contract

```text
Grove Shop artifact
├── application code
│   ├── Web
│   ├── Orders
│   ├── Inventory
│   ├── Payment
│   └── Shipping
├── embedded Web UI assets
├── Grove runtime/deployment metadata
├── Grove Shop operational actions and TUI
└── embedded customer configuration
```

There is no separate frontend deployment or production config file accompanying the deployed artifact. Configuration embedding creates a new immutable artifact identity even when application code is unchanged.

The artifact is also the operator entry point. Running it performs discovery and opens the TUI.

## Application and artifact identity
Grove distinguishes:
- **application identity** — stable across builds/config variants of Grove Shop;
- **artifact/build identity** — identifies the exact immutable artifact, including embedded configuration.

Startup interpretation:
- no matching application cluster -> bootstrap;
- same application + same artifact -> suggest node join;
- same application + different artifact -> suggest rollout.

Discovery is not itself trust/admission; normal Grove admission/security still applies.

## Runtime topology
The canonical demo uses three real Grovlet OS processes on one host. Each terminal can open the same Cluster view.

```text
Grovlet A        Grovlet B        Grovlet C
---------        ---------        ---------
Web              Inventory        Payment
Orders           Shipping
```

Placement may change during recovery and rollout. Distributed behavior must not depend on same-process shortcuts.

## Shared control-plane projection
No process-local map is authoritative cluster truth. Core NATS subjects/request-reply carry transient control/RPC traffic and JetStream/KV holds authoritative replicated control state.

The Web status pane, every TUI Cluster view, and structured automation read the same control-plane model. Node membership, placement, health, rollout progress, rollback, and activity therefore converge across all terminals.

## Stable ingress
The public Grove Shop ingress belongs to the cluster/application, not to a particular worker or artifact.

```text
Browser -> stable ingress address:port -> active/mixed-version workers
```

A rollout must not require a new public listener, URL, or client reconnect. The same ingress endpoint remains available before, during, and after rollout and rollback.

## Unified rollout model
The complete immutable artifact is the deployment unit:

```text
Artifact A = code N   + UI + config A
Artifact B = code N+1 + UI + config A   # code change
Artifact C = code N+1 + UI + config B   # config change
```

B and C use exactly the same rollout machinery. Grove may display metadata describing what differs, but deployment semantics do not branch on whether code or configuration changed.

The active known-good artifact remains authoritative while a candidate is introduced and health-gated. A failed candidate is rejected and Grove converges back to the previous complete artifact. Rollback never patches configuration in place.

The newly launched different-artifact process introduces the candidate and provides an observer/controller TUI; it must not silently increase the intended stable node count.

## Debugging topology
The final debugger proof uses one service worker per Grovlet:

```text
node-1      node-2      node-3      node-4      node-5
Web         Orders      Inventory   Payment     Shipping
```

This deterministic layout proves two independent debugger targets on different nodes. It is a demo placement, not a general affinity scheduler.
