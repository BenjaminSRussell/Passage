-- Wave 16 slice 21: editing a CREATED transfer order (FR-16.15). A line can be removed
-- before anything ships, so the store deletes from transfer_order_line. No new table and
-- no new column; the only change is the DELETE privilege on that one table (it already
-- holds SELECT, INSERT and UPDATE). Shipped lines are never deleted: the service refuses
-- the edit once the order has left CREATED.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT DELETE ON transfer_order_line TO passage_app';
  END IF;
END
$$;
