# 006 — Health checks

## Purpose

Determine whether an applied deployment is eligible to become last-good and whether a previously successful deployment is currently healthy after startup.

## Scope

HTTP and command checks during candidate verification, restoration verification, and startup observation.

## Requirements

- Candidate and restoration verification MUST begin only after runtime apply completed successfully and MUST use the saved deployment configuration being verified. Startup observation checks an already recorded active deployment without applying it again, after pending journal recovery takes precedence.
- Every configured check MUST pass in one verification round. Checks MUST execute sequentially in declaration order. A failed check ends its round immediately; remaining checks in that round MUST NOT run. Retry delay starts when the failing check has completed, including confirmed command termination. The next round restarts with the first check after the configured health interval, while time remains.
- A monotonic overall deadline starts when verification begins. Each check uses the lesser of its configured timeout and remaining overall time. No check or retry starts at or after the deadline. A round passes only if its last check succeeds before the deadline. Retry waits MUST be clipped to that deadline.
- HTTP checks MUST issue GET requests from the Pulltide host. Only 200–299 responses pass. Redirects MUST NOT be followed; other statuses, connection failures, TLS validation errors, and timeouts fail. TLS verification MUST remain enabled. Ambient HTTP proxy variables MUST NOT alter requests. Response bodies MUST NOT be captured or logged.
- Command checks MUST execute argument arrays without an implicit shell in the retained deployment workspace's source path. Exec checks use the saved exec environment; Compose checks use only `PATH=/usr/bin:/bin` and `LANG=C`, not the Compose application's dotenv/service environment. Compose command checks run on the host, not automatically inside a container.
- Startup windows MUST use a durable observation journal and per-check intents/results from 007. Interrupted startup checks require quiescence before a fresh saved-health window, without candidate suppression, stop, apply, or rollback. A new process never trusts an uncommitted prior observation as current health.
- Checks MUST be observational and MUST NOT schedule deployment mutations or leave background check processes. Command exit zero passes; nonzero exit or launch failure fails. Timeout fails the check and MUST terminate its process group using at most five seconds of graceful termination followed by at most five seconds of forced-termination confirmation. Unconfirmed termination blocks further checks and runtime mutation as completion-unknown.
- The overall verification window may be exceeded only by this at-most-ten-second termination allowance for an already running command. If termination remains unconfirmed after that allowance, report recovery-required; do not wait indefinitely or begin rollback concurrently.
- At least one check is required. A passing round makes the deployment eligible for durable success recording; it MUST NOT bypass state persistence requirements or prove absence of an older unfinished runtime operation.
- Startup observation MUST distinguish historical last-good from current health. While observation is unfinished, current health is unknown. A conclusively failed startup window records unhealthy; unfinished/uncertain checks or unavailable context record unknown. Both report recovery-required, preserve historical last-good, and block normal deployment. It MUST NOT trigger automatic drift repair or apply. Restart after operator repair reruns observation; passing observation releases this block without reapply.
- Diagnostics MUST identify check index/type and failure category without URLs containing credentials, response bodies, command arguments, environment values, or raw output. Command output follows the 64-KiB-per-stream transient capture-and-drain limit and no-raw-output rules in [exec](004-runtime-exec.md).

Exec command checks use 004's restricted foreground process profile and the already recorded recovery inventory. They do not create/rewrite recovery declarations. Unavailable/unsafe execution context or uncertain termination yields completion-unknown. This process runner does not implement HTTP policy or replace the ordered health-window orchestration in 002.

## Non-goals

TCP, gRPC, custom response-body assertions, authentication configuration, continuous monitoring, automatic drift repair, and proving that a shared endpoint belongs to a particular revision. Operators are responsible for targeting the intended deployment and writing observational checks.

## Failure behavior

A transient failed round retries within the deadline only after its command has conclusively terminated. Exhausting the deadline during candidate verification invokes rollback policy when prior mutation is quiescent. Failed restoration verification leaves recovery-required and preserves last-good. Failed startup observation leaves the existing runtime untouched and blocks new deployment until a later restart observes health successfully. Unconfirmed command termination prevents concurrent recovery work.

## Acceptance criteria

- **HEALTH-01:** HTTP 204 passes; HTTP 302, HTTP 500, invalid TLS, and connection timeout fail. Redirect targets receive no request, and a parent proxy variable does not redirect a check.
- **HEALTH-02:** Command exit zero passes; nonzero exit or timeout fails. A check observes exactly the saved working directory and documented environment after restart.
- **HEALTH-03:** For checks A, B, C where B first fails then passes, a recorded invocation trace is A, B, delay, A, B, C. C never runs in the failed round; the full successful round is required.
- **HEALTH-04:** A controlled monotonic clock shows retry delay measured from failure completion, clipped at the overall deadline, and no check starting at or after the deadline. Success exactly at the deadline is failure.
- **HEALTH-05:** A command running at the deadline receives graceful then forced termination within the ten-second allowance. Unconfirmed termination reports recovery-required with no rollback or additional check invocation.
- **HEALTH-06:** Restoration checks A's saved health configuration when candidate B changed it; success still requires a durable state write.
- **HEALTH-07:** After healthy deployment and restart, surviving healthy workload passes observation with no apply. Missing workload after host reboot fails observation, leaves historical last-good intact, and blocks candidate deployment. After operator repair and restart, health passes and polling resumes without reapply.
- **HEALTH-08:** Fake secrets emitted in command output or HTTP bodies never appear in status/logs; command capture remains within the documented per-stream limit while output is drained.
- **HEALTH-09:** Kill Pulltide during startup command checking or leave termination unconfirmed: durable observation evidence blocks same-boot competing checks. After quiescence, one fresh window runs without apply/rollback or suppression.
- **HEALTH-10:** Conclusive startup failure emits unhealthy with an observation time; unavailable context or unconfirmed completion emits unknown with null observation time.
