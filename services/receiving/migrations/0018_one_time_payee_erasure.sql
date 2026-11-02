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
