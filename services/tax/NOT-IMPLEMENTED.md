# Not implemented in PSG-13 slice A

Slice A ships the tax engine, provider failover, settlement reconciliation, the stateless calculate endpoint, the service skeleton and the web module stub.

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC1: tax matrix of 500 scenarios | built | `TestMatrix500` checks 500 generated scenarios (US 200, EU 180, IN 70, CA 50) against an exact-rational oracle; community-sourced, accountant-reviewed fixtures do not exist in the tree and the named accountant is an open architecture item |
| AC2: settlement reconciles to the GL tax accounts | partial | `Settle` reconciles to the cent against the `GLMovement` interface (tested with a fake); the real GL arrives with wave 11 `services/ledger/` |
| AC3: provider failover | partial | `CalculateWithFailover` falls back within 2 s and flags the document (TAX-0007); the `tax.provider.fallback.v1` event and recommit queue need the store and outbox (slice B) |
| AC4: SAF-T export validates against the OECD XSD | slice B | needs the vendored OECD XSD, an XSD validator dependency with README justification, and ledger and party data |
| AC6: contracts and chart | partial | `api/openapi.yaml` covers the calculate endpoint; AsyncAPI, Helm chart and Dockerfile are slice B (no events or deployment artefacts yet) |
| Deliverable 1: determination model | partial | groups x item groups, effective-dated rates, exemption certificates and brackets are in the engine; the jurisdiction hierarchy and persistence are slice B |
| Deliverable 2: calculation | built | inclusive and exclusive, compound, rounding, reverse charge, use tax, partial recoverability |
| Deliverable 3: withholding tax and reporting hooks | slice B | needs the store and the BR-13.8 withholding engine |
| Deliverable 4: provider adapters | partial | `TaxProvider` interface and the internal engine as default; Avalara and Vertex adapters need Vault credentials and the store |
| Deliverable 5: settlement job, VAT return, ESL, Intrastat and SAF-T extracts | partial | settlement domain only; the job, extracts and statutory formats need the store and ledger |
| Deliverables 6, 7, 8: Intrastat master data and journal, VAT recovery policies, withholding remittance runs | slice B | need the store, migrations and numbering for gap-free certificates |
| Duty-gated line override of the tax code (BR-13.1 step 5, TAX-0010) | slice B | needs the OPA duty `Override tax code` |
| `posting_lines` in the calculate response | slice B | the wave 11 line objects do not exist yet |
| Tables, RLS, outbox events, Idempotency-Key | slice B | the calculate endpoint and settlement domain write nothing |
| Ledger posting via `ledger.Post` | slice B | key `post:tax:settlement:<id>:settle`; `Poster` interface declared, no client |
| JWT validation, OPA | wave 2 | the gateway validates tokens; services trust tenant headers |
| `serviceCode` on 401 and 403 bodies | kernel handoff | tenant and authz middleware live in `platform/go` |
| Argo CD application, root `Makefile` and CI wiring | handoff | `deploy/` and `.github/` belong to wave 1 |
| Web workspace screens | slice B | the module is a navigation stub |
