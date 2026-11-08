-- Pricing context, wave 26 slice 27: free goods rules for a product group (FR-26.22, BR-26.13;
-- AX supplementary items and SAP free goods are keyed on a product or a material group). A rule
-- keeps its key in product_id; product_kind says whether that key is a product or a product
-- group, as customer_kind does for customer_key. Existing rules are product rules. The table,
-- its row level security and its grant to passage_app (SELECT, INSERT, UPDATE) come from 0009;
-- no new table here.
ALTER TABLE free_goods_rule
  ADD COLUMN IF NOT EXISTS product_kind text NOT NULL DEFAULT 'product'
    CHECK (product_kind IN ('product', 'product_group'));
