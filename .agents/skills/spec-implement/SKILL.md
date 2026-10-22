---
name: spec-implement
description: Implement an approved Pulltide specification or milestone using test-driven development. Use for implementation work, not specification-only reviews or unapproved feature design.
---

# Pulltide specification implementation

Paths below are relative to the repository root. This skill does not authorize deployments, commits, pushes, or work beyond the requested milestone.

## Establish the contract

- Read applicable `AGENTS.md` instructions, the relevant `specs/` documents, and their referenced requirements before editing. Specifications are normative; schemas, fixtures, and existing code are derived evidence.
- Inspect `git status`, the current branch, and relevant diffs. Preserve unrelated and uncommitted work; do not reset, stash, overwrite, or clean it away to simplify the task.
- Establish the approved scope from the request and existing conversation. If approval or required behavior is missing, or specifications conflict, identify the affected criteria and ask for the needed decision before implementing dependent behavior. Continue independent authorized work.
- Map each in-scope acceptance-criterion ID (or spec section) to an observable test case and its check command. Include failure, interruption, and recovery cases when the approved behavior requires them.

## Implement with red–green–refactor

1. Write or extend the smallest meaningful test for the next approved behavior **before implementing it**. Run it and verify that the failure demonstrates the missing behavior, not an unrelated setup error or broken assertion. Record that evidence.
2. Implement only enough approved behavior to pass. Run the focused test and confirm green before proceeding. Refactor only as needed while keeping the tests green. Do not break working behavior just to manufacture a red test.
3. Keep source/runtime details behind their driver boundaries. Preserve `apiVersion: pulltide.io/v1alpha1` and `kind: Application`; do not invent fields, defaults, or driver features. Do not silently change specifications, schemas, fixtures, or assertions to excuse a mismatch.
4. Never weaken safety requirements, including quiescence, health gating, durable state, retained rollback artifacts, or resource ownership. Surface a contract conflict rather than bypassing it.

## Verify and report

- Run checks appropriate to the changed layers and repository instructions. For Application-contract changes, follow `README.md` and the pinned tooling for `python3 tests/contract/check.py`; a schema pass does not replace the semantic requirements in `schemas/semantic-validation.md`.
- For Go work, use the existing module's focused tests and formatting, then required broader checks. Do not introduce a Go module, dependency, test framework, or runtime feature merely to satisfy this workflow.
- Inspect the final diff, run `git diff --check`, and inspect `git status`. Confirm changes stay within scope and unrelated work is preserved. Never commit or push without explicit user approval for that action.
- Report concisely: behavior changed, acceptance criteria covered, red/green evidence, exact check commands and results, and material limitations or remaining decisions. Distinguish expected TDD failures from unresolved failures and checks not run.
- For a failed or blocked check, explain the sanitized command, affected criterion/test, expected versus observed result, evidence for the cause, and what remains to fix. State whether it is caused by this change, pre-existing work, or the environment; label uncertainty instead of guessing. Never hide a failed check or expose credentials in diagnostics.
