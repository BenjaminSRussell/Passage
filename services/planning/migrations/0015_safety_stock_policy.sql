-- FR-22.2, BR-22.5: the safety-stock policy of a coverage group. With `today`
-- the safety stock is a demand at today; with `first-issue` it is a demand on
-- the item's first issue (first demand) date. Existing groups keep `today`.
ALTER TABLE coverage_group
  ADD COLUMN IF NOT EXISTS safety_stock_policy text NOT NULL DEFAULT 'today'
  CHECK (safety_stock_policy IN ('today', 'first-issue'));
