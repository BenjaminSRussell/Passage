-- mart.fin_journal_line: the only source for monetary metrics (FR-46.5).
--
-- amount_reporting is the signed amount in the ledger's currency: debit
-- positive, credit negative, as in the ledger's own balance rebuild
-- (services/ledger/internal/store/balance.go). A negative posting (storno)
-- keeps that sign: it reduces the opposite side, so its net is the same.
-- Translation to a separate reporting currency is not in the ledger yet; see
-- services/analytics/NOT-IMPLEMENTED.md (BR-46.1).
--
-- Every ledger journal line is posted (the journal is append-only), so status
-- is POSTED. BR-46.3: a reversal is its own entry with its own posting_date
-- and opposite sides, so it nets revenue to zero on the reversal's date and
-- the original date is never re-dated.
--
-- account_category comes from the main account type; COGS accounts are not a
-- ledger account type, so seeds/account_category_map.csv names them per
-- (tenant, chart, account number).
select
    l.tenant_id,
    l.id as journal_line_id,
    l.legal_entity_id,
    l.entry_id as journal_id,
    e.ledger_id,
    a.account_number as account_code,
    coalesce(
        m.account_category,
        case a.account_type
            when 'revenue' then 'REVENUE'
            when 'expense' then 'EXPENSE'
            when 'asset' then 'ASSET'
            when 'liability' then 'LIABILITY'
            when 'equity' then 'EQUITY'
            else 'OTHER'
        end
    ) as account_category,
    'POSTED' as status,
    e.posting_date,
    e.period_key as fiscal_period,
    g.currency,
    cast(case when l.debit_credit = 'D' then l.amount_acct else -l.amount_acct end as decimal(20, 6)) as amount_reporting,
    l.reversal_of_line_id as reverses_line_id,
    e.reversal_of_entry_id is not null as is_reversal,
    l.late
from {{ ref('core_journal_line') }} as l
join {{ ref('core_journal_entry') }} as e on e.tenant_id = l.tenant_id and e.id = l.entry_id
join {{ ref('core_ledger') }} as g on g.tenant_id = e.tenant_id and g.id = e.ledger_id
join {{ ref('core_main_account') }} as a on a.tenant_id = l.tenant_id and a.id = l.main_account_id
left join {{ ref('account_category_map') }} as m
    on m.tenant_id = a.tenant_id and m.chart_code = a.chart_code and m.account_number = a.account_number
