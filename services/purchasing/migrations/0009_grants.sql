DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT USAGE ON SEQUENCE purchase_requisition_number_seq, purchase_order_number_seq, purchase_agreement_number_seq, purchase_rfq_number_seq, purchase_scheduling_agreement_number_seq TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON purchase_requisition, purchase_requisition_line, purchase_order, purchase_order_line, purchase_order_charge, purchase_order_version, trade_agreement, outbox, idempotency_keys, purchase_agreement, purchase_agreement_line, purchase_rfq, purchase_rfq_line, purchase_rfq_invitation, purchase_rfq_reply, purchase_rfq_criterion, purchase_rfq_score, purchase_scheduling_agreement, purchase_sa_schedule_line, purchase_sa_release, purchase_sa_release_line, purchase_invoicing_plan, purchase_invoicing_plan_row TO passage_app';
  END IF;
END
$$;
