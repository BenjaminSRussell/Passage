-- Wave 27 slice 15: one-time customer orders (FR-27.22, AC-27.14).
-- sales_order.one_time_party_sealed: the walk-in buyer's name, address and tax
-- id captured on the order header, for the invoice (w29) and the open item
-- (w30) to read. It is personal data, so it is never stored in clear: the
-- column holds a platform fieldcrypt envelope (AES-GCM under a per-tenant data
-- key, bound to the tenant and to the field name sales_order.one_time_party).
-- one_time_country is the buyer's ISO 3166 country, kept in clear for routing
-- and tax and to mark the order as one-time without opening the party. Both
-- are null on an ordinary order. The order is sold to the legal entity's
-- shared one-time account; the party role itself is wave 7.
-- sales_parameter.one_time_order_limit(+_currency): the most a one-time order
-- may be worth (PRD 5.27.13); both null means the defaults (5,000 USD,
-- 4,500 EUR). No new table, so no new grant: sales_order and sales_parameter
-- are already granted in 0009 and 0015.

ALTER TABLE sales_order ADD COLUMN IF NOT EXISTS one_time_party_sealed bytea;
ALTER TABLE sales_order ADD COLUMN IF NOT EXISTS one_time_country char(2) CHECK (one_time_country ~ '^[A-Z]{2}$');
ALTER TABLE sales_order DROP CONSTRAINT IF EXISTS sales_order_one_time_pair;
ALTER TABLE sales_order ADD CONSTRAINT sales_order_one_time_pair
  CHECK ((one_time_party_sealed IS NULL) = (one_time_country IS NULL));

ALTER TABLE sales_parameter ADD COLUMN IF NOT EXISTS one_time_order_limit numeric(20,2) CHECK (one_time_order_limit >= 0);
ALTER TABLE sales_parameter ADD COLUMN IF NOT EXISTS one_time_order_limit_currency char(3);
ALTER TABLE sales_parameter DROP CONSTRAINT IF EXISTS sales_parameter_one_time_limit_pair;
ALTER TABLE sales_parameter ADD CONSTRAINT sales_parameter_one_time_limit_pair
  CHECK ((one_time_order_limit IS NULL) = (one_time_order_limit_currency IS NULL));
