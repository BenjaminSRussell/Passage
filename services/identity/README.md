# Identity authorization integration

This package implements the Wave 2 service-side OPA decision client. Services inject it into `platform/authz.Require`; every protected route asks the OPA sidecar before its handler runs. An unreachable or malformed policy response returns an error so the route fails closed.

```go
policy, err := identity.NewOPAClient("http://127.0.0.1:8181", nil)
if err != nil {
	log.Fatal(err)
}
router.Put("/v1/vendors/{id}", authz.Require(policy, http.MethodPut, "/v1/vendors")(handler))
```

Run `go test ./...` from this directory. The bundle is under `../../policies/authz`; `opa test ../../policies/authz` runs its Rego contract tests. The policy data seeds the 20 duties named in the Wave 2 PRD. Each route must pass its HTTP method and path to the policy middleware.

## Wave 2 status

This is the first isolated slice, not a complete Wave 2 implementation. The following remain: Keycloak realm and deployment, OIDC/SAML federation and MFA, JWT validation and tenant/host matching at the gateway and services, SCIM provisioning, persistent tenant/legal-entity and role-assignment APIs, policy compilation and bundle distribution, database RLS migrations and integration tests, Envoy Gateway deployment and rate limits, and the admin-security web module with effective-permission reporting. Those need follow-up owned-path PRs.
