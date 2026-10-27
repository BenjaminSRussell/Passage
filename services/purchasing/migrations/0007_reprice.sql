-- Wave 17 purchasing slice 6: subsequent price change on open POs (FR-17.22,
-- BR-17.18). Additive only (expand/contract); purchase_order_line already has
-- tenant_id with ENABLE/FORCE row-level security and its tenant policy.

-- The trade agreement price source a reprice run last handed to payables for an
-- invoiced line, so the same price change is never handed over twice.
ALTER TABLE purchase_order_line ADD COLUMN IF NOT EXISTS invoiced_reprice_source text;

-- The run selects open lines priced from a trade agreement, optionally from one row.
CREATE INDEX IF NOT EXISTS purchase_order_line_trade_price_idx
  ON purchase_order_line (tenant_id, price_source)
  WHERE status = 'OPEN' AND price_source LIKE 'TRADE_AGREEMENT:%';
