-- #483: PATCH /edi/v1/partners/{id} updates a partner under If-Match against trading_partner.version.
--
-- passage_app gets UPDATE on the columns the route writes and nothing else: tenant, id, environment and created_at stay
-- write-once, and partners are still never deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (name, isa_qualifier, isa_id, ack_sla_minutes, edifact_id, edifact_qualifier, edifact_contrl, status, version)
      ON trading_partner TO passage_app';
  END IF;
END
$$;

-- A partner with a trade profile is bound to the profile's legal entity, and only a caller scoped to that entity may
-- change it, as for the profile and the transport. The profile's restrictive RLS hides it from other callers, so they
-- cannot tell "no profile" from "a profile elsewhere"; this index answers that one question. Like transport_work it has
-- no RLS and no passage_app grant, is maintained by a SECURITY DEFINER trigger on partner_trade_profile, and is read
-- only by edi_partner_in_entity_scope(), which returns a boolean for the current tenant and scopes and nothing else.
CREATE TABLE IF NOT EXISTS partner_entity_index (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, partner_id)
);

DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION edi_partner_profile_track() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    BEGIN
      IF TG_OP = 'DELETE' THEN
        DELETE FROM partner_entity_index WHERE tenant_id = OLD.tenant_id AND partner_id = OLD.partner_id;
        RETURN OLD;
      END IF;
      INSERT INTO partner_entity_index (tenant_id, partner_id, legal_entity_id) VALUES (NEW.tenant_id, NEW.partner_id, NEW.legal_entity_id)
      ON CONFLICT (tenant_id, partner_id) DO UPDATE SET legal_entity_id = EXCLUDED.legal_entity_id;
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION edi_partner_in_entity_scope(partner uuid) RETURNS boolean
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT NOT EXISTS (SELECT 1 FROM partner_entity_index i
        WHERE i.tenant_id = nullif(pg_catalog.current_setting('passage.tenant_id', true), '')::uuid AND i.partner_id = partner
          AND NOT (coalesce(pg_catalog.current_setting('passage.legal_entity_scopes', true), '') = '*'
                   OR i.legal_entity_id::text = ANY(pg_catalog.string_to_array(pg_catalog.current_setting('passage.legal_entity_scopes', true), ',')))) $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION edi_partner_profile_track() FROM PUBLIC;
REVOKE ALL ON FUNCTION edi_partner_in_entity_scope(uuid) FROM PUBLIC;
DROP TRIGGER IF EXISTS partner_trade_profile_track ON partner_trade_profile;
CREATE TRIGGER partner_trade_profile_track AFTER INSERT OR UPDATE OF tenant_id, partner_id, legal_entity_id OR DELETE ON partner_trade_profile
  FOR EACH ROW EXECUTE FUNCTION edi_partner_profile_track();

-- Backfill the existing profiles. The migration sets no tenant, so under the forced policies an owner that is not a
-- superuser (the deployed shape) would read no row; the policy is lifted for the owner and forced again in the same
-- transaction, as fulfillment 0018 does.
ALTER TABLE partner_trade_profile NO FORCE ROW LEVEL SECURITY;
INSERT INTO partner_entity_index (tenant_id, partner_id, legal_entity_id)
  SELECT tenant_id, partner_id, legal_entity_id FROM partner_trade_profile
  ON CONFLICT (tenant_id, partner_id) DO UPDATE SET legal_entity_id = EXCLUDED.legal_entity_id;
ALTER TABLE partner_trade_profile FORCE ROW LEVEL SECURITY;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION edi_partner_in_entity_scope(uuid) TO passage_app';
  END IF;
END
$$;
