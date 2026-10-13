# 007 — Local state

## Purpose

Preserve deployment truth and sufficient recovery information across failures, process restarts, and host reboots.

## Scope

Local durable state, exclusive ownership, deployment records, operation journaling, and retained source content. These are draft resolutions requiring maintainer approval before implementation.

## Requirements

### Ownership and records

- v0.1 runs on one Linux host under one dedicated service account. `/var/lib/pulltide` is the canonical state root; alternate runtime roots are unsupported. Tests MAY isolate roots only to model separate hosts. Application names are unique within this host. A process MUST acquire the canonical exclusive Application lock before inspecting or mutating its managed runtime; process death releases the lock but does not prove external actions have stopped.
- A durable ownership registry separate from per-Application state MUST bind each name to its runtime and runtime resource identity before first mutation. Compose bindings include the daemon identity and exact project `pulltide-<name>`. Existing project resources without a matching ownership binding MUST NOT be adopted. Missing Application state with an existing binding MUST enter `recovery-required`, never first deployment.
- Complete loss of the root cannot reliably reveal arbitrary existing exec workloads. Operators MUST restore ownership and state records or ensure all prior managed resources and mutating operations are stopped before recreating the root. v0.1 provides no automatic adoption or inference of lost exec ownership.
- Core reconciliation exclusively writes authoritative state and selects transitions; source and runtime drivers return results and references, not independent deployment-state updates.
- State MUST include a format version, Application identity, observed desired target, active deployment (`known`, `absent`, or `unknown`), historical last-good, current health observation, failed-target suppression, public phase, sanitized error, and pending journal when present. Status is defined in [reconciliation](002-reconciliation.md).
- A deployment record MUST contain source-driver type, source identity, opaque immutable revision, configuration identity and its version, saved runtime and health configuration, effective execution inputs, ownership binding, and references to retained source and workspace. Git represents its revision by the full commit ID; core state MUST NOT derive recovery from Git ancestry.
- Records MUST retain enough inputs to stop, restore, and verify the deployment without resolving source again. State and retained configuration MUST be private to the service account. Credential-bearing URLs MUST NOT be persisted, and configured environment values MUST NOT appear in diagnostics. External runtime prerequisites remain subject to the runtime contracts.

### Durability and journal

- Before a journal references deployment inputs, clean snapshots, required workspaces, configuration, and their containing directory entries MUST be durably stored and validated. The journal MUST become durable before dependent runtime mutation. Retained immutable source MUST be separate from mutable execution workspaces.
- Complete state records MUST be replaced atomically and durably, including durable directory metadata. Interrupted writes expose the prior valid record or the new valid record. Format, identity, reference integrity, and internal consistency MUST be checked before recovery; corrupt or unsupported records MUST NOT be interpreted as empty state.
- The journal MUST retain candidate, prior active, historical last-good, all deployments involved in the transition, and the candidate's frozen recovery policy (rollback enablement and operation timeout). Saved health configuration belongs to the deployment being verified. Changed startup configuration MUST NOT alter an existing journal's recovery policy.
- Internal stages are separate from public phases. Each external action has a durable intent containing its exact deployment operand and a durable completion result. An intent without completion means the action **may have started**, including when the process died before invoking it. Operation identity and evidence needed by the runtime's quiescence barrier MUST be retained.

| Internal stage recorded before action | Action and operand | Recovery obligation after interruption |
| --- | --- | --- |
| `prepared` | No runtime action authorized yet | Abandon attempt; preserve prior active; do not suppress candidate |
| `prior-stop-intent` | Stop saved prior active (exec replacement) | Establish quiescence; finish that stop before restoration or cleanup |
| `candidate-apply-intent` | Apply candidate | Establish quiescence; treat candidate as possibly applied; recover under frozen policy |
| `candidate-verify-intent` | Check candidate | Terminate or establish completion of checks; candidate is uncommitted; recover under frozen policy |
| `cleanup-intent` | Stop exact recorded exec operand, or clean all owned Compose transition resources | Establish quiescence; replay cleanup using recorded operands before further mutation |
| `restore-apply-intent` | Apply saved last-good | Establish quiescence; exec MUST stop this potentially partial last-good apply before retrying restoration; Compose uses its recovery contract |
| `restore-verify-intent` | Check saved last-good | Establish quiescence; retry a bounded restoration attempt, including exec stop of the restoration operand |
| `candidate-success` / `restoration-success` | No external action; atomic success record | Journal is cleared in the same atomic record as active/last-good outcome; perform startup health observation |

- A durably completed stage advances to the next appropriate stage without pretending a later action began. A crash after verification but before the atomic success record remains an interrupted attempt. A cleanup continuation MUST preserve its restoration operand across another crash, not substitute the original failed candidate.
- Candidate success MUST atomically set active and last-good to the candidate and clear the journal. Restoration success MUST atomically set active to retained last-good, preserve that historical last-good and failed desired target, and clear the journal. No failed candidate becomes last-good.
- Referenced active, last-good, pending, and cleanup operands and their inputs MUST NOT be garbage-collected. Historical unreferenced inputs MAY be removed after durable references are released.

### Startup and suppression

- Startup MUST resolve a pending journal before observing or deploying new desired work. It MUST establish the runtime's quiescence barrier before health checks or mutations that could race old actions. Health success, absence of a client PID, and acquisition of the state lock are not proof of quiescence. Unknown completion leaves `recovery-required`, with no runtime calls that could race the old action.
- For a valid journal and satisfied prerequisites, each process startup MUST make exactly one recovery attempt under [rollback](008-rollback.md). Unusable storage, missing inputs, ownership mismatch, or unproven quiescence blocks this attempt until repaired and the process restarted. Normal polling MUST NOT retry failed recovery.
- A clean startup with known active MUST check that deployment with its saved health configuration before reporting current healthy status. It MUST NOT reapply it automatically. Failed startup health sets `recovery-required` and current health failed, retains historical active/last-good records, and makes no runtime mutation. Repair and restart repeats this check; success resumes reconciliation. A clean absent deployment requires no startup check.
- Failed or interrupted attempts for which any mutation intent exists, including prior-stop failure, MUST durably suppress the candidate before recovery is cleared. Suppression survives restart for the same desired episode. Only atomic successful observation of a different fully resolved target clears it; source failures do not. Thus failed B, then observed C, then B permits a new B attempt, even if C was not deployed. Merely prepared attempts do not create suppression.

## Non-goals

Remote or replicated state, multi-host leadership, alternate production state roots, automatic adoption, unlimited historical retention, transparent schema migration, and encryption key management.

## Failure behavior

Lock contention rejects the second writer. Invalid ownership or unreadable, unsupported, missing-but-owned, corrupt, or inconsistent state blocks mutation and requires operator repair. Failure to persist inputs or intent prevents runtime action. Failure to persist after mutation leaves recovery required; no durable success may be reported and no dependent mutation may proceed. Restart uses the last valid durable journal, never inferred success.

## Acceptance criteria

| ID | Scenario | Required observation |
| --- | --- | --- |
| STATE-01 | Two processes use one Application, or attempt an alternate root on the same host | Only canonical lock owner may mutate; alternate root rejected |
| STATE-02 | App state missing with ownership binding; unrelated existing Compose project without binding | `recovery-required` or ownership rejection respectively; no apply, cleanup, or adoption |
| STATE-03 | Crash before/after publishing retained inputs, before journal commitment | No runtime action without durable validated inputs and intent; unreferenced partial inputs never become deployment records |
| STATE-04 | Crash at `prepared`, prior-stop intent, candidate apply intent, and candidate verification | Prepared preserves A without suppression; each mutation-intent case suppresses B and follows journal table after quiescence |
| STATE-05 | Crash during restoration apply, restoration stop, or restoration verification | Correct saved A operand survives every restart; B is never redeployed; failed recovery remains pending |
| STATE-06 | Crash after successful check before atomic success, and after atomic success | Before commit: recover interrupted attempt; after commit: startup checks saved active with zero reapply calls |
| STATE-07 | Disk full before intent and after apply; interrupted state replacement | Before intent no mutation; after apply no success or further mutation; record is old or new valid state |
| STATE-08 | Corrupt record, unsupported format, missing snapshot, changed ownership binding | Explicit blocking error, preserved evidence, zero runtime mutations |
| STATE-09 | Healthy deployment restart with workload surviving, then host reboot with workload absent | Surviving workload checked without reapply; absent workload fails startup health, blocks mutation, retains historical last-good |
| STATE-10 | Suppressed B across restart; fetch failure; successful observation C followed by B | Restart and fetch failure retain suppression; observation C clears it durably; later B can retry |
| STATE-11 | Candidate policy changed on restart during pending recovery | Recovery uses journaled enablement/timeouts and each operand's saved health checks |
| STATE-12 | Delayed old mutation after client death, with no quiescence proof | No competing cleanup, restore, or success check; `recovery-required` remains visible |
