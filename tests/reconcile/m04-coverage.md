# M04 reconciliation acceptance coverage

The engine is an internal library, with no deployment CLI, daemon, real source,
process health runner, or runtime driver. Tests use fake adapters and the actual
M03 state store in test-only isolated roots. Production roots remain fixed.

## Contract and evidence

`New` validates before acquiring canonical ownership and exclusively owns the
returned Store until Close. Adapters never receive state-writing access. Adapter
orchestration and plan methods must be synchronous and observational; all external
preparation effects go through `Artifacts`, with one unique operation per callback.
Source materialization uses the journaled staging namespace. Driver results assert
termination of operation-side effects; neither persistence nor a free ownership
lock is termination evidence. Unknown outcomes block further checks and mutations.

Core owns eligibility, frozen policy, health rounds, recovery, and authoritative
transitions. Runtime adapters own replacement mode, exact cleanup groups, input/
archive/storage/ownership admission, local-only restoration, and quiescence
proof. Inspection is observational and cannot pull/import/build/clean resources.
HTTP/process semantics and the five-second graceful/five-second force termination
sequence belong to future adapters; core bounds calls and never treats a timeout
or panic as proof. Source/runtime plans may not run effects outside their callback,
retain callbacks, or schedule their own retries. These are trusted internal driver
contracts, not a sandbox for arbitrary Go plugins.

## Tests mapped to approved criteria

Tests are in `internal/state/reconcile_test.go` (external package integration with
real persistence), `internal/state/engine_support_test.go`, and
`internal/reconcile/operations_test.go`.

| Criteria | Observable coverage |
| --- | --- |
| RECON-01, 13; GIT-12; PRODUCT-06 | Deploy then no-op; opaque source revision tuple; normalized configuration identity; neutral runtime contracts |
| RECON-02, 11 | Fixed-delay polling; concurrent cycles serialized; shutdown during apply retains the original completed result and pending journal; no implicit workload stop |
| RECON-04–06, 12 | Source outage retains active, desired, and suppression; different desired episode clears suppression; prior-stop failure prevents candidate apply; no-op clears only eligible cycle errors |
| RECON-07–10, 15–16; STATE-04, 06, 09, 12–14, 17–18 | Actual-store restart matrix for every durable stage and applicable empty/success/failure/unknown result; validation before ownership; no candidate continuation/promotion; observation-only recovery never reapplies; same-boot uncertainty blocks all competing commands |
| RECON-14 | JSON Lines status shape, explicit nulls, transitions, and failed output without rollback; fixed diagnostics only; terminal persistence diagnostic from last trusted snapshot |
| RECON-17; STATE-07–08, 21–22; RB-17 | Failed admission and deleted retained input prevent dependent actions; injected ENOSPC before intent and after apply prevents advancement |
| HEALTH-03–04 | Ordered fail-fast A/B/delay/A/B/C rounds; completion-relative delay; clipped retry; success exactly at deadline fails |
| HEALTH-06–07, 09–10 | Saved startup policy; healthy/unhealthy/unknown observation distinction; observation intent retained for unknown completion; restart retries without apply |
| RB-01, 03–08, 11–16; STATE-05, 10–11, 15–16 | Retained A restored to a fresh identity/workspace; B suppression; cleanup-to-absence when disabled/no last-good; failed recovery blocks in-process retries; fresh restart recovers using frozen T; generated recovery files follow restored instance |
| RB-09; PRODUCT-06 | Driver-declared in-place replacement and grouped cleanup durably cover exact operand sets, without core Compose switches or synthetic prior stops |

The preparation/deployment restart matrix has 33 scenarios: four result states for eight operation stages,
plus prepared (which correctly permits no operation result). A separate observation
matrix covers all four result states. Additional tests cover
unknown observation, failed recovery, cleanup grouping, constructor ownership
release, and persistence/status fault boundaries.

The tests exercise core obligations, not all source/runtime-specific acceptance
criteria. Real Git, Docker, command/HTTP execution, actual signal/process-group
termination, physical power loss, and full image/archive conformance are outside
M04. Existing M03 tests continue checking file/directory sync failure, record
corruption, competing ownership, and crash-consistent writes. Private-artifact GC
is not implemented here; unreferenced files are retained safely rather than deleted.

## Persistence compatibility

Existing v1 records remain readable. New private optional fields record in-place
replacement, exact grouped cleanup scopes, and the suppressed outcome to resume
after startup observation. Zero values are omitted. Existing readers reject
unknown fields rather than misinterpret newer recovery evidence; no automatic
migration or reset is introduced. The public Application API is unchanged.

## Reproduce

With Go and the pinned Python contract dependencies already installed:

```sh
rg --files -g '*.go' -0 | xargs -0 gofmt -l
go test ./...
CGO_ENABLED=1 go test -race ./...
go vet ./...
python3 tests/contract/check.py
go build -o /tmp/pulltide-contract ./cmd/pulltide
python3 tests/contract/parity.py /tmp/pulltide-contract
git diff --check
```

For an untracked working tree, use `rg --files -g '*.go' -0 | xargs -0 gofmt -l`
to include newly created Go files. A writable `GOCACHE` may be needed in restricted
environments. Race checks require a working C compiler; do not report them passed
unless the instrumented suite actually ran.
