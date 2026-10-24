# Not implemented in PSG-19 slice A

Slice A ships the settlement engine, the preview endpoint, the service skeleton and the web module stub.

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC1: 300 settlement scenarios | built | `TestSettle_Grid300` checks 300 generated scenarios against an exact-rational oracle; no accountant-reviewed fixtures exist |
| AC2: pain.001 XSD validation | slice B | line budget; needs a generator, vendored XSD and validator |
| AC3: aging reconciles to AP control | slice C | needs migrations, a store and the PSG-18 vendor projection |
| AC4: idempotent payment run (Temporal) | slice D | needs the run workflow, a store and a Temporal SDK decision |
| Deliverable 1: store, commit, aging, withholding | partial | preview computes settlement; persistence is slice C; withholding engine is wave 13 |
| Deliverable 2, Deliverables 5 to 8, Deliverable 9: proposals, holds, approvals, prepayments, reversal, prenotes, calendars, confirmations, installments, mandates, bank account change | slice C and D | need the store, the wave 4 workflow and wave 7 calendars and terms |
| Deliverables 3, 4: ACH/SEPA pain.001, checks, card, wire, positive pay, advice, pain.002 | slice B | line budget |
| Ledger posting via `ledger.Post` | slice D | key `post:payables:settlement:<id>:settle`; the event context name (`payables` vs `procurement`) must be settled first |
| Tables, RLS, outbox events, Idempotency-Key | slice C | the preview writes nothing |
| JWT validation, step-up MFA, OPA | wave 2 | the gateway validates tokens; services trust tenant headers |
| `serviceCode` on 401 and 403 bodies | kernel handoff | tenant and authz middleware live in `platform/go` |
| Argo CD application, root `Makefile` wiring | handoff | `deploy/` belongs to wave 1 |
| Web workspace screens | slice C | the module is a navigation stub |
