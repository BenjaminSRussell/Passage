-- Row-count reconciliation raw.* -> core.* (PRD 9.46.9): the live keys of the
-- landing table (latest change is not a delete) equal the rows in core.
with raw_latest as (
    select tenant_id, id, _op,
        row_number() over (partition by tenant_id, id order by _source_lsn desc) as rn
    from {{ source('raw_ledger', 'journal_line') }}
),
raw_live as (
    select tenant_id, count(*) as n from raw_latest where rn = 1 and _op <> 'd' group by tenant_id
),
core_rows as (
    select tenant_id, count(*) as n from {{ ref('core_journal_line') }} group by tenant_id
)
select coalesce(r.tenant_id, c.tenant_id) as tenant_id, r.n as raw_live, c.n as core_rows
from raw_live as r
full outer join core_rows as c on c.tenant_id = r.tenant_id
where coalesce(r.n, 0) <> coalesce(c.n, 0)
