# Grove Shop native TUI debugging demo

## Purpose

This is the canonical human-facing debugging experience for the Grove Shop demo.

The primary experience is **inside the Grove Shop TUI**. External Delve/DAP endpoints remain supported for IDEs and automated acceptance, but they are a second frontend to the same Grove debug manager.

The demo should prove a stronger abstraction than "attach Delve to a remote process":

> Debug the distributed application, not the machines.

The operator must not need repository familiarity, PIDs, node addresses, worker ports, or prior knowledge of where a service is running.

## Debug-capable artifact

The Grove Shop debug build embeds the exact application source used to build the artifact, together with a source/symbol manifest that maps registered Grove services and methods to their implementation.

For MVP, source-code protection is explicitly out of scope. The source bundle may be stored directly in the application artifact. Protection, encryption, authorization, and source-redaction policy can be added later without changing the debugging UX.

Build debugging code with compiler optimizations/inlining disabled where required for reliable Delve breakpoints:

```bash
mkdir -p bin
go build -gcflags="all=-N -l" -o ./bin/groveshop ./cmd/grovlet
```

The resulting binary contains the application, Grove runtime, TUI, embedded configuration, embedded source/debug metadata, and the code necessary to expose Delve/DAP sessions.

## Place debugging at the end of the lifecycle demo

The debugging sequence is the payoff after the lifecycle demo has already shown:

1. start the first Grove Shop node from the application binary;
2. start the same binary in two more terminals and join the discovered cluster, for three total nodes;
3. open the shared Cluster view in the terminals and show service placement across nodes;
4. place an order and prove the distributed application works;
5. build a new Grove Shop binary containing a visible application feature and start it;
6. let the different-build startup flow suggest rollout to the same logical cluster;
7. follow rollout progress from the initiating TUI and the already-open Cluster views while the Grove-managed ingress port remains unchanged;
8. use the new feature through the same browser endpoint;
9. kill one node;
10. show the service(s) previously hosted there moving to eligible surviving nodes;
11. place another order and prove the upgraded application still works;
12. enter the native Debug experience and debug services now placed on different nodes without changing debugging workflow.

The debug demo must use the cluster's **current placement after recovery**, rather than relying on hard-coded node numbers.

## Runtime-guided Debug view

Grove already knows the semantic application boundaries because application operations are registered with Grove and cross-service calls use `grove.Call`.

The TUI must combine that semantic graph with the Grove calls actually observed during a real request. After the operator places an order, Debug should be able to show the application-level flow that executed, for example:

```text
GROVE / DEBUG / LAST FLOW

web.PlaceOrder                         node-1
      |
      v
orders.CreateOrder                     node-2
      |
      +----> inventory.Reserve         node-1
      |
      +----> payments.Charge           node-2
```

The exact node assignments are illustrative. They must reflect current runtime placement.

This is the primary entry point for an operator who does not know the application source. Grove should teach the operator which meaningful application functions participated in the flow.

Registered/executed Grove operations are therefore natural breakpoint suggestions. Do not require profiling heuristics to discover these primary application functions.

## Grove breakpoints and source breakpoints

Expose two conceptual breakpoint levels:

```text
◆ Grove breakpoint    payments.Charge
● Source breakpoint   stripe.go:143
```

A Grove breakpoint means "stop when this registered Grove operation executes." Grove resolves the operation to the current worker, debug symbol, source location, and Delve breakpoint.

A source breakpoint targets an arbitrary source line and is the deeper developer-oriented mechanism.

The operator should normally begin with Grove breakpoints from the observed flow. Source breakpoints become available after opening the embedded source.

A Grove breakpoint is identified by the semantic operation identity, not fundamentally by a hard-coded source line or node. Grove resolves it against the active artifact and current placement.

## Source navigation

Source is the center pane of the native debugger. Normal navigation should feel like a keyboard-first terminal tool rather than a miniature desktop IDE.

Do not make directory traversal the primary workflow. Navigate by service, registered operation, symbol, breakpoint, trace/flow, or fuzzy source search.

Suggested keys:

```text
j/k         move
h/l         change pane / collapse / expand
Enter       open selected operation, symbol, frame, or source target
Space       toggle breakpoint
Ctrl-P      fuzzy source/file/symbol search
@           symbols in current file
/           text search
n / N       next / previous match
:123        go to line
gg / G      top / bottom
Alt-Left    navigation back
Alt-Right   navigation forward
b           breakpoints
t           trace / observed flow
s           stack
v           locals
g           goroutines
e           evaluate expression
F5          continue
F10         step over
F11         step into
Shift-F11   step out
```

Selecting an observed `grove.Call` destination should be able to jump directly to the registered destination implementation rather than forcing the user through transport/plumbing code.

## Paused debugger layout

When a breakpoint is hit, the TUI should automatically enter a paused debug context. Source remains dominant and the right-side contextual pane becomes Locals.

```text
GROVE / DEBUG                    PAUSED ●  payments / node-2 / worker-7
----------------------------------------------------------------------------

service.go                                      LOCALS
------------------------------------------+---------------------------------
  88 func (s *Service) Charge(            | req
  89     ctx context.Context,              | +- OrderID    "O-184"
  90     req ChargeRequest,                | +- Amount     149.00
▶ 91 ) error {                             | +- Currency   "USD"
  92     payment := ...                    |
  93                                       | payment
                                           | +- ...
------------------------------------------+---------------------------------
payments.Charge · service.go:91 · goroutine 231

F5 continue   F10 over   F11 into   v locals   s stack   g goroutines   t flow
```

The right pane is contextual:
- `v` shows locals;
- `s` shows stack;
- `g` shows goroutines;
- `b` shows breakpoints;
- `t` shows the trace/observed Grove flow.

When the selected stack frame changes, both source and locals must update to that frame.

Locals must support keyboard expansion of structs, pointers, slices, maps, and nested values. `e` opens expression evaluation using Delve.

## Seamless debugging across nodes

The canonical native-debug sequence should demonstrate at least two services currently hosted on different nodes.

Example:

1. place an order;
2. open `Debug > Last flow`;
3. select `orders.CreateOrder`;
4. press `Enter` to open its embedded source;
5. press `Space` to create a Grove breakpoint;
6. select another executed operation such as `payments.Charge`, hosted on another node, and create a second Grove breakpoint;
7. place another order;
8. hit the first breakpoint and inspect locals/stack/source;
9. continue;
10. hit the second breakpoint on the other node and inspect locals;
11. continue and verify the order completes.

The user must not attach to another host, change a port, discover a PID, or otherwise change workflow when execution moves to a service on another node.

The intended impression is:

```text
observed flow
    ↓
registered Grove operation
    ↓
embedded source
    ↓
breakpoint
    ↓
source + locals + stack
    ↓
continue
    ↓
another service on another node
    ↓
source + locals + stack
```

## External DAP remains supported

The native TUI and external debugger endpoints must use the same Grove debug manager and target-resolution semantics.

For deterministic IDE/acceptance endpoints, keep application-binary actions such as:

```bash
./bin/groveshop action debug.attach orders --listen 127.0.0.1:40000
./bin/groveshop action debug.attach payment --listen 127.0.0.1:40001
```

These commands are not the headline human demo. They prove interoperability with ordinary Delve/DAP clients and provide deterministic automation endpoints.

Grove resolves service -> current node -> worker. The user never supplies a remote PID, node address, or node-local Delve port.

## Debug supervision semantics

While a worker is intentionally stopped at a breakpoint, Grove supervision must know that the worker is in an authorized debug session and must not treat the pause as an ordinary failure.

Ending the session returns the worker to normal supervision. If the target worker exits or is replaced during a low-level source-debug session, fail with an actionable diagnostic rather than silently moving a stateful Delve session.

Semantic Grove breakpoints may subsequently be re-resolved against current placement when a new debugging run begins.

## Acceptance requirements

The native debugging implementation is not complete until automated/manual coverage proves:

- the debug artifact contains source corresponding to the active build;
- source/symbol lookup can resolve registered Grove operations;
- the TUI can show the Grove operations actually executed by a real order flow;
- the observed flow reflects current service placement;
- an operator can open source from an observed operation without repository navigation;
- a Grove breakpoint can be created from an observed registered operation;
- arbitrary source breakpoints can also be created;
- a real order hits the expected breakpoint;
- paused source, locals, stack, goroutine state, stepping, continue, and expression evaluation are backed by real Delve state;
- a second breakpoint can be hit in a service hosted on another node without changing the user workflow;
- breakpoint pauses do not trigger ordinary failure recovery;
- the order completes after continuing;
- external DAP attachment still works through Grove;
- native TUI and external DAP use the same underlying debug target/session infrastructure;
- no node-local Delve listener is exposed as a public cluster endpoint.

## Boundaries

For the native TUI experience, Grove may coordinate application-level breakpoint intent across services, but Delve remains the Go debugging engine. Grove should not reimplement Go expression evaluation, stack unwinding, variable inspection, or stepping semantics.

Cross-service "step into" as though a distributed `grove.Call` were a normal Go function call is not required. The demonstrated experience is seamless navigation and breakpoint handling across services, not synthetic distributed instruction stepping.

Source-code protection, encryption, authorization/audit policy, and production hardening of embedded source are explicitly future work.
