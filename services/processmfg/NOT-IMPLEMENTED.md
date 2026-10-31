# Not implemented in PSG-24 slices 1 to 3, 9 and 11 (processmfg)

Built in slice 1: formulas with their lines (FR-24.1, PMF-0001), batch orders from a formula
snapshot scaled to the batch size (FR-24.2, BR-24.1), rescale before Started with optimistic
versioning, Idempotency-Key on every command, per-legal-entity batch-order numbers, outbox
events, RLS with legal-entity scope; the potency (BR-24.2), co-product allocation (BR-24.3),
catch-weight (BR-24.5), shelf-life (FR-24.6, BR-24.4) and weighted-average (BR-24.11) rules;
and the web screens in `apps/web/src/modules/processmfg`.

Built in slice 2, as stateless previews over batches the caller passes in (no tables, no
events): batch derivation with every FR-24.15 aggregation and the missing-sender warning
(PMF-0007); batch merge with attribute recomputation, weighted cost and parents (FR-24.16,
PMF-0006); bulk consolidation sizing with due-date pegging of packing orders (FR-24.17,
BR-24.12); the customer batch requirement check with field-by-field precedence, exclusion
reasons (PMF-0009) and same-batch selection (FR-24.19, BR-24.13, 409 PMF-0008); the EBR
approval gate (BR-24.15, PMF-0014); the domain checks of the signature service (BR-24.14,
PMF-0010 to PMF-0013); and their screens in `apps/web/src/modules/processmfg`.

Built in slice 3, again as stateless previews with screens: the batch attribute check against
the item's attribute definitions, flagging values outside min/max as exceptions (FR-24.5,
`POST /v1/production/batch-attributes:check`); the picking proposal that excludes batches by
disposition (AC-24.10, PMF-0016), expiry (AC-24.4, PMF-0003) and potency band (FR-24.3,
PMF-0005), honours the consume-expired override with its audit reason (BR-24.10) and takes the
rest FEFO (FR-24.6, `POST /v1/production/batch-picking:proposal`); and the rework batch order
plan with the FR-24.18 parameters (`POST /v1/production/rework-orders:preview`). The swarm draft
of slice 3 also re-implemented lean flow and kanban-rule persistence; main already held the
reviewed slice-1 version of that, so the duplicate was dropped (see services/lean).

Built in slice 9: the batch-order lifecycle and co-product cost allocation on an order
(Deliverable 1, FR-24.4, BR-24.3, AC-24.3 and the ticket's AC2). `:release`, `:start`,
`:report-finished` and `:end` move a planned order through released, started and reported to
ended, each with the order version, an Idempotency-Key (a retry replays the first answer and
writes no second event) and one `batchorder.statechanged` event in the same transaction. Report
as finished records the main quantity and the reported quantity of each co- and by-product line
(a line left out is reported at its plan); End takes the batch cost (platform money rounding, a
cost with more decimals than the currency's minor unit is refused) and the allocation method,
splits it with the BR-24.3 rule over the main item, the co-products and the by-product
burdens, stores the shares in `batch_order_allocation` (migration 0002; the shares add up to the
batch cost exactly, checked in the rule and again by the test against the stored rows; when the
main item weighs nothing, as with a zero standard cost under TCA, the few ten-thousandths that
rounding the co-products up can overshoot come off the largest co-product, so no share is
negative), writes
`dev.passage.production.coproduct.allocated.v1` in the same transaction, and the order then
carries its `allocation`. Rescale is refused from Started on (PMF-0105). The order detail screen
gains the release, start, report and end steps and the cost split. Both AsyncAPI documents
(processmfg and lean) now pass the documentation gate with no FAIL lines.

Built in slice 11: the batch quality record (Deliverable 2, FR-24.5, AC-24.10). The service keeps
its own register of the batches it records quality data for (`batch_record`, one per item and
batch number, migration 0003) until the wave-16 inventory batch store exists. Attribute
definitions per item (`POST` and `GET /v1/production/batch-attribute-definitions`, unique per item
and name, numeric limits and enum options validated by `domain.ValidateDefinitions`, and the PRD
`role` generic or potency, with at most one numeric potency attribute per item); batches
(`POST /batches` with the caller's legal entity by default and an optional `batchOrderId` that must
name a batch order of the same legal entity in the caller's scope, `GET /batches` filtered by item and
disposition, `GET /batches/{id}` with the current value of each attribute, the full attribute
history and the disposition history); `PUT /batches/{id}/attributes` records values through
`domain.CheckAttributes` (an undefined attribute, a wrong type or a non-option is refused with
COM-0001, a numeric value outside min/max is recorded with `pass = false`), appends them to an
append-only history, sets the batch's expiry date from a date recorded under the `expiry`
attribute (FR-24.6) and writes `batch.attributesrecorded` with the failure count and the value of
the item's potency attribute when recorded; `POST /batches/{id}:disposition` moves a batch to
available, on-hold, quarantine or rejected with a required reason, a history row and
`batch.dispositionchanged`. A disposition change by a user who recorded attribute values on the
same batch is flagged `segregationConflict` on the history row and the event (PRD 4.24.17), not
refused. Every command takes an
Idempotency-Key and the batch version (stale version PMF-0104, an unchanged disposition PMF-0111,
PMF-0108/0109/0110 for not found and duplicates). The disposition command has its own duty,
`production.batch-disposition.write`, apart from `production.batch-attribute.write`, so the person
who records test values need not be the one who releases or blocks the batch. The web module gains
the batch list, register form, batch detail (recording form per definition, current and historical
values, disposition form hidden without the duty, both histories) and the attribute definitions
page. The picking proposal and the other previews still take their batches as input; reading the
stored batches into them is the row below.

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: journals "as w21" on release, start, report and end (material issue, finished-goods receipt, WIP) | handoff | the status machine and the cost split are built here; posting issues, receipts and WIP journals is the wave-21 order engine (services/production) and the ledger, which consume `batchorder.statechanged` and `coproduct.allocated`. The `closed` status (period close) is not entered by this service; the batch cost is entered by the caller at End, because actual cost comes from wave-20 inventory costing and wave-21 WIP |
| Deliverable 1: co-product allocation on an order: standard costs and by-product rates read from item cost records, the `/co-products:allocate` command path, CoProductLine table, ledger journal lines | partial | the allocation is bound to the order and stored at End (slice 9), with standard costs, percentages and by-product rates entered by the caller; the stand-alone preview stays at `POST /v1/production/co-products:allocate`. Reading standard costs from wave-20 costing, the per-line `allocation_pct` and `by_product_cost_type` formula columns, reversing an ended order's allocation, and the journal lines for the ledger are later slices |
| Deliverable 1: potency role on items, pickability of batches outside min/max, consumption proposal (FR-24.3, AC-24.2 end to end) | partial | `ConsumePotency` and `PotencyInRange` are built and tested against pmf-potency-20 and exposed as a calculator; slice 3 adds the potency band to the picking proposal (`domain.CheckPickable`, PMF-0005) over batches the caller passes in. Reading the batches and the item's potency role from wave-16 inventory, and proposing the potency-adjusted quantity per picked batch on a batch order, need the batch store and the order engine; `PMF_POTENCY_TOLERANCE_PCT` is not read |
| Deliverable 2: batch attributes and dispositions (PUT `/inventory/batches/{id}/attributes`, `:disposition`, history, `batch.attributes-recorded` event, AC-24.10) | partial | built in slice 11 under `/v1/production/batches` on this service's own batch register (the PRD path `/inventory/batches` is routed to services/inventory). Left: linking `batch_record` to the wave-16 InventoryBatch (the register is keyed by item and batch number, with no quantity or location), the attribute-definition editing and retiring (definitions are insert-only), date limits on date attributes (the type is checked, the range is not), making the picking proposal and the customer batch requirement check read the stored disposition and expiry instead of caller-supplied batches, and batch status changes from inventory postings. The PRD calls the PUT a full replace; this slice appends the values sent and keeps the others current (nothing is cleared), and the definition's `unit_id` is not modeled |
| Segregation of *Maintain batch attributes* and *Maintain batch disposition* (PRD 4.24.17) | partial | the service enforces `production.batch-attribute.write` and the new `production.batch-disposition.write` on separate routes and flags a disposition change by a user who recorded attributes on the same batch (`segregationConflict`). Not built: flagging the reverse order (attributes recorded by the user who last changed the disposition), and the static conflict check when one role is granted both duties, which belongs to the role and duty administration of the security service; seeding the new duty in policies/authz/data.json is the handoff below |
| Duty entry for `production.batch-disposition.write` in OPA data (and the issue #313 list) | handoff | policies/ is outside this wave's ownership; the nav items stay gated on duties that already exist (`production.batch-order.read`, `production.batch-attribute.write`) and the new duty is enforced in the service and on the disposition form only |
| Deliverable 2: shelf life at RAF, FEFO on batch orders, expired-batch override duty (FR-24.6, BR-24.10, AC-24.4 `BATCH_EXPIRED`) | partial | `ShelfDates` and `ShortestExpiry` are built and tested; slice 3 builds FEFO, the PMF-0003 exclusion and the override with duty `production.batch.consume-expired` and a reason of at least 10 characters in the picking proposal. Dates at RAF, the audit-log entry of the override reason (it is returned as `expiredOverride` for the EBR exception, not stored) and picking on a real batch order belong with the order engine and inventory batches; the shelf-advice job is not built |
| Deliverable 2: catch-weight dual UoM on inventory and production transactions (FR-24.7, AC-24.4 of the ticket) | partial | `ValidateCatchWeight` (BR-24.5) is built and exposed as a calculator; `cw_qty` on InventoryTransaction and journal lines is a handoff to wave 16 (inventory) and wave 21 (production) |
| Deliverable 5: recipe and formula version control through ECO | handoff | `formula.bom_version_id` points at the wave-9 BOM version and orders snapshot the formula version; formula versioning and ECO submit belong to services/engineering (wave 9). Formulas cannot be edited in slice 1 |
| Deliverable 6: inheritance rules, derivation log and monitor (FR-24.15, AC-24.11) | partial | the derivation rule (`domain.Derive`, every aggregation, PMF-0007) is built and served as `POST /v1/production/batch-derivation:preview`. The InheritanceRule and DerivationLog tables, derivation at RAF, manual re-derivation with forward propagation (max depth 10), the derivation monitor, the `inheritance active` parameter and the nightly re-derivation job need the wave-16 batch store and RAF on the order engine, so they are later slices |
| Deliverable 6: batch merge `:merge` (FR-24.16, AC-24.12) | partial | the target computation (`domain.Merge`) is built and served as `POST /v1/production/batch-merge:preview`. Posting the transfers from each source batch, writing genealogy and the `merge-override` attribute setting need the wave-16 batch store; the PRD command path `/inventory/batches:merge` is routed to services/inventory, so the command lands there or under `/v1/production` with the store slice |
| Deliverable 6: documentary batches, tracking policy `batch-record-only` (FR-24.24) | handoff | the tracking dimension group is wave 16 |
| Deliverable 7: consolidated bulk and packed orders, BulkPackRelation, consolidation job (FR-24.17, BR-24.12, AC-24.13) | partial | sizing and due-date pegging (`domain.Consolidate`) are built and served as `POST /v1/production/bulk-consolidation:preview`. The BulkPackRelation table, reading planned and firmed packed orders from wave 22 within the consolidation window (default 7 days), creating the bulk and packing orders, the same-batch reservation of the bulk batch and the daily 21:00 job are later slices |
| Deliverable 7: rework batch orders (FR-24.18, AC-24.18) | partial | slice 3 builds the plan (`domain.PlanRework`, `POST /v1/production/rework-orders:preview`): part or all of the batch as first ingredient, keep-batch-number, require-quality-order, output disposition (default quarantine, never available behind a quality order), rework vs capitalized variance, genealogy parent. Creating the rework BatchOrder with extra ingredients from a rework formula, receiving the output to the batch at RAF, re-recording attributes and writing genealogy need the order engine (wave 21) and the batch store (wave 16); the quality order is wave 33 |
| Deliverable 7: same-batch reservation, customer sellable days and batch requirements (FR-24.19, BR-24.13, AC-24.14, `SAME_BATCH_UNAVAILABLE`) | partial | the rules are built (`domain.Resolve`, `domain.Check`, `domain.SelectSameBatch`) and served as `POST /v1/production/batch-requirements:check` (409 PMF-0008). The CustomerBatchRequirement table, the override with duty *Override customer batch requirements* and a reason, and enforcement in reservation (wave 27), picking (wave 28) and shelf-life aware planning (wave 22) are handoffs to those waves; the same-batch flag on the item is wave 16 |
| Deliverable 9: electronic batch record, review by exception, sealed PDF/A and JSON archive (FR-24.21, AC-24.16) | partial | the approval gate (`domain.CheckApprove`, PMF-0014) is built and served as `POST /v1/production/batch-records:approve-check`; the release gate (`domain.CheckRelease`, PMF-0015) is a domain rule only. ElectronicBatchRecord, EbrException and BatchRecordProfile tables, incremental compilation, exception creation (BR-24.15 triggers), review stages, sealing, the PDF/A-2b and JSON archive (wave-5 documents), addenda and the compile and seal jobs are later slices |
| Deliverable 9: 21 CFR Part 11 electronic signature service (FR-24.22, BR-24.14, AC-24.15) | partial | the domain checks are built and tested: lockout after three failures for 15 minutes (PMF-0011), session continuity (10 minutes idle), the factors a signing needs, signer equals session subject (PMF-0010), signer differs from author (PMF-0012), the SHA-256 of canonical JSON and hash invalidation (PMF-0013). `POST /api/v1/signatures`, Keycloak step-up re-authentication (wave 2), the Signature and SignatureStrategy tables, the append-only signature log to Iceberg, the security event on lockout and signature manifestation are later slices; the PRD catalog codes are kept, so no endpoint answers them yet |
| Deliverable 10: characteristic-dependent planning (FR-24.23) | handoff | the planning engine is services/planning (wave 22) |
| Deliverable 8 (lean subcontracting) | handoff | services/lean (see its NOT-IMPLEMENTED.md) |
| AC-24.5 numeric example | deviation | AC-24.5 says a 10-case RAF at 95 kg fails for nominal 10 kg, min 9, max 11. BR-24.5 (min × cw ≤ qty ≤ max × cw) accepts 95 kg with min 9 (90 ≤ 95). The test uses a per-unit minimum of 9.8 kg, which reproduces the AC outcome; the rule follows BR-24.5. The PRD example needs fixing |
| Event names in PRD 4.24.5 | deviation | the PRD writes `passage.production.batch-order.created.v1`; the kernel rule `dev.passage.<context>.<entity>.<verb>.v<n>` allows no hyphen, so the type is `dev.passage.production.batchorder.created.v1` (see events/asyncapi.yaml) |
| Formula lines: `potency_base_item`, `unit_id`, `scrap_pct`, `allocation_pct`, `by_product_cost_type` (PRD 4.24.4) | later slice of PSG-24 | the columns arrive with potency proposals and order-level co-product allocation, which use them |
| `/metrics` on an admin port | later slice of PSG-24 | no counters yet; the chart's ServiceMonitor is off by default |
| Deploy: second demo plant (food and chemicals line), flags `prod.process`, `prod.catch-weight` | handoff | deploy/ and the Helm umbrella belong to the deployment owner |
| Jobs: shelf-advice notification, consolidation, inheritance re-derivation, EBR compilation sweep and seal | later slice of PSG-24 | need Temporal (wave 4) and the entities above |
| Duty entries for `production.formula.write`, `production.batch-order.read`, `production.batch-order.write`, `production.batch-attribute.write`, `production.batch.merge`, `production.batch-record.approve`, `production.batch.consume-expired` in OPA data | handoff | policies/ is outside this wave's ownership; the service enforces duties from forwarded headers |
| Label, CoA and batch ticket documents (PRD 4.24.15) | handoff | w5 document service, w32 print service, w48 country packs |
| Performance budgets and load tests (PRD 4.24.19) | later slice of PSG-24 | no load path yet |
| Web: formula designer with ECO submit, derivation monitor, EBR review and signature prompts | later slice of PSG-24 | the backing endpoints are not built (see above), batch attribute recording and disposition change with history are built in slice 11; slice 2 adds screens for its five previews and slice 3 for the attribute check, picking proposal and rework plan; the module README lists every screen that exists |
