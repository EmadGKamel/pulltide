# Application schema coverage and semantic validation

The normative sources are [001 — Application contract](../specs/001-application-contract.md) and its referenced specifications. This document explains Milestone 01 coverage; it does not add contract rules. `application-v1alpha1.schema.json` is JSON Schema Draft 2020-12 for **Application input only**. The status interface in 002 is a separate contract and is not part of this schema.

## What the schema checks

- Exact API version/kind; required objects/fields; unknown-field rejection, including both discriminated runtime variants and each health-check variant. Git is the only source discriminator.
- Declared JSON types, null rejection, full-string Application/environment names, NUL rejection in declared string values, nonempty executable names/arrays/files, and duration bounds 1–86400.
- Top-level source/Compose paths must be nonempty relative Linux paths without a `..` component. There are no Windows path restrictions, shell interpretation, uniqueness constraints, or normalization rules added here. Only preparation can resolve symlinks or check existence.
- Fully qualified Git branch/tag syntax or a full 40/64-character hexadecimal ID. The reference-name pattern implements the Git-valid requirement in 003; it does not resolve refs or inspect repositories.
- Supported source URL scheme/authority shapes, prohibited query/fragment/userinfo forms, and HTTP-check scheme/authority shapes. The patterns are lexical guards, **not complete URL parsers**.

All `$ref` values are local. The schema neither downloads source nor reads Compose files. Build definitions belong in repository Compose files, not in Application fields. Their capability/security rules in 005 are preparation requirements, not new Application properties.

## Default annotations

Only defaults named in 001 appear. An omitted `reconcile` or `rollback` mapping has annotation `{}`; its child annotations describe the defaults to insert. Omitted `exec.env` has `{}`, `source.path` has `.`, reconciliation durations are 30/300, health window/interval are 60/5, check timeout is 5, and rollback enablement is true.

JSON Schema `default` is an annotation: validation does **not** insert values, create parent objects, or normalize documents. An explicit null never requests a default. The future Go normalizer must recursively expand the approved defaults before checking semantic constraints or computing configuration identity. The harness checks every annotation and verifies that validation leaves input unchanged. It does not implement production normalization/hashing.

## Rules outside portable JSON Schema

| Normative source / rule | Why the schema is insufficient | Milestone 01 coverage / future Go requirement |
| --- | --- | --- |
| 001: exactly one YAML mapping document; first entries apiVersion then kind | JSON instance validation sees neither a YAML stream nor normative mapping order | Harness checks stream count and first-entry order; schema checks root/required keys. Go must preserve this information before conversion. Comments/document-start markers do not create mapping entries. |
| 001: duplicate keys, aliases, merge keys, custom tags, non-string mapping keys | Parsing into JSON can erase the evidence or coerce keys | Harness checks YAML events/nodes before construction. Unused anchors and explicit standard string tags are not prohibited by the specs and have valid fixtures. Go must reject aliases/merges/custom tags without silently flattening them. |
| 001: declared YAML types without coercion, valid Unicode | JSON Schema cannot recover YAML scalar tags; JSON `integer` includes numbers such as 5.0 with zero fractional part | Harness rejects non-JSON YAML types and invalid Unicode scalar values, and separately rejects float-typed duration values even when integral. Go must retain/check declared scalar types, including string-only argv/env members and boolean-versus-integer distinction. Direct JSON consumers must also reject lone surrogate code units; this schema does not use a nonportable surrogate-range regex that could reject valid supplementary Unicode characters. |
| 001/006: each check timeout <= total health timeout after defaults | Draft 2020-12 has no standard instance-data comparison keyword; `$data` would be a nonstandard extension | Harness compares explicit/defaulted health window and check timeout without mutating input. Go must do the same after default expansion; e.g. overall 4 with omitted check timeout fails because default check timeout is 5. |
| 001/003: supported valid absolute URLs, no credentials or invalid host/authority syntax | Lexical schema patterns cannot substitute for complete URI/authority validation and credential policy | Go must parse complete URLs, reject malformed authorities/ports/escapes and forbidden credentials, and preserve exact input spelling for identity. A schema pass alone is insufficient. No URI `format` assertion or optional format dependency is implied. The harness tests the lexical constraints only. |
| 001: normalized configuration identity and meaningful order | Validation does not insert defaults, canonicalize, or hash | Go must implement identity-v1 exactly as specified, keep array order, and retain mapping-order independence. Harness verifies order/duplicates remain unchanged; canonical identity is not implemented in this milestone. |
| 001/007: immutable Application identity/runtime type against existing state | Requires prior state, ownership, and format/identity-version context | Future Go validation must compare authoritative state before mutation. Structural validity does not authorize adopting resources or changing runtime type. |
| 003/004/005: file existence, readability, executable availability, symlink confinement, complete referenced input closure, unsupported source content | Requires checked-out source and host/runtime capabilities | Future preparation must validate these before builder/service mutation. Fixture repository paths and commands need not exist; no command, Git fetch, Docker action, or filesystem preparation is executed by these checks. |
| 005/007/008: credentials, retained artifacts, capacity admission, quiescence and recovery | Requires private host/state/driver observations | Remains in the future runtime/reconciliation implementation, not this input schema. Schema-valid input is not deployment approval. |

## YAML scalar resolution boundary

The specifications do not select a YAML version or implicit scalar-resolution profile. This harness pins PyYAML and uses its safe scalar resolution; fixtures use unambiguous spellings or explicit standard tags. For version-dependent values (for example unquoted `yes`), that tool's behavior is **not a newly approved contract rule**. Before the production Go parser is implemented, the maintainer must settle its scalar-resolution compatibility against 001's no-coercion requirement. Quote string values to keep contract fixtures independent of that unresolved choice. No specification has been changed by this milestone.

## Reproducible checks and coverage

From the repository root run `python3 tests/contract/check.py` in an environment matching `tests/contract/requirements.lock`. It checks all exact direct/transitive dependency versions, validates the schema against the bundled Draft 2020-12 meta-schema, rejects remote schema references, disables network sockets during tests, and runs every fixture listed in `tests/fixtures/contract/cases.json`. No packages are installed during test execution.

The fixture manifest records expected `valid`, `yaml`, `schema`, or `semantic` outcomes; invalid documents must fail at the intended layer, not merely fail for any reason. YAML errors additionally identify the expected rejection category. Diagnostics omit raw document values and credential-bearing parser/validator messages. Valid means passing this milestone's structural and explicitly covered semantic checks; it does not certify URI semantics, preparation, state compatibility, or deployment readiness.

Additional tests cover required fields, null/wrong-type matrices, unknown fields at every defined object, runtime/check branches, defaults, boundaries, Git ref/path edge cases, meaningful array order, and exclusion of status fields. API-01–04 and the structural parts of API-05/06/09/10 have coverage here. Stateful behavior, canonical identity, and runtime build behavior in API-07/08/10 remain later milestones; no passing test is claimed for unimplemented behavior.

Reference semantics: [JSON Schema validation/defaults](https://json-schema.org/draft/2020-12/json-schema-validation), [validator API](https://python-jsonschema.readthedocs.io/en/v4.10.2/validate/), and [Git reference syntax](https://git-scm.com/docs/git-check-ref-format). The repository specifications remain the normative contract.
