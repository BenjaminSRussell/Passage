# Migration factory (wave 49, PSG-49)

Tooling for moving a customer off AX 2012 / D365 F&O or SAP ECC / S/4HANA: transactional
migration packs with control totals, legacy cross-references, parallel-run diffing and the
cutover runbook generator. Wave reference: PRD section 9.49, ticket PSG-49.

## Slices 1 to 6 (this build)

Built and tested:

- **Pack lifecycle** (`internal/domain/pack.go`, `run.go`): the nine packs, the run state machine
  (EXTRACTED → VALIDATED → LOADED → RECONCILED, FAILED, ROLLED_BACK), the rule that RECONCILED
  needs no MISMATCH control total, and the MIG-1501 cutoff-versus-freeze check.
- **Control totals** (`controltotal.go`, `run.go`): MATCH / WITHIN_TOLERANCE / MISMATCH, a tolerance
  that requires a reason, an order-independent sha256 over `(legacy_id, amount)`, the debit = credit
  rule, and the open_items totals: count, amount per currency, and amount per (company, account,
  currency).
- **Validation and quarantine** (`validate.go`): referential (party in master data), domain
  (ISO 4217 currency, control account present), and uniqueness rules, plus the
  `quarantine_max_pct` gate (0-10, default 1; MIG-1504). An amount with more than six decimal
  places (the stored `numeric(28,6)` scale) rejects the open_items, gl_balances, onhand or project_wip
  request, so a run never reconciles a value Passage stores rounded.
- **Idempotent loading** (`loader.go`, `internal/store`): records already in `legacy_xref` by natural
  key `(system, company, table, id)` are skipped, so a re-run loads zero records (MIG-1505). Runs of
  one (system, company) serialize on an advisory lock.
- **Parallel-run classifier** (`diff.go`): outer join on `(area, key)`, EXPECTED by rule `RND-001`
  (≤ 0.01), EXPLAINED by an operator reason given for that area and key, otherwise UNEXPLAINED.
  Sign-off gate: zero unexplained and explained ≤ 0.1 % of revenue (MIG-1509).
- **Cutover plan** (`cutover.go`): timed steps from T-14d to T+30d with pack load steps, rehearsal
  duration overrides, go/no-go sequencing (CUT-1601), the abort step at `abortOffsetHours`, the
  BR-49.9 abort state (CUT-1604), rollback once with two distinct approvers (CUT-1603), and a plan
  version bumped on every change. A step can be started (once, under the same go/no-go gate) so
  the runbook shows elapsed against planned, amber at 110 % and red at 125 % with CUT-1602; a
  completion without a duration is measured from the start. With `expandByEntity` the pack loads
  become a freeze, extract, load and reconcile chain per legal entity with a sign-off and hypercare
  check, ordered by CUT-1609 (D5a, the 84-step `fx-cutover-plan` golden file in `internal/domain/testdata`).
- **Rollback plan** (`rollback.go`, FR-49.9): generated from the plan, starting with the decision
  point at the abort deadline, then stop Passage access, restore legacy write access, resume the
  coexistence adapters, export the postings since T-0 and re-key them.
- **Pack catalog** (`catalog.go`, FR-49.1): the nine packs for AX 2012, D365 F&O, SAP ECC and
  S/4HANA with their FR-49.2 control totals and whether a run can be started today.
- **Rollback re-keying export** (`rekey.go`, FR-49.9): the postings since T-0 given with the
  rollback become a journal file (debit and credit, each journal balanced) and a document file,
  kept write-once in `rekey_line` and served as CSV; postings before T-0 are counted and left out.
- **Quarantine release** (`release.go`): a quarantined open item is corrected (party or control
  account), re-validated, loaded into `legacy_xref` and booked into the run's control totals; the
  run reaches RECONCILED with the last release.
- **Parallel-run review** (`parallel.go`): a stored run per (period, system) with its variances;
  reviewers explain variances and sign off, and the user who started the run cannot (MIG-001).
- **Project beginning balances** (`project.go`): the project_wip pack validates opening lines,
  loads them with their `legacy_xref` rows (never posting to the GL) and reconciles cost, sales,
  WIP cost and WIP sales per project and category to legacy; a project that differs is
  `OPENING_MISMATCH`.
- **GL balances** (`gl.go`, `internal/store/gl.go`): the gl_balances pack refuses an extract
  whose debits differ from its credits in a currency, quarantines balances by uniqueness (legacy
  id and (account, dimensions, currency)), referential (chart of accounts) and domain (ISO 4217)
  rules, loads the rest write-once into `gl_opening_balance` with their `legacy_xref` rows (never
  posting to the GL) and compares the balance per (company, account, dimensions, currency), the
  count of non-zero combinations and the trial balance per currency. A run that names an
  open_items run of the same company also compares the open items per control account with the
  GL balance of that account (BR-49.2, MIG-1506).
- **On-hand stock** (`onhand.go`, `internal/store/onhand.go`): the onhand pack quarantines lines by
  uniqueness (legacy id and (item, site, warehouse, location, batch, serial)), referential (the
  item master given with the run) and domain (site, warehouse and unit present, quantity above
  zero, value not negative, a serial of exactly one, ISO 4217) rules, loads the rest write-once
  into `onhand_opening_balance` at legacy cost with their `legacy_xref` rows (nothing posts to
  the inventory ledger) and compares the quantity per item and dimension, the value per item and
  site and the value per company and currency. A run that names a gl_balances run of the same
  company and an inventory account also compares that account's migrated balance with the loaded
  value (BR-49.3, `onhand.inventory_account`). A serial opens once per item and company whatever its dimension. Nothing is
  posted, so the go/no-go `packs_reconciled` item stays open for a plan that includes onhand.
- **Load of open AR into receivables** (`internal/target`, `internal/store/targetload.go`,
  `internal/api/targetload.go`, integration track S8): a run started with a `load` object
  (`target: receivables`, the legal entity, an optional import source and a legacy party to Passage
  customer map) stages its accepted records as receivables rows (`target_row`). `POST
  /migration/runs/{id}/load` sends them to `POST /v1/receivables/open-items:batch` in batches of at most
  1000 with a stable `Idempotency-Key` per batch, retrying 5xx, 429 and timeouts with backoff, records
  each row's outcome (or the target's `AR-OPENING-*` code as the pack's reject) and the open-item id the
  target assigned (also in `legacy_xref`, once), then compares the target's totals from `GET
  /v1/receivables/open-item-imports` (item count, debit, credit, net per currency) with what the
  factory sent; a difference is MISMATCH and flags the pack. A rerun of an unchanged snapshot sends
  nothing (`target_loaded` remembers each accepted row's hash). Open AP rows are `NOT_AVAILABLE`
  rejects until payables has an opening-item API. Both sides of the reconciliation cover the whole
  import source in the legal entity across runs; one load runs at a time per import source and legal
  entity, an older run is not loaded over a newer one, and one import source belongs to one legacy
  company. The factory calls as the service `migration-factory` (its own key) acting as
  `migration-loader`, each token holding one duty; without `RECEIVABLES_URL` and a signing key the service starts and the
  load answers 503 `MIG-1520`. Tests run against `internal/target/targettest`, an httptest fake that
  mirrors the receivables handler.
- **Legacy cross-reference lookup**: `GET /api/v1/migration/xref` by legacy id or by Passage record.
- **Store** (`internal/store`, `migrations/`): Postgres with forced row-level security on every
  table, the outbox written in the same transaction, `Idempotency-Key` replay, and an optimistic
  version check on plan commands.
- **HTTP API** (`internal/api`): the kernel router, tenant middleware and duty checks. Contract in
  `api/openapi.yaml` (a test checks it against the routes); events in `events/asyncapi.yaml`.
- **Web module** (`apps/web/src/modules/migration-factory`): the pack catalog, pack runs (list, open_items,
  gl_balances and project_wip start forms, detail with the control-total board, quarantine release and projects),
  project opening balances, parallel runs (list, start, variance review and sign-off), the legacy
  reference lookup, and cutover runbooks (list, generate form, detail with go/no-go checklist,
  step start with the live timer, step completion, the rollback plan, rollback with the re-keying
  postings, and the re-keying file downloads).
- **Chart** (`charts/passage-migration-factory`): Deployment, Service and ServiceMonitor toggle.

What is not built yet is listed in `NOT-IMPLEMENTED.md`.

## Run

```
cd tools/migration-factory
DATABASE_URL=postgres://... MIGRATE_ON_START=true HTTP_ADDR=:8080 go run ./cmd/migration-factory
```

To load open items into receivables also set `RECEIVABLES_URL=http://receivables:8080`,
`PASSAGE_SERVICE_NAME=migration-factory` and `PASSAGE_SERVICE_SIGNING_KEY` (base64 32-byte Ed25519
seed; or `PASSAGE_SERVICE_SIGNING_KEY_FILE`). The receivables service must trust the matching public
key for `migration-factory` and the grant table must hold `receivables.import.write` and `ARView`
for it (NOT-IMPLEMENTED.md, H-S8-1 and H-S8-2). An address without a key, or a key for another service
name, stops the start; neither set starts the service with the load step answering 503.

`DATABASE_URL` is required and should use the `passage_app` role (no BYPASSRLS). Requests need the
gateway headers the kernel reads: `X-Passage-Tenant` (required), `X-Passage-Duties` (comma list),
and optionally `X-Passage-User`.

## Test

```
cd tools/migration-factory
go vet ./... && go test ./...
```

Domain tests are table-driven, with `pgregory.net/rapid` property tests for hashing, validation
partitioning, idempotent re-runs and the 100-variance classifier. Store and API tests that need
Postgres use `platform/go/testkit` and skip without `PASSAGE_TEST_DATABASE_URL`; the row-level
security tests also need `PASSAGE_TEST_APP_DATABASE_URL` (the `passage_app` role), and the API tests
run as that role when it is set.

The wave proof `TestWave49_SyntheticYearCutoverRehearsal` (database) runs a synthetic AX 2012 and
SAP ECC year (three entities each) through the generated runbook and the rollback. Its extracts
and expected values come from `internal/api/testdata/synth/synth_reference.py`, an independent
standard-library reference; regenerate with `python3 -I internal/api/testdata/synth/synth_reference.py`.

Web: `cd apps/web && npx vitest --run src/modules/migration-factory && npx tsc --noEmit`.

## API and duties

| Route | Duty |
|---|---|
| `GET /api/v1/migration/packs` | `MigrationView` |
| `POST /api/v1/migration/packs/{pack}/runs` | `MigrationRun` |
| `GET /api/v1/migration/runs` | `MigrationView` |
| `GET /api/v1/migration/runs/{id}` | `MigrationView` |
| `GET /api/v1/migration/runs/{id}/control-totals` | `MigrationView` |
| `POST /api/v1/migration/runs/{id}/load` | `MigrationRun` |
| `GET /api/v1/migration/runs/{id}/target-load` | `MigrationView` |
| `GET /api/v1/migration/runs/{id}/target-rows` | `MigrationView` |
| `POST /api/v1/cutover/plans/generate` | `CutoverManager` |
| `GET /api/v1/cutover/plans` | `CutoverManager` |
| `GET /api/v1/cutover/plans/{id}` | `CutoverManager` |
| `POST /api/v1/cutover/plans/{id}/steps/{seq}/start` | `CutoverStepOwner` |
| `POST /api/v1/cutover/plans/{id}/steps/{seq}/complete` | `CutoverStepOwner` |
| `POST /api/v1/cutover/plans/{id}/rollback` | `CutoverManager` (two different callers approve; the second runs it) |
| `GET /api/v1/cutover/plans/{id}/rekeying/{file}` | `CutoverManager` |

Duties are checked by the kernel's local `DutyPolicy`. The OPA decision point replaces it later
(see NOT-IMPLEMENTED.md). Every POST accepts an optional `Idempotency-Key`; plan commands accept
`version` for the optimistic check.

## Events

| Type | When |
|---|---|
| `dev.passage.migration.pack.reconciled.v1` | every pack run, after its control totals are computed |
| `dev.passage.migration.pack.loaded.v1` | a pass of the load into the target service ended, with its row counts and reconciliation result |
| `dev.passage.cutover.step.completed.v1` | a runbook step is marked done |
| `dev.passage.cutover.plan.rolledback.v1` | a plan's rollback is executed, with the re-keying line counts |

## Error codes

Problem details use `type` `https://passage.dev/problems/<code>`. The stable `code` is one of:

| Code | HTTP | Meaning |
|---|---|---|
| `MIG-0001` | 422 | Input breaks a domain rule. The detail carries the message code (MIG-1501, MIG-1504, CUT-1601, CUT-1603, ...). |
| `MIG-0003` | 404 | Run or plan not found for this tenant. |
| `MIG-0004` | 409 | Illegal state transition: a second rollback, completing a step of a rolled-back plan, or reading the re-keying files of a plan that was not rolled back. |
| `MIG-0005` | 422 | Pack or source not available in this slice. |
| `MIG-0006` | 409 | The plan `version` in the request is not the current one. |
| `MIG-0007` | 422 | `Idempotency-Key` reused with a different request. |
| `MIG-1520` | 503 | The receivables target is not configured (`RECEIVABLES_URL`, service signing key). |
| `MIG-1521` | 502 | Receivables refused the factory's service token (missing grant or key); the load is FAILED and its rows stay pending. |
| `MIG-1522` | 409 | Another request is loading the run, or another run of the same import source and legal entity. |
| `MIG-1523` | 409 | The run was started without a `load` object (or its load is outside the caller's legal entities). |
| `MIG-1524` | 502 | Receivables refused the whole batch request (a 4xx that is not a row result); the load is FAILED and its rows stay pending. |
| `MIG-1525` | 409 | A later run of the same import source and legal entity has already been loaded; load the latest run. |
| `MIG-1526` | 409 | The run's import source is already used by another legacy (system, company). |
| `AUTH-0001` | 401 | No tenant or principal. (Kernel.) |
| `AUTH-0003` | 403 | Missing duty. (Kernel.) |
| `COM-0001` | 400 | Malformed JSON body. (Kernel.) |
| `COM-0413` | 413 | Body over the limit (16 MiB for pack runs). |

Message codes raised inside detail text:

- MIG-1501 cutoff after the legacy freeze
- MIG-1504 quarantine share exceeds the maximum
- MIG-1505 re-run loaded 0 new records (informational, returned in `message`)
- MIG-1509 parallel run cannot be signed off
- CUT-1601 go/no-go item open (step sequencing)
- CUT-1603 rollback needs two different authenticated approvers (approve, then confirm)
- CUT-1604 abort deadline approaching with go/no-go items open (returned in `abortWarning`)
- CUT-1609 a step of a per-entity runbook waits for its prerequisites (the entity's freeze and extract before a load, the load before its reconcile, every pack reconcile before the entity reconciliation, then the sign-off and hypercare check; every entity's sign-off before a plan-wide go/no-go step)

## Dependencies

Go: the platform module (`httpx`, `tenant`, `authz`, `ids`, `dbx`, `events`, `outbox`,
`testkit`), chi, pgx/v5, shopspring/decimal, and pgregory.net/rapid for tests. All are already in the platform module's dependency tree, so no new
dependency needs a justification.
