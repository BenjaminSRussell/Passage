-- One-time payee retention and erasure (BR-48.5, BR-48.6, #335 item 8).
-- subject_ref is the opaque data-subject id a compliance erasure command names; it is not personal data by
-- contract and stays after erasure as the record of which subject was erased. Invoices reference a payee by
-- foreign key and must outlive it for tax retention, so erasure overwrites the sealed columns in place and sets
-- erased_at; the row is never deleted. passage_app may update only those columns, so country, legal entity and
-- subject stay immutable.
ALTER TABLE receiving_one_time_payee ADD COLUMN IF NOT EXISTS subject_ref text CHECK (subject_ref IS NULL OR btrim(subject_ref) <> '');
ALTER TABLE receiving_one_time_payee ADD COLUMN IF NOT EXISTS erased_at timestamptz;
ALTER TABLE receiving_one_time_payee ADD COLUMN IF NOT EXISTS erasure_id uuid;

CREATE INDEX IF NOT EXISTS receiving_one_time_payee_subject_ix
  ON receiving_one_time_payee (tenant_id, legal_entity_id, subject_ref) WHERE erased_at IS NULL;

GRANT UPDATE (name_sealed, address_sealed, tax_id_sealed, email_sealed, bank_sealed, erased_at, erasure_id)
  ON receiving_one_time_payee TO passage_app;

CREATE OR REPLACE FUNCTION receiving_one_time_payee_erased_at_once() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.erased_at IS NOT NULL AND NEW.erased_at IS DISTINCT FROM OLD.erased_at THEN
    RAISE EXCEPTION 'erased_at is write-once' USING ERRCODE = '23514';
  END IF;
  IF OLD.erased_at IS NOT NULL AND (
    NEW.name_sealed IS DISTINCT FROM OLD.name_sealed OR NEW.address_sealed IS DISTINCT FROM OLD.address_sealed OR
    NEW.tax_id_sealed IS DISTINCT FROM OLD.tax_id_sealed OR NEW.email_sealed IS DISTINCT FROM OLD.email_sealed OR
    NEW.bank_sealed IS DISTINCT FROM OLD.bank_sealed OR NEW.erasure_id IS DISTINCT FROM OLD.erasure_id) THEN
    RAISE EXCEPTION 'an erased payee cannot be changed' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS receiving_one_time_payee_erased_at_once ON receiving_one_time_payee;
CREATE TRIGGER receiving_one_time_payee_erased_at_once BEFORE UPDATE ON receiving_one_time_payee
  FOR EACH ROW EXECUTE FUNCTION receiving_one_time_payee_erased_at_once();
