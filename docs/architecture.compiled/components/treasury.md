# Treasury

## Purpose & Boundaries
Context `treasury` (Go only, database `passage_treasury`; waves 36-37). Bank accounts/statements/reconciliation, cash forecast, checks, fixed assets, asset books, depreciation, leases (IFRS 16), letters of credit. Posts via `ledger.Post` (bank charges, reconciliation adjustments, depreciation, lease). Signs (Vault transit) and transmits the payment file that `procurement` generates.

## Interfaces
Prefix `/v1/treasury/`: `bank-statements:import` (202; camt.053/MT940/BAI2; idempotent by file hash), `bank-statements/{id}:auto-reconcile`, `bank-statement-lines/{id}:match` (posts via ledger.Post), `cash-positions`, `cash-forecasts` (202), `checks/{id}:void` (number retained), `bank-connections/{id}:test` (EBICS/SFTP; keys stay in Vault), `payment-submissions` (Svc:`paymentsubmission.write`; signed under principal `system:treasury-signer` holding duty `Release payments` — key custody control, not SoD), `fixed-assets/{id}:acquire|:dispose|:transfer|:revalue`, `depreciation-runs` (202), `leases/{id}:commence|:modify`, `GET /reconciliation/{check}` (bank, assets).

## Data Ownership
EN-069..071: bank_account (P3 fields in bank_account_secure; last4 mask), bank_statement (UQ file_hash), bank_statement_line (IL), fixed_asset, asset_book (depreciation_method straight_line|declining|macrs|units|custom), asset_transaction (F; gap-free `...-FAT-...`; types acquisition|depreciation|revaluation|disposal|transfer|impairment|lease_remeasurement), lease, lease_schedule_line, cash_forecast(+line), check (UQ (bank_account_id, check_number)), reconciliation_match (IL), letter_of_credit. Indexes IX-TR-01..11.

## Events Produced
`treasury.bankstatement.reconciled.v1`, `treasury.asset.depreciated.v1` (depreciationRunId, bookCode, totalAmount, journalEntryId).

## Events Consumed
`treasury-forecast`, `treasury-payments` (procurement.payment.posted), `treasury-cash` (sales.payment.applied/received), `treasury-payroll`, `treasury-freight` (warehouse.load.shipped), `treasury-fx`, `treasury-reversal`, `treasury-partymerge`, `treasury-periodcache`, `treasury-approvals`, `treasury-numbersequence`.

## Dependencies
internal: ledger, procurement (payment files), sales, workforce, warehouse. external: banks (EBICS 3.0, SFTP, host-to-host), Vault transit, ISO 20022 formats.

## Gotchas & Constraints
- Treasury suggestions (GBM match suggestions FR-36.4) never auto-post; auto-match weighted rules (auto >= 90, suggest 60-89, BR-36.1/36.2 assumed companion PRD).
- Banks: queue outbound files; alert on ack timeout; idempotent by file hash.
- Bank-account changes need approval workflow (SoD).
- Customer-credit statement lines enter cash application via `sales.payment.received` (CAMT053).
- Fixed asset depreciation 100k assets monthly < 10 min (AP-80); NBV vs GL reconciliation REC-14.

## Source Sections
- Backend 5.14 (858-878), 3.4 (340); Data EN-069..071 (3101-3131); Events 3.9 (4073-4074), consumer rows (4226-4227, 4307); AI 13 hook (7659)
