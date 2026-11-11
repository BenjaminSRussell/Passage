# Authz policy data

`data.json` seeds duties; `authz.rego` decides.

## Duty names

A duty name is the exact string the owning service passes to `RequireDuty` or `HasDuty`, and the string a role assignment, a service grant and the web app's nav gates hold.

### Convention (#210)

Every duty is dotted lower-case: `<service>.<object>.<action>`.

- `<service>` is the owning service's duty prefix: usually its directory name without a language suffix (`fieldservice`, `agents` for `agents-ts`, `mcp` for `mcp-ts`), or the prefix it already uses (`procurement` for purchasing, `receivables`, `migration` for both migration and the migration factory, `platform` for platform-health and the platform-wide duties).
- `<object>` is the business object, singular where it names one thing (`purchase-order`, `legal-hold`, `rate-card`).
- `<action>` is the verb (`read`, `write`, `view`, `maintain`, `post`, `approve`, `run`, `admin`, `record-delivery`). The `self`, `manage` and `admin` actions of `hr.*` follow the existing `hr.performance.*` split: the employee, the manager and the HR administrator.
- `<service>.<action>` (two segments) is allowed when the duty covers the whole service rather than one object: `receivables.view`, `projects.manage`, `edi.admin`, `budget.configure`.
- Each segment is `[a-z][a-z0-9]*`, with `-` between words. No capitals, spaces or underscores.

`TestDutyNamesFollowConvention` (`services/identity/data_json_test.go`) fails on a duty in `data.json`, a service grant or a service principal that does not match this shape.

Not yet three-segment: the payables duties seeded before the convention name the object first and leave out the service (`aging.read`, `payment-run.execute`, `settlement.read`, `vendor-block.manage`), as do `period.*` (close), `open-item.read`, `employee-vendor.maintain` and `pii.read`. They match the shape, so they were not renamed in #210.

The PRD (`docs/prd`, `docs/PRD-full.md`) keeps its own duty identifiers (`WMS_WORK_SUPERVISE`, `ARView`); a service README says which seeded duty implements a PRD duty when the names differ.

### Renames and role assignments

#210 renamed every PascalCase, SCREAMING_CASE and prose duty, and the underscore and four-segment ones (`party.vendor_bank.*`, `compliance.legal_hold.*`, `pricing.rebate.claim.approve`), in one change across `data.json`, the rego tests, the service constants, `platform/go/tenant` (service grants, service principals, `DutyAllScope`, now `org.switcher.all-scope`) and the web app. The old-to-new table is `services/identity/migrations/0010_unify_duty_names.sql`, which also moves every stored role assignment to the new name. There is no alias: a token or role assignment that still carries an old name grants nothing. Outside identity's role assignments, an old name can survive where it was stored or configured outside this repository, which the migration does not reach: the Keycloak duties mapper and client scopes, integration-hub `hub_api_client.scopes`, case-category access duties, and SoD rules entered by users.

`services/identity/opa_contract_test.go` scans every Go service under `services/` and `tools/migration-factory`. It fails when a duty constant or `RequireDuty`/`HasDuty` string literal is missing from `data.json`, and when a call passes an argument it cannot resolve to a constant or literal. Call sites that pass a looked-up duty (route tables, `anyDuty` loops) are listed with a reason in `dynamicDutyArguments`; an entry that no longer matches a call fails the test.

## Operator-only duties

`"operator_only": true` duties (the `ops.*` duties of the ops control plane) act on platform-wide state, not on one tenant's data. `authz.rego` grants them only when the subject's tenant is `data.operator_tenant_id`, which identity writes into the compiled bundle from `IDENTITY_OPERATOR_TENANT_ID`; without it they grant nothing. Identity also refuses to assign them in another tenant and drops any such assignment from the bundle, and the ops service serves the operator tenant only.

## Resources

Each permission is a route template matched with `glob.match(pattern, ["/", ":"], path)`:

- `*` matches one path segment and never a `:verb` suffix, so `journals/*` and `journals/*:post` are distinct routes.
- Platform duties use `<prefix>{,/**}` (the prefix and everything beneath it).
- Seed one row per mounted method and route template, so a narrow duty cannot reach a verb route owned by another duty.
- Routes that dispatch a `:verb` inside one handler (production order commands, down payments) are seeded as one `*:verb` row per verb on the duty that handler enforces.

## Costing route prefixes

`costing` (Wave 20 inventory costing, dotted `costing.*` duties) owns `/v1/costing/...`. `productcost` (Wave 39 product costing, `productcost.*` duties) owns `/v1/product-costing/...`, so the two duty families never share a route template (#342). In the web dev proxy only, `/api/v1/product-costing/...` resolves to `PASSAGE_SVC_PRODUCT_COSTING_URL`; `deploy/gateway/httproutes.yaml` has no productcost route yet.

## Path input

The PEP must pass the decoded `r.URL.Path` with no query string, or the route template (`/v1/vendors/*`). `*` matches an empty segment, so `/v1/vendors/` matches `/v1/vendors/*`, and an encoded `%3A` is not a `:` until it is decoded: a raw path can slip past a `*:verb` row or match a plain `*` row it should not.

## Flag duties

`"flag": true` duties (for example `ledger.journal.postdirect`, `budget.override`, `org.hierarchy.backdate`) are checked in a handler on a route another duty already opens. They carry no route permissions. `tax.code.maintain` is a flag until a tax-code read route is mounted. A duty can own routes and still be checked in handlers: `inventory.count.approve` opens `count-sheets/*:approve` and `count-sheets/*/lines/*:recount`, and the inventory handlers also read it with `HasDuty` for over-tolerance `:post`, marking open tags MISSING on `:transfer-tags`, and unmasking blind counts.

## Tests

`opa test --timeout 120s policies/authz -v` (the default 5 s per-test timeout is too short for the larger duty catalogue), then `cd services/identity && go test ./... -count=1` (needs `opa` on PATH).

Route templates are not cross-checked against the services' routers: routes are built from helpers, loops and in-handler action dispatch, so they cannot be extracted reliably without running the routers.
