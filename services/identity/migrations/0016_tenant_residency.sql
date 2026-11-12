-- Tenant residency (issue 605, EU Data Boundary; wave 48 "data residency per tenant"). residency is the code that says
-- where the tenant's data may live (EU, US, UK, GLOBAL); region is where the tenant was placed. The code-to-regions
-- table is platform/go/residency (one place, also used by the evidence report), so the database only checks the
-- shape: provisioning refuses a region the residency does not allow (OPS-1705) before the row is written. Tenants that
-- exist today keep GLOBAL (no restriction) and no recorded region, so nothing changes for them until an operator sets
-- a residency; the evidence report lists a tenant with no region as unplaced.
alter table identity_tenants
  add column residency text not null default 'GLOBAL' check (residency ~ '^[A-Z][A-Z0-9_-]{1,15}$'),
  add column region text check (region is null or region ~ '^[a-z0-9][a-z0-9-]{1,62}$');
