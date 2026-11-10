# features

The catalog of per-tenant switchable features (`Catalog`, `Templates`), the dependency rules for changing them (`Apply`), and the middleware a service mounts so a disabled feature answers with a plain-words problem.

Identity owns each tenant's state (`GET`/`PUT /v1/tenant/features`); a tenant that never changed a feature has it on.

## Mounting the gate

```go
source := features.NewCache(features.NewHTTPSource(identityURL, nil), features.DefaultTTL)
router.With(features.Require(source, "process-manufacturing"))
```

`services/processmfg` is the reference: it mounts the gate when `IDENTITY_URL` is set and is unchanged when it is not.

A disabled feature answers 403 `FEAT-0001` as `application/problem+json`: "Process manufacturing is turned off for your organisation. A tenant administrator can turn it on under Settings, Features." `errors.feature` carries the key. This is not a security boundary; duties are.

## Fail open

When the lookup fails (identity down, slow or refusing), the request goes ahead and a warning is logged. Features are about relevance, a tenant starts with everything on, and a feature check that took a module down whenever identity blipped would turn a convenience into an outage. `Cache` also keeps the last known state past its TTL when a refresh fails, so a tenant that switched a module off stays switched off through a blip.

Lookups are cached per tenant for `DefaultTTL` (30 s), so a toggle takes effect everywhere within that time without a deploy, and a request adds no database or network hit inside it. The map is bounded at 10,000 tenants and is dropped whole when full.

## Deploy handoff

`HTTPSource` calls identity directly with the tenant and user headers. Identity's NetworkPolicy admits only the gateway, so a consuming service's pods must be admitted (`networkPolicy.allowSameNamespace` or an equivalent rule) before `IDENTITY_URL` is set in a cluster.
