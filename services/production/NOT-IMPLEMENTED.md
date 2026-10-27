# Not implemented in PSG-21 slices 1 and 2

Status values: **later slice of PSG-21** (in scope for this wave, built in a later slice), or
**handoff** (needs another wave or owner). Rows marked **partial** say what slices 1 and 2 build
and what they do not. Everything not listed here is built and tested (see README).

## Ticket deliverables

| Deliverable | Status | What is built / what is missing |
|---|---|---|
| 1. Order lifecycle with status transitions and BOM/route explosion at creation; estimation | partial | Built: create with explosion (phantoms, `30.1` numbering, BR-21.2, BR-21.8, flushing principle per line), the BR-21.1 state machine with all eight commands and their posting effects inside this service (`:start` backflushes start lines, `:report-finished` posts a RAF journal, `:end` settles, `:reset-status` writes reversal journals), re-estimate until Started, header PATCH, status log, planned cost (FR-21.3). Missing: BOM and route version resolution as of the delivery date and site (wave 9 handoff: the request carries the snapshot and its unit costs), manual version override before Estimated, and the inventory transactions (`ordered`/`on-order` at estimate, reservations at release) of wave 16 |
| 2. Picking lists (backflush/manual), route cards, job cards; RAF with by-/co-products and scrap | partial | Built (slice 2): the ProductionJournal entity with types `picking-list`, `route-card`, `report-as-finished`; draft, post, delete-draft and read endpoints; proposals `remaining`, `proportional`, `none`; flushing principles `manual`, `start` (consumed at `:start` or auto-start), `finish` (consumed proportionally by each RAF); the over-consumption tolerance and duty (FR-21.5, PROD-0007); route-card hours, good and error quantities, the `finished` flag and auto-start of a Released order (FR-21.6); RAF receipts at the planned unit cost within the RAF tolerance, error quantity without receipt (FR-21.7). Missing: job cards (need the ProductionJob rows that scheduling creates, wave 23 / later slice), `available-at-location` consumption when a job reports (behaves like manual), by-/co-product RAF lines (FR-21.14, Could), `endJobs`, tracking dimensions and serial uniqueness (BR-21.10, PROD-0009, PROD-0010; wave 16 tracking), standard cost for standard-costed items at RAF (wave 20 costing method) |
| 3. WIP postings and order end with cost settlement: variances | partial | Built (slice 2): posting profiles by legal entity × item group × site with All fallbacks (FR-21.13); balanced WIP subledger rows per posted journal (Dr WIP / Cr inventory, Dr WIP / Cr labor and overhead absorption, Dr finished goods / Cr WIP), rounded to the currency's minor unit; actual cost from posted journals; End refuses unposted journals (PROD-0012) and an end date before the last posting, decomposes actual against planned (BR-21.6), clears the WIP balance into the four variance accounts and stores the Variance rows; zero-output End sends all cost to variance (FR-21.8); events `journal.posted` and `order.settled` carry the ledger lines. Missing: booking the postings in the general ledger and the `journalEntryId` link (wave 11 posting API; the events carry the lines for it), inventory transactions for issues and receipts and the actual issue cost from inventory (wave 16; issues use the request's or the estimated unit cost), the weighted-average "total variance only" rule of BR-21.6 (needs the item's costing method, wave 20), period control at End and on journals (wave 14), cancelling `ordered`/`on-order` transactions (wave 16), EUR reporting amounts (wave 12) |
| 4. Subcontracting operations with vendor PO linkage | partial | Built: the operation's `subcontract` flag and vendor unit price, planned as subcontract cost; route cards refuse hours on a subcontracted operation. Missing (later slice; wave 17 handoff for the PO): PO line creation at release, invoice-to-actual (until then a planned subcontract cost settles as a substitution variance), PO change on quantity change, event `passage.production.subcontract.po-requested.v1`, PROD-0014 accrual warning |
| 5. Production order inquiry: cost roll, status log, transactions | partial | Built: order read and list (planned and actual cost), cost view with planned against actual by group, WIP balance and variances, the journal list with lines and postings, status log (API and web). Missing: inventory transactions and the drill-down to the GL journal entry (waves 16 and 11) |
| 6. Split/merge, order change management, order-specific BOM/route edits | later slice of PSG-21 | FR-21.15 (needs WIP transfer journals and wave 23 rescheduling), FR-21.16 (needs wave 9 `bom.version.activated` events, OrderChangeRecord, `ocm-detect`), FR-21.18 (edit endpoints, audit diff, save as version) |
| 7. Trigger points, rework orders, as-built genealogy | later slice of PSG-21 | FR-21.17 (trigger points on route-card feedback), FR-21.20 (rework orders and the `rework` variance type), FR-21.19 (AsBuiltRecord, needs wave 16 tracking) |
| Deploy: production control workspace; demo plant runs its first order end to end | partial | Built: the workspace in `apps/web/src/modules/production/` (list with planned and actual cost, create, detail with status actions, journals panel to create, review, post and delete journals, costs with variances, status log; posting-profile setup page). An order runs end to end in this service and ends with 0 WIP (`TestJournals_WorkedSettlementExample`). Missing: the demo seed, and the inventory and GL sides of that order (waves 16 and 11) |

## Acceptance criteria

| AC | Status | Notes |
|---|---|---|
| AC1 lifecycle property test: every illegal transition rejected, every legal one posts the expected entries | partial | `TestTransition_Table`/`_Property` cover all 64 status × command pairs and `TestResetTo` all reset pairs; the store tests check the postings of `:start` (`TestJournals_Backflush`), `:report-finished`, `:end` and the reversals of `:reset-status` (`TestJournals_PartialOutputAndReset`, `TestJournals_Rules`). The property test does not yet generate postings per transition, and the inventory entries are wave 16 |
| AC2 costing fixtures: 50 orders produce expected WIP and variance postings | partial | `TestSettle_ProdOrders50Fixture` settles 50 orders against an independent exact-fraction generator, and `TestSettleWIP_ClearsThePostedBalance` proves the variances always clear the posted WIP in the currency's minor unit. The fixture is synthetic; the PRD's reviewed workbook from the demo-plant BOMs (waves 8, 9) is not built |
| AC3 backflush: RAF of 100 consumes BOM × (1 + scrap%) exactly | done | `TestJournals_Backflush` (RAF 40 then 60 consumes exactly 0.3333 × 100 × 1.02 on a finish-flushed line through the database) and `TestProposeQty_ProportionalSumsToTheRequirement` (property) |
| AC4 order end with partial RAF reconciled to the GL WIP account | partial | `TestJournals_PartialOutputAndReset`: after RAF 60 of 100 the WIP balance is posted consumption − 60 × planned unit cost, and End clears it to 0 with the subledger balanced. The reconciliation against the GL account itself waits for wave 11 |
| AC5 every deliverable has code or a reason here | done | this file |
| AC6 vet, tests, OpenAPI/AsyncAPI match code, chart lints, web vitest and tsc | done | spec parity is tested (`TestOpenAPIMatchesTheRouter`, `TestAsyncAPIMatchesThePayloads`) |
| PRD AC-21.1, AC-21.2, AC-21.3 | done | `TestExplode_ThreeLevelWithPhantoms`, store and HTTP tests for the 409 on `:start` from Created and for planned material from `/costs` |
| PRD AC-21.4 | done | `TestJournals_Backflush` |
| PRD AC-21.5 (batch-tracked RAF without a batch) | handoff | tracking dimensions are wave 16 |
| PRD AC-21.6 | done | `TestJournals_WorkedSettlementExample`: 305 kg at 10.20, 46 h, RAF 100, four variances totalling −49.00 and WIP 0.00 |
| PRD AC-21.7 | partial | as AC4 above |
| PRD AC-21.8 (`REVERSAL_BLOCKED_DOWNSTREAM`) | handoff | needs wave 16 to know that finished goods were shipped; the reversal itself is built |
| PRD AC-21.9 to AC-21.15 | later slice of PSG-21 | subcontract POs, bulk jobs, split, OCM, trigger points, as-built and rework |

## Deviations

| Item | Status | Reason |
|---|---|---|
| Paths `/api/v1/production/...` | deviation | served as `/v1/production/...`; the gateway adds `/api`, as for every service |
| Event `passage.production.order.status-changed.v1` | deviation | the kernel rule `dev.passage.<context>.<entity>.<verb>.v<n>` allows no hyphen, so it is `dev.passage.production.order.statuschanged.v1` |
| Event `dev.passage.production.order.updated.v1` | addition | not in the PRD table; PATCH changes quantity and delivery date that planning consumes, so the change is published |
| `journal.posted` and `order.settled` payloads | addition | they also carry the balanced ledger lines (`postings`) and the currency, so the general ledger (wave 11) can book them; `journalEntryId` is null until it does |
| `:report-finished` command and RAF journals | deviation | both exist: the command creates and posts a RAF journal in one call (finish lines proportional), and `POST …/journals` with type `report-as-finished` then `:post` does the same in two steps. Either moves a Started order to ReportedAsFinished |
| Posting-profile accounts | deviation | FR-21.13 lists picking-list issue/offset, WIP issue, WIP account, RAF receipt/offset and the variance accounts; the profile has one WIP account (issue and valuation are the same account), inventory (picking-list offset), finished goods (RAF receipt), labor and overhead absorption, and the four variance accounts. Resolution: item group × site, item group × All, All × site, All × All |
| Item group on the order | deviation | the order request carries `itemGroup` (the posting-profile key) until the released product (wave 8) supplies it; an order without one resolves through the All profiles |
| `PUT /v1/production/posting-profiles` and `DELETE /v1/production/journals/{id}`, `GET /v1/production/journals/{id}` | addition | the PRD lists only the profile read; maintaining profiles (4.21.6 setup screen) and deleting a draft (PROD-0012 "post or delete") need them |
| Idempotency through the kernel `idempotency_keys` table | deviation | keys and request fingerprints are stored on `production_order`, `production_status_log` and `production_journal` (same replay and 422 behaviour); there is no kernel middleware to share |
| BR-21.4 `pg_advisory_xact_lock` | deviation | commands, edits and journal posts take the order row lock (`select … for update`) first, which serializes them per order |
| Unit costs and rates on the create request | deviation | the active costing version (wave 20) and resource cost categories (wave 9) are read by the caller until the service resolves them itself |
| Journal posting date default | deviation | today in UTC; system journals (start backflush, RAF command, reversals, settlement) post today, a reversal never before its original |

## Handoffs and later work

| Item | Status | Reason |
|---|---|---|
| Order number from the wave 5 `production-order` number sequence; journal numbers from `prod-doc` | handoff | per-legal-entity counters issue `PO-000001` and `PJ-000001`; the sequence service is wave 5 |
| Default quantity, multiple, min and max from released-product settings; `estimateOnCreate`; product released in the legal entity | handoff | wave 8 released-product settings; only quantity > 0 is checked |
| FR-21.4 release: picking list and job card PDFs, reservations per policy, shortage warning (PROD-0005), `strict`, unreleased component block (PROD-0006) | later slice of PSG-21 | documents need the wave 5 document service, reservations and on-hand need wave 16 |
| Start effect: route card for operations with `autoStart` | later slice of PSG-21 | operations carry no `autoStart` flag until route versions come from wave 9 |
| `:schedule` dates (infinite capacity), ProductionJob rows, `scheduling_method`, job cards | later slice of PSG-21 | `:schedule` records the status only |
| BR-21.5 `REVERSAL_BLOCKED_DOWNSTREAM` (PROD-0004) | handoff | reversal journals are built; knowing that a reversed receipt was consumed downstream needs wave 16 |
| FR-21.12 bulk status update of up to 500 orders (Temporal) and PROD-0022 | later slice of PSG-21 | needs the wave 4 Temporal worker |
| GL booking of production postings and `journalEntryId` (w11), drill-down to the journal entry (w15) | handoff | the WIP subledger and the event postings are built; the ledger posting API is wave 11 |
| Inventory transactions for issues and receipts (`deducted`, receipts), actual issue cost, BR-21.9 negative inventory (PROD-0011), BR-21.10 serials (PROD-0009, PROD-0010) | handoff | wave 16 inventory and tracking |
| BR-21.3 period control and `PERIOD_CLOSED` (PROD-0002) on journals and End | handoff | wave 14 period control API; the posting date ≥ order creation part of BR-21.3 is built |
| Units of measure precision and conversion from wave 6 | partial | quantities use the fixed 6-place `QtyScale` and the per-line `roundUp` flag; per-unit precision is a wave 6 handoff |
| EUR reporting amounts per posting (wave 12) | handoff | postings are in the order currency, rounded to its minor unit with `platform/go/money` |
| Problem `serviceCode` (PROD-00nn) | handoff | the kernel `httpx.Problem` has no field for it (wave 0) |
| OPA duty entries for `production.*` in `policies/authz/data.json` | handoff | `policies/` is wave 2 territory; routes enforce duties with `platform/go/authz` today |
| JWT validation and the `passage-api` audience | handoff | wave 2 identity |
| Outbox relay (Debezium) | handoff | Kafka and Debezium are wave 4 |
| `production-db` CloudNativePG cluster, database and role, Argo CD Application, image registry entry, ingress, Kubernetes secret | handoff | `deploy/` is wave 1 territory; the chart (deployment, service, HPA, ServiceMonitor) is built |
| 4.21.13 parameters other than the RAF tolerance, over-consumption tolerance and auto-start (end jobs, estimate on create, proposal defaults per site, OCM, trigger points, split, rework, as-built) | later slice of PSG-21 / handoff | per-site parameters need a parameter store; the features behind most are later slices |
| Feature flags `prod.subcontracting`, `prod.coproduct-basic` | later slice of PSG-21 | no flag service in the kernel; the flags gate features not built |
| Scheduled jobs `wip-reconcile`, `cancel-stale-created-orders`, `ocm-detect`, `subcontract-accrual`, `order-cost-snapshot`, `auto-end-fully-reported` | later slice of PSG-21 | need Temporal (wave 4), the ledger (wave 11) and wave 14 close |
| Reconciliation tests (Σ open-order WIP = GL WIP, RAF value = inventory receipts, Σ variances = GL) | partial | the subledger side is tested (every journal and settlement balances; WIP is 0 after End); the GL and inventory sides need waves 11 and 16 |
| Inquiries: WIP balance report, variance report, shortage list, operations overview, journal inquiry across orders, subcontract list, OCM worklist, as-built, KPI (4.21.14) | later slice of PSG-21 / handoff | per-order WIP, variances, journals and operations feedback are served; cross-order reports and the KPI projection (wave 46) are not |
| Documents (4.21.15) and the `production.document.render`, `production.posting-profile.resolve`, `production.statutory-extract` extension points | later slice of PSG-21 / handoff | wave 5 document service; OPA policy hooks are wave 2 |
| Web: bulk select, saved views, `Ctrl+Shift+S`, journal editor `localStorage` drafts, SSE live costs, drill-down to the journal entry, product/site/resource lookups | later slice of PSG-21 / handoff | the journals panel, cost view and posting-profile page are built; the rest needs later slices and waves 6, 8, 9, 11 (see the web README) |
| Performance budgets and the 200-line journal latency test | later slice of PSG-21 | no load test yet |
| Migration from AX and SAP (4.21.11) | later slice of PSG-21 | needs the wave 10 migration framework |
| Open question 1 (cancel Released orders with automatic un-reserve) | later slice of PSG-21 | Released orders can be cancelled; there are no reservations to release yet |
