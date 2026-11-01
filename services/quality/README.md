# quality

Passage quality management service. Wave 33 (ticket PSG-33), slices 1 to 5.

## What it does (slice 1)

- **Sampling (FR-33.2, BR-33.1):** fixed, percent and ANSI/ASQ Z1.4 AQL sizing. The
  sizing function has Table I for every inspection level (S-1 to S-4, I to III) and
  Tables II-A, II-B and II-C (normal, tightened, reduced) for every AQL column from
  0.010 to 1000 (`internal/domain/z14.go`, checked cell by cell against
  `testdata/z14-tables.json`); a plan stored on an association or manual order is
  level II, NORMAL, AQL 1.0, 2.5 or 4.0. Sample quantity is capped at lot quantity.
  Switching (BR-33.2) is a pure state machine per (item, vendor, test group).
- **Quality associations (FR-33.3, BR-33.3):** specificity score
  `4·item + 2·party + 1·site` (item group counts as item weight 3); highest score wins,
  ties resolved by lowest `sequence`.
- **Tests and test groups (FR-33.1):** variable tests (min/max limits, 0–6 decimals,
  inclusive comparison after half-up rounding) and attribute tests (passing outcomes);
  test group versions are immutable.
- **Instruments (BR-33.8):** `due_at = last_calibrated_at + interval`; results from an
  overdue instrument are refused with `QM-0102`. Lists show `calibrationStatus`
  (`DUE_SOON` within 14 days, the QM-0103 horizon); `:calibrate` records a calibration.
- **Quality orders and results (FR-33.4, BR-33.4):** order state machine, batch result
  recording (up to 1,000 results per request), retest numbering (retest n needs n-1);
  a pending order takes only retests. One order per source line and trigger (BR-33.3).
  The order view carries its test group version, the evaluation (defective samples
  against Ac, required tests still missing) and, once closed, its decision.
- **Variable evaluation (BR-33.6):** half-up rounding to the declared decimals, half
  away from zero (`-1.25` → `-1.3`), then inclusive limits.
- **Usage decision (FR-33.6, BR-33.7):** quantities of the decision lines must sum to
  lot quantity; accept, reject, accept-with-deviation (reason required), rework,
  return-to-vendor, scrap.
- **Events** written to the outbox in the same transaction as the state change.

## What it does (slice 2)

- **Compliance lists (FR-33.13):** restricted or reporting lists with regions and a
  threshold (% w/w) per CAS number (check digit validated); BLOCK or WARN
  (`compliance_block_mode`). Each save of a code is its next immutable version; the
  highest version is the one evaluated.
- **Item compositions:** versioned declarations of substances (mass fraction w/w) and
  component items (mass fraction of the parent). The BOM roll-up uses these declared
  parts until the wave-9 BOM feeds them (NOT-IMPLEMENTED.md).
- **Evaluation (BR-33.11):** `c_s = declared + Σ mass_fraction × c_s(component)`,
  recursively (cycles QM-0165, more than 20 levels or 500 items QM-0162); a substance is
  a hit when strictly above its threshold. Reporting hits are QM-0121 warnings;
  restricted hits are QM-0122, an error under BLOCK and a warning under WARN. Results are
  versioned per (item, list version, region): a new version, and its
  `complianceresult.recorded` event, only when the outcome changes. Components without a
  composition are reported as `undeclaredItems`; an item without one cannot be evaluated
  (QM-0164).
- **Stability studies (FR-33.16, BR-33.14):** item, batch, protocol, storage condition,
  a test group per pull point, pull months 0 to 120. Pull date = start + months, same day
  of month clamped to month end; the quality order is due `stability_lead_days` (default
  7) before. Studies start PLANNED; `stabilitystudy.created` starts the scheduling
  workflow (a later slice).
- **SPC (FR-33.11):** X-bar/R for variable tests (subgroups of 2 to 25, ASTM constants)
  and p (with np at constant size) for attribute tests, from first-attempt results of the
  item's newest orders; each order is one subgroup, limits from the last 25, Western
  Electric rules 1 to 4. Chart statistics are numbers, not money; the stored results stay
  decimal.

## What it does (slice 3)

- **Nonconformances (FR-33.7):** source (customer, vendor, internal, production,
  service), problem type code, severity MINOR/MAJOR/CRITICAL, item, dims, quantity and
  unit, links to the quality order, vendor, customer, production order and quarantine
  order. Lifecycle NEW → APPROVED → IN_CORRECTION → CLOSED, or NEW → REFUSED (with a
  reason). An NCR naming a quality order takes its item, dims and unit, and cannot exceed
  its lot; on a closed order it is the order's amendment (BR-33.4).
- **NCR from a usage decision (FR-33.6, `auto_ncr_on_reject`):** a decision with a
  non-accepting quantity (reject, rework, return to vendor, scrap) raises a MAJOR NCR for
  that quantity in the decision's transaction; the decision carries `nonconformanceId`.
- **8D correction (FR-33.8, BR-33.9):** starting it moves the NCR to IN_CORRECTION and
  opens D1; D1 to D7 complete in order with notes; D3 is due two business days and D5
  30 days after start; completing D7 schedules the effectiveness check
  `effectivenessCheckDays` later (30 to 365, default 90). The check is refused before it
  is due (QM-0174) and from the user who completed D7 (QM-0175). EFFECTIVE completes D8,
  closes the correction and the NCR and writes `capa.closed`; NOT_EFFECTIVE reopens the
  8D at D4 with a new D5 due date. Overdue steps are computed on read. The NCR's version
  (ETag) covers its correction. The Temporal timers and escalations are a handoff
  (NOT-IMPLEMENTED.md).

## What it does (slice 4)

- **Incoming vendor certificates (FR-33.15, BR-33.13):** a quality association demands
  NONE, COA, COC or EN10204_31 (`certificateRequired`); a triggered order copies it, a
  MANUAL order takes it from the request. A receipt carries a document reference and
  values per test of the order's test group; a required test without a value, or any
  value outside its limits, is a deviation (a COC without values is a conformance
  statement). An out-of-specification receipt is RECEIVED_NOK and raises a MAJOR VENDOR
  NCR for the lot (`CERTIFICATE_NOK`) in the same transaction. A waiver needs
  QM_USAGE_DECISION and a reason. The latest receipt of the required type, or a waiver,
  sets `certificateStatus` (MISSING, RECEIVED_OK, RECEIVED_NOK, WAIVED, NOT_REQUIRED) on
  the order view. A decision that releases quantity (accept or accept with deviation)
  fails with QM-0125 unless the status is RECEIVED_OK or WAIVED; the store repeats the
  check under the order's row lock, and certificates lock the order too, so a concurrent
  receipt and decision serialise.
- **Physical samples (FR-33.20):** PRIMARY, POOLED (from PRIMARY samples of the same
  order) and RESERVE (retention) samples with quantity in the order's unit, container,
  storage location and condition. A retention sample expires `retentionYears` (default
  1, 0 to 10) after its batch expiry (21 CFR 211.170); the status is derived on read and
  is DUE_FOR_DISPOSAL from the expiry day (AC-33.18). Drawn samples (PRIMARY and
  RESERVE) may not exceed the lot.

## What it does (slice 5)

- **Stability study lifecycle (FR-33.16):** `:transition` moves a study PLANNED → ACTIVE →
  COMPLETED, or to TERMINATED from either open state, under `If-Match` (QM-0184 for a move
  the lifecycle does not allow). TERMINATED needs a reason and cancels the pulls still
  PLANNED (status CANCELLED); ORDERED pulls keep their quality orders. The study records
  who changed it, when and why, and `stabilitystudy.statuschanged` names the cancelled
  pulls so the scheduling workflow stops.
- **Source inspection at receipt (FR-33.19, BR-33.16, AC-33.14):** a source inspection
  is a SOURCE_INSPECTION quality order whose `sourceRef.id` is the PO line and
  `dims.batch` the batch (`sourceRef.line` tells several batches of one PO line apart).
  Receiving posts each receipt line to `/receipt-checks`: what closed source inspections
  of the PO line and batch released (accept plus accept with deviation), less what
  earlier receipts already used, covers the receipt up to its quantity. A fully covered
  receipt skips receiving inspection (`skip`, QM-0128); otherwise receiving creates a
  PURCHASE_RECEIPT order for `normalQty`. Checks of one PO line and batch serialise on an
  advisory lock; a receipt line is checked once (the same values answer the stored
  check, other values are QM-0185).
- **Certificates of analysis (FR-33.9, BR-33.10):** `coa:generate` issues an immutable
  revision for a closed order whose decision released quantity (QM-0186 otherwise),
  for the batch or one shipment (each its own revision chain). It prints the results the
  decision was taken on (recorded up to the decision, latest retest per test and
  sample) with the test group limits, the accepted and deviation quantities with the
  deviation reasons, and the decision user as signer; the caller is recorded as issuer.
  A later revision supersedes the previous one and needs a reason (QM-0187). The order's
  row lock serialises revisions; `coa.issued` goes out in the same transaction.
- **Calibration due list:** `GET /instruments?dueWithinDays=n` lists the instruments due
  within n days, overdue ones included (section 6.33.14).

Code layout: `internal/domain` (pure rules and unit/property tests),
`internal/store` (pgx, RLS-scoped transactions, outbox), `internal/api` (HTTP),
`internal/events` (event names and payloads), `migrations/` (`0001_init.sql`,
`0002_compliance_stability.sql`, `0003_nonconformance_capa.sql`, `0004_certificates_samples.sql`,
`0005_study_lifecycle_source_coa.sql`).

## Run

```
PASSAGE_ENV=dev SERVICE_NAME=quality DATABASE_URL=postgres://... \
  go run ./cmd/quality
```

Config is read from the environment; `DATABASE_URL` is required. Health: `/healthz`,
`/readyz`; metrics on the admin port (`/metrics`).

`INVENTORY_URL` (optional) is the base URL of `services/inventory`. With it, a failing result or a
nonconformance holds the lot in inventory and the usage decision or a refused / closed nonconformance
releases it, through a signed service token that holds only `inventory.hold.apply`; it needs
`PASSAGE_SERVICE_NAME=quality` and `PASSAGE_SERVICE_SIGNING_KEY` (or `_FILE`) and stops the service at
start when the key is missing. Without it quality does not touch inventory. Queued calls are sent by an in-process sweeper (not on the request path): at start, every `QUALITY_HOLD_SWEEP_INTERVAL` (default 1m) for the tenants in `QUALITY_HOLD_TENANTS` (comma-separated UUIDs; required when `INVENTORY_URL` is set, the service stops without it because tenants cannot be listed under row-level security; chart value `holdTenants`), and at once for a tenant that queued one; it logs an error for calls inventory refused and calls pending over 5 minutes.

## Test

```
cd services/quality
go vet ./... && go test ./...
```

Domain tests run without a database. Store and API tests use `platform/go/testkit` and
are skipped unless `PASSAGE_TEST_DATABASE_URL` is set.

## API

Contract: `api/openapi.yaml`. Events: `events/asyncapi.yaml`.

| Method | Path | Duty |
|---|---|---|
| POST | `/v1/quality/sampling-plans:size` | QM_VIEW |
| GET | `/v1/quality/test-groups` | QM_VIEW |
| GET | `/v1/quality/test-groups/{id}` | QM_VIEW |
| POST | `/v1/quality/test-groups` | QM_SETUP_MAINTAIN |
| GET | `/v1/quality/instruments?dueWithinDays=&limit=` | QM_VIEW |
| POST | `/v1/quality/instruments` | QM_SETUP_MAINTAIN |
| POST | `/v1/quality/instruments/{id}:calibrate` | QM_SETUP_MAINTAIN |
| GET | `/v1/quality/associations` | QM_VIEW |
| POST | `/v1/quality/associations` | QM_SETUP_MAINTAIN |
| GET | `/v1/quality/quality-orders?status=&siteId=&limit=` | QM_VIEW |
| POST | `/v1/quality/quality-orders` | QM_ORDER_MAINTAIN |
| GET | `/v1/quality/quality-orders/{id}` | QM_VIEW |
| POST | `/v1/quality/quality-orders/{id}/results` | QM_RESULTS_RECORD |
| POST | `/v1/quality/quality-orders/{id}:usage-decision` | QM_USAGE_DECISION |
| GET | `/v1/quality/quality-orders/{id}/certificates` | QM_VIEW |
| POST | `/v1/quality/quality-orders/{id}/certificates` | QM_RESULTS_RECORD |
| POST | `/v1/quality/quality-orders/{id}/certificates:waive` | QM_USAGE_DECISION |
| GET | `/v1/quality/samples?qualityOrderId=&status=&limit=` | QM_VIEW |
| POST | `/v1/quality/samples` | QM_RESULTS_RECORD |
| GET | `/v1/quality/compliance-lists?limit=` | QM_VIEW |
| POST | `/v1/quality/compliance-lists` | QM_COMPLIANCE_MAINTAIN |
| GET | `/v1/quality/items/{id}/composition` | QM_VIEW |
| POST | `/v1/quality/items/{id}/composition` | QM_COMPLIANCE_MAINTAIN |
| POST | `/v1/quality/items/{id}:evaluate-compliance` | QM_VIEW |
| GET | `/v1/quality/stability-studies?status=&itemId=&limit=` | QM_VIEW |
| POST | `/v1/quality/stability-studies` | QM_STABILITY_MAINTAIN |
| GET | `/v1/quality/stability-studies/{id}` | QM_VIEW |
| POST | `/v1/quality/stability-studies/{id}:transition` | QM_STABILITY_MAINTAIN |
| GET | `/v1/quality/receipt-checks?poLineId=&batch=&limit=` | QM_VIEW |
| POST | `/v1/quality/receipt-checks` | QM_ORDER_MAINTAIN |
| POST | `/v1/quality/coa:generate` | QM_COA |
| GET | `/v1/quality/quality-orders/{id}/coas` | QM_VIEW |
| GET | `/v1/quality/spc?itemId=&testId=&orders=` | QM_VIEW |
| GET | `/v1/quality/nonconformances?status=&source=&severity=&qualityOrderId=&limit=` | QM_VIEW |
| POST | `/v1/quality/nonconformances` | QM_NCR_MAINTAIN |
| GET | `/v1/quality/nonconformances/{id}` | QM_VIEW |
| POST | `/v1/quality/nonconformances/{id}:approve` | QM_NCR_MAINTAIN |
| POST | `/v1/quality/nonconformances/{id}:refuse` | QM_NCR_MAINTAIN |
| POST | `/v1/quality/nonconformances/{id}/corrections` | QM_CAPA_MAINTAIN |
| POST | `/v1/quality/nonconformances/{id}:complete-step` | QM_CAPA_MAINTAIN |
| POST | `/v1/quality/nonconformances/{id}:check-effectiveness` | QM_CAPA_MAINTAIN |

The PRD writes paths as `/quality/v1/...`; the service follows the repository
convention `/v1/<context>/...`. Lists answer `{"items": [...]}` with `limit` 1 to 500
(default 100). Creating and posting calls require `Idempotency-Key`; `:calibrate` and
`:evaluate-compliance` are idempotent by value. The usage decision requires
`If-Match` carrying the order's ETag (428 when absent, 412 when stale). Every change to a
nonconformance (approve, refuse, start the 8D, complete a step, the effectiveness check)
requires `If-Match` with the NCR's ETag; starting the 8D also takes `Idempotency-Key`. A study
transition requires `If-Match` with the study's ETag. Duties are checked
through the platform `authz` package; they are not read from the token.

## Error codes

Problems are RFC 9457 (`application/problem+json`) with `code` set to the stable value.

| Code | HTTP | Meaning |
|---|---|---|
| COM-0001 | 400 | Validation failed; `errors` names the fields |
| COM-0428 | 428 | `If-Match` required on the usage decision, NCR changes and study transitions |
| AUTH-0001 | 401 | Missing or malformed tenant |
| QM-0100 | 422 | No quality association matches the trigger |
| QM-0102 | 422 | `INSTRUMENT_OVERDUE`: instrument past its calibration due date |
| QM-0104 | 422 | Decision quantities do not sum to lot quantity |
| QM-0105 | 422 | Required test has no result |
| QM-0109 | 409 | Order is closed; amend through a nonconformance |
| QM-0111 | 409 | Result already recorded for this test, sample and retest |
| QM-0113 | 422 | Result references an unknown instrument |
| QM-0114 | 422 | Z1.4 table not loaded for the requested level |
| QM-0115 | 409 | Order status does not allow the operation (first results on a pending order, decision before pending) |
| QM-0116 | 409 | A quality order already exists for this source line and trigger (BR-33.3) |
| QM-0140 | 404 | Test group not found |
| QM-0141 | 404 | Quality order not found |
| QM-0142 | 404 | Instrument not found |
| QM-0151 | 409 | Instrument code already exists |
| QM-0152 | 409 | Concurrent test group version; retry |
| QM-0121 | — | Result code: reporting-list substance over its threshold (warning) |
| QM-0122 | — | Result code: restricted substance over its limit (error under BLOCK, warning under WARN) |
| QM-0160 | 409 | Concurrent version of the same compliance list; retry |
| QM-0161 | 404 | Stability study not found |
| QM-0162 | 422 | Bill of materials over 500 items or 20 levels |
| QM-0163 | 409 | Concurrent composition version of the same item; retry |
| QM-0164 | 404 | Item has no composition |
| QM-0165 | 422 | Declared components loop back on the item |
| QM-0166 | 422 | Results cannot form the chart (subgroup size outside 2 to 25, mixed sizes) |
| QM-0167 | 409 | The same `Idempotency-Key` is creating a study concurrently; retry |
| QM-0170 | 404 | Nonconformance not found |
| QM-0171 | 409 | The NCR lifecycle does not allow the change (for example approving a refused NCR, starting an 8D on a NEW one) |
| QM-0172 | 409 | The same `Idempotency-Key` is creating an NCR or starting a correction concurrently; retry |
| QM-0173 | 409 | Not the current 8D step, or the correction is not open |
| QM-0174 | 422 | The effectiveness check is not due yet |
| QM-0175 | 422 | The effectiveness check must be made by another user than the one who completed D7 |
| QM-0125 | 422 | A required incoming certificate is missing, out of specification or not waived (BR-33.13) |
| QM-0180 | 409 | Waiver on an order that requires no certificate |
| QM-0181 | 422 | A pooled sample names a sample that is not a PRIMARY sample of the same order |
| QM-0182 | 422 | Drawn samples (PRIMARY and RESERVE) would exceed the lot quantity |
| QM-0183 | 409 | The same `Idempotency-Key` is recording a certificate or sample concurrently; retry |
| QM-0128 | — | Result code on a receipt check: the batch was source-inspected, receiving inspection skipped |
| QM-0184 | 409 | The stability study lifecycle does not allow the move (for example completing a PLANNED study) |
| QM-0185 | 409 | The receipt line was already checked with another PO line, batch or quantity |
| QM-0186 | 422 | A CoA needs a quality order closed by a decision that released quantity |
| QM-0187 | 422 | A CoA already exists for this batch or shipment; a new revision needs a reason |
| QM-0188 | 409 | A CoA revision or receipt check is being written concurrently; retry |
| idempotency_key_reuse | 422 | The `Idempotency-Key` was used for a decision, certificate or sample on another order |
| conflict_version | 412 | Stale `If-Match` |
| idempotency_key_reuse | 422 | Decision or correction `Idempotency-Key` already used on another order or NCR |
| AUTH-0003 | 403 | Missing duty |

## Wave reference

Wave 33, PRD section 6.33. Slice 1 to 3 scope and deferred items are listed in
`NOT-IMPLEMENTED.md`. The web workspace is `apps/web/src/modules/quality`.

Tenant isolation: every table forces RLS with a tenant policy, and every read and
guarded update also repeats the tenant predicate, so a connection that bypasses RLS
still sees one tenant (`TestPostgres_RowLevelSecurity`).
