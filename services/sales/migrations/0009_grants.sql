DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON sales_order_version, agreement_release, product_proposal_response TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON product_listing, product_proposal_rule, supplementary_item_rule TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON sales_number_sequence, sales_order, sales_order_line, order_hold, credit_profile, sales_agreement, sales_quotation, sales_quotation_line TO passage_app';
  END IF;
END
$$;
