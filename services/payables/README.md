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

## API

`POST /v1/payables/settlements:preview` (duty `settlement.read`), `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port. Contract: `api/openapi.yaml`.

`POST /v1/payables/payment-files:preview` (duty `payment-file.preview`) builds a pain.001.001.09 file from a JSON request (same shape as `testdata/pain001/*.json`; `created` defaults to the server clock; at most 2000 payments), validates it against the vendored XSD and returns `content`, a `summary` (`paymentCount`, `controlSum`, `sha256`) and `validation`. `POST /v1/payables/payments/status:parse` (duty `payment-status.parse`) parses a pain.002.001.10 report (`content`) against the caller-supplied `messages` and `payments` into outcomes and unknown references. Both are stateless and persist nothing; the status parse does not replace the PRD's stateful `POST /payments/status:ingest`.

XSD validation runs `xmllint` (libxml2-utils) as a subprocess with `--nonet`, a minimal environment, a 10 second timeout and at most 3 concurrent runs. Only xmllint exit code 3 counts as an invalid document; any other failure is a 500. The service validates a known-good file at startup and refuses to start without `xmllint` on `PATH`; the Docker image installs it.

The preview settles one credit against open invoices per PRD FR-19.2: priority order, cash discount (pro-rated on partial settlement), realized FX gain or loss, write-off within the limit, and a balanced journal. Amounts, rates and percentages are decimal strings; rates come from the request until the wave 12 rate lookup exists. A cross-currency credit settles in the accounting currency, so its FX difference is zero by construction.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| PAY-0001 | 400 | invalid field: date, decimal string, currency, discount, duplicate or empty invoices |
| PAY-0002 | 422 | rate missing for a foreign currency, or not 1 for the accounting currency |
| AP-RUN-002 | 422 | bank identifier failed validation (`iban`, `bic` or `aba` named in the detail) |
| AP-FIL-001 | 422 | payment name has a character outside the profile charset after transliteration |
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

Configuration: `AP_CONTROL_ACCOUNT` (default `2000`) and `LEDGER_URL` (unset: reconcile answers 503). The ledger call forwards the caller's tenant, legal-entity, user and duty headers, so the caller needs `ledger.balance.read`; a service identity (#226) will replace the forwarding. A ledger reports one currency, so other currencies are listed unreconciled. Reconcile at a period end: the ledger side is cumulative to the period, the open-item side is the current balance.
