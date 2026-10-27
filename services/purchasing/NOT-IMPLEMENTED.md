# NOT-IMPLEMENTED — purchasing (PSG-17, wave 17)

Slice 1 builds the core of the document chain: requisitions with band/category
approval routing, purchase orders priced from trade agreements with a database
total identity, trade agreement price rows with the BR-17.3 overlap check, price
lookup with explain, lists for all three documents, the migration with RLS, the
HTTP surface, contracts, chart, Dockerfile and the procurement workspace web
module (`apps/web/src/modules/purchasing`). Slice 2 adds consolidation of approved
requisition lines into POs with the consolidation queue (FR-17.3, AC-17.9),
purchase agreements with release tracking, HARD/SOFT/NONE limits, validity,
`agreement.threshold.v1` and price override (FR-17.5, BR-17.10, AC-17.7 at PO
creation), agreement activate/hold/close, and the agreement and consolidation
screens. Everything below is absent or partial.

Status values: **later slice of PSG-17** (this wave, next slices) or **handoff**
(needs another wave or the platform owner).

## Deliverables (PSG-17 description, items 1–10)

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Requisition policy approval (amount bands, category) and consolidation into POs | Partial. Built: submit routing by amount band and line category with the built-in `DefaultApprovalPolicy` (`builtin-1` stamped on the requisition), auto-approval with `requisition.approved`; `requisitions:consolidate` grouping by legal entity × vendor × currency × site × delivery window with buyer vendor override, the open-lines queue with the NEEDS_SOURCING filter, requisition line ↔ PO line links and CLOSED requisitions (slice 2). **Later slice of PSG-17**: splitting a requisition line across POs (FR-17.3 "split lines"), starting an RFQ from the needs-sourcing queue (needs item 2), the Temporal approval workflow (approve/reject/delegate, 48 h SLA, escalation; wave 4 template), restart on line edit, requester-role rules, effective-dated policy versions, the per-legal-entity `consolidation_window_days` setting (the window is a request field defaulting to 7). |
| 2 | RFQ cycle: issue, supplier reply, compare, award | **Later slice of PSG-17**. Supplier portal replies depend on wave 20. |
| 3 | Purchase agreements with release tracking; trade agreements | Partial. Built: PRICE trade agreement rows (vendor/group/all × product/group/all, currency, unit, quantity break, validity, consignment flag), activation overlap check PRC-TA-002, list/detail, lookup with explain; blanket purchase agreements with the four commitment types, HARD/SOFT/NONE enforcement, released qty/amount drawn in the PO transaction, PRC-AGR-003/004/005, `agreement.threshold.v1`, price override, release list, DRAFT/EFFECTIVE/ON_HOLD/CLOSED lifecycle (slice 2). **Later slice of PSG-17**: returning released quantity when a PO line is cancelled or reduced (needs PO edit and `:cancel`), the daily threshold/expiry scan job and the expiry (< 30 days) half of `agreement.threshold`, the `agreement_threshold_pct` setting (90 is fixed), agreement price terms by quantity break, `default_commitment_type` on the header, the purchase agreement PDF (3.17.15), LINE_DISC/MULTILINE_DISC/TOTAL_DISC trade agreement relations, DRAFT → ACTIVE trade agreement journal posting (rows are created ACTIVE). The category check of a CATEGORY_VALUE line is **later slice of PSG-17** with procurement categories on PO lines (wave 8 hierarchy). |
| 4 | PO lifecycle with versioned change management, confirmation, charges, delivery schedules | Partial. Built: PO create (DRAFT) with lines, header charges (FIXED/PER_UNIT/PERCENT), persisted totals with a deferred identity trigger, `price_source`, list/detail; the reapproval rule evaluation (`domain.ReapprovalRequired`, AC-17.2/17.3 at domain level). **Later slice of PSG-17**: approval workflow for POs, `:confirm` and `po.confirmed`, `PurchaseOrderVersion` snapshots and JSON Patch diffs, PATCH with ETag/412 (PRC-PO-009), `:cancel`, line charges, delivery schedules (FR-17.10). Inventory ORDERED transactions on confirm are a **handoff** to the wave 16 `transactions:apply` contract. |
| 5 | Supplier output PDF/email; EDI 850/ORDERS hook | **Later slice of PSG-17** (render worker, email through wave 4). EDI transport is **wave 35** (`prc.edi_output`). |
| 6 | Scheduling agreements with FRC/JIT releases | **Later slice of PSG-17**. |
| 7 | Policy framework by org hierarchy; signing limits, requests, recertification | **Later slice of PSG-17**. Hierarchy resolution needs wave 6 purposes; approver skipping by signing limit (FR-17.17) is not done. |
| 8 | Procurement catalogs, vendor catalog import, category managers/assignments/questionnaires | **Later slice of PSG-17**. `external_catalog_ref` is not yet on the requisition line. |
| 9 | RFQ types, sealed bids, weighted scoring, amendments | **Later slice of PSG-17** (after item 2). |
| 10 | Service PO lines, outlines, invoicing plans, reprice, consignment price agreements | Partial. Built: the consignment flag on a trade agreement row, stored and excluded from PO pricing. **Later slice of PSG-17**: service lines, outlines and model service specifications (FR-17.23), invoicing plans (FR-17.21), `:reprice` (FR-17.22). |

## Acceptance criteria

| AC | Status | Notes |
|---|---|---|
| AC1 approval matrix (30 requisitions) | Partial | `internal/domain/approval_test.go` routes the 30 rows of `testdata/approval-matrix.csv` (6 amounts across the 4 default bands × 5 categories; the IT_HARDWARE "never auto-approve" rule is a test policy, not a default). The E2E run through the Temporal test server is **later slice of PSG-17**. |
| AC2 PO change after confirmation → new version, re-approval | Partial | Rule evaluation only (`reapproval_test.go`); versions and the change API are **later slice of PSG-17**. |
| AC-17.7 HARD agreement overrun → PRC-AGR-003 (PRD) | Built at PO creation | The release is drawn when the PO is created (`agreement_test.go` in domain and store); the PRD names confirmation, which is a **later slice of PSG-17**: until `:confirm` exists a DRAFT PO holds its release. |
| AC-17.9 10 lines, 2 vendors, 2 sites → 4 POs (PRD) | Built | `domain/consolidation_test.go`, `api/consolidate_test.go` (each PO line keeps its requisition line), `store/consolidation_test.go` (links, CLOSED, replay, no double ordering). |
| AC3 most specific trade agreement wins (property test) | Built | `pricing_test.go` rapid property also checks the tie order (latest valid_from, then lowest price); `store_test.go` checks the vendor > group > all example against Postgres. Group memberships come from the `api.Memberships` interface, whose default returns none until waves 7 (vendor groups) and 8 (product groups) expose them (**handoff**); the lookup accepts `vendorGroup`/`productGroup` meanwhile. |
| AC4 PO total identity for 1,000 random POs | Built | `totals_test.go` (seeded, 1–50 lines, charges, tax); the database trigger refuses an unbalanced header (`store_test.go`). |
| AC5 every deliverable has code or a row here | Built | This file. |
| AC6 checks | Built | `go vet`/`go test` (store tests run against Postgres when `PASSAGE_TEST_DATABASE_URL` is set), vitest and tsc for the web module, `helm lint`; `openapi_test.go` and `events_test.go` keep the specs in step with the routes and payloads. |

## PRD requirements not built in this slice

| Requirement | Status | Reason |
|---|---|---|
| FR-17.1 estimate fallback to the last PO price | **Later slice of PSG-17** | The trade agreement default (preferred vendor) and the PRC-REQ-003 warning are built; last-price needs PO history by product. |
| FR-17.1 account structure validation (PRC-REQ-001), financial dimensions, attachments | **Handoff** to wave 11 (account structures) and wave 4 (attachments) | Not accepted by the API yet. |
| FR-17.2 bands in accounting currency | **Handoff** to wave 12 (exchange rates) and wave 6 (legal entity accounting currency) | Bands are applied to the requisition total in the requisition currency. |
| FR-17.7 approval and document status machines | **Later slice of PSG-17** | Only DRAFT/NONE are produced. |
| FR-17.9 auto-charges, debit/credit types and accounts, line charges | **Later slice of PSG-17**; allocation is consumed by wave 18 | Header charges are entered explicitly. |
| FR-17.11 tax from the wave 13 engine | **Handoff** to wave 13 | Tax is supplied per line (`taxAmount`). |
| FR-17.13 purchasing policy settings | **Later slice of PSG-17** | `changeManagement` is a per-PO flag only. |
| FR-17.14 buyer workspace | Partial | Tiles for requisitions in review, approved requisitions, lines needing a vendor, draft POs, effective purchase agreements and active price rows are built; RFQ, expiring-agreement and change-order queues come with those features (**later slice of PSG-17**). |
| FR-17.15–FR-17.24 | **Later slice of PSG-17** | See deliverables 6–10. |
| BR-17.1 reject/cancel transitions, line cancellation | **Later slice of PSG-17** | Only DRAFT → IN_REVIEW/APPROVED on submit. |
| BR-17.3 delivery-date price date | **Later slice of PSG-17** (`prc.delivery_date_pricing`) | The order date (UTC today) is used. |
| BR-17.4 unit conversion fallback recorded in explain | **Later slice of PSG-17**; conversions from wave 6 UoM | The unit must match exactly. |
| BR-17.10 agreement validity and limits | Built | Checked per PO line against its delivery date when the PO is created. |
| BR-17.7, BR-17.9, BR-17.13, BR-17.16–19 | **Later slice of PSG-17** | Depend on edits, schedules, RFQs and service lines. |
| BR-17.12 PO currency from the vendor; PRC-FX-001 | **Handoff** to waves 7 and 12 | Currency is supplied and not checked against rates. |
| PO line `requisitionLineId` must name an APPROVED requisition line | Built | Direct and consolidated POs claim the line under a lock (OPEN, APPROVED, same legal entity) and mark it CONSOLIDATED. |
| `ConsolidateLines` "vendor not blocked" (3.17.5) | **Handoff** to wave 7 | Same vendor status query as PRC-PO-008. |
| Release approved requisitions job (3.17.16, daily auto-create POs) | **Later slice of PSG-17** | `:consolidate` is the on-demand path the job will call. |
| Vendor on hold / blocked (PRC-PO-008), sanctions screening | **Handoff** to wave 7 | No vendor status query exists yet. |
| 3.17.14 inquiries and reports, 3.17.15 PO document, 3.17.16 jobs | **Later slice of PSG-17** | |
| 3.17.10 feature flags, `PRC_*` configuration, Temporal and render workers in the chart | **Later slice of PSG-17** | The chart deploys the API only. |
| 3.17.11 AX / SAP migration packs | **Handoff** to the wave 10 migration framework | |
| 3.17.19 pricing lookup p95 < 20 ms at 500 rps (in-pod cache) | **Later slice of PSG-17** | The store narrows candidates by vendor/group/product in SQL; no cache or load test yet. |
| Reconciliation checks (requisition ⇄ PO, agreement released ⇄ PO lines, ORDERED ⇄ schedule) | **Later slice of PSG-17** | Consolidation and agreements now exist; the reconciliation tests over them, and the schedule one, are not written yet. |
| Events beyond `requisition.submitted.v1`, `requisition.approved.v1` and `agreement.threshold.v1` | **Later slice of PSG-17** | Emitted by flows not yet built. |

## Operational, platform and web items

| Item | Status | Reason |
|---|---|---|
| Idempotency table | Own `idempotency_keys` (tenant, key, request hash, resource id) | Replays return the stored resource instead of a stored response body, so the kernel's `dbx.IdempotencyDDL` columns (`response_status`, `response_body`) are not used. Consistent with how `services/org` keys replays to the resource. |
| `/v1/procurement/openapi.yaml`, `/asyncapi.yaml` serving endpoints | **Later slice of PSG-17** | Specs ship as files. |
| Outbox relay / Debezium connector | **Handoff** to the eventing backbone (deploy) | The service writes outbox rows in the business transaction. |
| Helm umbrella / Argo CD application, CloudNativePG `procurement-db` | **Handoff** to `deploy/` | Chart ships in `charts/purchasing`. |
| Demo vendors with price lists, demo plant purchasing policy (ticket "Deploy") | **Handoff** to wave 7 (vendors) and the seed owner | Seed data lives outside this wave's paths. |
| Vendor, product and legal-entity pickers in the web forms | **Handoff** to waves 6–8 web modules | Forms take UUIDs until shared pickers exist. |
| Approval task inbox actions (approve, reject, delegate) | **Later slice of PSG-17** with the wave 4 workflow | The requisition page shows the chain only. |
| Consolidation board drag-and-drop between suggested POs (3.17.6) | **Later slice of PSG-17** | The board selects lines, overrides the vendor per line and sets the window; the service does the grouping. |
