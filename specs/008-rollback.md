# 008 — Rollback

## Purpose

Restore the most recent verified successful deployment after a candidate fails or deployment is interrupted.

## Scope

Automatic rollback, retained last-good inputs, first-deployment cleanup, and recovery outcomes. Architecture choices are approved; these detailed amendments await review before implementation.

## Requirements

### Preconditions and policy

- Rollback is enabled by default. Failure or interruption after any runtime mutation intent, including prior-stop intent, MUST enter recovery. Source resolution, artifact preparation/builds, observation-only journals, and merely prepared deployment journals MUST preserve active without rollback or suppression. Unknown operations still block until quiescent.
- Last-good is historical evidence of the most recent health-verified, durably committed successful deployment. Its immutable source revision, configuration, and execution inputs MUST be retained; it MUST NOT be inferred from ancestry or apply exit status.
- Core reconciliation owns recovery policy and transitions. Runtime drivers implement preparation, mutation, quiescence, cleanup, and restoration semantics behind their contracts; source drivers supply retained immutable inputs. No core policy depends on Git ref types or Compose service names.
- Recovery MUST use rollback enablement and operation timeout frozen in the candidate journal for ALL actions, including prior/candidate/restoration stop, image import, workspace preparation, and apply. Operand historical timeouts and new startup policy MUST NOT override it. Health windows/checks use the saved policy of the instance being verified. Recovery MUST use saved inputs for each operand. It MUST NOT resolve moving refs, substitute current candidate settings for last-good, or allow changed startup policy to rewrite pending recovery.
- Before any recovery mutator, first validate required last-good/candidate cleanup inputs and full readable image archives plus storage admission under 005, even if the daemon currently holds those images. Missing/inconclusive inputs block the next mutator and never permit build, pull, authentication, or Git network fallback. These checks fit within the existing validation/preparation action budget; collection is not added to the recovery path. Before any recovery mutation or verification, the driver MUST establish that previous deployment-mutating actions and checks cannot still affect results. If completion cannot be established, keep `recovery-required` and the journal without issuing competing actions. The runtime specifications define permissible proof; state-lock ownership, client-process absence, and successful health alone are insufficient.

### Restoration and cleanup

- With rollback enabled and last-good available, Pulltide MUST durably record restoration intent and make one bounded restoration attempt. Exec MUST finish prior stop if candidate apply never began, or stop candidate with candidate inputs if its apply may have begun, before applying last-good. If an interrupted restoration apply may have begun, cleanup MUST stop the restoration deployment using its own saved inputs, not candidate inputs. Stop failure blocks apply.
- Compose restoration MUST apply saved last-good to the same owned daemon/project and remove orphans under [the Compose contract](005-runtime-compose.md). It MUST preserve named volumes and unrelated resources. An unresolved daemon-side action blocks restoration until quiescence is proven.
- Allocate a fresh restoration instance/workspace, journal its preparation and artifact import, and retain its exact operand before apply. Compose MUST reload missing images only from verified retained archives; never rebuild/pull, even if Dockerfiles or dependencies remain available. Restored apply uses last-good's immutable artifacts and saved health checks. Success atomically binds active AND last-good to the fresh instance/workspace, preserves target/configuration/image identities, records suppression/rolled-back, and clears recovery in that same commit. Old operands remain retained until durably unreferenced.
- With rollback disabled or no last-good, Pulltide MUST clean every possibly active owned deployment in the transition. Exec MUST complete prior stop when needed and stop any candidate or restoration whose apply may have begun, using each operation's saved operand; it MUST NOT reapply last-good. Compose MUST clean all owned project resources, including services absent from candidate files, without deleting named volumes. Only confirmed complete cleanup permits active `absent`, cleared pending recovery, and phase `failed`; uncertainty or cleanup failure retains pending recovery with active `unknown` and `recovery-required`.
- Last-good metadata MUST survive disabled rollback, cleanup, and restoration failure. It MUST NOT imply current health or restoration of data volumes or external side effects.

### Bounded attempts and restart

- An uninterrupted process MUST attempt recovery at most once per failed deployment. Recovery failures MUST NOT recursively trigger another rollback and normal polling MUST NOT retry them. Each restart with a valid pending journal, retained inputs, usable storage, matching ownership, and proven quiescence MUST attempt recovery once. If prerequisites are unsatisfied it MUST remain blocked with an explicit reason. Operator repair followed by restart is the v0.1 retry mechanism.
- For a recovery attempt let T be the journaled operation timeout, H the restored saved health window (zero for absence cleanup), and N the number of distinct missing archived images required for restoration. The attempt budget is at most `(N + 7) * (T + 10 seconds) + H + 10 seconds` when restoring, or `5 * (T + 10 seconds)` for absence cleanup. This covers one validation/preparation action, up to three distinct instance stops (or one Compose cleanup), N image imports, one restore apply, and two durability/absence-observation actions. Each action is individually bounded; no build/pull is allowed. Failure or unproven termination ends the attempt immediately; unused budget does not permit retries. No external action is repeated within an attempt; a completed successful cleanup checkpoint can be skipped. A repeated stop after a prior process failed counts as the new process's one attempt, not an in-process retry. Elapsed time never establishes quiescence.
- Recovery journal stages, action operands, and durable completion evidence MUST follow [local state](007-state.md). Failed or interrupted runtime mutation attempts MUST suppress their candidate. The same failed desired episode MUST NOT be redeployed after restoration or cleanup. Successfully observing a different fully resolved target durably clears suppression, allowing later return to the former target.
- Restart dispatch MUST follow every stage/result row in 007. Completed candidate verification without atomic success never promotes on restart. Interrupted preparation and startup-observation journals are handled by kind, never by workload rollback. Clean restart health failure without a pending workload-mutation journal MUST NOT trigger automatic reapplication or rollback. It requires operator repair and restart health verification as defined in local state.

## Non-goals

Manual revision-selection APIs, multi-revision rollback history, polling retries of a failed desired episode, automatic recovery from lost ownership, database restoration, volume restoration, and undoing external command side effects.

## Failure behavior

Missing retained inputs, ownership mismatch, unproven quiescence, failed cleanup, failed restoration, failed verification, or failed persistence MUST remain visible and MUST NOT be reported as successful rollback. Preserve last-good and pending evidence; active becomes unknown after uncertain mutation. Storage failure prevents dependent mutations. Runtime availability repaired after a failed recovery does not itself authorize polling retries; restarting initiates the one bounded retry when all prerequisites hold.

## Acceptance criteria

| ID | Scenario | Required observation |
| --- | --- | --- |
| RB-01 | Healthy A, candidate B applies but fails health | Stop B where required, apply retained A, verify saved A checks, then atomically report rolled-back; B never last-good |
| RB-02 | Source/registries/helpers unavailable and daemon cache cleared after successful A source build | Reload retained A image archives, restore exact IDs with zero rebuild, pull, credential-helper, or Git-network calls; corrupt archive blocks before another mutator |
| RB-03 | Prior A stop fails or is interrupted before B apply | B apply absent; B suppressed; after quiescence finish A stop before A restoration; another stop failure blocks apply |
| RB-04 | First deployment B fails; separately, A exists with rollback disabled and B fails | Complete all owned cleanup; active absent and failed only on confirmed cleanup; historical A retained in disabled case; zero A apply calls |
| RB-05 | B fails, A restoration apply or health fails | One restoration attempt, active unknown, historical A preserved, journal retained, no polling mutation or recursive rollback |
| RB-06 | Crash during A restoration, then another crash during its cleanup | On both recoveries use A's stop operands, never B's configuration; never redeploy B; saved stages and operands remain durable |
| RB-07 | Delayed B mutation after timeout; quiescence cannot be proved | No A apply or health verification; recovery remains blocked even if an endpoint reports healthy |
| RB-08 | Repair failed recovery prerequisites and restart, with policy changed in current document | Exactly one attempt under frozen policy; successful restore checked; another failure returns to blocked state |
| RB-09 | Compose update removes old service before failing; cleanup runs | All owned old and candidate service resources absent before active absent; named volumes and unrelated projects preserved |
| RB-10 | Runtime commands or checks exceed their budgets | Attempt stops within specified operation/check budgets plus grace; uncertain termination remains recovery-required |
| RB-11 | Failed B restored to A, then same B polled; later C is successfully resolved and B returns | Same episode B suppressed; observation C clears suppression durably; returning B eligible for a new attempt |
| RB-12 | Restore succeeds externally but atomic outcome persistence fails | No rolled-back success reported; pending journal retained and next startup conservatively recovers |
| RB-13 | Fresh restored workspace differs from old A; restart and later replacement | Active/last-good reference restored instance; health and stop read its generated files; GC preserves live workspace |
| RB-14 | Completed success/failure or uncertain result at every journal stage | Exactly 007 actions; no uncommitted candidate continuation; cleanup-completed operands skipped; one bounded attempt per restart |
| RB-15 | Preparation build or startup check interrupted | Quiescence required; A not stopped/restored; candidate not suppressed merely for preparation/observation |
| RB-16 | A/B/new config timeouts differ; N archived images missing | Every recovery operation uses B frozen T and bound above; checks use A health policy; no build fallback |
| RB-17 | Prior rollback artifact admission fails before B versus storage failure during recovery | Before B no mutation/suppression and A retained; pending recovery preserves journal and blocks dependent mutators even after a passing capacity estimate; no protected deletion |
