-- Wave 27 slice 32: quotation maintenance (FR-27.1, BR-27.5). A quotation can
-- now be cancelled (DRAFT or SENT -> CANCELLED, with a reason) and changed
-- while it is open: validity and probability on a DRAFT or SENT quotation, and
-- the whole line set on a DRAFT one. A line change replaces the quotation's
-- lines, so the application role needs DELETE on sales_quotation_line (it had
-- SELECT, INSERT and UPDATE). No new table: RLS policies are unchanged.

SET LOCAL lock_timeout = '5s';

ALTER TABLE sales_quotation ADD COLUMN IF NOT EXISTS cancel_reason text
  CHECK (cancel_reason IS NULL OR length(cancel_reason) BETWEEN 1 AND 200);
ALTER TABLE sales_quotation DROP CONSTRAINT IF EXISTS sales_quotation_cancel_reason_set;
ALTER TABLE sales_quotation ADD CONSTRAINT sales_quotation_cancel_reason_set
  CHECK (status <> 'CANCELLED' OR cancel_reason IS NOT NULL) NOT VALID;
ALTER TABLE sales_quotation VALIDATE CONSTRAINT sales_quotation_cancel_reason_set;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT DELETE ON sales_quotation_line TO passage_app';
  END IF;
END
$$;
