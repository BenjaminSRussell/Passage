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
| `POST /v1/integration/data-templates` | `migration.spec.write` | stores an immutable (entity, name, version) of default values for mapping targets; same content returns 200, different content under a used version 409 `MIG-TEMPLATE-VERSION-EXISTS`; a bad shape is 422 `MIG-TEMPLATE-INVALID` |
| `GET /v1/integration/data-templates`, `GET .../{id}` | `migration.run` | published templates (filter `entity`, `limit`); the single read includes the defaults |
| `GET /v1/integration/migration-runs/{id}/ledger` | `migration.run` | per-row lineage in source key order; `templated=true` keeps only rows that took a template default (`defaultFromTemplate`) |
| `GET /v1/integration/config-packages`, `GET .../{id}` | `migration.run` | applied packages newest first; the single read includes the manifest files |
| `POST /v1/integration/projects` | `migration.project.write` | creates a `migrate`, `copy` or `config_package` project; a copy needs two different legal entity ids and no source system, a migrate project needs a source system (it may also carry `targetLegalEntityId` and `connectionProfileId`); `Idempotency-Key` replays; a used name is 409 `MIG-PROJECT-NAME-EXISTS` |
| `GET /v1/integration/projects`, `GET .../{id}` | `migration.run` | projects in name order (filters `projectType`, `status`, `limit`, keyset `nextCursor`) |
| `PATCH /v1/integration/projects/{id}` | `migration.project.write` | name, status, `cutoverAt`, `scheduleCron` under the required `version` (an omitted or null field is unchanged, an empty `cutoverAt` or `scheduleCron` clears it); a stale version is 409 `MIG-VERSION-CONFLICT`; an archived project takes no change but reactivation (409 `MIG-PROJECT-ARCHIVED`); type, source system and legal entities cannot change. `scheduleCron` is five numeric fields (ranges, lists, steps; no names or macros), at most 100 characters, at least 15 minutes between runs (FR-10.16) |
| `GET/PUT /v1/integration/projects/{id}/key-rewrite-rules` | `migration.run` / `migration.project.write` | the ordered rules of a copy project; `PUT` replaces the whole set under `version` (bumps it) and refuses a rule an earlier rule for the same field always wins over (its prefix starts with the earlier one, including a repeat); an archived project's rules are frozen (409 `MIG-PROJECT-ARCHIVED`); other project types answer 422 `MIG-PROJECT-NOT-COPY` |
| `POST /v1/integration/projects/{id}/key-rewrite-rules:preview` | `migration.run` | rewrites sample `keys` with the stored rules or the `rules` in the body; writes nothing |
| `POST /v1/integration/connection-profiles` | `migration.connection.write` | creates a connection profile (`kind` `sql`, `rest` or `file`, a `sourceSystem`, non-secret `config`); `Idempotency-Key` replays; a used name is 409 `MIG-CONNECTION-NAME-EXISTS` |
| `GET /v1/integration/connection-profiles`, `GET .../{id}` | `migration.run` | profiles in name order (filters `kind`, `status`, `limit`, keyset `nextCursor`); they show `secretSet` and `secretUpdatedAt`, never the secret |
| `PATCH /v1/integration/connection-profiles/{id}` | `migration.connection.write` | name, `status` (`active` or `disabled`) and the whole `config` under the required `version`; kind and source system cannot change; changing a setting the secret is bound to clears the secret |
| `PUT /v1/integration/connection-profiles/{id}/secret` | `migration.connection.write` | seals the profile's one secret with fieldcrypt and bumps the version; write-only; 501 `MIG-SECRETS-NOT-CONFIGURED` without keys |
| `POST /v1/integration/connection-profiles/{id}:test` | `migration.connection.write` | probes the connection through the egress guard and answers 200 with `ok`, `failure`, and what was found (see Connection profiles); needs the write duty because it opens the secret |
| `POST /v1/integration/parallel-run-diffs` | `migration.run` | 501 `MIG-PARALLEL-RUN-DEFERRED` (wave 49) |

`POST /migration-runs` takes either an inline `mapping` or a `mappingSpecId` (an unknown id is 422 `MIG-SPEC-UNKNOWN`); an `Idempotency-Key` replays only for the same source, mode, entity, dry-run flag, mapping version and spec. Runs execute inside the request. Callers must be tenant-wide: a legal-entity-scoped caller gets 403. The deployment wires no extract or load backend yet, so `POST` returns 501 `MIG-BACKEND-NOT-CONFIGURED` until a later slice supplies the connectors and the batch-API loader.

Environment: `DATABASE_URL` (required), `MIGRATE_ON_START=true` applies `migrations/`, `HTTP_ADDR` (default `:8080`), `ADMIN_ADDR` (default `:9090`, serves `/metrics`), `FIELDCRYPT_KEYS_FILE` (or `FIELDCRYPT_KEYS`) with `FIELDCRYPT_CURRENT_KEY_ID` for connection secrets, `MIG_EGRESS_ALLOW` for the egress allowlist and `MIG_DROP_ROOT` for file connections (all three below).

## Connection profiles

A connection profile (`migration.connection_profile`, `0008_connection_profile.sql`) says how a tenant reaches its existing ERP, so a custom system can be integrated without waiting for a connector. `config` holds non-secret settings only, checked per kind (`domain/connection.go`): `sql` takes `driver` (`postgres`, the pgx stdlib driver, is the only one linked), `host`, `port`, `database`, `user`, `sslmode`; `rest` takes `baseUrl` (https, or http without authentication; no credentials, query or fragment), `authType` (`none`, `basic`, `bearer`, `header`), `username`, `headerName`, `pagingStyle`, `probePath`; `file` takes `dropDir` (a clean relative path under `MIG_DROP_ROOT`), `format`, `delimiter`, `filePattern`; every kind takes `codepage` (an IANA character set) and `timezone`. A key that looks like a secret (password, token, key, authorization) or is not a setting of the kind is 400.

The one secret (database password, API token, key or basic-auth password) is set with `PUT .../{id}/secret`, sealed with `platform/go/fieldcrypt` bound to the tenant and the field `migration.connection_profile.secret:{id}`, and stored as `secret_sealed` with the key id that sealed it. Nothing returns it: reads show `secretSet` and `secretUpdatedAt`. It is opened only inside `:test` (and, in later slices, by the extractors), never logged (a test captures `slog` output), and scrubbed from probe messages. Changing a setting the secret is bound to (`sql`: `driver`, `host`, `port`, `database`, `user`, `sslmode`; `rest`: `baseUrl`, `authType`, `username`, `headerName`) clears the stored secret (`domain.SecretInvalidated`), so a writer cannot point a profile at another server and have `:test` hand it the existing password or token, nor reuse a token as a basic-auth password; the answer shows `secretSet: false`. A secret sealed under a key the service no longer holds is 422 `MIG-SECRET-UNREADABLE`: set it again. Without fieldcrypt keys the secret and test routes answer 501 `MIG-SECRETS-NOT-CONFIGURED`.

Custom source systems: `sourceSystem` may be `custom_<name>` (`custom_[a-z0-9_]{1,40}`) for runs, cross-reference rows, projects and profiles; `domain.ValidSourceSystem` is the single check and the database CHECKs on `run`, `xref`, `project` and `connection_profile` accept the same set. A migrate project may carry `targetLegalEntityId` and a `connectionProfileId` (an active profile of the same source system; 400 otherwise, 422 `MIG-CONNECTION-UNKNOWN` for an unknown id).

### Egress guard

Every connection a profile causes goes through `internal/egress`, a Go port of the webhook guard in `services/integration-hub-ts/src/ssrf.ts` (its test cases are ported one for one in `egress_test.go`). Always denied: loopback, `169.254.0.0/16` (the cloud metadata address included), `fe80::/10`, unspecified, multicast, reserved and CGNAT ranges, every IPv6 address outside `2000::/3` (except `fc00::/7`), zone identifiers, NAT64 (`64:ff9b::/96`), 6to4 (`2002::/16`) around a non-public IPv4 address, and IPv4-mapped IPv6 judged as the IPv4 it is. Private ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`) are denied unless the operator lists them in `MIG_EGRESS_ALLOW`: comma-separated CIDRs or `host:port` entries (a typo stops the service at start). It is deployment configuration (chart value `egress.allowCidrs` / `egress.allowHostPorts`); a tenant cannot change it, and an always-denied range stays denied even when listed. A host name is resolved once, every answer is checked, and the connection is pinned to the checked address (a second DNS answer cannot redirect it; `net.Dialer.Control` re-checks the address actually connected). Redirects are never followed. The guard's `TestDialFunc` (which sends a checked connection to a local test server) is honoured only inside a `go test` binary (`testing.Testing`); in the service a guard carrying one refuses every dial. The chart adds an optional egress NetworkPolicy as the second line.

### Connection test

`POST .../{id}:test` answers 200 with `ok`, `kind`, `durationMs` and either `failure` (`MIG-EGRESS-DENIED`, `MIG-CONNECT-FAILED`, `MIG-AUTH-FAILED`, `MIG-PROBE-FAILED`, `MIG-SECRET-MISSING`, `MIG-DROP-NOT-CONFIGURED`, `MIG-DROP-MISSING`) or what it found: `sql` looks up `config.driver` in the driver registry (`internal/probe/sql.go`; `postgres` over pgx stdlib today, the approved `sqlserver`, `oracle` and `mysql` drivers are added there in S2, each as a connector built from the driver's config struct with the guard as its dialer, never a connection string formatted from tenant input), runs in a read-only transaction with a 5-second statement timeout, and lists schemas (50), tables (200) and the columns of the first 20 tables; the postgres connector sets every setting on the pgx struct and clears what pgx read from the service's own `PG*` environment; `rest` sends one GET to `baseUrl` + `probePath` with the configured authentication, reads at most 64 KiB of the answer (never returned) and reports the status; `file` opens `dropDir` under `MIG_DROP_ROOT` with `os.Root` (no `..`, no symlink out of the root) and counts files matching `filePattern`. It never returns response bodies or row data.

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

- `codepage_normalize` (`string`): cleans text that passed through a legacy code page. Bytes that are not valid UTF-8 are read as Windows-1252 (so `0x92` becomes a quote and `0x80` the euro sign), the text is put in Unicode NFC, control characters are dropped, typographic quotes, dashes, the ellipsis, non-breaking spaces and a byte order mark become ASCII or nothing, and runs of white space collapse to one space.

Further field options:

- `format` (`date`, `timestamp` and `decimal` fields, not with `fn`): the explicit source format of the text. Dates: `YYYY-MM-DD`, `DD.MM.YYYY`, `MM/DD/YYYY`, `DD/MM/YYYY`, `YYYYMMDD`. Timestamps (read in `sourceTimezone`, emitted in UTC): `DD.MM.YYYY HH:mm:ss`, `MM/DD/YYYY HH:mm:ss`, `DD/MM/YYYY HH:mm:ss`. Decimals: `decimal_comma` (`1.234,56`) and `decimal_point` (`1,234.56`), both with correctly placed thousands separators only and with SAP's trailing minus (`1.000,50-` is -1000.5). Text that does not match the format is `MIG-MAP-COERCE`, never guessed (so `29/02/2023` is rejected, and the text `1234.56` under `decimal_comma` is rejected, not read as 123456); decimals then follow the normal scale and magnitude rules. Rule `default`, `value`, `enum` targets and data template values must be written in the same format. A JSON number on a `decimal` field is already a number and is not re-read in the format (`1.234` stays 1.234 under `decimal_comma`); a JSON number under `YYYYMMDD` is read as a date. An unknown format, or one that does not suit the type, is `MIG-MAP-INVALID`.

- `enumDefault`: target value for a present value with no `enum` entry; the row is loaded with a `MIG-MAP-ENUM-DEFAULT` warning. Without it the row rejects with `MIG-MAP-ENUM`.
- `scale` (0 to 6, decimals only): the value is rounded half-even (`RoundBank`) to `scale`. When rounding changes it, the row is loaded with a `MAP-PRECISION-LOSS` warning carrying the absolute delta. The magnitude check still applies after rounding.

Mapping options:

- `rejectWhen`: up to 50 conditions `{field, op, value | values, code}` evaluated on the mapped output once every field rule has succeeded (a row with a field reject gets only its field rejects). `field` must be a mapping target. `op` is `empty` (target absent or blank, no value), `eq`, `ne` (`value`), `in`, `notIn` (`values`, 1 to 200) or `match` (`value` is an RE2 pattern of at most 200 characters, compiled at parse). Comparisons use the string form of the mapped value (`true`, not `Yes`, after `ax_noyes`; the padded value after `sap_lead_zero`). `ne` and `notIn` also hit an absent target; `eq`, `in` and `match` do not. `code` is the reason shown in the exception queue: upper-case words joined by hyphens (`^[A-Z][A-Z0-9]*(-[A-Z0-9]+)+$`, at most 64 characters, for example `CUST-NO-GROUP`) and never starting with `MIG-`. Each matching condition adds `{field, code, "matched rejectWhen <op>"}`, in condition order, and `expectRejects` tests can assert the codes. An unknown op is `MIG-MAP-UNKNOWN-FN`; any other bad condition is `MIG-MAP-INVALID`.
- `sourceTimezone`: IANA zone for the `ax_datetime` and `sap_dats` time sources; an unknown name rejects with `MIG-MAP-INVALID`.
- `tests`: `[{name, input, expect | expectRejects}]`. `expect` is the exact mapped output as strings per target; `expectRejects` lists the reject codes. Each case has exactly one of the two; names are unique. `POST /v1/integration/migration-runs` runs the tests after parsing and before any backend call: a failing case answers 422 `MIG-MAP-TEST-FAIL` with each failed name in `errors`. A mapping without tests is accepted. `POST .../{id}:rerun` uses the stored mapping and does not re-run tests.

Warnings are reported per row in `warnings` and counted in `counts.warned` on the run view; warned rows are still loaded. Rejects remain the exception queue.

## Data templates

A data template (`domain.DataTemplate`, FR-10.18) is `{name, entity, version, defaults: {target: value}}`. `POST /migration-runs` may name one with `dataTemplateId`. Per row, a mapping target whose source is absent (or a null marker) takes the template value instead of the rule `default`; a source value always wins, and a template value satisfies `required`. Template values skip `enum` like rule defaults do and must satisfy the target's type. `CheckTemplate` runs when a run starts: the template must be for the mapping's entity and name only mapping targets that are filled from a source column (a `const` rule is never absent), else 422 `MIG-TEMPLATE-INVALID`; an unknown id is 422 `MIG-TEMPLATE-UNKNOWN`. Template values are part of the content hash, so a re-run over unchanged rows is unchanged. The run pins `dataTemplateId` (kept on `:rerun`), `counts.templated` counts the mapped rows that took a default, and `migration.run_ledger.template_fields` records the targets per row (`default_from_template`).

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
| MIG-ROW-DUPLICATE-KEY | source key repeated within one extract; exact repeats load once, repeats with different values reject every occurrence so extract order cannot decide the load |
| MIG-LOAD-FAILED | target loader error for the batch |
| MIG-BACKEND-NOT-CONFIGURED | no extract and load backend wired (501) |
| MIG-PKG-SIGNATURE | package signature or a file sha256 does not match the manifest (422) |
| MIG-PKG-SCHEMA-NEWER | package schema is newer than the target supports (422) |
| MIG-PKG-INVALID | cyclic or unknown-file dependency, repeated file or record key, bad schema version (422) |
| MIG-NOT-CONFIGURED | no package target or signing key wired (501) |
| COPY-KEY-EXISTS | row rejected: rewritten key already exists in the target and was not created by this copy project |
| MIG-COPY-INVALID | copy into the same legal entity or one of another tenant, missing project, entity, legal entities or tenants, or no target reader to detect existing keys without `Upsert` |
| MIG-PARALLEL-RUN-DEFERRED | parallel-run diffs not built (501) |
| MIG-CONNECTION-NAME-EXISTS | another connection profile of the tenant has this name (409) |
| MIG-CONNECTION-UNKNOWN | a project names a connection profile this tenant does not have (422) |
| MIG-SECRETS-NOT-CONFIGURED | the service has no fieldcrypt keys, so secrets cannot be set or used (501) |
| MIG-PROBE-NOT-CONFIGURED | no connection prober wired (501) |
| MIG-SECRET-UNREADABLE | the stored secret cannot be opened with the current keys; set it again (422) |
| MIG-EGRESS-DENIED, MIG-CONNECT-FAILED, MIG-AUTH-FAILED, MIG-PROBE-FAILED, MIG-SECRET-MISSING, MIG-DROP-NOT-CONFIGURED, MIG-DROP-MISSING | failure codes inside a `:test` answer (HTTP 200) |
| MIG-TEMPLATE-INVALID | data template shape wrong, or it does not fit the mapping of the run (422) |
| MIG-TEMPLATE-UNKNOWN | `dataTemplateId` names no published template (422) |
| MIG-TEMPLATE-VERSION-EXISTS | the entity, name and version already hold a different template (409) |
| MIG-PROJECT-NAME-EXISTS | another project of the tenant has this name (409) |
| MIG-VERSION-CONFLICT | the `version` sent for a project is stale; the detail names the current one (409) |
| MIG-PROJECT-NOT-COPY | key rewrite rules were asked of a project that is not a copy project (422) |
| MIG-PROJECT-ARCHIVED | a change other than reactivation, or a rule replacement, on an archived project (409) |
| AUTH-0404 | unknown run, project, or a run of another tenant (404) |
| idempotency_key_reuse | `Idempotency-Key` replayed with a different request (422) |

## Configuration packages and legal-entity copy

A package is `{version, schemaVersion, manifest, files}`. Each file is `{name, entity, records: [{key, body}]}`; the manifest lists the files with `sha256` of each file's canonical JSON (sorted keys, numbers in shortest decimal form) and `dependsOn`. The signature is an HMAC-SHA256 (`domain.HMACSigner`; the tenant key from Vault is a handoff) over the canonical `{version, schemaVersion, files}` of the manifest, so a package cannot be relabelled as an older schema. `domain.Seal` fills the file hashes and `domain.Sign` signs.

Diff and apply (`domain.Diff`, `domain.Apply`) check the signature and every file hash, then the schema version, then apply files in dependency order. Records are compared by canonical content hash into `create`, `update` and `unchanged`; only create and update records are written, records present only in the target are never deleted, a second apply reports no changes, and a dry run returns the same diff and writes nothing. The deployment wires no package target or signing key, so both routes answer 501 `MIG-NOT-CONFIGURED` until a later slice supplies them (`api.API.WithPackages`). Applied packages are stored in `migration.config_package` with an append-only `applied` history (`tenant_id`, `applied_at`, idempotency key, `diff_counts`).

Legal-entity copy (`domain.CopyRun`) copies one entity through the migration pipeline: `domain.RewriteKeys` rewrites every legal-entity-scoped natural key (a field value starting with `from` gets `to` in its place, the first matching rule per field wins, `@key` names the record key) before hashing. The copy project id is the xref source system, so a second run over unchanged rows skips every row. A rewritten key that already exists in the target and was not created by the project is rejected `COPY-KEY-EXISTS` unless `Upsert` is set. Copying into the same legal entity, or into one of another tenant, is `MIG-COPY-INVALID`, and so is a copy wired without a target reader (existing keys could not be detected) unless `Upsert` is set. Rewrite rules persist in `migration.key_rewrite_rule`, per project (`migration.project`, `0006_project.sql`); the HTTP routes above manage them, but no route starts a copy run yet. The extractor, loader and reader are tagged with the source or target legal entity through `domain.WithLegalEntity`; wiring to real contexts is listed in `NOT-IMPLEMENTED.md`.

## Pipeline rules

- Idempotency key is `sha256(source_system|source_entity|source_key|content_hash)`, independent of the run.
- The target id is minted once and stored in the xref before the loader is called; the content hash is recorded only after a successful load, so a failed row retries with the same target id.
- `GET /v1/integration/xref` browses the cross-reference read-only (exact legacy key, literal key prefix, Passage id; keyset `nextCursor`) and `/xref/summary` counts keys per source system and entity.
- Loads go through `TargetLoader` in batches of at most 1000; no direct SQL into target contexts.
- Source rows missing since the last run are reported in `Report.Absent`, never deleted.
- `Rerun` reprocesses only the keys in the previous report's exception queue and skips reconciliation.

## Dependencies

`github.com/shopspring/decimal v1.4.0` (exact money arithmetic), `pgregory.net/rapid v1.1.0` (property tests), `github.com/go-chi/chi/v5 v5.2.1` and `github.com/jackc/pgx/v5 v5.7.2` (router and Postgres, required by the platform kernel), all on the platform allow-list.

## Unverified

The column lists in `tools/connectors` (DirParty, CustTable, VendTable, InventTable, EcoResProduct, BOM, Route, KNA1, LFA1, MARA, MAST, STKO, PLKO) come from public data dictionaries and are flagged `Unverified`; they must be verified against coverage oracles that are not available in this repository. No mapping document for them ships.
