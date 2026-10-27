# assets (Wave 37: Fixed Assets & Leases, slices 1 to 3)

The fixed-asset subledger and IFRS 16 leases. Slice 1 delivers the asset
register, depreciation books with the straight-line and declining-balance
(with switch to straight line, MACRS-style) engines, the projected schedule,
acquisitions at book creation, the transaction log, disposals (sale or scrap,
partial percentage with gain/loss on every open book) and lease recognition
with liability and ROU schedules. Slice 2 adds adjustments (acquisition and
depreciation adjustments, revaluation, impairment with a capped reversal),
asset additions and components, depreciation runs (propose, review, post, with
the sequential rule and an advisory lock), insurance policies with
insured-value index series, and capital appropriation (capex) requests with
variants and amount-based approval routing. Slice 3 adds lease
remeasurement on modification (BR-37.8) with the effective periodic-rate
option, physical inventory count sessions that reconcile scanned barcodes
against the register (FR-37.11), and the insurance report (7.37.14). The web module is
`apps/web/src/modules/assets`.

Reference: PSG-37, `docs/prd/phase-7-treasury-controlling.md` section 7.37.
The Helm chart is `charts/assets` (the PRD calls it `asset-svc`; the chart is
named after the service directory, per the repository convention).

## Layout

```
cmd/assets/            entrypoint: dbx.Connect -> (optional) dbx.Migrate -> httpx.NewRouter -> register -> httpx.Serve
internal/domain/       pure rules: Schedule (SL, DB/MACRS), Dispose, ComputeLease and Remeasure, ReconcileCount, Adjust and the
                       impairment reversal cap, DecideRun / RunAmount, insured value, capex state
                       machine and approval levels (no I/O)
internal/store/        pgx repositories, tenant transactions, idempotency_keys, outbox writes
internal/api/          chi handlers, DTOs, validation, duties
internal/events/       event types and payloads (dev.passage.assets.*.v1)
migrations/            0001_assets.sql (RLS on every table, append-only asset_transaction),
                       0002 adjustments, runs and additions, 0003 insurance and capex,
                       0004 lease modifications, rate method and count sessions
testdata/              worked-examples.json: PRD fixtures (AC-37.1, AC-37.2)
api/openapi.yaml       HTTP contract for every route (checked by TestRoutesMatchOpenAPI)
events/asyncapi.yaml   events published through the outbox (checked by TestEventsMatchAsyncAPI)
charts/assets/         Helm chart (liveness /healthz, readiness /readyz, ServiceMonitor toggle)
```

## Run

```
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_assets
export MIGRATE_ON_START=true        # dev only; stage/prod run migrations as a PreSync Job
go run ./cmd/assets
```

Ports: `HTTP_ADDR` (default `:8080`), `ADMIN_ADDR` (default `:9090`, `/metrics`).
`ASSET_MAX_BOOKS` (default 10, 1 to 20) caps books per asset.
Tenant and duties come from the gateway headers handled by `platform/go/tenant`
and `authz` until OPA is wired (wave 2 kernel).

## API rules

- Commands (`POST`) need `Idempotency-Key`. The key, a hash of the request and
  the response are stored in `idempotency_keys` in the same transaction as the
  change: a retry replays the first response, the same key with a different
  request is 422 `idempotency_key_reuse`.
- `GET /v1/assets/{id}` and the create return `ETag: "<version>"`;
  `POST /v1/assets/{id}/dispose` requires `If-Match` (428 without, 412 when stale).
  Runs and capex requests carry their own ETag: posting a run
  (`POST /v1/assets/depreciation-runs/{id}/post`) and submitting a request
  (`POST /v1/assets/capex-requests/{id}/submit`) require `If-Match`; both are
  state checks and take no Idempotency-Key. `PUT /v1/assets/index-series/{code}`
  is idempotent by itself.
- Adjustments (`POST /v1/assets/{id}/transactions`) post on one Open book and
  keep 0 <= accumulated <= cost. An impairment reversal is capped at the
  impairment still recorded and at the carrying amount the book would have had
  without impairment (FR-37.9); a capped reversal answers `limited: true` with
  an AST-0512 warning.
- A depreciation run covers one book code of a legal entity and one period:
  `YYYY-MM` for monthly (straight-line) books, `YYYY` for yearly
  (declining-balance) books. Each Open book is Due when the period is its next
  unposted schedule line; an earlier unposted period is an AST-0503 exception
  (BR-37.3, no catch-up yet) and exceptions never block the other books.
  Proposing again refreshes an unposted run; a posted period is AST-0502; a
  concurrent request for the same book and period is AST-0504 (advisory lock,
  BR-37.9). Posting re-decides each book under a row lock and turns a book
  that changed since the proposal into an AST-0018 exception. After an
  adjustment the charge spreads the remaining depreciable basis over the
  remaining periods in the schedule's proportions (BR-37.4); the last period
  clears the basis exactly.
- Leases: `rateMethod` Nominal (default) uses ibrRate / paymentsPerYear per
  period; Effective uses (1 + ibrRate)^(1 / paymentsPerYear) - 1, so the
  periods compound to the annual rate (lease.periodicRateMethod). `GET` and
  the create return the lease ETag. `POST /v1/leases/{id}/modify` (If-Match and
  Idempotency-Key) remeasures an Active or Modified lease from
  `effectivePeriod` on (BR-37.8): the liability becomes the present value of
  the remaining payments at the lease's rate for a payment change, or at the
  revised `ibrRate` for a scope change (a revised `termMonths` is always a
  scope change). The adjustment is recorded on the effective period's
  `remeasurement`, the later lines are replaced, earlier lines are kept, and
  the ROU moves by the same amount; the part that would take the ROU below
  zero is `profitLossExcess` (P&L). `liability` and `rouAsset` stay the
  amounts at recognition; `modifications` lists every remeasurement, and
  Σ ROU depreciation = rouAsset + Σ rouAdjustment. A period already posted
  to the ledger cannot be remeasured (AST-0027). The response carries the
  AST-0516 info message.
- Count sessions (`POST /v1/assets/count-sessions`, duty AssetCount) classify
  the scans against the Open assets of the legal entity that carry a barcode:
  Found, Missing and Unknown (one AST-0525 warning each); a repeated scan
  counts once (`duplicateCount`). The register is not changed.
- The insurance report (`GET /v1/assets/insurance-report?legalEntityId=`)
  lists the policies in force on `asOf` (default today) with sum insured,
  indexed replacement value, the NBV of every Open book and the
  under-insured flag; `expiringSoon` marks cover ending within 30 days, and
  `expiringWithinDays` / `insuranceType` filter it.
- A disposal dated before the end of a book's last depreciated period is
  AST-0508 (BR-37.6); the reversal set is not offered yet.
- A financial addition posts an AcquisitionAdj of its cost on the selected
  Open books (default: all) in the addition's currency (AC-37.12); a
  descriptive addition changes no balance and may precede acquisition.
- Insurance: indexed value = base value x index(valuation year) / index(base
  year), half-up to the minor unit (BR-37.10, AC-37.9). A missing valuation
  year uses the latest loaded year (AST-0531 warning); cover below 90% of the
  indexed value is under-insured (AST-0532 warning).
- Capex: submitting a Draft with a variant moves it to InApproval and routes it
  by the variant total (<= 50,000 PlantManager, <= 500,000 CFO, above Board,
  AC-37.11). The approval itself is the wave-4 workflow (not yet wired).
- Read duties: AssetView covers every GET (PRD 7.37.17) except leases; run,
  insurance (including the report), capex and count-session reads also accept
  AssetDepreciate, AssetInsuranceMaintain, CapexRequestCreate and AssetCount
  respectively.
- A disposal applies to every Open book of the asset (as SAP retirements post to
  all depreciation areas): each book records its own transaction with the
  disposed share of cost and accumulated depreciation, rounded once with the
  residual cents left on the asset (BR-37.7), and its own gain or loss against
  the same proceeds. 100% closes the asset and its books as Sold or Scrapped
  (BR-37.2). Scrap takes no proceeds.
- Straight-line life is in months; declining-balance life is in years.
  Conventions: None, FullMonth, MidMonth15 (half of the first and last month,
  the PRD worked example) for straight line; None and HalfYear for declining
  balance.
- A legal-entity header narrows every read through the restrictive RLS policy;
  creating in another entity is 403.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # store tests
PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app@... # also runs the RLS test as a non-bypass role
```

Store tests use `testkit.DB(t)` and skip without `PASSAGE_TEST_DATABASE_URL`.
They cover idempotent replay and key reuse, AC-37.5 on two books, the
subledger reconciliation (Σ transactions per book = the book's cost and
accumulated depreciation), the book limit, pagination and RLS; for slice 2 the
run lifecycle (refresh, stale If-Match, rerun rejection, sequential rule,
prospective charge after a revaluation, stale-book exception), the advisory
lock held by another session, impairment and its capped reversal, AC-37.12
additions, AC-37.9 insured values with fallback and under-insurance, and the
AC-37.11 capex routing; for slice 3 the remeasurement of the worked example
(payment change, scope change with a longer term, ROU shortfall to P&L,
replay, stale version, posted-period guard, schedule invariants), count
sessions (found / missing / unknown / repeats, other entities and disposed
assets not expected, pagination) and the insurance report (in force on the
date, AC-37.9 under-insurance, NBV per book, filters, pagination), all also
under RLS as the application role.
Domain tests are pure: table-driven, `pgregory.net/rapid` properties (life totals,
lease closing), and the worked-example fixtures.

## Dependencies

Only the platform module and what it already pulls in (chi, pgx/v5,
shopspring/decimal, google/uuid).

## Money and rounding

Amounts are `numeric(20,6)` with a currency. Period depreciation rounds half-up
to the currency minor unit and the last period is a plug, so the sum equals the
depreciable basis exactly (BR-37.1). Schedules render in the currency's minor
unit (JPY has none). Rates are decimal strings; no floats.

## Error codes

Problem details are `application/problem+json` with `code` and `type`
`https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| COM-0001 | 400 | Request validation failed (field errors in `errors`), `Idempotency-Key` missing, malformed `If-Match` or legal-entity header |
| AUTH-0001 | 401 | Missing or malformed tenant |
| AUTH-0003 | 403 | Missing duty, or legal entity outside the caller's scope |
| AST-0001 | 404 | Asset not found |
| AST-0002 | 409 | Asset number or barcode already exists in the legal entity |
| AST-0003 | 404 | Book not found on the asset |
| AST-0004 | 409 | Asset already has a book with this code |
| AST-0005 | 404 | Lease not found |
| AST-0006 | 412 / 428 | Version conflict on an asset, run, capex request or lease (stale or missing `If-Match`) |
| AST-0007 | 409 | Lease number already exists in the legal entity |
| AST-0010 | 422 | Depreciation profile invalid (life, method, convention, factor, salvage) |
| AST-0011 | 422 | Disposal invalid (percentage outside (0, 100], date before in-service) |
| AST-0012 | 422 | Lease invalid (term, frequency, payment, rate method), or a modification the lease does not allow (status, effective period, revised term) |
| AST-0013 | 422 | Too many books on the asset (`ASSET_MAX_BOOKS`) |
| AST-0014 | 422 | Adjustment invalid (zero, beyond the minor unit, before in-service, NBV or accumulated out of range, currency mismatch) |
| AST-0015 | 422 | Insurance policy invalid (coverage period, base year without an index value) |
| AST-0016 | 422 | Capex request status does not allow the change (BR-37.12) |
| AST-0018 | (run line) | The book changed between proposal and posting; propose again |
| AST-0019 | 404 | Depreciation run not found |
| AST-0020 | 422 | The asset already has 500 additions (FR-37.14) |
| AST-0021 | 404 | Capex request not found |
| AST-0022 | 404 | Variant not found on the capex request |
| AST-0023 | 409 | Capex request number already exists in the legal entity |
| AST-0024 | 404 | Index series not found |
| AST-0025 | 409 | The asset already has a policy with this number |
| AST-0026 | 404 | Count session not found |
| AST-0027 | 422 | A lease period from the effective period on is already posted |
| AST-0502 | 409 / run line | Depreciation for the book and period is already posted |
| AST-0503 | run line | Previous period is not depreciated (BR-37.3) |
| AST-0504 | 409 | Another depreciation run holds the book and period (BR-37.9) |
| AST-0508 | 422 | Disposal date precedes the last depreciated period (BR-37.6) |
| AST-0509 | 422 | Asset or book state does not allow the posting (BR-37.2) |
| AST-0512 | warning | Impairment reversal limited (FR-37.9) |
| AST-0515 | 422 | Lease IBR (or revised rate) outside (0, 50%] |
| AST-0516 | info | Lease remeasured; ROU adjusted by the amount (BR-37.8) |
| AST-0525 | warning | Scanned barcode is not in the register (FR-37.11) |
| AST-0531 | warning | Index series has no value for the valuation year; the latest year was used (BR-37.10) |
| AST-0532 | warning | Asset is under-insured: sum insured below 90% of the indexed value (FR-37.15) |
| idempotency_key_reuse | 422 | Idempotency-Key reused with a different request |

Other PRD message codes (AST-0501, 0507, 0511, 0520, 0535, 0541, 0542) belong to later slices; see NOT-IMPLEMENTED.md.

## Events

Written to the outbox in the same transaction as the change:
`dev.passage.assets.asset.acquired.v1` (one per book added),
`dev.passage.assets.asset.disposed.v1` (one per book disposed),
`dev.passage.assets.asset.adjusted.v1` (one per adjustment and per book a
financial addition posts to; not in the PRD event table),
`dev.passage.assets.depreciation.posted.v1` (one per posted run),
`dev.passage.assets.lease.recognized.v1`,
`dev.passage.assets.lease.remeasured.v1` (one per modification; not in the PRD
event table, added so the ledger sees every change of the liability and ROU).
See `events/asyncapi.yaml`.

## Known gaps

See NOT-IMPLEMENTED.md for everything in the ticket that is not in this slice,
with status and reason.
