# processmfg

Process manufacturing for Passage (wave 24, PSG-24, slices 1 to 3, 9 and 11 to 17). Formulas, batch orders
created from a formula snapshot and scaled to the batch size, rescale before the order starts,
and the potency, catch-weight and co-product allocation rules (slice 1). Slice 2 adds the batch
rules as stateless previews: batch derivation (FR-24.15), batch merge (FR-24.16), bulk
consolidation sizing and pegging (FR-24.17), the customer batch requirement check with
same-batch (FR-24.19), and the batch-record approval gate (BR-24.15); and the domain checks of
the electronic-signature service (FR-24.22, BR-24.14). Slice 3 adds three more previews: the
batch attribute check against the item's definitions (FR-24.5), the picking proposal that
applies disposition, expiry with the consume-expired override, potency band and FEFO (FR-24.3,
FR-24.5, FR-24.6, BR-24.10), and the rework batch order plan (FR-24.18).

Slice 1 persists formulas and batch orders (Postgres, RLS on every table, outbox events in
the same transaction). The slice-2 and slice-3 previews store nothing and emit no events: the batches they
judge are wave-16 InventoryBatch rows that the caller passes in. Slice 11 persists the batch quality record: attribute definitions, batches, recorded values with
their limit check, and dispositions with history, all append-only apart from the batch row. The
derivation log, the EBR store and the signature log are
later slices; see `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/processmfg        entrypoint: dbx.Connect, optional dbx.Migrate, httpx router, serve
internal/domain       pure rules (no I/O): scale.go (FR-24.1 validation, BR-24.1 scaling),
                      potency.go (BR-24.2), coproduct.go (BR-24.3), catchweight.go (BR-24.5),
                      shelflife.go (FR-24.6, BR-24.4, BR-24.11), inherit.go (FR-24.15),
                      merge.go (FR-24.16), consolidate.go (BR-24.12), customer.go (FR-24.19,
                      BR-24.13), ebr.go (BR-24.15), signature.go (FR-24.22, BR-24.14),
                      attributes.go (FR-24.5), pickable.go (FR-24.3, FR-24.6, BR-24.10,
                      AC-24.4, AC-24.10), rework.go (FR-24.18);
                      property tests use rapid
internal/store        Postgres repository: formulas, batch orders, rescale, lifecycle and cost allocation, idempotency, outbox
internal/api          HTTP handlers: decode → validate → tenant → store/domain → JSON/problem
internal/events       event types and payloads
migrations/           0001_init.sql (formula, formula_line, batch_order_sequence, batch_order,
                      batch_order_line, idempotency_keys, outbox), RLS on every table
testdata/             PRD 4.24.9 fixtures: pmf-potency-20, pmf-coby-12, pmf-catchweight-10
api/openapi.yaml      OpenAPI 3.1, checked against the router by internal/api/openapi_test.go
events/asyncapi.yaml  AsyncAPI 3, checked against internal/events by events_test.go
charts/processmfg     Helm chart (helm lint)
```

## Run

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/processmfg
```

With `IDENTITY_URL` set, every route answers 403 `FEAT-0001` while the tenant has the `process-manufacturing` feature switched off (`platform/go/features`, fail open, 30 s cache). Unset, the service behaves as before.

`DATABASE_URL` is required. `HTTP_ADDR` defaults to `:8080`. Batch record signing (21 CFR
Part 11) answers 503 PMF-0135 until a step-up re-authentication verifier is bound;
`PASSAGE_ENV=dev` binds a stand-in whose signatures record the method `dev-unverified`, so a
record sealed with them says `signaturesVerified: false`. The gateway sets
`X-Passage-Tenant`, `X-Passage-User`, `X-Passage-Duties` and `X-Passage-Legal-Entity`.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://owner@... PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app@... go test ./...
```

Store tests use `testkit.DBWithApp`: migrations run as the owner, every query as the
`passage_app` role (no BYPASSRLS), so tenant and legal-entity isolation is exercised. They are
skipped when `PASSAGE_TEST_DATABASE_URL` is unset.

- AC-24.1 scaling of a 1,000 kg formula to 2,500 kg (domain, API and store), a property test
  that percent-controlled lines sum exactly to the batch size, and the order snapshot.
- AC-24.2 and `pmf-potency-20`: 20 potency rows, one of which must be rejected (PMF-0002).
- AC-24.3 and BR-24.3: `pmf-coby-12` (4 methods × by-product cost types) and a rapid property
  that the allocation sums to the batch cost exactly.
- BR-24.5: `pmf-catchweight-10` bounds.
- AC-24.5 catch weight. The fixture uses a per-unit minimum of 9.8 kg; see NOT-IMPLEMENTED.md.
- AC-24.9 a percent-controlled formula summing to 99.5% is refused (PMF-0001).
- BR-24.11 weighted average (43.0000%).
- AC-24.11 derivation: weighted-average potency of 200 kg at 40% and 300 kg at 45% is 43.0000%
  (domain and API); senders without a value are reported (PMF-0007); a rapid property keeps the
  average between the sender values.
- AC-24.12 merge of 100 kg (expiry 2027-03-01) and 50 kg (2027-02-01) into 150 kg expiring
  2027-02-01 with both parents; PMF-0006 refusals (text, disposition, item, mixed kinds).
- AC-24.13 consolidation: 400 + 600 bottles at 0.5 kg with a 100 kg multiple give one 500 kg
  bulk order with both packing orders pegged; a rapid property checks sums, multiples, the
  maximum and that every packing order is pegged in full.
- AC-24.14 customer sellable days 60: only the 90-day batch qualifies; precedence customer over
  group over item (BR-24.13); on-hold batches are excluded (AC-24.10); same-batch shortfall is
  409 PMF-0008 with the largest batch quantity.
- FR-24.5 attribute check: wrong types, undefined attributes, enum values outside the options
  and invalid definitions are refused; a numeric value outside min/max (bounds inclusive) passes
  through flagged and counted as an exception.
- AC-24.4 an expired batch without the consume-expired duty is excluded with PMF-0003; with the
  duty and a reason of at least 10 characters (counted in characters after trimming) it is
  picked first and flagged (BR-24.10); AC-24.10 on-hold, quarantine and rejected batches are
  excluded with PMF-0016; FR-24.3 potency outside the band or not recorded is PMF-0005; a rapid
  property checks that the proposal takes only pickable batches, never more than a batch holds,
  and that picked plus shortfall equals the need.
- AC-24.18 rework of B-77 with the defaults keeps the batch number, outputs to quarantine behind
  a quality order, posts a rework variance and lists B-77 as parent; parameter and refusal
  cases (more than the batch, available output behind a quality order, rejected output).
- AC-24.15 lockout after three failed re-authentications (domain); AC-24.16 approval refused
  while an exception lacks disposition or signature (PMF-0014).

## Endpoints

| Method | Path | Duty | Purpose |
|---|---|---|---|
| POST | `/v1/production/formulas` | `production.formula.write` | Create a formula with its lines (FR-24.1); Idempotency-Key |
| GET | `/v1/production/formulas` | `production.batch-order.read` | Formulas by code |
| GET | `/v1/production/formulas/{id}` | `production.batch-order.read` | One formula with lines |
| POST | `/v1/production/formulas/{id}:scale` | `production.batch-order.read` | Scale preview, writes nothing (BR-24.1) |
| POST | `/v1/production/batch-orders` | `production.batch-order.write` | Create a batch order from a formula (FR-24.2); Idempotency-Key |
| GET | `/v1/production/batch-orders` | `production.batch-order.read` | Orders, newest first; `status`, `formulaId`, `limit` |
| GET | `/v1/production/batch-orders/{id}` | `production.batch-order.read` | One order with its scaled lines |
| POST | `/v1/production/batch-orders/{id}:rescale` | `production.batch-order.write` | New batch size before Started; `version`; Idempotency-Key |
| POST | `/v1/production/batch-orders/{id}:release` | `production.batch-order.write` | planned to released; `version`; Idempotency-Key |
| POST | `/v1/production/batch-orders/{id}:start` | `production.batch-order.write` | released to started (no more rescale); `version`; Idempotency-Key |
| POST | `/v1/production/batch-orders/{id}:report-finished` | `production.batch-order.write` | started to reported: main quantity and the reported quantity of each co-/by-product line (a line left out is reported at its plan) |
| POST | `/v1/production/batch-orders/{id}:end` | `production.journal.post` | reported to ended: splits the batch cost over the main item, co-products and by-product burdens (BR-24.3) and stores the shares; the order then carries `allocation` |
| POST | `/v1/production/potency:consume` | `production.batch-order.read` | Potency consumption and filler adjustment (BR-24.2) |
| POST | `/v1/production/catch-weight:validate` | `production.batch-order.read` | Catch-weight range check (BR-24.5) |
| POST | `/v1/production/co-products:allocate` | `production.journal.post` | Co-product allocation preview (BR-24.3) |
| POST | `/v1/production/batch-derivation:preview` | `production.batch-attribute.write` | Derive a receiver attribute from sender batches (FR-24.15, BR-24.11) |
| POST | `/v1/production/batch-merge:preview` | `production.batch.merge` | Target batch of a merge: quantity, cost, attributes, parents (FR-24.16) |
| POST | `/v1/production/batch-requirements:check` | `production.batch-order.read` | Customer batch requirement check and same-batch pick (FR-24.19, BR-24.13) |
| POST | `/v1/production/bulk-consolidation:preview` | `production.batch-order.write` | Bulk batch orders for packed orders, pegged by due date (FR-24.17, BR-24.12) |
| POST | `/v1/production/batch-records:approve-check` | `production.batch-record.approve` | Whether every EBR exception is dispositioned and signed (BR-24.15) |
| POST | `/v1/production/batch-attributes:check` | `production.batch-attribute.write` | Check recorded attribute values against the item's definitions; out-of-limit values are flagged as exceptions (FR-24.5) |
| POST | `/v1/production/batch-picking:proposal` | `production.batch-order.read` | What-if proposal over batches the caller describes: available only, FEFO, potency band, expired only on the `production.batch.consume-expired` override with a reason (FR-24.3, FR-24.5, FR-24.6, BR-24.10); a batch of the register is refused with PMF-0112 |
| POST | `/v1/production/batch-picking:propose-stored` | `production.batch-order.read` | The same proposal over registered batches of one item: disposition, expiry and attribute values read from the register, on-hand quantity per batch from the caller, potency band on the item's potency attribute by default (FR-24.3, FR-24.5, FR-24.6, AC-24.4, AC-24.10) |
| POST | `/v1/production/customer-batch-requirements` | `production.batch-requirement.write` | Define a customer batch requirement for an item at a customer, customer group or item scope: sellable days, attribute range, same-batch flag, validity; writes `batchrequirement.defined` (FR-24.19, BR-24.13) |
| GET | `/v1/production/customer-batch-requirements`, `/{id}` | `production.batch-order.read` | The rules, filtered by legal entity, item, customer, group and the day they are active |
| POST | `/v1/production/customer-batch-requirements/{id}:retire` | `production.batch-requirement.write` | Retire a rule under the version read; writes `batchrequirement.retired` |
| POST | `/v1/production/batch-record-profiles` | `production.batch-record.write` | Define the electronic batch record profile of an item: whether releasing a batch needs the approved record (default yes); one active profile per item and legal entity; writes `batchrecordprofile.defined` (FR-24.21) |
| GET | `/v1/production/batch-record-profiles`, `/{id}` | `production.batch-order.read` | The profiles, filtered by item and `active=true` |
| POST | `/v1/production/batch-record-profiles/{id}:retire` | `production.batch-record.write` | Retire a profile under the version read; writes `batchrecordprofile.retired` |
| POST | `/v1/production/batch-records` | `production.batch-record.write` | Compile the electronic batch record of a batch from its batch order and quality record, or refresh it while in review; one exception per recorded value outside its limits (BR-24.15); writes `batchrecord.compiled` and `batchrecord.exceptionraised` |
| GET | `/v1/production/batch-records`, `/{id}` | `production.batch-order.read` | Records by status, batch and item; the detail carries the content, the exceptions and, once sealed, the sealed document and its hash |
| POST | `/v1/production/batch-records/{id}/exceptions` | `production.batch-record.write` | Raise an exception by hand on a record in review |
| POST | `/v1/production/batch-records/{id}/exceptions/{exceptionId}:disposition` | `production.batch-record.write` | The review step: accept, raise a deviation or reject an open exception, signed by the call (meaning reviewed: printed name, step-up assertion in `X-Passage-Reauth-Assertion`, the BR-24.14 lockout, 403 PMF-0010, 423 PMF-0011, 409 PMF-0013 when the record changed, 503 PMF-0135 without a verifier); a decision is final (409 PMF-0125); writes `batchrecord.exceptiondispositioned` |
| POST | `/v1/production/batch-records/{id}:approve` | `production.batch-record.approve` | Approve by exception: every exception decided and signed (422 PMF-0014), no value recorded since the last compile (409 PMF-0121), and the approver neither raised nor decided an exception of the record nor created its batch order (422 PMF-0124); signed by the call with the meaning approved, like a decision; writes `batchrecord.approved` (BR-24.15, AC-24.16) |
| GET | `/v1/production/batch-records/{id}/pdf` | `production.batch-order.read` | A sealed record rendered as PDF/A-2b from its sealed JSON (embedded font, XMP pdfaid, sRGB output intent, document ID; checked with veraPDF) |
| POST | `/v1/production/batch-records/{id}:seal` | `production.batch-record.approve` | Seal an approved record: the canonical JSON document of the approved content (sorted keys, compact, UTC times, every signature, and `signaturesVerified` true only when every decision and the approval rest on a verified re-authentication) is stored with its SHA-256 and the database refuses any later change; writes `batchrecord.sealed` (FR-24.21) |
| POST | `/v1/production/batch-requirements:check-stored` | `production.batch-order.read` | Check registered batches against the stored rules of the legal entity (default the caller's) for the item, customer and group in force on the ship date; on-hand quantity per batch from the caller (FR-24.19, AC-24.14). Precedence per field: customer over group over item default; within a scope the rule that started last, then the one defined last. Customer and group ids are party/sales references checked for form only; ship date and expiry are calendar days compared without time-zone conversion |
| POST | `/v1/production/rework-orders:preview` | `production.batch-order.write` | Rework batch order plan: output batch, disposition, quality order, variance type, genealogy (FR-24.18) |
| POST | `/v1/production/rework-orders` | `production.batch-order.write` | Create a rework batch order from a stored batch: plan, rework formula of the batch's item, batch order, link (FR-24.18; PMF-0132 one open rework per batch) |
| GET | `/v1/production/rework-orders` | `production.batch-order.read` | List rework orders by status or source batch |
| GET | `/v1/production/rework-orders/{id}` | `production.batch-order.read` | One rework order |
| POST | `/v1/production/rework-orders/{id}:receive` | `production.batch-order.write` | Take the output back into the register once the batch order is reported: kept number moves the batch to the output disposition, else a new batch with a rework genealogy link (PMF-0133, PMF-0134); an output planned as available also needs `production.batch-disposition.write` |
| POST | `/v1/production/batch-attribute-definitions` | `production.batch-attribute.write` | Define a batch attribute of an item, role generic or potency (one per item) (FR-24.5); Idempotency-Key |
| GET | `/v1/production/batch-attribute-definitions` | `production.batch-order.read` | Definitions by item and name; `itemId`, `limit` |
| POST | `/v1/production/batches` | `production.batch-attribute.write` | Register a batch for quality records, optionally linked to a batch order of the same legal entity; Idempotency-Key |
| GET | `/v1/production/batches` | `production.batch-order.read` | Batches, newest first; `itemId`, `disposition`, `limit` |
| GET | `/v1/production/batches/{id}` | `production.batch-order.read` | One batch with current attributes, attribute history and disposition history |
| PUT | `/v1/production/batches/{id}/attributes` | `production.batch-attribute.write` | Record attribute values with the limit check; sets the expiry date; `version`; Idempotency-Key (FR-24.5, FR-24.6) |
| POST | `/v1/production/batches/{id}:disposition` | `production.batch-disposition.write` | Change the disposition with a reason; a change by a user who recorded attributes on the batch is flagged `segregationConflict` (PRD 4.24.17); `version`; Idempotency-Key (AC-24.10) |
| POST | `/v1/production/inheritance-rules` | `production.inheritance-rule.write` | Define how an attribute of a receiver batch is derived from its senders (FR-24.15): copy, min, max, weighted-average (decimals 0 to 6, default 4), earliest-date, latest-date or fixed; the item must define the attribute and the aggregation must fit its kind (400 COM-0001); one active rule per item and attribute (409 PMF-0127); writes `inheritancerule.defined` |
| GET | `/v1/production/inheritance-rules`, `/{id}` | `production.batch-order.read` | List by legal entity, item and `active`; one rule (404 PMF-0126) |
| POST | `/v1/production/inheritance-rules/{id}:retire` | `production.inheritance-rule.write` | Retire under the version read (409 PMF-0104, PMF-0128 when already retired); writes `inheritancerule.retired` |
| POST | `/v1/production/batches/{id}:derive` | `production.batch-attribute.write` | Derive the receiver's attributes from 1 to 50 sender batches with the item's active rules (FR-24.15, BR-24.11, AC-24.11): the senders' values come from the register, the caller passes the quantity consumed from each; the values are recorded in the attribute history (checked against the definitions), each derivation is logged with the senders used and those without a value (PMF-0007 warning), and `batch.attributesrecorded` and `batch.derived` are written; no active rule is 422 PMF-0129; a sender that descends from the receiver through the latest derivation of each batch is 422 PMF-0130; running it again re-derives |
| GET | `/v1/production/batch-derivations` | `production.batch-order.read` | The derivation monitor: the log newest first with the receiver value before and after, filtered by receiver batch, item or `withMissing` |

The slice-2 and slice-3 previews live under `/v1/production` because `/v1/inventory` is routed to
services/inventory (wave 16); the PRD's `/inventory/batches/{id}/...` commands arrive with the
batch store.

Duties are enforced with the kernel `authz.RequireDuty`. The OPA entries are a handoff to
`policies/`.

## Control recipes of released orders (#25)

`PUT /v1/production/recipe-targets/{itemId}` says where the control recipes of an item's batch orders go: the shop floor resource and the destination (`kiosk`, `pcs-opcua` with an edge node, or `pcs-mqtt` with an edge node and the recipe topic). Releasing a batch order of such an item generates its control recipe in the shopfloor service as the releasing user (service grant `processmfg` to `shopfloor`, duty `shopfloor.controlrecipe.create`, `SHOPFLOOR_URL` and the service signing key): the order's make instruction and its material list scaled to the batch. The order keeps the recipe's id or the reason it could not be made (`GET /v1/production/batch-orders/{id}/control-recipe`); a failure never undoes the release, and `POST /v1/production/batch-orders/{id}:send-recipe` repeats it (the shopfloor service deduplicates by the order). Migration `0011`; codes `PMF-0503` (no `SHOPFLOOR_URL`), `PMF-0504` (the order is not released or started) and `PMF-0505` (no recipe target).

## Events

| Type | When |
|---|---|
| `dev.passage.production.formula.created.v1` | a formula is created |
| `dev.passage.production.batchorder.created.v1` | a batch order is created (PRD `batch-order.created`) |
| `dev.passage.production.batchorder.rescaled.v1` | a batch order's size changes |
| `dev.passage.production.batchorder.statechanged.v1` | a batch order is released, started, reported as finished or ended |
| `dev.passage.production.coproduct.allocated.v1` | an order ends and its batch cost is split (same transaction as the end) |
| `dev.passage.production.batchrecordprofile.defined.v1`, `.retired.v1` | a batch record profile is defined or retired (FR-24.21) |
| `dev.passage.production.batchrecord.compiled.v1`, `.exceptionraised.v1`, `.exceptiondispositioned.v1`, `.approved.v1`, `.sealed.v1` | the electronic batch record is compiled, an exception is raised or decided, the record is approved or sealed (the sealed event carries the content hash) |

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `COM-0001` | 400 | Malformed body or invalid input; field errors in `errors` |
| `PMF-0001` | 422 | Percent-controlled lines do not sum to 100 within ±0.01 (FR-24.1) |
| `PMF-0002` | 422 | Compensating line would become negative; alias `POTENCY_COMPENSATION_NEGATIVE` (BR-24.2) |
| `PMF-0003` | 200 (exclusion) | Batch expired before the posting date; PRD alias `BATCH_EXPIRED`. A picking proposal lists it under `excluded` unless the consume-expired override applies (BR-24.10, AC-24.4) |
| `PMF-0004` | 422 | Catch weight outside min × units to max × units (BR-24.5) |
| `PMF-0005` | 200 (exclusion) | Batch potency outside the line's band, or not recorded (FR-24.3) |
| `PMF-0006` | 422 | Merge refused: item, warehouse, catch-weight unit, disposition or attribute kind differs, or a text attribute differs without merge-override (FR-24.16) |
| `PMF-0007` | 200 (warning) | `warningCode` of a derivation whose senders lack a value (FR-24.15) |
| `PMF-0008` | 409 | No single qualifying batch holds a same-batch line; PRD alias `SAME_BATCH_UNAVAILABLE` (FR-24.19) |
| `PMF-0009` | 200 (warning) | `warningCode` of a batch excluded by the customer's requirements (FR-24.19) |
| `PMF-0014` | 422 | Batch record cannot be approved: exceptions without disposition and signature (BR-24.15) |
| `PMF-0015` | 422 | Release refused: the item's profile needs an approved batch record (a batch is registered, moved to available or made available by a merge only with one; a merged batch stands on the records of the batches it was merged from; FR-24.21) |
| `PMF-0016` | 200 (exclusion) | Batch disposition is not available (FR-24.5, AC-24.10; not in the PRD catalog) |
| `PMF-0021` | 422 | Allocation weights sum to zero, or by-product burden exceeds batch cost (not in the PRD catalog) |
| `PMF-0101` | 404 | Formula not found |
| `PMF-0102` | 404 | Batch order not found |
| `PMF-0103` | 409 | Formula code already used |
| `PMF-0104` | 409 | Stale `version` on rescale |
| `PMF-0105` | 409 | Order has started; only planned or released orders can be rescaled |
| `PMF-0106` | 409 | The order's status does not allow the command (release needs planned, start released, report-finished started, end reported) |
| `PMF-0107` | 422 | Report as finished with a non-positive main quantity, a negative line quantity, or a line that is not a co-/by-product line |
| `PMF-0108` | 404 | Batch not found |
| `PMF-0109` | 409 | The item already has a batch with that number |
| `PMF-0110` | 409 | The item already defines a batch attribute of that name, or already has a potency attribute |
| `PMF-0111` | 409 | The batch already has the requested disposition |
| `PMF-0112` | 409 | The caller-supplied picking proposal lists a batch of the batch register; propose it with `batch-picking:propose-stored` (not in the PRD catalog) |
| `PMF-0113` | 404 | Customer batch requirement not found |
| `PMF-0114` | 409 | The customer batch requirement is already retired |
| `PMF-0115` | 404 | Batch record profile not found |
| `PMF-0116` | 409 | The item already has an active batch record profile, or the code is taken |
| `PMF-0117` | 409 | The batch record profile is already retired |
| `PMF-0118` | 404 | Batch record not found |
| `PMF-0119` | 409 | The batch's item has no active batch record profile |
| `PMF-0120` | 409 | The batch record's status does not allow the step (only a record in review changes, only an approved one is sealed) |
| `PMF-0121` | 409 | A value outside its limits was recorded after the record was compiled; compile it again |
| `PMF-0122` | 404 | The batch record has no such exception |
| `PMF-0123` | 422 | The batch has no batch order to compile a record from |
| `PMF-0124` | 422 | Segregation of duties: the approver raised or decided an exception of the record, or created its batch order (PRD 4.24.17) |
| `PMF-0125` | 409 | The exception is already decided; a decision is final |
| `PMF-0126` | 404 | Inheritance rule not found |
| `PMF-0127` | 409 | The item already has an active inheritance rule for the attribute |
| `PMF-0128` | 409 | The inheritance rule is already retired |
| `PMF-0129` | 422 | The receiver's item has no active inheritance rule to derive with |
| `PMF-0130` | 422 | A sender batch descends from the receiver through earlier derivations (a derivation cycle) |
| `PMF-0131` | 404 | Rework order not found in the caller's scope |
| `PMF-0132` | 409 | The batch already has an open rework order |
| `PMF-0133` | 409 | The output of this rework order was already received |
| `PMF-0134` | 422 | The rework's batch order is not reported as finished yet |
| `idempotency_key_reuse` | 422 | Idempotency-Key reused with a different request |
| `idempotency_key_in_flight` | 409 | A concurrent request with the same key committed first; retry for its answer |
| `AUTH-0001` | 401 | No tenant or principal |
| `AUTH-0003` | 403 | Missing duty, or legal entity outside the caller's scope |
| `COM-0500` | 500 | Internal error (detail hidden) |

## Wave reference

PRD section 4.24 (`docs/prd/phase-4-manufacturing.md`), Wave 24. Owned paths:
`services/processmfg/` and `apps/web/src/modules/processmfg/` (screens listed in that module's
README).

## Dependencies

Only the platform module and what it already pulls in: chi, pgx/v5, shopspring/decimal,
pgregory.net/rapid (tests).
