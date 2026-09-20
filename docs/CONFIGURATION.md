# Grove Shop Embedded Configuration Demo

## Purpose
Configuration is part of the Grove Shop immutable artifact. A configuration change therefore follows exactly the same rollout path as a code change.

There is no special human-facing "config deployment" mechanism.

## Good configuration
Use a customer-specific YAML source such as:

```yaml
customer:
  name: Acme Retail

inventory:
  reservation_buffer: 100
```

At least one value must influence normal application behavior and be visible through the running application or status metadata.

## Broken configuration

```yaml
customer:
  name: Acme Retail

inventory:
  reservation_buffer: -1
```

Inventory treats a negative reservation buffer as invalid and fails deterministically during candidate startup/initialization. Grove marks the candidate unhealthy and rejects it.

Do not use a hidden `crash=true` switch as the primary scenario.

## Artifact semantics
Configuration is embedded into the Grove application artifact before it is introduced to the cluster:

```text
application code
      |
    build
      v
Grove Shop binary
      |
      +-- embed config A -> Artifact A
      |
      `-- embed config B -> Artifact B
```

Every resulting artifact is immutable for deployment purposes. Artifact identity must include the embedded configuration so A and B differ even when code is identical.

A code change, config change, or both simply produces another artifact:

```text
same application identity + different artifact identity -> rollout candidate
```

## Human workflow
The configuration source may be prepared/embedded by build tooling, but rollout begins by running the resulting artifact, exactly as for a code change:

```bash
# produce artifact with desired embedded config
./bin/groveshop-new
```

The process discovers the existing Grove Shop cluster. Because application identity matches and artifact identity differs, the TUI offers **Roll out this build**.

Do not make `Deployments > New rollout > <config-file>` or `rollout.start --config ...` the headline workflow. Those forms incorrectly imply that configuration has a separate deployment path.

Structured config compile/embed/extract actions may exist for build automation, inspection, and tests, but the deployment input is the resulting immutable artifact.

## Rollback semantics

```text
Artifact A: code N + config A  ACTIVE
                 |
Artifact B: code N + config B  CANDIDATE
                 |
          config B invalid
                 |
        candidate rejected
                 |
Artifact A                    ACTIVE
```

Rollback restores/retains the previous complete known-good artifact. Grove must not mutate candidate config in place or copy only an old config value into the candidate.

## Stable ingress and shared observability
The browser remains on the same Grove-managed ingress address/port throughout a config-only rollout and rollback, exactly as for a code rollout.

Every open Cluster TUI view must show the same candidate artifact/config identity, validation failure, rollback reason, and final active artifact. The browser status pane observes the same read model.

Raw secrets or arbitrary configuration contents must never be displayed.

## Acceptance
Tests must prove that a config-only artifact change:
- is detected as a different artifact of the same application;
- enters the ordinary rollout path;
- is visible simultaneously to multiple Cluster observers;
- preserves the public ingress endpoint;
- can fail health validation;
- rolls back the complete artifact;
- leaves the previous application/config healthy and reachable through the same endpoint.
