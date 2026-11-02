-- Key check scheduling and the vanish threshold (PSG-10 integration track S4).
-- keycheck_force: an operator asked for the next run to be a full read (keyCheckNow or keyCheckAcceptLarge). It
-- replaces clearing keycheck_attempt_at, because a binding that never checked is no longer due at once. A run spends
-- it only when its claim saw it, so a request sent while a run reads the source waits for the next run.
-- keycheck_since: the anchor of a binding's first key check, which falls at a deterministic offset inside the period
-- after it (FNV hash of the binding id, domain.KeyCheckOffset), so a deploy does not send every first full read to
-- the customer's legacy system together. It stays due from then until a run attempts it, whatever times the
-- schedule runs at. Existing rows take the time of this migration, so their first checks spread over the next period;
-- a new row takes the time it is created.
-- keycheck_accept_large: an operator accepts, once, a full read that lists more than half of the loaded keys still
-- present as newly absent; the completed key check of a run claimed after it clears it.
-- Locking: sync_state holds one row per scheduled binding and the scheduler updates it every pass. The defaults are a
-- constant and now() (stable, evaluated once), so the ALTER is catalog-only and holds its ACCESS EXCLUSIVE lock for an
-- instant; the lock_timeout makes the migration fail (and the rollout retry) instead of queueing behind a long
-- transaction while every claim queues behind it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE migration.sync_state
  ADD COLUMN keycheck_force boolean NOT NULL DEFAULT false,
  ADD COLUMN keycheck_accept_large boolean NOT NULL DEFAULT false,
  ADD COLUMN keycheck_since timestamptz NOT NULL DEFAULT now();
