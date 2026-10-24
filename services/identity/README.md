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

Run `go test ./...` from this directory. The bundle is under `../../policies/authz`; `opa test ../../policies/authz` runs its Rego contract tests. The policy bundle defines 20 duties named in the Wave 2 PRD and starts with no assignments; the policy compiler must populate assignments per tenant before access is granted. Each route passes its HTTP method and resource path to the policy middleware.

The identity migrations create tenant, user and legal-entity records with forced tenant RLS; legal entities also use the shared restrictive legal-entity scope policy. The two-role PostgreSQL integration test runs when both `PASSAGE_TEST_DATABASE_URL` (a distinct migrator login) and `PASSAGE_TEST_APP_DATABASE_URL` (the actual `passage_app` login) are set. `passage_app` must be `NOSUPERUSER NOBYPASSRLS`, must not own the migrated tables, and needs only schema usage plus table DML privileges in the isolated test schema. Shared role provisioning remains a platform responsibility.

Stable problem codes in this slice: `AUTH-0001` unauthenticated or invalid token, `AUTH-0003` policy denial, `AUTH-0404` tenant or gateway identity mismatch, `AUTH-0502` JWKS unavailable, `AUTH-0503` OPA unavailable.

## Wave 2 status

This is an incomplete Wave 2 implementation. This service now has tenant, user and legal-entity persistence tables with RLS and a real two-role integration test, alongside the RS256/JWKS and OPA middleware slice. Remaining work includes Keycloak realm deployment, OIDC/SAML federation and MFA, gateway JWT/host validation, SCIM provisioning, tenant/legal-entity and role-assignment APIs, policy compilation and bundle distribution/version checks, RLS migrations and cross-tenant tests for every other service table, Envoy Gateway deployment and per-tenant rate limits, and the admin-security web module with effective-permission reporting. Shared database role provisioning and reconciliation of the `app.tenant_id` / `passage_migrate` architecture text with the current `passage.*` / `passage_app` platform contract remain a Wave 0 handoff.
