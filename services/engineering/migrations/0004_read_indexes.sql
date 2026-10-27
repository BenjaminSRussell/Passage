CREATE INDEX IF NOT EXISTS "IX-ENG-06" ON route_version (tenant_id, site_id, status);
CREATE INDEX IF NOT EXISTS "IX-ENG-07" ON resource (tenant_id, site_id, type);
