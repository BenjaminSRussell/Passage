-- core.journal_line: current state of ledger journal lines (FR-46.4, BR-46.2).
-- One row per (tenant_id, id); see macros/latest_state.sql for the exactly-once
-- rule. Columns follow services/ledger/migrations/0003_journal.sql.
{{ config(
    unique_key=['tenant_id', 'id'],
    post_hook="delete from {{ this }} where _is_deleted"
) }}

{{ latest_state(source('raw_ledger', 'journal_line'), [
    'tenant_id', 'id', 'legal_entity_id', 'entry_id', 'line_number', 'main_account_id',
    'debit_credit', 'currency_txn', 'amount_txn', 'amount_acct', 'negative_posting', 'reversal_of_line_id'
]) }}
