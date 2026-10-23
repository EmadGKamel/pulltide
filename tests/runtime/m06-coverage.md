# M06 Exec runtime adapter

M06 implements the approved restricted foreground Exec profile as an internal
library. Specifications 004 and 007 were amended first, with corresponding
clarifications in 002 and 006. No public Application fields or API version changed:
`apiVersion: pulltide.io/v1alpha1`, `kind: Application` remain authoritative.

## Ownership and lifecycle

`internal/runtime/exec.New(store, source, true)` requires an already-open canonical
ownership Store, a source adapter, and explicit operator attestation of the
foreground/local-only command profile. The bound resource identity must already
have been confirmed through M03's enrollment/ownership workflow. Construction
neither enrolls nor adopts resources. The additive `Store.Directory` accessor
exposes the directory of that existing ownership handle; it introduces no alternate
production-root option and performs no writes.

Discovery returns the complete selected subtree and existing declared source
executable references, including sibling lookups, without executing commands or
interpreting shell dependencies. Source fetching/object verification remains in
M05. Preparation checks retained input hashes, working directory and executable
access before prior stop. Source restoration remains core/source-owned; Exec's
restoration method validates the fresh workspace and saved command inputs without
fetching, rebuilding or copying old mutable markers.

Every apply/stop/command check verifies canonical ownership, current durable
operation intent and exact instance binding, required retained inputs, execution
context, and available storage evidence before launching. Saved records, not
caller-supplied command changes or ambient environment, supply execution inputs.
Core alone writes intent/result/checkpoint records, runs health windows, suppresses
targets, and performs rollback policy. The driver never promotes last-good or
starts recovery independently. Successful conforming stop is absence evidence;
health, PID/group disappearance, and registry strings alone are not resource
inspection or completion proof.

## Process and recovery inventory contracts

Commands execute literal argv with explicit cwd and only saved overrides over
`PATH=/usr/bin:/bin` and `LANG=C`. Primary relative executables stay inside their
workspace; absolute dependencies are explicit accessible operator-managed host
tools, never another Pulltide workspace/artifact. Symlink/dot-dot lookup order is
preserved. There is no implicit shell or environment inheritance.

The runner creates a process group, observes foreground exit and that supported
group, drains each output stream with at most 64 KiB retained transiently, and
never returns/logs/persists raw output. Cancellation/deadline sends TERM, then KILL
after at most five seconds, followed by at most five seconds for confirmation.
Linux `waitid(WNOWAIT)` leaves the leader unreaped until signalling finishes, so
its numeric group identity cannot be reused during cleanup. Inconclusive evidence,
signalling failure, surviving/unconfirmed groups or a foreground exit leaving
running group members yields unknown. A driver with an unknown result refuses
further commands; core preserves the pending journal and blocks rollback.

A successful apply must freshly produce `.pulltide-exec-recovery.json` in the
selected cwd, using `{"version":1,"files":["workspace-relative/path"]}`. Empty
`files` is an explicit declaration of no generated recovery requirements. The
inventory and listed readable regular files must satisfy the version/shape,
canonical path, no-follow, ownership, permissions, single-hard-link and durability
rules in 004. The inventory is limited to 1 MiB. Inventory and listed files plus
directory chains are synchronized before returning recovery references. Core then
synchronizes the owned workspace and commits those explicit references atomically.
No dependencies are inferred from arbitrary script text. Partial failed/interrupted
cleanup follows 007's exception instead of requiring an uncommitted inventory.

Stable declarations must match recorded references. A check that invalidates the
saved declaration cannot report successful observation or enable promotion. Every
fresh restoration apply must create its own inventory in its new workspace.

## Acceptance coverage

Unit tests are under `internal/runtime/exec`; real-store/core integration tests are
in `internal/state/exec_runtime_test.go`. All applications are harmless temporary
fixtures; no real application, systemd service, remote job, or Docker runtime is
mutated.

| Criteria | Observable proof |
| --- | --- |
| EXEC-01 | `TestDriverLiteralArgumentsEnvironmentAndInventory`: spaces and shell metacharacters arrive as literal positional arguments; only an explicitly selected shell interprets its script. |
| EXEC-02; command subset of HEALTH-02 | `TestDriverLiteralArgumentsEnvironmentAndInventory`, `TestEffectivePathAndEscapingWorkingDirectory`, `TestRestartedDriverUsesSavedCheckEnvironment`, `TestCommandChecksUseSavedContextAndReportUnavailableContextUnknown`: saved cwd/PATH/LANG/overrides, absent host HOME/secrets, saved check index and restart context. |
| EXEC-03/04/15 | `TestExecRuntimeRecoveryAndExternallyManagedService` (real driver/store/core): preparation, prior stop, failed B cleanup, fresh A restoration or disabled-rollback absence; idempotent synchronous external-manager fixture; suppressed B cannot replay. Existing M04 failure/health ordering matrices remain in force. |
| EXEC-05; termination subset of HEALTH-05 | `TestTimeoutCancellationAndUncertainGroups`, `TestCancellationAfterLaunch`, `TestDefaultEscalationUsesFiveSecondGrace`: foreground children, pre/post-launch cancellation, TERM and actual default five-second KILL escalation. `TestUnconfirmedGroupIsUnknown`, `TestInconclusiveObservationIsUnknown`, `TestSignallingFailureIsUncertain` inject inconclusive evidence safely. |
| EXEC-06; STATE-12; RB-07 | `TestExecInterruptedControllerBlocksSameBootRecovery` kills a real controller while a harmless local command group still exists. The reopened real adapter/core retains unresolved intent, starts no recovery and publishes no last-good. Existing M04 tests cover changed-boot policy; no host reboot is performed. |
| EXEC-07; command subset of HEALTH-08 | `TestOutputIsBoundedAndDiscarded` emits more than 64 KiB on both streams with a fake credential marker, completes without pipe blockage and verifies capture bounds. Returned results contain no raw output; only fixed errors escape the adapter. |
| EXEC-08/13; STATE-23 | `TestRecoveryInventory`, `TestInventoryUnsafeFilesAndDurability`, `TestDriverPreconditionsAndInventoryFailure`: fresh valid/empty inventory, malformed/duplicate/unknown/missing/oversized declarations, invalid Unicode and paths, missing/nonregular/symlinked/hard-linked/writable files, and failed synchronization cannot publish usable references. Restored-instance inventory binding is exercised in the real-store integration test. Wrong ownership is checked natively; changing file ownership is not required by the tests. |
| EXEC-09; RB-16 | Runner consumes the core-supplied context, never historical `Saved.Reconcile` timeouts. Existing M04 `TestEngineFrozenRecoveryTimeoutAndSavedHealth` coverage verifies candidate-frozen policy propagation; runner deadline/cancellation tests exercise the actual termination implementation. |
| EXEC-10/14; STATE-07/16 | `TestExecRuntimeCoreDurabilityFailureRetainsPendingEvidence` injects a real core workspace sync failure after apply; the handle is poisoned, reopened records retain pending intent, no health/success promotion follows. File/directory-sync injection in inventory validation returns no references. |
| EXEC-11 | `TestDiscoverAndPrepareAreObservational`, `TestDiscoverDeclaredExecutableSiblingsWithoutNormalizingSymlinks`: source subtree/sibling references, no command execution, reserved tracked inventory rejection and executable preflight. M05 retains responsibility for unsupported source features/full snapshot integrity. |
| EXEC-12 | `TestForegroundRunner`, `TestLeaderRemainsReservedDuringGroupCleanup`, `TestExecUnsupportedBackgroundWorkNeverStartsRollback`: waiting foreground children complete, unsupported background work stays unknown, group identity remains reserved during signals, and stop/check/rollback are not invoked after uncertainty. |
| EXEC-14; HEALTH-10 | `TestStableRecoveryDeclarationsBlockChangesAndPartialCleanupWorks`, `TestStableActiveRequiresExplicitRecoveryDeclaration`, `TestCommandCheckCannotPublishChangedRecoveryInventory`: changed/missing declarations block dependent commands, interrupted cleanup tolerates partial inputs, unavailable/nonobservational check context is unknown. |

## Compatibility and limitations

Persistent state remains version 1 with the existing `RequiredRecoveryFiles` shape.
Readers/writers and ownership remain core/state-owned; Exec only supplies explicit
recovery references. No existing record or application data is reset, migrated or
deleted to manufacture an empty declaration. Old records remain readable by the
state layer, but a stable active Exec instance without an explicit recovery
inventory binding blocks dependent actions until operator repair. Earlier binaries
contain no production real Exec adapter; this change adds no deployment entry
point or implicit mixed-version execution path.

The process proof is conditional on the approved trusted foreground/local-only
profile. It does NOT prove termination of escaped descendants or arbitrary external
jobs. Detached/background/remote/persistent mutators are unsupported. An external
service manager's synchronous completion contract is the operator's responsibility;
the local file fixture does not qualify real systemd deployments. No cgroups,
subreaper, service supervisor, sandbox or new external completion protocol is added.
Linux process metadata must be observable; inconclusive visibility fails closed.
Uncertain operations may leave a bounded reaping worker awaiting actual process
exit; that is not permission to issue another command. Same-boot process restart,
free locks, PID absence or health cannot supply new quiescence proof.

Inventory truthfulness (including an empty declaration), command idempotency,
correct external resource ownership, and synchronous external job completion remain
operator obligations; arbitrary shell code cannot be verified statically. Host tools
and application data are not archived or transactionally snapshotted. Native path
and permission checks assume the dedicated account and host administrators are
trusted, as in 007; they are not fencing against hostile same-account native code.
Storage admission is an observation, not a reservation or arbitrary-script size
estimate. Actual I/O/durability errors fail closed. Fault tests do not simulate real
power loss, disk firmware failure or an unkillable kernel task.

M06 provides command-check execution only. HTTP policy and a full M07 health
adapter/dispatcher are not implemented; this adapter returns unknown for an HTTP
check rather than fabricate a result. No daemon, production deployment CLI,
Docker Compose adapter, new dependency, subagent, commit or push was added.

## Red/green and verification

Runner/inventory/driver tests first failed because their implementations were
absent. Focused regressions also caught stable active records lacking explicit
inventory bindings and checks incorrectly returning success after changing recovery
declarations; both now fail closed. Early runner tests exposed a drain-channel wait
bug; supervision was also hardened for processes disappearing during proc reads.
Repeated runner and full race checks passed after those changes. The obsolete hung red-test run was interrupted; no red-test failure
remains unresolved.

Commands use the installed `/usr/local/go/bin` toolchain, writable
`GOCACHE=/tmp/pulltide-go-cache`, and `GOPROXY=off GOSUMDB=off`. No dependencies were
fetched or installed. Final check results are recorded below.

| Check | Final result |
| --- | --- |
| Go formatting (`gofmt -w` on changed Go files; repository-wide `gofmt -l`) | Passed; no unformatted files |
| `go test ./...` | Passed |
| `CGO_ENABLED=1 go test -race ./...` | Passed, including real Exec/core/store and controller-death tests |
| `go vet ./...` | Passed |
| `python3 tests/contract/check.py` | 121 tests passed; exact pins verified; network disabled |
| `go build -o /tmp/pulltide-m06-contract ./cmd/pulltide` | Passed |
| `python3 tests/contract/parity.py /tmp/pulltide-m06-contract` | 105 fixtures: 17 valid, 88 invalid; zero failures |
| `git diff --check` and untracked-file whitespace inspection | Passed |

No requested check is unavailable or blocked. Results apply to the implementation
and tests in this working tree; production deployment and stronger containment
qualification are not claimed.
