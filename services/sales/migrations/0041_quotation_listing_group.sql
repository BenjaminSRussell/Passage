-- Listing check at quotation entry (FR-27.15, BR-27.13) and the customer group
-- on a quotation. customer_group stands in for the w7 customer group, as on
-- sales_order (0004): it scopes GROUP listing entries and is copied onto the
-- order a conversion creates. listing_overrides logs every denied line a user
-- with sales.order.approve accepted at entry, and why; replacing the line set
-- of a DRAFT replaces the log. No new table, so no new grant: the application
-- role already has SELECT, INSERT and UPDATE on sales_quotation.
ALTER TABLE sales_quotation ADD COLUMN IF NOT EXISTS customer_group text CHECK (customer_group IS NULL OR length(customer_group) BETWEEN 1 AND 80);
ALTER TABLE sales_quotation ADD COLUMN IF NOT EXISTS listing_overrides jsonb NOT NULL DEFAULT '[]'::jsonb;
