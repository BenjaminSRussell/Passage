-- Reverse cross-reference lookup (GET /v1/integration/xref?targetId=): which legacy keys became, or were merged
-- into, a Passage record. Without these the lookup scans every mapping of the tenant. No new table, so no grants.
CREATE INDEX xref_target_idx ON migration.xref (tenant_id, target_id);
CREATE INDEX xref_merged_into_idx ON migration.xref (tenant_id, merged_into_target_id) WHERE merged_into_target_id IS NOT NULL;
