# features

The catalog of per-tenant switchable features (`Catalog`, `Templates`), the dependency rules for changing them (`Apply`), and the middleware a service mounts so a disabled feature answers with a plain-words problem.

Identity owns each tenant's state (`GET`/`PUT /v1/tenant/features`); a tenant that never changed a feature has it on.

## Mounting the gate

```go
source := features.NewCache(features.NewHTTPSource(identityURL, nil, signer), features.DefaultTTL)
router.With(features.Require(source, "process-manufacturing"))
```

`services/processmfg` is the reference: it mounts the gate when `IDENTITY_URL` is set and is unchanged when it is not.

A disabled feature answers 403 `FEAT-0001` as `application/problem+json`: "Process manufacturing is turned off for your organisation. A tenant administrator can turn it on under Settings, Features." `errors.feature` carries the key. This is not a security boundary; duties are.

## Fail open

When the lookup fails (identity down, slow or refusing), the request goes ahead and a warning is logged. Features are about relevance, a tenant starts with everything on, and a feature check that took a module down whenever identity blipped would turn a convenience into an outage. `Cache` also keeps the last known state past its TTL when a refresh fails, so a tenant that switched a module off stays switched off through a blip.

Lookups are cached per tenant for `DefaultTTL` (30 s), so a toggle takes effect everywhere within that time without a deploy, and a request adds no database or network hit inside it. Concurrent misses for a tenant share one lookup. A failed lookup is remembered for 5 s (`DefaultBackoff`), keeping the last known state or, with none, letting requests through, so an identity outage costs one call per tenant per backoff rather than one per request. The map holds 10,000 tenants and evicts the oldest entry when full.

## Authentication

`HTTPSource` signs a short-lived service token (`ServiceSigner.ApplyDuties` to `identity`, carrying only `identity.feature.view`) and sends it in `X-Passage-Service-Token`. It never sends gateway headers, so opening identity's NetworkPolicy to a consuming service does not let that pod forge a tenant or duties. Without a signing key `HTTPSource` refuses to call (`ErrUnsigned`) and the gate fails open; `services/processmfg` refuses to start with `IDENTITY_URL` set and no key.

The token is tenant-level (no legal entity) and any signed-in user's request may mint it, because it carries a read-only duty (`tenant.tenantReadDuties`). Identity verifies it with `tenant.Middleware` like any service token: `PASSAGE_SERVICE_NAME=identity`, `PASSAGE_SERVICE_PUBLIC_KEYS` with the consumer's public key, and the shared replay table (migration 0013). The grant is `processmfg -> identity: identity.feature.view` in both `platform/go/tenant/service.go` and `policies/authz/data.json`; it cannot change features or reach any other identity route.

## Deploy handoff

Set `IDENTITY_URL`, the consumer's signing key, and identity's public-key list. Roll identity out with `PASSAGE_SERVICE_PUBLIC_KEYS` (and `PASSAGE_SERVICE_NAME=identity`) set before, or together with, the first consumer that sets `IDENTITY_URL`.

Watch for this failure: **identity started without service public keys refuses every service token** with 401 `AUTH-0001` ("invalid service token"), including the feature read. The consumer sees an error from the lookup and the gate then fails open, so every module stays on and a feature a tenant turned off keeps answering. Nothing is logged louder than a `feature lookup failed; allowing the request` warning in the consumer. After a deploy, check that a consumer's warning count is zero (or request `GET /v1/tenant/features` through a consumer-signed token and expect 200, which `TestTenantFeaturesAcceptsTheConsumersServiceToken` and `TestTenantFeaturesRefusesEveryServiceTokenWithoutPublicKeys` in `services/identity` cover). Identity's NetworkPolicy admits only the gateway and the chart has no rule for consumers yet: add one that admits just the consuming pods. Do not use `networkPolicy.allowSameNamespace`; identity's admin routes trust `X-Passage-*` headers, so admitting every pod would let any of them forge a tenant administrator. The signed token protects the feature read itself, not those other routes.
