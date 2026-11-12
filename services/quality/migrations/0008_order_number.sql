-- Quality order numbers (#500). An order had only its id, so pages showed a short
-- id and a form could not pick an order by anything readable. Every order now
-- gets QO-000001, QO-000002, ... per tenant, from a counter row the insert
-- trigger takes under a row lock (a rolled-back insert gives its number back).
ALTER TABLE quality_order ADD COLUMN IF NOT EXISTS number text;

CREATE TABLE IF NOT EXISTS quality_order_counter (
  tenant_id uuid PRIMARY KEY,
  last_number bigint NOT NULL CHECK (last_number > 0)
);
ALTER TABLE quality_order_counter ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_order_counter FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS quality_order_counter_tenant_access ON quality_order_counter;
CREATE POLICY quality_order_counter_tenant_access ON quality_order_counter AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE OR REPLACE FUNCTION quality_order_assign_number() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  n bigint;
BEGIN
  IF NEW.number IS NULL THEN
    INSERT INTO quality_order_counter AS c (tenant_id, last_number) VALUES (NEW.tenant_id, 1)
      ON CONFLICT (tenant_id) DO UPDATE SET last_number = c.last_number + 1
      RETURNING last_number INTO n;
    NEW.number := 'QO-' || lpad(n::text, greatest(6, length(n::text)), '0');
  END IF;
  RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS quality_order_number ON quality_order;
CREATE TRIGGER quality_order_number BEFORE INSERT ON quality_order FOR EACH ROW EXECUTE FUNCTION quality_order_assign_number();

-- Backfill: orders without a number are numbered per tenant in the order they
-- were created, after the tenant's counter (so a re-run never reuses a number
-- the trigger already gave out), and the counter moves on past them. It reads
-- every tenant, so it runs only under a role that bypasses row-level security;
-- under passage_app it is skipped with a notice (the orders keep no number and
-- show their short id).
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) THEN
    RAISE NOTICE 'quality order number backfill skipped: role % does not bypass row-level security', current_user;
    RETURN;
  END IF;
  WITH numbered AS (
    SELECT o.tenant_id, o.id,
           coalesce(c.last_number, 0) + row_number() OVER (PARTITION BY o.tenant_id ORDER BY o.created_at, o.id) AS n
    FROM quality_order o LEFT JOIN quality_order_counter c ON c.tenant_id = o.tenant_id
    WHERE o.number IS NULL
  ), assigned AS (
    UPDATE quality_order o SET number = 'QO-' || lpad(numbered.n::text, greatest(6, length(numbered.n::text)), '0')
    FROM numbered WHERE o.tenant_id = numbered.tenant_id AND o.id = numbered.id
    RETURNING numbered.tenant_id, numbered.n
  )
  INSERT INTO quality_order_counter (tenant_id, last_number)
    SELECT tenant_id, max(n) FROM assigned GROUP BY tenant_id
    ON CONFLICT (tenant_id) DO UPDATE SET last_number = greatest(quality_order_counter.last_number, excluded.last_number);
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS quality_order_number_key ON quality_order (tenant_id, number) WHERE number IS NOT NULL;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON quality_order_counter TO passage_app';
  END IF;
END
$$;
