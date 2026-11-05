# NOT-IMPLEMENTED: carrier-gateway (wave 28 AC4, #482)

Slice 1 builds the following:

- The carrier adapter interface and the deterministic sandbox carrier.
- Rate shopping across adapters.
- Idempotent label purchase, kept in memory.
- Tracking.
- The service token verifier.
- The `/v1/carrier` API, the chart, the adapter contract suite and the recorded sandbox fixture.
- The fulfillment client and routes: `:carrier-rates`, `:carrier-label` and `:carrier-track` (services/fulfillment).
- The authz grants for fulfillment and transport.

Everything else is listed here.

| Deliverable / criterion | Status | Reason |
| --- | --- | --- |
| Reference adapters, EasyPost- and Shippo-style (#28 D3) | later slice | They need carrier test accounts and credentials. A real adapter implements `CarrierAdapter`. It reads its credentials from a mounted secret, never from a request. It maps the carrier's statuses to LABELLED / IN_TRANSIT / DELIVERED / EXCEPTION. It adds its API hosts to the chart's egress. `main.ts` refuses any adapter name other than `sandbox`. |
| AC4 contract tests against recorded fixtures for real carriers | partly built | Built: `test/contract.ts` holds the invariants every adapter keeps (rate format, label document, tracking order, unique event keys, status equals the last event). The sandbox replays `testdata/sandbox-recorded.json` against them. Missing: a recorded HTTP transport (record and replay of the carrier's request and response) for real adapters, and their fixtures. |
| Durable label store | later slice | `MemoryLabelStore` only. After a restart, a label request repeated with the same key buys a new label. For the sandbox it gets a new tracking number; tracking itself needs no store. A Postgres `LabelStore` (tenant RLS, unique key on tenant, legal entity, issuer and key) is the next slice. Until then the chart runs one replica with the `Recreate` strategy. |
| Shared service-token replay store | later slice | The replay cache is per process, like the Go verifier without `ShareReplayCache`. It comes with the Postgres store. |
| Label void / refund, manifest and end-of-day close (#28 D3, fulfillment `:manifest`) | later slice | The sandbox has nothing to void. These need real adapters. |
| Carrier webhooks `/webhooks/carriers/{provider}` with HMAC check, and the 30-minute tracking poll (fulfillment D5) | later slice | Built instead: fulfillment's `POST /v1/fulfillment/shipments/{id}:carrier-track` pulls tracking on demand and records new events once per provider event key. Missing: webhook receipt, which needs an edge route and per-provider secrets, and a scheduled poll. |
| Tracking restricted to labels the tenant bought | later slice | Tracking is by number, as on carrier sites. A tenant can track any number the carrier knows. Restricting it needs the durable label store. |
| transport calling the gateway (rate shop for parcel and LTL tenders, tracking ingest into `/v1/transport/tracking-events`) | handoff to transport (wave 34) | The grant `transport -> carrier-gateway` (`carrier.rate.quote`, `carrier.tracking.read`) is seeded. transport has no client yet. |
| Background (`service:`) callers | later slice | Every grant is exercised on behalf of a person. A background poller needs a service principal holding `carrier.tracking.read` in `servicePrincipals`. The gateway would then accept that actor from its issuer. |
| Carrier accounts per tenant or legal entity (negotiated rates, account numbers) | later slice | Every tenant shares the configured adapters. |
| Customs documents, hazmat and dangerous goods services, insurance, signature options | later slice | These are not modelled on `LabelRequest`. |
| Currencies other than USD; transit days in business days | later slice | The sandbox prices in USD and counts calendar days. |
| PDF labels | later slice | Only ZPL is produced. PDF rendering is docgen (wave 5). |
| Metrics, OpenTelemetry tracing | later slice | It logs JSON lines only. |
| CI job | handoff (deployment-cicd) | The package runs `npm run typecheck` and `npm test`. Like integration-hub-ts and workflow-ts, it is not yet in `.github/workflows/ci.yml`. The `agents` job there also runs `npm run build`, which this package does not have. |
