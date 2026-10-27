-- Reconciled measure test (FR-46.5): every journal entry in the mart nets to
-- zero, as it does in the ledger. A line dropped or double-counted on the way
-- from raw.* to mart.* shows up here as an unbalanced entry.
select tenant_id, journal_id, sum(amount_reporting) as net
from {{ ref('fin_journal_line') }}
group by tenant_id, journal_id
having sum(amount_reporting) <> 0
