-- Wave 20 slice 3: standard cost activation (FR-20.5, BR-20.7). Additive only.

ALTER TABLE costing_version
  ADD COLUMN IF NOT EXISTS activated_at timestamptz;

-- What activation recorded per item cost, so a replayed activation returns the
-- same revaluation and the price history can show it. previous_unit_cost is
-- null when the product x site had no active standard; book_value is then the
-- inventory value the new standard replaced. Quantities are numeric(28,6) like
-- the inventory ledger; money is numeric(20,6) in the version's currency.
ALTER TABLE item_cost
  ADD COLUMN IF NOT EXISTS previous_unit_cost numeric(20,6),
  ADD COLUMN IF NOT EXISTS book_value numeric(20,6),
  ADD COLUMN IF NOT EXISTS on_hand_qty numeric(28,6),
  ADD COLUMN IF NOT EXISTS revaluation_amount numeric(20,6),
  ADD COLUMN IF NOT EXISTS activated_at timestamptz;

-- One ACTIVE standard cost per product x site. Activation supersedes the old
-- row before it activates the new one, so a concurrent activation that slipped
-- past the per-legal-entity advisory lock fails here instead of leaving two.
CREATE UNIQUE INDEX IF NOT EXISTS item_cost_one_active
  ON item_cost (tenant_id, product_id, site_id) WHERE status = 'ACTIVE';

-- Price history and the as-of cost lookup read activated rows newest first.
CREATE INDEX IF NOT EXISTS item_cost_activated
  ON item_cost (tenant_id, product_id, site_id, activated_at DESC) WHERE activated_at IS NOT NULL;
