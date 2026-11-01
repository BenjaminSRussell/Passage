-- BR-9.1: a draft BOM version can be cancelled. The row is kept so the draft stays auditable;
-- cancelled is terminal, and explosion, where-used and activation checks only read active and
-- expired versions. No new table, so no grants change (passage_app already has UPDATE).
ALTER TABLE bom_version DROP CONSTRAINT IF EXISTS bom_version_status_check;
ALTER TABLE bom_version ADD CONSTRAINT bom_version_status_check
  CHECK (status IN ('draft', 'active', 'expired', 'superseded', 'cancelled'));
