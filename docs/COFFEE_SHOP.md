# Grove Coffee: the live demo

Grove Shop's browser demo is a coffee shop that is already running when the
page opens. Nothing is configured and nothing is triggered by hand: customers
arrive at random, the shop's staff serve them, and the queues, waits and
inventory evolve from the simulation itself. The staff are Grove execution
capacity, so adding or losing a Grove node visibly changes what the shop can
do.

## What maps to what

| Coffee shop | Grove |
| --- | --- |
| Customer | Incoming workload |
| Cashier | `Cashier.TakeOrder` handler |
| Barista | `Barista.MakeDrink` handler |
| Kitchen staff | `Kitchen.PrepareFood` handler |
| Staff member | One execution slot on one node (`StaffPerNode` = 3 per node) |
| Queue | Work waiting for a free slot (backpressure) |
| Utilization | Busy slots / assigned slots |
| The shop | The `Shop` component, one exclusive owner cluster-wide |
| Adding a node | +3 staff |
| Losing a node | −3 staff, interrupted work is retried by the rest |
| Supplier | Asynchronous actor with its own timing |
| Inventory | Application state |

## How it works

- **Shop** (service 6) runs the simulation. Every node hosts it, and Grove's
  exclusive capability `groveshop/shop` picks the one that runs it. If that
  node dies, another node opens the shop and the Web component hands it the
  previous shop's sales and inventory. If Grove's lease lapses for a few
  seconds (it can while the cluster recovers from a node loss), the shop
  pauses and resumes instead of starting over.
- **Cashier, Barista, Kitchen** (services 7–9) are ordinary Grove handlers
  on every node. Each piece of work (taking one order, making one drink,
  preparing one food item) is a `grove.Call`; Grove picks the node. The
  handler sleeps for the randomly drawn preparation time while holding one of
  its node's staff slots. The three handlers on a node share that node's
  slots.
- **Staff** come from Grove's status read model: every healthy node whose
  station components are healthy contributes `StaffPerNode` staff.
- **Shift manager.** Grove today runs every handler on every node and
  round-robins calls; it has no notion of per-handler load. So the shop
  itself decides which station its staff work at: every 1.5 s it moves one
  person from the station that can best spare one to the station with the
  most outstanding work. Adding a node therefore shows up where the shop
  needs it (for example, as extra baristas during a drink rush) rather than
  equally everywhere. Moving that decision into Grove is tracked in
  [grove#47](https://github.com/grove-project/grove/issues/47).

## Simulation

- **Customer flow** is set on the page (or `POST /api/shop/demand` with
  `{"mode": "steady", "per_minute": 120}`) and survives the shop moving to
  another node. There are two modes:
  - **Steady** (the default, 120 customers / min): customers walk in one at a
    time, evenly spaced, with a fixed product mix. The load on the cluster
    only changes when you change the flow or the nodes.
  - **Random:** a Poisson process around the set rate, with a slow wave,
    groups of 1–3 customers, random rushes (1.7–2.6× for 25–55 s, spaced
    minutes apart), and a product mix that drifts between drink-heavy,
    balanced and food-heavy regimes.
- **Orders** fan out: each drink goes to the baristas and each food item to
  the kitchen, concurrently. The order waits at pickup until every item is
  ready.
- **Inventory** is consumed when the cashier takes the order. Customers pick
  something else of the same kind when their choice is unavailable, and
  leave if nothing they want can be made.
- **Suppliers** (Roastery, Dairy Co., Fresh Foods) are dispatched when one of
  their products drops below 40%, arrive 35–70 s later, and are sometimes
  delayed by 20–50 s.
- **Backpressure.** When drinks or food fall more than six items per staff
  member behind, the cashiers stop taking new orders, so customers wait in
  line, where they can still leave, rather than after paying.
- **Abandonment.** Customers still in line after 45–120 s leave.
- **Calibration.** Each node serves roughly 35 customers / min at steady
  flow. At the default 120 / min, three nodes fall further behind every
  minute; a fourth node clears a six-minute backlog in about 1.5–2 minutes
  and a fifth in under one (`TestShopSimulationSteadyFlow`). In random mode
  around 63 / min, two nodes fall behind, three congest in rushes, and four
  absorb most rushes.

## Testing without Grove

`shop_sim_test.go` runs the real `Shop`, its run loops and the real staff
`Crew`s against a small in-process stand-in for Grove (round-robin calls
over live nodes, node kill and join, capacity with a detection delay) inside
a `testing/synctest` bubble. Thirty minutes of shop time take under a
second, so demand, staffing and recovery changes can be checked with
`go test -run TestShopSimulation -v .` before trying them on a real cluster.

## The page

- **Customer flow:** the rate (slider or number) and mode, applied with
  **Apply**, and how many nodes that flow needs.
- **Shop floor:** customers in line, the three station cards (staff, busy,
  waiting, oldest wait, utilization; a 🔥 marker when congested), the pickup
  counter and the active orders with each item's progress.
- **Live state:** customers inside, active orders, average and longest wait,
  drinks and food per minute, served, left unhappy, product mix.
- **Grove capacity:** online nodes and total staff. The small digit under
  each staff member is the node that did most of that station's recent work.
- **Inventory and supply:** stock bars with depletion estimate and delivery
  ETA, unavailable products, supplier status and delays.
- **Queues & wait:** the last ten minutes of the three queues and the average
  wait, with node joins and losses marked.
- **Live activity:** routine events are muted (and can be hidden); rushes,
  congestion, stock, suppliers, capacity shifts and node changes stand out.
- **Grove runtime** tab: the cluster status, placement and rollout state, and
  the lifecycle probe order Grove's rollout and recovery checks use.

## Demo story

1. Start the artifact and open the page: the shop is already serving.
2. With three nodes and the default steady flow of 120 customers / min, the
   cashier line and the average wait climb minute after minute and the
   stations turn 🔥. Raise or lower the flow under **Customer flow** to make
   the point stronger or gentler.
3. Run the artifact again in a second terminal and **Join** one node: a
   "Grove node joined" notice appears, three more staff show up where the
   shop needs them, and the queue drains. A fifth node drains it faster, and
   with five nodes you can push the flow to about 170 / min.
4. Quit that terminal: the node is lost, staff drop, interrupted work is
   retried, and the queues show the consequence.
5. Join again and watch the shop stabilize.

The web page explains what happens to the business; the Grove TUI's
**Cluster** view explains what Grove does underneath.
