-- Journal name posting restriction and description language (PRD 2.11 parameter
-- table, FR-11.9, FR-11.17) and the ledger's reinstatement flag (FR-11.8,
-- allow_reinstate). Additive only: every column has a default that keeps the
-- behaviour before this migration (anyone with the duty may post, descriptions
-- come from the legal entity's language, a reversal can be reversed again).
-- No new table, so no new grant: journal_name and ledger are already granted
-- SELECT, INSERT, UPDATE to passage_app.

ALTER TABLE journal_name
    ADD COLUMN IF NOT EXISTS posting_restriction text NOT NULL DEFAULT 'none'
        CHECK (posting_restriction IN ('none', 'own_journals_only')),
    ADD COLUMN IF NOT EXISTS description_language text NOT NULL DEFAULT 'legal_entity'
        CHECK (description_language IN ('legal_entity', 'user'));

ALTER TABLE ledger
    ADD COLUMN IF NOT EXISTS allow_reinstate boolean NOT NULL DEFAULT true;
