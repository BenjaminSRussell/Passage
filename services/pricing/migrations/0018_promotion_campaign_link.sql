-- Pricing context, wave 26 slice 25: a promotion plan may belong to a campaign (FR-26.10,
-- FR-26.20). The campaign must be in the same legal entity and currency (checked by the
-- service). No new table: the existing grants on promotion_plan (0008) cover the column.
ALTER TABLE promotion_plan ADD COLUMN IF NOT EXISTS campaign_id uuid;
ALTER TABLE promotion_plan ADD CONSTRAINT promotion_plan_campaign_fk
  FOREIGN KEY (tenant_id, campaign_id) REFERENCES campaign (tenant_id, id);
CREATE INDEX IF NOT EXISTS promotion_plan_campaign_ix ON promotion_plan (tenant_id, campaign_id) WHERE campaign_id IS NOT NULL;
