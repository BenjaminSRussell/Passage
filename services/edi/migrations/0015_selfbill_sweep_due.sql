-- #35 round 2, 35-5: the self-billing sweep runs on a schedule. The scheduler finds the tenants with work across tenants through
-- this SECURITY DEFINER function (like edi_post_due_tenants), then works one tenant at a time under row-level security.
DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION edi_selfbill_due_tenants() RETURNS SETOF uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
        SELECT m.tenant_id FROM message m
          JOIN interchange i ON i.tenant_id = m.tenant_id AND i.id = m.interchange_id AND i.direction = 'IN'
          WHERE m.transaction_set = 'INVOIC' AND m.status = 'MAPPED' AND m.canonical->>'selfBilled' = 'true'
        UNION
        SELECT sh.tenant_id FROM self_billing_shipment sh
          JOIN self_billing_config c ON c.tenant_id = sh.tenant_id AND c.partner_id = sh.partner_id
          WHERE sh.billed_by IS NULL AND sh.shipped_on + c.wait_days <= current_date
            AND NOT EXISTS (SELECT 1 FROM self_billing_dispute d WHERE d.tenant_id = sh.tenant_id AND d.shipment_id = sh.id AND d.kind = 'UNBILLED')
      $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION edi_selfbill_due_tenants() FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION edi_selfbill_due_tenants() TO passage_app';
  END IF;
END
$$;
