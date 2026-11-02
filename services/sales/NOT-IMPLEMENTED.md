# NOT-IMPLEMENTED — sales (PSG-27, wave 27)

Slice 1 builds the order core: create (UI and channel intake), the order list
(with an active-hold filter), confirm with the credit check,
CREDIT holds and the release that confirms the order, the stateless cumulative
ATP check, the ATP domain for delivery-date control, credit profiles (list,
read, create, replace), and the web screens for all of these.

Slice 2 adds change management: `PATCH /orders/{id}` (If-Match, header and
OPEN/RESERVED line edits, a version per change with a field-level diff in
`sales_order_version`, re-promise of only the changed lines of a confirmed
order, a credit re-check when the total rises), `:cancel` (lines cancelled,
active holds cleared), the change history (`GET /orders/{id}/versions`), the
`order.changed` and `order.cancelled` events; the stateless scheduling-release
reconciliation check (BR-27.12); the stateless backorder run preview
(WIN/GAIN/REDISTRIBUTE/FILL/LOSE, BR-27.11); and web screens for all of these.

Slice 3 adds holds applied by hand (`POST /orders/{id}/holds`, MANUAL, FRAUD
and PRICE_REVIEW, duty `sales.order.hold`); product listings and exclusions
(`product_listing`, create, inquiry, remove) checked on every line of a new
order with the SalesOrderApprove override logged on the order (FR-27.15,
BR-27.13, AC-27.11); and quantity sales agreements (`sales_agreement`, `SA-`
numbering, list, read with release history) whose `:release-order` creates a
DRAFT order at the agreement price and decrements the commitment atomically
(FR-27.10, BR-27.9, AC-27.9), with `agreement.released`; and web screens for
all of these.

Slice 4 adds quotations (`sales_quotation`, `SQ-` numbering, create with
`Idempotency-Key`, list, read, `:send`, `:lose`, and `:convert` of every or
selected open lines into one DRAFT order in one transaction, with
`quotation.converted`; FR-27.1, AC-27.6); the hold release queue
(`GET /holds`, ACTIVE holds oldest first with the failed credit rule against
its threshold, the customer's credit limit and the order version for the
release call), which replaces the order list as the credit release queue
screen; supplementary item rules (`supplementary_item_rule`, create, list,
remove) with the stateless `:evaluate` (FR-27.14, the AC-27.10 quantity rule);
and web screens for all of these.

Slice 5 adds MANDATORY supplementary lines at order commit (FR-27.14,
BR-27.14, AC-27.10): every new order (UI and channel create, quotation
conversion, agreement release) gets the lines its MANDATORY rules add, after
its own lines, each referencing its parent line (`supplementary_of`,
`parentLineNo`), inheriting the parent's site, warehouse and requested date,
counted in the net total and returned as `SLS-0012` warnings; a PATCH refuses
a supplementary line's own quantity or date, recomputes it from a new parent
quantity, cancels it when the parent drops below the rule's base quantity and
moves its dates with the parent; `:cancel` cancels it with its parent. The web
order detail marks supplementary lines and lists the added items after
creation, and the change screen locks their quantity and date.

Slice 6 adds product proposals (FR-27.21, SAP VB41): `product_proposal_rule`
per (ordered product, customer or every customer, proposed product) with a
reason CROSS_SELL, UP_SELL or ACCESSORY (create, list, remove; duplicates are
`SLS-0034`); the stateless `POST /product-proposals:evaluate`, which never
proposes a product already on the order or being committed and lets a
customer's own rule win; and `POST /product-proposals:respond`
(`Idempotency-Key`), which stores each accept or decline in
`product_proposal_response` so the rule list returns accepted and declined
counts (the acceptance rate). The new-order screen shows the proposals for its
lines and adds a line for an accepted one; the proposal rules screen lists,
creates, removes and checks rules. A supplementary line cancelled for falling
below its base quantity now reopens when its parent reaches the base again.

Slice 7 adds product allocation quotas (FR-27.16, BR-27.15, SAP OVZJ):
`allocation_quota` per (product, customer or every customer, period) with
create (`Idempotency-Key`, duty `sales.allocation.maintain`, overlapping
periods are `SLS-0035`), the quota inquiry (`GET /allocation-quotas` with
quota, consumed and remaining), a read with the draw history and a PATCH of the
quantity under `If-Match` (below the consumed quantity is 409 `SLS-0036`).
Confirmation (`:confirm` and the release of a CREDIT hold) draws each line's
whole quantity from the quota of its requested period, in requested-date order
and under the quota rows' lock (taken in id order, so concurrent confirms
cannot deadlock); when that period cannot cover the line it is confirmed on the
first day of the next period that can, with an `SLS-0013` warning on the order,
and when no later period can the confirm is refused with 422 `SLS-0013` and
nothing is consumed. A CREDIT release whose confirmation allocation cannot
cover still releases the hold: only the confirmation rolls back (savepoint),
the order stays unconfirmed and the result carries the `SLS-0013` refusal as a
warning; the still-valid release lets a later confirm pass credit. A customer's own quotas replace the
all-customer quotas for that product, and a requested date no quota covers is
not limited. `:cancel` returns every draw to the period it came from, and a
PATCH that changes a confirmed line's quantity or date, or cancels or reopens
it, gives the draw back and draws again; the version diff records the date the
line really ends up with. Concurrent quota creates for one product are
serialised (advisory lock), so overlapping periods cannot slip in together.
Events `allocationquota.created`,
`allocationquota.changed`, `allocation.consumed` and `allocation.returned`; web
screens for the quota list, new quota and quota detail (change quantity, draw
history), and the order detail, the change screen and the release queue report
the `SLS-0013` move or refusal. The
asyncapi messages now all carry a title, so the doc gate reports no FAIL line.

Slice 10 stores backorder processing runs (FR-27.17, BR-27.11, AC-27.12):
`POST /backorder-runs` (duty `sales.backorder.run`, `Idempotency-Key`) reads the
open lines of one product at a site and warehouse from the CONFIRMED orders
(JOURNAL orders, supplementary lines and lines past RESERVED are left out; a
line's confirmed quantity is what its schedule lines promise a date for),
plans the strategy over the supply given in the request and stores a PREVIEW
`backorder_run` with one `backorder_run_line` per line (before, after, the
order version it was computed on, the date a gain is promised: the latest of
the requested date, the supply date and the line's quota period). Nothing
changes until `POST /backorder-runs/{id}:commit` (`If-Match`): in one
transaction, serialised per (product, site, warehouse) by an advisory lock and
with the orders locked in id order, each changed line's schedule lines are
rewritten so that exactly the run's quantity carries a date (reductions come
off the latest dates and become UNPROMISED, gains become one ATP slice on the
gain date), the confirmed ship date of the line and its supplementary lines
follows, each order gets a version, `order.changed`, `atp.promised` (source
`BACKORDER_RUN`, only for changed lines, AC-27.12) and `backorderrun.committed`;
an order that moved since the preview refuses the whole commit with 409
`SLS-0042`, and so does any open line of the product, site and warehouse that
holds another promised quantity than the preview saw (another run or a promise
moved it, or the order was confirmed after the preview), so two previews over
the same supply cannot both commit. A retried commit of a committed run is 409
`SLS-0041` whatever its ETag. List and read of runs, and web screens for the run list, the new
run and the run detail with the commit. The asyncapi and openapi specs report
no FAIL line on the doc gates.

Slice 11 stores the sales parameters of a legal entity (PRD §5.27.13) and
makes the quotation, order and credit release flows read them:
`GET /parameters?legalEntityId` (duty `sales.order.read`; a legal entity that
never saved them gets the defaults at version 0, without `updatedAt`) and
`PUT /parameters?legalEntityId` (duty `sales.parameter.maintain`, `If-Match`,
`"0"` to create, every value required, `parameters.changed` through the outbox
in the same transaction). A legal entity outside the caller's scope is refused
(403) and hidden by row-level security. The names and ranges are the PRD's:
`quotationExpiryDays` (30, 1–365) is the validity of a quotation created
without `validUntil`; `quotationProbabilityDefault` (50, 0–100) is the win
probability of a quotation created without `probabilityPct`;
`creditReleaseValidityDays` (7, 1–30) is how long a released CREDIT hold keeps
covering its order (BR-27.4, set when the hold is released);
`creditHoldSlaBusinessDays` (2, 1–10) gives every row of `GET /holds` a
`slaDueAt` (Monday to Friday in UTC, time of day kept) and the queue screen
marks a hold past it (FR-27.8); `defaultOrderType` (ORDER; JOURNAL, ORDER or
SUBSCRIPTION) is the type of an order created through `POST /orders` without
one. Quotation conversion and agreement releases still create ORDER-type orders
(see the decision row below). The request hash covers the request as sent, so a
replay after a change of a default is still a replay. The web gets a Sales
parameters page (save shown only with `sales.parameter.maintain`), the new
order form offers "Legal entity default" as its order type, the quotation form
leaves validity and probability to the parameters, and the queue shows the SLA
from the service. The asyncapi and openapi specs report no FAIL line on the doc
gates.

Everything below from the ticket's deliverables (1–9) and PRD §5.27 acceptance
criteria is not in these slices. Status is either a later slice of PSG-27 or a handoff to another
wave or team.

Slice 8 adds schedule lines (FR-27.6, AC-27.8): `order_schedule_line`
(CONFIRMED, BACKORDER, UNPROMISED, plus CANCELLED and SUPERSEDED history, with
`source` LINE or ATP and, for an ATP slice, the `bucket_date` it drew on, which
with the line's product, site and warehouse is the future `atp_bucket` key),
written at confirm (one line per order line) and kept in step by PATCH and
`:cancel`; `POST /orders/{id}/lines/{lineNo}:promise`, which places one line of
a CONFIRMED order against the ATP buckets in the request and stores the split
(what ships by the requested date is CONFIRMED, later supply is BACKORDER,
uncovered quantity is UNPROMISED with no date), moves the line's confirmed ship
date to the first delivery, never earlier than the quota period the line drew
(BR-27.15), refuses JOURNAL orders (BR-27.7), and writes a version,
`order.changed` and `atp.promised`; `GET /orders/{id}/schedule-lines`; the
backorder list `GET /backorders` by promised date; and web screens for the
backorder list (with a re-promise link per line), the promise form and the
schedule lines on the order detail.

Slice 9 stores scheduling-agreement releases (FR-27.20, BR-27.12, AC-27.13):
`POST /agreements/{id}/schedule-releases` (duty `sales.agreement.maintain`,
`Idempotency-Key`) stores a FORECAST or JIT release with the customer's release
number, cumulative received quantity and requirement buckets under the
agreement's row lock (refused with `SLS-0029` outside the agreement's
validity) and reconciles it against the agreement's cumulative shipped
quantity and tolerance. Within tolerance it is ACCEPTED and replaces the
previous release's open schedule lines in the same transaction; beyond it, it
is stored HELD (`CUM_QTY_MISMATCH`, `SLS-0014`) and no schedule line changes.
`POST .../schedule-releases/{releaseId}:resolve` (`If-Match`) accepts a held
release by override (open quantity against the current shipped quantity,
refused when a newer release is in effect) or rejects it, both with a reason;
`POST /agreements/{id}:record-shipped` (`If-Match`) sets the cumulative shipped
quantity (it may fall only when the reset date moves forward; the closed
period's shipped quantity then moves into the `shippedOffset` of the ACCEPTED
and HELD releases, so a reset never reopens what was already shipped against
them) and recalculates the open lines of the release in effect; list and read of releases with their
lines; `cumTolerance`, `cumShipped` and `cumResetDate` on the agreement; events
`schedulerelease.received`, `.resolved` and `.recalculated`; and web screens for
the release list, the cumulative shipped form, a new release and the release
detail with the held-release review.

Slice 12 adds the quotation expiry run and the sales workspace (FR-27.1,
PRD §5.27.14): `POST /v1/sales/quotations:expire` (duty
`sales.quotation.maintain`, optional `legalEntityId`) moves every DRAFT or SENT
quotation whose validity ended before today to EXPIRED, oldest first and up to
500 per run (`more` says to run again), under the rows' locks, bumping each
version and writing `quotation.expired` in the same transaction; it is
state-based, so repeating it expires nothing twice and it takes no
`Idempotency-Key`. A partly converted quotation is CONVERTED and is untouched.
`GET /v1/sales/summary` (duty `sales.order.read`) returns the counts behind the
workspace: ACTIVE holds, backorder schedule lines (the rows of the backorders
list), open quotations whose last valid day is today or one of the next six
days, open quotations already lapsed, the open pipeline per currency (count,
net value and value weighted by win probability; currencies are never added
together), and won (CONVERTED), lost and expired quotations with the
conversion rate won / (won + lost + expired) over `windowDays` (default 90, at
most 365; measured by each quotation's last update). The pipeline KPI filters
of PRD §5.27.14 narrow the pipeline only: `status` (DRAFT or SENT),
`minProbabilityPct` (0 to 100) and `expiringWithinDays` (1 to 365). Days are
UTC calendar days, as for the validity checks of `:send` and `:convert`. No
schema change. The web module adds the Sales workspace page; its expiry
action is shown only to a holder of `sales.quotation.maintain`.

Slice 13 adds change approval per customer (FR-27.9, PRD §5.27.13
`change_approval_required`): `customer_change_policy` (GET/PUT
`/customers/{customerId}/change-policy`, If-Match, `changepolicy.changed`);
a PATCH of a CONFIRMED order of a customer that needs approval, by a caller
without `sales.order.approve`, is validated and stored as a PENDING
`order_change_request` instead of applied (202, `orderchange.requested`; one
PENDING request per order, `SLS-0043`); `GET /change-requests` (the approval
queue and an order's change history), `GET /change-requests/{id}`,
`:approve` (applies the change through the PATCH path in the same transaction,
refuses the requester `SLS-0045` and an order that moved `SLS-0046`,
`order.changed` then `orderchange.approved`) and `:reject` (comment required,
`orderchange.rejected`); the web change request queue, request detail with
approve and reject, the customer change approval page, and the change screen's
"sent for approval" outcome. The OpenAPI and AsyncAPI documents pass the doc
gates with no FAIL lines.

Slice 14 adds the agreement lifecycle (FR-27.10, BR-27.9): `PATCH
/agreements/{id}` (duty `sales.agreement.maintain`, `If-Match`) revises the
commitment quantity (never below what is released, `SLS-0053`), the agreed
price, validity and cumulative tolerance, and `POST /agreements/{id}:close`
closes it early with a reason (`CLOSED`, no more releases or schedule
releases, `SLS-0052`); every change appends a `sales_agreement_version` row
with the field-level diff, readable at `GET /agreements/{id}/versions`, and
writes `agreement.changed` or `agreement.closed`. Cancelling an order that an
agreement release created gives its quantity back to the commitment in the
cancellation transaction (`agreement_return`, one per release, `agreement.returned`,
shown as "Returned" in the release history). A revised price applies to later
releases only. The web agreement page gains the revise and close forms and the
history table (migration 0018). The OpenAPI and AsyncAPI documents pass the
doc gates with no FAIL lines.

Slice 15 adds one-time customer orders (FR-27.22, AC-27.14, `SLS-0016`):
`POST /orders` takes an optional `oneTime` buyer (name, address, country, tax
id), kept on the order header and returned on every order response. The buyer
is personal data and is sealed at rest with `platform/go/fieldcrypt`
(`sales_order.one_time_party_sealed`, migration 0019: AES-GCM under a
per-tenant key, tenant and field bound, key id in the value for rotation); only
the country is kept in clear (`one_time_country`). The `Idempotency-Key`
fingerprint of a one-time request is `hmac:<key id>:` + a tenant-keyed MAC
instead of a plain SHA-256, and a replay after a key rotation still matches.
Without `FIELDCRYPT_KEYS_FILE` (chart `fieldCrypt.keysSecret`) one-time orders
are refused and a stored one cannot be read or changed (503 `SLS-0054`);
ordinary orders are unaffected. The order is held to the legal entity's
`one_time_order_limit` (new optional `oneTimeOrderLimit` and
`oneTimeOrderLimitCurrency` in the sales parameters, default 5,000 USD and
4,500 EUR) on create, on a change that raises the total, and on confirm
(422 `SLS-0016`, nothing stored); a CREDIT release that would confirm an order
the limit no longer allows releases the hold and returns `SLS-0016` as a
warning. `order.confirmed` marks a one-time order with `one_time` carrying the
country only (no name, address or tax id on the bus), and `parameters.changed`
carries the limit. The web new-order form gains the one-time customer section,
the order page shows the buyer, and the sales parameters page edits the limit.
The OpenAPI and AsyncAPI documents pass the doc gates with no FAIL lines.

Slice 16 adds alternative items (FR-27.13): `alternative_item_rule` (migration
0020, granted to `passage_app`; an ordered product, an alternative product, one
customer or every customer, a priority from 1 to 99 and optional validity days
judged on the requested date), `POST` / `GET` `/alternative-item-rules` and
`DELETE` `/alternative-item-rules/{id}` (duty `sales.listing.maintain` to
change, `sales.order.read` to read; `SLS-0055` for a second rule for the same
ordered product, customer and alternative, `SLS-0113` when absent), and the
stateless `POST /alternative-items:evaluate`. It places the quantity against
the ordered product's ATP buckets and, when less than the whole quantity ships
by the requested date, against the buckets sent for each applicable alternative
(a customer's own rule wins over the rule for every customer): offers that
cover the whole quantity come first, then by priority and quantity available;
an alternative with only later supply is no offer; one sent without buckets is
listed as `unchecked`. The web gains the alternative items list (filter,
remove with confirmation), the new rule form and the check card, under
`/sales/alternative-items`. The OpenAPI document passes the doc gates with no
FAIL lines; the slice emits no event.

Slice 17 completes the change approval of FR-27.9: `:cancel` of a CONFIRMED
order of a customer whose changes need approval, by a caller without
`sales.order.approve`, is stored as a PENDING change request of kind `CANCEL`
(202 with `pendingChange`, `SLS-0043` when a request already waits) instead of
cancelling; approving it cancels the order in the same transaction as the
requester's cancellation (`order.cancelled`, then `orderchange.approved`),
through the same path as a direct cancel, so the allocation, the schedule and
any sales agreement quantity are returned. Cancellability is checked again at
approval: an order that moved since the request (a new version, a status past
CONFIRMED, or a direct cancel by an approver) is `SLS-0046` and a line no
longer OPEN/RESERVED is `SLS-0025`; both leave the request PENDING so it can be
rejected or withdrawn. A request of either kind blocks the other (`SLS-0043`);
a caller with `sales.order.approve` still bypasses approval for both. The
requester can withdraw their own PENDING request with
`POST /change-requests/{id}:withdraw` (`If-Match`, duty `sales.order.write`,
`SLS-0114` for anyone else, `SLS-0044` once decided): the request ends as
WITHDRAWN and `orderchange.withdrawn.v1` is written in the same transaction.
Migration 0021 adds `order_change_request.kind` and the WITHDRAWN status (no new
table, so no new grant). `orderchange.requested`, `.approved` and `.rejected`
carry `kind`. The web change request list and detail show the type and a
Withdraw action, and the order change screen reports a cancellation sent for
approval. The OpenAPI and AsyncAPI documents pass the doc gates with no FAIL
lines.

Route row for #313: done, `POST /v1/sales/change-requests/*:withdraw` is
seeded in `policies/authz/data.json` under the existing duty
`sales.order.write`. No new duty was added.

Slice 18 adds the credit check points of FR-27.7 and open-order exposure from
the orders themselves. `credit_check_points` (migration 0022, a column on
`sales_parameter`, no new table so no new grant; default `confirm` and
`release`) is saved with the sales parameters (`creditCheckPoints`, optional in
the PUT, duty `sales.parameter.maintain`; every change is recorded by
`parameters.changed` with the actor and the new points). Sales runs three
points. `entry` evaluates a new order (UI, channel intake, a converted
quotation and an agreement release; JOURNAL orders are not checked), stores the
result on the order and writes `creditcheck.evaluated` but places no hold, so
the order screen shows the warning before confirmation. `confirm` is the check
that places the CREDIT hold and is required: a set without it is refused (400),
because entry and release hold nothing and dropping confirm would switch credit
control off for the whole legal entity; credit control is switched off per
customer with limit type NONE. `packing_slip` and `invoice` are PRD values but
are refused (400, "not active yet") until w28 and w29 run those checks, and the
parameters page shows them as disabled "not active yet" boxes; the column's
CHECK already allows them. `release` records a fresh evaluation when a CREDIT
hold is released; the release stands whatever it says. The event carries
`point`. The exposure `openOrders` is no longer taken from the profile PUT: it
is the net total of the customer's other CONFIRMED and PARTIALLY_SHIPPED ORDER
and SUBSCRIPTION orders in the profile currency, computed at every check and on
every profile read and list (a value sent in the PUT is not stored), and counts
for limit type BALANCE_ALL (BR-27.3). The sum reads a partial index on the
open orders (`sales_order_open_exposure`), and the check locks the customer's
credit profile row for its transaction, so two orders of one customer
confirming at once are checked one after the other and the second counts the
first. Open orders in a currency other than the profile's cannot be converted
yet (no w12 FX rate): they are returned as `openOrdersOtherCurrencies` on the
profile, and a BALANCE_ALL check with any of them is at least a CR-1 WARN whose
note names them, so the understated exposure is not silent. The OpenAPI and
AsyncAPI documents pass the doc gates with no FAIL lines.

No new duty and no route change: the parameters routes and credit profile routes
are unchanged.

Slice 19 adds the intake dedup window and the credit review-due list.
`intake_dedup_window_days` (BR-27.10, migration 0023) is a sales parameter per
legal entity, 1 to 90 (the PRD range), default 30, optional in the parameters
PUT, in the parameters read and in `parameters.changed`. A repeat of
`(channel, externalRef)` for the same legal entity whose order is younger than
the window returns the stored order (200) or, with another body, 409
`SLS-0007`; an order that many days old no longer matches, so the reference
opens a new order and a repeat then finds the newest one. The reference is
matched within the order's legal entity, so the result does not depend on
which legal entities row-level security lets the caller see; the same
reference in two legal entities opens one order in each. The permanent
`UNIQUE (tenant_id, channel, external_ref)` of migration 0001 is replaced by an
index on `(tenant_id, legal_entity_id, channel, external_ref, created_at)`, and
concurrent first intakes of one reference are serialised by a transaction
advisory lock keyed `sales.intake:` plus tenant, legal entity, channel and
reference, so one order is created (`TestPostgres_IntakeDedupConcurrent`). An
Idempotency-Key still never expires. Decision to confirm: the PRD lists the
parameter at tenant scope; it is kept per legal entity with the other sales
parameters (a tenant-level setting needs a tenant parameters route and its
gateway row). `GET /credit-profiles?reviewDueBy=` lists the profiles whose
review date is on or before a day, oldest first (the list a credit manager
works from before CR-4 holds orders); the web credit profile list has a
"Review due by" filter and an empty state for it. No new duty, route row or
table: the list uses `sales.order.read` on the existing route, and
`sales_parameter` and `sales_order` are already granted.

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Quotation → order conversion, quotation lifecycle (`:send`, `:convert`, expiry, probability) | implemented in part | Built: priced quotations with validity (default +30 days, at most 365) and win probability (default 50), `SQ-` numbering per legal entity, list and read, `:send` (DRAFT, still valid), `:lose` with a reason, and `:convert` of all or selected open lines into one DRAFT order (channel `QUOTE`) at the quoted price and discount under the quotation's row lock, with the converted lines naming the order and `quotation.converted` in the same transaction; later conversions take the remaining lines. Slice 12 adds `POST /quotations:expire`, which sets EXPIRED (see slice 12 above). Not built: the scheduler that calls it hourly (handoff, below), CANCELLED, editing a quotation's lines or pricing them through the w26 resolver (lines are entered with prices and fixed at creation), `price_explain_ref` on quotation and order lines (FR-27.1 "preserving explain refs"; the w26 explain reference does not exist on either line yet), the quotation document (PDF, email, UBL JSON; PRD §5.27.15) and the lost/expired notice, the `quotation_expiry_days` and `quotation_probability_default` parameters (constants 30 and 50), a listing check at quotation entry (the check runs at conversion), and the customer group on the converted order (group listing entries apply only to orders that name it). `:send` and `:convert` are versioned by `If-Match` rather than the PRD's `Idempotency-Key`: a retry with the old ETag is refused (412) instead of replayed, as for holds. |
| 1 | Sales agreements, release orders, commitment decrement (FR-27.10, BR-27.9, AC-27.9 `AGREEMENT_EXHAUSTED`) | implemented in part | Built: one-product quantity commitments at an agreed price with validity, `SA-` numbering, list and read with release history, and `:release-order`, which under the agreement's row lock refuses a release beyond the remaining quantity (`SLS-0008`) or outside the validity period (`SLS-0029`), creates a DRAFT order (channel `AGREEMENT`) of one line at the agreement price and stores the release, all in one transaction. Not built: commitments by amount or for several products, the `overdelivery_pct` parameter (0 is applied), re-pricing of already released draft orders when the agreement price changes (BR-27.9; a revised price applies to later releases only), `agreement_line_id` on the order line (the link is `agreement_release.sales_order_id`), returning quantity when a released order's lines are reduced by PATCH (only whole-order cancellation returns), and the agreement confirmation document (PRD §5.27.15). |
| 1 | Order types JOURNAL / SUBSCRIPTION / RETURNED behaviour | later slice of PSG-27 | Types are accepted on create and stored, but JOURNAL no-reserve/no-ATP (BR-27.7) and the subscription/returned flows are not implemented. RETURNED order flow is wave 30. |
| 2 | Cumulative ATP on stored supply and demand (`atp_bucket` projection, lag < 30 s, planning batch-end marker) | later slice of PSG-27 | Only the pure cumulative/promise computation and a stateless `POST /v1/sales/atp:check` exist. The projection needs supply/demand events from w16/w17/w21/w22. |
| 2 | Delivery date control per line (NONE / SALES_LEAD_TIME / ATP / CTP; transport days; receiving calendar; past-date warning; fence-end TIME_FENCE) (FR-27.3, BR-27.2) | later slice of PSG-27 | Domain promise logic exists; per-line control, calendar and transport days are not wired into confirm. Calendar comes from w6/w48. |
| 2 | Schedule lines persisted per line (`order_schedule_line`, partial delivery split, AC-27.8 backorder list) | implemented in part | Built: see slice 8 above. Not built: `partial_delivery_allowed` / `overdelivery` parameters per customer (the caller chooses `completeOnly` per promise), manual split or merge of schedule lines, quantities delivered per schedule line (shipments are wave 28, which will drive the status of a schedule line), and a cancel-remainder action (`remaining_qty = 0` needs delivered quantities). A PATCH that changes a line's quantity or date resets its schedule to one line at the confirmed date, dropping a split, until the projection re-promises it. |
| 2 | Backorder handling and backorder list by promised date (FR-27.6) | implemented in part | Built: `GET /backorders` (BACKORDER and UNPROMISED schedule lines of CONFIRMED orders, by promised date, undated last, with days late) and the web list. The backorder run preview (deliverable 7) still takes its lines in the request; reading these lines as its input waits on the projection. |
| 2 | Alternative items (FR-27.13) | implemented in part; handoff for the ATP projection and w8 configured products | Built: see slice 16 above. Not built: the evaluation takes the ATP buckets of the ordered and the alternative products in the request (no stored `atp_bucket` projection yet, as for `atp:check`); swapping the product on a line from an offer (the user changes the line with PATCH today) and recording which offers were taken; an alternative chosen from a w8 configured or related product instead of a rule. |
| 2 | CTP via scheduling (FR-27.5, planned order link) | handoff | Needs the w23 finite-capacity CTP API. Not built here. |
| 3 | Credit limits, CR-1..CR-6, holds, release workflow | implemented in part | CR-1..CR-6 are implemented in the domain and run at confirm. Gaps are listed below. |
| 3 | CR-1 amounts converted to the profile currency at the wave 12 spot rate (BR-27.3) | handoff (w12 FX rates) | No rate source yet. An order in another currency than its profile fails CR-1 with a note, so a credit manager reviews it instead of the check comparing across currencies. CR-6 uses the order currency's threshold. |
| 3 | BR-27.3 "open AR + unapplied" | implemented as open AR − unapplied | Exposure amounts are stored non-negative, so unapplied cash (paid, not yet applied) is subtracted as a credit. Confirm the sign with the receivables owner when the w29/w30 projection replaces the PUT body. |
| 3 | Credit exposure from open AR, open items and unapplied cash (BR-27.3 inputs) | handoff | Open AR lives in `receivables_db.open_item` (w29/w30). Exposure is supplied in the credit-profile PUT body until that projection exists. |
| 3 | Credit check at entry, invoicing and release points; `credit_check_points` setting (FR-27.7) | implemented in part | Built (slice 18): the setting and the entry, confirm and release points, see slice 18 above. Not built: the packing slip and invoice points. Handoff to w28 and w29: run the credit check at packing slip / invoice posting, then make sales accept the value (`activeCheckPoints` in `internal/domain/parameters.go`) and count it as a blocking point (`blockingCheckPoints`), so a set may then leave out confirm. |
| 3 | `credit-check.evaluated` event and stored evaluations for every check | implemented for the entry, confirm and release points | Every check emits `creditcheck.evaluated` with its `point` in the same transaction and stores the evaluation on the order (`credit_check_result`, the latest check). Packing slip and invoice checks are not built (row above). |
| 3 | Credit exposure recompute and nightly reconciliation (`exposure.open_orders`) | implemented in part | Built (slice 18): `openOrders` is computed from this service's own CONFIRMED and PARTIALLY_SHIPPED orders at every check and on every profile read, so it needs no recompute job and there is no stored figure to reconcile. Not built: the nightly reconciliation job (nothing to reconcile until the w29/w30 projection stores exposure), conversion of orders in a currency other than the profile's (no FX rate, w12; they are listed and flagged instead), the shipped part of a PARTIALLY_SHIPPED order (the whole net total counts until w28 reports shipped quantities, which overstates exposure) and `deliveredNotInvoiced` from shipped quantities (w28/w29). |
| 3 | Credit review warnings daily job (06:00), review_date handling | implemented in part | Built (slice 19): `GET /credit-profiles?reviewDueBy=` and the web filter list the profiles due for review; the CR-4 review-date rule runs at confirm. Not built: the 06:00 job that notifies the credit manager (handoff: deployment-cicd CronJob and w3 notifications; it can call this list). |
| 4 | Holds: FRAUD / MANUAL / PRICE_REVIEW apply endpoint (`POST /orders/{id}/holds`) | implemented | Duty `sales.order.hold`; DRAFT, OPEN or CONFIRMED orders; one ACTIVE hold per type; the hold, version bump and `orderhold.applied` commit together. A FRAUD hold blocks confirmation. The request is versioned by `If-Match` rather than the PRD's `Idempotency-Key`: a retry with the old ETag is refused (412) instead of replayed. |
| 3, 4 | Credit release queue (PRD §5.27.6, §5.27.14: customer, order, amount, exposure vs limit, rule failed, age; release/reject with comment) | implemented in part | Built: `GET /holds` (duty `sales.credit.release` or `sales.order.hold`), ACTIVE holds oldest first with the order, amount, failed rule (for CR-1 the exposure plus order against the limit with tolerance), credit limit, age and SLA due time (`slaDueAt`, the legal entity's `credit_hold_sla_business_days` counted Monday to Friday, slice 11), and the queue screen releasing a hold at the order's ETag and flagging a hold past its due time. Not built: a reject action with a comment (no hold rejection state exists), the exposure breakdown by component (the profile screen shows it), and public holidays in the SLA count (the w6 work calendar is not read). |
| 4 | Temporal release workflow with approvers by duty and 2-business-day SLA timer (FR-27.8, AC E2E "SLA timer fires") | later slice of PSG-27; handoff for the Temporal namespace `sales` (deployment-cicd) | Release is a synchronous API call. The SLA due time is computed per legal entity (slice 11) and shown on the queue, but no workflow, no timer that fires and no escalation exist. |
| 4 | Release validity re-trigger on amount increase > 5% (BR-27.4) | implemented | A CREDIT release covers its own order only while it is valid (7 days) and the amount is within the released amount plus 5% (`domain.ReleaseCovers`). At confirm, and when a PATCH raises a CONFIRMED order's total, a failing credit check that no valid release covers places a CREDIT hold. |
| 4 | MANUAL / PRICE_REVIEW blocking warehouse release (BR-27.4) | handoff (w28) | The holds are placed and listed on the order; warehouse release belongs to fulfillment. |
| 4 | Change management: PATCH with If-Match, version per change, field-level diff, selective re-pricing/ATP/credit, `order.changed` and `order.cancelled` events, optional approval per customer (FR-27.9, AC-27.7) | implemented in part | Built: PATCH, `:cancel`, version history and both events. Re-pricing recomputes the changed lines' nets from the entered price (the w26 price resolver is not called; see "Order entry grid" under Web). Re-promise sets a changed line's confirmed date to its requested date, the delivery date control confirm uses until the ATP projection exists. Slice 13 adds the optional approval per customer: see the slice 13 note above and the FR-27.9 change approval row below. Not built: `/lines/{l}:cancel-remainder` (needs `delivered_qty`/`remaining_qty` from w28) and adding or removing lines by PATCH. |
| 4 | Line-level status machine beyond confirm (RESERVED / PICKED / DELIVERED / INVOICED) and order status derivation (BR-27.5) | later slice of PSG-27 | Confirm sets lines to OPEN with confirmed dates. Reservation and later states belong to w16/w28/w29. |
| 4 | Order status CANCELLED path | implemented | `:cancel` from DRAFT, OPEN or CONFIRMED when no line is past RESERVED (BR-27.5); ACTIVE holds move to `CLEARED`. Reservations are not released because none exist yet (BR-27.7, w16). |
| 5 | Customer portal order entry and status | handoff | Wave 30 (portal UI). The intake API it will use exists. |
| 5 | Multi-channel intake: `(channel, externalRef)` dedup (BR-27.10) | implemented in part | Same-key replay (200) and different payload (409 SLS-0007) are implemented and tested. The dedup window (`intake_dedup_window_days`, default 30 days, per legal entity) is enforced since slice 19; a repeat after the window opens a new order, and a reference is matched within its legal entity, not across the tenant (the PRD's tenant scope is a decision to confirm, see slice 19). Not built: the window does not apply to the other intake kinds (quotations, agreements and releases dedupe on their Idempotency-Key only). |
| 5 | Intake rate limit per key (`INTAKE_RATE_LIMIT_PER_KEY=1000/min`, 429 with Retry-After) | later slice of PSG-27; handoff for shared rate-limit store (Valkey addendum, open question) | Needs the kernel rate-limit middleware; no per-pod limiter here. |
| 5 | ChannelIntake client credentials (service accounts) | handoff | Keycloak `_internal`/client registration belongs to edge-identity and w35 integration. The duty `sales.channel.intake` is enforced in code. |
| 6 | Supplementary items (FR-27.14, BR-27.14, AC-27.10) | implemented in part; later slice of PSG-27; handoff for w26 free-goods proposals | Built: `supplementary_item_rule` per (ordered product, customer or every customer, item) with MANDATORY/OPTIONAL, quantity per base quantity, FREE or PRICED; create (duty `sales.listing.maintain`), list, remove; the stateless `POST /supplementary-items:evaluate`, which returns the AC-27.10 lines (1 per 10 on 25 gives 2, free) with a customer's own rule replacing the all-customers rule for the same item; MANDATORY lines added in the order-create transaction of every order (create, quotation conversion, agreement release) with `supplementary_of` (the parent line id), the rule id and the rule's base quantity and quantity per base kept on the line, the rule's unit and price (0.00 for FREE), no discount, and an `SLS-0012` warning per added line on the create result; BR-27.14 on PATCH (the supplementary line's own quantity and date are refused with `SLS-0024`, a new parent quantity recomputes it, a parent below the base quantity cancels it, parent dates carry over, and the follow-on changes are in the version diff and `order.changed`); cancellation with the order. A supplementary line cancelled because its parent fell below the base quantity reopens, recomputed, when the open parent reaches the base again (slice 6). Not built (later slice): prompting for OPTIONAL items at line commit in an order-entry grid (the evaluation endpoint is the prompt; there is no grid, see Web) and the `supplementary_prompt_optional` parameter; a MANDATORY line for a parent that was below the base quantity when the order was created (no line exists to reopen; PATCH cannot add lines); adding a supplementary line for a parent line added by PATCH (PATCH cannot add lines); the delivery address (BR-27.14 inheritance), since orders carry no address yet (w7); and a listing check on the supplementary item itself (only the ordered lines are checked; the item goes with its parent). Not built (handoff): product-group and customer-group rule scopes (w8 product groups, w7 customer groups) and the w26 FREE_GOODS proposals. |
| 6 | Product listing / exclusion (FR-27.15, BR-27.13, AC-27.11 `PRODUCT_NOT_LISTED`) | implemented in part | Built: `product_listing` (ALLOW/DENY per customer or group, validity), create, inquiry and remove, and the BR-27.13 check on every line of a new order on the order date: a denied line is 422 `SLS-0011` unless the caller has `sales.order.approve` and gives `listingOverrideReason`, and each accepted line is logged in `listingOverrides`. Not built: the customer group from w7 (handoff; the order takes `customerGroup` in the request until the party lookup exists, so a caller that omits it is checked at customer level only), the `listing_override_duty` parameter (the duty is fixed), and a check at line commit in an order-entry grid (no grid yet; see Web). PATCH cannot change a line's product, so changes need no re-check. |
| 6 | Cross-selling and product proposals (FR-27.21) | implemented in part; handoff for w8 related products | Built: `product_proposal_rule` (ordered product → proposed product, CROSS_SELL / UP_SELL / ACCESSORY, one customer or every customer; create with duty `sales.listing.maintain`, list by legal entity and ordered products, remove); the stateless `POST /product-proposals:evaluate` over the products being committed and those already on the order (nothing is created); `POST /product-proposals:respond` with `Idempotency-Key`, storing each accept or decline once in `product_proposal_response` (kept when the rule is removed) and returning accepted and declined counts per rule on the list; the new-order screen proposes products for its lines, adds a line for an accepted proposal and records the answer. Not built: rules derived from w8 related products (handoff: no related-products API yet; rules are entered by hand), product-group and customer-group scopes (w8, w7), proposals on the change and quotation screens and in the channel intake response, an analytics event for responses (the PRD event table names none; the counts are read from the rule list), and the dynamic product proposal from the customer's order history (SAP dynamic proposal). |
| 7 | Product allocation quotas (FR-27.16, BR-27.15, AC for SLS-0013) | implemented in part | Built: see slice 7 above. Not built: quota by product group or region (handoff: w8 product groups, w7 customer regions; a quota is per product and customer or every customer), splitting one line across periods (a line is confirmed whole in one period, so a line larger than any period's remaining quantity is refused), the `allocation_enabled` legal-entity parameter (a product is allocated when it has a quota covering the requested date; there is no parameter table), the first-of-period roll job (00:05), and drawing from ATP confirmation rather than the requested-date confirm (the ATP projection is not built). |
| 7 | Backorder processing runs WIN / GAIN / REDISTRIBUTE / FILL / LOSE with preview (FR-27.17, BR-27.11, AC-27.12) | implemented in part | Built: the stateless preview `POST /backorder-runs:preview`, and stored runs read from the confirmed orders with commit (see slice 10 above). Not built: supply read from the ATP projection (the run takes the supply total and its date in the request until the projection exists), the priority class from the customer (w7; the request names it per customer with a default of 5), a quota draw is not returned or moved when a line is deconfirmed or gains a date (the draw stays with the line), holds on an order are not considered, the 05:00 daily job, partial commit (a stale order refuses the whole run by design) and a discard of a PREVIEW. The lose threshold is a request field (default 3), not the legal-entity parameter. |
| 8 | Scheduling-agreement forecast/JIT releases and cumulative reconciliation (FR-27.20, BR-27.12, AC-27.13, `CUM_QTY_MISMATCH`) | implemented in part; handoff for DELFOR/DELJIT mapping (w35) and shipped quantities (w28) | Built: the stateless `POST /schedule-releases:check` and stored releases per agreement (see slice 9 above): reconciliation, hold, atomic replacement of the open lines, override or rejection of a held release, cumulative shipped with a reset date, recalculation, events. Not built: cumulative shipped taken from w28 shipments (handoff; it is entered through `:record-shipped` until then), the `cum_reset_rule` parameter (FISCAL_YEAR / CALENDAR_YEAR / NEVER; the reset date is set by hand), a tolerance per legal entity or customer (it is a field of the agreement, default 0), a release covering several products (an agreement has one product), generating or adjusting order schedule lines and orders from the open lines (the lines are the agreement's own and nothing yet creates orders from them), the release acknowledgment document (PDF, EDI), a four-eyes rule on the override (any holder of `sales.agreement.maintain` can resolve), and any bound on a release's content: requirement dates after the agreement's `validTo` and cumulative required beyond the commitment are stored as received (only the receipt date is checked against validity). A rejected release number cannot be sent again; the customer's correction needs a new number. |
| 9 | Customer consignment fill-up / issue / pick-up / return (FR-27.18) | later slice of PSG-27; handoff for the w16 owner dimension (CUSTOMER_LOCATION) | Depends on w16 ownership dimension. |
| 9 | Sales-order special stock segregation (FR-27.19) | later slice of PSG-27; handoff for w16 reservation segment and w21 production flag | Depends on w16 reservations and w21 production orders. |
| 9 | One-time customer orders (FR-27.22, AC-27.14, `one_time_order_limit`) | implemented in part; handoff for the w7 ONE_TIME party role and w29/w30 | Built: see slice 15 above. Not built: the order is sold to a shared one-time account whose id the caller supplies as `customerId` (the w7 ONE_TIME role and one account per legal entity are not enforced: any customer id may carry a `oneTime` buyer, and nothing yet stops a regular customer's order from naming one); the limit is checked against this order's own net total, not against the one-time account's other open orders or its open AR (`BALANCE` exposure needs the w29/w30 projection); a limit stated in one currency is not converted to another (w12 FX), so an order in a different currency than the configured limit is refused until a limit in that currency is saved. The invoice and open item copying the buyer (AC-27.14 second half) is w29/w30: on an `order.confirmed` with `one_time`, read the buyer from `GET /v1/sales/orders/{id}` (it is not on the event) and keep it sealed on their side too. No retention or erasure path for the sealed buyer yet (handoff to the compliance privacy owner, as for the receiving one-time payee); the buyer cannot be corrected after create. |
| 9 | Sales-order cost estimate hook `POST /orders/{id}/lines/{l}:estimate-cost` (FR-27.23) | handoff | Calls w20 cost snapshot and w39 simulation; neither API exists yet. Full costing is w39. |

## Functional requirements not covered above

| Requirement | Status | Reason |
|---|---|---|
| FR-27.11 configured products and make-to-order production link (AC-27.4) | implemented in part | Built: `configurationId` on an order line (migration 0017), checked on order create against the product service (`GET /v1/product/saved-configurations/{id}?productId=`, service token holding only `product.configuration.read`): unknown, not valid or another product's configuration is `SLS-0047`, an unreachable product service `SLS-0048` (fail closed, nothing stored); carried in `order.confirmed` as `configuration_id`; `POST /orders/{id}:plan-production` (duty `sales.order.plan-production`) creates, per configured line of a CONFIRMED order, the production order with origin `SalesOrderLine` through `POST /v1/production/sales-line-orders` (service token holding only `production.order.create-from-sales`, `Idempotency-Key` `sales-line-<lineId>`) and links it in `order_line_production` (RLS tenant + restrictive legal entity); idempotent per line on both sides. Integration style: synchronous HTTP with a service grant, the repo's pattern between services; the Kafka consumer of #405 is not used because sales events reach Kafka only through the outbox relay and no production consumer deployment exists. Not built: planning on confirm (the call is explicit, so a confirm never depends on production being up; a scheduler or an `order.confirmed` consumer in production can call the same route), the BOM and route of the configured product (production creates the order with none, to be resolved from the configuration with w9), re-planning after a PATCH of the line quantity or a cancel (the production order keeps the quantity it was planned with), and the production-side status on the order line. |
| FR-27.13 alternative items | implemented in part | See deliverable 2 and slice 16. |
| FR-27.9 change approval: notification of the manager, approval by amount threshold or by a named approver group | later slice of PSG-27 | Slice 13 built the per customer switch, the stored request and approve/reject; slice 17 added withdrawal by the requester and cancellations. The queue is polled, nothing notifies a manager; every approver holding `sales.order.approve` can decide any request. |
| Duties `sales.order.approve` for change requests and the customer change policy | done (seeded in `policies/authz/data.json`) | `sales.order.approve` is seeded with the change request decisions and the customer change policy PUT. |
| Change requests of other types of change (hold) | later slice of PSG-27 | A PATCH and `:cancel` (slice 17) are routed to approval; holds apply at once. |
| Duty for withdrawing a change request | decision to confirm | PRD §5.27.17 names no duty for a withdrawal. `:withdraw` requires `sales.order.write` (the PATCH duty) and that the caller is the requester; a user holding only `sales.order.confirm` who asked for a cancellation needs `sales.order.write` to withdraw it. |
| Duty for supplementary item rules | decision to confirm | PRD §5.27.17 names no duty for maintaining supplementary item rules. Create and remove require `sales.listing.maintain`, the product master duty this service already has; the evaluation requires `sales.order.read`. |
| Duty for product proposal rules and responses | decision to confirm | PRD §5.27.17 names no duty for FR-27.21. Create and remove require `sales.listing.maintain`, like supplementary item rules; the evaluation requires `sales.order.read`; recording a response requires `sales.order.write` or `sales.channel.intake`, the duties of order entry. |
| Duty for listing maintenance | decision to confirm | PRD §5.27.17 names no duty for maintaining `product_listing`. Create and remove require `sales.listing.maintain`; the override requires `sales.order.approve` (SalesOrderApprove). Neither has a role mapping yet (edge-identity). |
| PRD §5.27.6 duty for `:cancel` | decision to confirm | The API table maps `:cancel` to SalesOrderConfirm, so `:cancel` requires `sales.order.confirm`. PATCH requires `sales.order.write` (SalesOrderMaintain). The release check uses `sales.agreement.maintain` (SalesAgreementMaintain) and the backorder preview `sales.backorder.run` (BackorderRun); neither duty has a role mapping yet (edge-identity). |
| BR-27.6 advisory lock per (product, dims, site, warehouse) around ATP consumption | later slice of PSG-27 | ATP is not yet consumed by confirm or by `:promise` (which places a line on buckets supplied in the request and takes the order row lock only); the lock belongs with the projection. |
| BR-27.7 reservation on confirm (`reserve_on_confirm`) | handoff (w16) | Reservation API belongs to inventory. |
| BR-27.8 quantity rules (`whole_units`, unit conversion via w6, half-even) | later slice of PSG-27 | Decimal quantities are accepted at 6 places; whole-unit product flags and unit conversion (w6) are not applied. |
| Allocation precedence and timing | decision to confirm | A customer's own quotas replace the all-customer quotas for that product in every period (a customer with only a March quota is unconstrained in April even if an all-customer April quota exists); a quota draw moves the quota's version, so a planner's PATCH after a confirm needs a reload (412); quota is not drawn for an order confirmed before its quota existed until that line changes. |
| Duty and route rows for alternative item rules | decision to confirm; route rows done (seeded in `policies/authz/data.json`) | PRD §5.27.17 names no duty for FR-27.13. Create and remove require `sales.listing.maintain`, like supplementary item rules and product proposal rules; the list and the evaluation require `sales.order.read`. No new duty; the four routes are seeded next to the proposal rule rows: `sales.listing.maintain` carries POST `/v1/sales/alternative-item-rules` and DELETE `/v1/sales/alternative-item-rules/*`; `sales.order.read` carries GET `/v1/sales/alternative-item-rules` and POST `/v1/sales/alternative-items:evaluate`. The duty choice is still to confirm. |
| Alternative item ranking and rule shape (FR-27.13) | decision to confirm | The PRD says only "offer the configured alternative with its ATP". Defaults chosen: an offer that covers the whole quantity by the requested date ranks before a higher-priority partial one, then priority, then quantity available; an alternative with only later supply is not offered (the user can still keep the ordered product's later promise); for one alternative product a customer's own rule replaces the rule for every customer, while the general rules for other alternatives still apply to that customer. Rules are per legal entity: a customer bought from several legal entities needs a rule in each. Only one level is evaluated: A to B and B to A may both exist (mutual substitutes) and A to B to C does not offer C for A, so no cycle can loop; an alternative pointing to itself is refused (400 and a table check). Product and customer ids are checked as UUIDs only, as for supplementary and proposal rules: there is no product (w8) or customer (w7) lookup to confirm they exist or that the alternative shares the ordered product's unit. Rules are removed with `DELETE` and no event, like product listings, supplementary item rules and product proposal rules; the row's `created_by` is the only audit until the service's master-data changes are audited. The `sales.alternative_items` flag is not read (see config flags below). |
| Duty for stored backorder runs | done (seeded in `policies/authz/data.json`) | `sales.backorder.run` and `sales.order.read` are seeded with the stored backorder run routes. The PRD says a commit is also logged with SalesOrderMaintain on the affected orders; the commit records the actor in each order version instead of requiring the second duty. |
| Duty for sales parameters | done (seeded in `policies/authz/data.json`) | `sales.parameter.maintain` is seeded with the parameters PUT; reading stays on `sales.order.read`. A role mapping is edge-identity. |
| Duty for allocation quotas | decision to confirm | The PRD names AllocationMaintain (with BackorderRun, demand planner). Create and change require `sales.allocation.maintain`; the inquiry requires `sales.order.read`. The duty has no role mapping yet (edge-identity). |
| Parameters from PRD §5.27.13 | partly built | Built (slice 11): `default_order_type`, `quotation_expiry_days`, `quotation_probability_default`, `credit_release_validity_days` and `credit_hold_sla_business_days`, stored per legal entity and read by their flows. Built (slice 18): `credit_check_points`. Built (slice 19): `intake_dedup_window_days`. Built (slice 13): `change_approval_required`, kept by this service per legal entity and customer in `customer_change_policy` until the w7 customer master carries it (handoff: when w7 adds the field, sales reads it from there or keeps this table as its copy). Not built: `delivery_date_control_default` and the `atp_time_fence_days` / `atp_include_planned_orders` / `atp_include_safety_stock` settings (the ATP projection is not built; confirm uses the requested date), `reserve_on_confirm` (reservation is w16), `partial_delivery_default` and `overdelivery_pct` / `underdelivery_pct` (they need delivered quantities from w28 and the w7 customer and product fields), `credit_limit_type_default`, `mandatory_credit_check` and the credit tolerance defaults (each credit profile carries its own limit type and tolerance), `intake_rate_limit_per_key` (gateway), `supplementary_prompt_optional`, `listing_override_duty` (the override always needs `sales.order.approve`), `allocation_enabled`, `backorder_strategy_default` / `lose_threshold` (each run states its strategy and threshold), `cum_tolerance` / `cum_reset_rule`, `sales_order_stock_default` (product scope, w8). Built (slice 15): `one_time_order_limit` with its currency. The SLA is a Monday-to-Friday count in UTC: a public-holiday calendar and the legal entity's time zone are w6/w48. |
| Default order type on converted quotations and agreement releases | decision to confirm | `default_order_type` applies to `POST /orders` without `orderType` (and to the web form's "Legal entity default"). Converting a quotation and releasing from a sales agreement keep creating ORDER-type orders, as in AX where both create a sales order of type Sales order; a JOURNAL or SUBSCRIPTION default would change what a conversion reserves and invoices. |

## Events (PRD §5.27.5)

| Event | Status | Reason |
|---|---|---|
| `dev.passage.sales.order.confirmed.v1` | implemented | Written to the outbox in the confirm transaction, or in the release transaction when the CREDIT release confirms the order. Slice 15 adds the optional `one_time` marker of a one-time customer order with the buyer's country only; the name, address and tax id stay sealed in sales. |
| `dev.passage.sales.orderhold.applied.v1` / `.released.v1` | implemented for CREDIT holds | Applied is emitted when confirm places a CREDIT hold; released is emitted on release. Other hold types wait for the apply endpoint. Type names follow the convention `dev.passage.<ctx>.<entity>.<verb>.v<n>` (`orderhold`), not the PRD's `passage.sales.order-hold.*`; see note below. |
| `dev.passage.sales.creditcheck.evaluated.v1` | implemented | Emitted on every credit check (entry, confirm, release; `point` names which) with each rule's result, value and threshold. |
| `dev.passage.sales.order.changed.v1` / `cancelled.v1` | implemented | Written to the outbox in the PATCH and cancel transactions. `changed` carries the new version, reason, actor, the diff (`line_no` 0 for header fields) and the re-promised line numbers; `cancelled` carries the cancelled line ids. A PATCH that raises a confirmed order's total also writes `creditcheck.evaluated` and, on a hold, `orderhold.applied`. |
| `dev.passage.sales.atp.promised.v1` | implemented for the promise endpoint | Written in the `:promise` transaction with the line (product, site, warehouse), the requested date, the promised (confirmed) date, the source (`ATP`, or `ALLOCATION` when the quota period held the promise back), the schedule slices with the bucket date each drew on, and the backorder quantity. The PRD's single `bucket_date` is per slice because a partial promise draws on several buckets. It is not yet emitted by confirm or by a backorder run (needs the projection). |
| `dev.passage.sales.quotation.converted.v1` | implemented | Written in the `:convert` transaction with the quotation and order ids and numbers, the customer, the converted quotation line numbers, the actor and the new quotation version. |
| `dev.passage.sales.agreement.released.v1` | implemented (not in the PRD event table) | Written in the `:release-order` transaction with the created order id, the drawn, released and remaining quantities and the agreement status, so planning and key-account reporting can follow commitments. Confirm with the events owner. |
| `dev.passage.sales.agreement.changed.v1` / `.closed.v1` / `.returned.v1` | implemented (not in the PRD event table) | Written in the transaction of the PATCH, the `:close` and the order cancellation that gives a release's quantity back, with the agreement, legal entity, customer, product, the commitment, released and remaining quantities and the price after the change, the status, the field diff (`line_no` 0), the reason, the new version and the actor; `returned` adds the cancelled order and the returned quantity. Confirm with the events owner. |
| `dev.passage.sales.allocationquota.created.v1` / `.changed.v1`, `dev.passage.sales.allocation.consumed.v1` / `.returned.v1` | implemented (not in the PRD event table) | Written in the transaction of the quota create or change, of the confirm that draws the quota (also when a CREDIT release confirms the order), and of the cancel or change that returns it. `consumed` carries the quota, order and line, the quantity, the quota after the draw and, for a line moved to a later period, the new date and source `ALLOCATION`. Confirm the names with the events owner. |
| `dev.passage.sales.backorderrun.committed.v1` | implemented (not in the PRD event table) | Written in the commit transaction with the run, legal entity, product, site, warehouse, strategy, supply, allocated quantity, deconfirmed count and the changed lines (before and after quantity). `atp.promised` is written per changed line in the same transaction with source `BACKORDER_RUN`. Confirm the name and payload with the events owner. |
| `dev.passage.sales.schedulerelease.received.v1` / `.resolved.v1` / `.recalculated.v1` | implemented (not in the PRD event table) | Written in the transaction of the release store (status ACCEPTED or HELD, with the open lines and the superseded release), of the override or rejection of a held release, and of a shipped-quantity change that recalculates the release in effect. Confirm the names and payload with the events owner. |
| `dev.passage.sales.quotation.expired.v1` | implemented (not in the PRD event table) | Written in the expiry run's transaction, one per quotation, with the number, legal entity, customer, last valid day, previous status, probability, net total and currency, the actor and the new version. Confirm the name and payload with the events owner. |
| `dev.passage.sales.parameters.changed.v1` | implemented (not in the PRD event table) | Written in the transaction of the parameters create or replace with the legal entity, the five values, the optional one-time order limit and its currency (slice 15), the actor and the new version. Confirm the name and payload with the events owner. |
| `dev.passage.sales.orderchange.requested.v1` / `.approved.v1` / `.rejected.v1`, `dev.passage.sales.changepolicy.changed.v1` | implemented (not in the PRD event table) | Written in the transaction of the PATCH that stores the request, of the approval (with `order.changed` for the applied change), of the rejection, and of the policy create or replace. Confirm the names and payloads with the events owner. |

Note: the PRD names the hold events `passage.sales.order-hold.*`. The
architecture convention (`dev.passage.<context>.<entity>.<verb>.v<n>`,
lowercase) is followed here. Confirm the name with the events owner before
consumers depend on it.

## Acceptance criteria (PRD §5.27.8 and ticket)

| Criterion | Status | Reason |
|---|---|---|
| AC-27.1 BR-27.1 fixture: 45 on 03-01 → 03-04, bucket 1 referenced | partly met | The fixture promise (domain test) is met. The promised event does not yet reference an `atp_bucket` id (no projection). |
| AC-27.2 `atp-100` fixtures: 100 scenarios match expected dates; never over-promises (property) | met for the domain and the stored promise | `testdata/atp-100.json` holds 100 cases, each with a receipt after on-hand stock (the first nine are the BR-27.1, AC-27.1 and AC-27.8 worked examples and bucket-boundary edge cases; 20 are over-committed projections), generated with promise date, schedule and backorder by the independent reference `testdata/gen_atp100.py` (`python3 -I`, hand-derived literals for the nine worked cases, brute-force re-check of every case). Outcomes: 41 promised on the requested date, 26 later on incoming supply, 33 backordered in full, 21 partly backordered. `TestATPScenarios_100` checks every one in the domain, `TestPostgres_ATP100PromisedAndStored` through `PromiseLine` and the stored schedule lines, `TestATPScenarios_FixtureMatchesReference` that the file is the reference's output; `TestPromise_NeverOverPromisesOnHandOrReceipts` (rapid, over-committed projections, fractional quantities) checks no promise exceeds on-hand plus receipts by any date, none precedes the request, and none under-promises. The "all orders confirm" variant needs the projection. |
| AC-27.3 credit 10,000 / 8,500 / 2,000 WARN, 3,000 CREDIT hold, release confirms and emits `order-hold.released` | met | The WARN/FAIL outcomes are domain tests (and `testdata/credit-40.json`). The store test places the hold, refuses a release by the user who applied it, releases as a credit manager, sees the order CONFIRMED, and asserts the outbox holds `creditcheck.evaluated`, `orderhold.applied`, `orderhold.released` and `order.confirmed` (the last with the confirmed ship date). |
| AC-27.4 configured make-to-order with production linkage | met for the linkage; BOM and route are w9 | `TestPlanProduction_CreatesOneProductionOrderPerUnlinkedConfiguredLine` and `TestPostgres_ConfiguredLineIsStoredConfirmedAndLinkedToProductionOnce` (sales), `TestSalesLineOrder_IsIdempotentPerLineAndRefusesOtherLegalEntities` (production): the production order carries the line id and configuration, a second request for the line returns the first order, another configuration or legal entity is refused. See FR-27.11. |
| AC-27.5 intake 500 orders/min for 10 min, p99 < 500 ms, 5,000 exactly once | met in process against local Postgres; not run on a deployed stack | `TestPostgres_OrderIntake500PerMinuteP99Under500ms` offers 500 new orders a minute open loop through the service router, the application role (RLS) and a 20-connection pool, with a 1-50 line mix, listing checks, MANDATORY supplementary lines and 5% re-deliveries (same key, or a new key with the same channel and external reference): every new order stored once, every re-delivery returns the original, p99 23 ms over one minute; `PASSAGE_SALES_INTAKE_MINUTES=10` (the 5,000-order soak, run locally, not in CI) stored 5,000 orders exactly once with 241 re-deliveries, p99 28 ms, max 125 ms. Not measured: the deployed gateway, network and pod count (k6 against stage). |
| AC-27.6 quotation convert (lines 1 and 3) | met | `TestPostgres_QuotationConvertsSelectedLines`: a sent 3-line quotation converts lines 1 and 3 into one 2-line DRAFT order with the quoted products, quantities, prices and nets, the quotation is CONVERTED, the converted lines name the order, and `quotation.converted` lists lines 1 and 3. Explain refs are not carried (deliverable 1). |
| AC-27.7 change line 10 from 5 to 8 (version, diff, only line 10 re-promised) | met | `TestPostgres_ChangeConfirmedOrder`: the version increments, the diff is stored and listed in the history, only the changed line's confirmed date moves, and `order.changed` names only that line. The re-promise uses delivery date control NONE until the projection exists. |
| AC-27.8 partial delivery 40/45 → two schedule lines, backorder 5 on 03-04 | met for supplied buckets | `TestPostgres_ScheduleLinesAndBackorders`: promising 45 against ATP 40 on 03-01 and 5 on 03-04 stores two schedule lines and the backorder list shows 5 units on 2027-03-04 (3 days late); the buckets come in the request until the ATP projection exists. |
| AC-27.9 agreement exhausted → 422 `AGREEMENT_EXHAUSTED` | met | `TestPostgres_AgreementReleases`: a 1,000-unit agreement released 600 + 400 is EXHAUSTED and the next release is 422 `SLS-0008` with no order left behind; `TestPostgres_AgreementValidityAndConcurrency`: eight concurrent releases of 30 against 100 apply three and refuse five. |
| AC-27.10 supplementary items | met | `TestPostgres_MandatorySupplementaryLines`: with the rule "1 × MANUAL-EN free per 10 × FG-100", a 25-unit FG-100 line gives a zero-priced line of 2 MANUAL-EN whose `supplementary_of` is the parent line, reported as `SLS-0012`; an OPTIONAL rule and another customer's rule add nothing; lowering the parent to 9 cancels it in the same version; cancelling the order cancels the supplementary lines with their parent. `TestPostgres_SupplementaryOnConversionAndRelease` covers orders from a quotation conversion and an agreement release. |
| AC-27.11 listing denial `PRODUCT_NOT_LISTED` | met | `TestPostgres_ProductListingDenial`: C9 with a DENY for FG-120 is refused with `SLS-0011`; a reason without `sales.order.approve`, or the duty without a reason, is still refused; the duty and a reason accept the line and the override is stored on the order. |
| AC-27.12 backorder run REDISTRIBUTE | met for supplied supply | `TestPostgres_BackorderRuns`: a REDISTRIBUTE run over three 20-unit lines with 40 supply serves the priority-1 customer first, deconfirms the lowest-ranked line only, commits in one transaction and writes `atp.promised` only for the changed line; a later WIN run promises it on the supply date; no promise exceeds the supply. `TestPostgres_BackorderRunCommitSeesOtherPromises`: two previews giving the same 20 units to different lines cannot both commit, nor can a run after another order of the product was confirmed with a date; `TestPostgres_BackorderRunScope`: runs are invisible to another legal entity or tenant. The supply comes in the request until the ATP projection exists (deliverable 7). |
| AC-27.13 cumulative reconciliation `CUM_QTY_MISMATCH` | met | `TestPostgres_ScheduleReleases`: with 1,200 shipped, a release stating 1,150 is stored HELD with `CUM_QTY_MISMATCH` / `SLS-0014` and changes no schedule line; a release stating 1,200 with 1,500 required by 2027-04-15 opens one line of 300; the next release replaces the open lines in the same transaction. The stateless check keeps both fixture outcomes (API tests). |
| AC-27.14 one-time order and invoice address | met for the order side; invoice and open item are handoff (w29/w30) | `TestPostgres_OneTimeOrderCarriesPartyAndLimit`: a one-time order for "Walk-in Buyer GmbH" keeps the name, address and tax id, reads them back and marks `order.confirmed` as one-time; an order over the default 5,000 USD limit is 422 `SLS-0016` and leaves no order; `TestPostgres_OneTimePartyIsSealed`: none of the buyer's values is in clear in the order row, the request hash or the outbox; `TestPostgres_OneTimeReplayAndKeys`: a replay after a key rotation returns the order, a keyless service answers 503 `SLS-0054` for one-time orders only; `TestPostgres_OneTimeLimitFollowsParametersAndChanges`: a saved limit replaces the default, a change over it, a CREDIT release and a confirm after the limit was lowered do not confirm. The invoice and open item carrying the address is w29/w30. |
| PRD §5.27.9 "credit-40" fixture (40/40 outcomes incl. tolerance edges) | met | `testdata/credit-40.json`, checked by `TestEvaluateCredit_Credit40Fixtures`. |
| PRD §5.27.9 Playwright E2E + axe a11y; Pact contract tests with pricing/inventory/production | later slice of PSG-27 | The web pages have vitest tests that also run the in-repo WCAG checks (`checkAxeAA`); there is no Playwright in apps/web. Pact contracts need the w26/w16/w21 providers. |
| PRD §5.27.9 reconciliation: Σ open order amounts = `exposure.open_orders`; reservations = Σ RESERVED lines; ATP rebuild diff = 0 | later slice of PSG-27 | Needs the exposure recompute, reservations and projection. |
| Ticket AC6: `go vet ./... && go test ./...`; web vitest + tsc; OpenAPI/AsyncAPI match code; Helm lints | met | `TestOpenAPIMatchesRoutes` and `TestAsyncAPIMatchesEventTypes` fail if a route or event type is added without its contract entry, or the reverse. |

## Operations and deployment (PRD §5.27.10)

| Item | Status | Reason |
|---|---|---|
| HPA on RPS (`passage-sales`, 3 replicas) | later slice of PSG-27 | The chart has a fixed replica count. An HPA template needs a request-rate metric from the kernel metrics. |
| `sales-db` (CloudNativePG cluster) and secret provisioning | handoff (deployment-cicd / deploy/) | Outside the owned paths. The chart reads `DATABASE_URL` from secret `passage-sales-db`. |
| Temporal namespace `sales` | handoff (deployment-cicd, w4) | Not needed until the release workflow exists. |
| Config flags (`sales.ctp`, `sales.alternative_items`, `sales.change_approval`) and `ATP_*`, `CREDIT_CHECK_POINTS` | later slice of PSG-27 | Not read by the service yet. Change approval is always available; a customer without a saved policy needs no approval, so it is off until someone switches a customer on. |
| Quotation expiry hourly: scheduler calling `POST /v1/sales/quotations:expire` | handoff (deployment-cicd: a CronJob with a service identity holding `sales.quotation.maintain`) | The endpoint is built and safe to repeat; nothing in the chart calls it yet. The PRD partitions the job by legal entity: the scheduler either calls it once with an all-scope identity or once per legal entity with `legalEntityId`, and calls again while `more` is true. The monitor "none expired for 2 days while candidates exist" can read `quotationsLapsed` from `GET /summary`; no alert rule is built. |
| Duty for the quotation expiry run | done (seeded in `policies/authz/data.json`) | `sales.quotation.maintain` is seeded with the quotation expiry run. |
| Jobs: ATP rebuild 01:00, credit review daily, order auto-close, intake DLQ replay | later slice of PSG-27 | No jobs built. |
| SLO alerts (lag > 120 s, hold queue age, intake 5xx) and runbooks | handoff (deployment-cicd) | Alert rules live in deploy/ (outside owned paths). |

## Migration (PRD §5.27.11)

| Item | Status | Reason |
|---|---|---|
| AX / SAP extraction into sales_order, quotation, agreement, credit_profile | later slice of PSG-27 / handoff (migration-framework) | Migration packs belong to the migration framework; the target tables exist for the order, quotation, credit profile, sales agreement and product listing. |

## Web (`apps/web/src/modules/sales`)

| Item | Status | Reason |
|---|---|---|
| Sales orders list, order detail (confirm, release, credit rules), new order form, credit profiles list and form, ATP check | implemented | Routes under `/sales/...` registered in `module.ts`; pages use the shell design system with loading, empty and error states, and vitest tests in `pages.test.tsx`. |
| Allocation quotas list (filter by product, customer and date, share used), new quota (with the overlap error), quota detail (facts, change quantity with the below-consumed error, draw history including returned draws), and the `SLS-0013` warning on the order detail after a confirm | implemented | Routes `/sales/allocation-quotas`, `/sales/allocation-quotas/new` and `/sales/allocation-quotas/:id` in `module.ts`; `AllocationQuotasPage.tsx`, `AllocationQuotaDetailPage.tsx`; vitest tests in `allocations.test.tsx`. Product and customer are entered as UUIDs until the w7/w8 pickers exist. |
| Sales workspace (`/sales/workspace`): tiles for orders on hold, backorder lines, quotes expiring in 7 days and lapsed-not-expired, the open pipeline per currency, conversion rate, and the expire-lapsed-quotations action | implemented | Reads `GET /summary`; loading, empty and error states and vitest tests in `workspace.test.tsx`. Not built: the PRD's intake errors tile (§5.27.6; the service keeps no intake error log yet, and the Intake errors inquiry of §5.27.14 is not built), and the tiles drill into the existing lists without carrying the tile's filter. |
| Supplementary lines on orders: the order detail marks each supplementary line with its parent line and, after creation, lists the items added (`SLS-0012`); the change screen locks a supplementary line's quantity and date, refuses edits to them and explains that they follow the parent | implemented | `OrderDetailPage.tsx`, `NewOrderPage.tsx`, `ChangeOrderPage.tsx`; vitest tests in `supplementary-lines.test.tsx`. The quotation conversion and agreement release screens open the created order without the added-items notice; the order detail still marks the lines. |
| Quotations list, new quotation, quotation detail (send, convert selected lines with an optional listing override, mark lost); credit release queue with release; supplementary item rules list, new rule, remove, and the order-line check | implemented | Routes `/sales/quotations`, `/sales/quotations/new`, `/sales/quotations/:id`, `/sales/credit-holds` (now on `GET /holds`), `/sales/supplementary-items` and `/sales/supplementary-items/new` in `module.ts`; vitest tests in `quotations-supplementary.test.tsx`. |
| Quotations pipeline KPI (count, weighted value, conversion rate; PRD §5.27.14) | implemented | On the workspace page, with the PRD filters status, expiry window and minimum probability on the pipeline table. Not built: a pipeline by customer, sales rep or month, and the conversion rate by value (it is by count). |
| Order entry grid with inline price and ATP (300 ms) | later slice of PSG-27 | Needs the pricing API (w26) and the projection. The new order form takes ids and prices as entered; customer, product and site pickers need the w7/w8/w6 lookups. |
| Credit exposure by customer report (PRD §5.27.14) | later slice of PSG-27 | The credit profiles list shows each profile's entered open AR; the computed exposure needs the projection. |
| Order change (header date and line edits with the diff shown before saving), cancel with reason, change history on the order detail, scheduling-release check, backorder run preview | implemented | Routes `/sales/orders/:id/change`, `/sales/schedule-releases` and `/sales/backorder-runs` in `module.ts`; vitest tests in `changes.test.tsx`. |
| Backorder list | implemented | Route `/sales/backorders` (filters by legal entity, product, customer and promised date); the promise form is `/sales/orders/:id/promise` and the order detail lists the schedule lines of a confirmed order. Vitest tests in `schedule.test.tsx`. |
| Scheduling releases of an agreement (list with status, cumulative received, shipped, difference and open quantity; cumulative shipped form with the reset date; new release with requirement rows; release detail with open lines, requirements as received and the review of a held release) and the cumulative tolerance on the new-agreement form | implemented | Routes `/sales/agreements/:id/schedule-releases`, `.../new` and `.../:releaseId`, linked from the agreement detail; vitest tests in `schedule-releases.test.tsx`. |
| Backorder runs list (filters by legal entity, product and status), new run (strategy, supply, supply date, lose threshold, customer priority classes) and run detail (lines in processing order with promised quantity before and after, the SLS-0015 warning, commit, and the stale-order refusal with a link to plan again) | implemented | Routes `/sales/backorder-processing`, `/sales/backorder-processing/new` and `/sales/backorder-processing/:id` in `module.ts`; vitest tests in `backorder-runs.test.tsx`. The nav item is gated on `sales.order.read`; the service enforces `sales.backorder.run` on plan and commit, and the pages do not hide their actions by duty. |
| Change requests queue (`/sales/change-requests`, filter by legal entity and status), change request detail (facts, requested diff, approve and apply, reject with a comment, the service's refusals), customer change approval (`/sales/change-policy`), and the change screen's "sent for approval" outcome | implemented | `ChangeRequestsPage.tsx`, `ChangeRequestDetailPage.tsx`, `ChangePolicyPage.tsx`, `ChangeOrderPage.tsx`; vitest tests in `change-requests.test.tsx` and `changes.test.tsx`. The decision and save actions are shown only to a holder of `sales.order.approve`. Not built: a pending-change notice on the order detail (the queue and `GET /change-requests?orderId=` list them), and customer and legal entity pickers (UUIDs are entered until the w7 lookups exist). |
| Apply hold (order detail), listing overrides on the order, customer group and override reason on the new order form; product listings list, new entry and remove; sales agreements list, new agreement, agreement detail with release-order and release history | implemented | Routes `/sales/product-listings`, `/sales/product-listings/new`, `/sales/agreements`, `/sales/agreements/new` and `/sales/agreements/:id` in `module.ts`; vitest tests in `listings-agreements.test.tsx`. |
| Agreement lifecycle on the agreement detail: revise form (commitment, price, tolerance, validity, reason; only changed fields are sent), close form with a required reason, the closed banner, the history table with the newest entry's diff, and the Returned column of the release history; CLOSED in the list's status filter | implemented | Revise and close show only to holders of `sales.agreement.maintain` (an existing seeded duty) and never on a closed agreement; the service enforces both. |
| Customer portal order entry and status | handoff (wave 30) | Portal UI is wave 30. |
| Product proposal rules list (with accepted and declined counts and the acceptance rate), new rule, remove, and the proposal check; proposals on the new-order form (after the lines are entered, "Show proposals" lists the products to offer; "Add line" adds a line for the product and records the acceptance, "Not now" records the decline) | implemented | Routes `/sales/product-proposals` and `/sales/product-proposals/new` in `module.ts`; `ProposalRulesPage.tsx`, `OrderProposals.tsx`; vitest tests in `proposals.test.tsx`. The proposal is shown when asked for, not as each line is typed, until the order-entry grid exists. |
| Order page "One-time customer" section, new-order form checkbox with the buyer's name, address, country and tax id (validated like the service, `SLS-0016` shown with the limit), and the one-time order limit and currency on the sales parameters page | implemented | `OneTimeFields.tsx`, `NewOrderPage.tsx`, `OrderDetailPage.tsx`, `ParametersPage.tsx`; vitest tests in `one-time.test.tsx`. The customer field is still a UUID entered by hand; a one-time account picker needs the w7 lookup. |
| Alternative items list (filter by legal entity and ordered product, priority, validity, remove with confirmation), new rule (`SLS-0055` explained), and the check card (ordered and alternative ATP buckets as lines of text; the covering offer first, partial offers, unchecked alternatives, and the cases where none is needed or none can ship) | implemented | Routes `/sales/alternative-items` and `/sales/alternative-items/new`, nav item on `sales.order.read`; vitest tests in `alternative-items.test.tsx`. Swapping a line to an offered alternative is not built; the order change screen edits the line. |
