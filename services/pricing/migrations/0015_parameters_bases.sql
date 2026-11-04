-- Pricing parameters per legal entity (PRD 5.26.13): pricing date basis (BR-26.2), total
-- discount basis and the smart rounding bands of journal adjustments (FR-26.7). The table,
-- its row level security and its grant to passage_app come from 0012; no new table here.
-- smart_rounding_bands is NULL for the PRD default table (0-10 .99, 10-100 .95, 100+ whole).
ALTER TABLE pricing_parameter
  ADD COLUMN IF NOT EXISTS pricing_date_basis text NOT NULL DEFAULT 'SHIP_DATE'
    CHECK (pricing_date_basis IN ('SHIP_DATE', 'ORDER_DATE', 'DELIVERY_DATE')),
  ADD COLUMN IF NOT EXISTS total_discount_basis text NOT NULL DEFAULT 'AFTER_LINE_DISC'
    CHECK (total_discount_basis IN ('AFTER_LINE_DISC', 'GROSS')),
  ADD COLUMN IF NOT EXISTS smart_rounding_bands jsonb
    CHECK (smart_rounding_bands IS NULL OR (jsonb_typeof(smart_rounding_bands) = 'array'
      AND jsonb_array_length(smart_rounding_bands) BETWEEN 1 AND 10));
