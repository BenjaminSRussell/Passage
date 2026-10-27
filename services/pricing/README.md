# pricing (wave 26, PSG-26)

Deterministic pricing engine for Passage: resolves net prices and discounts for a
customer / product / date / quantity / currency tuple from condition records, and returns
the ordered trace of conditions that produced the price. Ticket PSG-26, PRD §5.26.

## Status (slices 1-4)

Built in slice 4:

- **Trade promotion funds** (FR-26.19, BR-26.15): funds with type, currency, customer scope, owner,
  budget per period (YYYY-MM) and `allowOverspend`. `:attribute` charges usage (campaign accrual,
  rebate accrual, claim, deduction) to a period under the fund row lock: 422 PRC-0012 when usage
  would exceed the period budget without overspend, 409 PRC-0011 when the fund is closed. Each
  attribution writes `fundusage.recorded` in the same transaction; a repeated `Idempotency-Key`
  replays the first response without adding usage. `:revise` changes the budget and overspend flag
  at the body `version` (the budget must still cover every period's usage without overspend);
  `:close` freezes usage and `:reopen` lifts it (both versioned, `fund.closed` / `fund.reopened`).
  `GET /funds/{id}/usage` is the Fund usage inquiry: usage per period and source plus budget, used
  and remaining per period.
- **Promotion planning** (FR-26.20): a plan names the merchandising event category and type,
  customer scope, optional fund (same legal entity and currency, open), one product, dates, the 13
  weekly units before the promotion, forecast lift % and planned spend. The response derives the
  baseline (13-week average), forecast units (baseline × started weeks × (1 + lift %)) and, once
  `:actuals` records the units, weeks and spend in the window, the effectiveness
  (actual weekly average − baseline) / baseline in percent.
- Migration `0004_funds_promotions.sql` (trade_fund, trade_fund_usage, promotion_plan; RLS and
  legal-entity scope on each).
- **Web**: fund list, create and detail (attribute usage, revise budget, close and reopen, usage by
  period and source); promotion list, create and detail (baseline, forecast, actuals and
  effectiveness).

Built in slice 3:

- **Commission transactions and posting** (FR-26.12, FR-26.18, BR-26.12, AC-26.12): sales groups
  with reps and share percentages (summing to 100, kept in entered order so the first rep takes the
  rounding residual), commission rules per sales group and customer/item commission group (one per
  combination) resolved specific to general (PRC-0013 when none matches), REVENUE or MARGIN basis
  (MARGIN needs the line's `costSnapshot`). `POST /commission/postings` stands in for the
  invoice.posted consumer: one transaction per rep (Dr 6200 / Cr 2170) with
  `commission.calculated` in the same transaction; a redelivered line returns the stored rows. A
  credit note line reverses with the original line's basis, rate and shares, so a full credit nets
  every rep to zero even after the rule changed; credits are capped at the invoiced net (PRC-0037).
  Commission inquiry by rep, sales group, month, customer and invoice line. Migration
  `0003_commission_retro.sql` (sales_group, sales_group_rep, commission_rule,
  commission_transaction; RLS on each).
- **Retroactive rebate recompute** (FR-26.23, BR-26.7, BR-26.14, AC-26.14): an agreement is active
  from its creation day; when its `validFrom` is earlier, `POST /rebates/agreements/{id}:retro-recompute`
  replays the invoices posted between `validFrom` and the activation (the caller supplies them until
  the w29/w30 invoice ledger exists), adds the lines not accrued before to their period's basis, and
  posts the delta per period with `retroactive=true` (one accrual row and one `rebateaccrual.posted`
  event per period). Posted invoices are never re-priced; the `Idempotency-Key` names the run, and a
  second run over the same invoices posts nothing. The response carries the PRC-0006 notice.
- **Web**: sales group list, create (reps and shares) and detail (rules and adding a rule), the
  commission transactions inquiry, and the retroactive recompute form on the rebate agreement detail
  (the accruals grid marks retroactive rows).

Built in slice 2:

- **Multiline discounts and surcharges** (FR-26.5, FR-26.6, BR-26.4): `MULTILINE_DISC` records
  (keyed by product group or all products) match on the summed quantity of the lines sharing a
  product group and unit; `SURCHARGE` records (order level) apply when the subtotal is below the
  small-order threshold (250 USD, 200 EUR) and are reported as `surcharge` as well as added to
  `orderNet`. Migration `0002_journals_rebates.sql` widens the condition type check.
- **Price journals** (FR-26.9, AC-26.7, AC-26.10): `internal/domain/journal.go` validates a journal
  as a whole (rules, stale versions, overlaps with active records and with other lines, one line per
  record) and builds the reversal; `internal/store/journal.go` posts it in one transaction under the
  legal entity's journal lock (activations take the same lock in shared mode), writes one
  activated/withdrawn event per record plus `journal.posted`, and reverses into a new journal.
  Post and reverse replay on a repeated `Idempotency-Key`.
- **Rebate agreements and accruals** (FR-26.11, BR-26.8, AC-26.8): tiered agreements by customer or
  customer group; `domain.Accrue` moves a period to its target at the new cumulative basis and books
  the difference on the invoice line, so a tier crossing recomputes the period. The store serialises
  accruals per (agreement, period), ignores a redelivered invoice line and writes
  `rebateaccrual.posted` (Dr 4150 / Cr 2150 by default) for the ledger.
- **Web**: price journal list, create and detail (validate, post, reverse, before/after per line);
  rebate agreement list, create (tiers) and detail (accruals by period); journals-to-post tile.

Built in slice 1:

- **Condition records** (FR-26.2): create (DRAFT), read, list, activate and withdraw, with
  the overlap check (BR-26.6, 409 PRC-0002 naming the conflicting id) and an outbox event in
  the same transaction. Create, activate and withdraw honour `Idempotency-Key` (a retry
  replays the first response); activate and withdraw check the body `version` (412).
  Store: `internal/store`, migration `0001_init.sql` with RLS (tenant and legal-entity scope).
- **Resolve with explain** (FR-26.3, FR-26.4, FR-26.5 for LINE_DISC and TOTAL_DISC, FR-26.7 unit
  rounding): `internal/domain/resolve.go`. Access sequence for PRICE (BR-26.1 steps 1–6), with
  `search_next` and `exclusive`. Explain trace is content-addressed (sha256).
- **Simulation** (FR-26.13, first half): `POST /v1/pricing/simulate` prices inline records with
  no storage.
- **Pure calculation helpers** that later slices wire up: `SmartRound` (FR-26.7 journal bands),
  `RebateAccrual` (FR-26.11 and FR-26.15 tier math), `CommissionAmount` and `SplitCommission`
  (FR-26.18, BR-26.12 split and residual cent), `FreeGoods` (FR-26.22, BR-26.13).
- **Fixtures**: `testdata/pricing-scenarios.json` (19 hand-worked cases covering AC-26.1, 26.2,
  26.3, 26.5, BR-26.1, BR-26.3, BR-26.10 and PRC-0001), run by `internal/domain/scenarios_test.go`.
  `property_test.go` resolves 10,000 seeded random requests and checks byte-identical output (AC2).
- **Contract tests**: `internal/api/contract_test.go` (router = OpenAPI paths) and
  `internal/events/events_test.go` (event types and payload fields = AsyncAPI).
- **Web module**: `apps/web/src/modules/pricing` (workspace tiles, trade agreements grid,
  create/copy form, detail with activate and withdraw, simulation / explain), all on the API.
- **Chart**: `charts/pricing` (Deployment, Service, HPA 70% CPU, ServiceMonitor toggle).

Not yet built: see `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/pricing/            entrypoint (dbx.Connect -> dbx.Migrate -> httpx.NewRouter -> Serve)
internal/domain/        pure engine: types, resolve, calc, journal validation and reversal, rebate tiers and accrual
internal/store/         Postgres repository (pgx, tenant-scoped transactions, outbox)
internal/api/           HTTP handlers and validation
internal/events/        event type constants and payloads
migrations/             0001_init.sql (condition_record, outbox, idempotency_keys), 0002 (journals, rebates), 0003 (commission, retro flag); all with RLS
api/openapi.yaml        OpenAPI 3.1 for every endpoint
events/asyncapi.yaml    AsyncAPI 3 for every published event
charts/pricing/         Helm chart
testdata/               fixtures
```

## Endpoints (served under `/v1/pricing`; the browser uses `/api/v1/pricing` via the BFF)

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/pricing/resolve?explain=true` | pricing.view | up to `PRICING_MAX_LINES` (500) lines; the PRD's PricingResolve duty maps here until w27 |
| POST | `/v1/pricing/simulate?explain=true` | pricing.view | inline records, nothing stored |
| GET | `/v1/pricing/condition-records?legalEntityId=&status=&type=&customer=&product=&asOf=&limit=` | pricing.view | at most 100 rows, ordered by id |
| POST | `/v1/pricing/condition-records` | pricing.maintain | `Idempotency-Key` honoured; creates DRAFT |
| GET | `/v1/pricing/condition-records/{id}` | pricing.view | |
| POST | `/v1/pricing/condition-records/{id}:activate` | pricing.maintain | body `{version}`; overlap check; `Idempotency-Key` honoured |
| POST | `/v1/pricing/condition-records/{id}:withdraw` | pricing.maintain | body `{version, reason}`; `Idempotency-Key` honoured |
| GET | `/v1/pricing/journals?legalEntityId=&status=&limit=` | pricing.view | price journal log, newest first, no lines |
| POST | `/v1/pricing/journals` | pricing.journal.post | up to 100,000 CREATE / UPDATE / DELETE lines; creates OPEN; `Idempotency-Key` honoured |
| GET | `/v1/pricing/journals/{id}` | pricing.view | with up to 1,000 lines (before / after) and every line error |
| POST | `/v1/pricing/journals/{id}:validate` | pricing.journal.post | VALIDATED when clean, else OPEN with line errors |
| POST | `/v1/pricing/journals/{id}:post` | pricing.journal.post | all lines or none (PRC-0005); `Idempotency-Key` honoured |
| POST | `/v1/pricing/journals/{id}:reverse` | pricing.journal.post | 201 with the reversing journal; `Idempotency-Key` honoured |
| GET | `/v1/pricing/rebates/agreements?legalEntityId=` | pricing.rebate.view | |
| POST | `/v1/pricing/rebates/agreements` | pricing.rebate.maintain | `Idempotency-Key` honoured |
| GET | `/v1/pricing/rebates/agreements/{id}` | pricing.rebate.view | |
| GET | `/v1/pricing/rebates/accruals?agreementId=&period=` | pricing.rebate.view | period such as 2027-03, 2027-Q1 or 2027 |
| POST | `/v1/pricing/rebates/accruals` | pricing.rebate.maintain | one posted invoice line; 201 new, 200 already accrued (stand-in for the invoice.posted consumer) |
| POST | `/v1/pricing/rebates/agreements/{id}:retro-recompute` | pricing.rebate.maintain | `Idempotency-Key` required (names the run); body `{lines}` of posted invoices before the activation |
| GET | `/v1/pricing/commission/sales-groups?legalEntityId=` | pricing.commission.view | |
| POST | `/v1/pricing/commission/sales-groups` | pricing.commission.maintain | `Idempotency-Key` honoured; reps with shares summing to 100 |
| GET | `/v1/pricing/commission/sales-groups/{id}` | pricing.commission.view | |
| GET | `/v1/pricing/commission/rules?salesGroupId=` | pricing.commission.view | most specific first |
| POST | `/v1/pricing/commission/rules` | pricing.commission.maintain | `Idempotency-Key` honoured; 409 PRC-0038 for a second rule on the same groups |
| POST | `/v1/pricing/commission/postings` | pricing.commission.maintain | one posted invoice or credit note line; 201 new, 200 already posted (stand-in for the invoice.posted consumer) |
| GET | `/v1/pricing/commission/transactions?legalEntityId=&salesGroupId=&repId=&customerId=&invoiceLineId=&period=` | pricing.commission.view | commission inquiry; period is a month |
| GET | `/v1/pricing/funds?legalEntityId=` | pricing.view or pricing.fund.maintain | |
| POST | `/v1/pricing/funds` | pricing.fund.maintain | `Idempotency-Key` honoured; open at version 1 |
| GET | `/v1/pricing/funds/{id}` | pricing.view or pricing.fund.maintain | |
| POST | `/v1/pricing/funds/{id}:attribute` | pricing.fund.maintain | body `{period, source, reference, amount}`; PRC-0011 / PRC-0012; `Idempotency-Key` honoured |
| POST | `/v1/pricing/funds/{id}:revise` | pricing.fund.maintain | body `{version, budgetPerPeriod, allowOverspend}`; `Idempotency-Key` honoured |
| POST | `/v1/pricing/funds/{id}:close` | pricing.fund.maintain | body `{version}`; `Idempotency-Key` honoured |
| POST | `/v1/pricing/funds/{id}:reopen` | pricing.fund.maintain | body `{version}`; `Idempotency-Key` honoured |
| GET | `/v1/pricing/funds/{id}/usage` | pricing.view or pricing.fund.maintain | Fund usage inquiry |
| GET | `/v1/pricing/promotions?legalEntityId=` | pricing.view or pricing.fund.maintain | |
| POST | `/v1/pricing/promotions` | pricing.fund.maintain | `Idempotency-Key` honoured; fund must be open and match (PRC-0011, COM-0001) |
| GET | `/v1/pricing/promotions/{id}` | pricing.view or pricing.fund.maintain | |
| POST | `/v1/pricing/promotions/{id}:actuals` | pricing.fund.maintain | body `{units, weeks, spend}`; overwrites; stand-in for the nightly actuals job |
| GET | `/healthz`, `/readyz`, `/metrics` | none | `/metrics` on the admin port |

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| PRC-0001 | 200 (line) / 422 | No price for a product on the pricing date (reported on the line) |
| PRC-0002 | 409 | Overlapping ACTIVE record for the same key, currency and unit; names the id |
| PRC-0003 | 422 | Negative amount (BR-26.10) |
| PRC-0005 | 422 | Journal has validation errors at post or reverse; nothing was posted (names up to five lines) |
| PRC-0009 | 422 | Request exceeds `PRICING_MAX_LINES` |
| PRC-0030 | 404 | Condition record not found (store) |
| PRC-0031 | 409 | Invalid state transition (store) |
| PRC-0034 | 404 | Price journal not found |
| PRC-0006 | 200 (notice) | Retroactive recompute re-accrued posted invoices (count in the message) |
| PRC-0011 | 409 | Fund is closed: attribution, revision or a promotion on it is refused (reopen first) |
| PRC-0012 | 422 | Fund budget exceeded by the named amount without overspend (attribution or a budget cut) |
| PRC-0013 | 422 | No commission rule at any level for the sales group |
| PRC-0035 | 404 | Rebate agreement not found |
| PRC-0036 | 404 | Sales group not found |
| PRC-0037 | 422 | Credit note cannot be applied: no commission on the reversed line, another sales group or currency, or credits above the invoiced net |
| PRC-0038 | 409 | A commission rule already exists for the sales group, customer group and item group |
| PRC-0039 | 404 | Trade fund not found |
| PRC-0040 | 404 | Promotion plan not found |
| conflict_version | 412 | Body version does not match the stored version |
| idempotency_key_reuse | 422 | Same `Idempotency-Key` with a different body, or an invoice line accrued again with a different date, value or quantity |
| COM-0001 | 422 | Validation failure; `errors` maps each field to its message (percentages outside 0–100, a product-keyed TOTAL_DISC, bad dates, ...) |
| AUTH-0001 | 401 | Missing or malformed tenant |
| AUTH-0003 | 403 | Missing duty (kernel `authz`) |

## Events

Written to `outbox` in the same transaction as the change (see `events/asyncapi.yaml`):

- `dev.passage.pricing.conditionrecord.activated.v1` (activation and journal post; `journal_id` set by journals)
- `dev.passage.pricing.conditionrecord.withdrawn.v1`
- `dev.passage.pricing.journal.posted.v1` (post and reverse)
- `dev.passage.pricing.rebateaccrual.posted.v1` (`retroactive=true` on a retroactive recompute delta)
- `dev.passage.pricing.commission.calculated.v1` (one per rep per invoice line; rep id only, never a name)
- `dev.passage.pricing.fund.closed.v1`, `dev.passage.pricing.fund.reopened.v1`
- `dev.passage.pricing.fundusage.recorded.v1` (one per attribution, with the period usage and budget after it)

The entity segments are `conditionrecord` and `rebateaccrual` (as in `internal/events`); the PRD
writes `condition-record` and `rebate-accrual`. Naming is a handoff to the kernel owner (see NOT-IMPLEMENTED.md).

## Configuration

12-factor env vars, validated at startup: `PASSAGE_ENV`, `SERVICE_NAME`, `HTTP_ADDR`,
`ADMIN_ADDR`, `DATABASE_URL`, `PRICING_MAX_LINES` (500), `PRICING_EXPLAIN_MAX_BYTES` (65536).
`PRICING_CACHE_TTL` is set by the chart but is not read yet (no cache in this slice).

## Run and test

```
cd services/pricing
go test ./...                      # runs without Postgres; store/API DB tests skip
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # runs store and API DB tests
go vet ./...
```

Domain tests are pure table-driven unit tests plus property tests with `pgregory.net/rapid`.
Dependencies beyond the platform module: `github.com/shopspring/decimal` (money arithmetic,
already in the platform's dependency set) and `pgregory.net/rapid` (test only).

## Wave reference

PRD §5.26 (functional requirements FR-26.1–26.23, rules BR-26.1–26.15, AC-26.1–26.14).
Depends on waves 6 (units), 7 (customers and groups), 8 (products and variants), 11
(journal), 12 (currencies), 25 (phase order). Consumed by 27 (sales orders, free goods lines),
19 (AP claims), 30 (rebate settlement), 43 (payroll commission export).
