# Manufacturing

## Purpose & Boundaries
Context `manufacturing` (database `passage_manufacturing`; waves 21-25). Deployables: `manufacturing` (Go: production orders, MRP, scheduling, OEE), `manufacturing-ts` (Temporal worker: ECO release, planning jobs, production-order settlement saga S3b; wave 21; joined inventory after review), `manufacturing-py` (forecasting ETS/Croston via statsforecast, CP-SAT optimizer). Owns production orders, planning, schedules, kanban, time registration, machine events. Posts WIP relief/labor/overhead inside RAF itself; inventory posts FG receipt valuation.

## Interfaces
Prefix `/v1/manufacturing/`: `production-orders/{id}:estimate|:schedule|:release|:start|:report|:report-as-finished|:end|:cancel`; `picking-lists/{id}:post`; `subcontracts/{id}:send-material`; `planning-runs` (202; mode full|netChange) + `/{id}`; `planned-orders/{id}/pegging` (explains 100% of qty), `:firm|:unfirm`; `planning-messages` (+`:acknowledge`); `forecast-runs`; `schedules:run`, `schedules/{id}/gantt`, `:commit`; `kanban-cards/{id}:scan`; `time-registrations` (device class 120/min); `machine-events:ingest` (Svc device cert, HTTP fallback); `machine-events` (7-day retention); `oee` (ISO 22400, 5 s refresh); `GET /reconciliation/{check}` (wip).
- RAF: `:report-as-finished` idempotency key = rafId; posts via `ledger.Post` key `post:manufacturing:productionorder:{rafId}:wip-relief`, then finalizes in one local tx; emits `reportedasfinished.v1`. Compensation `POST .../raf/{rafId}:reverse` -> `rafreversed`.

## Data Ownership
EN-053..055: `production_order` (F-ish profile, immutable only after `ended`; status created|estimated|scheduled|released|started|reported_finished|ended|cancelled — cancel only from created..released; BOM/route snapshot copied; wip_journal_entry_ids[], wip_gl_seqs[], variance_journal_entry_id, valuation_journal_entry_id/valuation_gl_seq, gl_posting_status) with children prod_bom_line, prod_route_operation, picking_list(+line), route_card, report_as_finished, production_variance (types lot_size|quantity|price|substitution|efficiency|rate; sum exact), production_order_event; planning_run/planned_order/planned_order_peg/planning_message/coverage_group/item_coverage; schedule/schedule_job (GiST no-double-booking), kanban_rule/card; time_registration (IL monthly partition on work_date); machine_event (I, daily partition, PK (tenant_id, event_time, id), 7 d hot); oee_aggregate. Indexes IX-MF-01..24.

## Events Produced
`manufacturing.productionorder.released.v1`, `.reportedasfinished.v1`, `.rafreversed.v1` [P], `.ended.v1` (variances[] sum exactly to actual-standard), `manufacturing.plannedorder.firmed.v1`, `manufacturing.schedule.changed.v1`, `manufacturing.machineevent.received.v1` (48 partitions, key tenantid:resource_id, 7 d retention).

## Events Consumed
`manufacturing-planning` (inventory.transaction.posted, purchaseorder.confirmed, salesorder.confirmed, schedule.changed), `manufacturing-bom`, `manufacturing-changeorder`, `manufacturing-orders` (plannedorder.firmed, schedule.changed), `manufacturing-oee`, `manufacturing-shopfloor-bff`, `manufacturing-raf-valuation` (inventory.transaction posted/rejected referenceType=productionorder -> sets gl_posting_status/valuation ids), `manufacturing-saga-bridge` (productionorder.ended, inventory.transaction posted/rejected -> signalWithStart S3b), `manufacturing-reservations`, `manufacturing-product-ref`, `manufacturing-periodcache`, `manufacturing-numbersequence`.

## Dependencies
internal: ledger (sync Post), inventory, product, sales/procurement (demand/supply refs), warehouse, workforce, projects (condition triggers consume machine events). external: Temporal, OR-Tools, OPC-UA/MQTT edge connector (no deployable yet; proposed `iot-edge` Go, wave 25, 24 h store-and-forward), Kafka.

## Gotchas & Constraints
- S3b workflow id `production-settlement:<tenantid>:<productionOrderId>`; step 1 `PostVarianceSettlement` key `post:manufacturing:productionorder:<orderId>:settlement`; step 2 awaits `inventory-prodcost` transaction.posted (15 min timer); reopening only via reversing settlement + later `ended` with higher sequence.
- Go engines expose job endpoints; TS workflow starts them and awaits `*.completed` as Temporal signal (no Temporal Go SDK).
- MRP determinism: same output across runs, input permutations, goroutines; incremental = full regeneration; NFR-P5 50k items x 12 periods x 5 levels < 10 min; scheduling 5k operations < 60 s.
- OEE aggregates are async projections (R, from machine_event). machine events 10k/min/plant.
- State-machine illegal transitions return `state_transition_invalid`.
- Picking/route consumption to WIP follows S2 pattern (Dr WIP / Cr Inventory).

## Source Sections
- Backend 5.11 (757-788), 3.2 (291); Data EN-053..055 (2917-2970); Events 3.7 (4042-4052), 9.3 S3a/S3b (4534-4584); consumer rows (4212-4218, 4286-4298)
