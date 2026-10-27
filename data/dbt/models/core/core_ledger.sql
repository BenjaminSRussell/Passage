-- core.ledger: current state of ledgers (the accounting currency of each
-- journal line). Columns follow services/ledger/migrations/0002_chart_ledger.sql.
{{ config(
    unique_key=['tenant_id', 'id'],
    post_hook="delete from {{ this }} where _is_deleted"
) }}

{{ latest_state(source('raw_ledger', 'ledger'), [
    'tenant_id', 'id', 'legal_entity_id', 'code', 'chart_code', 'currency'
]) }}
