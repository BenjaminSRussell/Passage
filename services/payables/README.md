# payables

Wave 19 deployable: accounts payable settlement and payments. This slice ships the settlement engine and a stateless preview endpoint. One Go module, one image (`payables`). Deferred deliverables are listed in `NOT-IMPLEMENTED.md`.

## Run and test

```
cd services/payables
go vet ./... && go test ./...
DATABASE_URL=postgres://... go run ./cmd/payables
```

## Test data

`testdata/xsd/` holds the official pain.001.001.09 and pain.002.001.10 schemas, vendored verbatim (`SOURCE.md` records origin, date and sha256; never edit them). `testdata/pain001/` holds one JSON input and one `.golden.xml` per bank profile; `testdata/pain002/` holds hand-written status reports. Regenerate the pain.001 goldens with `go test ./internal/domain -run Golden -update`; never edit a golden by hand. The XSD contract tests and the golden API tests need `xmllint` (libxml2-utils) on `PATH` and skip with an explicit message when it is absent.

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `HTTP_ADDR` | default `:8080` |
| `ADMIN_ADDR` | default `:9090`, serves `/metrics` only |
| `FX_URL` | base URL of the fx service; unset: foreign-currency valuation and proposals with foreign items answer 503 |
| `FX_RATE_TYPE` | fx rate type used for valuation, default `spot` |
| `FX_TIMEOUT` | Go duration for the fx call, default 2s |

## API

`POST /v1/payables/settlements:preview` (duty `settlement.read`), `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port. Contract: `api/openapi.yaml`.

`POST /v1/payables/payment-files:preview` (duty `payment-file.preview`) builds a pain.001.001.09 file from a JSON request (same shape as `testdata/pain001/*.json`; `created` defaults to the server clock; at most 2000 payments), validates it against the vendored XSD and returns `content`, a `summary` (`paymentCount`, `controlSum`, `sha256`) and `validation`. `POST /v1/payables/payments/status:parse` (duty `payment-status.parse`) parses a pain.002.001.10 report (`content`) against the caller-supplied `messages` and `payments` into outcomes and unknown references. Both are stateless and persist nothing; the status parse does not replace the PRD's stateful `POST /payments/status:ingest`.

XSD validation runs `xmllint` (libxml2-utils) as a subprocess with `--nonet`, a minimal environment, a 10 second timeout and at most 3 concurrent runs. Only xmllint exit code 3 counts as an invalid document; any other failure is a 500. The service validates a known-good file at startup and refuses to start without `xmllint` on `PATH`; the Docker image installs it.

The preview settles one credit against open invoices per PRD FR-19.2: priority order, cash discount (pro-rated on partial settlement), realized FX gain or loss, write-off within the limit, and a balanced journal. Amounts, rates and percentages are decimal strings; rates come from the request until the wave 12 rate lookup exists. A cross-currency credit settles in the accounting currency, so its FX difference is zero by construction.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| PAY-0001 | 400 | invalid field: date, decimal string, currency, discount, duplicate or empty invoices |
| PAY-0002 | 422 | rate missing for a foreign currency, or not 1 for the accounting currency; also an fx service with no rate for the pair and date |
| AP-RUN-002 | 422 | bank identifier failed validation (`iban`, `bic` or `aba` named in the detail) |
| AP-FIL-001 | 422 | payment name has a character outside the profile charset after transliteration |
| AP-PRP-001 | 409 | proposal run id already used with a different request, or items changed, were held or were taken by another proposal while the proposal was built (retry) |
| AP-PRP-002 | 409 | proposal is not a draft (approve or cancel) |
| AP-PRP-003 | 403 | the approver is the proposal's creator (segregation of duties) |
| AP-PRP-004 | 409 | the proposal's run date is before today (UTC); create a new proposal |
| AP-PRP-005 | 422 | no open items are due; nothing is stored |
| COM-0404 | 404 | open item or proposal not found (or outside the caller's tenant and legal entity) |
| COM-0502 | 502 | fx service unavailable |
| COM-0503 | 503 | fx rate source is not configured |
| AP-FIL-002 | 200 | generated pain.001 failed XSD validation; reported in `validation.errors` of the preview response, never as a problem |
| AP-STS-001 | 200 | bank rejected a payment in a pain.002 report; returned as an outcome `warning` |
| AP-STS-002 | 200 | pain.002 names a message or end-to-end id the caller does not know; returned in `unknown` |
| COM-0001 | 400 | malformed body, unknown field or wrong JSON type |
| COM-0413 | 413 | body over the limit: 4 MiB for the preview, 16 MiB for status parse, 10 MiB of status XML |
| COM-0500 | 500 | internal error, detail hidden |
| AUTH-0001 | 401 | missing or malformed tenant (kernel header middleware) |
| AUTH-0003 | 403 | caller lacks the duty (kernel authz) |

Problem bodies from this service carry `code` (a stable slug such as `validation_failed`) and `serviceCode` (the table above).

## Metrics

Settlement preview: `payables_settlement_previews_total`, `payables_settlement_preview_client_errors_total`, `payables_settlement_preview_server_faults_total`. Payment files: `payables_payment_file_previews_total`, `payables_payment_status_parses_total`, `payables_payment_file_client_errors_total`, `payables_payment_file_server_faults_total`. Plus per-route request counters and latency histograms (stdlib atomics rendered as text).

## Vendor aging and AP control reconciliation

`GET /v1/payables/aging` (duty `aging.read`) buckets open items (`open_amount`) by days past due at `as_of` (CURRENT, 1-30, 31-60, 61-90, 91-180, OVER-180) per currency, and per vendor with `group_by=vendor`. `GET /v1/payables/aging:reconcile` (duty `aging.reconcile`, required `ledger` query parameter) compares total open AP per currency with the AP control account in the ledger (credits minus debits from `GET /v1/ledger/balances` through the period of `as_of`) and returns the difference and a `reconciled` flag.

Configuration: `AP_CONTROL_ACCOUNT` (default `2000`) and `LEDGER_URL` (unset: reconcile answers 503) and `LEDGER_TIMEOUT` (Go duration, default 3s). The ledger call forwards the caller's tenant, legal-entity, user and duty headers, so the caller needs `ledger.balance.read` and `ledger.journal.read` (the ledger is fetched to confirm it belongs to the legal entity; a ledger 403 is returned as 403); a service identity (#226) will replace the forwarding. A ledger reports one currency in the accounting currency and includes translated foreign invoices, so other currencies are listed unreconciled and, while foreign-currency items are open, the ledger-currency line is marked `comparable: false` (reason `foreign-currency items require translation`) and the overall result is not reconciled. Reconcile at a period end: the ledger side is cumulative to the period, the open-item side is the current balance.

## Open-item API, FX valuation and payment proposals

`GET /v1/payables/open-items` and `GET /v1/payables/open-items/{id}` (duty `open-item.read`) read the open-item store. The list filters by `vendor`, `due_from`, `due_to`, `currency` and `status`, is ordered by due date then id and pages with `limit` (1 to 200, default 50) and the opaque `cursor` returned as `nextCursor`. Every request names its legal entity (`legal_entity` or the caller's single legal-entity scope); an unscoped caller without `legal_entity` gets 400, and an entity outside the caller's scope gets 403. With `value_in=USD` each item carries a `valuation` (open amount times the spot rate of today, rounded to the currency's minor unit).

Rates come from `GET /v1/fx/rates` of the fx service (`internal/fx`, same adapter shape as `internal/ledger`). The call forwards the caller's tenant, legal-entity, user and duty headers, so the caller also needs the fx duty `View exchange rates`; a service identity (#226) will replace the forwarding. A pair without a rate is a 422 (`PAY-0002`), a caller fx refuses is a 403, anything else is a 502.

`POST /v1/payables/payment-proposals` (duty `payment-proposal.write`) builds a payment proposal for a legal entity:

- Selection: open items with `due_date <= dueBy` (default `runDate`), status OPEN or PARTIALLY_SETTLED, not on payment hold (`ap_open_item.payment_hold`), vendor not in `ap_vendor_block`, and not already on a proposal. At most 5000 items; lower `dueBy` otherwise.
- Cash discount: `runDate` is the payment date and must not be earlier than today (UTC), so a backdated date cannot revive an expired discount; replaying an existing run stays allowed. Taken when `runDate <= discount_date`, the item is still OPEN (a partly settled item pays its full remaining amount) and the discount is below the open amount. `pay = open_amount - discount`.
- Grouping: per vendor, currency and payment method (`paymentMethod`, overridden per vendor by `vendorPaymentMethods`). Every group and line is valued in `legalEntityCurrency` at the spot rate of `runDate` (today when `runDate` is in the future); each line is rounded to the currency's minor unit before summing.
- Persistence: `ap_payment_proposal`, `ap_payment_proposal_group`, `ap_payment_proposal_line`, status DRAFT. A line keeps the item version it was selected at and the insert re-checks version, open amount, hold and vendor block, so an item that changed in between fails the whole proposal with 409 and nothing is written. An item can sit on one live proposal only (partial unique index over lines with `reserved`); `POST /v1/payables/payment-proposals/{id}:cancel` (duty `payment-proposal.write`, DRAFT only, audited in `cancelled_by`/`cancelled_at`) releases the lines while keeping them for audit, and releasing on execution is part of #102. A run with nothing due is a 422 and stores nothing.
- Idempotency: unique per (legal entity, `runDate`, `runId`). Repeating the identical request returns the stored proposal (200, `Idempotent-Replayed: true`) without selecting or valuing again; the same key with a different request is 409 `AP-PRP-001`.

`GET /v1/payables/payment-proposals/{id}` (duty `payment-proposal.read`) returns the proposal with groups and lines. `POST /v1/payables/payment-proposals/{id}:approve` (duty `payment-proposal.approve`) moves a DRAFT to APPROVED and requires a different user than the creator (`AP-PRP-003`); the table also carries a check constraint so no code path can store a self-approval. Approval locks each line's open item and re-checks version, open amount, hold, vendor block and status; a changed line is a 409 `AP-PRP-001` naming the items, and a run date before today (UTC) is a 409 `AP-PRP-004`, so an expired discount can never be approved. Creator and approver both need an identified user. The application role may update only the status and the approval and cancellation columns of a proposal, and a trigger refuses any update once the proposal is APPROVED or CANCELLED (APPROVED stays terminal until #102 adds execution states), and a line is released only by cancelling its proposal.

Payment holds and vendor blocks are read here and written by `store.SetPaymentHold`, `store.BlockVendor` and `store.UnblockVendor`; there is no HTTP route for them yet. The approved proposal is the input of the payment run (#102); withholding tax is not applied.

Metrics: `payables_payment_proposals_total{outcome}` (created, replayed, approved, self_approval_refused, cancelled) and `payables_fx_rate_faults_total`.
