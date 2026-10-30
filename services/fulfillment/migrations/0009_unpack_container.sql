-- Fulfillment slice 8: unpack a CLOSED container before it ships. The container
-- row stays (the SSCC is never issued again, BR-28.5) with status UNPACKED and the
-- time it was taken apart. No new table: container keeps its tenant policy and
-- its SELECT, INSERT, UPDATE grants.

ALTER TABLE container ADD COLUMN IF NOT EXISTS unpacked_at timestamptz;
ALTER TABLE container DROP CONSTRAINT IF EXISTS container_status_check;
ALTER TABLE container ADD CONSTRAINT container_status_check CHECK (status IN ('OPEN', 'CLOSED', 'SHIPPED', 'UNPACKED'));
