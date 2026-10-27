# NOT-IMPLEMENTED (treasury, PSG-36)

Slice 1 builds: bank accounts with the Draft -> Active -> Inactive -> Closed
lifecycle (FR-36.1, partial), camt.053 statement import with duplicate and
sequence-gap detection (FR-36.2, partial), the cash forecast over stored entries
(FR-36.9, partial), the pure matching-score engine (FR-36.3 scoring, BR-36.1,
BR-36.2; domain only), the `statement.imported.v1` event, and the web module
`apps/web/src/modules/treasury` (bank accounts list, create and detail;
statement import and detail; cash forecast with entry form).

Slice 2 (PSG-3602) is a completeness pass over this file against PRD 7.36:
it adds the PRD sections (parameters, documents, batch jobs, duties and SoD,
migration, performance, localization, coverage closure, operations) that the
slice 1 table did not cover. No code changed.

## Deliverables and requirements not built in this slice

| Deliverable / requirement | Status | Reason |
|---|---|---|
| D1 / FR-36.1: signatories, bank transaction types, `next_check_number`, non-IBAN account and ABA routing numbers (checksum), editing number, currency or GL account through the 2-step approval workflow | later slice of PSG-36 | Slice 1 creates and transitions accounts only; there is no update endpoint yet, so there is nothing to route through approval. The approval workflow needs the Temporal workflow service (`services/workflow-ts`, owned elsewhere). |
| D1 / FR-36.1: IBAN encrypted at rest (pgcrypto, key in Vault) and shown unmasked to `TreasuryBankAccountMaintain` | handoff (wave 5 Vault integration) | No Vault key is available to the service. Until it is, only an unkeyed SHA-256 lookup hash, the masked IBAN and the last four characters are stored; the full IBAN is not retained, so payment files cannot be generated from this table yet. The hash becomes an HMAC with a Vault key in the same change. |
| D1 / FR-36.6: check management (issue, void, reprint, stop, positive pay) | later slice of PSG-36 | Not started. |
| D1 / FR-36.7: letters of credit, guarantees and facilities | later slice of PSG-36 | Not started. |
| D2 / FR-36.2: BAI2, MT940 and configurable CSV formats; camt.053 files with more than one `Stmt`; XSD validation; per-bank profile overrides; `statement.sequenceGapPolicy` Ignore and Block (only the Warn default is built) | later slice of PSG-36 | Only camt.053 (v02/v08 by local-name parsing) is read; a multi-statement file is refused with TRS-0493. |
| D2 / FR-36.3: rule sets, rule activation with a precision/recall report, 1:N and N:1 matches, `run-rules` and manual-match endpoints, `ReconciliationMatch` table | later slice of PSG-36 | `internal/domain/match.go` scores and decides per line, but no API, table or event uses it yet. |
| AC1 / AC-36.2: 1,000-line fixture auto-matches >= 95% | not met | The domain test `TestDecide_RecordedFixturePrecision` auto-matches the 600 exact lines with no wrong match; the 250 reference-only lines score in the Suggested band with the single default weight set. Reaching 95% needs the ordered rule sets (a reference rule) of FR-36.3. The `recon-1000` fixture across camt.053/BAI2/MT940 does not exist yet. |
| D2 / FR-36.4: ML match suggestions | later slice of PSG-36 | Needs a Python service (`cashforecast-svc`), not in this wave's ownership. |
| FR-36.5: reconciliation workspace, close, BR-36.3 state machine, BR-36.7 line-level 409 | later slice of PSG-36 | Lines carry `version` for BR-36.7, but nothing updates them yet. |
| D3 / FR-36.8: cash position from camt.052 and statement balances | later slice of PSG-36 | Not started; no camt.052 parser. |
| D3 / FR-36.9: forecast sources (AP, AR, PO/SO, recurring, payroll, projects) feeding `ForecastSource.Contribute`; `forecast.horizon` parameters; BR-36.6 banking-day calendar; monthly partitioning and 24-month retention of `cash_forecast_entry` | later slice of PSG-36 | The forecast buckets stored entries (manual, or posted by a source with its `sourceType`); no subledger is read yet. Buckets use calendar days in UTC. |
| AC2 / AC-36.7: forecast equals the sum of sources per bucket | partial | `TestBuild_AdditivityProperty` (rapid) checks bucket = sum of contributions for the domain builder; the 10,000-item `forecast-10k` fixture is not built. |
| D4 / FR-36.10, FR-36.11: intercompany netting and cash pooling | later slice of PSG-36 | Not started. |
| D5 / FR-36.12, AC3, AC4: SFTP, EBICS and XS2A adapters, Vault signing, file-sent audit | handoff | Needs `bank-connect-svc`, the wave-35 integration hub for SFTP transport and wave-5 Vault, none owned by this wave. |
| D6 / FR-36.13 to FR-36.15: bills of exchange, promissory notes, postdated checks | later slice of PSG-36 | Not started. |
| D7 / FR-36.16: cash desks and cash journal | later slice of PSG-36 | Not started. |
| D8 / FR-36.17: camt.086 bank fee analysis | later slice of PSG-36 | Not started. |
| D9 / FR-36.18, FR-36.20: treasury deals and FX exposure | later slice of PSG-36 | Not started. |
| D10 / FR-36.19, FR-36.25: loan contracts and balance interest | later slice of PSG-36 | Not started. |
| D11 / FR-36.21 to FR-36.23: payment requests, bank chains, payment hub routing | later slice of PSG-36 | Not started. |
| D12 / FR-36.24: in-house cash center | later slice of PSG-36 | Not started (flag `treasury.ihc`). |
| Events other than `statement.imported.v1` (reconciled, check.voided, cashposition.updated, file.sent, instrument, pdc, cashdesk, deal, paymentrequest, ihc) | later slice of PSG-36 | Their state changes are not built yet. |
| Web: reconciliation workspace, cash position, checks, facilities, bank connections screens; a shared legal-entity picker | later slice of PSG-36 / handoff | The screens follow their endpoints. The legal-entity switcher lives in the org module and is not exported to other modules; a shared picker belongs to the shell owner (wave 5), so the treasury pages take a legal entity id. |
| 7.36.13: `Treasury parameters` page with effective-dated, audited parameters (`recon.*`, `statement.*`, `position.staleHours`, `forecast.*`, `check.*`, `facility.*`, `instrument.*`, `pdc.*`, `cashDesk.*`, `fees.*`, `deal.*`, `loan.*`, `interest.*`, `bankChain.*`, `routing.*`, `ihc.*`) | later slice of PSG-36 | No parameter store yet. The matching engine uses the PRD defaults (auto 90, suggest 60, tie margin 5) as constants; the upload limit comes from the `TREASURY_MAX_FILE_MB` environment variable (default 50), not a tenant parameter. |
| 7.36.14: inquiries and reports (reconciliation status, unmatched-lines aging, check register, facility utilization, instrument lists, cash book, fee analysis, treasury position/flow/posting lists, FX exposure, loan schedule, payment batch monitor, IHC balances, interest run result) and their read models | later slice of PSG-36 | Each follows the feature it reports on. Built today: the bank account list, statement list and lines, and the cash flow forecast drill-down by bucket. |
| 7.36.15: printed and electronic documents (check with MICR line, positive pay file, bill of exchange, remittance list, promissory note, cash receipt, cash book, deal confirmation, interest advice, IHC statement, payment order, fee variance report) | later slice of PSG-36 | Their source entities are not built; check and remittance files also need the wave-19 format engine and wave-48 country packs. |
| 7.36.16: batch jobs (`statement-poll`, `cash-position-refresh`, `forecast-rebuild`, `facility-expiry-check`, `ml-match-retrain`, `bank-gl-reconcile`, `pdc-maturity-deposit`, `instrument-maturity`, `deal-valuation`, `valuation-reversal`, `loan-interest-accrual`, `gl-interest-run`, `payment-batch-window`, `fee-statement-poll`, `ihc-interest-and-statements`, `cash-desk-open-day-check`) | later slice of PSG-36 / handoff | None is scheduled. They run as Temporal schedules, whose worker service is owned outside this wave. The forecast is computed on each read instead of from an hourly snapshot. |
| 7.36.17: duties beyond those enforced today, and SoD conflicts | later slice of PSG-36 / handoff | Enforced through `platform/authz` today: `treasury.view` (TreasuryView), `treasury.bank-account.write` (TreasuryBankAccountMaintain, create), `treasury.bank-account.approve` (TreasuryBankAccountApprove, activate, deactivate, close), `treasury.statement.import` (TreasuryStatementImport) and `treasury.forecast.write` (manual forecast entries; the PRD names no duty for it). The other duties arrive with their endpoints. SoD conflicts (for example maintain vs approve on the same account) are OPA rules in `policies/`, outside this wave. |
| 7.36.9 / FR-36.2: statement-parser contract fixtures for camt.053 v02 as a file, BAI2 and MT940; match-uniqueness property test; reconcile-and-close E2E; import throughput test (10,000 lines < 30 s, 200,000 lines < 10 min); fixtures `ebics-sandbox` and `facility-20` | later slice of PSG-36 | Only `testdata/camt053/sample-08.xml` exists; v02 is covered by inline documents in `camt053_test.go`. The other tests need the formats, the reconciliation schema, adapters and facilities that are not built. |
| 7.36.9: reconciliation checks (Σ bank account book balance = GL bank main account per entity; Σ outstanding checks = positive pay open items; cash position = latest closing + pending) | later slice of PSG-36 | Need book balances (wave-11 journal), checks and the cash position read model. The per-statement check opening + Σ lines = closing is built and tested. |
| 7.36.19: performance budgets; monthly partitioning of `bank_statement_line` by booking date; GIN trigram index on counterparty; rule-run and manual-match latency targets | later slice of PSG-36 | Plain tables with tenant-leading b-tree indexes for now; no load test exists. Trigram counterparty similarity in FR-36.3 is computed in Go, not in Postgres. |
| 7.36.20: localization extension points (`InstrumentRemittanceFormat`, `PdcPolicy`, `CashJournalPolicy`, `BankFormatProfile`, `RegulatoryTradeReport`), banking-day calendars, LEI/UTI on deals, retention enforcement | later slice of PSG-36 / handoff | The extension points come with the features they extend; formats and country rules come from wave-48 packs and the wave-19 format engine; retention is enforced through wave-5 policies. |
| 7.36.10: Helm charts `bank-connect-svc` and `cashforecast-svc`; feature flags `treasury.ml-suggestions`, `treasury.cash-pooling`, `treasury.bills-of-exchange`, `treasury.ihc`; config `TREASURY_AUTO_MATCH_THRESHOLD`; SLO alerts (unreconciled lines > 7 days, connection failure, key rotation due); runbooks | later slice of PSG-36 / handoff | Only the `charts/treasury` chart exists. The two other services are not built. Alert rules and runbooks belong with the deployment and observability owners once the jobs exist. |
| 7.36.11: migration from AX / SAP (BankAccountTable, BankChequeTable, BankLC*/BankLG*, T012/T012K, FEBKO/FEBEP, PAYR) and its validation (Σ migrated balances = GL bank accounts) | handoff | Owned by the migration-framework wave. The import targets here are the bank account and statement APIs. |
| 7.36.21: hedge accounting (IFRS 9 / ASC 815 designation, OCI reserve, effectiveness tests, hedge plan reports, THM* customizing); securities, money-market paper and options | out of scope (v1) | Deferred to v2 by the PRD coverage closure. FX deals will be valued through P&L (FR-36.18). |

## Handoffs outside this wave's ownership

| Item | Owner | Note |
|---|---|---|
| Kafka publication of events (Debezium relay) | eventing backbone | The outbox row is written in the same transaction; relay and topic `passage.treasury.statement` provisioning are not here. |
| Container registry push, cosign signing, SBOM | CI and deployment (`deploy/`, `.github/`) | |
| Helm umbrella and Argo CD application entry | `deploy/` | |
| OPA policy bundle for treasury duties | `policies/` | The service enforces duties through `platform/authz`. |
| Vault role `bank-connect` and transit keys | wave 5 | Blocks IBAN encryption and bank connectivity above. |
