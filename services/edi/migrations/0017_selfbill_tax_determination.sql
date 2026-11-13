-- #35 (35-5) round 3: the VAT rate a self-billed line is checked at comes from the tax service. The partner names what the tax
-- engine determines it from (the customer's tax country and group, the item tax group of the deliveries); tax_rate_pct stays the
-- rate used when no tax country is set or the tax service is not configured.
ALTER TABLE self_billing_config ADD COLUMN IF NOT EXISTS tax_country text NOT NULL DEFAULT '' CHECK (tax_country = '' OR tax_country ~ '^[A-Z]{2}$');
ALTER TABLE self_billing_config ADD COLUMN IF NOT EXISTS tax_group text NOT NULL DEFAULT '' CHECK (char_length(tax_group) <= 40);
ALTER TABLE self_billing_config ADD COLUMN IF NOT EXISTS item_tax_group text NOT NULL DEFAULT '' CHECK (char_length(item_tax_group) <= 40);
ALTER TABLE self_billing_document ADD COLUMN IF NOT EXISTS tax_rate_source text NOT NULL DEFAULT 'PARTNER' CHECK (tax_rate_source IN ('PARTNER', 'TAX'));
