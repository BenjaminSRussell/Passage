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

Run `go test ./...` from this directory. The bundle is under `../../policies/authz`; `opa test ../../policies/authz` runs its Rego contract tests. The policy bundle defines 22 duties (the Wave 2 seed plus View number sequences and Record audit changes for the Wave 5 service paths) and starts with no assignments; the policy compiler must populate assignments per tenant before access is granted. Each route passes its HTTP method and resource path to the policy middleware.

Stable problem codes in this slice: `AUTH-0001` unauthenticated or invalid token, `AUTH-0003` policy denial, `AUTH-0404` tenant or gateway identity mismatch, `AUTH-0502` JWKS unavailable, `AUTH-0503` OPA unavailable.

## Wave 2 status

This is the first isolated slice, not a complete Wave 2 implementation. The following remain: Keycloak realm and deployment, OIDC/SAML federation and MFA, gateway JWT/host validation, SCIM provisioning, persistent tenant/legal-entity and role-assignment APIs, policy compilation and bundle distribution/version checks, database RLS migrations and integration tests, Envoy Gateway deployment and per-tenant rate limits, and the admin-security web module with effective-permission reporting. Those need follow-up owned-path PRs.
