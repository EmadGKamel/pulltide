# M03 durable state and operation journal

`state` is a Linux-only internal storage layer. It invokes no source/runtime
driver, health command, reconciler, scheduler or rollback. The existing public
Application contract and validation CLI are unchanged.

## Ownership and format

Production APIs use only `/var/lib/pulltide`; alternate roots are private test
helpers, not configuration. The root must be owned by the current service UID
with no group/other permission. Records/lock files are private regular files.
Path traversal uses directory file descriptors and no-follow opens. Application
names, runtime/resource bindings, installation identity and service UID are
checked before use. Compose ownership records retain the daemon/project tuple;
no daemon is inspected by this package.

`Initialize(true)` is an explicit caller attestation that an operator confirmed
no prior resources or mutators remain, including first installation. This is the
maintainer-approved resolution of the missing-registry bootstrap ambiguity. It
refuses nonempty/existing stores and provides no automatic repair/adoption.
Normal `Open` fails closed without the separate ownership registry. `Enroll`
requires explicit resource-absence evidence from its caller; it does not inspect
or adopt resources. Current Application validation and actual driver ownership
checks must precede these calls in the future orchestrator.

Version 1 uses complete checksummed JSON envelopes. Unknown versions/fields,
duplicate JSON keys, invalid identities, inconsistent operands/stages and corrupt
checksums are rejected. Configuration identities implement specification 001's
canonical `identity-v1`; saved specifications must already be valid/defaulted.
There is no prior persisted M03 format or migration path; unsupported formats
remain blocked without rewriting them. No production data migration/deletion
is performed.

The registry and per-Application state are separate atomic files. Enrollment
writes state before binding it: interruption between those commits leaves an
unbound record that is blocked, not silently adopted or overwritten. Bound names
with missing state likewise require operator repair. Registry/root loss is never
interpreted as an empty installation.

## Durability and concurrency

An Application `flock` lives for the handle lifetime. A host artifact `flock`
serializes registry updates and every authoritative reference publication/release
across Applications. Lock files are never unlinked. A mutex serializes operations
and close within one handle; callers receive independent state copies. Locks are
advisory and require all core writers to cooperate. Same-account operator code
and host administrators are trusted, not sandboxed.

Commits write a unique same-directory temporary file, check short writes, sync
and close the file, rename it, then sync the containing directory. Errors poison
the handle; no subsequent transition is accepted until reopen. After a possible
rename, the visible record may be either complete generation, never a partial
record. Reopen validates the record, binding and declared retained inputs.

Immutable driver-declared input files are read fully and SHA-256 checked, with
files and containing directory chains synchronized before publication. Success
checkpoints synchronize the entire owned checkpoint workspace, including nested
directories. Existing unrelated active workspaces are not used as checkpoint
barriers. Source confinement, full driver input closure, manifest/image semantics,
and external storage admission remain driver responsibilities. Successful driver
completion/quiescence is required before accepting a workspace checkpoint; app
writes/external data are not transactionally snapshotted.

Drivers may record already-published generated recovery files with
`RecordCheckpoint`; paths are relative to that instance's workspace. Stable
checkpoints verify their existence/readability, not byte equality. Interrupted
or failed actions may leave partial/missing generated files on that exact
operand; cleanup must tolerate them. Successful stop clears its generated-file
requirements. Restoration starts with a distinct workspace and no inherited
mutable markers; its apply publishes its own recovery files. All immutable
source/config/archive references remain mandatory, including during recovery.

On checkpoint/persistence failure, a bounded best-effort write attempts sanitized
failure/unknown evidence without a usable-workspace barrier. It reads the visible
complete generation after a possible rename, preserves pending operands, and
never permits further work on the poisoned handle. If a terminal commit failed,
its pending pre-commit journal is retained by that best-effort record. A failed
fallback leaves the last complete record for reopen inspection.

## Journal and restart boundaries

Preparation, deployment and observation journals retain unique attempt/operation
IDs, boot identity, frozen candidate policy, exact operands, staging references,
per-check intents/results and cleanup evidence. Instance bindings are immutable;
only declared mutable recovery-file metadata changes at a completed checkpoint.
Distinct retained workspaces cannot be identical or contain one another.

Desired observation and suppression clearing share one commit. Candidate or
restoration promotion, active/last-good publication, suppression where required,
and journal clearing likewise share one commit. Disabled/no-last-good cleanup
commits absence and suppression together, retaining historical last-good.

`InspectRecovery` maps every specification 007 row/result to an obligation. It is
not an executable recovery plan or permission to mutate. Unknown intent remains
uncertain even after lock release/process death. Boot IDs are retained; this
layer conservatively requests quiescence evidence for uncertain results rather
than independently declaring processes/daemon operations stopped.

`BeginRecovery(true)` requires the future driver's explicit quiescence assertion.
Recorded assertions are not proofs obtained by the store. Resumed journals
cannot continue/promote the old candidate or restoration. Old startup windows
must be safely abandoned before a fresh window. Recovery failure blocks another
session on the handle; normal polling is not implemented. The future core must
keep one ownership handle for its process lifetime to enforce process-wide retry
limits. Closing/reopening a handle is not an in-process recovery retry API.

Per-check completion is distinct from whole-window health success: promotion
requires a full ordered successful round and an explicit whole-window result.
Historical health is reset to unknown on open. Startup failure cannot start
another window on the same handle. No runtime action is run by these methods.

## Acceptance evidence and limits

| Approved criteria | Tests / scope |
| --- | --- |
| STATE-01/02 | Private root/registry, missing bound state, same-handle/concurrent/process locks and no adoption |
| STATE-03/07/16/22 | Atomic boundary faults, short writes, nested input/workspace barriers, checkpoint failure evidence and poisoned handles |
| STATE-04/06; RECON-16 | Every journal stage with absent/success/failure/unknown result, prepared stray evidence, restart candidate-continuation regression |
| STATE-05/15; RB-12/13 | Exact restoration instance, fresh workspace, frozen policy, atomic active/last-good/suppression and failed terminal commit |
| STATE-08 | Corrupt/unsupported records, invalid inputs, symlinks, checksums and missing required recovery files |
| STATE-09/13/14/17 | Historical health reset, observation intent survives uncertainty, restart-only startup retry and no observation suppression |
| STATE-10; RECON-04/05 | Suppression survives same desired/stale/restart; only a different resolved target clears it |
| STATE-11 | Candidate timeout/rollback policy stays frozen through restoration despite prior settings |
| STATE-12 | Killed owner releases file lock but pending intent still requires external quiescence evidence |
| STATE-18/19 | Successful cleanup checkpoints are preserved; host artifact lock serializes publication/release |

These are storage/inspection tests, not complete driver/end-to-end acceptance
claims. Tests simulate syscall boundary failures and kill a lock-owning process;
they do not simulate physical power loss, faulty storage firmware or a specific
filesystem's crash recovery. Guarantees depend on correct Linux local filesystem
`fsync`/rename/flock behavior and the dedicated account's exclusive management.

Actual quiescence, runtime/resource absence, whole health-window timing, complete
source/driver closure, image identity/platform semantics and storage-capacity
admission require future drivers/core integration. STATE-20 reclamation scheduling
and deletion are not implemented. No files are automatically deleted, including
unreferenced artifacts or interrupted `.pending-*` records; bounded reclamation
must add its durable backlog and final cross-Application reference checks later.

Run `go test ./...`, `go test -race ./...`, `go vet ./...`, formatting verification,
`python3 tests/contract/check.py`, and the existing fixture parity command.
Race testing additionally requires `CGO_ENABLED=1` and an available C compiler.

## Validation in this environment

With the maintainer's Go 1.27.3 toolchain, formatting verification,
`go test ./...`, `go vet ./...`, the 121 Python contract checks and all 105
Python/built-Go-CLI expected fixture outcomes passed. `git diff --check` passed.
The standalone parity script from the separate CI work is not on this branch;
the existing manifest/checker and a temporary CLI parity runner were used without
changing contract tests or fixtures.

`go test -race ./...` could not execute: default cgo is disabled, and retrying with
`CGO_ENABLED=1` fails because `gcc`/`cc` is unavailable. No compiler was installed.
After a maintainer-provided C compiler is available, rerun
`CGO_ENABLED=1 go test -race ./...`. This milestone is not race-validated yet.
