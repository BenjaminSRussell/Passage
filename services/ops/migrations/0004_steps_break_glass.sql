-- Failover step results and break-glass for ops.failover.execute (PSG-50, part 2). Platform state, not tenant data: no
-- tenant_id and no RLS (allowlisted in tools/rlscheck), like the other ops tables.

-- When a region's lag was last reported. A reading older than OPS_LAG_STALE_S is treated as unknown (very large), so a stopped
-- reporter blocks a failover instead of leaving the last, now stale, number in place.
ALTER TABLE ops_region ADD COLUMN lag_reported_at timestamptz;

-- One row per executed step of a failover plan, written by the runner (passage-ops failover) as each step finishes, so a
-- crashed run resumes at the first step that is not DONE and the record shows who ran what.
CREATE TABLE ops_failover_step (
  event_id    text NOT NULL REFERENCES ops_failover (event_id),
  action      text NOT NULL,
  ord         integer NOT NULL CHECK (ord > 0),
  region      text NOT NULL,
  status      text NOT NULL CHECK (status IN ('DONE', 'FAILED')),
  detail      text NOT NULL DEFAULT '',
  -- true when the fence step was recorded without reaching the old region (it is lost); needs a second operator's approval.
  forced      boolean NOT NULL DEFAULT false,
  started_at  timestamptz NOT NULL,
  finished_at timestamptz NOT NULL,
  reported_by text NOT NULL,
  PRIMARY KEY (event_id, action)
);

-- A break-glass window: a named operator without the ops.failover.execute duty may use its routes until expires_at.
-- The window is capped at 120 minutes, needs a reason code and a ticket, and is reviewed afterwards by someone else.
CREATE TABLE ops_break_glass (
  id             uuid PRIMARY KEY,
  user_id        text NOT NULL,
  reason_code    text NOT NULL,
  ticket_ref     text NOT NULL,
  granted_at     timestamptz NOT NULL DEFAULT now(),
  expires_at     timestamptz NOT NULL,
  revoked_at     timestamptz,
  review_due_at  timestamptz NOT NULL,
  reviewed_by    text,
  reviewed_at    timestamptz,
  review_outcome text CHECK (review_outcome IN ('APPROVED', 'FLAGGED')),
  review_note    text NOT NULL DEFAULT '',
  CHECK (expires_at > granted_at AND expires_at <= granted_at + interval '120 minutes'),
  CHECK ((reviewed_by IS NULL) = (reviewed_at IS NULL) AND (reviewed_by IS NULL) = (review_outcome IS NULL)),
  CHECK (reviewed_by IS NULL OR reviewed_by <> user_id)
);
CREATE INDEX ops_break_glass_user ON ops_break_glass (user_id, expires_at);
CREATE INDEX ops_break_glass_unreviewed ON ops_break_glass (review_due_at) WHERE reviewed_by IS NULL;

-- Every request a window authorised.
CREATE TABLE ops_break_glass_use (
  id       bigserial PRIMARY KEY,
  grant_id uuid NOT NULL REFERENCES ops_break_glass (id),
  method   text NOT NULL,
  path     text NOT NULL,
  used_at  timestamptz NOT NULL DEFAULT now()
);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ops_failover_step TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ops_break_glass TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON ops_break_glass_use TO passage_app';
    EXECUTE format('GRANT USAGE ON SEQUENCE %I TO passage_app', 'ops_break_glass_use_id_seq');
  END IF;
END
$$;
