# customer-portal BFF (#30)

The back end of `apps/customer-portal`. TypeScript on Node 24 (types stripped, no build step, no runtime dependencies), like
`supplier-portal-ts`, which it follows. Component: `portal-bff`.

## What it does

| Part | Responsibility |
|---|---|
| `src/oidc.ts` | Code flow with PKCE (S256) against the Keycloak client `passage-portal-customer`. Verifies the ID token (RS256 from the realm JWKS, issuer, audience, expiry, nonce) and requires the `customer-user` realm role plus `tid`, `customer_id` and `le` claims. The access token is never used |
| `src/session.ts` | In-memory sessions behind an HttpOnly `__Host-` cookie (30 min idle, 8 h absolute), single-use login state, CSRF token for sign-out, per-session rate limit |
| `src/serviceToken.ts` | Signs service tokens (Ed25519, `platform/go/tenant` format) with `iss: customer-portal`, the session's tenant and one legal entity, `act` = contact, and `cus` = the customer. Grants: the read-only customer-portal duties on receivables, billing and sales only |
| `src/routes.ts` | The table of `/api/v1/*` routes and the one upstream call each becomes. Every route is a GET; nothing else is proxied |
| `src/server.ts` | `/bff/login`, `/bff/callback`, `/bff/me`, `/bff/logout`, `/healthz`, `/readyz`, and the `/api/v1` proxy |

## Customer scoping

The customer reaches an owning service only as the signed `cus` claim. Services read it from the verified principal
(`tenant.PortalCustomer`, which is true only for a token signed by `customer-portal`), refuse the `/customer-portal/` routes to any
other caller, and narrow every query to it; a row of another customer reads as not found, the same answer as an unknown id. The BFF
never forwards a client-supplied `customer`, `customerId`, `legalEntityId` or `tenantId` (the query allowlist is `limit`, `cursor`,
`status`, `asOf`), takes the legal entity from the session (`X-Legal-Entity` must be one of the contact's), has no write route, and
turns an upstream 401 into 502 so a misconfigured key never reads as "sign in again".

## Run

Environment: `OIDC_ISSUER`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URI`, `APP_ORIGIN`, `RECEIVABLES_URL`, `BILLING_URL`, `SALES_URL`,
`PASSAGE_SERVICE_SIGNING_KEY` (or `_FILE`); optional `OIDC_CLIENT_ID`, endpoint overrides, `HTTP_ADDR`, `COOKIE_SECURE=false`
(local http). The services trust this key through `PASSAGE_SERVICE_PUBLIC_KEYS` (`customer-portal=<base64 public key>`).

```
npm ci && npm test && npm run typecheck
```

Tests use a fake identity provider and fake upstreams; the grants test keeps `serviceToken.ts` equal to `policies/authz/data.json`
and `platform/go/tenant/service.go`.

See [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).
