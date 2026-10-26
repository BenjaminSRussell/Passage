# NOT-IMPLEMENTED — services/costing (PSG-20, wave 20, slice 1)

Every ticket deliverable and acceptance criterion not built in slice 1, with its status and reason.

| Deliverable or AC | Status | Reason |
|---|---|---|
| D1 Costing engine: FIFO, LIFO, weighted average, standard with variance capture; moving-average settlement | Built (pure engine and preview) | Persisted settlement rows (`CostSettlement`) and `cost_amount_adjustment` on inventory transactions need wave 16 transaction tables and the close job |
| D2 Inventory close/recalculation job (Temporal, per-product children, pre-check, GL adjustment vouchers, cancel) | later slice of PSG-20 | Needs Temporal workers, wave 16 transaction reads and wave 11–14 posting (`Post(JournalEntry)`) |
| D3 Inventory value report reconciling to GL by dimension | later slice of PSG-20 | Needs `CostTransaction` projection and ledger balances |
| D4 Costing versions, cost changes, revaluation | Partly built: DRAFT version create and item-cost import | Activation with revaluation voucher, `cost.changed` event, pending/active state machine and `ItemCost` supersession are a later slice |
| D5 Supplier portal: PO ack, ASN, invoice submission, document exchange | later slice of PSG-20 | Portal is a separate app and BFF; see `apps/supplier-portal/NOT-IMPLEMENTED.md` |
| D6 Parallel inventory valuation ledgers | later slice of PSG-20 | Needs wave 12 ledger groups; FR-20.15 |
| D7 Split valuation and batch valuation (valuation type dimension) | later slice of PSG-20 | Needs `ValuationCategory` storage and the per-type accounts; FR-20.16. The engine takes no valuation-type key yet |
| D8 Inventory aging report and month-end snapshots | later slice of PSG-20 | Needs cost layers after settlement; FR-20.17 |
| D9 Portal vendor requests, onboarding, catalog upload, service entry, consignment view, document distribution | later slice of PSG-20 | Portal work; see `apps/supplier-portal/NOT-IMPLEMENTED.md` |
| FR-20.1 example (FIFO 1,600.00 / LIFO_DATE 1,700.00 / periodic 1,650.00) | Built | Covered by the table-driven domain tests |
| FR-20.1 `MOVING_AVG` invoice-difference handling and `STANDARD` at ItemCost | Partly built: method accepted and valued at the supplied standard | Invoice-difference settlement and ItemCost lookup need the store and the activation slice |
| FR-20.2 close pre-checks, performance (1M txns < 20 min), locking | later slice of PSG-20 | Close job |
| FR-20.3 recalculation preview | later slice of PSG-20 | Same engine as close, without locking; needs close job plumbing |
| FR-20.4 cost transactions, daily `costing.reconcile`, value report p95 < 5 s | later slice of PSG-20 | Projection and job |
| FR-20.5 pending price import from CSV, copy with percent change, atomic activation | Partly built: JSON import into DRAFT | CSV and copy-with-percent are not built; activation is a later slice |
| FR-20.6 manual cost adjustments (`POST /api/v1/costing/adjustments`) | later slice of PSG-20 | Needs GL posting and `CostTransaction` |
| FR-20.7 PPV at receipt and invoice | Built as calculation (`/internal/v1/costing/ppv`) | Posting of the PPV voucher and `CostTransaction` kind PPV need wave 18 receipt/invoice hooks |
| FR-20.8 marking (`marked_transaction_id`) | later slice of PSG-20 | Settlement pairing by marking; needs wave 16 field |
| FR-20.9 to FR-20.22 portal features | later slice of PSG-20 | Portal app |
| FR-20.14 phase-exit walkthrough (recorded) | later slice of PSG-20 | Needs all of waves 17–20 on stage |
| FR-20.15 valuation ledgers | later slice of PSG-20 | See D6 |
| FR-20.16 valuation types | later slice of PSG-20 | See D7 |
| FR-20.17 aging | later slice of PSG-20 | See D8 |
| BR-20.3 unsettled issue fallback | Built in engine: a later receipt in the same run settles the shortage first; what is left is valued at the fallback (RUNNING_AVG of the stock before the issue, then standard, then last receipt; or STANDARD / LAST_RECEIPT) and listed as an exception | Carrying open exceptions from one close into the next, and persisting them on the close record, need the close job (later slice) |
| BR-20.5 `costing_level` SITE / ENTITY and transfers between sites | later slice of PSG-20 | The engine settles one product x site stream; grouping per level and transfer legs need wave 16 transactions and the close job |
| FR-20.5 version fields `fallback_version_id`, `site_scope`, `allow_zero_cost`; ActivateVersion checks `CST-ACT-001/002/004` | later slice of PSG-20 | Only the fields the create and import endpoints use are stored; the rest arrive with activation |
| `GET /api/v1/costing/products/{id}/cost` (cost lookup for waves 21, 27) | later slice of PSG-20 | Needs ACTIVE item costs, which only activation produces |
| Read endpoints for versions and item costs, version comparison | later slice of PSG-20 | Slice 1 writes versions and pending costs; the costing version screen that reads them comes with activation |
| §3.20.6 screens: costing workspace, inventory close, value report, costing version / item costs, cost adjustment | later slice of PSG-20 | Slice 1 web module has the settlement preview page only; each screen follows its API |
| `POST /v1/costing/settlements:preview` | Built (addition) | Not in the PRD API table: a stateless preview of the engine (FR-20.3 preview without a close) until the close job exists |
| BR-20.6 invoice difference after close | later slice of PSG-20 | Needs close and invoice posting |
| BR-20.12 advisory locks per product, `CST-LOCK-001` | later slice of PSG-20 | Needs close child workflows |
| AC-20.1 100-sequence fixture per method, reference workbook to the cent | Partly built: domain tests cover the FR-20.1 example and property tests | The 100 × 7 reference workbook (`cost_reference.xlsx`, accountant-maintained) is not in the repo; `testdata/` is empty until it is supplied |
| AC-20.2 FR example values | Built | See FR-20.1 row |
| AC-20.3 1M-transaction close and tie-out | later slice of PSG-20 | Stage performance fixture `cost-1m` and close job |
| AC-20.4 PPV 50.00 and 30.00, inventory 1,000.00 | Built for the PPV amounts (50.00 and 30.00); inventory and GR/IR effect later | GL effect needs posting |
| AC-20.5 activation of 10,000 costs | later slice of PSG-20 | Activation |
| AC-20.6 resumable close, byte-equal export | later slice of PSG-20 | Close job |
| AC-20.10 to AC-20.15 | later slice of PSG-20 | Depend on the items above |
| Events (`close.*`, `cost.changed`, `reconciliation.diff`, `agingsnapshot.created`) | later slice of PSG-20 | Slice 1 has no outbox writes; listed in `events/asyncapi.yaml` under `x-planned-events`. The PRD's `costing.aging.snapshot.created.v1` has one segment too many for the kernel type pattern, so it is planned as `dev.passage.costing.agingsnapshot.created.v1` |
| Migrations `inventory/v20.*` in the shared inventory DB | handoff | This service keeps its own migration `migrations/0001_costing.sql`; the schema placement in `inventory-db` is a deploy decision (deploy/ is outside this wave's ownership) |
| Feature flags `cst.enabled`, `cst.moving_avg`, `cst.marking` | handoff | Flag wiring lives in deploy/ and the platform config, both outside this wave's ownership |
| Jobs (reconciliation 02:15, ack-overdue 08:00 etc.) | handoff for scheduler config; later slice for job code | Schedules belong in deploy/; job code needs the close and projection work |
| Temporal close workers (`passage-costing` chart, 4 workers) | later slice of PSG-20 | Slice 1 chart deploys the API only |
| Monitoring alerts, runbooks | handoff | Alert rules live with the observability config in deploy/ |
| Portal Keycloak realm `suppliers` | handoff | Realm provisioning is in deploy/ and the edge-identity wave |
| Authentication via JWT / OPA | later slice (platform wave 2 and edge-identity) | Slice 1 reads the gateway's trusted tenant and duty headers, as the reference service does |
| Idempotency middleware (`idempotency_keys` table from `dbx.IdempotencyDDL`) | Partly built: `Idempotency-Key` stored on the version row with unique (tenant, key) | The generic middleware is a kernel item; not requested from wave 0 in this slice |
| Vendor-facing activity audit (`audit_event`) | later slice of PSG-20 | Needed for P3 data; slice 1 stores no P3 |
