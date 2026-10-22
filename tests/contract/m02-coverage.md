# M02 acceptance coverage

Run `go test ./...` for Go tests and `python3 tests/contract/check.py` for
reference checks. Both consume the same complete manifest; Go compares
acceptance/rejection, not Python-specific error stages.

| Specification | Observable tests |
| --- | --- |
| API-01, API-02 | `TestContractFixtures`: exact identifiers, closed discriminated runtimes/checks |
| API-03 | Fixture corpus, `TestClosedObjectsAndNulls`, malformed/secret-safe cases; Python required/type/unknown-field matrices |
| API-04 | Duration fixtures, `TestDefaultsAndOrder`: integer typing/bounds and defaulted per-check comparison |
| API-05 (defaulting/order) | `TestDefaultsAndOrder`, nonmutation check in every fixture; identity hashing remains outside the parser milestone |
| API-06 (static) | Corpus for executable/argv/env/path/files; no executable or file access |
| API-09 (spelling/order) | Corpus preserves input strings; ordered files/checks and duplicate files checked explicitly |
| API-10 (input only) | Compose files accepted, embedded build fields rejected; no Compose build or driver |
| API-11 | Shared Core fixtures, `TestCoreNumericValues`, `TestCoreScalarResolution`; Python tests exact scalar values and types |
| 001/003 complete URL syntax | `TestURLSemantics` plus source/check URL corpus; parsing has no network access |
| 001 validation before mutation | Parser accepts bytes and returns a model; CLI reads only the supplied document; no driver/state/process APIs |
| CLI | `internal/cli/TestValidate`: success, invalid document, missing file, usage |
| Malformed input | `TestMalformedAndSecretSafe`, `FuzzParse`, deterministic sanitized diagnostics test |

State compatibility, identity hashing/canonical byte generation (API-05/07/09),
repository preparation/symlink checks, executable capabilities, runtime/state
ownership and recovery (API-06/08/10) require later milestones. Passing input
validation does not authorize deployment or establish deployment readiness.

Go tests were written before their implementation, but their red/green cycle
could not execute during initial implementation because no Go toolchain was available. The observed pre-code
`go test ./...` failure was an environment failure, not evidence of a failing
behavioral test. New Core fixtures did expose seven actual failures in the old
Python loader; those checks pass after the loader amendment.

## Resumed verification

The maintainer-installed `/usr/local/go/bin/go` reports Go 1.27.3 linux/amd64.
Codex's PATH omits `/usr/local/go/bin`, so checks used absolute executable paths.
The first Go test run reproduced rejection of three valid standard-tag fixtures
and a scalar-resolution unit case. yaml.v3 exposes short tags such as `!!str`;
normalizing through `Node.LongTag()` fixes the standard/custom-tag distinction.
The existing failing fixtures and scalar test now pass, including the unchanged
custom-tag and invalid-tag-value rejection fixtures.

All seven project Go files were formatted. `go test ./...`, `go vet ./...`,
`python3 tests/contract/check.py` (121 checks), and `git diff --check` passed.
The validation CLI was built and invoked independently for every manifest entry;
Python and Go both matched all 105 expected outcomes (17 valid, 88 invalid), with
zero mismatches. This checks acceptance parity, not identical error messages.
The normal Go test run executes fuzz seeds; extended fuzzing was not performed.
