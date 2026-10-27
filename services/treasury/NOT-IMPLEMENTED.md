# NOT-IMPLEMENTED (treasury, PSG-36 slice 1)

Slice 1 builds: bank accounts with the Draft -> Active -> Inactive -> Closed
lifecycle (FR-36.1, partial), camt.053 statement import with duplicate and
sequence-gap detection (FR-36.2, partial), the cash forecast over stored entries
(FR-36.9, partial), the pure matching-score engine (FR-36.3 scoring, BR-36.1,
BR-36.2; domain only), the `statement.imported.v1` event, and the web module
`apps/web/src/modules/treasury` (bank accounts list, create and detail;
statement import and detail; cash forecast with entry form).

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

## Handoffs outside this wave's ownership

| Item | Owner | Note |
|---|---|---|
| Kafka publication of events (Debezium relay) | eventing backbone | The outbox row is written in the same transaction; relay and topic `passage.treasury.statement` provisioning are not here. |
| Container registry push, cosign signing, SBOM | CI and deployment (`deploy/`, `.github/`) | |
| Helm umbrella and Argo CD application entry | `deploy/` | |
| OPA policy bundle for treasury duties | `policies/` | The service enforces duties through `platform/authz`. |
| Vault role `bank-connect` and transit keys | wave 5 | Blocks IBAN encryption and bank connectivity above. |
