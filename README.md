# Pulltide

Pulltide is a pull-based application reconciler that brings GitOps-style deployment to runtimes beyond Kubernetes. Observable behavior is specified in [specs](specs/000-product.md).

## Application contract checks (Milestone 01)

The [Application schema](schemas/application-v1alpha1.schema.json) uses JSON Schema Draft 2020-12 and `apiVersion: pulltide.io/v1alpha1`. See [coverage and required semantic validation](schemas/semantic-validation.md); schema validation alone does not establish deployment readiness. Status is a separate contract.

Use Python 3.10 or newer (reference run: CPython 3.12.3), with the exact direct/transitive versions in `tests/contract/requirements.lock`. One-time setup on a Python installation with `venv` and `pip` available:

```sh
python3 -m venv .venv-contract
.venv-contract/bin/python -m pip install -r tests/contract/requirements.lock
.venv-contract/bin/python tests/contract/check.py
```

Setup may need network access. Test execution is offline: it uses bundled schema metadata/local references, installs nothing, and disables network sockets. For offline setup, obtain the pinned wheels beforehand, then install with `--no-index --find-links=/path/to/wheels -r tests/contract/requirements.lock`.

If the exact pinned packages are already installed, run directly:

```sh
python3 tests/contract/check.py
```

The command runs the valid/invalid YAML fixtures and contract matrices, and exits nonzero on unexpected acceptance, rejection, schema errors, or dependency-version mismatch. No Git, Docker, application command, runtime deployment, or production Go validator is invoked.

## Go Application parser (Milestone 02)

The validation-only CLI reads one Application document, validates its structure
and applicable input semantics, and applies the documented defaults to a separate
typed model. It does not fetch Git, inspect referenced files, run commands, access
Docker, or modify deployment state. YAML scalars use the YAML 1.2 Core policy in
specification 001. Diagnostics contain fixed explanations and trusted field paths,
not document values or parser excerpts.

A Go toolchain supporting the module's Go 1.22 language version is required. If Go
is missing, obtain maintainer approval for a separate setup step, then install a
Go toolchain using the [official installation instructions](https://go.dev/doc/install).
Do not treat installation as part of validation. With the approved toolchain
installed and its `bin` directory on PATH, one-time dependency setup is:

```sh
go version
GOTOOLCHAIN=local go mod download
```

Dependencies are pinned in `go.mod` and verified by `go.sum`. Setup may require
network access; retain the populated module cache for offline execution. Run:

```sh
gofmt -w cmd/pulltide internal/application internal/cli
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test ./...
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go vet ./...
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go run ./cmd/pulltide validate tests/fixtures/contract/valid/exec-minimal.yaml
python3 tests/contract/check.py
```

The CLI exits 0 for valid input, 1 for invalid/unreadable input, and 2 for usage
errors. Tests consume the existing fixture manifest plus parser/CLI unit cases;
see [acceptance coverage and remaining boundaries](tests/contract/m02-coverage.md).
M02 was verified with `/usr/local/go/bin/go version` reporting Go 1.27.3 on
Linux/amd64: formatting, `go test ./...`, `go vet ./...`, and the Python checks
passed. Python and the built Go CLI matched all 105 expected fixture outcomes
(17 valid, 88 invalid). If Codex cannot find the installed toolchain, use its
absolute paths or add `/usr/local/go/bin` to PATH for the current shell:
`export PATH="/usr/local/go/bin:$PATH"`.
