# NOT-IMPLEMENTED — billing (PSG-29, wave 29, slice 1)

Slice 1 builds free-text invoice proposal, posting, read and list, the totals and
tax-recheck rules, the schema and event contract, and the billing web screens
(`apps/web/src/modules/billing`). Everything below is outstanding. Status is `later slice of PSG-29` (same service, later build) or
`handoff` (needs another owner, outside this wave's paths).

| # | Ticket item / acceptance criterion | Status | Reason |
|---|---|---|---|
| 1 | D1 Invoice from shipments (proposals from posted packing slips, one invoice per order, FR-29.1) | later slice of PSG-29 | Needs the packing-slip event contract (wave 28); no inbound consumer exists yet |
| 2 | D1 Consolidated invoices (FR-29.2, BR-29.5 consolidation keys, 2000-line split) | later slice of PSG-29 | Depends on the proposal builder from item 1 |
| 3 | D1 Credit notes (FR-29.5, BR-29.6 partial recognition reversal) | later slice of PSG-29 | `CanReverse` domain rule exists; credit-note document flow and revrec reversal not built |
| 4 | D1 Invoice layouts: PDF and UBL 2.1 JSON, `GET .../document?format=` (FR-29.11) | handoff | Document rendering is wave 5 (docgen) and UBL transmission wave 35; this service needs the docgen client |
| 5 | D2 Revenue recognition (contracts, obligations, schedules, deferred postings on invoice post) | later slice of PSG-29 (contracts) + handoff (ledger postings to 2400/4000) | Revenue contracts live in `services/revrec`; posting to the ledger needs the ledger.Post client (wave 11/12) |
| 6 | D3 Subscription billing (recurring schedules, proration on invoice, usage lines, daily job 03:00, FR-29.10) | later slice of PSG-29 | Proration domain function exists; subscription aggregate, job and usage ingestion not built |
| 7 | D4 Intercompany invoicing hook (`intercompany=true`) | later slice of PSG-29 (hook is present, always false) | Intercompany parties arrive in wave 40 |
| 8 | D5 Billing audit trace `GET /invoices/{id}/trace` (FR-29.12) | later slice of PSG-29 | Needs packing-slip, order-line and price-explain references from waves 26 and 28 |
| 9 | D6 Subscription groups, fee categories, period templates, indexation (FR-29.14, BR-29.12) | later slice of PSG-29 | Depends on item 6 |
| 10 | D6 Expense deferral schedules (FR-29.15, BR-29.13, 1300/1310 split) | handoff | Feeds from wave 19 AP invoice lines; the schedule runs belong to revrec, the AP input to wave 19 |
| 11 | D7 Customer self-billing statement reconciliation (FR-29.16, BR-29.14) | later slice of PSG-29 | Needs statement import (SFTP and EDI 820 from wave 35) |
| 12 | D7 Bundle (solution) invoice lines (FR-29.17, BR-29.1 allocation at invoice) | later slice of PSG-29 | Needs bundle definition (wave 8) and revrec contracts (item 5) |
| 13 | D7 Installment terms split into open items at post (FR-29.18, BR-29.11) | later slice of PSG-29 | `SplitInstallments` domain function is built and tested; wiring into post and receivables open items is not |
| 14 | D8 Document delivery routing and delivery log, resend (FR-29.19, BR-29.15) | handoff | Delivery channels belong to the wave 5 print service, wave 35 EDI and wave 46 routing; this service only needs to emit the event |
| 15 | §5.29.1 `invoice.posted` consumers: receivables open item, ledger journal, portal | handoff | Receivables (wave 30) and ledger (wave 11) consume the event; the journal is written by ledger.Post, not built here |
| 16 | §5.29.5 post validation: closed AR/GL period (BIL-0003) | later slice of PSG-29 | Needs the period-control client (wave 14); posting date is today-truncated with no period check |
| 17 | §5.29.5 `:post` batch (multi-select ≤ 500 invoices with per-row results, `BILLING_BATCH_MAX`) | later slice of PSG-29 | Single post only. A retried single post is idempotent by state (the PRD's key = invoice id) and an optional `If-Match` guards the reviewed version (412 BIL-0103) |
| 18 | BIL-0005 (credit beyond original), BIL-0006 (invoice date before slip date), BIL-0007 (auto split), BIL-0009 (SSP missing warning), BIL-0010 (late usage), BIL-0012 (index missing), BIL-0013/0014 (self-billing), BIL-0015 (EDI fallback), BIL-0016 (delivery failure), BIL-0017 (deferral term) | later slice of PSG-29 | Each belongs to an item above |
| 19 | AC-29.1 property test over 1,000 random invoices | partially met | Rapid property test covers USD, EUR and JPY at the default iteration count, not a fixed 1,000-invoice fixture (`invoices-random-1k`) |
| 20 | AC-29.7 (subscription first bill 206.90 EUR), AC-29.9 (DMF-DE free text 19% VAT), AC-29.10 (installments), AC-29.14 (delivery), AC-29.15 (bundle) | partially met / later | AC-29.9 totals (100.00 + 19.00 = 119.00 on account 4900) are tested through the API with the rate-table engine; the Dr 1200 / Cr 4900 / Cr 2300 journal waits on item 15. `Prorate` and `SplitInstallments` reproduce AC-29.7 and AC-29.10 in unit tests; wiring waits on items 6 and 13; bundle on item 12; delivery on item 14 |
| 21 | AC-29.8 tax engine down: 503 `TAX_UNAVAILABLE`, no journal, no open item | met with a different code | Returns 503 `BIL-0001` (the PRD message catalog code); no stored proposal and no post on outage |
| 22 | §5.29.9 fixtures: `invoices-random-1k`, `subscription-edges-25`, `consolidation-12`, `credit-notes-15` under `testdata/` | later slice of PSG-29 | Fixtures depend on items 2, 3 and 6 |
| 23 | §5.29.9 Pact contract with fulfillment and receivables | handoff | Needs the fulfillment (wave 28) and receivables (wave 30) provider stubs owned by those waves |
| 24 | §5.29.9 posting saga integration test (billing ↔ tax ↔ ledger ↔ receivables) with injected failures | handoff | Needs the live tax (wave 13) and ledger (wave 11) services; slice 1 covers the tax-unavailable and tax-moved failures in unit tests |
| 25 | §5.29.10 feature flags `billing.consolidation`, `billing.subscriptions`, `billing.einvoice` | later slice of PSG-29 | No flag client in the platform kernel yet; flags land with the features |
| 26 | §5.29.10 `subscription billing` and recognition job schedules, roll-forward check 02:30 | later slice of PSG-29 | Jobs need items 6 and 5 |
| 27 | §5.29.10 SLOs and alert rules (post p95 < 3 s, failed posts > 1% in 15 min) | handoff | Counters are served on the admin port (`/metrics`, 9090) and the chart has a ServiceMonitor toggle; latency histograms and PrometheusRule alerts belong to the observability stack (wave 0) |
| 28 | §5.29.10 docgen ConfigMap per entity | handoff | Owned by wave 5 docgen |
| 29 | §5.29.13 parameters: `summary_update_default`, `consolidation_cycle_default`, `split_by_address`, `invoice_date_source`, `invoice_number_scope`, `tax_rounding_level`, `usage_late_days`, `subscription_billing_time`, `document_delivery_default`, `document_resend_retries`, `einvoice_enabled` | later slice of PSG-29 | Parameters are read from customer and legal-entity setup (wave 7 / wave 11); only `BILLING_MAX_LINES` and the tax tolerance are in code |
| 30 | §5.29.13 `tax_rounding_level` LINE/DOCUMENT per country pack | handoff | Country packs are wave 48; slice 1 rounds per line |
| 31 | §5.29.14 inquiries and reports (billing due list, invoice register, credit notes analysis) | later slice of PSG-29 | Need the packing-slip and credit-note data first |
| 32 | §5.29.15 documents: PDF, UBL, self-billing and revenue summary | later slice of PSG-29 / handoff | Rendering is docgen (wave 5); e-invoice transmission is wave 35 |
| 33 | §5.29.11 migration from AX / SAP (CustInvoiceJour, VBRK, open items) | later slice of PSG-29 | Migration pack lives in the migration framework (`integration` DB) |
| 34 | §5.29.20 PDF/A-3 and retention (10-year immutability) | handoff | Archive is wave 48; this slice keeps posted invoices immutable at the application layer only |
| 35 | FR-29.6 tax engine client (wave 13 `POST /v1/tax/calculate`) | handoff | The tax service needs the caller to send its tax configuration, which billing does not hold. Until a determination client exists, `BILLING_TAX_RATES` (explicit percent per tax code) is the interim engine; unset, every quote fails closed with BIL-0001, and an unknown tax code is refused with BIL-0102, never taxed at zero |
| 36 | FR-29.4 free-text dimensions and recurring templates | later slice of PSG-29 | Lines carry account, amount and tax code; financial dimensions (wave 11) and templates are not stored yet |
| 37 | §5.29.4 invoice fields: `exchange_rate_id` and three-currency amounts (w12), `cash_discount_date/amount`, `tax_document_id`, `document_ref`, `einvoice_ref`; line fields `order_line_id`, `packing_slip_line_id`, `product_id`, `unit`, `cost_amount`, `performance_obligation_id`, `price_explain_ref`, `dimensions` | later slice of PSG-29 | Each arrives with the flow that fills it (shipment invoices, revrec contracts, docgen, FX) |
| 38 | §5.29.4 partitioning of invoices by posting month | later slice of PSG-29 | Volumes of slice 1 do not need it; indexes on (invoice_account_id, status, due_date) and the newest-first list exist |
| 39 | BR-29.9 fiscal year from the entity's fiscal calendar; numbering through the wave-5 sequence service | later slice of PSG-29 | The number series is per (entity, posting calendar year) in billing's own serialized `number_sequence_counter` row, drawn in the posting transaction; the org fiscal calendar (wave 6) and the numbering service are not read yet |
| 40 | §5.29.6 Billing workspace tiles (ready to invoice, failed posts, deferred balance, subscriptions due), due list, multi-select post, tax and journal preview before post, print and credit actions | later slice of PSG-29 | Slice 1 ships the invoices list, free-text form, invoice detail with post, and the revenue allocation preview; the rest needs items 1, 3, 5, 6 and 15 |
| 41 | Invoices list cursor pagination | later slice of PSG-29 | `GET /invoices` returns the newest `limit` (≤ 200) headers with status and customer filters; no cursor yet |
