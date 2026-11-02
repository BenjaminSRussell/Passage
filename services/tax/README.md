# tax

Wave 13 deployable: tax determination, calculation, provider failover and settlement reconciliation. It ships the pure tax engine, the stateless calculate and withholding endpoints, a small store (recorded tax transactions, settlement runs, the recommit queue, outbox), settlement reconciliation to the general ledger and a SAF-T tax extract. One Go module, one image (`tax`), one chart (`charts/tax`). Deferred deliverables are listed in `NOT-IMPLEMENTED.md`.

## Run and test

```
cd services/tax
go vet ./... && go test ./...
DATABASE_URL=postgres://... go run ./cmd/tax
```

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `HTTP_ADDR` | default `:8080` |
| `ADMIN_ADDR` | default `:9090`, serves `/metrics` only |
| `TAX_PROVIDER_TIMEOUT_MS` | external provider timeout before the internal engine takes over; default 2000 |
| `MIGRATE_ON_START` | `true` applies `migrations/` at start (the chart sets it from `migrateOnStart`) |
| `LEDGER_URL` | base URL of the ledger service; unset leaves `POST /v1/tax/settlements:run` answering 503 |
| `LEDGER_TIMEOUT` | ledger call timeout, default 3s |
| `PASSAGE_SERVICE_SIGNING_KEY` or `PASSAGE_SERVICE_SIGNING_KEY_FILE`, `PASSAGE_SERVICE_NAME` | the service identity the ledger call is signed with; required when `LEDGER_URL` is set. `service_grants.tax.ledger` in `policies/authz/data.json` (mirrored in `platform/go/tenant`) grants it `ledger.balance.read` and nothing else |

## API

`POST /v1/tax/calculate` (duty `Calculate tax`), `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port. The browser reaches it as `/api/v1/tax/calculate`; the BFF rewrites the prefix. Request and response shapes are in `internal/domain/model.go`; `api/openapi.yaml` documents every route with its duty and schemas, `events/asyncapi.yaml` the published event, and `TestOpenAPIMatchesRoutesAndDuties` fails when a route, its duty or the policy seed drifts from them.

### Settlement and the general ledger

`POST /v1/tax/transactions` (duty `tax.transaction.record`) records posted tax amounts (`id`, `code`, `direction`, `tax_date`, `amount`) for the request's single legal entity; an id is unique per legal entity; an identical replay is a counted duplicate and a replay with different content is 400 COM-0001. `POST /v1/tax/settlements:run` (duty `tax.settlement.run`) takes only `period_from` and `period_to`; the ledger and the map from `code|direction` (for example `VAT19|output`) to its GL tax account come from the legal entity's stored configuration, set with `PUT /v1/tax/ledger-config` (duty `tax.ledgerconfig.manage`, body `ledger_id` and `accounts`), and a request that names either is refused. No stored configuration is 422 COM-0503, and each run keeps the `ledger_id` and `accounts` it used. For every key it reads the account's movement over the period from the ledger (`GET /v1/ledger/balances` with `period_from` and `period_to`, signed with the service identity; output tax is the credit movement, input tax the debit movement), compares it with the recorded transactions of the period, settled or not, and refuses with TAX-0008 on any difference (the response names the code and direction, never the amounts), so a settlement reconciles to the cent or does not happen. The period must cover whole calendar months because the ledger's balances are monthly (otherwise COM-0001). Open transactions are then marked with the new run, version counted per period (a later run settles only the delta); nothing open is TAX-0009. Transactions are locked for the run, so two concurrent runs cannot settle the same row. A ledger that cannot be read is 502 COM-0502. Amounts are taken to be in the ledger's accounting currency. `Settle`, `GLMovement` and the fake used by the unit tests stay in `internal/domain`; `internal/ledger` is the production `GLMovement`.

### Provider fallback and recommit

When an external provider fails or times out, the internal engine answers with `fallback: true` and TAX-0007, the request is queued in `tax_recommit_queue` and `dev.passage.tax.provider.fallback.v1` goes through the outbox in the same transaction. A queue failure is logged and counted (`tax_recommit_queue_failures_total`) and never fails the calculation. A request without a single legal entity cannot be queued (the queue is row-level secured per legal entity). `POST /v1/tax/recommit:run?limit=` (duty `tax.recommit.run`, default 20, at most 50) re-calculates the oldest pending entries on their provider: an answer marks the entry done and keeps the result, a provider still down leaves it pending with the attempt counted, and after 5 attempts the entry is marked `failed` and is not taken again; entries with fewer attempts are taken first, so one stuck entry cannot block newer ones. The scheduler calls it per legal entity; it holds the batch's row locks while it calls the providers, so keep the limit small.

### SAF-T

`GET /v1/tax/saft?period_from=&period_to=&company_name=&registration_number=&country=&currency=` (duty `tax.saft.export`) returns an `urn:OECD:StandardAuditFile-Tax:2.00` document with the header and a tax table of per-code period totals. It validates against the schema in `testdata/saft/`, which is a documented subset and not the official OECD XSD (see `testdata/saft/SOURCE.md`); the test shells out to `xmllint` and skips when it is not installed.

The request carries the document and an inline `config` (tax groups, item groups, codes with effective-dated rates, exemption certificates) because no tax store exists yet. Order of operations follows PRD BR-13.1; every step writes to `determination_trace`. Amounts and percentages are decimal strings with at most 6 decimal places.

Rounding: `per_line` rounds each line's tax per code; `per_document` rounds the code total once and distributes it to lines by largest remainder. For price-inclusive lines the base is the gross less the rounded taxes, so base plus taxes always equals the gross and the document tax equals the rounded sum of unrounded taxes. Brackets on inclusive lines are evaluated on the gross.

### Withholding

`POST /v1/tax/withholding/calculate` (duty `Calculate withholding tax`) is stateless like calculate. Body: `currency`, `party_type` (`vendor` or `customer`), `payment_date`, `code`, `gross_amount` (positive), `paid_to_date` (cumulative paid to this party this year before this payment, default 0) and inline `codes` (`code`, `jurisdiction`, `income_type`, effective-dated `rates` with `rate_pct`, optional `threshold`). With a threshold, only the part of `paid_to_date + gross_amount` above it is withheld (550 paid, 100 invoice, threshold 600: base 50). `gross_amount` may not have more decimals than the currency's minor unit, and `codes` must be unique. Tax is rounded to the currency minor unit. Response: `taxable_base`, `withholding_amount`, `net_payable`, `rate_pct` and `determination_trace`. Errors: TAX-0001 (no rate on the date), TAX-0002 (rate outside 0 to 100 or overlapping rates), TAX-0003 (requested code not in `codes`), COM-0001. Payables calls it with a signed service token; `service_grants.payables.tax` in `policies/authz/data.json` (mirrored in `platform/go/tenant`) grants it the duty. Metric: `tax_withholding_calculations_total`.

Naming: the PRD calls the chart `passage-tax`; existing charts are named after the service, so the chart is `charts/tax`.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| TAX-0001 | 422 | no rate valid on the tax date |
| TAX-0002 | 422 | rate outside 0 to 100, or overlapping rates |
| TAX-0003 | 422 | party tax group or item tax group not resolvable |
| TAX-0007 | 422 | named provider is not configured; warning when the internal engine replaced a failed provider |
| TAX-0008 | 422 | settlement differs from the GL tax account movement |
| TAX-0009 | 422 | no open tax transaction in the settlement period |
| COM-0502 | 502 | the general ledger could not be read |
| COM-0503 | 503 | the store or the ledger is not configured |
| COM-0001 | 400 | malformed body, unknown field, wrong JSON type or invalid field |
| COM-0500 | 500 | internal error, detail hidden |
| AUTH-0001 | 401 | missing or malformed tenant (kernel header middleware) |
| AUTH-0003 | 403 | caller lacks the duty (kernel authz) |

Warnings in the response `warnings` array: TAX-0004 (no code shared by the groups), TAX-0005 (certificate not valid on the tax date), TAX-0006 (reverse charge ineligible), TAX-0007 (provider fallback). TAX-0008 is returned by the settlement domain when a run differs from the GL movement.

## Metrics

`tax_calculations_total`, `tax_calculation_failures_total`, `tax_withholding_calculations_total`, `tax_provider_fallbacks_total`, `tax_recommit_queue_failures_total`, `tax_recommits_completed_total`, `tax_settlement_runs_total`.

## Handoffs outside the owned paths

- `policies/authz/data.json`: grant the duty `Calculate tax` to the service roles that call the endpoint; the new duties (`tax.transaction.record`, `tax.settlement.run`, `tax.recommit.run`, `tax.saft.export`, `tax.ledgerconfig.manage`) are seeded and need role assignments.
- `deploy/argocd`, the CI matrix and root `lint-charts`: add the tax application now that the chart exists.
- The settlement domain posts through the `Poster` interface with key `post:tax:settlement:<id>:settle`; no client is wired because the clearing accounts of the posting are not specified.
