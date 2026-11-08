-- Pricing context, wave 26 slice 28: product scope on a rebate agreement (FR-26.11, PRD §5.26.4
-- "product scope"; AX rebate agreements and SAP condition contracts name the products or
-- material group they pay on). An agreement with neither column accrues every invoice line, as
-- before; with product_id it accrues only lines of that product; with product_group_id only
-- lines of that product group. The table, its row level security and its grants to passage_app
-- come from 0002 and 0008; no new table here.
ALTER TABLE rebate_agreement ADD COLUMN IF NOT EXISTS product_id uuid;
ALTER TABLE rebate_agreement ADD COLUMN IF NOT EXISTS product_group_id uuid;
ALTER TABLE rebate_agreement DROP CONSTRAINT IF EXISTS rebate_agreement_product_scope_check;
ALTER TABLE rebate_agreement ADD CONSTRAINT rebate_agreement_product_scope_check
  CHECK (product_id IS NULL OR product_group_id IS NULL);
