ALTER TABLE ap_open_item DROP CONSTRAINT IF EXISTS ap_open_item_due_date_source_check;
ALTER TABLE ap_open_item ADD CONSTRAINT ap_open_item_due_date_source_check CHECK (due_date_source IN ('EVENT', 'DEFAULTED', 'OPENING', 'TERMS'));
