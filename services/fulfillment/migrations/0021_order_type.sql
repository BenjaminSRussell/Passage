-- Fulfillment slice 18: the sales order type and the release priority on a
-- shipment, and the partial-release flag on a shipment line. No new tables:
-- shipment and shipment_line already carry tenant_id with forced row-level
-- security and their passage_app grants (SELECT, INSERT, UPDATE) from earlier
-- migrations.
--
-- shipment and shipment_line are live tables, so this file only changes the
-- catalog: a column with a constant default is a fast default (no rewrite, and
-- NOT NULL needs no scan because the default is not null), and the checks are
-- added NOT VALID so nothing is scanned under the ACCESS EXCLUSIVE lock, which
-- lasts an instant. The lock_timeout makes the migration fail (and the rollout
-- retry) instead of queueing behind a long transaction while every later query
-- on the table queues behind it. 0022 validates the checks without that lock.
SET LOCAL lock_timeout = '5s';

-- A JOURNAL order (sales BR-27.7) skips credit check and ATP and must never be
-- released, picked, packed or shipped; release skips its lines, and the
-- shipment stores the type so a pick, pack or post that reaches one anyway is
-- refused.
ALTER TABLE shipment ADD COLUMN IF NOT EXISTS order_type text NOT NULL DEFAULT 'ORDER';
ALTER TABLE shipment ADD CONSTRAINT shipment_order_type_check
  CHECK (order_type IN ('ORDER', 'JOURNAL', 'SUBSCRIPTION', 'RETURNED')) NOT VALID;

-- Release priority (FR-28.1, section 5.28.6 order priority): 1 (first) to 9,
-- 5 when the caller names none. A release run creates the shipments of higher
-- priority orders first.
ALTER TABLE shipment ADD COLUMN IF NOT EXISTS priority smallint NOT NULL DEFAULT 5;
ALTER TABLE shipment ADD CONSTRAINT shipment_priority_check CHECK (priority BETWEEN 1 AND 9) NOT VALID;

-- Partial release by ATP (release_requires_full_atp = false): a line released
-- for less than the order line quantity is flagged, so a later run releases
-- the open remainder instead of skipping the line as already released.
ALTER TABLE shipment_line ADD COLUMN IF NOT EXISTS partial boolean NOT NULL DEFAULT false;
