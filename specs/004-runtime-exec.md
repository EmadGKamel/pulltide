# 004 — Exec runtime

## Purpose

Deploy applications through operator-supplied local commands with a defined lifecycle.

## Scope

Exec preparation, replacement, cleanup, restoration, observation, execution context, timeouts, and recovery compatibility on the local Linux host.

## Requirements

### Execution and lifecycle

- `apply` and `stop` MUST be nonempty argument arrays executed without an implicit shell. Operators may explicitly invoke a shell. Preparation MUST validate the working directory and executable accessibility before stopping the prior deployment.
- Commands MUST run in the deployment's retained mutable workspace at its configured source path. The environment MUST contain only `PATH=/usr/bin:/bin`, `LANG=C`, and the saved `exec.env` overrides; no host environment is inherited. Executable lookup uses that effective PATH. The same saved context MUST be used during cleanup, restoration, and command health checks.
- The immutable source snapshot and mutable workspace MUST be separate. Commands may write their workspace but MUST NOT modify the immutable snapshot. Files needed by stop or health MUST remain in that retained workspace. Restoration MUST create a fresh unique deployment instance/workspace from last-good's immutable snapshot; allocate and durably record it before apply. An interrupted restoration retains its own instance/operand for cleanup. Success atomically binds active and last-good to the restored instance as specified in 007. Source/definition confinement does not prohibit explicit host executables or operator-managed external application data; those are not retained source inputs.
- For replacement, preparation MUST complete before the prior deployment's stop begins. That stop MUST complete successfully before candidate apply. First deployment has no prior stop. Core reconciliation owns intent recording and policy; the driver returns outcomes and MUST NOT promote last-good or independently initiate rollback.
- Apply MUST finish after arranging the application's long-running service through an operator-provided mechanism. Pulltide is not a service supervisor. Deployment mutations MUST finish within the command lifetime: commands MUST NOT leave background deployment scripts, remote deployment jobs, or scheduled deployment mutations running after return. A long-running application service is permitted, but it MUST NOT continue unfinished deployment work.
- Operators MUST supply idempotent apply and stop commands. Stop MUST tolerate an already-stopped or partially applied deployment and complete removal of the owned workload. Apply MUST tolerate replay after quiescence has been established. Command success cannot prove arbitrary scripts meet these obligations; these are operator responsibilities.
- Commands MUST publish generated stop/health recovery files crash-safely and tolerate missing/partial files after interruption. Before durable completion or a dependent action, Pulltide synchronizes the owned workspace and directories under 007; failed synchronization blocks advancement. Running application data and external paths are not transactionally snapshotted.
- Cleanup MUST use the configuration and workspace of every deployment whose apply may have started. A previous stop that may have started MUST be finished before restoration. Restoration applies last-good only after required stops complete successfully, then uses last-good health checks. Stop failure prevents any further apply.
- Zero exit status indicates operation success; nonzero exit or launch failure indicates failure. Apply success alone never establishes deployment health. Stop success means absent only for conforming commands that satisfy the complete-removal obligation.

### Bounds and uncertain completion

- The current attempt's frozen operation timeout bounds normal commands. Every recovery command uses the pending attempt's frozen timeout, regardless of the saved operand's historical timeout or newly loaded configuration; the operand still supplies command/env/workspace. At timeout, Pulltide MUST send termination to the launched process group, allow at most five seconds for exit, then force termination and allow at most five further seconds for exit confirmation. Failure to confirm termination returns `completion-unknown`; no further runtime mutation is permitted.
- Driver outcomes distinguish `completed-success`, `completed-failure`, and `completion-unknown`. A nonzero exit is completed-failure only when command descendants that could mutate the deployment have also exited. Timeout with confirmed termination is completed-failure. These results describe mutation completion, not health or rollback success.
- After Pulltide process death, an operation whose journal says it may have started MUST be treated as completion-unknown until quiescence is proved. Missing PID, a passed health check, or restarting Pulltide on the same host boot is insufficient. For v0.1 local operations, a changed Linux boot identity establishes that old command processes cannot continue; external or reboot-persistent deployment jobs are prohibited. Without proof, remain recovery-required rather than starting cleanup or restoration.
- Raw stdout and stderr MUST NOT be persisted or exposed in logs/status. Each stream may use at most 64 KiB of transient capture; excess bytes MUST be discarded while draining the stream so output cannot block the command. Diagnostics MUST contain only operation, exit/signal/timeout category and bounded structured identifiers, never arguments, environment values, or raw output.

## Non-goals

Built-in process supervision, sandboxing untrusted commands, automatic privilege escalation, shell language interpretation, remote deployment jobs, and transactional reversal of arbitrary command side effects. Host tools and external data are operator-managed dependencies; their contents are not archived by saving configuration.

## Failure behavior

A failed prior stop prevents candidate apply and invokes the shared recovery policy. Failed candidate apply or health invokes that policy without advancing last-good. Unknown completion blocks all cleanup and restoration until quiescence is proved. With no last-good deployment, confirmed failed work is cleaned up; cleanup failure or uncertain termination leaves active unknown and recovery-required. Environment changes in the Pulltide parent process MUST NOT change saved execution inputs.

## Acceptance criteria

- **EXEC-01:** Arguments containing spaces or shell metacharacters reach a recording fixture literally unless an explicit shell is selected.
- **EXEC-02:** Changing the parent PATH, HOME, and a same-named environment variable across restart does not change a fixture's saved environment; only the baseline plus saved overrides are present. Its working directory is the retained workspace source path.
- **EXEC-03:** A trace shows preparation, prior stop, then candidate apply. Failed prior stop produces no candidate apply; successful apply followed by failed health never promotes last-good.
- **EXEC-04:** An idempotent service fixture survives replay of stop/apply with exactly one service instance after recovery. This validates Pulltide ordering with a conforming fixture, not arbitrary script idempotency.
- **EXEC-05:** A command ignoring graceful termination receives forced termination after five seconds; confirmation ends within the additional five seconds. An unconfirmed survivor produces completion-unknown and no subsequent mutation.
- **EXEC-06:** Killing Pulltide while a fixture can still mutate resources leaves recovery blocked on same-boot restart, even if its original PID disappears or health passes. After a changed boot identity establishes quiescence, journal recovery can proceed.
- **EXEC-07:** A fixture emits more than 64 KiB per stream, including fake credentials. It does not block on output; transient capture stays within the per-stream limit, and status/logs contain none of the raw output or configured secrets.
- **EXEC-08:** Generated recovery files survive committed completion across power loss; stop tolerates missing/partial files after interrupted apply. Restoration marker lives in a new instance/workspace used by later startup health/stop and protected from GC.
- **EXEC-09:** A timeout of 10, B timeout of 30, and new startup timeout of 60 yield 30 for recovery stop/apply of A; saved A commands/env/workspace remain authoritative.
- **EXEC-10:** Interrupted synchronization before completion yields persistence failure with no dependent command or success; external application-data writes are never claimed restored.
