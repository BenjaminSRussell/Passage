ALTER TABLE eco ADD COLUMN IF NOT EXISTS created_by text NOT NULL DEFAULT '';
ALTER TABLE eco ADD COLUMN IF NOT EXISTS decision_reason text NOT NULL DEFAULT '';
UPDATE eco SET created_by = o.payload->>'actor'
FROM outbox o
WHERE eco.created_by = ''
  AND o.tenant_id = eco.tenant_id
  AND o.aggregate_id = eco.id::text
  AND o.aggregate_type = 'eco'
  AND o.type LIKE '%.eco.submitted.%'
  AND coalesce(o.payload->>'actor', '') <> '';
