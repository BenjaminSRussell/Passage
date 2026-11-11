-- Failover approval (PSG-50, OPS-1702): a failover that accepts data loss is a request until a second operator approves it.
-- The approver is the verified caller of the approve route, never a value in the request body, and never the requester.
-- Platform state, not tenant data: no tenant_id and no RLS (allowlisted in tools/rlscheck), like ops_failover.
CREATE TABLE ops_failover_request (
  id           uuid PRIMARY KEY,
  event_id     text NOT NULL UNIQUE,
  to_region    text NOT NULL REFERENCES ops_region (name),
  lag_seconds  integer NOT NULL,
  requested_by text NOT NULL,
  status       text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'APPROVED')),
  approved_by  text,
  approved_at  timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,
  CHECK ((status = 'APPROVED') = (approved_by IS NOT NULL AND approved_at IS NOT NULL)),
  CHECK (approved_by IS NULL OR approved_by <> requested_by)
);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ops_failover_request TO passage_app';
  END IF;
END
$$;
