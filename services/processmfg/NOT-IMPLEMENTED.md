# Not implemented in PSG-24 slices 1 to 3, 9 and 11 to 15 (processmfg)

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
rounding the co-products up can overshoot are taken back one ten-thousandth at a time, largest
co-product first, so no share is negative), writes
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
page. The other previews still take their batches as input; the picking proposal can also read the stored batches (slice 12).

Built in slice 12: the picking proposal over the stored batch register (FR-24.3, FR-24.5, FR-24.6,
AC-24.4, AC-24.10, BR-24.10). `POST /v1/production/batch-picking:propose-stored` takes the item, the
posting date, the line quantity, an optional potency band and the on-hand quantity per batch id; the
disposition, expiry date and current attribute values of each batch are read from `batch_record` and
the attribute history (`store.PickCandidates`) instead of being supplied, so a batch put on hold or
past its expiry cannot be picked by leaving that out of the request. It runs the same
`domain.ProposePicking` as the caller-supplied proposal (PMF-0016, PMF-0003, PMF-0005, the
consume-expired override with its duty and reason), takes the potency attribute from the item's
potency-role definition when the band names none, and answers with the batch numbers. A batch outside
the caller's scope (tenant or legal entity) is PMF-0108; one of another item is COM-0001. Item and
batch ids are compared in canonical UUID form, and the batches and their current values are read
with one query each. The caller-supplied `batch-picking:proposal` stays as a what-if preview for
batches that are not registered: a batch id the register holds in the caller's scope is refused
with 409 PMF-0112, so the hold or expiry on record cannot be bypassed by restating the batch as
available there. It stores nothing and writes no event (no migration). The web module gains "Pick from stored batches" (list of the item's batches, an on-hand
field per batch, the proposal with picked and excluded batches). The OpenAPI and AsyncAPI documents of
processmfg and lean pass both documentation gates with no FAIL lines.

Built in slice 13: the merge of stored batches with its genealogy (Deliverable 6, FR-24.16,
AC-24.12). `POST /v1/production/batches:merge` (duty `production.batch.merge`, Idempotency-Key)
takes the new batch number and two to 50 source batches, each with the batch version the caller
read, the warehouse, an optional catch-weight unit, the quantity and the cost per unit (the
register keeps no quantity or cost; those are the wave-16 inventory ledger's). In one transaction
it locks the sources in id order, refuses a stale version (PMF-0104), reads each source's
disposition and current attribute values from the register (so a hold cannot be bypassed by
restating a batch), runs `domain.Merge` (PMF-0006 for a different item, legal entity, warehouse,
catch-weight unit or disposition, a mixed attribute kind, or a text attribute that is not in
`mergeOverride`), checks the merged values against the item's attribute definitions, registers the
target batch (disposition of the sources, expiry the earliest, merged values in the attribute
history with their pass flags, a duplicate number is PMF-0109), writes one `batch_genealogy` row
per source (migration 0004, with the quantity and cost each contributed) and
`dev.passage.production.batch.merged.v1` (with the warehouse and catch-weight unit the sources
share, where inventory posts the transfers). Each source's version moves by one, so a second merge or
a disposition change from the version read before the merge is refused as stale (PMF-0104); the
source keeps its disposition and stays registered and pickable, and can be merged again from its
new version, because the quantity merged may be part of its stock. `GET /v1/production/batches/{id}/genealogy` returns the
parents and the batches a batch went into. The `batch-merge:preview` calculator stays for
unregistered batches. The web module gains "Merge stored batches" (item batches with select,
quantity and cost per row, the result with its parents) and a genealogy card on the batch detail.
The OpenAPI and AsyncAPI documents of processmfg and lean pass both documentation gates with no
FAIL lines.

Built in slice 14: customer batch requirements (Deliverable 7, FR-24.19, BR-24.13, AC-24.14). The
`customer_batch_requirement` table (migration 0005, granted SELECT, INSERT and UPDATE to
passage_app, RLS with tenant and legal-entity policies) holds one rule per row for an item at one
scope (a customer, a customer group or the item default), setting sellable days (0 to 3,650), an
attribute range and the same-batch flag, with an optional validity window. `POST` and `GET
/v1/production/customer-batch-requirements`, `GET .../{id}` and `POST .../{id}:retire` (under the
version read; a rule is never edited, it is retired and replaced) enforce the new duty
`production.batch-requirement.write`, validate with `domain.ValidateRequirement` (a rule sets at
least one field; Resolve's checks) and write `dev.passage.production.batchrequirement.defined.v1`
and `.retired.v1` through the outbox in the same transaction. `POST
/v1/production/batch-requirements:check-stored` resolves the stored rules in force on the ship date
of the legal entity for the item, the customer and the customer's group (customer over group over
item default, field by field; within a scope the rule that started last, then the one defined last,
so a successor with a later `validFrom` takes over from an open-ended rule on that day), reads the disposition, expiry and attribute values of
each batch from the register, and answers qualifying and excluded batches (PMF-0009 with the reason),
the same-batch pick or 409 PMF-0008, and the rules it took a value from; only the on-hand quantity
per batch is passed. Ship date and expiry are calendar days compared without time-zone conversion
(the caller sends the shipping site's local day). Customer and customer-group ids are references to
party and sales master data (waves 4 and 27), validated as UUIDs only; the caller names the
customer's group. The web module gains the rule list, define form, detail with retire, and the stored check.
Both documentation gates for the processmfg OpenAPI and AsyncAPI documents pass with no FAIL
lines.

Built in slice 15: the electronic batch record with review by exception (Deliverable 9, FR-24.21,
BR-24.15, AC-24.16). Migration 0006 adds `batch_record_profile`, `electronic_batch_record` and
`ebr_exception` (SELECT, INSERT and UPDATE granted to passage_app; tenant and legal-entity RLS; no
deletes). A profile (`POST`, `GET` and `GET /{id}` on `/v1/production/batch-record-profiles`, `:retire`
under the version read) says that an item's batches get a record and whether releasing a batch needs it
approved (default yes); one active profile per item and legal entity. While it asks for approval a batch is
registered or moved to available only with an approved or sealed record (`domain.CheckRelease`, 422
PMF-0015), so the gate that was a domain rule is now enforced in `RegisterBatch` and `ChangeDisposition`.
`POST /v1/production/batch-records` compiles the record of a batch from its batch order and quality record
(content: the order with its lines and cost split, the batch with its attributes and dispositions), or
refreshes it while in review, and raises one exception per recorded value outside its limits, once per
history row (BR-24.15). A reviewer raises more by hand, and each exception is decided once (accepted,
deviation-raised, rejected; a decision is final, 409 PMF-0125) with the id of the signature behind it; approval
is refused with 422 PMF-0014 until every exception has both, with 409 PMF-0121 when a value outside its limits
was recorded after the last compile, and with 422 PMF-0124 when the approver raised or decided an exception of
the record or created its batch order (the segregation of PRD 4.24.17: *Approve batch records* against *Review
batch records* on the same record and *Maintain batch orders* on the same order). Sealing an approved record
stores the canonical JSON document (sorted keys, compact, no HTML escaping, UTC times, numbers as written) of the
record, the content as it was approved and the exceptions, with its SHA-256 (`contentHash` is the hex SHA-256 of
`sealedDocument`, checked by a database CHECK and the store test; an independent test vector pins the encoding).
The database holds the immutability, not only the store: triggers refuse any update or delete of a sealed
record, any change to an approved record other than sealing it with the approved content, any change to an
exception of a record no longer in review, and any change to a decided exception. A batch merge that would make an
available target applies the release gate to its sources, following earlier merges back to batches with records,
and a merged batch is released on those records, so a merge cannot turn an unapproved batch into a releasable one. Every command takes an Idempotency-Key
and the record version (PMF-0104); `batchrecordprofile.defined` and `.retired` and `batchrecord.compiled`,
`.exceptionraised`, `.exceptiondispositioned`, `.approved` and `.sealed` go through the outbox in the same
transaction. Duties: the new `production.batch-record.write` (the PRD's *Review batch records* plus setup: profiles,
compile, raise, decide) and the existing `production.batch-record.approve` (approve, seal) are enforced by the
service and on the pages.
The web module gains the record list with compile, the record detail with the review steps and the sealed
hash, and the profile list, define form and retire. Both documentation gates for the processmfg OpenAPI and
AsyncAPI documents pass with no FAIL lines.

Wave 24 proof fixtures: `pmf-potency-adv-40.csv` and `pmf-coby-adv-23.json` under testdata/,
written by the independent exact-rational references `gen_pmf_potency_adv.py` and
`gen_pmf_coby_adv.py`; the co-product cases are also ended on batch orders in the store. Building
them found that by-product burdens which fit the batch cost exactly (percent burdens totalling
100 %) were refused with PMF-0021 when their 4-place rounding overshot the cost; the overshoot is
now taken back off the burdens one ten-thousandth at a time, largest first, and burdens are
refused only when they exceed the cost both exactly and rounded. Taking it all off the single
largest share instead (burdens, and co-products under a weightless main item) went negative when
many tiny shares rounded up (four CLF 0.0001 burdens on a cost of 0.0002); the three CLF cases
at the end of the fixture cover that.

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: journals "as w21" on release, start, report and end (material issue, finished-goods receipt, WIP) | handoff | the status machine and the cost split are built here; posting issues, receipts and WIP journals is the wave-21 order engine (services/production) and the ledger, which consume `batchorder.statechanged` and `coproduct.allocated`. The `closed` status (period close) is not entered by this service; the batch cost is entered by the caller at End, because actual cost comes from wave-20 inventory costing and wave-21 WIP |
| Deliverable 1: co-product allocation on an order: standard costs and by-product rates read from item cost records, the `/co-products:allocate` command path, CoProductLine table, ledger journal lines | partial | the allocation is bound to the order and stored at End (slice 9), with standard costs, percentages and by-product rates entered by the caller; the stand-alone preview stays at `POST /v1/production/co-products:allocate`. Reading standard costs from wave-20 costing, the per-line `allocation_pct` and `by_product_cost_type` formula columns, reversing an ended order's allocation, and the journal lines for the ledger are later slices |
| Deliverable 1: potency role on items, pickability of batches outside min/max, consumption proposal (FR-24.3, AC-24.2 end to end) | partial | `ConsumePotency` and `PotencyInRange` are built and tested against pmf-potency-20 and exposed as a calculator; slice 3 adds the potency band to the picking proposal (`domain.CheckPickable`, PMF-0005) over batches the caller passes in. The stored picking proposal (slice 12) reads the potency role from the item's attribute definitions; the quantity per batch is passed by the caller until the wave-16 inventory ledger can be read. Proposing the potency-adjusted quantity per picked batch on a batch order, need the batch store and the order engine; `PMF_POTENCY_TOLERANCE_PCT` is not read |
| Deliverable 2: batch attributes and dispositions (PUT `/inventory/batches/{id}/attributes`, `:disposition`, history, `batch.attributes-recorded` event, AC-24.10) | partial | built in slice 11 under `/v1/production/batches` on this service's own batch register (the PRD path `/inventory/batches` is routed to services/inventory). Left: linking `batch_record` to the wave-16 InventoryBatch (the register is keyed by item and batch number, with no quantity or location), the attribute-definition editing and retiring (definitions are insert-only), date limits on date attributes (the type is checked, the range is not), making the customer batch requirement check read the stored disposition and expiry instead of caller-supplied batches (the picking proposal does since slice 12), and batch status changes from inventory postings. The PRD calls the PUT a full replace; this slice appends the values sent and keeps the others current (nothing is cleared), and the definition's `unit_id` is not modeled |
| Segregation of *Maintain batch attributes* and *Maintain batch disposition* (PRD 4.24.17) | partial | the service enforces `production.batch-attribute.write` and the new `production.batch-disposition.write` on separate routes and flags a disposition change by a user who recorded attributes on the same batch (`segregationConflict`). Not built: flagging the reverse order (attributes recorded by the user who last changed the disposition), and the static conflict check when one role is granted both duties, which belongs to the role and duty administration of the security service; seeding the new duty in policies/authz/data.json is the handoff below |
| Inventory holds on a picked batch (services/inventory `inventory_hold`, PR #409) | handoff | the stored picking proposal reads the disposition and expiry of this service's batch register, but not the wave-33 inventory holds placed by quality: `batch_record` is not linked to the inventory lot (the row above), and the on-hand quantity per batch is supplied by the caller, so held stock is left out only when the caller passes the available quantity (`availPhysical`, already net of holds). Reading the available quantity and active holds per lot from services/inventory needs that link, an inventory client in this service and a `processmfg -> inventory` service grant for `inventory.read` in policies/authz (outside this wave); until then a whole-lot hold is not a PMF-0016 exclusion here |
| Picking proposal over stored batches: the audit-log entry of the override reason, quantities from inventory and the proposal on a real batch order | later slice of PSG-24 | the stored proposal (slice 12) returns `expiredOverride` per batch and stores nothing; the other previews (derivation, customer requirements, rework) still take their batches as input; the merge reads them from the register (slice 13) |
| `policies/authz/data.json`: resource rows for `POST /v1/production/batches:merge` under `production.batch.merge`, `GET /v1/production/batches/*/genealogy` under `production.batch-order.read` (slice 13) and `POST /v1/production/batch-picking:propose-stored` under `production.batch-order.read` (slice 12) | done (seeded in `policies/authz/data.json`) | POST `/v1/production/batches:merge` under `production.batch.merge`; GET `/v1/production/batches/*/genealogy` and POST `/v1/production/batch-picking:propose-stored` under `production.batch-order.read`. |
| Duty entry for `production.batch-requirement.write` in OPA data (and the issue #313 list), and resource rows for `/v1/production/customer-batch-requirements*` and `POST /v1/production/batch-requirements:check-stored` under `production.batch-order.read` | done (seeded in `policies/authz/data.json`) | `production.batch-requirement.write` carries POST `/v1/production/customer-batch-requirements` and `/customer-batch-requirements/*:retire`; `production.batch-order.read` carries GET `/customer-batch-requirements[/*]` and POST `/v1/production/batch-requirements:check-stored`. |
| Duty entry for `production.batch-disposition.write` in OPA data (and the issue #313 list) | done (seeded in `policies/authz/data.json`) | `production.batch-disposition.write` carries POST `/v1/production/batches/*:disposition`. |
| Deliverable 2: shelf life at RAF, FEFO on batch orders, expired-batch override duty (FR-24.6, BR-24.10, AC-24.4 `BATCH_EXPIRED`) | partial | `ShelfDates` and `ShortestExpiry` are built and tested; slice 3 builds FEFO, the PMF-0003 exclusion and the override with duty `production.batch.consume-expired` and a reason of at least 10 characters in the picking proposal. Dates at RAF, the audit-log entry of the override reason (it is returned as `expiredOverride` for the EBR exception, not stored) and picking on a real batch order belong with the order engine and inventory batches; the shelf-advice job is not built |
| Deliverable 2: catch-weight dual UoM on inventory and production transactions (FR-24.7, AC-24.4 of the ticket) | partial | `ValidateCatchWeight` (BR-24.5) is built and exposed as a calculator; `cw_qty` on InventoryTransaction and journal lines is a handoff to wave 16 (inventory) and wave 21 (production) |
| Deliverable 5: recipe and formula version control through ECO | handoff | `formula.bom_version_id` points at the wave-9 BOM version and orders snapshot the formula version; formula versioning and ECO submit belong to services/engineering (wave 9). Formulas cannot be edited in slice 1 |
| Deliverable 6: inheritance rules, derivation log and monitor (FR-24.15, AC-24.11) | partial | the derivation rule (`domain.Derive`, every aggregation, PMF-0007) is built and served as `POST /v1/production/batch-derivation:preview`. The InheritanceRule and DerivationLog tables, derivation at RAF, manual re-derivation with forward propagation (max depth 10), the derivation monitor, the `inheritance active` parameter and the nightly re-derivation job need the wave-16 batch store and RAF on the order engine, so they are later slices |
| Deliverable 6: batch merge `:merge` (FR-24.16, AC-24.12) | partial | the stored merge and its genealogy are built in slice 13 (`POST /v1/production/batches:merge`, `GET /batches/{id}/genealogy`), on this service's own batch register, with the quantity and cost per source given by the caller. Posting the inventory transfers out of each source and into the target is services/inventory (wave 16) from `dev.passage.production.batch.merged.v1`; the sources keep their register record and disposition (the merge only moves their version), and the register holds no quantity, so merging more than a source's on-hand, or merging the same stock twice from freshly read versions, is not caught here, and an emptied source drops out of the stored picking proposal only through the on-hand the caller passes; merging into one of the sources instead of a new batch (FR-24.16 "new or one of the sources"); the cost per unit carries no currency (the legal entity's, as entered); the `merge-override` setting per attribute definition (today the request lists the attribute names); the PRD command path `/inventory/batches:merge` is routed to services/inventory |
| Deliverable 6: documentary batches, tracking policy `batch-record-only` (FR-24.24) | handoff | the tracking dimension group is wave 16 |
| Deliverable 7: consolidated bulk and packed orders, BulkPackRelation, consolidation job (FR-24.17, BR-24.12, AC-24.13) | partial | sizing and due-date pegging (`domain.Consolidate`) are built and served as `POST /v1/production/bulk-consolidation:preview`. The BulkPackRelation table, reading planned and firmed packed orders from wave 22 within the consolidation window (default 7 days), creating the bulk and packing orders, the same-batch reservation of the bulk batch and the daily 21:00 job are later slices |
| Deliverable 7: rework batch orders (FR-24.18, AC-24.18) | partial | slice 3 builds the plan (`domain.PlanRework`, `POST /v1/production/rework-orders:preview`): part or all of the batch as first ingredient, keep-batch-number, require-quality-order, output disposition (default quarantine, never available behind a quality order), rework vs capitalized variance, genealogy parent. Creating the rework BatchOrder with extra ingredients from a rework formula, receiving the output to the batch at RAF, re-recording attributes and writing genealogy need the order engine (wave 21) and the batch store (wave 16); the quality order is wave 33 |
| Deliverable 7: same-batch reservation, customer sellable days and batch requirements (FR-24.19, BR-24.13, AC-24.14, `SAME_BATCH_UNAVAILABLE`) | partial | the rules, their storage (`customer_batch_requirement`, slice 14) and the check over stored batches (`POST /v1/production/batch-requirements:check-stored`, 409 PMF-0008) are built; `batch-requirements:check` stays for batches the caller describes. Not built: the override with duty *Override customer batch requirements* and a reason (the check excludes, it does not override), item groups and customer-group lookup (rules are per item and per customer-group id; the grouping itself belongs to party and item master data), the same-batch flag on the item and on a sales line (wave 16 and 27), and enforcement in reservation (wave 27), picking (wave 28) and shelf-life aware planning (wave 22), which read the rules from the `batchrequirement` events or the check. Overlapping rules of one scope are allowed and resolved deterministically (latest start, then latest defined); there is no end-dating of a stored rule other than retiring it or defining a successor, and no lookup of the customer, group or item ids |
| Deliverable 9: electronic batch record, review by exception, sealed PDF/A and JSON archive (FR-24.21, AC-24.16) | partial | built in slice 15: profiles, compilation, exceptions (BR-24.15), review, approval, the release gate (PMF-0015) and the seal with the SHA-256 of the canonical JSON document. Left: the PDF/A-2b rendering and the archive of the sealed JSON in the wave-5 document store (the document is stored in the row, not archived), incremental compilation from events (the record is compiled and refreshed on command, so a value recorded later is caught by PMF-0121 at approval, not added by itself), further exception triggers beyond a value outside its limits (a deviation or an override of the expired-batch rule, a rework step), review stages with their own roles, addenda to a sealed record, and the compile and seal jobs. Review stages are one step (any holder of the review duty decides; the approver is checked against the reviewers of the record and the creator of its batch order only, since the order's later lifecycle steps keep no actor) |
| Duty entry for `production.batch-record.write` in OPA data (and the issue #313 list), and resource rows for the slice-15 routes: `POST /v1/production/batch-record-profiles` and `/batch-record-profiles/*:retire`, `POST /v1/production/batch-records`, `/batch-records/*/exceptions` and `/batch-records/*/exceptions/*:disposition` under `production.batch-record.write`; `POST /v1/production/batch-records/*:approve` and `/batch-records/*:seal` under `production.batch-record.approve`; `GET /v1/production/batch-record-profiles[/*]` and `GET /v1/production/batch-records[/*]` under `production.batch-order.read`. Default roles per PRD 4.24.17: the write duty to Production reviewer, the approve duty to QA manager; the SoD pair *Review batch records* / *Approve batch records* (same record) is also checked by the service (PMF-0124) | handoff | `policies/authz/data.json` is outside this wave's paths; the service enforces the duties with platform authz and the pages gate their actions on them. The nav items use the existing read duty |
| **NOT 21 CFR PART 11 COMPLIANT: the signature behind an exception decision is not verified (FR-24.22, AC-24.15)** | handoff | There is no signature service or signature table on main (services/compliance has only the FR-48.13 domain rules, no store or endpoint). `signatureId` is therefore stored exactly as given: any well-formed UUID is accepted, with no re-authentication, no check that the signature exists, belongs to the caller or is bound to the record hash. What the service does hold: the decision is attributed to the authenticated caller, it is final, the approver must be someone else (PMF-0124), and the sealed document says `"signaturesVerified": false` so it never claims a verified signature. Until the signature service exists (this wave's FR-24.22 slice or wave 48's FR-48.13) the EBR must not be used as the GMP record of a regulated batch. Needed then: verify each signatureId (signer = dispositionedBy, meaning `reviewed`, hash of the record), a QA approval signature (meaning `approved`) on approve, and `signaturesVerified: true` in the sealed document |
| Deliverable 9: 21 CFR Part 11 electronic signature service (FR-24.22, BR-24.14, AC-24.15) | partial | the domain checks are built and tested: lockout after three failures for 15 minutes (PMF-0011), session continuity (10 minutes idle), the factors a signing needs, signer equals session subject (PMF-0010), signer differs from author (PMF-0012), the SHA-256 of canonical JSON and hash invalidation (PMF-0013). `POST /api/v1/signatures`, Keycloak step-up re-authentication (wave 2), the Signature and SignatureStrategy tables, the append-only signature log to Iceberg, the security event on lockout and signature manifestation are later slices; the PRD catalog codes are kept, so no endpoint answers them yet |
| Deliverable 10: characteristic-dependent planning (FR-24.23) | handoff | the planning engine is services/planning (wave 22) |
| Deliverable 8 (lean subcontracting) | handoff | services/lean (see its NOT-IMPLEMENTED.md) |
| AC-24.5 numeric example | deviation | AC-24.5 says a 10-case RAF at 95 kg fails for nominal 10 kg, min 9, max 11. BR-24.5 (min × cw ≤ qty ≤ max × cw) accepts 95 kg with min 9 (90 ≤ 95). The test uses a per-unit minimum of 9.8 kg, which reproduces the AC outcome; the rule follows BR-24.5. The PRD example needs fixing |
| Event names in PRD 4.24.5 | deviation | the PRD writes `passage.production.batch-order.created.v1`; the kernel rule `dev.passage.<context>.<entity>.<verb>.v<n>` allows no hyphen, so the type is `dev.passage.production.batchorder.created.v1` (see events/asyncapi.yaml) |
| Formula lines: `potency_base_item`, `unit_id`, `scrap_pct`, `allocation_pct`, `by_product_cost_type` (PRD 4.24.4) | later slice of PSG-24 | the columns arrive with potency proposals and order-level co-product allocation, which use them |
| `/metrics` on an admin port | later slice of PSG-24 | no counters yet; the chart's ServiceMonitor is off by default |
| Deploy: second demo plant (food and chemicals line), flags `prod.process`, `prod.catch-weight` | handoff | deploy/ and the Helm umbrella belong to the deployment owner |
| Jobs: shelf-advice notification, consolidation, inheritance re-derivation, EBR compilation sweep and seal | later slice of PSG-24 | need Temporal (wave 4) and the entities above |
| Duty entries for `production.formula.write`, `production.batch-order.read`, `production.batch-order.write`, `production.batch-attribute.write`, `production.batch.merge`, `production.batch-record.approve`, `production.batch.consume-expired` in OPA data | done (seeded in `policies/authz/data.json`) | All seven duties are seeded with the routes the service enforces them on. |
| Label, CoA and batch ticket documents (PRD 4.24.15) | handoff | w5 document service, w32 print service, w48 country packs |
| Performance budgets and load tests (PRD 4.24.19) | later slice of PSG-24 | no load path yet |
| Web: formula designer with ECO submit, derivation monitor and signature prompts | later slice of PSG-24 | the backing endpoints are not built (see above), batch attribute recording and disposition change with history are built in slice 11 and the EBR review screens in slice 15; slice 2 adds screens for its five previews and slice 3 for the attribute check, picking proposal and rework plan; the module README lists every screen that exists |
