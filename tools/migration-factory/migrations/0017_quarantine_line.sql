-- Migration factory, wave 49 slice 13 (decided item S11-a): releasing quarantined bank_balances and
-- onhand lines. The quarantine row keeps the six-field record shared by every pack, which is lossy for
-- a stock line (site, warehouse, location, batch, unit, value) and for a bank balance's role as a line.
-- A release needs the whole line, so the quarantine row now carries it as `line`. Rows quarantined
-- before this migration have none and cannot be released (re-extract and run the pack again).
--
-- pack_quarantine is live. A nullable column without a default is a catalog-only change, but the ALTER
-- still takes ACCESS EXCLUSIVE for an instant: the lock_timeout makes the migration fail (and the
-- rollout retry) instead of queueing behind a long transaction. No new table; UPDATE on
-- pack_quarantine was granted by 0002 (the release marks the row RELEASED).
SET LOCAL lock_timeout = '5s';

alter table pack_quarantine add column line jsonb;
