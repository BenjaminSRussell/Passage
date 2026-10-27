{#-
  latest_state(relation, columns): the current state of every key in a raw.*
  CDC landing table (FR-46.4, BR-46.2). Keys are (tenant_id, id), as in every
  Passage table.

  Exactly-once effect: the winner per key is the change with the highest
  _source_lsn among ALL raw rows for that key, not only the rows in the
  incremental window. A redelivered or late older change therefore can never
  overwrite a newer one, and replaying any permutation of events yields the
  same table.

  Incremental runs pick the keys that received any change in the last 3 days of
  ingestion (_ingested_at, so rows with an old _source_ts that arrive late are
  still picked up) and recompute only those keys. The newest change of a
  deleted key is a tombstone (_is_deleted); the model's post-hook removes it
  from the current table after the merge has replaced the live row.
-#}
{% macro latest_state(relation, columns) %}
with
{% if is_incremental() %}
changed_keys as (
    select distinct tenant_id, id
    from {{ relation }}
    where _ingested_at >= (
        select coalesce(max(_ingested_at), timestamp '1970-01-01 00:00:00 UTC') from {{ this }}
    ) - interval '3' day
),
{% endif %}
ranked as (
    select
        r.*,
        row_number() over (partition by r.tenant_id, r.id order by r._source_lsn desc) as _rn
    from {{ relation }} as r
    {% if is_incremental() %}
    join changed_keys as k on k.tenant_id = r.tenant_id and k.id = r.id
    {% endif %}
)

select
    {% for column in columns %}{{ column }},
    {% endfor %}_source_lsn,
    _source_ts,
    _ingested_at,
    coalesce(late, false) as late,
    _op = 'd' as _is_deleted
from ranked
where _rn = 1
{% endmacro %}
