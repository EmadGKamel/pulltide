# M05 Git source adapter: acceptance and boundaries

The approved two-phase source preparation amendment is implemented as an internal
library. The public `pulltide.io/v1alpha1` Application contract is unchanged.
There is no deployment CLI, daemon, or real runtime driver.

## Recovered work and completed changes

The interrupted session had already amended specifications 002, 003, 004 and 005,
introduced the source/runtime interfaces, added core sequencing, and implemented
Git staging/publication and retained-source restoration. The earlier packed-object
corruption regression was preserved. This continuation inspected those files and
completed acceptance tests, closure handling and documentation rather than
replacing the implementation.

The preparation sequence is resolution, journaled private staging, read-only
runtime discovery, complete closure validation and durable publication, runtime
preparation, retained validation, then workload intent/action. Git never parses
Compose or Exec dependency formats. Discovery and runtime preparation receive a
source-neutral read-only snapshot; bytes and entry metadata are returned as owned
values. Executable modes and stored symlink targets remain available for exact
builder input copies. After publication, reads outside the accepted closure fail.

An immutable source snapshot plus a versioned manifest bind the revision, selected
path, snapshot checksum, and resolved accepted roots (including symlink-reachable
siblings). Both files are checksum-referenced through the existing state contract.
Their contents and directory entries are synchronized before an eligible instance
is returned. Mutable workspaces are separate copies. Restoration verifies the
entire retained closure and creates a fresh direct-child workspace under the
pending attempt's frozen deadline, without invoking Git or runtime preparation.
Runtime-specific local restoration follows source restoration.

The documented defects are resolved:

- Symlink resolution checks directory type before every component, including dot,
  dot-dot and trailing slash. Regular-file intermediate components fail.
- Selected-source validation occurs before staging callbacks; discovered closure
  validation occurs before publication callbacks. Invalid discovery/publication
  cannot reach runtime preparation or workload mutation.
- Staging and restoration use exclusive, canonical direct-child namespaces.
  Existing, nested, traversing and symlinked workspace paths are rejected.

Additional regressions found and fixed: external Git alternates could incorrectly
supply another source's offline commit; published access could omit accepted
symlink-reachable siblings; source restoration lacked a frozen deadline for its
observational retained-input validation. Alternate/promisor/grafted/shallow caches
are rejected, symlink-reachable roots are retained in the closure, and core supplies
the restoration deadline. None of these changes assert runtime quiescence.

## Acceptance coverage

All test names below are in `internal/source/git`, unless a state test is named.
Local repositories support both SHA-1 and SHA-256 object formats. SSH is a local
upload-pack fixture, with no network server or real credentials.

| Criteria | Observable tests |
| --- | --- |
| GIT-01, GIT-02 | `TestResolveMaterializeAndOffline`, `TestMovingRefsAndPeeling`: branch update/force movement, lightweight/annotated moved tags, recursive tag peeling, rejection of blob/tree tags, full tuple identity, byte-exact materialization and executable modes. |
| GIT-03, GIT-05, GIT-11 | `TestSSHAuthenticationTimeoutInterruptionAndRetry`: SSH username selection, denied authentication with a fake secret, in-flight timeout, truncated transport, no cached branch fallback and successful later poll. `TestSanitizedFailuresAndCancellation` checks credential-bearing URL rejection and cancellation. State/core policy is separately exercised below. |
| GIT-04, GIT-12 | `TestColdExactCommitAndSourceIdentityAcrossRestart`, `TestResolveMaterializeAndOffline`: cold exact-commit retrieval, uppercase IDs, offline verification, both object lengths, absent commit rejection, restart-stable tuples and distinct URL spellings. `TestCacheAlternatesCannotSupplyAnotherSource` and `TestCacheScopeIntegrityAndIndependentRetention` reject cross-source/incomplete caches. Existing M04 tests retain opaque non-Git tuple coverage. |
| GIT-06, GIT-16 | `TestInputConfinement`, `TestValidationBeforeCallbackAndWorkspaceOverlap`, `TestNamespaceConfinement`, `TestSymlinkResolutionMatchesMaterializedFilesystem`, `TestSymlinkTraversalLimitFailsBeforeStaging`: escaping/missing/non-directory paths, dangling/cyclic/escaping symlinks, regular-file intermediate components, symlink target preservation, finite repeated link traversal versus true cycles, the native Linux lookup limit, and overlap/traversal/reuse rejection before callbacks. |
| GIT-07 | `TestHooksFiltersAndCacheWorkFilesCannotChangeBytes`, `TestSnapshotEntryMetadata`, `TestResolveMaterializeAndOffline`: hooks/filter/line-ending settings and modified/untracked cache files do not change tracked bytes; executable modes and link metadata survive; `.git` is absent; workspace writes do not change retained bytes. |
| GIT-08, GIT-14 | `TestDiscoveryCompleteSiblingClosure`, `TestUnsupportedClosure`: a runtime discovery double follows transitive sibling definitions, env files and full context directories. Ignored LFS/gitlink inputs, missing/escaping roots and reference cycles fail; safe siblings succeed; unsupported content outside the closure remains unexpanded. Invalid closure returns no eligible reference and invokes no publication callback. |
| GIT-09, GIT-17 | `TestInterruptedPublicationAndCorruption`: injected interruption after stage creation, snapshot sync, artifact/manifest sync/rename, extraction, workspace sync and publication returns no eligible reference; fresh attempts succeed. `TestRetainedManifestAndArtifactCorruption` rejects missing, corrupt and symlinked retained files before restoration callbacks. |
| GIT-10, GIT-15, GIT-17 | `TestSnapshotSurvivesConcurrentSourceChanges`: moved/fetched remote and changed returned buffers cannot alter the candidate; removing the ref, cache and remote still permits exact fresh restoration. A recording Git executable verifies zero Git calls. `TestStagingChangedBeforePublication` rejects changed/missing staging before and inside publication; `TestPublishedSnapshotPreservesSymlinkReachability` preserves the full accepted closure. |
| Concurrency | `TestConcurrentCacheAndExclusiveMaterialization`, `TestConcurrentPublicationHasOneEligibleResult`: shared-cache fetch serialization, exclusive staging/workspace creation, and exactly one successful publication per snapshot. |
| RECON-18 | `internal/state/TestEngineTwoPhaseSourcePreparationFailuresPreserveActive`: actual store plus adapter doubles verify staging/discovery/publication/preparation/validation/action order, preservation of running active/last-good on failure, zero dependent calls and no failed-target suppression. |
| RECON-19 | `internal/state/TestEngineSourceRestorationPrecedesRuntimeRestoration`: verified source restoration with a deadline precedes runtime-specific restoration/apply; unavailable retained recovery input blocks dependent recovery actions and preserves last-good/journal. |
| GIT-13; EXEC-11; COMPOSE-21 | Source confinement and neutral discovery/retention interfaces are covered. Actual external data mounts, executable validation, Compose reference semantics, builder invocation and runtime restore require future runtime adapters. No real-runtime acceptance is claimed. |

## Red/green evidence

- The original corruption test failed at a presumed loose-object path. Actual
  `count-objects`/`verify-pack` output confirmed packed storage. An isolated Git
  2.43.0 comparison established that strict fetch verification selects
  `index-pack --strict`. The test now removes checked loose/verified pack storage
  and confirms the blob is unreadable before asserting offline resolution fails.
- The documented defect regression initially accepted `run/../run`, `run/.` and
  `run/` symlinks and invoked callbacks; nested restoration was accepted. Those
  assertions now pass without weakening source safety.
- New offline alternate-cache and published-sibling regressions initially failed
  because content from another source was accepted and reachable siblings were
  blocked. Both pass after the owning source layer fixes.
- Snapshot metadata initially failed to compile because mode/target access was
  missing. The neutral entry contract now supplies both, and the test passes.
- A final symlink regression initially rejected a safe finite repeated traversal.
  Active-target tracking now distinguishes it from a cycle, while enforcing the
  native Linux 40-traversal limit; both kernel-comparison tests pass.
- Source restoration initially failed the deadline assertion. Core now supplies
  the pending policy's bounded recovery context; the ordering/deadline test passes.

## Compatibility and remaining boundaries

No public Application fields, API group/version, state format version, or runtime
features changed. The earlier uncommitted one-file M05 snapshots have no complete
manifest and are deliberately not accepted as retained inputs. There is no
production M05 migration or automatic rewriting/deletion of existing data.

The adapter requires the installed host Git executable and host authentication
facilities. Tests use local Git 2.43.0; they do not qualify real HTTPS/SSH servers,
external credential stores, physical power loss, faulty storage firmware, or
arbitrary malicious same-account native code. Interruption tests inject durability
boundary failures, not actual hardware power cuts. The dedicated account and
local filesystem fsync/rename/flock behavior remain prerequisites under 007.

Snapshots and retained verification use memory proportional to repository size.
Timeouts fail safely; no streaming/large-repository performance guarantee is made.
Corrupt or interrupted caches may require operator removal/repair of the disposable
cache; the driver never substitutes a different revision. Retained inputs remain
independent of that cache. No artifact/workspace reclamation is added: existing
state/core reference ownership remains authoritative and unreferenced partial
files are retained safely for later bounded reclamation work.

Actual runtime drivers must implement complete dependency discovery, permitted
external dependency validation, exact builder input copying, image/archive handling
and quiescence. These are explicit follow-on boundaries, not open decisions for
this M05 source implementation. No subagents, commits, pushes, or M06 implementation
were used.

## Final verification

All requested checks passed after the final implementation change. Go used the
installed `/usr/local/go/bin` toolchain, `GOCACHE=/tmp/pulltide-go-cache`,
`GOPROXY=off` and `GOSUMDB=off`; no dependencies were fetched or installed.

| Command | Result |
| --- | --- |
| `gofmt -w` on changed Go files; `rg --files -g '*.go' -0 \| xargs -0 gofmt -l` | Formatted; no remaining formatting output |
| `go test ./...` | Passed |
| `CGO_ENABLED=1 go test -race ./...` | Passed, including Git and state/core integration tests |
| `go vet ./...` | Passed |
| `python3 tests/contract/check.py` | 121 tests passed; exact dependency pins verified; network disabled |
| `go build -o /tmp/pulltide-m05-contract ./cmd/pulltide` | Passed |
| `python3 tests/contract/parity.py /tmp/pulltide-m05-contract` | 105 fixtures; 17 valid, 88 invalid; zero failures |
| `git diff --check` | Passed |
| Untracked-file `git diff --no-index --check` inspection | Eight files checked; no whitespace diagnostics |

Earlier red tests listed above were expected TDD failures, not remaining failures.
No available check is blocked by network access. Real runtime behavior and hardware
crash durability remain the explicitly unclaimed boundaries above.
