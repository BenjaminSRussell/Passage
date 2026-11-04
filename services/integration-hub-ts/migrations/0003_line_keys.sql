-- Integration hub: the legacy keys of a pushed collection's lines, and the lookup table versions a render read.
-- No new table: the columns are on hub_delivery (tenant_id, row-level security and the passage_app grants of
-- SELECT, INSERT, UPDATE come from 0001), written by the attempt update the store already makes.

-- [{"index": 0, "legacyKey": "L-1"}, ...]: the key the legacy ERP assigned to each element of the pushed collection (position from 0).
ALTER TABLE hub_delivery ADD COLUMN IF NOT EXISTS line_keys jsonb CHECK (line_keys IS NULL OR jsonb_typeof(line_keys) = 'array');
-- [{"name": "uom", "table": "uom_conversion", "version": 3}, ...]: the lookup tables (migration lookup-tables) the last render read.
ALTER TABLE hub_delivery ADD COLUMN IF NOT EXISTS render_lookups jsonb CHECK (render_lookups IS NULL OR jsonb_typeof(render_lookups) = 'array');
