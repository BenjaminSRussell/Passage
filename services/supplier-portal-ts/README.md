# supplier-portal BFF (#511)

The back end of `apps/supplier-portal`. TypeScript on Node 24 (types stripped, no build step, no runtime dependencies), like
`carrier-gateway-ts`. Component: `portal-bff` (docs/architecture.compiled/components/portal-bff.md).

## What it does

| Part | Responsibility |
|---|---|
| `src/oidc.ts` | Code flow with PKCE (S256) against the Keycloak client `passage-portal-supplier`. Verifies the ID token (RS256 from the realm JWKS, issuer, audience, expiry, nonce) and requires the `supplier-user` or `supplier-admin` realm role plus `tid`, `vendor_id` and `le` claims. The access token is never used |
| `src/session.ts` | In-memory sessions behind an HttpOnly `__Host-` cookie (30 min idle, 8 h absolute), single-use login state, CSRF token, per-session rate limit |
| `src/serviceToken.ts` | Signs service tokens (Ed25519, `platform/go/tenant` format) with `iss: supplier-portal`, the session's tenant and one legal entity, `act` = user, and `vnd` = the vendor. Grants: the vendor-portal duties on purchasing, receiving and party only |
| `src/routes.ts` | The table of `/api/v1/*` routes and the one upstream call each becomes. Nothing else is proxied |
| `src/server.ts` | `/bff/login`, `/bff/callback`, `/bff/me`, `/bff/logout`, `/healthz`, `/readyz`, the `/api/v1` proxy, and the public routes below |
| `src/scan.ts` | The upload scanner interface, a ClamAV adapter (clamd INSTREAM over TCP) and a fake; `screenUpload` applies the policy |
| `src/provision.ts` | The `UserProvisioner` interface for the identity-provider user of an accepted invitation, the default `PendingProvisioner`, and a fake |

## Public routes and remainders (#511)

`GET /bff/registration`, `POST /bff/prospects`, `POST /bff/invites/verify` and `POST /bff/invites/accept` need no session. Each refuses another origin, reads at most 16 KiB, and takes a slot from a per-client-address limit (`PUBLIC_RATE_PER_HOUR`, default 10) taken from the rightmost `X-Forwarded-For` entry, so the edge must be the only proxy in front. A registration is signed with a vendorless token holding only `party.vendor-portal.register`; an invitation call is signed for the vendor named in the token with only `party.vendor-portal.invite-accept`, and party checks the secret: nothing in the token is trusted. `/api/v1/invites` is for vendor administrators (the `supplier-admin` role); the invitation token and its accept link are in the one answer that creates the invitation. Uploads (`/api/v1/documents`) go through the scanner first.

## Vendor scoping

The vendor reaches an owning service only as the signed `vnd` claim. Services read it from the verified principal
(`tenant.Principal.VendorID`), refuse the vendor routes to any other caller, and narrow every query to it; a row of another vendor
reads as not found. The BFF never forwards a client-supplied `vendorId`, `legalEntityId` or `tenantId` (query or body), takes the
legal entity from the session (`X-Legal-Entity` must be one of the user's), checks CSRF and origin on every write, and turns an
upstream 401 into 502 so a misconfigured key never reads as "sign in again".

## Run

Environment: `OIDC_ISSUER`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URI`, `APP_ORIGIN`, `PURCHASING_URL`, `RECEIVING_URL`, `PARTY_URL`,
`PAYABLES_URL`, `PASSAGE_SERVICE_SIGNING_KEY` (or `_FILE`); optional `OIDC_CLIENT_ID`, endpoint overrides, `HTTP_ADDR`,
`COOKIE_SECURE=false` (local http). The services trust this key through `PASSAGE_SERVICE_PUBLIC_KEYS` (`supplier-portal=<base64 public key>`).

| Variable | Default | Meaning |
|---|---|---|
| `CLAMAV_ADDR` | unset | `host:port` of clamd. Set: every upload is scanned and a failed scan refuses it |
| `CLAMAV_TIMEOUT_MS` | 30000 | bound on one scan (at least 1000) |
| `SCAN_ALLOW_UNSCANNED` | `false` | `true` accepts uploads when no scanner is configured (dev); ignored when a scanner is configured |
| `REGISTRATION_TENANT_ID`, `REGISTRATION_LEGAL_ENTITY_ID` | unset | both set opens the public registration page into that tenant and legal entity |
| `PUBLIC_RATE_PER_HOUR` | 10 | public requests per client address per hour |

The chart (`charts/supplier-portal`) exposes these as `scan.*`, `registration.*`, `publicRatePerHour` and `upstreams.payables`.

```
npm ci && npm test && npm run typecheck
```

Tests use a fake identity provider and fake upstreams; the grants test keeps `serviceToken.ts` equal to `policies/authz/data.json`
and `platform/go/tenant/service.go`.

See [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).
