# 001 — Application contract

## Purpose

Define the public, declarative Application document shared by all v0.1 components.

## Scope

YAML input, validation, defaults, configuration identity, and the relationship between application identity, source, runtime, health, and reconciliation policy. These specification revisions require maintainer approval before implementation.

## Requirements

Every Application document MUST start with the following two entries, in this order:

```yaml
apiVersion: pulltide.io/v1alpha1
kind: Application
```

The following is a declarative contract example, not implementation code:

```yaml
apiVersion: pulltide.io/v1alpha1
kind: Application
metadata:
  name: example
spec:
  source:
    type: git
    url: https://example.com/team/application.git
    ref: refs/heads/main
    path: .
  reconcile:
    intervalSeconds: 30
    operationTimeoutSeconds: 300
  runtime:
    type: exec
    exec:
      apply: ["./deploy.sh"]
      stop: ["./stop.sh"]
  health:
    timeoutSeconds: 60
    intervalSeconds: 5
    checks:
      - type: http
        url: http://127.0.0.1:8080/health
        timeoutSeconds: 2
      - type: command
        command: ["./check.sh"]
        timeoutSeconds: 5
  rollback:
    enabled: true
```

### Validation and defaults

Input MUST contain exactly one YAML document with a mapping root. Mapping keys MUST be strings. Unknown fields at every level, duplicate keys, explicit null values, aliases, merge keys, custom tags, and scalar coercion MUST be rejected. Strings, booleans, integers, mappings, and arrays MUST have their declared types; quoted numbers are strings, not durations. All strings MUST be valid Unicode without NUL. Only `env` accepts arbitrary mapping keys, subject to its constraints below.

### YAML scalar resolution

v0.1 MUST use YAML 1.2 Core Schema scalar resolution, independently of the destination field type. Plain `true`, `True`, `TRUE`, `false`, `False`, and `FALSE` resolve as booleans; `yes`, `no`, `on`, `off`, and date-like values resolve as strings. Decimal integer syntax is `[-+]?[0-9]+` (leading zeros remain decimal), octal is `0o[0-7]+`, and hexadecimal is `0x[0-9a-fA-F]+`. Legacy sexagesimal numbers and implicit timestamp typing are not supported. Core null spellings, including empty scalars, resolve as null and are rejected by this contract.

Core float spellings, including integral `5.0` and exponent notation, remain float-typed and MUST NOT satisfy integer fields. Non-finite numbers MUST be rejected. Quoted and block scalars resolve as strings unless an explicit permitted standard tag selects another type. Permitted standard tags are `!!str`, `!!bool`, `!!int`, `!!float`, `!!null`, `!!map`, and `!!seq`; their node kinds and scalar spellings MUST match their YAML 1.2 Core definitions. For example, `!!int "0o10"` is integer 8, `!!str true` is a string, and `!!bool yes` or `!!int 1.5` MUST fail. Explicit float tags may use decimal integer spellings as float values; octal and hexadecimal spellings are not float representations. Unsupported standard tags, custom tags, aliases, and merge keys remain prohibited; unused anchors remain permitted. A quoted or explicitly string-tagged `"<<"` mapping key is an ordinary string key, subject to the field's allowed keys.

Implementations MUST NOT resolve scalars based on the expected field, coerce strings to booleans/numbers, convert floats to integers, or stringify non-string values. Numeric spelling is resolved to its value before bounds/default/identity rules apply. Python and Go validators MUST agree on acceptance of the shared fixture corpus.

| Field | Required type and constraints | Omission behavior |
| --- | --- | --- |
| `apiVersion` | String, exactly `pulltide.io/v1alpha1` | Reject |
| `kind` | String, exactly `Application` | Reject |
| `metadata` | Mapping containing only `name` | Reject |
| `metadata.name` | String matching `[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?` in its entirety | Reject |
| `spec` | Mapping of the fields below | Reject |
| `spec.source` | Mapping of `type`, `url`, `ref`, `path` | Reject |
| `spec.source.type` | String, exactly `git` | Reject |
| `spec.source.url` | Nonempty credential-free repository location; supported forms and identity defined in [003](003-source-git.md) | Reject |
| `spec.source.ref` | Nonempty string: fully qualified valid `refs/heads/...` or `refs/tags/...`, or full 40/64 hexadecimal commit ID | Reject |
| `spec.source.path` | Nonempty relative path within the materialized tree | `.` |
| `spec.reconcile` | Mapping of the two fields below | Empty mapping, then child defaults |
| `spec.reconcile.intervalSeconds` | Integer from 1 through 86400 | 30 |
| `spec.reconcile.operationTimeoutSeconds` | Integer from 1 through 86400 | 300 |
| `spec.runtime` | Mapping of `type` and its matching driver block | Reject |
| `spec.runtime.type` | String, exactly `exec` or `docker-compose` | Reject |
| `spec.runtime.exec` | Mapping of `apply`, `stop`, `env`; required only for `exec`, forbidden otherwise | Reject for `exec` |
| `spec.runtime.exec.apply` | Nonempty array of strings; first element nonempty executable name | Reject |
| `spec.runtime.exec.stop` | Nonempty array of strings; first element nonempty executable name | Reject |
| `spec.runtime.exec.env` | String-to-string mapping; keys match `[A-Za-z_][A-Za-z0-9_]*` in their entirety | Empty mapping |
| `spec.runtime.docker-compose` | Mapping containing only `files`; required only for `docker-compose`, forbidden otherwise | Reject for `docker-compose` |
| `spec.runtime.docker-compose.files` | Nonempty array of nonempty relative path strings; declaration order is significant | Reject |
| `spec.health` | Mapping of `timeoutSeconds`, `intervalSeconds`, `checks` | Reject |
| `spec.health.timeoutSeconds` | Integer from 1 through 86400 | 60 |
| `spec.health.intervalSeconds` | Integer from 1 through 86400 | 5 |
| `spec.health.checks` | Nonempty array of check mappings | Reject |
| Each check's `type` | String, exactly `http` or `command` | Reject |
| Each HTTP check's `url` | Absolute HTTP(S) URL with nonempty host, no user information or fragment; forbidden on command checks | Reject for HTTP |
| Each command check's `command` | Nonempty string array with nonempty executable name; forbidden on HTTP checks | Reject for command |
| Each check's `timeoutSeconds` | Integer from 1 through 86400, no greater than total health timeout | 5 |
| `spec.rollback` | Mapping containing only `enabled` | Empty mapping, then child default |
| `spec.rollback.enabled` | Boolean | `true` |

Empty strings after the executable argument are permitted. Commands execute without an implicit shell. Absolute paths and `..` components in configured source/Compose paths MUST be rejected. Source paths resolve relative to the materialized repository root; Compose file paths resolve relative to the selected source directory. After symlink resolution all MUST stay inside the materialized tree. Directory/file existence and readability checks occur during preparation, before runtime mutation. Command arguments are not treated as source file paths; arbitrary operator commands remain operator responsibility.

The operation timeout bounds each external source/preparation action (including each Compose image build/pull/import/export), apply, stop, and restoration action; termination behavior is defined in [004](004-runtime-exec.md) and [005](005-runtime-compose.md). Health deadline behavior is defined in [006](006-health.md). All recovery actions use the failed attempt's frozen timeout, even when operating on prior/last-good instances with different historical settings. Startup command checks instead use the saved observation health window/check timeouts.

Compose `build` remains valid in repository Compose files; it requires no additional Application field. The supported build profile and retained image artifacts are defined in [005](005-runtime-compose.md). Local sibling build/context references follow driver path rules, not the stricter top-level Application path grammar. Those definitions and all recursively referenced inputs must be confined/validated before invoking the builder. External application data are allowed only through the explicit driver exception, never as build or definition inputs.

### Identity and compatibility

`metadata.name` is immutable within the existing state identity. Host ownership is defined in [007](007-state.md). Renaming creates a separate identity and MUST NOT adopt old resources implicitly. Changing runtime type against existing state MUST be rejected. v0.1 loads fixed configuration at startup; changes require restart. Recovery of pending work uses saved configuration and policy as defined in [007](007-state.md), before newly supplied configuration can deploy.

Configuration identity is `identity-v1:` followed by the lowercase SHA-256 hex digest of canonical normalized `spec` bytes. Normalization inserts all defaults, including omitted optional mappings. It does not normalize URL spelling, ref spelling, path spelling, command text, or Unicode. Canonical JSON uses UTF-8 without BOM or trailing newline, no insignificant whitespace, mapping keys sorted recursively by Unicode code point, arrays in original order, integer decimal notation without leading zeros, and literal `true`/`false`. Strings escape quotation marks and backslashes with a backslash; control characters U+0001–U+001F use lowercase `\u00xx`; all other characters are literal UTF-8. NUL is already invalid. This serialization is an identity definition, not a requirement to store user documents as JSON.

All normalized `spec` fields participate; `metadata` does not. Equal canonical bytes MUST produce equal identity across restart and software upgrade. An implementation MUST preserve `identity-v1` comparison for existing records; an unsupported identity version MUST block mutation, not silently produce a new target. Equivalent default omission or mapping order MUST NOT trigger deployment. Different significant array order, explicit values, or string spelling MUST produce a distinct target even at the same source revision. A target combines configuration identity with the source-neutral revision tuple from [003](003-source-git.md).

An instanceId identifies an execution workspace/artifact binding independently of target identity. Restoring the same target creates a fresh instance without changing this configuration identity. Saved successful deployment inputs govern restoration; new commands, health checks, Compose files, or policy MUST NOT rewrite a pending attempt or historical record.

## Non-goals

Kubernetes API registration, CRDs, templating, configuration hot reload, arbitrary extension fields, secret-reference APIs, and compatibility with legacy API groups. No new public API fields are introduced by this clarification.

## Failure behavior

Validation MUST finish before runtime or deployment-state mutation. Errors identify the invalid field without echoing credentials or secret values. Invalid documents preserve existing deployment state. Required files found unreadable after materialization are preparation failures and preserve the existing workload. An unsupported stored configuration identity version requires repair/compatible software before mutation.

## Acceptance criteria

| ID | Scenario and required observable result |
| --- | --- |
| API-01 | The example validates. Any other API version or kind, including the former API identifier, is rejected before mutation. |
| API-02 | Each driver block validates independently; both blocks, the wrong block, or missing selected block fail. HTTP/command checks reject each other's fields. |
| API-03 | For every field, fixtures exercise the declared type, missing required value, explicit null, and unknown member. Duplicate keys, aliases, multiple documents, non-string keys, quoted numeric durations, and NUL fail. |
| API-04 | Durations 1 and 86400 pass; 0, -1, 86401, fractional, boolean, and string values fail. A check timeout greater than health timeout fails after defaults. |
| API-05 | Omitting `reconcile`, `rollback`, and `exec.env` yields exactly the same identity as their fully explicit defaults. Reordering mappings preserves identity; reordering commands/checks/files changes identity. |
| API-06 | Empty executable names and non-string argv/files/env members fail; empty later argv strings pass. Absolute/traversing paths fail statically; missing files and escaping symlinks fail preparation with zero runtime mutations. |
| API-07 | Identical canonical normalized spec bytes yield identical identity before/after restart and upgrade. A changed explicit value at the same commit produces a different target; an unsupported identity version blocks mutation. |
| API-08 | Runtime-type changes with existing state fail. Pending restoration uses recorded inputs despite changed startup configuration, including changes to rollback policy. |
| API-09 | Equivalent URL/path spellings are not collapsed. Canonical JSON fixtures exercise Unicode key ordering, control-character escaping, UTF-8, arrays, and booleans so separate implementations produce identical bytes/digests. |
| API-10 | Compose string and mapping source-build definitions require no new Application fields; valid local builds prepare immutable artifacts. Unsupported build inputs fail preparation before service mutation. |
| API-11 | Shared fixtures verify YAML 1.2 Core booleans, quoted strings, leading-zero decimal/octal/hex integers, floats versus integers, dates, valid standard tags, invalid tagged values, and unchanged YAML prohibitions; no destination-field coercion occurs. |
