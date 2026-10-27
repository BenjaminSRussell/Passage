# Not implemented in PSG-21 slice 1

Status values: **later slice of PSG-21** (in scope for this wave, built in a later slice), or
**handoff** (needs another wave or owner). Rows marked **partial** say what slice 1 builds and
what it does not. Everything not listed here is built and tested (see README).

## Ticket deliverables

| Deliverable | Status | What is built / what is missing |
|---|---|---|
| 1. Order lifecycle with status transitions and BOM/route explosion at creation; estimation | partial | Built: create with explosion (phantoms, `30.1` numbering, BR-21.2, BR-21.8), the BR-21.1 state machine with all eight commands, re-estimate until Started, reset to any earlier status, header PATCH, status log, planned cost (FR-21.3). Missing: BOM and route version resolution as of the delivery date and site (wave 9 handoff: the request carries the snapshot and its unit costs), manual version override before Estimated, and the inventory and GL effects BR-21.1 attaches to each transition (rows below) |
| 2. Picking lists (backflush/manual), route cards, job cards; RAF with by-/co-products and scrap | partial | Built: report as finished records good and error quantities on the header within the RAF tolerance (FR-21.7 quantity rule, PROD-0008). Missing (later slice of PSG-21): the ProductionJournal entity and its types, proposals (`remaining`, `proportional`, `none`), flushing principles, over-consumption duty, route and job card hours, RAF receipts, by-/co-products (FR-21.14), `acceptError`, `endJobs`, tracking dimensions (FR-21.7, BR-21.10) |
| 3. WIP postings and order end with cost settlement: variances | partial | Built: `domain.Settle` (BR-21.6) with the BR-21.7 example, the 50-order fixture and the identity property test; `:end` guards. Missing (later slice; needs wave 11 posting and wave 16 inventory): WipPosting rows, actual cost from posted journals, WIP reversal and variance journals, the Variance entity, posting profiles (FR-21.13), the weighted-average "total variance only" rule of BR-21.6, the end-date and period checks of FR-21.8, cancelling `ordered`/`on-order` transactions |
| 4. Subcontracting operations with vendor PO linkage | partial | Built: the operation's `subcontract` flag and vendor unit price, planned as subcontract cost. Missing (later slice; wave 17 handoff for the PO): PO line creation at release, invoice-to-actual, PO change on quantity change, event `passage.production.subcontract.po-requested.v1`, PROD-0014 accrual warning |
| 5. Production order inquiry: cost roll, status log, transactions | partial | Built: order read, list, planned cost by group, status log (API and web). Missing: actual cost and variance columns, journal list, inventory transactions and GL drill-down (need journals) |
| 6. Split/merge, order change management, order-specific BOM/route edits | later slice of PSG-21 | FR-21.15 (needs WIP transfer journals and wave 23 rescheduling), FR-21.16 (needs wave 9 `bom.version.activated` events, OrderChangeRecord, `ocm-detect`), FR-21.18 (edit endpoints, audit diff, save as version) |
| 7. Trigger points, rework orders, as-built genealogy | later slice of PSG-21 | FR-21.17 (needs job feedback), FR-21.20 (rework orders and the `rework` variance type, needs RAF and variance journals), FR-21.19 (AsBuiltRecord, needs picking and RAF journals and wave 16 tracking) |
| Deploy: production control workspace; demo plant runs its first order end to end | partial | Built: the workspace in `apps/web/src/modules/production/` (list, create, detail with status actions, RAF, reset, planned cost, status log). Missing: the demo seed and an end-to-end order that posts and ends with 0 WIP (needs the postings above and waves 11 and 16) |

## Acceptance criteria

| AC | Status | Notes |
|---|---|---|
| AC1 lifecycle property test: every illegal transition rejected, every legal one posts the expected entries | partial | `TestTransition_Table`/`_Property` cover all 64 status × command pairs, `TestResetTo` all reset pairs, and the store lifecycle test runs them through Postgres. The posting half waits for journals |
| AC2 costing fixtures: 50 orders produce expected WIP and variance postings | partial | `TestSettle_ProdOrders50Fixture` settles 50 orders (full, partial and zero output, setup, substitution) against expectations from an independent exact-fraction generator. The fixture is synthetic: the PRD's reviewed workbook from the demo-plant BOMs (waves 8, 9) and the posting of the results are not built |
| AC3 backflush: RAF of 100 consumes BOM × (1 + scrap%) exactly | partial | `RequiredQty` is exact (`TestRequiredQty_BackflushIsExact`, `_ScrapIsProportional`); the proportional picking-list consumption through a RAF journal is a later slice |
| AC4 order end with partial RAF reconciled to the GL WIP account | partial | The settlement identity holds for partial RAF (`TestSettle_PartialRAFLeavesWIPConsistent`, `_LotSizeVarianceOnPartialOutput`); the GL reconciliation needs WipPosting |
| AC5 every deliverable has code or a reason here | done | this file |
| AC6 vet, tests, OpenAPI/AsyncAPI match code, chart lints, web vitest and tsc | done | spec parity is tested (`TestOpenAPIMatchesTheRouter`, `TestAsyncAPIMatchesThePayloads`) |
| PRD AC-21.1, AC-21.2, AC-21.3 | done | `TestExplode_ThreeLevelWithPhantoms`, store and HTTP tests for the 409 on `:start` from Created and for planned material from `/costs` |
| PRD AC-21.4 to AC-21.15 | later slice of PSG-21 | need journals, postings, subcontract POs, bulk jobs, split, OCM, trigger points, as-built and rework |

## Deviations

| Item | Status | Reason |
|---|---|---|
| Paths `/api/v1/production/...` | deviation | served as `/v1/production/...`; the gateway adds `/api`, as for every service |
| Event `passage.production.order.status-changed.v1` | deviation | the kernel rule `dev.passage.<context>.<entity>.<verb>.v<n>` allows no hyphen, so it is `dev.passage.production.order.statuschanged.v1` |
| Event `dev.passage.production.order.updated.v1` | addition | not in the PRD table; PATCH changes quantity and delivery date that planning consumes, so the change is published |
| Report as finished as a `:report-finished` command | deviation | until RAF journals exist, the command records the reported quantities on the header and moves the order to ReportedAsFinished; a partial RAF that keeps the order Started arrives with RAF journals |
| Idempotency through the kernel `idempotency_keys` table | deviation | keys and request fingerprints are stored on `production_order` and `production_status_log` (same replay and 422 behaviour, as the costing service does); there is no kernel middleware to share |
| BR-21.4 `pg_advisory_xact_lock` | deviation | commands and edits take the order row lock (`select … for update`), which serializes them per order; the advisory lock comes with journal posting |
| Unit costs and rates on the create request | deviation | the active costing version (wave 20) and resource cost categories (wave 9) are read by the caller until the service resolves them itself |

## Handoffs and later work

| Item | Status | Reason |
|---|---|---|
| Order number from the wave 5 `production-order` number sequence | handoff | a per-legal-entity counter issues `PO-000001`; the sequence service is wave 5 |
| Default quantity, multiple, min and max from released-product settings; `estimateOnCreate`; product released in the legal entity | handoff | wave 8 released-product settings; only quantity > 0 is checked |
| FR-21.4 release: picking list and job card PDFs, reservations per policy, shortage warning (PROD-0005), `strict`, unreleased component block (PROD-0006) | later slice of PSG-21 | documents need the wave 5 document service, reservations and on-hand need wave 16 |
| Start effects (auto-post `start`-flushed picking, route card for `autoStart` ops, period open) | later slice of PSG-21 | need journals and wave 14 period control |
| `:schedule` dates (infinite capacity), ProductionJob rows, `scheduling_method` | later slice of PSG-21 | `:schedule` records the status only |
| FR-21.9 reversal journals and `REVERSAL_BLOCKED_DOWNSTREAM` (BR-21.5, PROD-0004) | later slice of PSG-21 | reset reverses the reported quantities and the estimate; journal reversal needs journals and wave 16 |
| FR-21.12 bulk status update of up to 500 orders (Temporal) and PROD-0022 | later slice of PSG-21 | needs the wave 4 Temporal worker |
| FR-21.13 posting profiles and `GET /v1/production/posting-profiles` | later slice of PSG-21 | needed by journal posting |
| Events `dev.passage.production.journal.posted.v1`, `dev.passage.production.order.settled.v1` | later slice of PSG-21 | emitted by journal posting and settlement |
| BR-21.3 posting date and `PERIOD_CLOSED` (PROD-0002) | handoff | wave 14 period control API |
| BR-21.9 negative inventory (PROD-0011), BR-21.10 serial duplicates (PROD-0009, PROD-0010) | later slice of PSG-21 | need wave 16 stock and tracking |
| Units of measure precision and conversion from wave 6 | partial | quantities use the fixed 6-place `QtyScale` and the per-line `roundUp` flag; per-unit precision is a wave 6 handoff |
| Currency rounding per wave 12 and EUR reporting amounts | handoff | amounts are kept at 4 decimals and shown to cents; posting rounding comes with postings |
| Problem `serviceCode` (PROD-00nn) | handoff | the kernel `httpx.Problem` has no field for it (wave 0) |
| OPA duty entries for `production.*` in `policies/authz/data.json` | handoff | `policies/` is wave 2 territory; routes enforce duties with `platform/go/authz` today |
| JWT validation and the `passage-api` audience | handoff | wave 2 identity |
| Outbox relay (Debezium) | handoff | Kafka and Debezium are wave 4 |
| `production-db` CloudNativePG cluster, database and role, Argo CD Application, image registry entry, ingress, Kubernetes secret | handoff | `deploy/` is wave 1 territory; the chart (deployment, service, HPA, ServiceMonitor) is built |
| Config `PRODUCTION_OVERCONSUMPTION_PCT`, `PRODUCTION_AUTOSTART_ON_FEEDBACK` and the other 4.21.13 parameters except the RAF tolerance | later slice of PSG-21 | read by picking, route-card and OCM features that are not built |
| Feature flags `prod.subcontracting`, `prod.coproduct-basic` | later slice of PSG-21 | no flag service in the kernel; the flags gate features not built |
| Scheduled jobs `wip-reconcile`, `cancel-stale-created-orders`, `ocm-detect`, `subcontract-accrual`, `order-cost-snapshot`, `auto-end-fully-reported` | later slice of PSG-21 | need Temporal (wave 4), journals and wave 14 close |
| Reconciliation tests (Σ open-order WIP = GL WIP, RAF value = inventory receipts, Σ variances = GL) | later slice of PSG-21 | need WipPosting and waves 11 and 16 |
| Inquiries: WIP balance, variance report, shortage list, operations overview, journals, subcontract list, OCM worklist, as-built, KPI (4.21.14) | later slice of PSG-21 / handoff | read journals and postings; the KPI projection is wave 46 |
| Documents (4.21.15) and the `production.document.render`, `production.posting-profile.resolve`, `production.statutory-extract` extension points | later slice of PSG-21 / handoff | wave 5 document service and the posting features |
| Web: bulk select, saved views, `Ctrl+Shift+S`, journal editor with `localStorage` drafts, cost analysis with drill-down, SSE live costs, product/site/resource lookups | later slice of PSG-21 / handoff | the service features behind them are later slices; lookups need waves 6, 8 and 9 (see the web README) |
| Performance budgets and the 200-line journal latency test | later slice of PSG-21 | no load test yet |
| Migration from AX and SAP (4.21.11) | later slice of PSG-21 | needs the wave 10 migration framework |
| Open question 1 (cancel Released orders with automatic un-reserve) | later slice of PSG-21 | Released orders can be cancelled; there are no reservations to release yet |
