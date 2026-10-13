# 000 — Product

## Purpose

Pulltide is a pull-based application reconciler. These v0.1 specifications resolve the initial specification review and are drafts for maintainer approval before implementation. They define behavior, not implementation code.

## Scope

The MVP includes Git source and revision detection, reconciliation, exec and Docker Compose runtimes, HTTP and command health checks, local state, last-good tracking, and rollback. Pulltide is distributed as one Go binary on a Linux host. Operator-provided commands and Docker Compose are external dependencies, not bundled services. No central control plane is required.

## Requirements

- MUST, MUST NOT, and SHOULD are normative. Public documents MUST start with `apiVersion: pulltide.io/v1alpha1` followed by `kind: Application`.
- Each Application selects one source and one runtime and requires at least one health check. Git is the only v0.1 source driver; `exec` and `docker-compose` are the only v0.1 runtime drivers.
- Core reconciliation MUST treat revisions as opaque driver-qualified identities and runtime results as driver-neutral outcomes. Git object resolution and Compose service operations belong to their drivers.
- The reconciler exclusively owns state, policy, scheduling, journals, failure suppression, and promotion of last-good. Drivers MUST NOT independently modify these records or schedule retries.
- Deployment success requires completed runtime application, passing health checks, and durable success recording. Historical last-good does not establish current runtime health.
- Local runtime work MUST be serialized across process lifetimes, not merely across loop iterations. Unknown completion MUST block further commands and runtime mutation until the quiescence condition in [reconciliation](002-reconciliation.md) is met.
- One canonical installation and operating account per host owns `/var/lib/pulltide`. Alternate runtime state roots and concurrent installations targeting the same host resources are unsupported and MUST NOT be configurable. See [state ownership](007-state.md).
- Workspaces and records MUST be isolated by Application. Resource ownership MUST be established before mutation; preexisting resources MUST NOT be silently adopted.
- Credentials, environment values, raw child output, and HTTP response bodies MUST NOT appear in status or diagnostics. Retained explicit runtime configuration is private local data, not status.
- Runtime commands and repository contents are trusted operator inputs. Pulltide does not sandbox them or guarantee reversal of their external side effects.

### Driver boundaries

| Boundary | Required behavior | Owner |
| --- | --- | --- |
| Source resolve | Return `(driver, sourceIdentity, revision)` or a classified failure; no stale substitution | Source driver |
| Source materialize | Provide validated immutable content and an isolated workspace; report durable artifact references | Source driver, with persistence committed by core |
| Runtime prepare | Validate inputs, ownership, fixed execution context, and recovery prerequisites without deployment mutation | Runtime driver |
| Runtime replace | Describe and execute driver-specific bounded actions using saved operands; core journals each action before invocation | Runtime driver and core journal |
| Runtime cleanup/restore | Identify all owned transition resources and saved deployment operands; return confirmed outcome or uncertainty | Runtime driver |
| Runtime quiescence | Establish that prior deployment operations cannot continue mutating resources; otherwise return unknown | Runtime driver |
| Runtime observation | Provide ownership/absence evidence; configured health checks establish health, not operation completion | Runtime driver and health runner |
| State transitions | Decide eligibility, record intent/results, suppress failures, publish status, promote or recover | Core reconciler |

These are behavioral boundaries; they do not prescribe a Go interface or add future drivers.

### Selected review resolutions

- Recovery uses the policy captured by the interrupted attempt, not newly loaded policy.
- Failed-target suppression lasts for one continuously desired target, including across restart. A different successfully resolved target ends that episode.
- Startup rechecks saved health without reapplying. Unhealthy startup blocks mutation until operator repair and a successful restart check; continuous drift repair remains outside scope.
- Unknown external-operation completion blocks recovery. Process restart alone never establishes safe completion.
- Runtime child environments are explicitly bounded; ambient host environment is not implicitly inherited.
- Exact Git commits may use verified repository-scoped local content offline. Moving refs require fresh remote resolution.

The detailed contracts are [Application](001-application-contract.md), [reconciliation](002-reconciliation.md), [Git](003-source-git.md), [exec](004-runtime-exec.md), [Compose](005-runtime-compose.md), [health](006-health.md), [state](007-state.md), and [rollback](008-rollback.md).

## Non-goals

Non-Git source implementations, Kubernetes, remote runtime hosts, multi-node coordination, remote state, a UI, RBAC, deployment approval gates, notifications, secret management, autoscaling, build pipelines, zero-downtime deployment, continuous monitoring, database/volume restoration, and reversal of arbitrary external side effects. Repository specification approval is a development step, not a deployment feature.

## Failure behavior

Invalid input, ownership uncertainty, missing recovery artifacts, and uncertain operation completion MUST fail closed before further runtime mutation. Source/preparation failures preserve the existing deployment. Mutation failures follow the saved rollback policy only after quiescence is established. Status MUST distinguish historical success, current observation, failure, and unknown runtime state.

## Acceptance criteria

| ID | Scenario | Required observation |
| --- | --- | --- |
| PRODUCT-01 | Either supported runtime receives a new eligible Git revision | Resolve, prepare, journal, apply, verify, and durable promotion occur in order |
| PRODUCT-02 | A candidate fails and prior work is conclusively finished | Last-good is restored and checked when saved policy enables rollback |
| PRODUCT-03 | An earlier deployment operation may still be running | No cleanup, health command, or restore runs; status is recovery-required |
| PRODUCT-04 | Restart or host reboot with historical success | Saved health is rechecked; no historical record alone produces current healthy status |
| PRODUCT-05 | Second local owner or an unowned existing project | Mutation is rejected before any deployment command |
| PRODUCT-06 | Driver conformance review | Core policy refers to qualified revisions and driver outcomes; Git/Compose details remain in driver contracts |

### Review closure map

| Review finding | Normative resolution and acceptance coverage |
| --- | --- |
| C1 — Surviving operations | 002 quiescence; 004/005 driver completion; 007 recovery matrix |
| M1 — Ownership | 007 canonical registry and missing-state rules; 005 project ownership |
| M2 — Reboot health | 002 startup; 006 startup verification |
| M3 — Journal stages | 007 durable operation and crash-boundary matrix; 008 interrupted restoration |
| M4 — Durable artifacts | 003 materialization; 007 durability ordering |
| M5 — Execution inputs | 004/005 saved execution context; 006 check environment |
| M6 — Compose absence | 005 whole-project cleanup and absence verification |
| M7 — Failure classification | 002 suppression; 007 recovery completion; 008 failure recording |
| M8/M9 — Validation/identity | 001 field/default and canonical identity contracts |
| M10 — Status | 002 JSON Lines observation contract |
| N1 — Bounds | 004 process/output bounds; 006 deadlines; 008 restoration bound |
| N2 — Health rounds | 006 fail-fast ordered checks and completion-relative interval |
| N3 — Acceptance coverage | Named scenario tables in 001–008, including source failures and crash boundaries |
| D1 — Driver boundaries | 000 shared behavioral boundaries; 002 core responsibilities |
| D2/D3/D4 — Policy, suppression, recovery | 002 scheduling and eligibility; 007 saved policy; 008 restart recovery |
| D5/D6 — Cached commits/materialization | 003 exact-commit and raw-content rules |
| D7 — Poll/shutdown timing | 002 fixed-delay polling and bounded shutdown |
