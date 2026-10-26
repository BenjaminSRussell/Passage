# Not implemented in PSG-19 slices 1 to 5

Slice A ships the settlement engine, the preview endpoint, the service skeleton and the web module stub. Slice 3a (PSG-1903) adds the positive pay CSV generator and the payment and prenote state machines as pure domain code. Slice 4a (PSG-1904) adds the pain.001.001.09 generator and the `AP-FIL-002` error type with its `xmllint` line helper; slice 5 (PSG-1905) vendors the official pain.001.001.09 and pain.002.001.10 XSDs, adds the pain.001 fixtures with golden files, the `xmllint` contract and always-on structural tests, and the pure pain.002 status parser (`ParsePain002`) with its fixtures. Everything stays pure and stateless; the NACHA generator, NACHA return parsing and the HTTP endpoints are slice 6.

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC1: 300 settlement scenarios | built | `TestSettle_Grid300` checks 300 generated scenarios against an exact-rational oracle; no accountant-reviewed fixtures exist |
| AC2: pain.001 XSD validation | built | vendored `testdata/xsd/pain.001.001.09.xsd`, `TestPain001Fixtures_XSD` and `TestPain001_CorruptedFileFailsXSD` in `internal/domain/pain001_fixtures_test.go`; the external bank harness half (ACCP for 50 payments) is the external row below |
| AC3: aging reconciles to AP control | slice C | needs migrations, a store and the PSG-18 vendor projection |
| AC4: idempotent payment run (Temporal) | slice D | needs the run workflow, a store and a Temporal SDK decision |
| Deliverable 1: store, commit, aging, withholding | partial | preview computes settlement; persistence is slice C; withholding engine is wave 13 |
| Deliverable 2, Deliverables 5 to 8, Deliverable 9: proposals, holds, approvals, prepayments, reversal, prenotes, calendars, confirmations, installments, mandates, bank account change | slice C and D | need the store, the wave 4 workflow and wave 7 calendars and terms |
| Deliverable 3 item 1 and AC1: IBAN, BIC, ABA validators, `PaymentMethod`, `BankProfile` | built | `internal/domain/bank.go`; `CARD` is accepted as a type with no media format; domain codes `AP-RUN-002` and `AP-FIL-001` are not yet mapped to HTTP |
| AC3: charset transliteration (`Transliterate`) | built | `internal/domain/translit.go`, hand table; unmapped letters reject with `AP-FIL-001` instead of being dropped |
| pain.001.001.09 generator (`GeneratePain001`), `AP-FIL-002` `XSDError` and `ParseXmllintLine` | built | `internal/domain/pain001.go` and `xsderror.go`; pure functions, checked structurally by parse-back tests and not yet reachable over HTTP |
| XSD vendoring (pain.001 and pain.002), pain.001 fixtures and the `xmllint` contract test | built | `testdata/xsd/` (two XSDs and `SOURCE.md` with origin, date and sha256, checked by `TestVendoredXSD_SHA256`), `testdata/pain001/` (`sepa-50`, `sepa-structured`, `us`, `uk`, `ch`, `wire` with goldens, regenerated only with `-update`), `xsdcheck_test.go`, `pain001_fixtures_test.go` (golden, contract, structural, corrupted-file tests); contract tests skip without `xmllint` |
| `xmllint` in the CI image | built | the `go` job in `.github/workflows/ci.yml` installs `libxml2-utils` before `make test-go`, so the XSD contract tests run in CI; they still skip, not fail, where `xmllint` is absent locally |
| Positive pay CSV generator (`GeneratePositivePay`, account masking) | built | `internal/domain/positivepay.go`; pure function, not yet reachable over HTTP |
| NACHA ACH generator with prenotes (`GenerateNacha`, `ParseNachaInput`) | built | `internal/domain/nacha.go`, `nacha_test.go` (position-based validator, golden and reject tests) and `testdata/nacha/` (`ccd-10`, `ppd`, `ctx`, `prenote` with goldens, regenerated only with `-update`); pure function, not yet reachable over HTTP |
| Payment (BR-19.1) and prenote (BR-19.13) state machines (`TransitionPayment`, `TransitionPrenote`) | built | `internal/domain/status.go`; pure functions, not yet reachable over HTTP |
| pain.002 status parsing (AC01 `BANK_DETAILS` hold, AP-STS-001, AP-STS-002) and its fixtures | built | `internal/domain/pain002.go` (`ParsePain002`), `pain002_test.go` (table, negative, rapid property tests) and `testdata/pain002/` (ten fixtures validated against the vendored XSD); pure function, not yet reachable over HTTP |
| NACHA return and NOC parsing (R01-R85, C01-C13, prenote R02/R03/R04) | slice 6 | ticket item 5 |
| `POST /v1/payables/payment-files:preview`, `POST /v1/payables/payments/status:parse`, OpenAPI, `problemSlugs` and status mapping (including the HTTP mapping of `AP-FIL-002`, `AP-STS-001` and `AP-STS-002`), metrics | slice 6 | ticket item 6 |
| Check PDF with MICR (E-13B) line, check lots, amount in words | deferred | needs a PDF renderer decision and check stock layouts |
| Payment advice PDF and email or portal delivery (FR-19.12) | deferred | needs wave 15 templates and the wave 20 portal |
| Bank test harness ACCP for 50 SEPA payments | external | needs a sandbox bank |
| `pain001Version` `001.001.03` profile override | deferred | rejected with "not supported in this slice" |
| Card provider adapter | deferred | PRD open question 3: no provider adapter until v1.x |
| Object-store `passage-payment-files` bucket and `GET /payment-files/{id}/content` | deferred | needs the store slice and a wave 1 deploy handoff |
| `POST /payments/status:ingest` persistence and DLQ | deferred | needs the store slice |
| `VendorTransaction` creation from wave 18 posted invoices (FR-19.1); incoming handoff from `services/receiving/NOT-IMPLEMENTED.md` row "Deliverable 2: vendor invoice capture, OCR adapter, UBL/Peppol" | slice C | needs the payables store; the receiving row says the payables-side API belongs in `services/payables/` |
| Ledger posting via `ledger.Post` | slice D | key `post:payables:settlement:<id>:settle`; the event context name (`payables` vs `procurement`) must be settled first |
| Tables, RLS, outbox events, Idempotency-Key | slice C | the preview writes nothing |
| JWT validation, step-up MFA, OPA | wave 2 | the gateway validates tokens; services trust tenant headers |
| `serviceCode` on 401 and 403 bodies | kernel handoff | tenant and authz middleware live in `platform/go`; `api/openapi.yaml` documents the real bodies (`Unauthorized` as text/plain JSON, `Forbidden` as `KernelProblem`) |
| Argo CD application, root `Makefile` wiring | handoff | `deploy/` belongs to wave 1 |
| Web workspace screens | slice C | the module is a navigation stub |

## Line-cap note

PR #62 added 1768 lines against the 1500-line cap. The overshoot was accepted at merge: the excess is the 300-scenario oracle grid, the Helm chart, `api/openapi.yaml` and `go.sum`, none of which can be split from the slice without leaving the preview endpoint untestable, undeployable or undocumented. No placeholders or essay blocks were flagged. Issue #72 closes the deferred review findings and stays well under the cap.
