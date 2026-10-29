DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint
      WHERE conrelid = 'inventory_journal'::regclass
        AND conname = 'inventory_journal_reverses_fk') THEN
    ALTER TABLE inventory_journal ADD CONSTRAINT inventory_journal_reverses_fk
      FOREIGN KEY (tenant_id, reverses_journal_id) REFERENCES inventory_journal (tenant_id, id);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint
      WHERE conrelid = 'inventory_journal_line'::regclass
        AND conname = 'inventory_journal_line_reverses_fk') THEN
    ALTER TABLE inventory_journal_line ADD CONSTRAINT inventory_journal_line_reverses_fk
      FOREIGN KEY (tenant_id, reverses_line_id) REFERENCES inventory_journal_line (tenant_id, id);
  END IF;
END
$$;
