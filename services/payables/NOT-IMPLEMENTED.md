# Not implemented in PSG-19 slices 1 to 3

Slice A ships the settlement engine, the preview endpoint, the service skeleton and the web module stub. Slice 3a (PSG-1903) adds the positive pay CSV generator and the payment and prenote state machines as pure domain code. Slice 4a (PSG-1904) adds the pain.001.001.09 generator and the `AP-FIL-002` error type with its `xmllint` line helper; the vendored XSDs, fixtures, conformance test and pain.002 parser follow in slice 4b.

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC1: 300 settlement scenarios | built | `TestSettle_Grid300` checks 300 generated scenarios against an exact-rational oracle; no accountant-reviewed fixtures exist |
| AC2: pain.001 XSD validation | slice 4b | the generator exists; the vendored XSD, fixtures and `xmllint` contract test are not yet delivered |
| AC3: aging reconciles to AP control | slice C | needs migrations, a store and the PSG-18 vendor projection |
| AC4: idempotent payment run (Temporal) | slice D | needs the run workflow, a store and a Temporal SDK decision |
| Deliverable 1: store, commit, aging, withholding | partial | preview computes settlement; persistence is slice C; withholding engine is wave 13 |
| Deliverable 2, Deliverables 5 to 8, Deliverable 9: proposals, holds, approvals, prepayments, reversal, prenotes, calendars, confirmations, installments, mandates, bank account change | slice C and D | need the store, the wave 4 workflow and wave 7 calendars and terms |
| Deliverable 3 item 1 and AC1: IBAN, BIC, ABA validators, `PaymentMethod`, `BankProfile` | built | `internal/domain/bank.go`; `CARD` is accepted as a type with no media format; domain codes `AP-RUN-002` and `AP-FIL-001` are not yet mapped to HTTP |
| AC3: charset transliteration (`Transliterate`) | built | `internal/domain/translit.go`, hand table; unmapped letters reject with `AP-FIL-001` instead of being dropped |
| pain.001.001.09 generator (`GeneratePain001`), `AP-FIL-002` `XSDError` and `ParseXmllintLine` | built | `internal/domain/pain001.go` and `xsderror.go`; pure functions, checked structurally by parse-back tests and not yet reachable over HTTP |
| XSD vendoring (pain.001 and pain.002), pain.001 fixtures and the `xmllint` contract test | slice 4b | the ISO message archive was not reachable when slice 4a was built; CI image `xmllint` availability is unconfirmed and CI configuration is outside the owned paths |
| Positive pay CSV generator (`GeneratePositivePay`, account masking) | built | `internal/domain/positivepay.go`; pure function, not yet reachable over HTTP |
| NACHA ACH generator with prenotes | slice 3b | ticket item 3 |
| Payment (BR-19.1) and prenote (BR-19.13) state machines (`TransitionPayment`, `TransitionPrenote`) | built | `internal/domain/status.go`; pure functions, not yet reachable over HTTP |
| pain.002 status parsing (AC01 `BANK_DETAILS` hold, AP-STS-001, AP-STS-002) and its fixtures | slice 4b | ticket item 4 |
| NACHA return parsing | slice 3d | ticket item 5 |
| `POST /v1/payables/payment-files:preview`, `POST /v1/payables/payments/status:parse`, OpenAPI, `problemSlugs` and status mapping, metrics | slice 3e | ticket item 6 |
| Check PDF with MICR (E-13B) line, check lots, amount in words | deferred | needs a PDF renderer decision and check stock layouts |
| Payment advice PDF and email or portal delivery (FR-19.12) | deferred | needs wave 15 templates and the wave 20 portal |
| Bank test harness ACCP for 50 SEPA payments | external | needs a sandbox bank |
| `pain001Version` `001.001.03` profile override | deferred | rejected with "not supported in this slice" |
| Card provider adapter | deferred | PRD open question 3: no provider adapter until v1.x |
| Object-store `passage-payment-files` bucket and `GET /payment-files/{id}/content` | deferred | needs the store slice and a wave 1 deploy handoff |
| `POST /payments/status:ingest` persistence and DLQ | deferred | needs the store slice |
| Ledger posting via `ledger.Post` | slice D | key `post:payables:settlement:<id>:settle`; the event context name (`payables` vs `procurement`) must be settled first |
| Tables, RLS, outbox events, Idempotency-Key | slice C | the preview writes nothing |
| JWT validation, step-up MFA, OPA | wave 2 | the gateway validates tokens; services trust tenant headers |
| `serviceCode` on 401 and 403 bodies | kernel handoff | tenant and authz middleware live in `platform/go` |
| Argo CD application, root `Makefile` wiring | handoff | `deploy/` belongs to wave 1 |
| Web workspace screens | slice C | the module is a navigation stub |
