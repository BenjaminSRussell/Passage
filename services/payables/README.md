# payables

Wave 19 deployable: accounts payable settlement and payments. This slice ships the settlement engine and a stateless preview endpoint. One Go module, one image (`payables`). Deferred deliverables are listed in `NOT-IMPLEMENTED.md`.

## Run and test

```
cd services/payables
go vet ./... && go test ./...
DATABASE_URL=postgres://... go run ./cmd/payables
```

## Test data

`testdata/xsd/` holds the official pain.001.001.09 and pain.002.001.10 schemas, vendored verbatim (`SOURCE.md` records origin, date and sha256; never edit them). `testdata/pain001/` holds one JSON input and one `.golden.xml` per bank profile; `testdata/pain002/` holds hand-written status reports. Regenerate the pain.001 goldens with `go test ./internal/domain -run Golden -update`; never edit a golden by hand. The XSD contract tests need `xmllint` (libxml2-utils) on `PATH` and skip with an explicit message when it is absent.

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `HTTP_ADDR` | default `:8080` |
| `ADMIN_ADDR` | default `:9090`, serves `/metrics` only |

## API

`POST /v1/payables/settlements:preview` (duty `settlement.read`), `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port. Contract: `api/openapi.yaml`.

The preview settles one credit against open invoices per PRD FR-19.2: priority order, cash discount (pro-rated on partial settlement), realized FX gain or loss, write-off within the limit, and a balanced journal. Amounts, rates and percentages are decimal strings; rates come from the request until the wave 12 rate lookup exists. A cross-currency credit settles in the accounting currency, so its FX difference is zero by construction.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| PAY-0001 | 400 | invalid field: date, decimal string, currency, discount, duplicate or empty invoices |
| PAY-0002 | 422 | rate missing for a foreign currency, or not 1 for the accounting currency |
| AP-RUN-002 | n/a | bank identifier failed validation (`iban`, `bic` or `aba` named in the detail); domain code, not yet reachable over HTTP |
| AP-FIL-001 | n/a | payment name has a character outside the profile charset after transliteration; domain code, not yet reachable over HTTP |
| AP-FIL-002 | n/a | generated pain.001 failed XSD validation (`xmllint` line mapped by `ParseXmllintLine`); domain code, not yet reachable over HTTP |
| AP-STS-001 | n/a | bank rejected a payment in a pain.002 report; domain code, not yet reachable over HTTP |
| AP-STS-002 | n/a | pain.002 names a message or end-to-end id the caller does not know; domain code, not yet reachable over HTTP |
| COM-0001 | 400 | malformed body, unknown field or wrong JSON type |
| COM-0500 | 500 | internal error, detail hidden |
| AUTH-0001 | 401 | missing or malformed tenant (kernel header middleware) |
| AUTH-0003 | 403 | caller lacks the duty (kernel authz) |

Problem bodies from this service carry `code` (a stable slug such as `validation_failed`) and `serviceCode` (the table above).

## Metrics

`payables_settlement_previews_total`, `payables_settlement_preview_failures_total` (stdlib atomics rendered as text).
