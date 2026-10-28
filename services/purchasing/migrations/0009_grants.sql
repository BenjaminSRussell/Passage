-- Application role privileges. passage_app (no BYPASSRLS) reads and writes through the
-- RLS policies; it never owns the tables. DELETE is limited to the tables the store
-- rewrites (invoicing plans, PO charges and versions, scheduling-agreement schedule
-- lines); trade agreements, RFQ lines and criteria, releases, the outbox and
-- idempotency keys are append-only. Skipped when the role does not exist (developer
-- databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT USAGE ON SEQUENCE purchase_requisition_number_seq, purchase_order_number_seq, purchase_agreement_number_seq, purchase_rfq_number_seq, purchase_scheduling_agreement_number_seq TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON purchase_requisition, purchase_requisition_line, purchase_order, purchase_order_line, purchase_agreement, purchase_agreement_line, purchase_rfq, purchase_rfq_invitation, purchase_rfq_reply, purchase_rfq_score, purchase_scheduling_agreement TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON purchase_order_version, purchase_sa_schedule_line TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON purchase_invoicing_plan, purchase_order_charge TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON purchase_invoicing_plan_row, trade_agreement, outbox, idempotency_keys, purchase_rfq_line, purchase_rfq_criterion, purchase_sa_release, purchase_sa_release_line TO passage_app';
  END IF;
END
$$;
