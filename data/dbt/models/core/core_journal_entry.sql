-- core.journal_entry: current state of ledger journal entries (posting date,
-- period and reversal link for each line). Columns follow
-- services/ledger/migrations/0003_journal.sql.
{{ config(
    unique_key=['tenant_id', 'id'],
    post_hook="delete from {{ this }} where _is_deleted"
) }}

{{ latest_state(source('raw_ledger', 'journal_entry'), [
    'tenant_id', 'id', 'legal_entity_id', 'ledger_id', 'voucher', 'posting_sequence', 'posting_date',
    'period_key', 'reversal_of_entry_id', 'posted_at'
]) }}
