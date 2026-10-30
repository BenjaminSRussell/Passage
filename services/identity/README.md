# Identity authorization integration

This package implements a service-side JWT verifier and OPA decision client. Configure `JWTVerifier` with a tenant's trusted issuer, expected audience, tenant ID, and JWKS URL. Route requests through its middleware before `RequireOPA`; the verifier checks RS256 signature, issuer, audience, `sub`, `tid`, `iat`, `exp`, and `nbf`, then overwrites the untrusted gateway principal with the verified subject. `RequireOPA` rejects contexts without the verifier's private marker. Duties are looked up in OPA's tenant-scoped bundle assignments; callers cannot provide duties in a header or token. An unreachable or malformed policy response returns 503.

```go
policy, err := identity.NewOPAClient("http://127.0.0.1:8181", nil)
if err != nil {
	log.Fatal(err)
}
verifier, err := identity.NewJWTVerifier(issuer, "passage-api", tenantID, jwksURL, nil)
if err != nil {
	log.Fatal(err)
}
r.With(verifier.Middleware, identity.RequireOPA(policy, http.MethodPut, "/v1/vendors")).Put("/v1/vendors/{id}", handler)
```

Run `go test ./...` from this directory. The bundle is under `../../policies/authz`; `opa test ../../policies/authz` runs its Rego contract tests. The policy bundle defines 114 duties and starts with no assignments; the policy compiler must populate assignments per tenant before access is granted. Each route passes its HTTP method and resource path to the policy middleware.

Stable problem codes in this slice: `AUTH-0001` unauthenticated or invalid token, `AUTH-0003` policy denial, `AUTH-0404` tenant or gateway identity mismatch, `AUTH-0502` JWKS unavailable, `AUTH-0503` OPA unavailable.

## Wave 2 status

This is the first isolated slice, not a complete Wave 2 implementation. The following remain: Keycloak realm and deployment, OIDC/SAML federation and MFA, gateway JWT/host validation, SCIM provisioning, persistent tenant/legal-entity and role-assignment APIs (the tenant, user, legal-entity and effective-dated role-assignment tables with forced RLS live in `migrations/`), policy compilation and bundle distribution/version checks, Envoy Gateway deployment and per-tenant rate limits, and the admin-security web module with effective-permission reporting. Those need follow-up owned-path PRs.

## Schema grants and scoping

`passage_app` has SELECT and INSERT only on `identity_tenants` (insert is limited by RLS to the tenant id the transaction is scoped to): status changes and renames belong to the migrator, so a tenant cannot reactivate itself, rename its realm, or probe other tenants' realm names (only the operator tenant can read other tenants). `identity_role_assignments` rejects overlapping grants of the same user, role and legal entity (an exclusion constraint, open-ended `valid_to` counts as infinity), so a revoke always takes effect. A caller scoped to specific legal entities does not see tenant-wide assignments (null `legal_entity_id`); only a `*` scope does.

## Effective permissions

`LoadCatalogue` parses `../../policies/authz/data.json` into an engine that mirrors `authz.rego`: `Evaluate(duties, method, resource)` returns allow, the lexically first held duty that grants it, and (on deny) the policy's least-privileged `duty_missing`. `Effective(duties)` evaluates every distinct resource pattern and method in the catalogue at a sample path (`{,/**}` becomes `/item-1`, `*` becomes `item-1`). Globs outside literal text, `*` and a trailing `{,/**}` fail at load so the engine cannot drift from OPA silently.

`NewEffectivePermissionsAPI(pool, catalogue).Register(r)` serves `GET /v1/identity/users/{id}/effective-permissions` (duty `View effective permissions`, OpenAPI in `api/openapi.yaml`). Until the role catalogue exists (#83), `identity_role_assignments.role` holds a duty name. Active assignments (`valid_from <= now() < valid_to`) are resolved: tenant-wide rows always, plus rows for `?legalEntityId=` when given. Roles that are not seeded duties are listed in `ignoredRoles` and grant nothing, as in OPA. A caller without the all-entity scope, scoped or unscoped, receives 403.

`effective_test.go` checks the engine against the real policy (OPA server, same harness as `opa_contract_test.go`): 100 routes sampled evenly across the sorted catalogue, times eight fixture principals, must match OPA on allow and `duty_missing`; the full report is also compared row by row for four principals.

## Tenant, legal-entity and role-assignment APIs

`NewTenantsAPI(pool, operatorTenantID)`, `NewLegalEntitiesAPI(pool)` and `NewRoleAssignmentsAPI(pool, catalogue, operatorTenantID)` serve `/v1/tenants` (create, list, get), `/v1/legal-entities` (create, list, get) and `/v1/authz/assignments` (assign, list by `userId` and/or `role` (one is required), get, `POST .../{id}/end` and its alias `DELETE .../{id}`). They reuse the seeded duties `Maintain tenants`, `Maintain legal entities` and `Maintain security roles` and the catalogue's resource patterns, so the duty count stays at 114.

All three run on the ordinary `passage_app` pool; no path bypasses RLS. Tenant routes are operator-only: the caller must hold `Maintain tenants` and belong to `operatorTenantID`. Create sets `passage.tenant_id` to the new tenant's id inside its transaction so the row passes its own RLS check (`passage_app` holds INSERT, not UPDATE or DELETE, on `identity_tenants`) and writes the audit row in the operator tenant too. List and get read through the `identity_tenants_operator_read` policy, which admits SELECT only when the caller's tenant is the single row of `identity_operator`; the migrator or deploy seeds that row with the same id as `operatorTenantID`.

Tenant-level writes (creating a legal entity, a tenant-wide assignment, ending one) run with an explicit `*` legal-entity scope set by the handler, and only when the request carries no legal-entity scope and the caller holds the matching admin duty (`Maintain legal entities` or `Maintain security roles`); every other request runs with the caller's own scope. Assigning a role to yourself is refused (403 `IAM-0416`), `Maintain tenants` can only be granted in the operator tenant, and past or inactive-legal-entity assignments are refused.

An assigned role must be a seeded duty name. The no-overlap rule for one user, role and legal entity is the `identity_role_assignments_no_overlap` exclusion constraint (409 `IDN-ASSIGNMENT-OVERLAP`); a revoke is `end` (never a delete), which only shortens; ending a future-dated assignment sets `valid_to = valid_from`, an empty range that overlaps nothing and grants nothing. Every mutation writes an `identity_audit` row (actor from `X-Passage-User`) and an `outbox` event (`dev.passage.identity.{tenant,legalentity,roleassignment}.{created,assigned,ended}.v1`) in the same transaction as the change.

Not implemented (issue #83 remainder): SCIM 2.0 `/Users` and `/Groups`, OPA bundle build and distribution, Keycloak push (#80), gateway host-to-tenant mapping (#213), and a `cmd/identity` server binary (needs per-tenant JWT verifier and OPA wiring).
