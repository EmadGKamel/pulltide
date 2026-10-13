# 002 — Reconciliation

## Purpose

Converge an Application on a source-qualified immutable target through a serialized, recoverable lifecycle without embedding source or runtime implementations in the core.

## Scope

Startup, polling, target eligibility, driver orchestration, external-operation serialization, status, and shutdown. Persistent journal stages and recovery actions are defined in [state](007-state.md) and [rollback](008-rollback.md).

## Requirements

### Targets, ownership, and scheduling

- A source revision is `(driver, sourceIdentity, revision)`. Core MUST compare these opaque strings without interpreting Git object syntax. A target adds the configuration identity from [Application](001-application-contract.md).
- Startup MUST validate the current document before acquiring canonical ownership and reading state. Invalid current input blocks recovery as well as new deployment; the operator must repair the document first. Valid new input does not replace the saved policy or operands of pending recovery.
- First reconciliation begins immediately after startup recovery/observation succeeds. Polling is fixed-delay: wait the current configuration's `intervalSeconds` after the prior cycle completes. No ticks are queued and no concurrent resolver or runtime action runs for that Application.
- Each cycle asks the source driver to resolve its configured source. A failed resolution preserves the previous desired target, marks it stale in status, and invokes no deployment commands.
- A different successfully resolved full target MUST be persisted atomically with clearing the previous suppression episode. Resolution failure, ref-string edits alone without successful resolution, and restart MUST NOT clear suppression.
- An unchanged currently verified active target MUST NOT reapply. A suppressed target MUST NOT apply. Otherwise an eligible target proceeds through durable preparation, attempt recording, driver-specific journaled actions, health verification, and atomic success recording.
- Preparation MUST complete and durably retain required source, configuration, and recovery inputs before any current workload is stopped or updated. Ownership and driver execution context MUST match saved records.
- The core MUST record intent before every external runtime action, persist its outcome before the next action, and promote active/last-good only after health success and durable commit. Driver-specific replacement sequences belong to runtime drivers, not core type switches.
- A failure after a mutating-action intent exists, including prior-stop failure and interruption with uncertain invocation, MUST suppress the candidate target in the same atomic recovery-completion record that clears pending work. The pending journal itself blocks retry until then. Source/preparation failures and an abandoned prepared attempt with no mutating-action intent are not suppressed.
- Suppression retains one target per continuously desired episode, not an unlimited failed-revision history. Failed B followed by successful observation of C ends B's episode; a later observation of B makes B eligible again even if C never deployed. A new configuration identity is part of this comparison.
- No new target resolution or deployment may supersede pending recovery. After successful recovery, resume polling using current configuration; saved runtime and health inputs remain authoritative for the recovered deployment.

### Quiescence and operation outcomes

- Each action returns `completed-success`, `completed-failure`, or `completion-unknown`. Completed failure means the driver can prove its deployment-side effects have stopped; a nonzero process exit alone is not sufficient for a driver with asynchronous effects.
- Before recovering interrupted work, the driver MUST prove that earlier deployment mutators cannot continue changing the Application. Current health, lack of a PID, a free state lock, and a Pulltide process restart are not proof.
- For v0.1 local-only drivers, a changed Linux host boot identity is sufficient to rule out the prior boot's local command and daemon operations. It does not prove healthy or absent workloads, resolve ownership conflicts, or validate recovery inputs; those checks still apply. Exec deployment commands MUST NOT delegate unfinished deployment mutations to remote or persistent background jobs.
- Within the same boot, conclusive driver completion evidence permits recovery. Without that evidence, completion-unknown MUST remain recovery-required with no further command checks, cleanup, apply, or restore. Operators must stop the outstanding mutators and establish supported evidence; a host reboot provides the common v0.1 route when completion cannot otherwise be established.
- A crash after action intent but before invocation is conservatively treated as possible invocation. Safety may therefore require a reboot even if the action never actually started.

### Startup observation

- A pending journal takes precedence over startup health and follows the recovery matrix. No journal plus a saved active deployment requires ownership/context verification and one bounded health-verification window using that saved deployment, without apply.
- Until startup verification passes, status MUST report current health unknown. Passing checks reestablish healthy observation without changing last-good. Failed checks or unavailable context yield recovery-required and block mutation in that process.
- Repairing the workload/context and restarting MUST retry startup observation once. Pulltide MUST NOT automatically apply or rollback a previously successful deployment merely because startup health failed. This is the explicit host-reboot policy; workload persistence is operator-managed.
- If active is confirmed absent and no journal exists, startup has nothing to health-check and may reconcile normally subject to suppression. A known absence MUST NOT be inferred solely from failed health checks.

### Status observation contract

The process MUST emit one UTF-8 JSON object per line on stdout for initial status, every externally visible phase/observation transition, each cycle result, and terminal errors. Human diagnostics, if any, go to stderr. These events are observations, not the recovery source of truth; consumers use the latest event for an Application. A status-emission error MUST NOT rewrite committed deployment state and MUST be reported on stderr when possible.

Every event MUST contain these fields; unavailable values are explicit JSON null, never invented revisions:

| Field | Shape and meaning |
| --- | --- |
| `statusVersion` | Literal `pulltide.io/status/v1alpha1`, separate from the Application kind/version |
| `application` | Validated Application name, or null on input failure |
| `phase` | One of the phases below |
| `desired` | Full target object or null; includes `source: {driver, sourceIdentity, revision}` and `configurationIdentity` |
| `desiredStale` | Boolean; true before fresh startup resolution or after resolution failure |
| `active` | Object `{state, target}`; state is `known`, `absent`, or `unknown`; target non-null only for known |
| `lastGood` | Full historical target or null; not a current-health assertion |
| `suppressedTarget` | Full suppressed target or null; pending candidate is separately exposed |
| `pending` | Null or `{attemptId, stage, operandTarget, candidateTarget}` from the durable journal |
| `health` | `unknown`, `healthy`, `unhealthy`, or `not-applicable`; last observed this process, not continuous monitoring |
| `healthObservedAt` | UTC RFC3339 timestamp or null; absence uses not-applicable and null |
| `error` | Null or `{category, stage, checkIndex}`; category is a stable code from the table below, checkIndex is zero-based or null |

Error categories are `validation`, `ownership`, `source`, `preparation`, `runtime`, `health`, `persistence`, `uncertain-operation`, `recovery`, and `shutdown`. Error fields MUST NOT contain command text, environment values, source URLs, HTTP bodies, or child output. Detailed public driver-specific error categories may only be added through a reviewed specification change.

| Phase | Meaning / allowed successor classes |
| --- | --- |
| `idle` | No work started; proceed to preparation, startup verification, failure, or blocked recovery |
| `preparing` | Read-only preparation; proceed to applying or failed |
| `applying` | Journaled replacement; proceed to verifying, rolling-back, failed cleanup outcome, or recovery-required |
| `verifying` | Candidate or startup health; proceed to healthy, rolling-back, failed cleanup outcome, or recovery-required |
| `healthy` | Saved active verified in this process; remain healthy for no-op cycle, or proceed to preparation/failure |
| `rolling-back` | Recovery cleanup/restoration, including disabled rollback cleanup; proceed to rolled-back, failed, or recovery-required |
| `rolled-back` | Verified saved deployment restored; no-op cycles retain this outcome; a new eligible attempt enters preparing |
| `failed` | Cycle/attempt failed but no unresolved mutation remains; may retry source/preparation or consider an unsuppressed target |
| `recovery-required` | Safety/storage/ownership/startup-health prerequisite failed; no new mutation in this process |

Startup may enter rolling-back directly from a valid pending journal after the safety barrier. Source/preparation errors use failed while retaining active and its last observation; they do not mean the running workload failed. Unknown active state always has unknown health. Candidate mutation makes active unknown until verified success, confirmed cleanup absence, or verified restoration. Failed cleanup remains unknown. A suppressed no-op retains the prior failed/rolled-back outcome and error; source recovery alone does not erase a candidate failure. A successful no-op over a known verified target after a source/preparation error clears that cycle error and reports healthy.

### Shutdown

- On shutdown request, cancel polling and do not start a new action or recovery attempt. An already running external action may complete within its remaining configured deadline plus the ten-second termination allowance; shutdown adds no fresh deadline.
- Persist any completed action result if storage is available, but do not promote a candidate unless verification already completed and its success can be committed. Incomplete work retains its journal for startup recovery.
- Source/preparation work may be cancelled and discarded without runtime cleanup. Shutdown MUST NOT stop an established workload as an implicit side effect. Runtime action termination does not mean termination of the application's separately supervised service.
- Failure to stop an in-flight mutator retains completion-unknown; releasing the lock does not authorize a successor process to bypass the safety barrier.

## Non-goals

Webhooks, concurrent actions for one Application, automatic runtime drift repair, continuous health monitoring, manual retry/revision-selection APIs, and distributed coordination.

## Failure behavior

Before mutation, active and last-good remain intact. After possible mutation, active is unknown until verification or confirmed cleanup. Storage failure blocks further mutation; event output cannot substitute for a durable journal. Any uncertain earlier mutator blocks recovery itself. A valid blocked journal is reconsidered once per restart when its prerequisites can be proven; normal polling never repeatedly attempts blocked restoration.

## Acceptance criteria

| ID | Scenario | Required observation |
| --- | --- | --- |
| RECON-01 | Repeated identical healthy target | Exactly one deployment apply; later cycles are no-ops |
| RECON-02 | Cycle takes longer than polling interval | Next resolution starts one interval after completion; no backlog/concurrency |
| RECON-03 | Moving ref changes during deployment | Attempt uses original revision; next cycle may observe new revision |
| RECON-04 | Failed B, restart, source outage | B remains suppressed; outage does not reset desired episode |
| RECON-05 | Failed B, resolved C, then resolved B | C observation clears suppression durably; B is eligible again |
| RECON-06 | Prior-stop failure or interrupted mutating intent | Pending work blocks retries; terminal recovery records B suppression atomically |
| RECON-07 | Delayed old mutation after process death/timeout | No recovery action or command health check before conclusive quiescence |
| RECON-08 | New boot, intact historical success, service missing | Startup check fails; last-good retained; no apply; recovery-required |
| RECON-09 | Intact active service across restart | Saved startup checks pass; no apply; healthy observation is fresh |
| RECON-10 | Invalid new config with pending journal | No recovery command; validation error until document repaired |
| RECON-11 | Shutdown during apply | No new action; existing deadline unchanged; result or uncertainty retained in journal |
| RECON-12 | Source failure while A is running | JSON event is failed, desiredStale true, active A retained, health accurately identified as last observation |
| RECON-13 | Same commit with changed normalized config | Target identity differs; deployment may proceed after startup/recovery and fresh resolution |
| RECON-14 | Status during first deployment, mutation, cleanup, and rollback | Null/absent/unknown/known and each target's identity obey the table; no secrets or raw output |
