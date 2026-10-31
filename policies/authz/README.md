# Authz policy data

`data.json` seeds duties; `authz.rego` decides.

## Duty names

A duty name is the exact string the owning service passes to `RequireDuty` or `HasDuty`. Styles are mixed (platform duties are prose, purchasing and receiving are PascalCase, ledger, inventory, reporting, close and payables are dotted) and are kept as-is until the unification follow-up (#210) lands.

`services/identity/opa_contract_test.go` scans every Go service under `services/` and `tools/migration-factory`. It fails when a duty constant or `RequireDuty`/`HasDuty` string literal is missing from `data.json`, and when a call passes an argument it cannot resolve to a constant or literal. Call sites that pass a looked-up duty (route tables, `anyDuty` loops) are listed with a reason in `dynamicDutyArguments`; an entry that no longer matches a call fails the test.

## Resources

Each permission is a route template matched with `glob.match(pattern, ["/", ":"], path)`:

- `*` matches one path segment and never a `:verb` suffix, so `journals/*` and `journals/*:post` are distinct routes.
- Platform duties use `<prefix>{,/**}` (the prefix and everything beneath it).
- Seed one row per mounted method and route template, so a narrow duty cannot reach a verb route owned by another duty.
- Routes that dispatch a `:verb` inside one handler (production order commands, down payments) are seeded as one `*:verb` row per verb on the duty that handler enforces.

## Known collision: costing routes

`productcost` (`CostingView`, `CostingMaintain`) and `costing` (`costing.read`, `costing.reconcile`, `costing.version.write`) both mount `/v1/costing/reconciliations` and `/v1/costing/versions`. Both duty sets are seeded for those templates, so either family reaches either service's route. Fixing it needs distinct route prefixes in the routers; the issue is tracked outside this file.

## Path input

The PEP must pass the decoded `r.URL.Path` with no query string, or the route template (`/v1/vendors/*`). `*` matches an empty segment, so `/v1/vendors/` matches `/v1/vendors/*`, and an encoded `%3A` is not a `:` until it is decoded: a raw path can slip past a `*:verb` row or match a plain `*` row it should not.

## Flag duties

`"flag": true` duties (for example `ledger.journal.postdirect`, `inventory.count.approve`, `budget.override`, `org.hierarchy.backdate`) are checked in a handler on a route another duty already opens. They carry no route permissions. `Maintain tax codes` and `ConsolidationView` are flags until a tax-code or consolidation read route is mounted.

## Tests

`opa test --timeout 120s policies/authz -v` (the default 5 s per-test timeout is too short for the larger duty catalogue), then `cd services/identity && go test ./... -count=1` (needs `opa` on PATH).

Route templates are not cross-checked against the services' routers: routes are built from helpers, loops and in-handler action dispatch, so they cannot be extracted reliably without running the routers.
