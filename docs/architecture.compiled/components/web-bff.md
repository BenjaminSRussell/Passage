# web-bff

## Purpose & Boundaries
TS edge service `services/web-bff-ts` (renamed from `bff-ts`; wave 5; ownership Q21). Cookie-session OIDC token handler for `apps/web`; hosts the read-only GraphQL gateway; SSE live updates. Sessions stored server-side in `passage_platform` (BFF-owned Postgres table; refresh tokens encrypted through Vault transit; Q8). It is the only component that attaches `Authorization: Bearer`; never forwards `/bff/...` to services.

## Interfaces
- Auth: `GET /bff/login` (code+PKCE S256, tenant from host), `GET /bff/callback`, `POST /bff/logout` (RP-initiated logout, revoke refresh token), `/bff/backchannel-logout`, `GET /bff/v1/me` (profile, tenant, LEs, privilege summary), `GET /bff/v1/permissions?le=` (returns `{le, computedAt, ttlSeconds, grantsVersion, privileges[], duties[]}`; one OPA query per user x LE via `data.passage.authz.ui_privileges`), `GET /bff/v1/stream` (SSE: job.progress, task.created/completed, record.changed, entitydefinition.changed, grants.changed), `GET /bff/v1/search?q=&le=&types=&limit=8` (fan-out to per-context `?q=` pg_trgm, 150 ms timeout per context).
- Passthrough: `/api/v1/<context>/...` rewritten to `/v1/<context>/...` with bearer token attached. GraphQL `POST /v1/platform/graphql` (depth <= 8, cost-limited, DataLoader batching; resolvers call REST with caller's token; P2/P3 field masking).
- Cookies: `__Host-passage_sid` (opaque >= 256 bits, HttpOnly, Secure, SameSite=Strict, Path=/), login transaction cookie `__Host-passage_login` (Lax, 5 min). Non-GET requests need `X-Passage-CSRF: 1` and same-origin `Origin` else 403.

## Data Ownership
Session store table in `passage_platform` (placement is Q8); no other state.

## Events Produced
absent — the source document does not address this

## Events Consumed
absent — the source document does not address this (SSE bridge from Kafka/Temporal is a backend question Q7).

## Dependencies
internal: platform, edge-identity, all context APIs. external: Keycloak (client `passage-web`, confidential secret in Vault), OPA, Envoy.

## Gotchas & Constraints
- No tokens in browser, ever; `returnTo` must be same-origin relative path (open-redirect check). Callback answers with small same-origin page navigating to `returnTo` so Strict cookie is sent.
- OPA unavailable -> permissions endpoint returns 503 (fail-secure); UI shows only no-privilege shell routes. Refresh token rotation reuse -> session deleted, 401 `session_expired`.
- Refresh access token when < 60 s from exp. Idle timeout 30 min (assumption A3), absolute 12 h; dialog 2 min before.
- Step-up: BFF triggers Keycloak ACR re-auth with max_age when OPA returns `requires_acr: mfa`.
- Tenant derived from app host `<tenant-slug>.app.<env>.passage.dev` (scheme undecided Q9).
- Duty-gating in UI is presentational only.

## Source Sections
- Frontend 2.4 (4849-4859), 8.2-8.4 (5585-5676), 6 (5348-5358); Backend 3.2 route table (308-315), 5.5 BFF rows (426-429), 7.1 (1123)
