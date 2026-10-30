DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION period_status_track_reopen_expiry() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    BEGIN
      IF TG_OP = 'DELETE' THEN
        DELETE FROM reopen_expiry WHERE tenant_id = OLD.tenant_id AND ledger_id = OLD.ledger_id AND fiscal_period_id = OLD.fiscal_period_id AND module = OLD.module;
        RETURN OLD;
      END IF;
      IF NEW.reopen_expires_at IS NULL OR NEW.status NOT IN ('open'::text, 'on_hold'::text) THEN
        DELETE FROM reopen_expiry WHERE tenant_id = NEW.tenant_id AND ledger_id = NEW.ledger_id AND fiscal_period_id = NEW.fiscal_period_id AND module = NEW.module;
      ELSE
        INSERT INTO reopen_expiry(tenant_id, ledger_id, fiscal_period_id, module, reopen_expires_at)
        VALUES (NEW.tenant_id, NEW.ledger_id, NEW.fiscal_period_id, NEW.module, NEW.reopen_expires_at)
        ON CONFLICT (tenant_id, ledger_id, fiscal_period_id, module) DO UPDATE SET reopen_expires_at = EXCLUDED.reopen_expires_at;
      END IF;
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION close_reopen_due_tenants() RETURNS SETOF uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT DISTINCT tenant_id FROM reopen_expiry WHERE reopen_expires_at <= pg_catalog.now() $body$;
  $f$, current_schema());
END
$do$;

REVOKE ALL ON FUNCTION period_status_track_reopen_expiry() FROM PUBLIC;
REVOKE ALL ON FUNCTION close_reopen_due_tenants() FROM PUBLIC;
