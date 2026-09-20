# Grove Shop MVP Demo Flow

## Goal
Prove Grove's lifecycle with one application binary UX: bootstrap/join a cluster, demonstrate placement and recovery, build a new immutable artifact, run it, and follow the rollout live from every terminal while the browser remains on one stable ingress endpoint.

Configuration-only changes use the exact same artifact rollout path.

## Preconditions
- Grove Shop source is available.
- Good config source exists at `configs/acme.yaml`.
- Broken config source exists at `configs/acme-broken.yaml`.
- Initial Grove Shop artifact contains the good embedded config.
- No separately installed `grove` CLI is required.

## Demo sequence

### 1. Start node 1
Run the initial artifact:

```bash
./bin/groveshop
```

No compatible Grove Shop cluster is discovered, so Grove bootstraps one. Open **Cluster** in the TUI.

### 2. Join nodes 2 and 3
Run the exact same artifact in two more terminals:

```bash
./bin/groveshop
```

Each process discovers the existing cluster with the same application and artifact identity. The TUI suggests **Join** as the default action. Confirm it and open **Cluster** in each terminal.

All three Cluster views must show the same three-node membership and service placement.

### 3. Open Grove Shop
Open the Web URL exposed by the Grove-managed ingress and leave the browser open for the remainder of the lifecycle demo.

The page shows Orders and Grove Cluster Status. Record the ingress address/port; it must not change during rollout or rollback.

### 4. Exercise placement and recovery
Create an order and verify:

```text
Created -> Reserved -> Paid -> Shipping -> Completed
```

Terminate one node. The surviving Cluster views must show the node failure and service relocation live. The browser remains reachable through the same ingress endpoint. Create another order and prove the application still works.

Restore/rejoin the node as appropriate for the deterministic demo and verify all Cluster views converge.

### 5. Make a code change and build a new artifact
Add the demo feature, build Grove Shop, and embed the desired good configuration into the resulting immutable artifact.

Run that new artifact:

```bash
./bin/groveshop-new
```

It discovers the existing cluster with the same application identity but a different artifact identity and offers **Roll out this build**. Confirm.

### 6. Follow the rollout everywhere
Keep Cluster view open in the original node terminals and in the candidate terminal.

All views must show the same rollout progression: active/candidate identity, node/service transitions, health, progress, and activity events.

At the same time:
- keep the browser open;
- keep polling through the original ingress address/port;
- continue using existing Orders functionality;
- do not change URL or restart the client.

After cutover, demonstrate the new feature through the same browser endpoint.

### 7. Prove config changes use the same flow
Create another artifact from the same application code but embed `configs/acme-broken.yaml`.

Run the resulting artifact:

```bash
./bin/groveshop-bad-config
```

Grove must not offer a special config deployment flow. It sees the same application identity plus a new artifact identity and offers the same **Roll out this build** action.

### 8. Watch failed candidate and rollback
All Cluster views and the browser status pane show, conceptually:

```text
Artifact A ACTIVE
       |
Artifact B CANDIDATE
       |
Candidate services STARTING
       |
Inventory rejects reservation_buffer < 0
       |
Candidate UNHEALTHY
       |
ROLLBACK / CANDIDATE REJECTED
       |
Artifact A ACTIVE
       |
CLUSTER HEALTHY
```

The browser remains on the exact same ingress endpoint throughout.

### 9. Verify recovery
Create another order after rollback.

Expected:
- order succeeds;
- the previous complete known-good artifact/config is active;
- no config mutation or manual repair occurred;
- all Cluster views agree;
- ingress address/port is unchanged.

## Headline demo contract
The human story is intentionally compact:

```text
run artifact -> bootstrap cluster
run same artifact -> join
run same artifact -> join

kill node -> watch shared Cluster views -> service recovers

edit code/config -> produce new immutable artifact
run new artifact -> rollout suggested
press Enter -> watch rollout in every terminal
browser stays on same endpoint

embed bad config -> run resulting artifact
same rollout -> health failure -> automatic rollback
```

Do not replace this with generic cluster-create, join-address, deploy, or config-rollout command trees.

## Final E2E mapping
Automated acceptance must reproduce the semantics programmatically:
1. start Artifact A and bootstrap a cluster;
2. start two more A processes and verify same-artifact join semantics;
3. verify multiple observers see the same three-node state;
4. capture and exercise the ingress endpoint;
5. exercise node failure/service recovery;
6. produce/start Artifact B and verify different-artifact rollout semantics;
7. observe rollout concurrently from multiple read-model observers;
8. prove requests continue through the unchanged ingress endpoint;
9. verify B becomes active and new behavior is reachable;
10. produce/start config-only Artifact C with broken config;
11. verify it enters the same rollout path;
12. observe Inventory failure and rollback;
13. verify the previous complete artifact is authoritative again;
14. prove the ingress endpoint is still unchanged and an order succeeds;
15. clean up all processes/state.

No fixed sleeps, manual browser interaction, Docker, or shell orchestration are allowed for acceptance.
