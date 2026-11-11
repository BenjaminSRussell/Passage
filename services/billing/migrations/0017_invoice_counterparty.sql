-- #13: what the tax service needs to file an EU sales list. A free-text invoice proposal may carry the
-- customer's country, VAT number and supply kind; the tax report then sends them with the base amount.
-- Nothing in billing stores a customer's tax registration (the party service serves none), so an
-- invoice without them reports its base amount only.
ALTER TABLE invoice
  ADD COLUMN IF NOT EXISTS counterparty_country text CHECK (counterparty_country IS NULL OR counterparty_country ~ '^[A-Z]{2}$'),
  ADD COLUMN IF NOT EXISTS counterparty_vat_id text CHECK (counterparty_vat_id IS NULL OR length(counterparty_vat_id) BETWEEN 1 AND 20),
  ADD COLUMN IF NOT EXISTS supply_kind text CHECK (supply_kind IS NULL OR supply_kind IN ('goods', 'services', 'triangular'));
