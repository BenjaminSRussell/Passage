-- Default description templates (FR-11.17). A template is keyed by
-- (scope, legal entity, source type, language); a legal entity's own template
-- overrides the tenant template for the same source type and language.
--
-- Tenant-scope rows carry no legal entity, so the uniqueness key maps them to
-- the nil UUID.

CREATE TABLE IF NOT EXISTS description_template (
    tenant_id       uuid        NOT NULL,
    id              uuid        NOT NULL,
    scope           text        NOT NULL CHECK (scope IN ('tenant', 'legal_entity')),
    legal_entity_id uuid,
    source_type     text        NOT NULL CHECK (source_type ~ '^[a-z][a-z0-9_]{0,59}$'),
    language        text        NOT NULL CHECK (language ~ '^[a-z]{2}(-[A-Z]{2})?$'),
    template        text        NOT NULL CHECK (char_length(template) BETWEEN 1 AND 255),
    field_bindings  jsonb       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(field_bindings) = 'object'),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    CHECK ((scope = 'tenant') = (legal_entity_id IS NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS description_template_scope_uq
    ON description_template (tenant_id,
                             COALESCE(legal_entity_id, '00000000-0000-0000-0000-000000000000'::uuid),
                             source_type, language);

ALTER TABLE description_template ENABLE ROW LEVEL SECURITY;
ALTER TABLE description_template FORCE ROW LEVEL SECURITY;
CREATE POLICY description_template_tenant_access ON description_template AS PERMISSIVE
    USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
-- Tenant templates are shared by every legal entity; a legal entity's own
-- templates are visible and writable only inside the caller's scopes.
CREATE POLICY description_template_legal_entity_scope ON description_template AS RESTRICTIVE
    USING (legal_entity_id IS NULL
           OR current_setting('passage.legal_entity_scopes', true) = '*'
           OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
    WITH CHECK (legal_entity_id IS NULL
           OR current_setting('passage.legal_entity_scopes', true) = '*'
           OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The app role needs the new table (0005_grants.sql cannot cover it). Templates
-- are configuration: no DELETE, as for reason codes and structures.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON description_template TO passage_app';
  END IF;
END
$$;
