# Not implemented in PSG-19 slice A

Slice A delivers the settlement engine, the preview endpoint, the service skeleton and the web module stub. Everything else is listed here with its reason.

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC1: 300 settlement scenarios | built | `TestSettle_Grid300` checks 300 generated scenarios against an exact-rational oracle; the accountant-reviewed fixture set under `testdata/` is not available |
| AC2: pain.001 validates against the ISO 20022 XSD and bank harness | slice B of PSG-19 | line budget; needs a generator, a vendored XSD and a validator choice (no XSD validator in the standard library) |
| AC3: aging reconciles to the AP control account per vendor group | slice C of PSG-19 | line budget; needs migrations, a store and the vendor transaction projection from PSG-18 |
| AC4: idempotent payment run (Temporal) | slice D of PSG-19 | line budget; needs the run workflow, a store and a Temporal SDK decision (no Temporal package in the tree) |
| Deliverable 1: open-item store, settlement commit, vendor aging | partial | preview computes settlement; persistence, commit and aging are slice C |
| Deliverable 1: withholding on applied amount (FR-19.2 step 7) | wave 13 | the withholding engine is not in the tree; consume through an interface when it lands |
| Deliverable 2: payment proposal, holds, approval workflow | slice C and D | needs the store and the wave 4 workflow |
| Deliverable 3: ACH/SEPA pain.001, check printing, card, wire, positive pay | slice B | line budget |
| Deliverable 4: payment advice, pain.002 ingestion | slice B | needs payment files and the mailer and object store interfaces |
| Deliverable 5: prepayments, credit notes, reversal | slice C | needs the store |
| Deliverable 6: ACH prenotes, payment calendars | slice C | needs the store and the wave 7 calendars |
| Deliverable 7: balance confirmations, AP correspondence | slice C | needs the store |
| Deliverable 8: installments, noted items, guarantees, direct debit mandates, one-time payee | slice C | needs the store and wave 7 payment terms |
| Deliverable 9: bank account change control | slice C | needs the store, the workflow and party bank accounts |
| Ledger posting through `ledger.Post` | slice D | settlement commit posts with key `post:payables:settlement:<id>:settle`; the event context name (`payables` vs `procurement` in the compiled architecture) must be settled first |
| Tables, RLS policies, outbox events, Idempotency-Key | slice C | the preview writes nothing; these arrive with persistence |
| JWT validation (`aud=passage-api`, RS256), step-up MFA, OPA decisions | wave 2 | Envoy Gateway and Keycloak validate tokens; services trust the tenant headers behind the gateway |
| `serviceCode` on 401 and 403 problem bodies | kernel handoff | the tenant and authz middleware live in `platform/go`, which a wave may not edit |
| Argo CD application and root `Makefile` wiring | handoff | `deploy/` belongs to wave 1; the root `Makefile` discovers the module and chart automatically |
| Web workspace screens | slice C | the module is a navigation stub until the store and endpoints exist |
