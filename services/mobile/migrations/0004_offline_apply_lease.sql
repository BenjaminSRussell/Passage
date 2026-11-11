-- A replay marks the transaction it is applying at the warehouse for a short lease (#522). A supervisor who rejects a
-- PENDING transaction while the lease runs is told to wait, so a transaction is never recorded REJECTED while its effect
-- reaches the warehouse. A lease left by a replay that died simply runs out.
ALTER TABLE offline_transaction ADD COLUMN IF NOT EXISTS apply_until timestamptz;
