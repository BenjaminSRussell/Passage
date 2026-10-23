# Edge and Identity (Envoy Gateway, Keycloak, OPA)

## Purpose & Boundaries
Envoy Gateway does TLS 1.3, JWT validation per tenant issuer, rate limiting and coarse route-duty ext_authz ONLY; business authorization, routing logic and validation stay in services. Keycloak 26+ (Operator, 3 replicas): realm per tenant + one `_internal` realm for service/saga identities. OPA: sidecar per pod plus standalone OPA-Envoy plugin for gateway ext_authz; bundles compiled by `platform-ts`. Optional in-cluster Valkey (proposed, needs ADR-0001 addendum) for global rate-limit counters only.

## Interfaces
- Envoy: Gateway API `HTTPRoute`, `SecurityPolicy`, `BackendTrafficPolicy`; routes by `/v1/<context>` prefix (longer prefix e.g. `/v1/procurement/portal` routes to TS deployable); cert-manager. Maps `tenant_id`, `azp`, `tier` to headers used only for rate limiting/logs (services re-derive tenant from token, except `_internal` path). Generates `X-Correlation-Id` if absent.
- Rate limits: descriptors `(tenant_id, tier)` and `(tenant_id, azp)`; Starter 600/min, Professional 3,000, Enterprise 12,000 sustained, burst 100/500/2,000; `device` class 120/min per device; per-agent caps under tenant budget; `429 rate_limited` + `Retry-After`; body limits 1 MiB single / 10 MiB bulk (`413 payload_too_large`); CORS closed by default with per-tenant origin allow-lists at gateway; webhook delivery tiers 60/300/1,200 per min.
- Keycloak realm template: clients, scopes, mappers (audience mapper adds `aud: passage-api`, mandatory, verified by realm template test), password+MFA policy; `tenant_id` hard-coded claim per realm. Claims: iss, sub, aud, azp, exp, iat, jti, typ, acr, tenant_id, tier, and for agents agent_id, on_behalf_of. Duties/roles/LE grants deliberately NOT in token. Token lifetimes: access 15 min, refresh 12 h single-use rotation (Revoke Refresh Token on, reuse 0), SSO session max 12 h; tenants may shorten not lengthen.
- Flows: browser users via BFF (code+PKCE S256); devices OAuth device grant RFC 8628 (device class 120 req/min); partners client credentials with private_key_jwt or mTLS (azp = "API key"; no separate API-key system); internal services client credentials in `_internal` + mTLS + `X-Passage-Tenant`; agents OAuth 2.1 code+PKCE delegated token; federation OIDC/SAML brokering; SCIM via facade in platform-ts (Keycloak core has no SCIM).
- Step-up: OPA obligation `requires_acr: "mfa"` for payment.approve, payment.release, periodcontrol.admin, vendorbank.write, customerbank.write, any `*.reveal`, role assignment; service returns 401 + `WWW-Authenticate: Bearer error="insufficient_user_authentication", acr_values="mfa"`.
- OPA bundle: `GET /v1/platform/policy-bundles/current` (Svc:`policy.bundle.read`, mTLS), ETag/delta, <=10 s propagation; fail-secure 503 if sidecar unreachable or bundle older than 5 min.
- `/.well-known/oauth-protected-resource` (RFC 9728) for MCP clients.

## Data Ownership
Keycloak and Temporal have their own CNPG clusters (outside the 12 contexts, OQ-12). Valkey (if adopted) holds rate-limit counters only, no P3. Bundle data lives in platform context.

## Events Produced
absent — the source document does not address this (kill-switch changes reach OPA data within 10 s via bundle).

## Events Consumed
absent — the source document does not address this

## Dependencies
internal: platform (bundle compiler, tenants API), platform-kernel. external: Keycloak, OPA, Envoy Gateway, cert-manager, Valkey (optional), Vault (client secrets).

## Gotchas & Constraints
- Envoy validation is not trusted alone: every service re-verifies JWT (east-west calls bypass gateway).
- Valkey NEVER Redis; rate limiting fails open to Envoy's local per-pod limiter, not unlimited; Valkey is a soft dependency, never in readiness.
- Keycloak SCIM extension unconfirmed (Q3); token exchange / `act` claim support unconfirmed (Q8): fallback custom claim mapper + token-exchange policy.
- OPA bundle size risk at 100 tenants x 10k users: delta bundles, per-tenant-cohort sharding, load test in wave 2 (Q22).
- Per-tenant Kafka ACLs on shared topics impossible; see eventing-backbone egress topics.
- Network: namespaces per tier (edge, bff, services, data, platform-infra, ai, integration-egress) with default-deny NetworkPolicy; mTLS east-west; egress via gateway with RFC1918/link-local/metadata blocking for webhooks.
- Moonaan defaults (Cognito, Verified Permissions/Cedar, API-gateway-less) are NOT used.

## Source Sections
- Backend 7.1 (1117-1133), 7.2 (1135-1145), 7.4 (1156-1179), 7.17 (1324-1326); Frontend 8.1-8.2 (5564-5626)
- Cross-Cutting Security & Auth (7744-7771); Open Questions 1,5,28 (7779-7807)
