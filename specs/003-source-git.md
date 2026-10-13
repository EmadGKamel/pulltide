# 003 — Git source

## Purpose

Resolve desired revisions and provide reproducible, isolated source content for deployment and rollback.

## Scope

One Git repository and one configured branch, tag, or exact commit per Application. Git is the first source driver; core reconciliation handles driver results rather than Git operations.

## Requirements

### Driver boundary and revision identity

A source driver MUST provide resolution, immutable materialization, and validation of retained content. Resolution returns a revision tuple `(driver, sourceIdentity, revision)`. The core compares tuple values for equality and treats the last two components as opaque identifiers; it MUST NOT interpret commit ancestry, branch syntax, Git caches, or timestamps. Materialization returns a retained artifact reference and a selected source directory. Core/state ownership governs artifact retention and release according to [007](007-state.md); the source driver owns its private resolution cache and MUST NOT evict referenced retained artifacts.

For Git, `driver` is `git`; `sourceIdentity` is `sha256:` followed by lowercase SHA-256 hex of the exact credential-free `spec.source.url` UTF-8 bytes; `revision` is the full lowercase hexadecimal commit object ID (40 characters for SHA-1 repositories, 64 for SHA-256 repositories). Equal commits in differently identified repositories remain distinct revision tuples. No URL normalization or repository-mirror equivalence is inferred.

Supported repository locations are absolute HTTPS/SSH URLs and absolute `file://` URLs without query or fragment. HTTPS URLs MUST have no user information. SSH URLs MAY include a username, but MUST NOT include a password; the username is an identity/transport selector, not a stored credential. Operators MUST NOT embed HTTPS tokens/passwords in URL paths and MUST supply authentication through host Git credential/SSH facilities; arbitrary secret-looking path values cannot be identified by structural URL validation. SCP-style and relative filesystem locations are not supported. URL validation and error reporting MUST NOT echo rejected input or credential-helper output. Host authentication access is source-driver behavior and does not imply that runtime children inherit host environment.

### Resolution and retry

Branch and tag refs MUST use their fully qualified Git-valid `refs/heads/...` or `refs/tags/...` names. Each resolution MUST fetch fresh remote information within the configured operation timeout. A removed ref, inaccessible repository, failed authentication, timeout, or interrupted fetch is a resolution failure; a cached branch/tag value MUST NOT become desired. Lightweight and annotated tags MUST resolve recursively to a commit. Tags pointing to non-commit objects MUST fail.

An exact full commit object ID MAY resolve offline only when that exact object and all required tree/blob content have previously been retrieved and verified in the cache associated with the same source identity. A commit present only in another repository's cache is insufficient. Otherwise the driver MUST retrieve and verify the requested commit/content from the configured repository or fail. A remote may refuse unavailable or unadvertised commits; no fallback to a branch, shortened object ID, or different commit is allowed. Successful offline pinned resolution does not claim remote freshness. Uppercase hexadecimal input is accepted and returned as lowercase revision identity; configuration string spelling remains significant under [001](001-application-contract.md).

Revision comparison MUST use tuple identity, not timestamps, commit messages, or ancestry. Force-pushes and moved tags that change the resolved commit are changes. Resolution/preparation failures are retried at later polls and do not create failed-deployment suppression entries; suppression starts only when runtime mutation may have begun, as defined in [002](002-reconciliation.md).

### Materialization and durable retention

Each candidate MUST materialize exact tracked blob bytes and file modes from the resolved commit into an isolated immutable artifact. Regular files retain Git's executable-bit distinction. Symlinks retain their stored target text, but MUST be validated to resolve within the materialized tree; cyclic or dangling symlinks MUST fail preparation. No Git hooks, clean/smudge filters, line-ending conversion, local modifications, untracked files, or `.git` administrative content may enter the runtime workspace. Gitlinks outside the accessible selected subtree may be left unexpanded; other filesystem entry types unsupported by this contract MUST fail preparation.

The selected `spec.source.path` MUST exist as a directory inside the artifact. All paths exposed to runtime or health MUST remain inside the materialized repository tree after symlink resolution. Before runtime mutation, reject Git submodule entries (gitlinks) and Git LFS pointer files within the selected source subtree or reachable through its symlinks. An LFS pointer is a tracked text blob beginning with the standard `version https://git-lfs.github.com/spec/v1` line and containing `oid sha256:` and `size` records. Unsupported content outside that accessible subtree does not require expansion and is not a deployment input. No submodule or LFS download is performed.

Artifact completeness and content integrity MUST be verified before publication. Artifact contents and the containing directory entries MUST be durable before state/journal records reference them and before runtime mutation begins. Interrupted publication MUST leave either a complete verified artifact or unpublished temporary data; partial data is never deployment-eligible. A restart MUST verify referenced artifacts before using them and block mutation if they are missing or corrupt.

Runtime and command health checks use a separate mutable workspace copied from the immutable artifact, with working directory set to the selected source path. Runtime writes MUST NOT alter retained source. Preparation MUST NOT overwrite an active workspace or any referenced artifact. Restoration MUST create a fresh workspace from retained last-good content, rather than reuse potentially modified candidate files. Mutable workspace retention for cleanup of interrupted work is governed by [007](007-state.md).

Last-good and pending-operation artifacts MUST remain locally available independently of the remote, ref existence, and the driver's disposable fetch cache. The driver MUST NOT garbage-collect them before the core/state owner durably releases their references. Lost or corrupt required artifacts are recovery failures, never permission to substitute current branch content.

Network, Git, materialization, and validation operations MUST obey the configured operation timeout. Preparation failure MUST result in zero runtime mutation. Credentials, credential-helper output, and authentication environment MUST NOT be placed in deployment records, status, or diagnostics; diagnostics identify the failed operation using the source identity and safe error classification.

## Non-goals

Additional source drivers, submodule initialization, Git LFS downloading, repository write-back, commit signing policy, automatic builds, and remote freshness guarantees for verified cached exact commits. The source-neutral boundary does not add another driver to v0.1.

## Failure behavior

Authentication errors, missing refs/commits, non-commit tags, timeouts, invalid or unsupported content, invalid paths, and unavailable repositories MUST report a source/preparation failure and preserve the current deployment. Failed or interrupted fetches and materialization MUST NOT make partial work eligible. Later polls retry resolution/preparation. A pinned verified cached commit may succeed offline under the rules above. Rollback MUST use verified local retained content; a missing/corrupt required artifact blocks recovery without changing to a different revision.

## Acceptance criteria

| ID | Scenario and required observable result |
| --- | --- |
| GIT-01 | Branch update returns the new full commit tuple and byte-exact materialization. Different timestamps/messages at the same tuple do not create a new revision. |
| GIT-02 | Force-push, moved lightweight tag, and moved annotated tag detect changed commit identity without requiring ancestry. An annotated tag chain peels to its commit; a tag targeting a blob/tree fails with no runtime calls. |
| GIT-03 | Removing a previously fetched branch/tag or interrupting its next fetch yields a resolution failure, never cached/default-branch deployment. The next successful poll may deploy. |
| GIT-04 | Verified full pinned commits resolve offline from their source-scoped complete cache. An absent commit, incomplete cache, or same object in another source cache requires retrieval and fails if unavailable, with no fallback. Test both supported object-ID lengths. |
| GIT-05 | An unavailable repository, denied credentials, operation timeout, and transport interruption preserve active/last-good state and schedule later retry. Assert zero runtime mutations and no deployment suppression entry. |
| GIT-06 | Paths escaping the tree, missing source directories, escaping/dangling/cyclic symlinks, and unreadable required inputs fail preparation before stop/apply. Internal symlinks retain their target text. |
| GIT-07 | Custom filters, hooks, line-ending settings, modified cache files, and untracked files cannot change materialized tracked bytes. Executable modes survive; `.git` is absent from workspaces. Runtime writes leave artifact bytes unchanged. |
| GIT-08 | Accessible gitlinks and LFS pointer files fail before mutation; unsupported content outside the accessible selected subtree requires no expansion. |
| GIT-09 | Interrupt fetch/materialization/publication at each durability boundary. Only verified durable artifacts can be referenced by a journal; unpublished partial content is ignored after restart. Corrupt referenced content blocks recovery. |
| GIT-10 | Delete the remote ref, disconnect the remote, and remove the disposable fetch cache. Retained last-good content still restores into a fresh workspace with the original bytes. |
| GIT-11 | Credential-bearing URL rejection and injected authentication failures expose neither embedded secrets nor credential-helper output in diagnostics/status/state. Valid SSH username selection works without storing credentials. |
| GIT-12 | Distinct credential-free URL spellings produce distinct source identities; identical URL bytes and commit IDs produce identical tuples across restart. Core reconciliation can exercise a source-driver test double using opaque non-Git revision values without parsing them. |
