DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON party, consumer_inbox, idempotency_keys, payment_term, posting_profile, customer_role, party_merge TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON pending_change TO passage_app';
  END IF;
END
$$;
