# migration

Data migration framework for Passage (wave 10): the declarative mapping DSL, the extract-map-validate-load-reconcile pipeline behind interfaces with in-package fakes, idempotent re-run, dry run, exception queue with re-run, run ledger with lineage, and reconciliation (`internal/domain`); Postgres persistence of runs, ledger and the source-to-target id cross-reference (`internal/store`, schema `migration`); the HTTP API (`internal/api`); and the Helm chart with a connectors sub-chart. Legacy table specs live in `tools/connectors`. Everything unbuilt is listed in `NOT-IMPLEMENTED.md`.

Test: `go vet ./... && go test ./...` (`-short` skips the full-size 50k/20k/100k/10k load). Database tests need `PASSAGE_TEST_DATABASE_URL`, and the RLS test also `PASSAGE_TEST_APP_DATABASE_URL`.

## API

| Route | Duty | Result |
|---|---|---|
| `POST /v1/integration/migration-runs` | `migration.run` | 202 job envelope (`jobId`, `statusUrl`, `estimatedCompletionSeconds`) and the run; honours `Idempotency-Key` |
| `GET /v1/integration/migration-runs/{id}` | `migration.run` | counts, rejects, absent keys, reconciliation |
| `POST /v1/integration/migration-runs/{id}:rerun` | `migration.run` | new run linked by `parentRunId` that reprocesses only the exception queue |
| `GET /v1/integration/migration-runs` | `migration.run` | runs newest first; filters `entity`, `status`, `limit` |
| `POST /v1/integration/mapping-specs` | `migration.spec.write` | parses the mapping, requires at least one test case and runs the tests, stores an immutable (entity, version); same content returns 200, different content under a used version 409 `MIG-SPEC-VERSION-EXISTS` |
| `GET /v1/integration/mapping-specs`, `GET .../{id}` | `migration.run` | published versions; the single read includes the mapping |
| `POST /v1/integration/config-packages:diff` | `migration.run` | verifies a signed package and reports per file, in dependency order, the records apply would create or update; writes nothing |
| `POST /v1/integration/config-packages:apply` | `migration.package.apply` | verifies, writes created and updated records in dependency order, never deletes, appends an `applied` entry; `Idempotency-Key` is required and a replay returns the original answer; `version` is required and at most 20 characters (400 otherwise) |
| `GET /v1/integration/config-packages`, `GET .../{id}` | `migration.run` | applied packages newest first; the single read includes the manifest files |
| `POST /v1/integration/parallel-run-diffs` | `migration.run` | 501 `MIG-PARALLEL-RUN-DEFERRED` (wave 49) |

`POST /migration-runs` takes either an inline `mapping` or a `mappingSpecId` (an unknown id is 422 `MIG-SPEC-UNKNOWN`); an `Idempotency-Key` replays only for the same source, mode, entity, dry-run flag, mapping version and spec. Runs execute inside the request. Callers must be tenant-wide: a legal-entity-scoped caller gets 403. The deployment wires no extract or load backend yet, so `POST` returns 501 `MIG-BACKEND-NOT-CONFIGURED` until a later slice supplies the connectors and the batch-API loader.

Environment: `DATABASE_URL` (required), `MIGRATE_ON_START=true` applies `migrations/`, `HTTP_ADDR` (default `:8080`), `ADMIN_ADDR` (default `:9090`, serves `/metrics`).

## Mapping DSL

Mappings are JSON (a subset of the YAML the architecture names; no YAML library is on the allowed dependency list, so the stored text stays valid if a parser is approved later).

```json
{"entity":"customer","version":1,"fields":[
  {"source":"CustGroup","target":"segment","type":"string","enum":{"10":"retail"},"required":true},
  {"source":"CreditMax","target":"creditLimit","type":"decimal","default":"0"}]}
```

Types: `string`, `int`, `decimal`, `date`, `bool`, `timestamp` (RFC 3339, emitted in UTC). Absent, null and blank values are treated alike: default fill, else `required` rejects, else the field is omitted. Decimals never pass through float. Without `scale`, a decimal above scale 6 or at or above 1e14 is rejected, not rounded. Reject details carry the field name and code, never the value.

Rule kinds (`rule`, default `copy`):

- `const`: `value` is the target value and there is no `source`; the value must satisfy the type at parse time.
- `concat`: `sources` (at least two, type `string`) joined by `separator`. Each part is trimmed and blank parts are skipped; when every part is blank the field is absent.

Functions (`fn`), each with a fixed declared type:

- `ax_datetime` (`timestamp`): AX `utcdatetime` text (`2006-01-02 15:04:05` or `2006-01-02T15:04:05`) read in the mapping's `sourceTimezone` (IANA name, default `UTC`). `1900-01-01`, with or without `00:00:00`, is the AX null marker and is absent.
- `ax_noyes` (`bool`): `0`, `1`, `No` or `Yes`, case-insensitive.
- `sap_dats` (`date`, or `timestamp` when `timeSource` names a `HHMMSS` column read in `sourceTimezone`; a blank time is `000000`): `YYYYMMDD`. `00000000` and `99991231` are absent.
- `sap_lead_zero` (`string`, `width` 1 to 40 required): an all-digit value is left-padded with zeros to `width`; a digit value longer than `width` rejects; any other value passes through unchanged.

Further field options:

- `enumDefault`: target value for a present value with no `enum` entry; the row is loaded with a `MIG-MAP-ENUM-DEFAULT` warning. Without it the row rejects with `MIG-MAP-ENUM`.
- `scale` (0 to 6, decimals only): the value is rounded half-even (`RoundBank`) to `scale`. When rounding changes it, the row is loaded with a `MAP-PRECISION-LOSS` warning carrying the absolute delta. The magnitude check still applies after rounding.

Mapping options:

- `rejectWhen`: up to 50 conditions `{field, op, value | values, code}` evaluated on the mapped output once every field rule has succeeded (a row with a field reject gets only its field rejects). `field` must be a mapping target. `op` is `empty` (target absent or blank, no value), `eq`, `ne` (`value`), `in`, `notIn` (`values`, 1 to 200) or `match` (`value` is an RE2 pattern of at most 200 characters, compiled at parse). Comparisons use the string form of the mapped value (`true`, not `Yes`, after `ax_noyes`; the padded value after `sap_lead_zero`). `ne` and `notIn` also hit an absent target; `eq`, `in` and `match` do not. `code` is the reason shown in the exception queue: upper-case words joined by hyphens (`^[A-Z][A-Z0-9]*(-[A-Z0-9]+)+$`, at most 64 characters, for example `CUST-NO-GROUP`) and never starting with `MIG-`. Each matching condition adds `{field, code, "matched rejectWhen <op>"}`, in condition order, and `expectRejects` tests can assert the codes. An unknown op is `MIG-MAP-UNKNOWN-FN`; any other bad condition is `MIG-MAP-INVALID`.
- `sourceTimezone`: IANA zone for the `ax_datetime` and `sap_dats` time sources; an unknown name rejects with `MIG-MAP-INVALID`.
- `tests`: `[{name, input, expect | expectRejects}]`. `expect` is the exact mapped output as strings per target; `expectRejects` lists the reject codes. Each case has exactly one of the two; names are unique. `POST /v1/integration/migration-runs` runs the tests after parsing and before any backend call: a failing case answers 422 `MIG-MAP-TEST-FAIL` with each failed name in `errors`. A mapping without tests is accepted. `POST .../{id}:rerun` uses the stored mapping and does not re-run tests.

Warnings are reported per row in `warnings` and counted in `counts.warned` on the run view; warned rows are still loaded. Rejects remain the exception queue.

## Error codes

| Code | Meaning |
|---|---|
| MIG-MAP-INVALID | mapping document unknown field, wrong type, unknown type, duplicate target or bad default |
| MIG-MAP-REQUIRED | required source value absent |
| MIG-MAP-COERCE | value not convertible to the declared type |
| MIG-MAP-ENUM | value has no enum mapping |
| MIG-MAP-RANGE | decimal scale above 6 (or above `scale`) or magnitude at or above 1e14 |
| MIG-MAP-UNKNOWN-FN | unknown `rule`, `fn` or `rejectWhen` `op` in the mapping document (422) |
| MIG-MAP-TEST-FAIL | one or more mapping tests failed; the failed names are in `errors` (422) |
| MIG-MAP-ENUM-DEFAULT | warning: unmatched enum value mapped to `enumDefault`; the row is loaded |
| MAP-PRECISION-LOSS | warning: decimal rounded to `scale`; the detail carries the absolute delta; the row is loaded |
| MIG-ROW-DUPLICATE-KEY | source key repeated within one extract |
| MIG-LOAD-FAILED | target loader error for the batch |
| MIG-BACKEND-NOT-CONFIGURED | no extract and load backend wired (501) |
| MIG-PKG-SIGNATURE | package signature or a file sha256 does not match the manifest (422) |
| MIG-PKG-SCHEMA-NEWER | package schema is newer than the target supports (422) |
| MIG-PKG-INVALID | cyclic or unknown-file dependency, repeated file or record key, bad schema version (422) |
| MIG-NOT-CONFIGURED | no package target or signing key wired (501) |
| COPY-KEY-EXISTS | row rejected: rewritten key already exists in the target and was not created by this copy project |
| MIG-COPY-INVALID | copy into the same legal entity or one of another tenant, missing project, entity, legal entities or tenants, or no target reader to detect existing keys without `Upsert` |
| MIG-PARALLEL-RUN-DEFERRED | parallel-run diffs not built (501) |
| AUTH-0404 | unknown run, or a run of another tenant (404) |
| idempotency_key_reuse | `Idempotency-Key` replayed with a different request (422) |

## Configuration packages and legal-entity copy

A package is `{version, schemaVersion, manifest, files}`. Each file is `{name, entity, records: [{key, body}]}`; the manifest lists the files with `sha256` of each file's canonical JSON (sorted keys, numbers in shortest decimal form) and `dependsOn`. The signature is an HMAC-SHA256 (`domain.HMACSigner`; the tenant key from Vault is a handoff) over the canonical `{version, schemaVersion, files}` of the manifest, so a package cannot be relabelled as an older schema. `domain.Seal` fills the file hashes and `domain.Sign` signs.

Diff and apply (`domain.Diff`, `domain.Apply`) check the signature and every file hash, then the schema version, then apply files in dependency order. Records are compared by canonical content hash into `create`, `update` and `unchanged`; only create and update records are written, records present only in the target are never deleted, a second apply reports no changes, and a dry run returns the same diff and writes nothing. The deployment wires no package target or signing key, so both routes answer 501 `MIG-NOT-CONFIGURED` until a later slice supplies them (`api.API.WithPackages`). Applied packages are stored in `migration.config_package` with an append-only `applied` history (`tenant_id`, `applied_at`, idempotency key, `diff_counts`).

Legal-entity copy (`domain.CopyRun`) copies one entity through the migration pipeline: `domain.RewriteKeys` rewrites every legal-entity-scoped natural key (a field value starting with `from` gets `to` in its place, the first matching rule per field wins, `@key` names the record key) before hashing. The copy project id is the xref source system, so a second run over unchanged rows skips every row. A rewritten key that already exists in the target and was not created by the project is rejected `COPY-KEY-EXISTS` unless `Upsert` is set. Copying into the same legal entity, or into one of another tenant, is `MIG-COPY-INVALID`, and so is a copy wired without a target reader (existing keys could not be detected) unless `Upsert` is set. Rewrite rules persist in `migration.key_rewrite_rule`. The extractor, loader and reader are tagged with the source or target legal entity through `domain.WithLegalEntity`; wiring to real contexts is listed in `NOT-IMPLEMENTED.md`.

## Pipeline rules

- Idempotency key is `sha256(source_system|source_entity|source_key|content_hash)`, independent of the run.
- The target id is minted once and stored in the xref before the loader is called; the content hash is recorded only after a successful load, so a failed row retries with the same target id.
- Loads go through `TargetLoader` in batches of at most 1000; no direct SQL into target contexts.
- Source rows missing since the last run are reported in `Report.Absent`, never deleted.
- `Rerun` reprocesses only the keys in the previous report's exception queue and skips reconciliation.

## Dependencies

`github.com/shopspring/decimal v1.4.0` (exact money arithmetic), `pgregory.net/rapid v1.1.0` (property tests), `github.com/go-chi/chi/v5 v5.2.1` and `github.com/jackc/pgx/v5 v5.7.2` (router and Postgres, required by the platform kernel), all on the platform allow-list.

## Unverified

The column lists in `tools/connectors` (DirParty, CustTable, VendTable, InventTable, EcoResProduct, BOM, Route, KNA1, LFA1, MARA, MAST, STKO, PLKO) come from public data dictionaries and are flagged `Unverified`; they must be verified against coverage oracles that are not available in this repository. No mapping document for them ships.
