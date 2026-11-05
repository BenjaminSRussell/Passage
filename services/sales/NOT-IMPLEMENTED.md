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

Slice 20 adds the intake error log and the JOURNAL order rules. A channel
order (any channel but `UI`) that `POST /orders` refuses, by validation (400
`COM-0001`), a listing denial, a differing payload (409 `SLS-0007`), a
configuration problem or any other 4xx, is written to `intake_error`
(migration 0024) in its own transaction, because the request's transaction
rolls back: one row per legal entity, channel, external reference, payload
hash and error code, with `attempts`, first and last seen, the message and
the status OPEN or RESOLVED; the payload itself is kept only from slice 30 (see there), and its hash always.
A retry of the same payload raises `attempts` and reopens a resolved row.
Server faults (5xx), requests whose legal entity is missing or outside the
caller's scope, and the 409 `SLS-0007` of a concurrent request that stored the
same order first (a retry returns the order, so nothing was refused) are not
logged; a replay of the same payload is answered 200 and never reaches the
log. The write runs under the caller's tenant and legal-entity scope
(row-level security refuses a row for another entity) and a failure to write
it is only logged, so the refusal answered is unchanged. A reference refused
for its length is kept to its first 100 characters. Each refused request adds
at most one row; the log has no per-sender cap, as a cap would hide real
refusals, and a sender's volume is bounded by the intake rate limit
(gateway) like its orders. Retention and purge of resolved rows are not built
(w48 retention). `GET /intake-errors` (filters legal entity,
channel, status, error code), `GET /intake-errors/{id}` and
`POST /intake-errors/{id}:resolve` (`If-Match`, a note of 1 to 500
characters; `SLS-0116` when already resolved) use the existing duties
`sales.order.read` and `sales.order.write`; `intakeerror.recorded.v1` (per
attempt) and `intakeerror.resolved.v1` go through the outbox in the same
transaction. The web gets the list and the detail with the close form. BR-27.7
for JOURNAL orders is completed: a JOURNAL order takes no credit check at
confirm or on a total-raising change (it already took none at entry and
counts for no exposure), and draws no allocation quota at confirm or on a
change; it still could not be promised. `order.confirmed.v1` now carries
`order_type`, so the w16 reservation and w22 planning consumers can skip a
JOURNAL order's lines (BR-27.7: never reserve or consume ATP); the order type
cannot be changed after creation, so a JOURNAL order cannot be confirmed
unchecked and then turned into an ORDER. The workspace gets the intake errors
tile (`openIntakeErrors` on `GET /summary`, OPEN rows of the legal entity).

Slice 21 adds delivery date control (FR-27.3, BR-27.2): the control default
and sales lead time per legal entity, an optional control per order line, the
confirmed ship date rule at confirm and on a PATCH re-promise, customer
delivery terms (transport days and receiving weekdays) with a derived
confirmed receipt date, `deliveryterms.created` / `.changed` events, and the
web pages for them. See the deliverable 2 row below for what is not built.

Slice 22 closes the order-type gap with fulfillment and adds delivery and
invoice progress (BR-27.5, FR-28.8). `order.confirmed.v1` lines now carry
`atp_qty`, the quantity whose schedule lines are CONFIRMED (promised by the
requested date), so the caller of `POST /v1/fulfillment/releases:run` and
`POST /v1/fulfillment/drop-ships` can send `atpQty` next to `orderType` (the
event's `order_type`); sales itself calls neither endpoint (see the handoff row
below). `POST /orders/{id}:record-progress` (`Idempotency-Key`; new duties
`sales.order.record-delivery` for a `DELIVERED` report and
`sales.order.record-invoice` for an `INVOICED` one, so fulfillment cannot post
invoices nor billing deliveries) takes the report of a posted packing slip or
invoice with the quantities that report covers, never cumulative ones (always
positive: returns and credit notes are not recorded here), under the order's
row lock: `sales_order_line.delivered_qty` and
`invoiced_qty` grow (delivery up to the ordered quantity, invoice up to the
delivered quantity, `SLS-0059`), a line moves to PICKED while delivered in part
(no longer changeable or cancellable), DELIVERED when the whole quantity is
delivered and INVOICED when it is all invoiced, and the order status is derived
from its lines (PARTIALLY_SHIPPED, SHIPPED, INVOICED; cancelled lines are left
out). A CONFIRMED or PARTIALLY_SHIPPED order takes both reports, a SHIPPED order
an invoice; anything else, every JOURNAL and RETURNED order and an invoice on a
SUBSCRIPTION order are `SLS-0058`. The same `sourceRef` of a kind is applied
once per order: a repeat with the same lines and quantities (in any order, `4`
and `4.0` alike) is answered from the stored report (200), with other lines it
is 409 `SLS-0060`.
Each report writes an order version with the field-level diff, a stored
`order_progress` row (migration 0026, tenant and legal-entity policies,
granted SELECT, INSERT; the two `sales_order_line` columns are catalog-only
with a `lock_timeout`, their check added NOT VALID and validated by 0027) and
`orderprogress.recorded.v1`, all in one transaction. `GET /order-progress`
lists the reports (filters order, kind, legal entity). A partly delivered order
stays in play: its OPEN and RESERVED lines can still be promised and taken by
a backorder run, and `GET /backorders` and the summary count include
PARTIALLY_SHIPPED orders and leave out delivered and invoiced lines. A
partly shipped order still cannot be changed by PATCH or cancelled (BR-27.5
allows cancel only up to CONFIRMED; `:cancel-remainder` is not built, see the
deliverable 4 change management row). The web gets the
"Deliveries and invoices" list, a record form per order (limits each line to what is
still open) and a card with the reports plus delivered and invoiced columns on
the order page. The OpenAPI and AsyncAPI documents pass the doc gates with no FAIL
lines.

Slice 23 adds `POST /orders/{id}:cancel-remainder` (BR-27.5), the close of a
partly shipped order. Only a PARTIALLY_SHIPPED order takes it (`SLS-0025`
otherwise; `If-Match`, duty `sales.order.write`, which the PRD duty catalog
gives `:cancel-remainder` under SalesOrderMaintain; optional reason up to 500
characters). Every line with nothing delivered, still OPEN or
RESERVED, becomes CANCELLED; a line delivered in part keeps what was delivered
(its quantity and net become the delivered quantity and the net at the same
price and discount, status DELIVERED or INVOICED when all of it was invoiced);
a line delivered in full is left. The order's net total is recomputed, so its
open exposure (BR-27.3) falls to what was delivered and the "credit exposure of a
partly delivered order" row below is answered for the case a remainder is
closed. The order ends SHIPPED, or INVOICED when everything delivered was
invoiced, and can still take invoice reports. In the same transaction: the
allocation quota drawn by a cancelled line is returned and a cut line's draw is
lowered to what it keeps (`allocation.returned` for each), the agreement quantity
of the release that opened the order goes back for the closed quantity of its
non-supplementary lines (at most the release quantity, once, `agreement.returned`
with `returned_qty`), the line's open schedule lines are CANCELLED, a version
with the field-level diff (quantity, line status, status, net total) is written
and `order.remaindercancelled.v1` is queued with the legal entity and the
cancelled and kept quantity of each closed line. Holds are not touched (unlike
`:cancel`): the order lives on to be invoiced, a hold is released only through
`holds/{h}:release` by its own duty and never by the user who applied it, and
fulfillment keeps its copy of each hold from `orderhold.applied` / `released`
and refuses a packing slip while one is active (FUL-0001), which also stops a
slip for the closed remainder that was in flight. A delivery reported after the
close is refused with `SLS-0058` ("nothing is left to deliver ... its remainder
was cancelled"); invoice reports for the delivered quantity are still taken.
There is no `Idempotency-Key` replay (as for `:confirm` and `:cancel`): the
If-Match version makes a retry safe, so a retried close gets `412 SLS-0006` and
never closes twice; the client reloads and sees the order SHIPPED. No migration and no new table:
`allocation_consumption` and `sales_order_line` are already updated by the
store. The web order page gains a "Cancel the remainder" card for a partly
shipped order, shown to a user with `sales.order.write`, with a required
confirmation box, a note that active holds stay, and the service's refusals. The OpenAPI and AsyncAPI documents
pass the doc gates with no FAIL lines.

Slice 24 closes two candidates left by slices 22 and 23. Credit exposure
(BR-27.3) of a PARTIALLY_SHIPPED order is now only its undelivered part: for
each line not cancelled and delivered in less than its quantity, the line net
less the net of the delivered quantity at the same price and discount (a line
delivered in full counts nothing), summed with the CONFIRMED orders' net totals,
on every credit check, profile read and profile list (the credit check locks the
profile row as before; no migration, `sales_order_open_exposure` still serves
the CONFIRMED sum). The delivered part is delivered-not-invoiced
(`deliveredNotInvoiced`, supplied with the profile at slice 24 and computed by sales since slice 25)
and then open AR once billing posts the invoice (w29/w30); it is counted only
for the BALANCE_PACKING_SLIP and BALANCE_ALL limit types, from those inputs. And `POST /orders/{id}/lines/{lineNo}:cancel-remainder` closes one line of
a PARTIALLY_SHIPPED order by the same rules as the whole-order close (a line
with nothing delivered is CANCELLED, a line delivered in part is closed at the
delivered quantity, quota, agreement quantity and open schedule lines follow,
one version with the diff, `order.remaindercancelled.v1` with that one line);
the other lines are not touched and the order status follows its lines (it ends
SHIPPED or INVOICED once every line is delivered or cancelled). A line already
delivered in full or cancelled is `SLS-0025`, an unknown line `SLS-0109`, a
stale `If-Match` `412 SLS-0006`; duty `sales.order.write`. The order page's
"Cancel the remainder" card gains a "Close" choice of every line or one line.
The OpenAPI and AsyncAPI documents pass the doc gates with no FAIL lines.

Slice 26 adds the reject half of the credit release queue (PRD §5.27.6,
"release/reject with comment"): `POST /orders/{id}/holds/{holdId}:reject`
(`If-Match`, duty `sales.credit.release`, required `comment` of 1 to 500
characters). Only an ACTIVE CREDIT hold of an order still awaiting confirmation
(DRAFT or OPEN) can be rejected (`SLS-0062` for another hold type or a CONFIRMED
order, `SLS-0022` for a hold that is not ACTIVE, `412 SLS-0006` for a stale
ETag, 403 for the user who applied the hold, as for a release). In one
transaction under the order's row lock the hold becomes REJECTED (migration
0029: the status value and `rejected_by`, `rejected_at`, `rejection_comment` on
`order_hold`; no new table, so no new grant), the order is cancelled exactly as
`:cancel` does (lines cancelled, allocation quota and agreement quantity
returned, open schedule lines cancelled, the order's other ACTIVE holds
CLEARED, a version whose reason is "Credit hold rejected: <comment>"), and
`orderhold.rejected.v1` (with the comment) and `order.cancelled.v1` are queued.
No change approval applies (the order is unconfirmed). There is no
`Idempotency-Key` replay: the If-Match version makes a retry safe, as for
`:cancel`. The queue screen gains a "Reject" button on CREDIT rows that opens a
comment form (required, 500 characters) and shows the service's refusals; the
queue rows carry `orderStatus`, so a CREDIT hold raised on a CONFIRMED order by
a total-raising change offers only "Release". The order detail's hold table
shows the rejecting user and comment in its "Decision" column. The order
version and `order.cancelled.v1` carry "Credit hold rejected: <comment>" cut to
the 500 characters a cancel reason may have; the whole comment is on the hold
(check `order_hold_rejection_check`: 1 to 500 characters, and who, when and why
on every REJECTED hold) and in `orderhold.rejected.v1`. A rejection also counts
in `sales_orders_cancelled_total`. The OpenAPI and AsyncAPI documents pass the doc gates with no
FAIL lines.

Slice 27 finishes the amount-threshold and named-approver half of change
approval (FR-27.9). `customer_change_policy` (migration 0032, no new table, so
no new grant: 0016 already gives SELECT, INSERT, UPDATE) gains
`approval_threshold` + `threshold_currency` and `approver_user_ids` (uuid[], at
most 20); `sales_order` gains `unapproved_change_value` (0032, its check
validated in 0033; 0009 already grants UPDATE). `PUT
/customers/{id}/change-policy` replaces `approvalRequired` and every member the
body names (`approvalThreshold` as a decimal string above 0 and below 10^15
with at most 4 places, together with a three-letter `thresholdCurrency`;
`approverIds` as identity user ids, stored canonical, de-duplicated and
sorted; `null` / `[]` clear them) and keeps a member the body leaves out, so
the plain `{"approvalRequired": true}` body of slice 13 clients does not wipe a
threshold or group set since. With the switch on and a threshold set, a PATCH
of a CONFIRMED order is measured on a copy with the same `ApplyChange` the
PATCH would use (an invalid change is still 422 before anything is stored):
the value it moves is the sum of each line's absolute net movement, so a rise
on one line and an equal cut on another count twice, not zero. It applies at
once only while that value plus the order's `unapproved_change_value` (what
earlier changes applied without approval moved) stays below the threshold; the
applied value is then added to it. A large change split into several small
PATCHes therefore reaches the threshold like the large change would. A
date-only change moves no money and applies at once. Approved changes and the
direct changes of an approver are not counted. A `:cancel` moves the whole
order total on top of that. An order in a different currency than
`thresholdCurrency` always needs approval, because there is no rate (w12) to
convert it; the web form says so. A non-empty `approverIds` limits who may
approve or reject a request for that customer (anyone else gets 403
`SLS-0150`, new code, README catalogue) and who may skip approval with the
`sales.order.approve` duty: a duty holder outside the group who PATCHes or
cancels the customer's confirmed order gets a stored request (202) like a
requester, otherwise the group could be bypassed by a direct change. The
requester cannot approve their own request and can still withdraw it. An empty
group lets anyone holding the duty decide, so no policy leaves requests
without an approver; a group whose members all left is changed through the
same PUT (duty `sales.order.approve`, not limited to the group, so it cannot
lock itself). The group is read at decision time, so a change of the group
applies to requests already waiting. `changepolicy.changed.v1` carries
`approval_threshold`, `threshold_currency` and `approver_ids`. The web
"Customer change approval" page gains the threshold, currency and named
approvers fields with client-side checks (user ids must be UUIDs) and sends
`null` / `[]` to clear them. No new duty and no new route: the existing
`sales.order.approve` / `sales.order.read` rows cover it, so there is nothing
to seed. Not checked: that a named approver exists in identity or holds
`sales.order.approve` (no identity user lookup from sales; a member without the
duty simply cannot decide). The OpenAPI and AsyncAPI documents pass the doc
gates with no FAIL lines.

Slice 28 adds and removes lines by `PATCH /orders/{id}` (FR-27.9), the gap
every earlier change row listed ("neither is adding or removing lines by
PATCH"). No migration, no new table and no new route: the body of the existing
PATCH (duty `sales.order.write`) gains `addLines` (entered like order lines;
`configurationId` is refused with 400), `removeLines` (line numbers) and
`listingOverrideReason`, so there is nothing to seed. An added line is numbered
after the order's highest line number (a removed line keeps its number, so a
number is never reused), is checked against the customer's product listings
like a line at entry (`SLS-0011`; the `sales.order.approve` duty with a reason
accepts it and the override is appended to the order's `listing_overrides`),
gets the MANDATORY supplementary lines of its product (the `SLS-0012` warning;
the follow-the-parent rules of BR-27.14 then keep them in step), and, on a
CONFIRMED order, is promised at once through the delivery date control confirm
uses, allocated against its quota (`SLS-0013` when no period can cover it),
scheduled and counted in the credit re-check and the one-time-order limit
because the total rises. A removed line is set to CANCELLED, not deleted: only
an OPEN or RESERVED line can go, a supplementary line cannot be removed alone
(remove its parent and it is cancelled with it), a line with a production order
planned cannot be removed (cancel the production order first), and a change
that would leave the order with no line is refused (cancel the order instead);
all `SLS-0024`. A removed line returns its quota draw and leaves the schedule
through the same path as a quantity cut. Added and removed lines, header and
line edits go in one version: the diff names an added line with a `lineAdded`
entry (the product) followed by `lineId`, `unit`, `siteId`, `warehouseId`,
`quantity`, `unitPrice`, `discPct`, `requestedShipDate` and, when confirmed,
`confirmedShipDate`, each with an empty old value, so a consumer of
`order.changed.v1` has what it needs to plan the new line, and a removed line
as `lineStatus` OPEN to CANCELLED; `repromised_lines` lists the added lines of
a confirmed order. The approval path of FR-27.9 covers them: the stored
request keeps `addLines` and `removeLines`, the value moved for the threshold
counts an added or removed line's whole net, and approving applies them with
the same code (the approver holds `sales.order.approve` and may decide for
the customer, so the replayed listing check accepts the override the request
carries; a requester without the approve duty cannot store a denied line at
all, since the listing check runs when the request is made). The web change page gains a "Remove line N from the order"
checkbox per line, an "Add a line" editor (the shared line grid, with the
delivery date control) and the listing override reason, with the diff showing
both before saving. Not built: a configured product on an added line
(`configurationId`; production planning for it stays an explicit call),
adding or removing lines of a PARTIALLY_SHIPPED order (PATCH still reaches
DRAFT, OPEN and CONFIRMED orders only), re-checking OPTIONAL supplementary
items of an added line (the evaluation endpoint is the prompt, as at entry),
and the stored request's diff shows a `lineId` for an added line that the
approval replaces with a new one (the diff of `order.changed` and the version
history carry the real id). The OpenAPI and AsyncAPI documents pass the doc
gates with no FAIL lines.

Slice 28 review: three gaps closed. (1) Fulfillment follows
`order.remaindercancelled.v1` and not `order.changed`, so a line removed from
a CONFIRMED order whose shipment was already released would still have been
picked and packed. The removal now also queues `order.remaindercancelled.v1`
(after `order.changed`, same version, `status` CONFIRMED, now in the schema's
enum) for the removed lines and the supplementary lines cancelled with them,
each with `kept_qty` 0; fulfillment cuts what it released, closes their
drop-ships and refuses a later slip (FUL-0066), and a slip posted first is
refused by sales because the line is cancelled. Those lines are cancelled for
good, so fulfillment's kept quantity never has to rise again. A line partly
shipped cannot be removed at all: its first delivery makes it PICKED and the
order PARTIALLY_SHIPPED, which PATCH does not reach (use the per-line
`:cancel-remainder`). (2) Once a line is added, line 1 of an order released
from a sales agreement (the release line) can be removed; its quantity now
goes back to the agreement as a whole-order cancel gives it (at most the
release quantity, once per release, `agreement.returned`). (3) A request made
by a user with the approve duty outside the customer's named group, carrying a
listing override, could be stored but never approved (the replay refused it
with `SLS-0011`); the approval now accepts the override. Line numbers cannot
collide under concurrent PATCHes: every PATCH and approval locks the order row
and checks its version (a second writer gets 412, an approval of a request
made at an older version `SLS-0046`), and `(tenant_id, order_id, line_no)` is
unique. The slice 27 threshold counts an added or removed line's whole net in
the order's cumulative unapproved value (tested).

Slice 29 closes the fulfillment gap found in the slice 28 review: a `:cancel` of a
CONFIRMED order (direct or through an approved CANCEL request) and a PATCH that
lowers a confirmed line quantity now queue `order.remaindercancelled.v1` (kept 0
for a cancel, the new quantity for a cut; `status` CANCELLED in the schema enum,
`line_status` OPEN and RESERVED added), so the consumer fulfillment already has
follows them. The review added the raise side: a PATCH that raises a confirmed
line's quantity, or reopens a supplementary line a cut had cancelled, puts that
line in the same event with `kept_qty` the new quantity and `cancelled_qty` 0.
Fulfillment's consumer keeps the newest version per line, so without this a
cut-then-raise left its stored ceiling at the cut quantity for good (release
capped, the slip post refused with FUL-0066), and a reopened supplementary line
stayed at kept 0 (tested; the consumer reads only `line_id` and `kept_qty`, so
the new status values need no consumer change). Both specs pass the doc gates
with no FAIL lines.

Slice 30 adds the replay of refused channel orders (PRD §5.27.10, "Intake
retry / DLQ replay"). The intake error log kept only the payload hash, so a
refused order could not be sent again once its cause (a listing, a
configuration, a mapping) was fixed. Migration 0034 adds `request_payload`,
`idempotency_key`, `replayed_order_id` and `replay_count` to `intake_error` (no
new table, so no new grant). A refused channel order now keeps its request body
and Idempotency-Key on the OPEN row (the same payload failing again replaces
them); resolving the row, by hand or by a replay, clears both, so the buyer
data of a one-time order is not kept longer than the failure needs. A
one-time customer request carries the buyer's name, address and tax id, which
this service never stores in clear (slice 15), so its body is kept only as a
fieldcrypt envelope in `request_payload_sealed` (field
`intake_error.request_payload`) and not at all when the service runs without
`FIELDCRYPT_KEYS` (the row is then not replayable, 409 `SLS-0118`; a sealed row
read without keys is 503 `SLS-0054`). The body and key are never returned by
the API (`replayable` says whether they are there), never put in an event and
never logged; the row keeps intake_error's tenant and legal-entity RLS.
`POST /intake-errors/{id}:replay` (no body, no `If-Match`) sends the kept
request through the same intake code as `POST /orders` (`submitOrder`, shared)
as the caller and under the kept key (a key derived from the row id when the
request had none), so a replay whose order was already stored returns that
order and does not add another. Stored or found: the row is RESOLVED with the
note "Replayed as order SO-n", `replayed_order_id` set and
`intakeerror.resolved.v1` written in the same transaction. The replay runs with
the replaying user's own legal-entity scope and duties, not the original
sender's: the row is invisible outside the user's scope (RLS, 404), the order's
legal entity is checked again (403), and `sales.order.write` is a duty that
may already create orders through `POST /orders`. Two replays of one row (or a
single and a bulk replay at once) store one order: they share the key, so the
second finds the order (and the row is closed once) or gets the 409 `SLS-0007`
of the insert in progress, which is answered as is and not counted (nor are a
fault or a 401/403). Refused again (any other
4xx, including a body that no longer validates): the answer is still 200 with
`replayed` false and the new refusal in `failure`; the row stays OPEN with
`attempts` and `replay_count` raised, its message replaced and
`intakeerror.recorded.v1` written (the row keeps its first error code, so a
different refusal does not open a second row). A resolved row is 409
`SLS-0116`; a row logged before this slice (no kept body) is 409 `SLS-0118`.
`POST /intake-errors:replay` replays up to `limit` (default 50, at most 100)
OPEN rows of the legal entity, oldest first, optionally by channel and error
code; rows at 10 attempts or more are the dead letters of the PRD: counted in
`skipped`, left alone, and replayable one by one by hand. It is safe to repeat,
which is what the scheduled retry does. The web gets a "Replay order" action on
the intake error detail (order link once replayed, the refusal when it fails
again), a "Replay open errors" action on the list with the counts, and
`replayable` / `replayCount` / `replayedOrderId` on the error. Both specs pass
the doc gates with no FAIL lines.

Slice 31 keys the request hash of refused one-time orders and adds the order
auto-close. (1) `intake_error.request_hash` of a request that carries a
one-time buyer was a plain SHA-256, so a guessed buyer name and tax id could be
confirmed against the log (and against `intakeerror.recorded.v1` and the API's
`requestHash`). It is now an HMAC under the field encryption key,
`hmac:<key id>:<hex>`, in a hash domain of its own
(`sales.intake.error`), as `sales_order` does since slice 15; an ordinary
request keeps its plain hash, since it names no buyer. The dedupe of the log
survives key rotation: before the upsert the store recomputes the stored keyed
hashes of the same legal entity, channel, reference and error code under the
key each names and reuses the match, so a retry raises `attempts` instead of
opening a second row. A service without `FIELDCRYPT_KEYS` stores the constant
`unkeyed` for such a request (any digest would leak), so those refusals dedupe
on (legal entity, channel, reference, error code) alone. Migration 0035
cannot compute an HMAC in SQL, so it replaces the plain hash of every row that
may cover a buyer (sealed body, no kept body, or a clear body with a `oneTime`
member) with `cleared:<row id>`; rows whose clear body has no `oneTime`
member keep theirs. The cost is that a retry of a cleared row's payload opens a
new row beside the old one, which an operator can resolve. No new table, so no
new grant. (2) `POST /orders:auto-close` (PRD §5.27.16, "Order auto-close"; BR-27.5
INVOICED to CLOSED) moves INVOICED orders that nothing has changed for
`graceDays` (0 to 365, default 30) and that carry no ACTIVE hold to CLOSED,
oldest first, up to `limit` (1 to 500, default 100), for the query
`legalEntityId` or the caller's (an all-scope caller covers every legal
entity). `dryRun=true` lists the candidates and closes nothing. Each order
closes in its own transaction under its row lock and is re-checked against the
version, status, age and holds the listing saw (an order that changed meanwhile
is SKIPPED), so the job is safe to repeat and two runs close an order once. A
close writes an order version (diff `status` INVOICED to CLOSED, reason with
the grace days) and `order.closed.v1` through the outbox in the same
transaction. A CLOSED order takes no progress report (`SLS-0058`), change or
cancel. Duty: the existing `sales.order.write`. The web gets "Close invoiced
orders" (preview, close, result table, empty and error states). Both specs pass
the doc gates with no FAIL line.

The slice 31 review fixed: (a) migration 0035 updated no row under the
deployed migration role (the database owner is NOSUPERUSER NOBYPASSRLS,
`intake_error` forces RLS and a migration sets no tenant), so every plain
digest would have stayed; it now lifts FORCE for the one UPDATE and forces it
again in the same transaction, as fulfillment 0018 and purchasing 0023 do, and
the store test runs the step as a non-bypass owner (`passage_app`) when
`PASSAGE_TEST_APP_DATABASE_URL` is set and checks FORCE is back. (b) The
rotation dedupe recomputed an HMAC per stored row of the reference, which grows
without bound for a channel that sends no reference (`external_ref` ''); it now
computes one HMAC per other key the stored rows name and looks the candidates
up by equality on `intake_error_key`. (c) Migration 0036 adds a partial index
`sales_order_auto_close (tenant_id, updated_at, id) WHERE status = 'INVOICED'
AND order_type = 'ORDER'`; the nightly listing read every order of the tenant
before. (d) The item's `invoicedAt` is renamed `lastChangedAt`: it is
`updated_at`, which a later change (a hold release) also moves. (e) The web
offered Preview to a reader, but the preview is the same POST under
`sales.order.write` and was refused; both actions now need the duty.

Slice 32 completes the quotation lifecycle of BR-27.5 (FR-27.1). (a)
`POST /quotations/{id}:cancel` (`If-Match`, a reason of 1 to 200 characters,
duty `sales.quotation.maintain`) moves a DRAFT or SENT quotation to CANCELLED
(`cancel_reason`, migration 0038, a CHECK that a CANCELLED row has one) and
writes `quotation.cancelled.v1`; a CONVERTED, LOST, EXPIRED or already
CANCELLED quotation is 422 `SLS-0030`, and a partly converted quotation keeps
its orders. (b) `PATCH /quotations/{id}` (`If-Match`, same duty) changes the
validity (today to today + 365 days) and the probability of a DRAFT or SENT
quotation, and replaces the whole line set of a DRAFT one (renumbered from 1,
nets recomputed, one transaction under the row lock); a SENT quotation takes no
new lines (`SLS-0030`: its prices are what the customer holds), a quotation
past its validity cannot be changed (`SLS-0031`), a body that changes nothing
is 400, and every change bumps the version and writes `quotation.changed.v1`
(what changed, new validity, probability, line count, net total). The
application role gets DELETE on `sales_quotation_line` (migration 0038), which
the line replacement needs. (c) The web quotation detail gets a "Change the
quotation" form (validity, probability, and for a DRAFT the line grid; only the
differences are sent; on a version conflict the quotation is re-read, the
user's edits are kept and untouched fields take the stored value, and the next
save carries the new version) and a "Cancel the quotation" form with a required
reason; the detail shows the cancel reason. Both specs pass the doc gates with no FAIL
line.

Wave 30 AC3 adds return orders (`return_order` / `return_order_line`, migration
0037): create in REQUESTED from a billing invoice (customer, currency, net unit
price and the returnable quantity, invoiced less billing credit notes' creditedQty
and other live RMAs per product, come from the invoice; 422 `SLS-0123` above it), `:authorize` by a user other
than the creator (30-day validity), `:receive` into quarantine (lines left out
receive 0), `:inspect` (accepted quantity and disposition per received line; no
lines when nothing was received; RETURN_TO_STOCK refused on a line without site,
warehouse or return cost; CURRENT_COST read here), `:reject` (REQUESTED or
AUTHORIZED only),
`:disposition` (restocks the accepted RETURN_TO_STOCK quantity after the state
commit through inventory's service-only return-receipt command
(duty `inventory.return-receipt.post`; sales no longer holds the general journal
duties), key `rma-receive:<lineId>`, at the ORIGINAL_COST packing
slip cost, the CURRENT_COST on-hand average or ZERO) and `:credit` (credit from
accepted quantities, or the whole expected quantity of a line created
CREDIT_ONLY that nothing came back for; stored as CREDITED, receivables posts the
note, then CLOSED). A failed restock or credit post leaves the stored state and
is retried by repeating the command. Credit computes restocking (BR-30.3: 15% on
RETURN_TO_STOCK, 0% on CREDIT_ONLY, with a 25.00 USD / 20.00 EUR floor applied
once per note to the RETURN_TO_STOCK lines together and never above their value);
a note with nothing to credit closes the RMA with net 0 and no credit note.
Events: `returnorder.{authorized,received,inspected,dispositioned,credited}.v1`.
AC-30.5 is covered by `internal/store/rma_e2e_test.go`. Peers are wired from
`BILLING_URL`, `FULFILLMENT_URL`, `INVENTORY_URL` (with
`SALES_RETURN_OFFSET_ACCOUNT`, the MOVEMENT journal's offset account) and
`RECEIVABLES_URL`; a missing peer answers 503. Not built: SCRAP / REPAIR /
REPLACE / REJECT_RETURN_TO_CUSTOMER, tax lines, a physical quarantine stock
status (receive records the quantity only), per-slip cost when one product
shipped on several packing slips at different costs (the first slip's cost is
used), and a different return price per invoice line of the same product (the
weighted average net unit price is used), and an event for `:reject` (the
REJECTED status is visible on read and list only). Duties stay `sales.order.read` /
`sales.order.write`.

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Quotation → order conversion, quotation lifecycle (`:send`, `:convert`, expiry, probability) | implemented in part | Built: priced quotations with validity (default +30 days, at most 365) and win probability (default 50), `SQ-` numbering per legal entity, list and read, `:send` (DRAFT, still valid), `:lose` with a reason, and `:convert` of all or selected open lines into one DRAFT order (channel `QUOTE`) at the quoted price and discount under the quotation's row lock, with the converted lines naming the order and `quotation.converted` in the same transaction; later conversions take the remaining lines. Slice 12 adds `POST /quotations:expire`, which sets EXPIRED (see slice 12 above). Slice 32 adds `:cancel` (CANCELLED) and `PATCH` (validity, probability and, for a DRAFT, the line set; see slice 32 above). Not built: the scheduler that calls it hourly (handoff, below), pricing quotation lines through the w26 resolver (lines are entered with prices; editing them is by hand), editing the customer, currency or invoice account of a quotation (create a new one), `price_explain_ref` on quotation and order lines (FR-27.1 "preserving explain refs"; the w26 explain reference does not exist on either line yet), the quotation document (PDF, email, UBL JSON; PRD §5.27.15) and the lost/expired notice, the `quotation_expiry_days` and `quotation_probability_default` parameters (constants 30 and 50), a listing check at quotation entry (the check runs at conversion), and the customer group on the converted order (group listing entries apply only to orders that name it). `:send` and `:convert` are versioned by `If-Match` rather than the PRD's `Idempotency-Key`: a retry with the old ETag is refused (412) instead of replayed, as for holds. |
| 1 | Sales agreements, release orders, commitment decrement (FR-27.10, BR-27.9, AC-27.9 `AGREEMENT_EXHAUSTED`) | implemented in part | Built: one-product quantity commitments at an agreed price with validity, `SA-` numbering, list and read with release history, and `:release-order`, which under the agreement's row lock refuses a release beyond the remaining quantity (`SLS-0008`) or outside the validity period (`SLS-0029`), creates a DRAFT order (channel `AGREEMENT`) of one line at the agreement price and stores the release, all in one transaction. Not built: commitments by amount or for several products, the `overdelivery_pct` parameter (0 is applied), re-pricing of already released draft orders when the agreement price changes (BR-27.9; a revised price applies to later releases only), `agreement_line_id` on the order line (the link is `agreement_release.sales_order_id`), returning quantity when a released order's lines are reduced by PATCH (only whole-order cancellation returns), and the agreement confirmation document (PRD §5.27.15). |
| 1 | Order types JOURNAL / SUBSCRIPTION / RETURNED behaviour | JOURNAL implemented (slice 20); RETURNED RMA implemented as wave 30 AC3; later slice of PSG-27 for SUBSCRIPTION | JOURNAL orders take no credit check, no allocation quota and no promise, and count for no exposure (BR-27.7); reservation is w16. The subscription flow is not implemented. RETURNED goods are credited through `/v1/sales/return-orders` (wave 30 AC3), not through a RETURNED sales order type. |
| 2 | Cumulative ATP on stored supply and demand (`atp_bucket` projection, lag < 30 s, planning batch-end marker) | later slice of PSG-27 | Only the pure cumulative/promise computation and a stateless `POST /v1/sales/atp:check` exist. The projection needs supply/demand events from w16/w17/w21/w22. |
| 2 | Delivery date control per line (NONE / SALES_LEAD_TIME / ATP / CTP; transport days; receiving calendar; past-date warning; fence-end TIME_FENCE) (FR-27.3, BR-27.2) | implemented in part (slice 21) | Built: `delivery_date_control_default` and `sales_lead_time_days` per legal entity (parameters), an optional `deliveryDateControl` per order line (NONE or SALES_LEAD_TIME), applied at confirm, at the release that confirms and when a PATCH re-promises a line: the confirmed ship date is the requested date, today when that is past (SLS-0003 warning), and with SALES_LEAD_TIME never earlier than today plus the lead time (SLS-0057 notice); a supplementary line takes its parent line's control. Customer delivery terms (`/delivery-terms`: transport days 0 to 60, receiving ISO weekdays) are copied onto the order at confirm (`confirmedDeliveryTerms`, columns `confirmed_transport_days` / `confirmed_receiving_days`) and give each line its `confirmedReceiptDate` (confirmed ship date plus the copied transport days, rolled to the next copied receiving day), also on `order.confirmed` lines; a later change of the customer's terms does not move a confirmed order's receipt dates, and a re-promised ship date carries its receipt date with it. Web: delivery terms list, create and change pages, the parameters fields, a control select on the new-order line, receipt and control columns on the order. Not built: ATP and CTP controls (refused with 400 until the ATP projection and the w23 CTP API exist; the PRD default ATP is NONE here), TIME_FENCE / SLS-0004 (needs the ATP fence), a calendar from w6/w48 (the receiving weekdays are sales-owned; holidays are not modelled), a lead time per product or customer (one value per legal entity), changing a line's control by PATCH, the SLS-0003 / SLS-0057 notices on a PATCH (the version diff shows the moved date), a receipt date stored per line (it is computed from the stored ship date and the order's copied terms, so it cannot be queried in SQL), and quota period start winning over a later lead-time date when allocation moves a line (the quota date is used). |
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
| 3 | Credit exposure recompute and nightly reconciliation (`exposure.open_orders`) | implemented in part | Built (slice 18): `openOrders` is computed from this service's own CONFIRMED and PARTIALLY_SHIPPED orders at every check and on every profile read, so it needs no recompute job and there is no stored figure to reconcile. Not built: the nightly reconciliation job (nothing to reconcile until the w29/w30 projection stores exposure), conversion of orders in a currency other than the profile's (no FX rate, w12; they are listed and flagged instead), and `deliveredNotInvoiced` from shipped quantities (w28/w29; see the slice 24 decision below). Since slice 24 a PARTIALLY_SHIPPED order counts only its undelivered part. |
| 3 | Credit review warnings daily job (06:00), review_date handling | implemented in part | Built (slice 19): `GET /credit-profiles?reviewDueBy=` and the web filter list the profiles due for review; the CR-4 review-date rule runs at confirm. Not built: the 06:00 job that notifies the credit manager (handoff: deployment-cicd CronJob and w3 notifications; it can call this list). |
| 4 | Holds: FRAUD / MANUAL / PRICE_REVIEW apply endpoint (`POST /orders/{id}/holds`) | implemented | Duty `sales.order.hold`; DRAFT, OPEN or CONFIRMED orders; one ACTIVE hold per type; the hold, version bump and `orderhold.applied` commit together. A FRAUD hold blocks confirmation. The request is versioned by `If-Match` rather than the PRD's `Idempotency-Key`: a retry with the old ETag is refused (412) instead of replayed. |
| 3, 4 | Credit release queue (PRD §5.27.6, §5.27.14: customer, order, amount, exposure vs limit, rule failed, age; release/reject with comment) | implemented in part (release and reject) | Built: `GET /holds` (duty `sales.credit.release` or `sales.order.hold`), ACTIVE holds oldest first with the order, amount, failed rule (for CR-1 the exposure plus order against the limit with tolerance), credit limit, age and SLA due time (`slaDueAt`, the legal entity's `credit_hold_sla_business_days` counted Monday to Friday, slice 11), and the queue screen releasing a hold at the order's ETag and flagging a hold past its due time. Slice 26 adds the reject action with a comment (see the slice 26 note above). Not built: the exposure breakdown by component (the profile screen shows it), and public holidays in the SLA count (the w6 work calendar is not read). |
| 4 | Temporal release workflow with approvers by duty and 2-business-day SLA timer (FR-27.8, AC E2E "SLA timer fires") | later slice of PSG-27; handoff for the Temporal namespace `sales` (deployment-cicd) | Release is a synchronous API call. The SLA due time is computed per legal entity (slice 11) and shown on the queue, but no workflow, no timer that fires and no escalation exist. |
| 4 | Release validity re-trigger on amount increase > 5% (BR-27.4) | implemented | A CREDIT release covers its own order only while it is valid (7 days) and the amount is within the released amount plus 5% (`domain.ReleaseCovers`). At confirm, and when a PATCH raises a CONFIRMED order's total, a failing credit check that no valid release covers places a CREDIT hold. |
| 4 | MANUAL / PRICE_REVIEW blocking warehouse release (BR-27.4) | handoff (w28) | The holds are placed and listed on the order; warehouse release belongs to fulfillment. |
| 4 | Change management: PATCH with If-Match, version per change, field-level diff, selective re-pricing/ATP/credit, `order.changed` and `order.cancelled` events, optional approval per customer (FR-27.9, AC-27.7) | implemented in part | Built: PATCH, `:cancel`, version history and both events. Re-pricing recomputes the changed lines' nets from the entered price (the w26 price resolver is not called; see "Order entry grid" under Web). Re-promise sets a changed line's confirmed date to its requested date, the delivery date control confirm uses until the ATP projection exists. Slice 13 adds the optional approval per customer: see the slice 13 note above and the FR-27.9 change approval row below. Slice 23 adds `POST /orders/{id}:cancel-remainder` for the whole order (see the slice 23 note above); closing the remainder of a single line is built (slice 24); adding and removing lines by PATCH is built (slice 28). PATCH changes DRAFT, OPEN and CONFIRMED orders only, so the undelivered lines of a PARTIALLY_SHIPPED order cannot be changed (they can still be promised and run); whether PATCH should reach them is a decision to confirm. |
| 4 | Line-level status machine beyond confirm (RESERVED / PICKED / DELIVERED / INVOICED) and order status derivation (BR-27.5) | implemented in part (slice 22) | Built: PICKED (delivered in part), DELIVERED and INVOICED lines and the derived PARTIALLY_SHIPPED, SHIPPED and INVOICED order statuses, driven by `:record-progress`. Not built: RESERVED (set when the w16 reservation exists), CLOSED by age (slice 31, the nightly auto-close; the w30 payment signal is still not used), the automatic caller (fulfillment and billing do not call `:record-progress` yet, see the handoff row), reversal of a delivery or invoice (returns are w30) and the over-delivery tolerance (`overdelivery_pct` is 0: a delivery above the ordered quantity is `SLS-0059`). |
| 4 | Order status CANCELLED path | implemented | `:cancel` from DRAFT, OPEN or CONFIRMED when no line is past RESERVED (BR-27.5); ACTIVE holds move to `CLEARED`. Reservations are not released because none exist yet (BR-27.7, w16). |
| 5 | Customer portal order entry and status | handoff | Wave 30 (portal UI). The intake API it will use exists. |
| 5 | Multi-channel intake: `(channel, externalRef)` dedup (BR-27.10) | implemented in part | Same-key replay (200) and different payload (409 SLS-0007) are implemented and tested. The dedup window (`intake_dedup_window_days`, default 30 days, per legal entity) is enforced since slice 19; a repeat after the window opens a new order, and a reference is matched within its legal entity, not across the tenant (the PRD's tenant scope is a decision to confirm, see slice 19). Not built: the window does not apply to the other intake kinds (quotations, agreements and releases dedupe on their Idempotency-Key only). |
| 5 | Intake rate limit per key (`INTAKE_RATE_LIMIT_PER_KEY=1000/min`, 429 with Retry-After) | later slice of PSG-27; handoff for shared rate-limit store (Valkey addendum, open question) | Needs the kernel rate-limit middleware; no per-pod limiter here. |
| 5 | ChannelIntake client credentials (service accounts) | handoff | Keycloak `_internal`/client registration belongs to edge-identity and w35 integration. The duty `sales.channel.intake` is enforced in code. |
| 6 | Supplementary items (FR-27.14, BR-27.14, AC-27.10) | implemented in part; later slice of PSG-27; handoff for w26 free-goods proposals | Built: `supplementary_item_rule` per (ordered product, customer or every customer, item) with MANDATORY/OPTIONAL, quantity per base quantity, FREE or PRICED; create (duty `sales.listing.maintain`), list, remove; the stateless `POST /supplementary-items:evaluate`, which returns the AC-27.10 lines (1 per 10 on 25 gives 2, free) with a customer's own rule replacing the all-customers rule for the same item; MANDATORY lines added in the order-create transaction of every order (create, quotation conversion, agreement release) with `supplementary_of` (the parent line id), the rule id and the rule's base quantity and quantity per base kept on the line, the rule's unit and price (0.00 for FREE), no discount, and an `SLS-0012` warning per added line on the create result; BR-27.14 on PATCH (the supplementary line's own quantity and date are refused with `SLS-0024`, a new parent quantity recomputes it, a parent below the base quantity cancels it, parent dates carry over, and the follow-on changes are in the version diff and `order.changed`); cancellation with the order. A supplementary line cancelled because its parent fell below the base quantity reopens, recomputed, when the open parent reaches the base again (slice 6). Built in slice 28: a parent line added by PATCH gets its MANDATORY supplementary lines. Not built (later slice): prompting for OPTIONAL items at line commit in an order-entry grid (the evaluation endpoint is the prompt; there is no grid, see Web) and the `supplementary_prompt_optional` parameter; a MANDATORY line for a parent that was below the base quantity when the order was created (no line exists to reopen, and an added line cannot name an existing parent); the delivery address (BR-27.14 inheritance), since orders carry no address yet (w7); and a listing check on the supplementary item itself (only the ordered lines are checked; the item goes with its parent). Not built (handoff): product-group and customer-group rule scopes (w8 product groups, w7 customer groups) and the w26 FREE_GOODS proposals. |
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
| FR-27.9 change approval: notification of the manager, approval by amount threshold or by a named approver group | implemented in part (slice 27) | Slice 13 built the per customer switch, the stored request and approve/reject; slice 17 added withdrawal by the requester and cancellations; slice 27 added the amount threshold and the named approver group per customer (see the slice 27 note above). Not built: the notification of the manager (the queue is polled; handoff to w3 notifications), a threshold per legal entity or customer group (it is per customer), a threshold in a currency other than the order's (no w12 rate: such orders always need approval), approver groups by role (the group is named user ids, as the role mapping is edge-identity), a check that named approvers exist in identity, and resetting an order's unapproved change value (it only grows over the order's life; see decisions). |
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
| `dev.passage.sales.orderprogress.recorded.v1` | implemented (not in the PRD event table) | Written in the `:record-progress` transaction with the report (kind, source reference), each covered line (quantity, delivered and invoiced quantity and status after), the order status before and after, the actor and the new order version. Consumers - billing, customer portal, analytics. Confirm the name and payload with the events owner. |
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
| Intake retry / DLQ replay every 5 min, DLQ after 10 attempts (PRD §5.27.10) | implemented in part (slice 30); handoff for the scheduler (deployment-cicd) | Built: the kept request, `POST /intake-errors/{id}:replay`, the bulk `POST /intake-errors:replay` (oldest first, dead letters at 10 attempts counted and skipped) and the web actions, see slice 30. Not built: the 5-minute caller (a CronJob with a service identity holding `sales.order.write`, calling the bulk endpoint per legal entity), which is outside the owned paths. Rows logged before slice 30 have no kept request (`SLS-0118`): resolve them. Retention of resolved rows is still w48. |
| "Today" of the delivery date rule is the UTC day (slice 21) | decision to confirm | The past-date rule (SLS-0003) and SALES_LEAD_TIME count from the UTC calendar day, as every other date rule of this service does (agreement validity, credit release validity, listings). Sales holds no time zone for a legal entity or a shipping site (org has the site and warehouse time zones; BR-28.3 dates shipping in the warehouse time zone), so for an entity far from UTC the rule is one day off for some hours around midnight. Using the shipping warehouse's time zone needs it replicated from org. |
| Transport days are calendar days; release-confirm re-dates (slice 21) | decision to confirm | BR-27.2 says ship date plus transport days rolled to the next receiving day; the days are counted as calendar days (no transport calendar or holidays until w6/w48) and only the receipt is rolled to a receiving weekday (the terms need at least one, so the roll ends within a week). An order that is confirmed again when its hold is released gets new ship dates from the rule and copies the customer's terms in force at that moment. |
| JOURNAL orders take no credit check at confirm or on a change | decision to confirm | BR-27.7 says only that a JOURNAL order never reserves or consumes ATP. As at entry (slice 18) and in the exposure (JOURNAL orders count for none), the credit check is skipped at confirm and on a total-raising change, since in AX a journal-type order posts no packing slip or invoice. This holds only while w28/w29 refuse to ship or invoice a JOURNAL order (they can read `order_type` from `order.confirmed`); if JOURNAL orders may ship, they need the credit check back. |
| Which intake refusals are logged | decision to confirm | Logged: every 4xx refusal of a non-`UI` channel order in the caller's scope (validation `COM-0001`, `idempotency_key_reuse`, `SLS-0007` for a reference reused with a different payload, listing, one-time limit, configuration). Not logged: replays (200), the concurrent-request 409, 5xx, out-of-scope legal entities and malformed JSON (no legal entity to log under). Credit holds are not refusals (the order is stored) and are not logged. |
| Sales sends nothing to fulfillment; `orderType`, `priority` and `atpQty` on the release call (slice 22) | handoff (fulfillment, wave 28; or a release caller outside sales) | Sales has no component that calls `POST /v1/fulfillment/releases:run` or `/drop-ships`, builds release lines or consumes `shipment.released`, so there is no call here to extend (fulfillment's release call is made by its web form, whose caller offers the lines). The data a caller needs is on `dev.passage.sales.order.confirmed.v1`: per order `order_type` (set `orderType` on every line from it, or do not offer a JOURNAL order's lines) and per line `atp_qty` (set `atpQty`); the order priority is not modelled in sales (no column, no PRD field), so `priority` stays at fulfillment's default 5 until an order priority is specified. A caller reading `shipment.released` must treat its line quantities as what was released in part (a later run releases the rest) and report what a posted packing slip delivered to `POST /v1/sales/orders/{id}:record-progress` (`kind` DELIVERED, one `sourceRef` per slip, the slip's quantities only). |
| Billing and fulfillment do not call `:record-progress` yet (slice 22) | handoff (fulfillment w28: packing slip posted; billing w29: invoice posted) | The endpoint, the order statuses and the `orderprogress.recorded` event are built and tested; the callers are not. Contract for both: send each document once per order with its own quantities (deltas), its number as `sourceRef` and a stable `Idempotency-Key` per (document, order), and retry with the same key and body; a repeat of the same document with the same lines is answered 200 from the stored report. Fulfillment signs as its service principal with `sales.order.record-delivery`, billing as its service principal with `sales.order.record-invoice`. A non-stocked line that billing invoices without a packing slip (FR-29.1) must be reported DELIVERED under the invoice number before the INVOICED report, which needs billing to hold `sales.order.record-delivery` as well (see the decision row below). |
| Authz seed for order progress (slice 22) | handoff (authz seed, #313) | Two new duties, each with only `{"resource": "/v1/sales/orders/*:record-progress", "methods": ["POST"]}` (not `/v1/sales/orders/*`, which would also reach PATCH and the other order actions at the gateway): `sales.order.record-delivery` and `sales.order.record-invoice`; the service checks the report kind against them. Service grants, narrow: `service_grants.fulfillment.sales = ["sales.order.record-delivery"]` and `service_grants.billing.sales = ["sales.order.record-invoice", "sales.order.record-delivery"]`, the second only for non-stocked lines billing invoices without a packing slip (FR-29.1); drop it from billing if the "Invoice may not pass delivery" decision below moves that delivery into the invoice report. A role mapping for a clerk correcting by hand is edge-identity. The list `GET /v1/sales/order-progress` is `sales.order.read`: seed `{"resource": "/v1/sales/order-progress", "methods": ["GET"]}` under it. The web nav item "Deliveries and invoices" is gated on `sales.order.read`; the record action and each report kind are gated on the new duties in the page only. |
| Invoice may not pass delivery (slice 22) | decision to confirm | An INVOICED report is refused (`SLS-0059`) beyond the line's delivered quantity. The PRD has billing invoice from posted packing slips, drop-ship lines get their slip from the vendor confirmation (FR-28.7) and down payments are open items, not line invoices (FR-30.21), so these all fit. Two PRD cases do not: order lines for non-stocked items invoiced without a slip (FR-29.1), for which billing reports DELIVERED under the invoice number first (two calls, each idempotent), and milestone billing (w29 revrec), which invoices amounts rather than line quantities and is not reported here. Alternative to confirm: let an INVOICED report deliver the non-stocked remainder in the same call (AX posts the physical update with the invoice). |
| SUBSCRIPTION and RETURNED orders take no invoice report (slice 22) | decision to confirm | A SUBSCRIPTION order is invoiced per billing period by its w29 billing subscription (FR-29.10), so a per-period invoice cannot be counted against the line quantity (the second period would be refused and the first would mark the order INVOICED): INVOICED on a SUBSCRIPTION order is `SLS-0058`, DELIVERED is accepted, and such an order ends at SHIPPED. A RETURNED order receives goods and is credited (w30 RMA, receipt and credit note), so it takes neither report. |
| Authz seed for `:cancel-remainder` (slice 23) | handoff (authz seed, #313) | No new duty: the action uses the existing `sales.order.write`. One route row is needed under `sales.order.write` in `policies/authz/data.json`: `{"resource": "/v1/sales/orders/*:cancel-remainder", "methods": ["POST"]}`, so the gateway lets the call through. |
| Delivered-not-invoiced computed in sales (slice 25) | implemented; decision to confirm | `deliveredNotInvoiced` is computed from each line's `delivered_qty − invoiced_qty` at the line price and discount (BR-27.3), for the customer's other ORDER orders that are PARTIALLY_SHIPPED or SHIPPED, on the profile read, the list and every credit check; a value sent in the PUT is ignored. A partly shipped order now keeps its full exposure (delivered part here, undelivered part in `openOrders`). SUBSCRIPTION orders are left out (billing invoices them per period, not per line quantity, so `invoiced_qty` never moves; slice 22), which leaves the delivered part of a partly shipped SUBSCRIPTION order in no exposure figure until the receivables projection reports its open AR; JOURNAL orders are left out too. Alternative: count a SUBSCRIPTION order's delivered part until it is SHIPPED. Orders in other currencies are returned in `deliveredNotInvoicedOtherCurrencies` and make a BALANCE_PACKING_SLIP or BALANCE_ALL CR-1 pass a WARN, as for open orders. Decision: w29/w30 will own the receivables side; once billing posts invoices to AR the delivered-and-invoiced part is open AR, and a document that bills ahead of delivery is not modelled (an invoice may not pass delivery, `SLS-0059`). Index `sales_order_delivered_exposure` (migration 0028). |
| Authz seed for the per-line `:cancel-remainder` (slice 24) | handoff (authz seed, #313) | No new duty: the action uses the existing `sales.order.write`. One route row is needed under `sales.order.write` in `policies/authz/data.json`: `{"resource": "/v1/sales/orders/*/lines/*:cancel-remainder", "methods": ["POST"]}`, so the gateway lets the call through. |
| Authz seed for the credit hold rejection (slice 26) | handoff (authz seed, #313) | No new duty: the action uses the existing `sales.credit.release`. One route row is needed under `sales.credit.release` in `policies/authz/data.json`: `{"resource": "/v1/sales/orders/*/holds/*:reject", "methods": ["POST"]}`, next to the existing `/v1/sales/orders/*/holds/*:release` row, so the gateway lets the call through. The queue nav item stays gated on the seeded read duty. |
| Replay as the replaying user (slice 30) | decision to confirm | A replay re-submits the kept request as the user (or scheduler identity) who replays it, not as the channel that sent it: the user's legal-entity scope applies and the order's `actor` is the user. One consequence: a user holding `sales.order.approve` who replays a request that asked for a listing override (`listingOverrideReason`) gets the override the channel's identity could not grant. Alternatives: never pass the approve duty on a replay, or keep the original principal's duties on the row. |
| Re-refusal answered 200 (slice 30) | decision to confirm | A replay that intake refuses again answers 200 with `replayed: false` and the refusal in `failure`, because the call did change the row (an attempt, the new message, `intakeerror.recorded`) and the bulk call reports the same per-row shape. Alternative: answer the refusal's own 4xx for the single replay. |
| Kept refused requests: retention (slice 30) | decision to confirm; w48 | The body and key are kept while the row is OPEN, however long that is, including dead letters (10 or more attempts); they go only when the row is resolved. Alternative: purge them from OPEN rows after a period (for example 30 days) and leave such a row not replayable. The intake error row itself (hash, message, no body) has no retention yet (w48). |
| Bulk replay cost and faults (slice 30) | decision to confirm | One bulk call replays up to 100 rows one after another, each in its own transactions (the order, then the row), inside the request; at the default of 50 it stays well under the PRD's 1-minute run. A server fault on one row (for example the product service down for a configured line) stops the batch with that problem; the replays done before it are kept and the next run starts again from the oldest row, so a row that faults every time holds back the rows behind it until it is resolved or replayed by hand. Partitioning is by legal entity (optional channel filter) rather than by channel as the PRD's job row says. |
| Derived replay key `replay:<id>` (slice 30) | implemented | A request refused without an Idempotency-Key is replayed under `replay:<intake error id>`, stable across replays so a repeat finds the order. A sender could send such a key itself only by knowing the row id, and would then get only an order with the identical body (the request hash must match) or a 422; keys are unique per tenant. |
| Authz seed for the order auto-close (slice 31) | handoff (authz seed, #313) | No new duty: the action uses the existing `sales.order.write`. One route row is needed under `sales.order.write` in `policies/authz/data.json`: `{"resource": "/v1/sales/orders:auto-close", "methods": ["POST"]}` (the existing `/v1/sales/orders/*:cancel-remainder` style rows do not cover it). The scheduler needs a service principal holding `sales.order.write` (and the legal-entity scope it closes for), or an all-scope one. Until seeded the web action is refused with 403; the preview is refused too, as it is the same route. |
| Authz seed for the quotation cancel and change (slice 32) | handoff (authz seed, #313) | No new duty: both actions use the existing `sales.quotation.maintain`. Two route rows are needed under `sales.quotation.maintain` in `policies/authz/data.json`: `{"resource": "/v1/sales/quotations/*:cancel", "methods": ["POST"]}` and `{"resource": "/v1/sales/quotations/*", "methods": ["PATCH"]}` (`*` does not cross `:` or `/`, so the existing `/v1/sales/quotations/*:send` style rows and the read row `/v1/sales/quotations/*` for GET under `sales.order.read` do not reach them). Until seeded the gateway refuses both calls; the service already enforces the duty. |
| Quotation line replacement and "today" (slice 32) | decision to confirm | A line change deletes the DRAFT's lines and inserts the new set renumbered from 1, so every line gets a new id. Nothing refers to a quotation line id: conversion, `quotation.converted` and the order line go by line number, a DRAFT has never converted, and `price_explain_ref` does not exist yet (row 1). If a later slice keys anything on the quotation line id (explain refs, attachments), the PATCH must instead take line ids and upsert. "Today" for the validity window and the lapsed check is the UTC day of the service clock, as for create, `:send` and `:convert` (see the slice 21 row). |
| What a SENT quotation may change (slice 32) | decision to confirm | The PRD gives no edit rule. Chosen: a SENT quotation may take a new validity (shorter or longer, from today up to a year) and probability, but no lines, since the customer holds its prices; to requote, cancel it and create a new one. A quotation past its validity cannot be changed or extended, even before the expiry run marks it EXPIRED, but it can still be cancelled. Customer, currency and invoice account never change. |
| Order auto-close scheduler and `order.closed` consumers (slice 31) | handoff (deployment-cicd; portal, analytics) | PRD §5.27.16 runs it nightly at 02:00 per legal entity: a CronJob calling `POST /v1/sales/orders:auto-close?legalEntityId=<id>` for each entity, repeated while `more` is true and the call closed at least one order (an order SKIPPED on every run, whose lines no longer derive INVOICED, would otherwise keep the head of the list and loop the job). `dev.passage.sales.order.closed.v1` (topic `passage.sales.order`) has no consumer yet. |
| Order auto-close grace period (slice 31) | decision to confirm | The PRD gives no rule for INVOICED to CLOSED, and row 4 below ties CLOSED to the wave 30 payment signal, which does not exist. The close is therefore by age: unchanged for `graceDays` (default 30) with no active hold. Alternatives: a `order_auto_close_days` sales parameter per legal entity (it would be added to the parameters PUT, read and `parameters.changed`), or closing only once w30 reports the invoice paid. Payment is not checked either way. |
| Refusals of one-time requests without keys share a row (slice 31) | decision to confirm | Without `FIELDCRYPT_KEYS` the hash of a one-time request is the constant `unkeyed`, so every such refusal under one (legal entity, channel, reference, error code) raises `attempts` on one row and the last message wins. No request body is lost: without keys such a body is never kept, so the row is not replayable either way. Alternative: refuse to log them at all without keys. |
| Keyed intake hash across a key rollout (slice 31) | decision to confirm | Replicas that briefly hold different current keys can each open a row for one refused request (one per key); later retries land on the older row. A key removed from `FIELDCRYPT_KEYS` cannot be recomputed, so a retry of a request logged under it opens a row under the current key. Both are accepted splits of `attempts`, not lost refusals. |
| Plain digests already published (slice 31) | decision to confirm; w48 | Migration 0035 clears `intake_error.request_hash`, but `intakeerror.recorded` / `resolved` events written by slices 20 to 30 carry the plain digest of a one-time request in `request_hash`, both in Kafka (`passage.sales.intakeerror`, kept for the topic's retention) and in the sales `outbox` table, which is never pruned. Rewriting outbox rows would reach Debezium as updates and is not done; pruning the outbox and the topic retention belong to the data retention work (w48). |
| Auto-close and returns or payments (slice 31) | decision to confirm | The close looks only at the order: an INVOICED order closes after the grace period whether or not the invoice is paid (w30 payment signal) or an RMA against its invoice is open. Return orders are created from the billing invoice, not from the order's status, so a CLOSED order does not block a return. |
| `order.closed` for fulfillment (slice 31) | none needed | The `fulfillment-order` consumer ignores the type (it follows `order.confirmed`, `order.changed`, `order.cancelled` and `order.remaindercancelled`), and an INVOICED order has nothing left to ship. |
| Authz seed for the intake error replay (slice 30) | handoff (authz seed, #313) | No new duty: both actions use the existing `sales.order.write`. Two route rows are needed under `sales.order.write` in `policies/authz/data.json`: `{"resource": "/v1/sales/intake-errors/*:replay", "methods": ["POST"]}` and `{"resource": "/v1/sales/intake-errors:replay", "methods": ["POST"]}` (the existing `/v1/sales/intake-errors/*:resolve` row does not cover them). Until seeded the web actions are refused with 403. |
| Fulfillment's copy of holds and `orderhold.rejected` (slice 26) | handoff (fulfillment, wave 28) | A rejection cancels a DRAFT or OPEN order, which was never confirmed, so fulfillment holds no shipment for it (fulfillment does not follow `order.cancelled`; see the slice 28 review row). A consumer that mirrors each hold from `orderhold.applied` / `released` should also treat `dev.passage.sales.orderhold.rejected.v1` (`order_id`, `hold_id`, `comment`, `version`) as the end of that hold, so its copy does not show an ACTIVE hold on a cancelled order. Collections and the portal may show the comment. |
| A credit hold rejection cancels the order (slice 26) | decision to confirm | The PRD says only "release/reject with comment" (§5.27.6). Rejecting ends the order: it is cancelled exactly as `:cancel` does (same code path), with no approval step because the order is unconfirmed, and the customer is quoted or ordered again. Only a CREDIT hold of a DRAFT or OPEN order can be rejected; a CREDIT hold raised on a CONFIRMED order by a total-raising change is answered by a release, a further change or `:cancel` (which runs change approval). Alternatives: leave the order OPEN with the hold REJECTED for the sales rep to change and confirm again, or allow rejection of the CONFIRMED-order hold through change approval. A release takes no comment yet (the PRD's "with comment" is built for rejection only). |
| Fulfillment and the slice 29 `order.remaindercancelled` (slice 29) | handoff (fulfillment, wave 28) | Sales now emits what the existing `fulfillment-order` consumer (`internal/api/orderconsumer.go`, `internal/store/remaindercancel.go`) already handles, so no new event type and no consumer change is required for correctness: the consumer decodes only `order_id`, `legal_entity_id`, `version` and per line `line_id` and `kept_qty` (unknown `status` / `line_status` values are ignored), upserts `order_line_kept` when the stored `order_version` is not newer (a redelivery re-applies the same ceiling and cuts nothing more; exceptions are open-once), and cuts released and picked quantity above `kept_qty` less what shipped. What sales emits: (1) a `:cancel` of a CONFIRMED order (direct, or an approved CANCEL request) queues it after `order.cancelled`, same version and transaction, `status` CANCELLED, every cancelled line `kept_qty` 0 and `cancelled_qty` the line quantity; (2) a PATCH of a CONFIRMED order puts in one event, after `order.changed` with the same version, every line whose quantity ceiling moved: removed and cancelled supplementary lines (kept 0), lowered lines (kept the new quantity, `line_status` OPEN or RESERVED), raised lines and reopened supplementary lines (kept the new quantity, `cancelled_qty` 0). Only OPEN or RESERVED lines can change or be cancelled, and the first delivery moves a line to PICKED and the order to PARTIALLY_SHIPPED, so sales never sends a `kept_qty` below what it has recorded as delivered; a slip fulfillment posted before the event arrived is raised by the consumer as SLIP_POSTED_BEYOND_KEPT and its DELIVERED report is refused by sales (`SLS-0058` on a cancelled order, `SLS-0059` beyond a cut line's quantity). Left for fulfillment: a raise on a drop-ship line does not reopen the drop-ship quantity the cut closed (the line is skipped at release while a drop-ship is active), and a CANCEL request waiting for approval does not stop fulfillment from shipping (the first delivery moves the order on, so the approval is then refused with `SLS-0046`). The fulfillment AsyncAPI text for the consumed message still says "remainder of a partly shipped order" and should name these cases. |
| Sales and the drop-ship closes of wave 28 (slice 26) | nothing to build in sales | Fulfillment's `dropship.closed` / `dropship.cancelled` with reason `order_remainder_cancelled` follow sales' own `order.remaindercancelled`, which sales has already applied when it emits it. Sales consumes no events (it has no Kafka consumer; its inputs are HTTP calls), so it cannot cancel an order line a second time on them, and it needs no handler to treat them as known: the close of the remainder is done at the moment sales commits it. If sales later consumes drop-ship events (for example to drive `:record-progress`), a close with that reason must be ignored for cancellation. |
| Authz seed for slice 25 | none needed | No new duty, route or service grant: the computed figure is on the existing credit-profile reads (`sales.order.read`). |
| Fulfillment must react to `order.remaindercancelled` (slice 23) | handoff (fulfillment, wave 28) | Sales cannot see shipments, so it does not refuse a close while a release, pick or pack for the remainder is in flight; fulfillment owns that side. The `fulfillment-order` consumer should follow `dev.passage.sales.order.remaindercancelled.v1` on `passage.sales.order` (ordered by `version`, like `order.confirmed`). For each entry of `lines` (`line_id`, `kept_qty`, `cancelled_qty`; `kept_qty` 0 and `line_status` CANCELLED for a line closed with nothing delivered), it should cancel or close the shipment lines and pick work of that sales line that are not yet on a posted packing slip, beyond `kept_qty` less what was already posted. It should release their reservations (FR-28.2) and stop releasing the line again. It should refuse a packing slip post for that quantity with a FUL code naming the closed remainder, so the post fails before the goods leave. If a slip was already posted for the remainder before the event arrived (the race), the slip's `:record-progress` DELIVERED report is refused by sales with `422 SLS-0058` ("nothing is left to deliver ... its remainder was cancelled"). Fulfillment must not retry that report. It should raise it as an exception for the order desk: the goods are off the order and need a return (w30) or a new order. |
| Duty for `:cancel-remainder` (slice 23) | decision to confirm | The PRD has two answers: the API table puts `:confirm, :cancel; /lines/{l}:cancel-remainder` under SalesOrderConfirm, and the duty catalog (and the Backorders screen) puts `:cancel-remainder` under SalesOrderMaintain, with SalesOrderConfirm only for `:confirm`. The route takes `sales.order.write` (SalesOrderMaintain), as the catalog says, while `:cancel` keeps `sales.order.confirm`. Alternative: `sales.order.confirm`, like `:cancel`. |
| Holds at a remainder close (slice 23) | decision to confirm | ACTIVE holds stay on the closed order and are released by their owners (see the slice 23 note). Alternative: clear a CREDIT hold at the close, because the exposure it guarded is gone, emitting `orderhold.released` so fulfillment's copy follows. FRAUD and MANUAL holds would still stay. |
| Remainder close needs no approval (slice 23) | decision to confirm | FR-27.9 change approval applies to CONFIRMED orders. A close of a PARTIALLY_SHIPPED order keeps the delivered quantity and is allowed to a holder of `sales.order.confirm`, without an approver or a change request. |
| Threshold measure and budget (slice 27) | decision to confirm | The PRD names only a per-customer switch (5.27.13 `change_approval_required`); the amount threshold and its measure are a slice default. A change is measured as the sum of each line's absolute net movement, and the changes applied to an order without approval share one budget below the threshold for the order's whole life (approved changes do not reset it), so a change cannot be split to avoid approval. Alternatives: measure the net total delta (a line swap would count 0), or reset the budget at each approval. |
| Named approvers vs the approve duty (slice 27) | decision to confirm | A non-empty group limits both deciding a request and skipping approval with `sales.order.approve`; the policy PUT itself stays open to any holder of the duty, so a group whose members left can be changed, at the cost that a duty holder outside the group can rewrite the group (audited in `changepolicy.changed`). Alternative: limit the PUT to the group once one is named, with an administrator override. |
| PUT keeps members it does not name (slice 27) | decision to confirm | `PUT /change-policy` keeps a stored threshold or group the body leaves out (null / [] clear them), so slice 13 clients cannot wipe them. Alternative: a strict whole-policy replace, which is simpler but lets an older client silently drop a threshold and group. |
| Jobs: ATP rebuild 01:00, credit review daily, order auto-close, intake DLQ replay | later slice of PSG-27; handoff (deployment-cicd) | No scheduler is built here. The intake replay (slice 30) and the order auto-close (slice 31, `POST /orders:auto-close`) are endpoints that a scheduler calls; the ATP rebuild and the credit review notice are not built. |
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
| Sales workspace (`/sales/workspace`): tiles for orders on hold, backorder lines, quotes expiring in 7 days and lapsed-not-expired, the open pipeline per currency, conversion rate, and the expire-lapsed-quotations action | implemented | Reads `GET /summary`; loading, empty and error states and vitest tests in `workspace.test.tsx`. The intake errors tile (§5.27.6) counts OPEN intake errors since slice 20 (the list is under Intake errors in the navigation). Not built: the tiles drill into the existing lists without carrying the tile's filter. |
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
| Catch-weight sales lines (FR-24.7, BR-24.5, Wave 24 AC4) | partial - #480 | Order lines take `catchWeight` {nominal, min, max} (0 < min <= nominal <= max, 6 decimals; 400 under `catchWeight` otherwise); `quantity` is a whole number of cases (also on a quantity change and on every `:record-progress` report, DELIVERED or INVOICED, `SLS-0063`) and `unit` is the unit of that count (the case unit, such as CS); nominal, min, max, `actualWeight` and `unitPrice` are per the product's inventory weight unit, which the line does not store yet (product master, #480). Convention (D365-style, one for receiving, sales and inventory): a catch-weight line is priced per weight unit, so the PO line unit is a mass unit (receiving `IsWeightUnit`) and `NetUnitPrice` is per weight unit; receipt `qty` is the weight and `cwQty` the cases; inventory `qty` is the weight and `cw_qty` the cases; sales `quantity` is cases and `unitPrice` is per weight unit. LineNet at intake is price x cases x nominal. A DELIVERED `:record-progress` with `actualWeight` checks that weight against the cases that report delivers (`SLS-0061` outside min/max x cases, `SLS-0063` for fractional cases, a non-catch-weight line or an INVOICED report), adds it to the line's recorded `actual_weight` (cases reported without a weight count at nominal) and restates LineNet as price x (actual weight + undelivered cases x nominal) and `netTotal`. Credit exposure (`undeliveredNetSQL`, `deliveredNotInvoicedSQL`) uses `actual_weight`, else delivered cases x nominal. Proof: 10 cases at nominal 10 kg and 2.00 -> 200; 5 cases at 51 kg -> 202; 5 more at 52 kg -> 206. Inventory issue of the sales line is wave 16 `transactions:apply`. `order.confirmed` and `orderprogress.recorded` carry `quantity` in cases; since #480 a catch-weight line also carries `catch_weight` {nominal, min, max} on `order.confirmed` and `reported_weight` and `delivered_weight` on a DELIVERED `orderprogress.recorded`, so a consumer that reserves or issues inventory (inventory `qty` is the weight) takes the weight from those fields, never from `quantity`. Refused (400) on a quotation line (the quotation store keeps no spec and order-from-quotation would copy a weight-priced net onto a plain line) and on a line added by an order change; not on the web order form. Built in #480 (follow-up): `orderprogress.recorded` also carries the order `currency` and each line's `product_id`, `unit`, `site_id` and `warehouse_id`, and the inventory postings consumer (`cmd/inventory consume`, service principal `service:inventory-postings` with only `inventory.transaction.apply`, Kafka group `inventory-postings`, dead-letter topic `passage.inventory.postings.dlq`) books receiving's `receipt.posted` (origin `RECEIPT_LINE`, status RECEIVED, qty = the weight, `cw_qty` = the cases, valued qty x `unit_price`) and sales' DELIVERED `orderprogress.recorded` (origin `SALES_LINE`, status DEDUCTED, qty = -`reported_weight`, `cw_qty` = the cases, enteredQty = -cases in the line's case unit, valued at the row's running average per weight unit including physical receipts, `store.IssueBasis`) under the legal entity of the document, the dimensions site and warehouse (plus `inventory_status` for a receipt not received AVAILABLE). `TestPostgres_CatchWeightDeliveryMatchesTheInventoryContract` checks a delivery of 5 cases / 51 kg against inventory's fixture `services/inventory/testdata/catchweight-flow/orderprogress-delivered.json` member for member. The GR/IR open item carries `cwQty` and fulfillment cogs-matrix-14 costs a catch-weight line on its weight. Not built: a plain line's delivery is not booked in inventory (no unit conversion, #96), and fulfillment does not yet report the shipped weight to `:record-progress` itself. |
