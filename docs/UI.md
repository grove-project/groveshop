# Grove Shop Demo UI

## Goal
The demo has two synchronized operational surfaces:
1. the browser, showing business continuity plus cluster status;
2. the application TUI, whose **Cluster** view can be open in every node terminal.

Both consume Grove's authoritative structured control-plane read model.

## Browser
The page keeps two persistent areas:

```text
+-----------------------------+---------------------------+
| Orders UI                   | Grove Cluster Status      |
| create / inspect orders     | nodes / placement         |
| order state                 | active + candidate        |
| business result             | rollout / rollback        |
+-----------------------------+---------------------------+
```

The Orders pane can create/list orders and show the deterministic progression:

```text
Created -> Reserved -> Paid -> Shipping -> Completed
```

The Cluster Status pane continuously polls Grove and shows cluster health, nodes, service placement/health, active and candidate artifact/build identity, embedded-config identity, deployment phase, and rollback state/reason.

Target polling interval for MVP: approximately 500 ms to 1 second. Use HTTP polling, not WebSockets/SSE, unless a later task changes this requirement.

## Stable browser continuity
The browser must stay open on the same Grove-managed ingress URL and port before, during, and after rollout/rollback.

A candidate must not replace the public endpoint. During mixed-version rollout:
- status polling continues through the same endpoint;
- existing business functionality remains reachable;
- the user can continue creating/observing orders;
- after cutover, new behavior appears through that same endpoint;
- rollback also preserves the endpoint.

No URL change, browser restart, or client reconnect to another ingress is part of the demo.

## Cluster TUI view
Every running Grove Shop terminal must be able to open a first-class **Cluster** view.

It is a cluster view, not a local-node dashboard. It must show at least:
- cluster health;
- active/candidate artifact identities;
- node membership and per-node artifact/build;
- service placement and health;
- node join/leave/failure and service movement;
- rollout/rollback phase and progress;
- recent cluster activity/events.

When several terminals are tiled, they must visibly update from the same shared state. A node join, failure, service migration, rollout start/progress/completion, or rollback must appear promptly in every surviving open Cluster view.

During a rollout, the initiating new-artifact terminal and all existing node terminals follow the same rollout state. After completion they converge on the same active artifact and healthy state.

## Ownership rule
Browser and TUI only observe/control through Grove's structured actions and read model. Health detection, candidate rejection, rollback, placement, reconciliation, and ingress ownership remain Grove responsibilities.

Neither UI may infer control-plane state by scraping logs or presentation strings.

## Serving model
The Web component is part of Grove Shop and serves embedded UI assets from the Grove cluster. There is no standalone frontend process or loose production asset directory.

## Testing
API/read-model tests and final E2E coverage must prove:
- intermediate rollout states are observable without fixed sleeps;
- multiple TUI/read-model observers converge on the same transitions;
- browser polling continues through the same ingress endpoint;
- code-only and config-only artifact rollouts present the same lifecycle model.
