# API gateway (Envoy Gateway)

Part of #82 (item b). Services trust the `X-Passage-*` identity headers read by `platform/go/tenant` and `platform/go/authz`, so the gateway must be the only source of them. Live enforcement proof is tracked on #213.

## Request path

1. `ClientTrafficPolicy/passage-api-strip-identity-headers` removes `X-Passage-Tenant`, `-Legal-Entity`, `-User`, `-Duties` and `-Tier` in `earlyRequestHeaders`, before any HTTP filter runs. A route-level `RequestHeaderModifier` would run after JWT auth and delete the headers the JWT just set, so it is not used.
2. `SecurityPolicy/passage-api-jwt` (on every HTTPRoute) validates the bearer token (issuer, audience `passage-api`, signature from the JWKS) and maps claims to headers: `tid` to `X-Passage-Tenant`, `le` to `X-Passage-Legal-Entity`, `sub` to `X-Passage-User`, `duties` to `X-Passage-Duties`, `tier` to `X-Passage-Tier`. Requests without a valid token get 401. A token whose `tid` is the nil platform tenant (`00000000-0000-0000-0000-000000000000`) is denied.
3. `BackendTrafficPolicy/passage-api-tenant-rate-limit` applies a global per-tenant limit keyed on `X-Passage-Tenant` by tier from `X-Passage-Tier`: starter 600/min, professional 3000/min, enterprise 12000/min. A tenant-only rule at 12000/min caps tokens with a missing or unknown tier. Burst allowances (100/500/2000) are not expressible in this API and are not configured.

## Routes

Each service has an `HTTPRoute` named after it. `/api/v1/<svc>` is rewritten to `/v1/<svc>` (the BFF convention from #199); `/v1/<svc>` passes through for partners and agents. Health endpoints are not routed. Add a service by adding its HTTPRoute and listing it in the `targetRefs` of the SecurityPolicy and BackendTrafficPolicy; `deploy/scripts/test_gateway.py` fails if a route is left out.

## Dev JWKS

`passage-dev-jwks` holds a static placeholder public key (kid `passage-dev-placeholder`); no private key is kept, so nothing can mint accepted tokens until #80 supplies an IdP. To switch to the IdP, replace `localJWKS` in the SecurityPolicy provider with `remoteJWKS: {uri: https://<idp>/realms/<realm>/protocol/openid-connect/certs}` and set `issuer` to the realm URL. Realm-per-tenant needs one provider per realm.

## Assumptions to confirm with #80 and #213

- Claim names `tid` (matches `services/identity/jwt.go`), `le`, `duties`, `tier` are the Keycloak mapper outputs; `duties` must be a string or string array.
- The Envoy Gateway install (#213) provides the CRDs, `ClientTrafficPolicy.headers.earlyRequestHeaders` support, and a global rate-limit service backed by Valkey. The Argo CD app `deploy/envs/dev/apps/gateway.yaml` is annotated `passage.dev/requires` and uses `SkipDryRunOnMissingResource` so it can sync once the CRDs exist.
- Services are still reachable inside the cluster without the gateway; restricting that is item 2 of #82.

## Tests

`make -C deploy test-gateway` renders the kustomization with `kubectl kustomize` and asserts header removal covers every header in `tenant.go` and every claim-mapped header, every route is under the SecurityPolicy and the rate limit, the limit is keyed on tenant, and the `/api` rewrite. It needs `pyyaml`.
