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
| `LEDGER_URL` | base URL of the ledger service; unset: aging reconcile answers 503 and payment runs 422 |
| `AP_CONTROL_ACCOUNT` | control account of the aging reconciliation, default `2000` (payment runs use their posting profile) |
| `PAYABLES_POSTING_PROFILES` / `PAYABLES_POSTING_PROFILES_FILE` | JSON map legal entity id to `{ledgerId, bankAccount, controlAccount, discountAccount, file}` where `file` holds the paying side of the pain.001 (debtor, debtorAccount, debtorBic, profile). The mounted file wins over the variable and unknown fields are rejected at startup; a legal entity without a profile cannot run payments (422) |
| `FIELDCRYPT_KEYS_FILE` / `FIELDCRYPT_KEYS`, `FIELDCRYPT_CURRENT_KEY_ID` | `id=base64(32 bytes)` lines (a mounted secret) and the key id new values use; old ids stay in the file so rotation keeps old values readable. The mounted file wins over `FIELDCRYPT_KEYS`. Without keys a proposal carrying bank details, and a run start, are a 503 |
| `PASSAGE_SERVICE_NAME`, `PASSAGE_SERVICE_SIGNING_KEY_FILE`, `PASSAGE_SERVICE_PUBLIC_KEYS` | service identity (#303): payables signs its ledger calls (reads and the journal post) and verifies the workflow's token (`workflow-ts=<base64 public key>`) |

## API

`POST /v1/payables/settlements:preview` (duty `settlement.read`), `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port. Contract: `api/openapi.yaml`.

`POST /v1/payables/payment-files:preview` (duty `payment-file.preview`) builds a pain.001.001.09 file from a JSON request (same shape as `testdata/pain001/*.json`; `created` defaults to the server clock; at most 2000 payments; `method.type` is limited to `TRANSFER` and `WIRE`, the only methods that render as pain.001, while bank profiles also accept `ACH`, `CHECK` and `CARD`), validates it against the vendored XSD and returns `content`, a `summary` (`paymentCount`, `controlSum`, `sha256`) and `validation`. `POST /v1/payables/payments/status:parse` (duty `payment-status.parse`) parses a pain.002.001.10 report (`content`) against the caller-supplied `messages` and `payments` into outcomes and unknown references. Both are stateless and persist nothing; the status parse does not replace the PRD's stateful `POST /payments/status:ingest`.

XSD validation runs `xmllint` (libxml2-utils) as a subprocess with `--nonet`, a minimal environment, a 10 second timeout and at most 3 concurrent runs. Only xmllint exit code 3 counts as an invalid document; any other failure is a 500. The service validates a known-good file at startup and refuses to start without `xmllint` on `PATH`; the Docker image is distroless (`distroless/cc-debian12:nonroot`) and carries `xmllint` plus its shared libraries copied from a digest-pinned `debian:12-slim` stage.

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
| AP-PRP-003 | 403 | the approver is the proposal's creator, or the run executor is the proposal's creator or approver (segregation of duties) |
| AP-PRP-004 | 409 | the proposal's run date is before today (UTC); create a new proposal |
| AP-RUN-011 | 409 | the run is no longer running (failed or completed), or its journal is posted and it can no longer fail |
| AP-RUN-012 | 409 | a run step needs an earlier step first |
| AP-RUN-013 | 422 | the run cannot be built: no posting profile for the legal entity, a group is not TRANSFER or WIRE or not in the legal-entity currency, a vendor has no approved bank details, a discount has no discount account, or the profile's ledger belongs to another legal entity |
| AP-RUN-014 | 422 | the ledger rejected the payment journal (4xx); a ledger 5xx is 502 and retryable |
| AP-RUN-015 | 409 | an open item changed between approval and the file or settlement step; nothing was built or settled (after the journal posted it also raises `payables_payment_run_stuck_total`) |
| AP-RUN-016 | 403 | a payment run needs an identified user (`X-Passage-User` or a service token's `act`); an application code so the workflow treats it as terminal, not as an auth fault |
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

`group_by=vendorGroup` on `aging:reconcile` adds `vendorGroups`: open AP per currency and vendor group (`UNASSIGNED` last). The vendor group is stored on `ap_open_item.vendor_group_id` (migration 0006, nullable) from the optional `vendor_group_id` of `invoice.posted`; a vendor has one group in the party master (`party.vendor.vendor_group_id`, whose posting profile selects the control account), so no multi-group choice is made here (purchasing's multi-group memberships are pricing-only). Rows created before the migration, or from events without the field, are `UNASSIGNED`; redelivering the event with a group fills a still-empty group and never overwrites an assigned one. The ledger balance endpoint has no vendor-group dimension, so there is one control balance per ledger: group lines are a breakdown of the currency total (`comparable: false`), and the per-currency comparison and `reconciled` flag are unchanged. The aging report rejects `group_by=vendorGroup`.

Configuration: `AP_CONTROL_ACCOUNT` (default `2000`) and `LEDGER_URL` (unset: reconcile answers 503) and `LEDGER_TIMEOUT` (Go duration, default 3s). The ledger call forwards the caller's tenant, legal-entity, user and duty headers, so the caller needs `ledger.balance.read` and `ledger.journal.read` (the ledger is fetched to confirm it belongs to the legal entity; a ledger 403 is returned as 403); a service identity (#226) will replace the forwarding. A ledger reports one currency in the accounting currency and includes translated foreign invoices, so other currencies are listed unreconciled and, while foreign-currency items are open, the ledger-currency line is marked `comparable: false` (reason `foreign-currency items require translation`) and the overall result is not reconciled. Reconcile at a period end: the ledger side is cumulative to the period, the open-item side is the current balance.

## Open-item API, FX valuation and payment proposals

`GET /v1/payables/open-items` and `GET /v1/payables/open-items/{id}` (duty `open-item.read`) read the open-item store. The list filters by `vendor`, `due_from`, `due_to`, `currency` and `status`, is ordered by due date then id and pages with `limit` (1 to 200, default 50) and the opaque `cursor` returned as `nextCursor`. Every request names its legal entity (`legal_entity` or the caller's single legal-entity scope); an unscoped caller without `legal_entity` gets 400, and an entity outside the caller's scope gets 403. With `value_in=USD` each item carries a `valuation` (open amount times the spot rate of today, rounded to the currency's minor unit).

Rates come from `GET /v1/fx/rates` of the fx service (`internal/fx`, same adapter shape as `internal/ledger`). With a service signing key (`PASSAGE_SERVICE_SIGNING_KEY[_FILE]`) the call carries a signed service token holding only `fx.rate.lookup` for the request's legal entity, so the caller needs no fx duty (#305); without a key it forwards the caller's headers and the caller needs `View exchange rates`. A pair without a rate is a 422 (`PAY-0002`), a caller fx refuses is a 403, anything else is a 502.

`POST /v1/payables/payment-proposals` (duty `payment-proposal.write`) builds a payment proposal for a legal entity:

- Selection: open items with `due_date <= dueBy` (default `runDate`), status OPEN or PARTIALLY_SETTLED, not on payment hold (`ap_open_item.payment_hold`), vendor not in `ap_vendor_block`, and not already on a proposal. At most 5000 items; lower `dueBy` otherwise.
- Cash discount: `runDate` is the payment date and must not be earlier than today (UTC), so a backdated date cannot revive an expired discount; replaying an existing run stays allowed. Taken when `runDate <= discount_date`, the item is still OPEN (a partly settled item pays its full remaining amount) and the discount is below the open amount. `pay = open_amount - discount`.
- Grouping: per vendor, currency and payment method (`paymentMethod`, overridden per vendor by `vendorPaymentMethods`). Every group and line is valued in `legalEntityCurrency` at the spot rate of `runDate` (today when `runDate` is in the future); each line is rounded to the currency's minor unit before summing.
- Persistence: `ap_payment_proposal`, `ap_payment_proposal_group`, `ap_payment_proposal_line`, status DRAFT. A line keeps the item version it was selected at and the insert re-checks version, open amount, hold and vendor block, so an item that changed in between fails the whole proposal with 409 and nothing is written. An item can sit on one live proposal only (partial unique index over lines with `reserved`); `POST /v1/payables/payment-proposals/{id}:cancel` (duty `payment-proposal.write`, DRAFT only, audited in `cancelled_by`/`cancelled_at`) releases the lines while keeping them for audit, and releasing on execution is part of #102. A run with nothing due is a 422 and stores nothing.
- Idempotency: unique per (legal entity, `runDate`, `runId`). Repeating the identical request returns the stored proposal (200, `Idempotent-Replayed: true`) without selecting or valuing again; the same key with a different request is 409 `AP-PRP-001`.

`GET /v1/payables/payment-proposals/{id}` (duty `payment-proposal.read`) returns the proposal with groups and lines. `POST /v1/payables/payment-proposals/{id}:approve` (duty `payment-proposal.approve`) moves a DRAFT to APPROVED and requires a different user than the creator (`AP-PRP-003`); the table also carries a check constraint so no code path can store a self-approval. Approval locks each line's open item and re-checks version, open amount, hold, vendor block and status; a changed line is a 409 `AP-PRP-001` naming the items, and a run date before today (UTC) is a 409 `AP-PRP-004`, so an expired discount can never be approved. Creator and approver both need an identified user. The application role may update only the status and the approval and cancellation columns of a proposal, and a trigger allows only the transitions DRAFT to APPROVED or CANCELLED, APPROVED to EXECUTING, and EXECUTING to EXECUTED or FAILED (migration 0007), and a line is released only by cancelling or failing its proposal.

Payment holds and vendor blocks are read here and written by `store.SetPaymentHold`, `store.BlockVendor` and `store.UnblockVendor`; there is no HTTP route for them yet. Neither a hold nor a block bumps the item version (the version moves only with the open amount); a held or blocked item is refused at approval and at run start because both re-read `payment_hold` and `ap_vendor_block`, and a hold that is released again leaves the proposal valid. Audit of every reader of `ap_open_item.version` (#317): proposal creation (`insertGroups`) and `verifyLines` compare `version` and test `payment_hold` and `ap_vendor_block` separately, so neither relies on a hold bumping the version; `settleTx` (and so `:settle`) uses it only as the optimistic lock on the amount and deliberately ignores a hold placed after the journal posted; the version is not in the `payables.openitem.*` event payloads and nothing outside this service reads it (the web app and workflow-ts never call the open-item routes). No consumer breaks. The approved proposal is the input of the payment run below; withholding tax is not applied.

Metrics: `payables_payment_proposals_total{outcome}` (created, replayed, approved, self_approval_refused, cancelled) and `payables_fx_rate_faults_total`.

## Payment runs (#102)

An APPROVED proposal is executed by a payment run, orchestrated by the Temporal `paymentRunWorkflow` in `services/workflow-ts` (retries, crash recovery) over idempotent payables endpoints. Duties: `payment-run.execute` (start, the step endpoints and `GET .../{id}/file`; separate from `payment-proposal.approve`) and `payment-run.read` (`GET /v1/payables/payment-runs/{id}`). Every call names a legal entity (fail-closed scope) and every table has tenant and legal-entity RLS.

Who decides what a run pays and where:

- Payee bank details are part of the proposal: `creditors` (vendor id to name, address, iban or otherId, bic or routingNumber) is supplied at proposal creation, is part of the request hash (a replay with other details is a 409), is stored encrypted in `ap_payment_proposal_payee` (`platform/go/fieldcrypt`, AES-256-GCM, a data key per tenant, tenant and vendor bound as additional data, key id in the value for rotation; the application role can insert and read but never update or delete) and the approver sees them as `payees`: name, bank identifier (BIC or routing number) in full and the account number masked to its last four characters. The payee ciphertext is bound to tenant, proposal and vendor, and payee rows can only be inserted while the proposal is a DRAFT. Run start never accepts bank details and rejects any unknown field.
- The ledger, the bank, control and discount accounts and the paying bank account come from the legal entity's posting profile (`PAYABLES_POSTING_PROFILES`), never from a request. The profile is snapshotted onto the run at start (accounts in columns, the paying bank file settings sealed with fieldcrypt) and every later step uses the snapshot. The generated pain.001 is also stored sealed (tenant, run id and field bound), because it carries the payees' full account numbers.
- The executor must be neither the creator nor the approver of the proposal, compared case- and whitespace-insensitively, at start (API 403 `AP-PRP-003` and a trigger on `ap_payment_run`) and on every step and `GET .../file`. `GET .../file` carries full bank details, so it needs the execute duty and every read logs `audit: payment file read` and counts in `payables_payment_file_reads_total`.
- Only groups in the legal-entity currency run, TRANSFER or WIRE only; foreign-currency groups are a 422 until realized FX gain or loss is built.

Steps:

- `POST /v1/payables/payment-runs` `{legalEntityId, proposalId}` starts the run: idempotent per (legal entity, proposal) with `Idempotent-Replayed` on 200 (also after the run date; a new start needs `runDate >= today`, else 409 `AP-PRP-004`). It locks the proposal, moves APPROVED to EXECUTING and re-verifies every line. A changed line (hold, vendor block, version, amount) makes the run FAILED, the proposal FAILED and releases its lines in the same transaction.
- `:generate-file` re-verifies the lines (a hold placed since start is a 409 `AP-RUN-015`, and the workflow then fails the run before any journal), builds one pain.001.001.09 payment per group (end-to-end id `P-<group id>`, remittance = the invoice numbers), validates the XSD and stores the file, its sha256 and one `ap_payment` row per group (a group is paid once, ever). Terminal 422 on a bank or XSD failure.
- `:post-journal` first records `journal_requested_at` under the run lock, then calls the ledger through `POST /v1/ledger/journal-entries` with `Idempotency-Key: payables:payment-run:<run id>` and a service token carrying only `ledger.journal.post` for the run's legal entity: debit the control account per group at gross valued amounts, credit the bank account the paid valued total and the discount account the difference. A reply lost after the ledger committed is replayed by the ledger's idempotency; a 409 `LEDGER_IDEMPOTENCY_IN_PROGRESS` (a slow first post still running) is a retryable 502, never a failure. Only a ledger 400 or 422 is a definitive business rejection: it is recorded (`journal_rejected_at`), answers 422 `AP-RUN-014`, and the journal can never be requested again. Every other answer (401, 403, 404, 408, 429, a 409 of either kind, 5xx, a transport error) is a retryable 502 that records nothing, because the first post may have committed with its reply lost. Posting needs a signed ledger client: with `LEDGER_URL` but no signing key payment runs are disabled at startup with a warning.
- `:fail` (reason required, at most 500 bytes) marks run and proposal FAILED and releases the lines. It is refused (409 `AP-RUN-011`) once the journal was requested, unless the ledger definitively rejected it and nothing posted; the proposal and run triggers enforce the same, and the journal columns cannot be cleared once set. `journal_rejected_at` is frozen the same way (migration 0008): it can be set only while `journal_requested_at` is set and `journal_posted_at` is not, a check constraint requires a requested journal, and the application role cannot clear or move it, so it cannot be used to unlock `:fail`. The executor check (neither creator nor approver) also applies to a replayed start: a creator or approver replaying the start gets 403 `AP-PRP-003`, not the run. Each step's executor check reads only the proposal's creator and approver, never the encrypted payee details; a proposal read, file or payee call without field keys is a 503 `COM-0503`.
- `:settle` settles every unsettled proposal line in one transaction (open amount reduced, PARTIALLY_SETTLED or SETTLED, version bumped, `payables.openitem.settled` event with the run id as settlement id). `:complete` marks the run COMPLETED and the proposal EXECUTED.

Each step locks the run row, so concurrent duplicates serialise; a step already done returns the run unchanged. Steps must run in order (409 `AP-RUN-012`). The workflow retries `generate-file` five times and then fails the run; `post-journal`, `settle` and `complete` retry for 24 hours while the failure is retryable (a payables 401 or authorization 403 is retryable, a business 403 is not). When those retries end after the journal was requested the workflow counts `passage_payment_run_stuck` (Temporal SDK metric) and fails visibly. The workflow authenticates with a short-lived Ed25519 service token (#303) whose only duty is `payment-run.execute` and whose actor is the user who started the run.

Metrics: `payables_payment_runs_total{outcome}` (started, replayed, completed, failed), `payables_payment_run_ledger_faults_total`, `payables_payment_file_reads_total` and `payables_payment_run_stuck_total`.

Alert: page on `increase(payables_payment_run_stuck_total[5m]) > 0`. It counts a settle step that found an open item changed after the journal posted: the money is posted and nothing is settled, the run stays RUNNING and the structured error log `payment run stuck` carries the run id. Resolve by hand: inspect the named item, then either reverse the journal in the ledger (the run cannot do that yet) or settle the item against the run, and complete the run.
