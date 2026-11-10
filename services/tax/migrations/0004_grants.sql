DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON tax_jurisdiction, tax_code, tax_group, tax_entity_setting, tax_vat_box, tax_vat_box_code, tax_intrastat_master, tax_intrastat_journal, tax_intrastat_line, tax_intrastat_transferred TO passage_app';
  END IF;
END
$$;
