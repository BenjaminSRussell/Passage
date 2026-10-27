-- Revrec slice 4: the ratable basis of a contract (§5.29.13 ratable_basis,
-- FR-29.8 "RATABLE (daily or monthly)"). The PRD sets it per legal entity; until
-- the legal-entity setting can be read (wave 11) the caller sends it on the
-- contract and it is stored with the contract, so a contract's schedule can
-- always be explained from its own row. Existing contracts were planned on the
-- day-weighted basis, which is the default.
ALTER TABLE revenue_contract
  ADD COLUMN IF NOT EXISTS ratable_basis text NOT NULL DEFAULT 'DAILY'
    CHECK (ratable_basis IN ('DAILY', 'MONTHLY'));
