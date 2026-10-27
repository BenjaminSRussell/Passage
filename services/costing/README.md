# costing

Passage inventory costing service: wave 20 (ticket PSG-20, "Inventory Costing & Supplier Collaboration"), **slices 1 to 5**.

The service owns its tables and applies its own migrations (`migrations/0001_costing.sql`, `0002_activation.sql`,
`0003_valuation_setup_and_aging.sql`, `0004_cost_transaction.sql`) through
`dbx.Migrate`. The PRD places them in schema `costing` of `inventory-db` (`inventory/v20.*`); that
placement is a deploy decision recorded as a handoff in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## What slice 5 adds

The `CostTransaction` projection, the inventory value report and its GL tie-out (FR-20.4, with the
valuation ledger of FR-20.15 and the valuation type of FR-20.16).

- **Recording** `POST /internal/v1/costing/cost-transactions` (duty `costing.cost-transaction.post`,
  the producers' service account): a batch of 1 to 500 value-affecting events, all or nothing. Each
  row names its producer's `sourceKey` (a voucher line, a settlement row), legal entity, product,
  site, optional warehouse, valuation type, valuation ledger, inventory transaction and ledger
  voucher, the kind (PHYSICAL, FINANCIAL, INVOICE_DIFF, SETTLEMENT_ADJ, PPV, REVALUATION,
  MANUAL_ADJ, RECALC), a signed quantity and amount and the financial date. Value-only kinds carry
  quantity 0 (validated, and a check constraint). Amounts are decimals that fit `numeric(19,4)`
  without rounding. A `sourceKey` already recorded with the same content is a replay: it is skipped
  and not counted in `inserted`; with other content the whole batch is refused (`CST-CTX-001`).
  A named ledger must be one of the row's legal entity (`CST-VLG-005`); rows of the primary ledger
  are stored as the primary valuation, so it never splits over two keys. Every row is in its
  ledger's currency (`CST-CTX-002`): a non-primary ledger's, the primary ledger's, or, before any
  primary ledger exists, the currency of the legal entity's first rows (an advisory lock keeps two
  first batches from choosing two). The table is append-only: a trigger refuses UPDATE and DELETE.
- **Value report** `GET /v1/costing/value-report?legalEntityId=&asOf=&valuationLedgerId=&siteId=&groupBy=`
  (duty `costing.read`): quantity, physical value (PHYSICAL rows, not yet invoiced), financial
  value (every other inventory kind) and their sum, grouped by any of `product`, `site`,
  `warehouse`, `valuationType` (default `site`; an empty `groupBy` is one total row), with totals.
  PPV rows never count: a STANDARD item received above standard stays at the standard (AC-20.4).
  Balances with zero quantity and zero value are left out; a residual value on zero quantity stays.
  The currency is the ledger's; a ledger holding rows in another currency is refused
  (`CST-RPT-004`), never added up. Amounts print at the currency's minor unit, or in full when
  they carry more precision, so a sub-cent residual stays visible. Postgres sums to product ×
  site × warehouse × valuation type, the domain regroups (`internal/domain/valuereport.go`).
- **Tie-out** `POST /v1/costing/value-report:tie-out` (duty `costing.read`): the caller sends the
  ledger balances of the inventory accounts grouped like the report; each balance names exactly
  the grouping dimensions (`CST-RPT-002`) and once (`CST-RPT-003`). Each group is compared with
  its ledger balance, a group on one side only with zero, so an unexplained ledger balance is a
  difference, never dropped. The response counts the differing groups and the net difference.
  It only reads; the daily `costing.reconcile` job that reads the ledger itself and emits
  `reconciliation.diff` is a later slice.
- **Drill-down** `GET /v1/costing/cost-transactions?legalEntityId=&valuationLedgerId=&productId=&siteId=&kind=&asOf=`
  pages the cost transactions, newest financial date first.
- Migration `0004_cost_transaction.sql`: `cost_transaction` with tenant RLS (ENABLE and FORCE), the
  restrictive legal-entity scope policy, the append-only trigger, a unique source key per tenant
  and the PRD's indexes (product × date, voucher) plus one for the report.
- **Web screens**: `/costing/value-report` (legal entity, as-of, ledger and site, group-by
  choices, totals and rows, each row opening its cost transactions, and the tie-out against
  pasted ledger balances) and `/costing/cost-transactions` (filters, list, load more).

## What slice 4 adds

Valuation setup (FR-20.15, FR-20.16) and stored month-end aging (FR-20.17, 3.20.15).

- **Valuation ledgers** `POST /v1/costing/valuation-ledgers` (duty `costing.valuation.write`) registers a
  ledger of a legal entity bound to a wave 12 ledger group, with its costing method, currency and
  exchange rate type (SPOT, AVERAGE, HISTORICAL). At most one ledger per legal entity is primary
  (`CST-VLG-003`), a ledger group carries one valuation ledger (`CST-VLG-004`) and codes are unique
  per legal entity (`CST-VLG-002`). `GET /v1/costing/valuation-ledgers?legalEntityId=` lists them,
  primary first. The ledger group id is not checked against the ledger service yet.
- **Valuation categories** `POST /v1/costing/valuation-categories` (tenant-wide): a split category
  lists its valuation types, each with its inventory account; a BATCH category lists none, because
  each batch becomes its own type at receipt (BR-20.14). A batch category with types, a split
  category without, or a type listed twice is refused with `CST-VAL-003`; the table repeats the
  batch rule as a check constraint. `GET /v1/costing/valuation-categories` lists them.
- **Aging bucket sets** `POST|GET /v1/costing/aging/bucket-sets` (duty `costing.aging.write` to save):
  named day bounds per legal entity, validated like the aging preview's `bucketBounds`; a name is
  unique in its legal entity (`CST-AGE-002`).
- **Aging snapshots** `POST /v1/costing/aging/snapshots` (duty `costing.aging.write`, `Idempotency-Key`
  required) ages every product × site line as of a date exactly as the aging preview ages one
  product (per valuation type when split, average methods repriced from FIFO layers) and stores the
  result as month-end evidence that is never edited. Buckets come from a saved set of the same
  legal entity, from `bucketBounds`, or the default. With `valuationLedgerId` the snapshot takes the
  ledger's method and currency. The same transaction writes
  `dev.passage.costing.agingsnapshot.created.v1` to the outbox (Kafka key: the legal entity). A
  replayed key returns the stored snapshot with 200 and writes nothing; the key with input that
  computes a different snapshot is `idempotency_key_reuse` (compared by a SHA-256 of the computed
  snapshot). `GET /v1/costing/aging/snapshots` pages summaries without lines, newest as-of first;
  `GET /v1/costing/aging/snapshots/{id}` returns the lines. The lines carry their transactions until
  the `CostTransaction` projection exists (see NOT-IMPLEMENTED).
- Migration `0003_valuation_setup_and_aging.sql`: `valuation_ledger`, `valuation_category`,
  `aging_bucket_set`, `aging_snapshot`, each with tenant RLS (ENABLE and FORCE) and, where the table
  carries `legal_entity_id`, the restrictive legal-entity scope policy.
- **Web screens**: `/costing/valuation-setup` (ledgers with a legal-entity filter, and categories),
  `/costing/valuation-setup/ledgers/new`, `/costing/valuation-setup/categories/new`,
  `/costing/aging/snapshots` (list, load more), `/costing/aging/snapshots/new` (ledger and saved
  bucket set pickers), `/costing/aging/snapshots/:id` (totals, tie-out, buckets, lines by valuation
  type) and `/costing/aging/bucket-sets` (save and list).

## What slice 3 adds

Standard cost versions end to end (FR-20.5, BR-20.7): read, fill, compare, preview, activate, look up.

- **Reads.** `GET /v1/costing/versions` (filter by `legalEntityId`, `status`, `type`; ordered by
  code; cursor pages), `GET /v1/costing/versions/{id}` (with `itemCount`; the `ETag` is the
  optimistic `version`) and `GET /v1/costing/versions/{id}/item-costs` (by site and product, each
  with `activeCost`, the standard active for that product × site in another version, so the screen
  can compare pending to active, plus what activation recorded once the row is activated).
- **CSV import.** `item-costs:import` also takes `Content-Type: text/csv` with the header
  `productId,siteId,unitCost` (any order, case-insensitive, extra columns ignored). The file is all
  or nothing: the first bad row refuses it with `CST-IMP-001` naming the line.
- **Copy with percent change** `POST /v1/costing/versions/{id}/item-costs:copy`: copies another
  version of the same legal entity and currency into a DRAFT target, each cost moved by
  `percentChange` and rounded half-even to six places. A repeat replaces the pending costs.
- **Activation preview** `POST /v1/costing/versions/{id}:preview-activation` (duty `costing.read`):
  the revaluation without writing anything — total, one amount per site (the per-site voucher, the
  sum of its rounded lines) and the 20 largest deltas — plus the `version` to activate against.
- **Activation** `POST /v1/costing/versions/{id}:activate` with `If-Match: "<version>"`. In one
  transaction it supersedes the ACTIVE standard of each product × site, makes the pending costs
  ACTIVE with the revaluation recorded on each row, marks the version ACTIVE and writes one
  `dev.passage.costing.cost.changed.v1` per product × site to the outbox. The revaluation is
  `(new − old) × on-hand`, rounded half-even to the currency (BR-20.4); where a product × site has
  no standard yet it is `new × on-hand − bookValue`, so the caller must give the book value of
  that stock (`CST-ACT-007`) instead of the service revaluing it from zero. Activations in one
  legal entity are serialised by a transaction advisory lock, and a partial unique index keeps at
  most one ACTIVE cost per product × site. Activating an ACTIVE version answers `ALREADY_ACTIVE`
  with the recorded revaluation and writes nothing (idempotent per version id). Only DRAFT
  STANDARD versions whose effective date has arrived activate; PLANNED versions feed the wave 39
  roll-up and never become the inventory standard. On-hand (and book value) come in the request
  body: the service does not read the wave 16 inventory ledger yet (see NOT-IMPLEMENTED).
- **Cost lookup** `GET /v1/costing/products/{id}/cost?site=&date=` for waves 21 and 27: the
  standard activated last on or before the end of `date` (UTC, default today), even if it has been
  superseded since; `CST-COST-001` when there is none.
- Migration `0002_activation.sql`: `costing_version.activated_at`; on `item_cost` the recorded
  `previous_unit_cost`, `book_value`, `on_hand_qty`, `revaluation_amount`, `activated_at`; the
  one-ACTIVE partial unique index and an index for the as-of lookup.
- **Web screens**: `/costing/versions` (list with filters), `/costing/versions/new` (create form)
  and `/costing/versions/:id` (details, item costs compared to active, CSV import, copy with
  percent, and activation that stays disabled until the revaluation preview for the same on-hand
  has been shown).

## What slice 2 adds

- **Marking** (FR-20.8): an issue names the receipt it settles against first (`markedReceiptId`).
  The marked quantity is reserved on the receipt when it enters the engine, so no unmarked issue or
  earlier shortage consumes it, and the marked part settles at that receipt's own cost under every
  method except STANDARD (where every unit costs the standard). Under the average methods the
  reserved part leaves the pool. A marked issue sequenced before its same-day receipt waits for it.
  A mark on a receipt, an unknown receipt or a receipt dated after the issue is refused with `CST-TXN-001`.
- **Split valuation** (FR-20.16, `internal/domain/valuation.go`): stock is kept per valuation type
  (`valuationType` on each transaction, optional `defaultValuationType` on the request). Each type
  is its own cost pool. In a split product every receipt needs a type (its own or the default). An
  untyped issue takes the type of the receipt it marks, else the default, else the only type holding
  stock; it is refused with `CST-VAL-001` when several hold stock. `CanChangeCategory` refuses a category change while stock
  exists (`CST-VAL-002`). The settlement preview returns `remainingByValuationType` when types are used.
- **Parallel valuation ledgers** (FR-20.15, BR-20.13, `internal/domain/ledger.go`):
  `POST /v1/costing/valuation-ledgers:preview` settles one transaction sequence under several
  ledgers, each with its own method, currency and rate. Exactly one ledger is primary with rate 1.
  Receipt values convert at the ledger rate and round to its scale. Nothing is posted.
- **Inventory aging** (FR-20.17, BR-20.15, `internal/domain/aging.go`):
  `POST /v1/costing/aging:preview` buckets remaining cost by calendar days since receipt. Default
  buckets are 0–30, 31–90, 91–180, 181–365 and > 365; `bucketBounds` overrides them. For the average
  methods the ages come from FIFO layers, repriced to the average, so `tiesToRemaining` is true.
  A split product is aged per valuation type (each at its own average) and the response adds
  `valuationTypes`. Transactions dated after `asOf` are refused.

- **Web screens** (`apps/web/src/modules/costing`): the settlement preview now takes a default
  valuation type and shows remaining value by type; new pages at `/costing/valuation-ledgers`
  (ledger editor, per-ledger results and issue costs side by side) and `/costing/aging` (buckets,
  tie-out to the remaining value, and aging per valuation type).

## What slice 1 does

- **Settlement engine** (`internal/domain`, pure, no I/O). Values issues against receipts under
  an item model group's costing method: `FIFO`, `LIFO`, `LIFO_DATE`, `WEIGHTED_AVG_RUNNING`,
  `WEIGHTED_AVG_PERIODIC`, `MOVING_AVG`, `STANDARD` (FR-20.1). Quantity that stock does not
  cover when an issue is costed waits on a backlog; a later receipt in the same run settles it
  first. What no receipt covers is valued at the BR-20.3 fallback (running average of the stock
  before the issue, then the standard cost, then the last receipt cost, or the configured
  `STANDARD` / `LAST_RECEIPT`) and listed as an exception. Rounding is half-even per settlement row, with the last
  row absorbing the residual (BR-20.4). Ordering is by (financial date, physical date, id), so
  the output is deterministic (BR-20.2, property-tested with `pgregory.net/rapid`).
- **Purchase price variance** (`internal/domain/ppv.go`): receipt PPV `(PO − standard) × qty` and
  invoice PPV `(invoice − PO) × qty` (FR-20.7, AC-20.4). A positive amount is a debit.
- **Settlement preview API** `POST /v1/costing/settlements:preview`. Stateless; nothing is posted.
- **Costing versions** `POST /v1/costing/versions` (DRAFT, idempotent per `Idempotency-Key`, code
  unique per legal entity) and item-cost import `POST /v1/costing/versions/{id}/item-costs:import`
  (one statement, idempotent per product and site, bumps the version's `version`). Persisted with
  tenant RLS and the legal-entity scope policy (`migrations/0001_costing.sql`).
- **PPV hook** `POST /internal/v1/costing/ppv` (calculation only; persisting `CostTransaction` is
  a later slice).
- Web module `apps/web/src/modules/costing`: settlement preview page (slice 2 adds the valuation ledger and aging screens).

Everything else in the ticket is listed in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## Run

```sh
# from the repo root
PASSAGE_ENV=dev SERVICE_NAME=costing DATABASE_URL=postgres://... MIGRATE_ON_START=true \
  go run ./services/costing/cmd/costing
```

Endpoints: `GET /healthz`, `GET /readyz`, the API above, metrics on the admin port 9090.

## Test

```sh
cd services/costing
go vet ./... && go test ./...
```

Store tests use `platform/go/testkit` and skip unless `PASSAGE_TEST_DATABASE_URL` is set. They run
the queries as a non-superuser role so RLS applies, which needs `PASSAGE_TEST_APP_DATABASE_URL` as
`passage_app`:

```sh
PASSAGE_TEST_DATABASE_URL=postgres://admin@... PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app@... go test ./...
```

## Contracts

- HTTP: [`api/openapi.yaml`](api/openapi.yaml)
- Events: [`events/asyncapi.yaml`](events/asyncapi.yaml). `dev.passage.costing.cost.changed.v1` is
  published on activation and `dev.passage.costing.agingsnapshot.created.v1` when a snapshot is
  stored; the planned catalog is listed under `x-planned-events`.
- Chart: [`charts/costing`](charts/costing) (`helm lint charts/costing`).

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `COM-0001` | 400 | Request failed validation; `errors` maps field to reason |
| `AUTH-0001` | 401 | Missing or malformed tenant |
| `AUTH-0003` | 403 | Legal entity is outside the caller's scope |
| `CST-TXN-001` | 422 | Invalid settlement input (a non-positive quantity, or `STANDARD` without a positive standard unit cost) |
| `CST-TXN-002` | 422 | Duplicate transaction id in one preview |
| `CST-PPV-001` | 422 | PPV inputs invalid (quantity not positive or a negative unit price) |
| `CST-VAL-001` | 422 | An untyped issue must name a valuation type because several types hold stock |
| `CST-VAL-002` | 422 | A valuation category cannot change while the product holds stock (domain guard; no endpoint changes a category yet) |
| `CST-VLG-001` | 422 | Valuation ledger set invalid (no or two primaries, duplicate id, non-positive rate, primary rate not 1) |
| `CST-VLG-002` | 409 | A valuation ledger with this code already exists in the legal entity |
| `CST-VLG-003` | 409 | The legal entity already has a primary valuation ledger |
| `CST-VLG-004` | 409 | The ledger group already carries a valuation ledger in this legal entity |
| `CST-VLG-005` | 404 / 422 | Valuation ledger not found (404 for a snapshot or report naming an unknown ledger or one of another legal entity; 422 for a cost transaction row naming one) |
| `CST-CTX-001` | 422 | A cost transaction `sourceKey` was already recorded with other content; the batch is refused |
| `CST-CTX-002` | 422 | A cost transaction is not in its valuation ledger's currency |
| `CST-RPT-001` | 422 | Value report grouping unknown or repeated (product, site, warehouse, valuationType) |
| `CST-RPT-002` | 422 | A tie-out ledger balance does not name exactly the grouping dimensions |
| `CST-RPT-003` | 422 | Two tie-out ledger balances name the same dimension values |
| `CST-RPT-004` | 422 | The valuation ledger holds cost transactions in more than one currency |
| `CST-VAL-003` | 422 | Valuation category types invalid: a batch category with types, a split category without, or a type listed twice |
| `CST-VAL-004` | 409 | A valuation category with this code already exists |
| `CST-AGE-002` | 409 | An aging bucket set with this name already exists in the legal entity |
| `CST-AGE-003` | 404 | Aging bucket set not found (a snapshot named an unknown set) |
| `CST-AGE-004` | 404 | Aging snapshot not found |
| `CST-VER-CODE-DUPLICATE` | 409 | A costing version with this code already exists for the legal entity |
| `CST-VER-NOT-FOUND` | 404 | Costing version not found in this tenant |
| `CST-VER-NOT-DRAFT` | 409 | Item costs can only be imported or copied into, and only a DRAFT version activated (an ARCHIVED one is refused) |
| `CST-IMP-001` | 422 | Item-cost CSV refused (header, a bad row named by line, duplicates, too many rows) |
| `CST-ACT-001` | 422 | Costing version is blocked for activation |
| `CST-ACT-002` | 422 | Pending costs include zero and zero cost is not allowed |
| `CST-ACT-003` | 422 | The version has no pending item costs |
| `CST-ACT-005` | 422 | Only a STANDARD version can be activated |
| `CST-ACT-006` | 422 | The version's effective date is still ahead |
| `CST-ACT-007` | 422 | A product × site has stock but no active standard, and no `bookValue` was given |
| `CST-ACT-008` | 409 | Another activation changed the same product costs at the same moment; preview again |
| `CST-COPY-001` | 422 | Percent change at or below -100, or a copied cost past 14 integer digits |
| `CST-COPY-002` | 422 | The source version has no item costs |
| `CST-COPY-003` | 422 | The source is the target version itself |
| `CST-COPY-004` | 422 | The source belongs to another legal entity or uses another currency |
| `CST-COST-001` | 404 | No standard cost in force for the product and site on that date |
| `conflict_version` | 412 | `If-Match` is not the version's current optimistic version |
| `COM-0428` | 428 | `If-Match` missing on activation |
| `idempotency_key_reuse` | 422 | `Idempotency-Key` reused with a different body |
| `AUTH-0003` | 403 | Missing duty (kernel `authz.RequireDuty`) |
| `COM-0500` | 500 | Unexpected error; detail hidden, logged with the request id |

## Duties

| Duty | Routes | PRD duty (§3.20.17) |
|---|---|---|
| `costing.read` | `POST /v1/costing/settlements:preview`, `POST /v1/costing/valuation-ledgers:preview`, `POST /v1/costing/aging:preview`, `GET /v1/costing/versions`, `GET /v1/costing/versions/{id}`, `GET /v1/costing/versions/{id}/item-costs`, `POST /v1/costing/versions/{id}:preview-activation`, `GET /v1/costing/products/{id}/cost`, `GET /v1/costing/valuation-ledgers`, `GET /v1/costing/valuation-categories`, `GET /v1/costing/aging/bucket-sets`, `GET /v1/costing/aging/snapshots`, `GET /v1/costing/aging/snapshots/{id}`, `GET /v1/costing/value-report`, `POST /v1/costing/value-report:tie-out`, `GET /v1/costing/cost-transactions` | CostingInquire |
| `costing.version.write` | `POST /v1/costing/versions`, `POST /v1/costing/versions/{id}/item-costs:import`, `POST /v1/costing/versions/{id}/item-costs:copy` | CostingVersionMaintain |
| `costing.version.activate` | `POST /v1/costing/versions/{id}:activate` | CostingVersionActivate |
| `costing.ppv.post` | `POST /internal/v1/costing/ppv` | service account of receiving and payables |
| `costing.cost-transaction.post` | `POST /internal/v1/costing/cost-transactions` | service account of the producers (wave 16 postings, the close, the PPV hook) |
| `costing.valuation.write` | `POST /v1/costing/valuation-ledgers`, `POST /v1/costing/valuation-categories` | ValuationSetupMaintain |
| `costing.aging.write` | `POST /v1/costing/aging/bucket-sets`, `POST /v1/costing/aging/snapshots` | CostingInquire saves report setups; the month-end snapshot job's service account stores snapshots |

## Dependencies

Only the platform module and what it already pulls in (chi, pgx/v5, shopspring/decimal, google/uuid,
pgregory.net/rapid for tests). No new dependencies in slice 1.

## Wave reference

PSG-20 · wave 20 · PRD `docs/prd/phase-3-procure-to-pay.md` §3.20 · depends on PSG-19 (waves 16–19 for
transactions, receipts, invoices and payments).
