# customer-portal BFF (#30)

The back end of `apps/customer-portal`. TypeScript on Node 24 (types stripped, no build step, no runtime dependencies), like
`supplier-portal-ts`, which it follows. Component: `portal-bff`.

## What it does

| Part | Responsibility |
|---|---|
| `src/oidc.ts` | Code flow with PKCE (S256) against the Keycloak client `passage-portal-customer`. Verifies the ID token (RS256 from the realm JWKS, issuer, audience, expiry, nonce) and requires the `customer-user` realm role plus `tid`, `customer_id` and `le` claims. The access token is never used |
| `src/session.ts`, `src/redisStore.ts`, `src/resp.ts` | Sessions behind an HttpOnly `__Host-` cookie (30 min idle, 8 h absolute), single-use login state, CSRF token, per-session rate limit. The store is an interface: in memory for development, or a Valkey or Redis server shared by the pods (`SESSION_STORE_URL`; a small RESP client, no dependency). An unreachable store is 503, never a sign-out |
| `src/serviceToken.ts` | Signs service tokens (Ed25519, `platform/go/tenant` format) with `iss: customer-portal`, the session's tenant and one legal entity, `act` = contact, and `cus` = the customer. Grants: the read-only customer-portal duties on receivables, billing and sales only |
| `src/routes.ts` | The table of `/api/v1/*` routes and the one upstream call each becomes, with the one duty each carries. Reads are GET; the only writes are `POST /orders` and `POST /returns` |
| `src/server.ts` | `/bff/login`, `/bff/callback`, `/bff/me`, `/bff/logout` (answers the Keycloak end-session URL), `/healthz`, `/readyz`, `/api/v1/companies` (names from identity) and the `/api/v1` proxy |

## Customer scoping

The customer reaches an owning service only as the signed `cus` claim. Services read it from the verified principal
(`tenant.PortalCustomer`, which is true only for a token signed by `customer-portal`), refuse the `/customer-portal/` routes to any
other caller, and narrow every query to it; a row of another customer reads as not found, the same answer as an unknown id. The BFF
never forwards a client-supplied `customer`, `customerId`, `legalEntityId` or `tenantId` (the query allowlist is `limit`, `cursor`,
`status`, `asOf`), refuses a write body that names one, takes the legal entity from the session (`X-Legal-Entity` must be one of the
contact's), and turns an upstream 401 into 502 so a misconfigured key never reads as "sign in again". A contact may act for a bill-to or
ship-to account by naming it in `X-Customer`: the BFF asks party for the accounts of the contact's own customer and signs the named one
only when it is listed. Each call carries only the duty of its route (`sales.customer-portal.order` for an order, and so on). The two
writes need a CSRF token, a same-origin `Origin`, a JSON body up to 64 KiB and an `Idempotency-Key` of 8 to 80 characters.

## Run

Environment: `OIDC_ISSUER`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URI`, `APP_ORIGIN`, `RECEIVABLES_URL`, `BILLING_URL`, `SALES_URL`, `PARTY_URL`, `IDENTITY_URL`,
`PASSAGE_SERVICE_SIGNING_KEY` (or `_FILE`); optional `OIDC_CLIENT_ID`, endpoint overrides (`OIDC_END_SESSION_ENDPOINT`,
`OIDC_POST_LOGOUT_REDIRECT_URI`), `SESSION_STORE_URL` with `SESSION_STORE_PASSWORD` (or `_FILE`), `HTTP_ADDR`, `COOKIE_SECURE=false`
(local http). The services (receivables, billing, sales, party and identity) trust this key through `PASSAGE_SERVICE_PUBLIC_KEYS`
(`customer-portal=<base64 public key>`).

```
npm ci && npm test && npm run typecheck
```

Tests use a fake identity provider, fake upstreams and an in-process Valkey stand-in (`test/fakeRedis.ts`); the grants test keeps `serviceToken.ts` equal to `policies/authz/data.json`
and `platform/go/tenant/service.go`.

See [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).
