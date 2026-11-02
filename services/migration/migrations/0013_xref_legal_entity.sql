-- Legal entity of a cross-reference row (PSG-10, closes the cross-legal-entity risk left by integration track S5).
-- NULL means tenant-wide master data: the owning service keeps the record without a legal entity (party, product,
-- item). A row for a record the owning service scopes to one legal entity (customer and vendor roles, open AR items,
-- the sales orders and invoices an outbound mapping records) carries that legal entity. The loader writes it for the
-- rows it loads, xref:record for the rows it creates, and 0014 backfills the rows an earlier run loaded.
-- No new table, so no grants (SELECT, INSERT, UPDATE on migration.xref were granted by 0001).
-- Only catalog changes here: a nullable column without a default and the policy swap need the table lock for an
-- instant, and the backfill is a separate migration so this lock is not held while it runs.
ALTER TABLE migration.xref ADD COLUMN legal_entity_id uuid;

-- A table with a legal_entity_id column carries the usual restrictive policy on it (tools/rlscheck): the all-entities
-- scope sees every row, a scoped caller only the rows of its own legal entities. A NULL row is therefore visible
-- under the all-entities scope only; the outbound store keeps that scope and filters (NULL or the token's legal
-- entity) itself, because the policy cannot admit NULL (rlscheck refuses a null-admitting policy). The policy only
-- widens what a scoped session saw before (nothing): every path that read the table under '*' still sees every row.
DROP POLICY xref_legal_entity_scope ON migration.xref;
CREATE POLICY xref_legal_entity_scope ON migration.xref AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
  WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
