# 005 — Docker Compose runtime

## Purpose

Reconcile a source-defined Docker Compose project while keeping project identity stable across revisions.

## Scope

The public runtime discriminator `docker-compose`, backed by the host's Docker Compose v2 command interface (`docker compose`), using the local Linux Docker daemon.

## Requirements

### Identity and preparation

- Preparation MUST verify Docker/Compose availability, local daemon access, and validity of the complete configured input before runtime mutation. The only v0.1 endpoint is `unix:///var/run/docker.sock`; ambient Docker contexts and DOCKER_HOST MUST NOT select another endpoint. The daemon identity MUST be saved with runtime ownership and compared before every mutation. A mismatch or unavailable identity blocks mutation.
- The project name MUST be exactly `pulltide-<metadata.name>` and MUST remain identical across replacement, cleanup, and restoration. The canonical host installation and Application ownership rules in [state](007-state.md) apply. A preexisting project without matching durable ownership MUST be rejected, never adopted or torn down. Ownership MUST be reserved durably before creating resources; project name alone is not ownership evidence.
- Configured Compose files MUST be passed explicitly in declared order. CLI working directory and project directory MUST both be the retained workspace's configured source path. All relative paths MUST be resolved consistently from that project directory. Deployment-definition files and their resolved symlinks MUST remain within the materialized repository; missing or escaping definition inputs fail preparation.
- Compose invocation MUST use a fixed baseline environment of `PATH=/usr/bin:/bin` and `LANG=C` and an explicit local daemon endpoint. No ambient COMPOSE, DOCKER, proxy, HOME, or other parent variables may alter behavior. Interpolation MUST use only that baseline and the tracked `.env` at the configured source path when present; no parent-directory or implicit alternate environment file discovery is allowed. Absence of that file means an explicitly empty dotenv input. Saved source inputs determine the context during restoration.
- Preparation MUST resolve and validate service `env_file`, `include`/`extends`, and file-backed configs/secrets within the materialized repository. These deployment-definition inputs MUST be tracked, retained, and readable; missing inputs, remote inputs, and escaping paths or symlinks fail preparation. Build definitions are unsupported in v0.1; each service image MUST use an immutable digest reference. Image pulls are permitted. Explicit `container_name`, externally shared networks/volumes, and explicit network/volume `name` overrides are unsupported because they bypass project identity; project-scoped named volumes and networks are permitted. Bind mounts for application data may reference external operator-managed paths, checked for accessibility during preparation; their contents and named-volume data are not archived or restored by Pulltide. Such data paths MUST NOT be used to supply otherwise unsupported external deployment definitions.
- The immutable source snapshot MUST remain separate from the mutable workspace. Runtime file mounts use the workspace; cleanup retains the relevant workspace and saved configuration. Restoration uses a fresh workspace from last-good's snapshot. Operator workloads MUST tolerate that workspace replacement and use named volumes for data that must survive it.

### Runtime operations

- Apply MUST have the semantics of `docker compose up --detach --remove-orphans` with the explicit ordered files and stable project name. Ordinary replacement does not require prior teardown. Core reconciliation owns journaling, policy, and state promotion; the driver provides preparation, replacement, cleanup, restoration, observation, and quiescence outcomes.
- Each CLI operation MUST obey its saved operation timeout. Timeout termination uses the five-second graceful plus five-second forced-termination confirmation limits in [exec](004-runtime-exec.md). Exited CLI processes do not prove that daemon-side work stopped.
- Outcomes distinguish `completed-success`, `completed-failure`, and `completion-unknown`. Successful CLI completion establishes operation completion but requires Pulltide health verification before deployment success. A CLI error, daemon disconnection, timeout, or interrupted operation MUST be completion-unknown unless conclusive evidence establishes no outstanding daemon mutation. Current container state, no CLI PID, and passing health alone are insufficient evidence.
- Unknown completion MUST block cleanup, restoration, and all other mutation. v0.1 may establish quiescence after a changed Linux host boot identity, because this driver permits only the local daemon and no external deployment jobs. Same-boot Pulltide restart alone MUST NOT unblock recovery. Resource observation after reboot is still required; a reboot proves old mutation termination, not application health or absence.
- Restoration MUST reapply last-good files to the same owned project and remove services absent from that configuration. Last-good health checks MUST then pass.
- Cleanup MUST remove all owned project services and project-created networks across both prior and candidate configurations, including orphans left by partial updates. Active may be absent only after observation confirms no owned services remain and all mutations are quiescent. Named volumes MUST be preserved. Pulltide MUST NOT perform volume deletion, system-wide cleanup, or cleanup of unrelated resources.
- State MUST retain ordered file lists, source snapshot and workspace references, saved configuration, and daemon/project ownership needed for reapplication. Operators MUST retain needed images locally or ensure digest references remain fetchable for rollback. Unavailable images may prevent restoration; Pulltide does not archive images. External bind-mounted data remain operator-owned state outside reproducible rollback.

## Non-goals

Swarm, Kubernetes, remote daemons, remote-host provisioning, registry watching, image archival, image builds, mutable image tags, external shared resource management, volume snapshots, and database migration reversal.

## Failure behavior

Invalid inputs and ownership/context mismatches fail before runtime mutation. Apply failures preserve last-good. Confirmed-complete failures invoke shared rollback policy; uncertain completion blocks recovery until quiescence is established. Failed restoration or health verification reports recovery-required, never a healthy prior project. A missing image during restoration is a recovery failure, even when source inputs remain available.

## Acceptance criteria

- **COMPOSE-01:** Two successful revisions and restoration use exactly one project named `pulltide-<metadata.name>`. A preexisting unowned project is rejected without mutation.
- **COMPOSE-02:** Missing files, escaping symlinks, untracked referenced inputs, unsupported external inputs, and invalid merged configuration produce no mutation. Declared file order determines override order.
- **COMPOSE-03:** Changing parent DOCKER_HOST, Docker context, COMPOSE_FILE, COMPOSE_PROJECT_NAME, HOME, or interpolation variables does not change effective saved invocation inputs. Changing the local daemon identity blocks mutation.
- **COMPOSE-04:** A failed update removing an old service and adding a new one cleans up every owned service when cleanup policy applies, preserves named volumes and an unrelated project, and reports absent only after observation confirms removal.
- **COMPOSE-05:** With confirmed quiescence, unhealthy B restores A's saved files, removes B-only services, and passes A's checks before reporting rolled-back.
- **COMPOSE-06:** A daemon mutation delayed beyond CLI exit/timeout prevents recovery mutation. Same-boot restart stays blocked; a changed host boot identity permits journal recovery after ownership and resource observation checks.
- **COMPOSE-07:** Restoring while Git is offline uses retained inputs. An unavailable required image instead produces recovery-required without advancing last-good.
