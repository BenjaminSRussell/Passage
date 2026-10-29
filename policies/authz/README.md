# Authz policy data

`data.json` seeds duties; `authz.rego` decides.

## Duty names

A duty name is the exact string the owning service passes to `RequireDuty` or `HasDuty`. Styles are mixed (platform duties are prose, purchasing and receiving are PascalCase, ledger, inventory, reporting, close and payables are dotted) and are kept as-is until the unification follow-up (#210) lands.

`services/identity/opa_contract_test.go` fails when a duty constant in the wave 11-19 services is missing from `data.json`.

## Resources

Each permission is a route template matched with `glob.match(pattern, ["/", ":"], path)`:

- `*` matches one path segment and never a `:verb` suffix, so `journals/*` and `journals/*:post` are distinct routes.
- Platform duties use `<prefix>{,/**}` (the prefix and everything beneath it).
- Seed one row per mounted method and route template, so a narrow duty cannot reach a verb route owned by another duty.

## Flag duties

`"flag": true` duties (`ledger.journal.postdirect`, `ledger.journal.postledger`, `inventory.count.approve`) are checked in a handler on a route another duty already opens. They carry no route permissions.

## Tests

`opa test policies/authz -v`, then `cd services/identity && go test ./... -count=1` (needs `opa` on PATH).

Route templates are not cross-checked against the services' routers: routes are built from helpers, loops and in-handler action dispatch, so they cannot be extracted reliably without running the routers.
