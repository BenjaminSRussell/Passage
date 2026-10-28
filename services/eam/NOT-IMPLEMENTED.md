# NOT-IMPLEMENTED — services/eam (PSG-44, slices 1 and 2)

Every ticket deliverable and acceptance criterion that slices 1 and 2 do not build, with
its status and reason. Status is either `later slice of PSG-44` or `handoff` (needs
work outside the owned paths).

## Deliverables (ticket section and PRD FR)

| # | Item | Status | Reason |
|---|---|---|---|
| D1 | Functional locations, ltree path, location history (FR-44.1) | later slice of PSG-44 | Slice 1 focuses on the asset-to-plan-to-WO path. Assets have no location or parent. |
| D1 | Asset hierarchy (parent asset), install/remove history, asset type entity | later slice of PSG-44 | Not needed for the scheduler path. Asset type is a text code for now. |
| D1 | BOMs and spares, measurement points beyond counters | later slice of PSG-44 | Spares depend on wave-16 inventory. |
| D1 | Asset edit (name, type, site, production resource, manufacturer/model/serial, fixed asset link) | later slice of PSG-44 | Slice 2 builds the lifecycle (activate, retire to INACTIVE, dispose); the other asset fields are set at creation only. Criticality changes through assessments. |
| D2 | Plan checklists, est. hours, planned parts (FR-44.4) | later slice of PSG-44 | Requires checklist and parts tables. |
| D2 | Shift factors (BR-44.1 (a)), merge window (BR-44.1 (e)), counter release tolerance (BR-44.1 (f)) | later slice of PSG-44 | Next-due uses last completion only. Work orders are created one per line, not merged. |
| D2 | Plan scope by asset type or location; calendar_id | later slice of PSG-44 | Plan lines are per asset. |
| D2 | Request merge (8.44.6 Request triage "merge") | later slice of PSG-44 | Slice 2 builds triage, defer and reject with history; merging two requests into one is not built. |
| D2 | Maintenance rounds (FR-44.14), sequences (FR-44.15) | later slice of PSG-44 | Separate aggregates and scheduler branches. |
| D3 | Work order jobs, checklists and mandatory-item completion check (EAM-WO-004) | later slice of PSG-44 | No job or checklist tables yet. |
| D3 | Scheduling against wave-23 resources and team capacity (FR-44.8) | handoff (wave 23) | Wave 23 owns the resource model and capacity. Slice 2 emits `downtime.recorded` for recorded downtime windows; planned downtime of SCHEDULED/RELEASED orders (tentative, then firm at RELEASED, BR-44.5) needs planned_start/planned_end, which work orders do not carry yet. |
| D3 | Parts reservation, pick and issue through wave-16 (FR-44.9, BR-44.6) | handoff (wave 16) | Needs the inventory reservation and journal API. The `work_order_part` table exists but no endpoint writes it. |
| D3 | Labor from the wave-25 kiosk or mobile app (FR-44.10) | handoff (wave 25 / PWA) | Only the API booking endpoint is built. |
| D3 | Cost posting to asset and GL via ledger.Post, reconciliation (FR-44.11, AC2) | handoff (ledger) | The WO event carries costs (`workorder.completed.v1`). Ledger posting is a synchronous call that this slice does not make. WO-cost-to-GL reconciliation is therefore not tested. |
| D3 | CAPITAL work orders with a fixed asset or CIP link (FR-44.11) | later slice of PSG-44 (fixed asset: wave 37) | No fixed-asset or CIP link exists yet, so every CAPITAL order is refused with EAM-WO-005. |
| D3 | ROUND and REFURBISHMENT work order types | later slice of PSG-44 | Not in the default enabled set (8.44.13); the API refuses them until rounds (FR-44.14) and refurbishment (FR-44.20) exist. |
| D3 | Closing with pending journals (EAM-WO-002), CLOSED transition from costs (BR-44.3) | later slice of PSG-44 | Depends on ledger posting. CLOSED is allowed without checks. |
| D3 | Emergency regularization alert (EAM-WO-003, BR-44.7) | later slice of PSG-44 | Needs the hourly `eam-emergency-regularize-check` job. |
| D3 | ON_HOLD reasons | later slice of PSG-44 | Not modeled. Failure codes are built in slice 2 as fault registration (FR-44.16). |
| D4 | Condition consumer for the wave-25 IoT stream (Kafka) | handoff (wave 25 / eventing) | Slice 1 accepts readings through `POST /counters/{id}/readings` with source IOT. The bridge that calls it is not built. |
| D4 | RATE_OVER(window) rules, tag-based rules (BR-44.4) | later slice of PSG-44 | Only threshold rules (`>`, `<`) on counters exist. |
| D4 | p95 < 5 s latency target (AC3) and 10 readings/s performance test | later slice of PSG-44 | The breach path is synchronous and tested for correctness. Load test is not run. |
| D4 | Predictive hooks (`eam.predictive-hooks` flag) | later slice of PSG-44 | Hooks only; no model in v1. |
| D5 | Asset KPIs by location, type and criticality; cost per asset; backlog hours (FR-44.13) | later slice of PSG-44 | Only MTBF, MTTR and availability per asset exist. Cost and backlog need the GL and the WO list views. |
| D5 | `eam-kpi-rollup` hourly job, `/reports/eam/kpis` path | later slice of PSG-44 | KPI is computed on request at `GET /v1/eam/kpis`. |
| D6 | Fault analysis (FR-44.16 analysis by asset, type, symptom, cause and period; 8.44.14 Fault analysis with Pareto and 30-day repeat rate; Fault report document) | later slice of PSG-44 | Slice 2 builds the catalog designer, fault registration and the completion gate. The analysis needs a reporting projection (`eam_faults`). |
| D6 | Fault registration required per work order type as a parameter (8.44.13) | later slice of PSG-44 | Hard-coded to the default: required for CORRECTIVE and EMERGENCY. |
| D6 | Warranty duration in counter units (`counter_id`, `counter_limit`), warranty check at each part issue, per-line coverage flags (BR-44.14) | later slice of PSG-44 (part issue: handoff to wave 16) | Slice 2 checks at work order creation on the order date and flags the whole order. Part issue is not built (D3 wave 16 row), so claims carry the order's parts cost when parts are covered. |
| D6 | Voiding a warranty, warranty inquiry across assets (8.44.14), `eam-warranty-expiry` job, warranty check on WO create as a site parameter | later slice of PSG-44 | The `status` column allows VOIDED but no endpoint sets it. Warranties are listed per asset. The check always runs. |
| D6 | Vendor warranty claim (FR-45.15) | handoff (wave 45) | Slice 2 emits `dev.passage.eam.warranty.claimable.v1` at completion; wave 45 creates the claim. |
| D7 | Permits to work and LOTO (FR-44.19, BR-44.11, AC11) | later slice of PSG-44 | Work orders do not yet gate on permits. |
| D8 | Maintenance budgets (FR-44.18, BR-44.13), budget event | later slice of PSG-44 (budget transfer: handoff to wave 38) | Needs plan forecasting and the wave-38 budget API. |
| D8 | Refurbishment orders and condition valuation (FR-44.20, BR-44.12) | later slice of PSG-44 (condition dimension: handoff to wave 16) | Needs the wave-16 condition dimension, which is not available. |
| D8 | Fleet attributes and fuel log (FR-44.21) | later slice of PSG-44 | Requires worker assignment (wave 42). |
| D8 | Plan review reminder for assets with criticality ≥ 4 every 12 months (BR-44.15), site criticality → priority map (8.44.13) | later slice of PSG-44 | Slice 2 builds the versioned assessment matrix, the derived criticality and the priority default 6 − criticality; the reminder job and the per-site map are not built. |

## Acceptance criteria (PRD 8.44.8 and ticket)

| AC | Status | Note |
|---|---|---|
| AC-44.1 | built | `TestScheduler_BR441Example` (store) runs the example end to end: readings give 6.5 h/day over 30 days, the line carries its 2026-08-01 / 12,000 h completion, and scheduling on 2026-10-21 creates a work order due 2026-10-30 with next due reading 12,500 and forecast 2026-11-21. |
| AC-44.2 | later slice of PSG-44 | Needs the 40-plan fixture set (`testdata/eam-plans-40`). |
| AC-44.3 | handoff (ledger) | Labor cost accumulates on the work order (8 h at 45.00 = 360.00, store test). Parts issue and the GL posting to the maintenance expense account (710.00 with parts) are not built. |
| AC-44.4 | partial | Breach creates one request in the reading transaction and dedupes while it is open and within the cooldown (store tests). The 5 s latency target is not measured. |
| AC-44.5 | handoff (wave 23) | Slice 2 records downtime windows on the production resource and emits `downtime.recorded` in the same transaction (`TestDowntime_BR445`). Zeroing M-12's capacity is wave 23's consumer; planned downtime of RELEASED orders is not built (D3). |
| AC-44.6 | built | Store test: EAM-CTR-001. |
| AC-44.7 | later slice of PSG-44 | Needs checklists. |
| AC-44.8 | built | Domain test and store test (`TestAssetKPI_AC448`): MTBF of 900 h for 3 failures in 2,700 h of uptime. |
| AC-44.9 | later slice of PSG-44 | Needs rounds. |
| AC-44.10 | later slice of PSG-44 | Needs sequences. |
| AC-44.11 | later slice of PSG-44 | Needs permits and LOTO. |
| AC-44.12 | later slice of PSG-44 | Needs refurbishment and the wave-16 condition dimension. |
| AC-44.13 | partial | `TestWarranty_AC4413`: a 24-month parts-only warranty from 2025-11-01 covers an order created on 2026-10-21, and completion emits `warranty.claimable` with 350.00 of parts and 0.00 of labor. Part issue itself is a wave-16 handoff, so the test sets the parts cost; coverage is per order, not per part line. |
| AC-44.14 | later slice of PSG-44 | Needs budgets. |
| AC-44.15 | built | `TestFault_AC4415` (store) and `TestHTTP_FaultCatalogAndCompletion` (HTTP): EAM-FLT-001 without a fault, EAM-FLT-002 for a combination the asset type does not allow. |
| AC-44.16 | built | `TestCriticality_AC4416`: SAFETY 3, PRODUCTION 5, likelihood 4 give risk 20 and criticality 5, and a new CORRECTIVE order defaults to priority 1. |

Ticket acceptance criteria: AC1 partially built (the BR-44.1 example and a time line
with its completion reset run against the database; the 40-plan fixture is not built). AC2 handoff (see D3 above).
AC3 partial (see D4). AC4 handoff (wave 23 consumes `downtime.recorded`; see AC-44.5). AC5 satisfied by this file. AC6
satisfied for the Go module; the web checks pass.

## Other gaps in slice 1

| Item | Status | Reason |
|---|---|---|
| Idempotency for `POST /plans/schedule` | later slice of PSG-44 | Re-runs are safe by construction (one open WO per line). No stored key. |
| Kernel httpx idempotency middleware (`idempotency_keys` table) | handoff (platform kernel) | Slice 1 stores keys on each aggregate row instead. |
| Work order and asset numbers from wave-5 number sequences (site scope) | handoff (wave 5 / platform) | Slice 1 uses global sequences `WO-n` and `AST-n`. |
| `dev.passage.eam.budget.calculated.v1`, `dev.passage.eam.permit.status.changed.v1` | later slice of PSG-44 | Their features are not built. `downtime.recorded`, `warranty.claimable` and `request.triaged` are emitted from slice 2. |
| Criticality and lifecycle events | later slice of PSG-44 | The PRD names none; assessments and lifecycle changes are not published. |
| ETag reads, search by text, list filters beyond status and asset | later slice of PSG-44 | Lists page by cursor with status and asset filters; versions travel in the body and If-Match. |
| Request merge | later slice of PSG-44 | See D2. |
| Work order currency from the site or legal entity | later slice of PSG-44 (handoff: wave 6 org) | Conversion in the web module sends USD, the service default; plan-generated orders use the plan currency. |
| Mobile offline replay (8.44.6, 72 h) | later slice of PSG-44 | PWA app is outside this service. |
| Nightly CronJob for `eam-plan-schedule`, Kafka consumer deployment, mobile BFF | handoff (deployment-cicd) | The chart has only the API deployment. The schedule sweep endpoint is ready to be called by a CronJob. |
| Monthly partitioning of `counter_reading` (8.44.4) | later slice of PSG-44 | Single table in slice 1. |
| Legal-entity scoping (`dbx.LegalEntityScopePolicy`) | later slice of PSG-44 | No legal entity column on EAM tables yet. |
| Parameters 8.44.13 (number sequences per site, tolerances per tenant, cooldown overrides) | later slice of PSG-44 | Hard-coded defaults in slice 1 (cooldown 60 min, tolerance 10%, horizon 90 days). |
| Web: plant structure tree, work order board (kanban + Gantt), technician mobile PWA, KPI dashboard, round/sequence editor, fault analysis, permit board, budget, rotables (8.44.6) | later slice of PSG-44 | Built: workspace tiles, asset list/create/detail with counters, readings, rules, KPIs, lifecycle (activate, retire, dispose), criticality matrix and versions, and warranties; plan list and editor; request list with convert, triage, defer and reject, and create; work order list/create/detail with status actions, completion, labor, fault registration, downtime windows and history; fault catalog list and designer. Workspace tile "backlog hours" is shown as open work orders until jobs carry estimated hours. Shell owner: no Needs. |
