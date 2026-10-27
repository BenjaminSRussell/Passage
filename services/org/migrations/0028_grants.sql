-- Application role privileges. passage_app (no BYPASSRLS) only appends to the outbox
-- and hierarchy closure; elsewhere row-level security, not grants, keeps tenants out
-- of the seeded platform rows. Skipped when the role does not exist (developer
-- databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON idempotency_keys, country, currency, region, language, reference_status, legal_entity, legal_entity_name, operating_unit, warehouse, site, postal_address, address_purpose, location, fiscal_calendar, fiscal_period, hierarchy_type, hierarchy_version, hierarchy_node, currency_rounding, unit_of_measure, uom_conversion, team_type, team, team_member, org_unit_translation TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON outbox, hierarchy_closure TO passage_app';
  END IF;
END
$$;
