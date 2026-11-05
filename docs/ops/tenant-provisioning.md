# Tenant provisioning and decommission (FR-50.8, BR-50.9)

## Provision: `POST /api/v1/ops/tenants` (platform.global.admin, `Idempotency-Key` required)
Target: <= 15 min end to end (`provisioning.timeout_min`, default 15). Five concurrent provisions.

Steps, each idempotent so a retry resumes:
1. Validate residency region is deployed (OPS-1705 otherwise). EU tenants go to EU regions only.
2. Create the Keycloak realm for the tenant.
3. Create databases and per-tenant schemas with RLS policies (`dbx.RLSPolicy`).
4. Create Kafka ACLs for the tenant topic prefix.
5. Create the lakehouse namespace and object-store prefixes.
6. Install the signed license (`PUT .../license`, Ed25519).
7. Emit `dev.passage.ops.tenant.provisioned.v1` with tenant_id, region, realm, databases[].

Acceptance (AC-50.6): residency EU provisions into `eu-west` with RLS, realm, ACLs and license in <= 15 min.

## Decommission: `DELETE /api/v1/ops/tenants/{id}` (platform.global.admin plus second approver)
1. Export all tenant data encrypted, retained for the longest applicable record-class minimum from the
   tenant's country packs (DE/FR: 10 years) unless the customer takes custody and signs a release (BR-50.9).
2. Revoke the realm and ACLs; drop tenant schemas only after the export is verified.
3. Record the decommission in the audit chain.

## Sandboxes
Sandboxes are re-created from masked production through the same provisioning path with
`provisioning.default_tier` set to DEV, TEST or UAT.
