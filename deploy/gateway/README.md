# API gateway (Envoy Gateway)

Part of #82 (item b). Services trust the `X-Passage-*` identity headers read by `platform/go/tenant` and `platform/go/authz`, so the gateway must be the only source of them. Live enforcement proof is tracked on #213.

Requires Envoy Gateway v1.4 or newer: `localJWKS` on the JWT provider and `shared` on rate-limit rules do not exist before it. The CRDs for v1.4.0 (Gateway API `GatewayClass`, `Gateway`, `HTTPRoute`; Envoy Gateway `ClientTrafficPolicy`, `SecurityPolicy`, `BackendTrafficPolicy`) are vendored in `deploy/third_party/envoy-gateway-v1.4.0/crds.yaml` (Apache-2.0, extracted from the v1.4.0 `install.yaml`) and the manifests are validated against them with kubeconform.

## Request path

1. `ClientTrafficPolicy/passage-api-strip-identity-headers` removes `X-Passage-Tenant`, `-Legal-Entity`, `-LegalEntity`, `-User`, `-Duties`, `-Tier` and `-Service-Token` in `earlyRequestHeaders`, before any HTTP filter runs. `X-Passage-LegalEntity-Scope` is the one deliberate pass-through: a client sends `ALL` to ask for the all-entity read view, and `platform/go/tenant` honours it only on GET/HEAD/OPTIONS and only when the JWT-derived `X-Passage-Duties` holds `org.switcher.all_scope`; it grants nothing on its own. A route-level `RequestHeaderModifier` would run after JWT auth and delete the headers the JWT just set, so it is not used.
2. `SecurityPolicy/passage-api-jwt` (targets the Gateway, so every route and any future route is covered) validates the bearer token (issuer, audience `passage-api`, signature from the JWKS) and maps claims to headers: `tid` to `X-Passage-Tenant`, `le` to `X-Passage-Legal-Entity`, `sub` to `X-Passage-User`, `duties` to `X-Passage-Duties`, `tier` to `X-Passage-Tier`. Requests without a valid token get 401. A token whose `tid` is the nil platform tenant in its canonical form (`00000000-0000-0000-0000-000000000000`) is denied here; claim matching is exact-string only, so uppercase or unhyphenated spellings are not matched at the gateway. `platform/go/tenant` canonicalizes and rejects them (#257).
3. `BackendTrafficPolicy/passage-api-tenant-rate-limit` applies a global per-tenant limit keyed on `X-Passage-Tenant` by tier from `X-Passage-Tier`: starter 600/min, professional 3000/min, enterprise 12000/min. Every rule is `shared: true`, so a tenant's budget is shared across all routes rather than per route. A token with a missing or unknown tier matches the inverted-regex fallback rule and is limited to the starter 600/min (fail closed). Burst allowances (100/500/2000) are not expressible in this API and are not configured.

## Routes and listeners

Each service has an `HTTPRoute` named after it. `/api/v1/<svc>` is rewritten to `/v1/<svc>` (the BFF convention from #199); `/v1/<svc>` passes through for partners and agents. Health endpoints are not routed. Add a service by adding its HTTPRoute and listing it in the `targetRefs` of the BackendTrafficPolicy (the SecurityPolicy targets the Gateway); `deploy/scripts/test_gateway.py` fails if a route is left out.

The Gateway has two listeners. `https` (443, TLS terminate, Secret `passage-api-tls`, to be issued by cert-manager or supplied by the environment) is the listener for stage and prod. `http-dev` (80) is for dev only; remove it in any non-dev environment.

## Dev JWKS and claim contract

`passage-dev-jwks` holds a static placeholder public key (kid `passage-dev-placeholder`); no private key is kept, so nothing can mint accepted tokens until #80 supplies an IdP. To switch to the IdP, replace `localJWKS` in the SecurityPolicy provider with `remoteJWKS: {uri: https://<idp>/realms/<realm>/protocol/openid-connect/certs}` and set `issuer` to the realm URL. Realm-per-tenant needs one provider per realm.

`duties` must be a single string claim holding the comma-joined duty names (for example `ap_clerk,approver`), because `tenant.go` splits `X-Passage-Duties` on commas. Envoy base64-JSON-encodes array claims when copying them to a header, so a string array breaks parsing. In Keycloak, use a "Hardcoded claim" or script mapper that emits one string (JSON type String, multivalued off); do not use the multivalued "User Realm Role" style mapper.

## Assumptions to confirm with #80 and #213

- Claim names `tid` (matches `services/identity/jwt.go`), `le`, `duties`, `tier` are the Keycloak mapper outputs.
- The Envoy Gateway v1.4+ install (#213) provides the CRDs, `ClientTrafficPolicy.headers.earlyRequestHeaders` support, and a global rate-limit service backed by Valkey. The Argo CD app `deploy/envs/dev/apps/gateway.yaml` is annotated `passage.dev/requires` and uses `SkipDryRunOnMissingResource` so it can sync once the CRDs exist.
- Services are still reachable inside the cluster without the gateway; restricting that is item 2 of #82.
- Host-to-tenant matching (a request host must resolve to the token's tenant) is deferred until the tenant registry in #83 exists.

## Tests

`make -C deploy test-gateway` first validates the rendered kustomization against the vendored CRDs with `kubeconform -strict` (`go install github.com/yannh/kubeconform/cmd/kubeconform@v0.6.7`), then runs `scripts/test_gateway.py` with `kubectl kustomize`: header removal covers every header in `tenant.go` plus the explicit list, the SecurityPolicy targets the Gateway, rate-limit rules are shared, keyed on tenant, with the 600/min fallback, the TLS listener, and the `/api` rewrite. It needs `pyyaml`.

## Service tokens

Decision (#226): signed service-principal tokens now, SPIFFE/mTLS later. A service that calls another no longer forwards the end user's duties; it sends `X-Passage-Service-Token`, an Ed25519-signed token (`platform/go/tenant/service.go`) with claims `iss` (caller), `aud` (target), `tid`, `le` (one explicit legal entity), `act` (the end user, which the target records as the actor), `duties` (the caller's own grant for that target), `iat`, `exp` (at most 60 seconds) and `jti`. The target's middleware replaces every user header with the token's principal; an invalid, expired, mis-addressed, replayed or over-granted token is a 401, never a fallback to user headers.

- Grants are static: `policies/authz/data.json` `service_grants` (caller, then target, then duties), mirrored in `tenant.serviceGrants` and kept equal by a test. Today: receiving to ledger (`ledger.journal.post`, `ledger.journal.reverse`), receiving to close (`period.check`), receiving to purchasing (`procurement.invoicing-plan.settle`, #289), payables to ledger (`ledger.balance.read`, `ledger.journal.read`), party to numbering (`numbering.write`).
- The gateway strips the header from clients (`ClientTrafficPolicy`), so only in-cluster callers holding a signing key can present one.
- The legal entity is explicit and bound to the end user: `Mint` refuses an entity outside the user's `LegalEntityScopeList` (or any entity for a no-entity ALL-scope read), so a service cannot act in an entity the user could not.
- Grants are as narrow as the targets' duties allow. `numbering.write` guards both sequence creation and allocation, `ledger.journal.post` guards entries and batches, and `ledger.journal.read` guards every ledger lookup; payables needs the lookup and `ledger.balance.read`. Finer duties need target-side route changes.
- Callers set `PASSAGE_SERVICE_NAME` and `PASSAGE_SERVICE_SIGNING_KEY_FILE` (a mounted secret holding the base64 32-byte Ed25519 seed; `PASSAGE_SERVICE_SIGNING_KEY` is the inline form) and optionally `PASSAGE_SERVICE_KEY_ID`. Targets set `PASSAGE_SERVICE_NAME` and `PASSAGE_SERVICE_PUBLIC_KEYS` (`caller[:kid]=base64pub,...`, not secret). Several keys per caller (distinct `kid`) allow rotation: add the new public key everywhere, switch the caller's seed and `kid`, then drop the old key. A repeated caller and kid, or a malformed value, stops the target at startup. Charts: targets (ledger, close, numbering) take `serviceIdentity.publicKeys`; callers (receiving, payables, party) take `serviceIdentity.signingKeySecret` (a Secret with key `signing-key`, mounted read-only at `/var/run/passage/service-identity`, mode 0440 with pod `fsGroup: 65532`) and `serviceIdentity.keyId`. The all-scope exception in Mint applies only to read-only grants.
- Replay memory is per process, an expiry heap capped by `PASSAGE_SERVICE_REPLAY_CACHE_SIZE` (default 100000, sized for about 1700 tokens/s per pod over the 60s lifetime; a full cache rejects). A token captured in transit can be replayed once per other replica within its 60s life; mTLS between pods and a shared store are the follow-ups.
- Every accepted token is logged (`service call`: caller, end-user actor, legal entity, path, correlation id).
- With no signing key a caller keeps forwarding the user's own headers (previous behaviour); a malformed key stops startup.
