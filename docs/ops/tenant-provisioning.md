# Tenant provisioning and decommission (FR-50.8, BR-50.9)

## Provision: `POST /api/v1/ops/tenants` (`ops.tenant.provision`, `Idempotency-Key` required)

Built in `services/ops` (#50 part 3): the residency check (step 1) and the plan, step tracking and event (step 7). The runner that
performs steps 2 to 6 reports each with `PUT /api/v1/ops/tenants/{id}/provision-steps/{action}` (`realm`, `databases`, `kafka-acls`,
`lakehouse`, `license`); see `services/ops/NOT-IMPLEMENTED.md` for what the runner still needs.
Target: <= 15 min end to end (`provisioning.timeout_min`, default 15). Five concurrent provisions.

Steps, each idempotent so a retry resumes:
1. Validate the tenant's region against its residency (OPS-1705 otherwise, see "Tenant residency" below). EU tenants go to EU regions only.
2. Create the Keycloak realm for the tenant.
3. Create databases and per-tenant schemas with RLS policies (`dbx.RLSPolicy`).
4. Create Kafka ACLs for the tenant topic prefix.
5. Create the lakehouse namespace and object-store prefixes.
6. Install the signed license (`PUT .../license`, Ed25519).
7. Emit `dev.passage.ops.tenant.provisioned.v1` with tenant_id, region, realm, databases[].

Acceptance (AC-50.6): residency EU provisions into `eu-west` with RLS, realm, ACLs and license in <= 15 min.

## Tenant residency (#605)
Every tenant record carries a **residency**: `identity_tenants.residency` (migration `services/identity/migrations/0016_tenant_residency.sql`) is
one of `EU`, `US`, `UK`, `GLOBAL`, and `identity_tenants.region` is where the tenant was placed. `POST /v1/tenants` (identity, operator only)
takes `residency` (default `GLOBAL`, which allows any region; `EU` under the `eu-data-boundary` overlay) and a `region` (required unless the residency is `GLOBAL`), and refuses a region outside the residency, or outside the cloud profile's `allowedRegions` when it lists any, with
422 `OPS-1705` ("Residency region {region} is not deployed; tenant not provisioned" in the PRD catalogue, here also the placement refusal)
before it writes anything: no tenant row, audit row or event exists for a refused placement. Tenants created before this migration are
`GLOBAL` with no region, so nothing changes for them until an operator sets a residency; the evidence report lists them as unplaced.

The map from a residency code to the regions it allows is one file, `platform/go/residency/residency.json`, embedded by the Go package
`platform/go/residency` (`residency.Check`, `residency.Allows`) and read by `deploy/multi-region/tools/residency_report.py`, so the
provisioning check and the auditor's report cannot disagree. Regions are written both as Azure region names (`westeurope`,
`swedencentral`, ...: the `eu-data-boundary` overlay's `allowedRegions` in `deploy/cloud-profiles/profiles.json`) and as the legacy ids of
`deploy/multi-region/values` (`eu-west`, `eu-central`, `eu-north`, `us-east`, `us-west`). `EU` is the EU and EFTA set of the EU Data
Boundary. Tests fail when the table drifts from the cloud profile (Go `TestTableMatchesTheCloudProfile`, Python
`test_residency_report.py`). To add a region, add it to the one residency that owns it in `residency.json`; a region is in at most one residency.

Not built yet: provisioning in the ops control plane (`POST /api/v1/ops/tenants`, the steps above) does not exist in `services/ops`, so only the
identity tenant create path enforces the check today; the ops flow must call `residency.Check(tenant.residency, region)` as its first step.
There is no route to change a tenant's residency or region after creation; moving a tenant is a data migration, not an update.

## Residency evidence report (#605)
For an auditor: tenant -> regions for the database, object store, Kafka and backups, with every tenant whose residency allows less than
where its data is flagged.

```
psql -At -c "select coalesce(json_agg(json_build_object('tenantId', tenant_id, 'name', display_name,
  'residency', residency, 'region', region)), '[]') from identity_tenants" > tenants.json
python3 deploy/multi-region/tools/residency_report.py --tenants tenants.json --json residency.json --csv residency.csv
```

It reads the tenants export, the region value files (`deploy/multi-region/values/**`, merged over the chart defaults) and the residency
table, and writes one row per tenant per data store plus a `violations` list (exit status 1 when there is one). A tenant's data is in its own
region and in the pair peer's (the replica, the MirrorMaker 2 target, the peer's backups and object store), so each store lists both.
Regions the values do not state (the database and, without `objectStore.region`, the object store; an s3 backup without `backup.s3.region`)
are derived from `region.id` and marked `derived`. Violation kinds: `outside-residency` (an EU tenant whose backup is in a US region),
`placement`, `unplaced`, `no-region-values`, `unknown-residency`. See the tool's docstring for the exact rules.

## Decommission: `DELETE /api/v1/ops/tenants/{id}` (platform.global.admin plus second approver)
1. Export all tenant data encrypted, retained for the longest applicable record-class minimum from the
   tenant's country packs (DE/FR: 10 years) unless the customer takes custody and signs a release (BR-50.9).
2. Revoke the realm and ACLs; drop tenant schemas only after the export is verified.
3. Record the decommission in the audit chain.

## Sandboxes
Sandboxes are re-created from masked production through the same provisioning path with
`provisioning.default_tier` set to DEV, TEST or UAT.
