# Pulltide Development Instructions

Pulltide is a pull-based application reconciler developed using spec-driven development. These instructions apply throughout the repository.

## Core Rules

- Specifications in `./specs` are the source of truth for observable behavior.
- Do not introduce behavior that is not defined in the specifications.
- Do not implement features before their specifications are reviewed and approved by the maintainer.
- Identify contradictions, ambiguities, and missing requirements before coding. Present unresolved architectural decisions to the maintainer rather than silently choosing behavior.
- Keep changes within the requested scope. A review request does not authorize implementation or specification edits.
- Public API compatibility matters, including during the alpha phase.

## Public API

The current application contract is:

```yaml
apiVersion: pulltide.io/v1alpha1
kind: Application
```

- Preserve this API group, version, and kind across specifications, schemas, examples, documentation, tests, and implementation.
- Do not rename or advance the API version without an explicitly approved specification change.
- If legacy API identifiers remain, report the inconsistency and change them only within the authorized task scope.

## Development Workflow

For behavioral changes:

1. Read the relevant specifications and inspect the existing implementation and tests.
2. Identify ambiguities, contradictions, missing failure cases, and compatibility implications.
3. Update the specifications first when authorized. Resolve open design decisions with the maintainer.
4. Add or update measurable acceptance criteria and obtain specification approval before implementation.
5. Write or update tests for those criteria, including relevant failure and recovery behavior.
6. Implement the minimum code required to satisfy the approved specifications and tests.
7. Run the relevant checks and review the implementation against the specifications.
8. Report what changed, verification results, remaining risks, and any unresolved decisions.

For documentation-only changes, use appropriate document and consistency checks. Do not add implementation tests that do not validate behavior.

## Specification Review

During a specification-only review, do not write implementation code or modify specifications unless the task explicitly authorizes those edits.

Review these areas independently where useful:

1. Product scope and architecture consistency.
2. Application API contract.
3. Reconciliation semantics and idempotency.
4. Git source behavior and failure scenarios.
5. Runtime driver contracts.
6. Health checking and rollback behavior.
7. State persistence and crash recovery.
8. Testability and acceptance criteria.

Look for contradictions, ambiguous behavior, undefined states, missing failure cases, race conditions, unsafe deployment behavior, untestable requirements, hidden Docker Compose coupling, barriers to future drivers, unclear state ownership, rollback edge cases, and behavior after process restart or host reboot.

Group findings under `CRITICAL`, `MAJOR`, `MINOR`, and `OPEN DESIGN DECISIONS`. For every finding, include the affected specification and section, the problem, why it matters, and a recommended resolution. Separate established defects from decisions that require maintainer input.

## Subagent Collaboration

- When independent work can be parallelized, use subagents where they improve review quality or save time, subject to the available tools and task instructions.
- Suitable tasks include specification review, API contract validation, test planning, and implementation review.
- Give each subagent a bounded task, relevant specifications, constraints, expected output, and explicit file ownership if edits are authorized.
- Use read-only subagents for reviews. Do not let reviewers modify specifications or implementation without authorization.
- Avoid concurrent edits to the same files. Split implementation work only when ownership and integration boundaries are clear.
- Subagents must follow these instructions and the same approved specifications. Delegation does not authorize new scope or architectural decisions.
- The coordinating agent must reconcile conflicting findings, verify conclusions, integrate authorized changes, and run the relevant checks. It remains responsible for the final result.
- If subagents are unavailable, perform the review sequentially and state that limitation without claiming independent reviews occurred.

## Git Rules

- Inspect `git status` before making changes and again before committing.
- Do not overwrite, discard, or commit unrelated user changes.
- If the working directory is not a Git repository, report that and clarify the intended repository before initializing one.
- Review the diff and stage only files belonging to the intended change.
- Keep commits small and logically scoped; prefer one conceptual change per commit.
- Show the planned commit message before committing significant changes.
- Commit when the task or established workflow authorizes it. Do not treat a review-only request as permission to commit changes.
- Do not amend, rebase, reset, or force-push unless explicitly instructed.
- Do not push, merge, publish a release, or change remote configuration without authorization.
- Never commit secrets, credentials, private keys, tokens, or environment-specific sensitive data. Use safe placeholders in examples.
- Run relevant checks before committing and disclose any failures or unavailable checks.
- Report created commits and any remaining uncommitted changes accurately.

## Architecture

Pulltide must remain:

- Pull-based.
- Runtime-agnostic.
- Source-agnostic.
- Usable without a central control plane.
- Deployable as a single Go binary.

Keep core reconciliation independent of Docker Compose and Git-specific implementation details. Git is the first source driver, not the definition of the source abstraction. Runtime behavior belongs behind runtime driver contracts. Preserve the ability to add future drivers without implementing them ahead of approved scope.

## v0.1 Scope

The established scope is:

- Git source and revision detection.
- Reconciliation loop.
- `exec` and Docker Compose runtime drivers.
- Health checking.
- State persistence and last good revision tracking.
- Rollback.

The following are outside v0.1 unless an approved specification change adds them:

- UI or central control plane.
- RBAC.
- Multi-node orchestration.
- Kubernetes runtime driver.
- Notifications.
- Secrets manager.
- Deployment approval gates as a product feature.

Repository specification approval is a development workflow requirement, not a deployment approval-gate feature. Exact behavior and acceptance criteria must come from the approved specifications; this file does not substitute for them.

## Communication

- Explain the outcome and next practical step in plain language; the maintainer is learning AI-assisted development.
- State when a decision needs maintainer input and why. Continue independent authorized work while that decision is pending.
- Explain when specification review, tests, Git commits, or subagents are useful for the current task.
- Do not claim checks passed, code was reviewed, or commits were created without verifying those actions.
