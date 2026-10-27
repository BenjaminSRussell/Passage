-- core.main_account: current state of main accounts. SCD2 history for master
-- data is a later slice (NOT-IMPLEMENTED.md, FR-46.5). Columns follow
-- services/ledger/migrations/0002_chart_ledger.sql.
{{ config(
    unique_key=['tenant_id', 'id'],
    post_hook="delete from {{ this }} where _is_deleted"
) }}

{{ latest_state(source('raw_ledger', 'main_account'), [
    'tenant_id', 'id', 'chart_code', 'account_number', 'name', 'account_type'
]) }}
