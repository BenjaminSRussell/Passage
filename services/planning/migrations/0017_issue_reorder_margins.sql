-- FR-22.5: issue and reorder margins on item coverage, beside the receipt
-- margin. The issue margin moves each issue (demand) of the item earlier by
-- that many working days; the reorder margin adds that many working days to the
-- lead time of its planned orders. Existing rows keep 0. No new table, so the
-- grants of 0010 still apply.
ALTER TABLE item_coverage
  ADD COLUMN IF NOT EXISTS safety_margin_issue_days integer NOT NULL DEFAULT 0
    CHECK (safety_margin_issue_days BETWEEN 0 AND 365),
  ADD COLUMN IF NOT EXISTS safety_margin_reorder_days integer NOT NULL DEFAULT 0
    CHECK (safety_margin_reorder_days BETWEEN 0 AND 365);
