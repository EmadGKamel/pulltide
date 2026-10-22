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
