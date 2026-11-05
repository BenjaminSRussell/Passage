# carrier-gateway

The service between Passage and the carriers (wave 28 AC4, #482; parent #28): rate shopping, label purchase and
tracking behind one carrier adapter interface. fulfillment and transport call it with signed service tokens; there
is no person-facing route.

## Layout

| Path | What |
| --- | --- |
| `src/adapter.ts` | `CarrierAdapter` (`services`, `rate`, `label`, `track`) and `CarrierError` (`UNKNOWN_SERVICE`, `NOT_SERVICEABLE`, `NOT_FOUND`, `UNAVAILABLE`) |
| `src/sandbox.ts` | The sandbox carrier: deterministic, no credentials, no network |
| `src/gateway.ts` | Rate shopping across adapters, idempotent label purchase (`LabelStore`), tracking |
| `src/serviceToken.ts` | Service token verifier, the same checks as `platform/go/tenant` `ServiceVerifier` |
| `src/server.ts`, `src/main.ts` | The `/v1/carrier` HTTP API (`api/openapi.yaml`) |
| `test/contract.ts` | The adapter contract: invariants every adapter keeps, replayed against recorded fixtures |
| `testdata/sandbox-recorded.json` | The sandbox's recorded interactions (rates, labels, tracking, refusals) |
| `testdata/go-minted-token.json` | A token minted by the Go signer, which the TS verifier must accept |
| `charts/carrier-gateway` | Helm chart (one replica, ingress from fulfillment and transport only, DNS-only egress) |

## API

| Route | Duty | Callers |
| --- | --- | --- |
| `GET /v1/carrier/carriers` | `carrier.rate.quote` | fulfillment, transport |
| `POST /v1/carrier/rates` | `carrier.rate.quote` | fulfillment, transport |
| `POST /v1/carrier/labels` (Idempotency-Key) | `carrier.label.create` | fulfillment |
| `GET /v1/carrier/tracking/{carrier}/{trackingNumber}` | `carrier.tracking.read` | fulfillment, transport |

The three duties are service-only in `policies/authz/data.json`. The grants are in `service_grants` there and in
`serviceGrants` in `platform/go/tenant/service.go`. `test/grants.test.ts` keeps both, `src/serviceToken.ts` and
`api/openapi.yaml` equal.

A token is accepted when it is signed by a key in `PASSAGE_SERVICE_PUBLIC_KEYS`, is addressed to `carrier-gateway`,
lives at most 60 s (5 s skew), names a canonical tenant and legal entity, carries only duties the issuer's grant
holds, and its id is new. A `service:` actor is refused: no background principal holds a carrier duty.

Rates are sorted cheapest first, then fastest. A carrier that refuses or is down shows up in `failures`, and the
other carriers still answer. A label is bought once per Idempotency-Key within the token's tenant, legal entity and
issuer. A repeat with the same request returns the stored label (200, `created: false`). A repeat with a different
request is refused with 422 CGW-0422.

## Sandbox carrier

- Services: `GROUND` costs 6.50 + 0.85/kg (3 days domestic, 6 international). `EXPRESS` costs 14.00 + 2.10/kg (1 day, 3 days). International adds 12.00. All prices are in USD and round up to the cent.
- Billable weight: the larger of actual and volumetric weight (cm³ / 5000) per parcel, rounded up to 100 g.
- It serves US, CA, MX and 18 European countries. Any other country is `NOT_SERVICEABLE`.
- The tracking number is `SBX`, then the service digit, the exception digit, the transit days, the label time (10 digits, Unix s), 4 digits of SHA-256 of the key, and a GS1 mod-10 check digit. Tracking is computed from the number and the clock, so it needs no store and survives a restart: LABELLED at the label time, IN_TRANSIT 4 h later, then DELIVERED after the transit days. A ship-to postal code of `00000` gives EXCEPTION after 24 h instead.
- The label is ZPL with a Code 128 barcode, printed "SANDBOX, NOT FOR SHIPPING".

## Run and test

```sh
npm ci
npm run typecheck
npm test
RECORD_FIXTURES=1 npx vitest --run test/record.test.ts   # re-record testdata/sandbox-recorded.json (review the diff)
PASSAGE_SERVICE_PUBLIC_KEYS=fulfillment=<base64> npm start  # HTTP_ADDR defaults to :8080, CARRIER_ADAPTERS to sandbox
```

`CARRIER_GATEWAY_WRITE_VECTOR=1 go test ./internal/carrier/` in `services/fulfillment` re-mints
`testdata/go-minted-token.json`.

For the sandbox, the recorded fixture is a golden file: it catches any change in behaviour that was not meant. A
real adapter's fixture is recorded from the carrier's test environment (NOT-IMPLEMENTED.md).
