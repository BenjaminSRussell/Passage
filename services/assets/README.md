# assets (Wave 37: Fixed Assets & Leases, slices 1 to 6)

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
against the register (FR-37.11), and the insurance report (7.37.14). Slice 4
adds the capex decisions (approve, reject, postpone, reopen, release, close)
with the decision trail and the `capexrequest.approved` event, and the
mid-quarter convention on declining-balance books with the on-demand BR-37.5
test. Slice 5 adds transfer and split of assets (FR-37.7, also to another
legal entity of the tenant) and group assets for pooled tax depreciation
(FR-37.16). The web module is
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
                       0004 lease modifications, rate method and count sessions,
                       0005 MidQuarter convention, capex creator and decision trail,
                       0006 transfers and splits, group assets and their members,
                       0008 the MACRS method
testdata/              worked-examples.json: PRD fixtures (AC-37.1, AC-37.2); dep-100, disposal-30
                       and lease-25 proof fixtures with their gen_*.py reference generators
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
  (`POST /v1/assets/capex-requests/{id}/submit`) and every capex decision
  require `If-Match`; they are state checks and take no Idempotency-Key. `PUT /v1/assets/index-series/{code}`
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
  Σ ROU depreciation = rouAsset + Σ rouAdjustment of the modifications not
  `superseded`. A modification that takes effect before an earlier recorded
  one replaces the schedule from its own period, so the earlier one is
  marked `superseded` and its ROU adjustment is left out of the new one's
  `rouBefore` (the ROU carrying amount). A period already posted
  to the ledger cannot be remeasured (AST-0027). The response carries the
  AST-0516 info message.
- Lease termination (slice 7, D4c): `POST /v1/leases/{id}/terminate`
  (LeaseMaintain; If-Match and Idempotency-Key; `afterPeriod`, optional
  `terminationPayment` and `reason`) ends an Active or Modified lease on the
  last day of period `afterPeriod` (1 up to the period before the last). The
  schedule after it is dropped; the liability at that point (the closing of
  `afterPeriod`) and the ROU carrying amount (the depreciation still to come,
  less the ROU adjustment of any modification that takes effect after the
  termination) are derecognized and `gainLoss = liability - ROU carrying -
  payment` (positive is a gain) is recorded on `lease_termination`; the lease
  becomes `Terminated` and `lease.terminated` is written to the outbox in the
  same transaction. A later period already posted to the ledger, or a ledger
  booking in flight, refuses it (AST-0027). The termination takes effect at
  the latest at the end of the payment period running today: a later
  `afterPeriod` is refused (AST-0036), because ending the lease at a later date
  agreed now is a change of term, booked as a modification with the shorter
  `termMonths`. There is no termination inside a payment period. `POST /v1/leases/gl-postings` books
  the termination journal after every period up to the termination: Dr
  liability, Dr ROU accumulated depreciation, Cr ROU asset at cost, Cr payment
  clearing, and the gain (Cr) or loss (Dr) on the remeasurement gain account.
- Lease ledger posting (slice 6, D4d): `POST /v1/leases/gl-postings`
  (AssetPostJournal; legalEntityId, ledgerId, postThrough, optional leaseId)
  books the recognition journal (Dr ROU asset, Cr lease liability, dated at
  commencement) and one journal per payment period that has ended by
  `postThrough`, dated on its last day (commencement + period x 12 /
  paymentsPerYear months - 1 day): Dr interest expense / Cr liability for the
  interest, Dr liability / Cr payment clearing for the payment, Dr depreciation
  expense / Cr ROU accumulated depreciation, and on the period a modification
  takes effect Dr or Cr ROU asset against the liability with the part beyond
  the ROU carrying amount credited to the remeasurement gain account. Periods
  go in order; a lease whose journal the ledger refuses stops there and is
  listed under `failed`, other leases go on. Each journal is stored as a
  pending booking (`asset_pending_booking`, book code LEASE) before the ledger
  is called and resent unchanged until it answers; a ledger refusal drops it,
  a lost answer keeps it, and a lease with a pending booking cannot be
  remeasured (AST-0027). The ledger entry lands on `lease.recognition_journal_id`
  and `lease_schedule_line.posted_journal_id` and `dev.passage.assets.lease.posted.v1`
  is written in the same transaction. `GET /v1/leases/{id}/gl-postings` shows
  which journals are booked. Accounts come from `PUT /v1/leases/posting-profile`
  (AssetConfigure, If-Match to replace; GET by LeaseMaintain or AssetConfigure);
  without one the placeholders apply and the booking is flagged
  `placeholderProfile`: ROU asset 1700, ROU accumulated depreciation 1790, lease
  liability 2400, interest expense 7200, depreciation expense 6820, payment
  clearing 2110, remeasurement gain 7120, termination gain / loss 7130, short-term and
  low-value lease expense 6830. Superseded modifications are not
  booked. A line whose remeasurement does not agree with the lease's live
  modifications is reported as AST-0610 and not booked (a data fault guard).
  Staging locks the leases (the lock a remeasurement takes), so a
  remeasurement cannot replace a period between reading it and staging its
  journal; a request overtaken by another that confirmed the same journal
  reports it booked with the same entry. Each journal is one per payment
  period: a lease paid quarterly, half-yearly or yearly books its interest and
  ROU depreciation at the end of each payment period, not monthly, and an
  advance payment is booked with its period's journal on the period's last
  day, not on its due date (see NOT-IMPLEMENTED.md D4d).
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
  AC-37.11). Decisions are one route each under
  `POST /v1/assets/capex-requests/{id}/`: `approve`, `reject` and `postpone`
  (InApproval only), `release` (Approved) and `close` (Released) need
  CapexRequestApprove; `reopen` (Postponed back to Draft, clearing the variant
  and routing) is the requester's and needs CapexRequestCreate. Reject and
  postpone need a `comment`. The user who created the request (`createdBy`)
  or is its `requesterId` may not approve, reject or postpone it (403
  AST-0028, PRD 7.37.17). Approve sets `approvedAmount` to the selected
  variant's total and writes `capexrequest.approved` in the same transaction.
  Every decision is recorded in `decisions` (who, when, from / to status,
  level, amount, comment). Routing a request to the approver of its level is
  the wave-4 workflow (not yet wired): any CapexRequestApprove holder decides.
- `GET /v1/assets/mid-quarter-test?legalEntityId=&bookCode=&year=` (AssetView
  or AssetDepreciate) runs BR-37.5 on demand: the current cost of the code's
  books placed in service in the (calendar) year by quarter, without books
  fully disposed of in that year; more than 40% in Q4 applies mid-quarter
  (AST-0511 warning) and the books whose convention disagrees are listed. It
  switches and posts nothing.
- Read duties: AssetView covers every GET (PRD 7.37.17) except leases; run,
  insurance (including the report), capex and count-session reads also accept
  AssetDepreciate, AssetInsuranceMaintain, CapexRequestCreate (or
  CapexRequestApprove) and AssetCount respectively.
- A disposal applies to every Open book of the asset (as SAP retirements post to
  all depreciation areas): each book records its own transaction with the
  disposed share of cost, accumulated depreciation and the impairment within
  it, rounded once with the
  residual cents left on the asset (BR-37.7), and its own gain or loss against
  the same proceeds. 100% closes the asset and its books as Sold or Scrapped
  (BR-37.2). Scrap takes no proceeds.
- Straight-line life is in months; declining-balance and MACRS life is in years.
  `MACRS` applies the IRS Pub. 946 GDS percentage tables (A-1 HalfYear, A-2
  to A-5 MidQuarter by in-service quarter) to the cost for a 3, 5, 7, 10, 15
  or 20-year recovery period, rounded half-up per year with the last year
  taking the remainder; salvage must be 0 and the factor is ignored.
  Conventions: None, FullMonth, MidMonth15 (half of the first and last month,
  the PRD worked example) for straight line; None, HalfYear and MidQuarter
  for declining balance. MidQuarter charges 10.5, 7.5, 4.5 or 1.5 months of
  the first year by in-service quarter (IRS Pub. 946 Tables A-2 to A-5, the
  5-year rows checked to the published rates).
- Transfer and split (`POST /v1/assets/{id}/transfer`, AssetMaintain,
  Idempotency-Key and If-Match with the asset ETag): every Open book of an
  Open asset moves. `kind: transfer` has one target taking 100%, optionally in
  another legal entity of the tenant (the caller needs that entity in scope);
  `kind: split` has 2 to 50 targets in the source entity whose percentages
  (two decimals) total 100.00% (AST-0507). Each book's cost, accumulated
  depreciation, impairment share, salvage and schedule basis are allocated by
  percentage, rounded half-up with the residual cents on the last target
  (BR-37.7), keeping 0 <= impairment <= accumulated <= cost on every target.
  Target books keep the source profile, in-service date and last depreciated
  period, so the next run continues the schedule. Every source book gets a
  Transfer / Split transaction that empties it and ends Transferred; the
  source asset ends Transferred (BR-37.2). The target books open with a
  Transfer / Split transaction (`sourceType` transfer / split, `sourceId` the
  transfer), so Σ transactions = projections holds on both sides. A transfer
  dated before the end of a book's last depreciated period is AST-0508 (as for
  a disposal, BR-37.6). One `asset.transferred` or `asset.split` event is
  written in the same transaction. `GET /v1/assets/{id}/transfers` lists the
  transfers an asset took part in as source or target.
- Group assets (`POST /v1/assets/group-assets`, AssetConfigure; `GET` list and
  read with AssetView or AssetConfigure): a group pools the books with one
  book code of member assets of one legal entity, class and tax year, in one
  currency (AST-0031); a book belongs to one group asset (BR-37.11, 409
  AST-0535). The group has its own profile and depreciates from 1 January of
  the tax year; its basis is the members' current cost on the book, read live,
  and the read returns the projected yearly schedule (AC-37.10: 40 x 600.00
  over 60 months is 4,800.00 a year). Member books take no own charge: a run
  lists them as AST-0035 exceptions with no amount. The group's own run line
  (one charge on the basis with a drill to the members) is not built yet.
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
date, AC-37.9 under-insurance, NBV per book, filters, pagination); for slice 4
the capex decisions (own-request refusal, stale version, illegal moves, the
approved amount and the outbox payload of `capexrequest.approved`, reopen and
resubmit, the trail) and the mid-quarter test (a MidQuarter book stored,
quarters, other years / codes / entities ignored, same-year disposal left out,
books to review, two currencies refused); for slice 5 a 60/40 split of a
depreciated, impaired asset with two books (allocation, impairment share,
Transferred source, reconciliation on every book, replay and key reuse, the
next run continuing on the targets), a transfer to another legal entity with
its guards, and AC-37.10 on 40 Sammelposten members (basis, yearly schedule,
duplicate membership, class / year / book checks, members as AST-0035 run
exceptions, live basis after a partial sale), all also under RLS as the
application role.
Domain tests are pure: table-driven, `pgregory.net/rapid` properties (life totals,
lease closing), the worked-example fixtures and the Wave 37 proof fixtures:
`dep-100` (100 books against the Pub. 946 tables and straight-line edge cases),
`disposal-30` (successive partial disposals) and `lease-25` (IFRS 16
schedules), each computed by an independent `testdata/gen_*.py` (regenerate
with `python3 testdata/gen_dep100.py > testdata/dep-100.json` and likewise).
The store test `TestStore_PartialDisposalsPostGainLossOnEveryBook` posts a
hand-derived disposal chain on an IFRS and a MACRS book.

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
| AUTH-0001 | 401 | Missing or malformed tenant; a capex decision without an authenticated user |
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
| AST-0027 | 422 | A lease period from the effective period on is already posted, or a ledger booking of the lease awaits the ledger's answer |
| AST-0028 | 403 | The user created or requested the capex request and may not approve, reject or postpone it |
| AST-0029 | 422 | The mid-quarter test covers books in more than one currency |
| AST-0030 | 422 | Transfer or split invalid (a transfer with other than one target, a split with fewer than 2 or more than 50, a split into another legal entity, a date before in-service, a zero target quantity) |
| AST-0031 | 422 | A member does not fit the group asset (other legal entity, class, tax year or currency, no book with the group's code, book not Open, listed twice) |
| AST-0032 | 404 | Group asset not found |
| AST-0033 | 409 | Group code already exists in the legal entity |
| AST-0035 | run line | The book is depreciated at its group asset and takes no own charge (FR-37.16) |
| AST-0502 | 409 / run line | Depreciation for the book and period is already posted |
| AST-0503 | run line | Previous period is not depreciated (BR-37.3) |
| AST-0504 | 409 | Another depreciation run holds the book and period (BR-37.9) |
| AST-0507 | 422 | Split percentages do not total 100.00% (or a share is not above 0 with at most two decimals) |
| AST-0508 | 422 | Disposal or transfer date precedes the end of the last depreciated period (BR-37.6) |
| AST-0509 | 422 | Asset or book state does not allow the posting (BR-37.2) |
| AST-0511 | warning | Q4 placed-in-service share exceeds 40%; mid-quarter applies (BR-37.5) |
| AST-0512 | warning | Impairment reversal limited (FR-37.9) |
| AST-0515 | 422 | Lease IBR (or revised rate) outside (0, 50%] |
| AST-0516 | info | Lease remeasured; ROU adjusted by the amount (BR-37.8) |
| AST-0525 | warning | Scanned barcode is not in the register (FR-37.11) |
| AST-0531 | warning | Index series has no value for the valuation year; the latest year was used (BR-37.10) |
| AST-0532 | warning | Asset is under-insured: sum insured below 90% of the indexed value (FR-37.15) |
| AST-0535 | 409 | The asset's book already belongs to a group asset (BR-37.11) |
| AST-0610 | n/a | A lease schedule line's remeasurement does not agree with the live modifications of its period (a data fault, not expected); its journal is not booked (listed under `failed`) |
| idempotency_key_reuse | 422 | Idempotency-Key reused with a different request |

Other PRD message codes (AST-0501, 0520, 0541, 0542) belong to later slices; see NOT-IMPLEMENTED.md.

## Events

Written to the outbox in the same transaction as the change:
`dev.passage.assets.asset.acquired.v1` (one per book added),
`dev.passage.assets.asset.disposed.v1` (one per book disposed),
`dev.passage.assets.asset.adjusted.v1` (one per adjustment and per book a
financial addition posts to; not in the PRD event table),
`dev.passage.assets.depreciation.posted.v1` (one per posted run),
`dev.passage.assets.lease.recognized.v1`,
`dev.passage.assets.lease.remeasured.v1` (one per modification; not in the PRD
event table, added so the ledger sees every change of the liability and ROU),
`dev.passage.assets.capexrequest.approved.v1` (one per approval, with the
approved variant's amounts per year, for budgeting and the forecast).
See `events/asyncapi.yaml`.

## Known gaps

See NOT-IMPLEMENTED.md for everything in the ticket that is not in this slice,
with status and reason.

### Short-term and low-value lease elections (D4c, slice 8)

`POST /v1/leases` takes `election` (`None`, `ShortTerm`, `LowValue`; IFRS 16.5-8),
recorded per lease. A short-term lease has a term of 12 months or less at
commencement and no purchase option (`purchaseOption`, IFRS 16 Appendix A). The
standard makes the short-term election per class of underlying asset (IFRS 16.8);
the lease carries no asset class, so the accountant applies the class policy to
each lease. A low-value lease states `underlyingAssetValue`, the value of the
underlying asset when new whatever its age (IFRS 16.B3-B5), in
`underlyingAssetCurrency`: USD or EUR, at most 5,000 (the PRD default of
`lease.lowValueThreshold`, after IFRS 16.BC100's USD 5,000 or less). The value
currency defaults to the lease currency; the service holds no exchange rates, so a
lease in another currency names USD or EUR and the accountant converts at the
assessment date. A lease under an election is not recognized: liability and ROU
asset are 0, its schedule lines carry only the payment, and
`POST /v1/leases/gl-postings` books one journal per payment period (Dr the exempt
lease expense account, Cr payment clearing) with no recognition journal. A lease
has one level payment amount and an exempt lease cannot be modified, so each
period's payment is the straight-line expense of IFRS 16.6. A refused election,
or a modification or termination of such a lease, is AST-0037. The termination
journal now books its gain or loss on the profile's own
`terminationGainLossAccount` (it used the remeasurement gain account); both new
profile accounts are optional on PUT and default to the remeasurement gain and
depreciation expense accounts.
