-- #35 P4a: a mapped inbound 850 or ORDERS is posted to sales by a background loop (partners opt out with auto_post = false).
-- unit_map turns the partner's units of measure (EA, CA, PCE) into ours; a unit with no entry parks the order.
-- sales_request pins the body sent under a message's Idempotency-Key until sales answers, so a retry after a transient failure
-- sends the same body whatever happened to the cross-references since. post_attempts and post_retry_at are the loop's back-off.
ALTER TABLE partner_sales_defaults ADD COLUMN IF NOT EXISTS auto_post boolean NOT NULL DEFAULT true;
ALTER TABLE partner_sales_defaults ADD COLUMN IF NOT EXISTS unit_map jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE message ADD COLUMN IF NOT EXISTS sales_request jsonb;
ALTER TABLE message ADD COLUMN IF NOT EXISTS post_attempts integer NOT NULL DEFAULT 0;
ALTER TABLE message ADD COLUMN IF NOT EXISTS post_retry_at timestamptz;

DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION edi_post_due_tenants() RETURNS SETOF uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT DISTINCT m.tenant_id FROM message m
        JOIN interchange i ON i.tenant_id = m.tenant_id AND i.id = m.interchange_id AND i.direction = 'IN'
        JOIN partner_sales_defaults d ON d.tenant_id = m.tenant_id AND d.partner_id = i.partner_id AND d.auto_post
        WHERE m.status = 'MAPPED' AND m.transaction_set IN ('850', 'ORDERS')
          AND (m.post_retry_at IS NULL OR m.post_retry_at <= pg_catalog.now()) $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION edi_post_due_tenants() FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION edi_post_due_tenants() TO passage_app';
  END IF;
END
$$;
