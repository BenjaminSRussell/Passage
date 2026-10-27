# Not implemented in PSG-24 slice 1 (processmfg)

Built in slice 1: formulas with their lines (FR-24.1, PMF-0001), batch orders from a formula
snapshot scaled to the batch size (FR-24.2, BR-24.1), rescale before Started with optimistic
versioning, Idempotency-Key on every command, per-legal-entity batch-order numbers, outbox
events, RLS with legal-entity scope; the potency (BR-24.2), co-product allocation (BR-24.3),
catch-weight (BR-24.5), shelf-life (FR-24.6, BR-24.4) and weighted-average (BR-24.11) rules;
and the web screens in `apps/web/src/modules/processmfg`.

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: batch order state machine (release, start, report as finished, end) and journals "as w21" | later slice of PSG-24 | slice 1 stores orders in `planned` and allows rescale while planned or released; status transitions and journals reuse the wave-21 order engine (services/production), which this slice does not call yet |
| Deliverable 1: co-product allocation on an order (`/batch-orders/{id}/co-products:allocate`, End with journal lines, `co-product.allocated` event, CoProductLine table) | later slice of PSG-24 | the allocation rule is built and exposed as a preview (`POST /v1/production/co-products:allocate`); binding it to an order's actual cost needs the wave-21 End and the ledger posting |
| Deliverable 1: potency role on items, pickability of batches outside min/max, consumption proposal (FR-24.3, AC-24.2 end to end) | partial | `ConsumePotency` and `PotencyInRange` are built and tested against pmf-potency-20 and exposed as a calculator; the picking proposal needs wave-16 inventory batches; `PMF_POTENCY_TOLERANCE_PCT` is not read |
| Deliverable 2: batch attributes and dispositions (PUT `/inventory/batches/{id}/attributes`, `:disposition`, history, `batch.attributes-recorded` event, AC-24.10) | later slice of PSG-24 | needs the wave-16 InventoryBatch; no tables yet |
| Deliverable 2: shelf life at RAF, FEFO on batch orders, expired-batch override duty (FR-24.6, BR-24.10, AC-24.4 `BATCH_EXPIRED`) | partial | `ShelfDates` and `ShortestExpiry` are built and tested; RAF, picking and the override belong with the order engine and inventory batches; the shelf-advice job is not built |
| Deliverable 2: catch-weight dual UoM on inventory and production transactions (FR-24.7, AC-24.4 of the ticket) | partial | `ValidateCatchWeight` (BR-24.5) is built and exposed as a calculator; `cw_qty` on InventoryTransaction and journal lines is a handoff to wave 16 (inventory) and wave 21 (production) |
| Deliverable 5: recipe and formula version control through ECO | handoff | `formula.bom_version_id` points at the wave-9 BOM version and orders snapshot the formula version; formula versioning and ECO submit belong to services/engineering (wave 9). Formulas cannot be edited in slice 1 |
| Deliverable 6: inheritance rules, derivation log and monitor (FR-24.15, AC-24.11) | partial | `WeightedAverage` (BR-24.11) and `ShortestExpiry` (BR-24.4) are built; InheritanceRule, DerivationLog, the re-derivation job and the monitor are later slices |
| Deliverable 6: batch merge `:merge` (FR-24.16, AC-24.12) | later slice of PSG-24 | needs the batch store and the transfer posting |
| Deliverable 6: documentary batches, tracking policy `batch-record-only` (FR-24.24) | handoff | the tracking dimension group is wave 16 |
| Deliverable 7: consolidated bulk and packed orders, BulkPackRelation, consolidation job (FR-24.17, BR-24.12, AC-24.13) | later slice of PSG-24 | needs planned and firmed orders from wave 22 |
| Deliverable 7: rework batch orders (FR-24.18, AC-24.18) | later slice of PSG-24 | needs the batch store and the wave-33 quality order link |
| Deliverable 7: same-batch reservation, customer sellable days and batch requirements (FR-24.19, BR-24.13, AC-24.14, `SAME_BATCH_UNAVAILABLE`) | handoff | sales lines arrive in wave 27; the rules apply in the wave-27 reservation and the wave-28 picking |
| Deliverable 9: electronic batch record, review by exception, sealed PDF/A and JSON archive (FR-24.21, AC-24.16) | later slice of PSG-24 | needs the document service (wave 5), the PI sheets (wave 25) and the signature service |
| Deliverable 9: 21 CFR Part 11 electronic signature service (FR-24.22, BR-24.14, AC-24.15) | later slice of PSG-24 | cross-module service (wave 33 and wave 48 reuse it) that needs Keycloak step-up re-authentication (wave 2); not built in slice 1 |
| Deliverable 10: characteristic-dependent planning (FR-24.23) | handoff | the planning engine is services/planning (wave 22) |
| Deliverable 8 (lean subcontracting) | handoff | services/lean (see its NOT-IMPLEMENTED.md) |
| AC-24.5 numeric example | deviation | AC-24.5 says a 10-case RAF at 95 kg fails for nominal 10 kg, min 9, max 11. BR-24.5 (min × cw ≤ qty ≤ max × cw) accepts 95 kg with min 9 (90 ≤ 95). The test uses a per-unit minimum of 9.8 kg, which reproduces the AC outcome; the rule follows BR-24.5. The PRD example needs fixing |
| Event names in PRD 4.24.5 | deviation | the PRD writes `passage.production.batch-order.created.v1`; the kernel rule `dev.passage.<context>.<entity>.<verb>.v<n>` allows no hyphen, so the type is `dev.passage.production.batchorder.created.v1` (see events/asyncapi.yaml) |
| Formula lines: `potency_base_item`, `unit_id`, `scrap_pct`, `allocation_pct`, `by_product_cost_type` (PRD 4.24.4) | later slice of PSG-24 | the columns arrive with potency proposals and order-level co-product allocation, which use them |
| `/metrics` on an admin port | later slice of PSG-24 | no counters yet; the chart's ServiceMonitor is off by default |
| Deploy: second demo plant (food and chemicals line), flags `prod.process`, `prod.catch-weight` | handoff | deploy/ and the Helm umbrella belong to the deployment owner |
| Jobs: shelf-advice notification, consolidation, inheritance re-derivation, EBR sweeps | later slice of PSG-24 | need Temporal (wave 4) and the entities above |
| Duty entries for `production.formula.write`, `production.batch-order.read`, `production.batch-order.write` in OPA data | handoff | policies/ is outside this wave's ownership; the service enforces duties from forwarded headers |
| Label, CoA and batch ticket documents (PRD 4.24.15) | handoff | w5 document service, w32 print service, w48 country packs |
| Performance budgets and load tests (PRD 4.24.19) | later slice of PSG-24 | no load path yet |
| Web: formula designer with ECO submit, batch attributes and disposition screens, EBR review, signature prompts | later slice of PSG-24 | the backing endpoints are not built (see above); the module's README lists the screens that exist |
