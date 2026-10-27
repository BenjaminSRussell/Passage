# NOT-IMPLEMENTED: integration-hub-ts (PSG-35, wave 35)

Slice 1 implements only the webhook delivery policy. Every other deliverable is listed here.

| Deliverable / criterion | Status | Reason |
| --- | --- | --- |
| FR-35.8 public REST and GraphQL APIs, OpenAPI 3.1 for entities since wave 3 | later slice of PSG-35 | Needs the entity APIs of waves 3 to 34 and a gateway policy set. |
| FR-35.8 API clients (Keycloak client credentials), scopes mapped to duties, sandbox tenant per client | later slice of PSG-35 | Needs Keycloak client provisioning and the `ApiClient` store. |
| FR-35.8 quotas: token bucket 600/min, burst 100, 429 with Retry-After (HUB-0113, BR-35.8) | later slice of PSG-35 | Enforced at Envoy Gateway (deploy/, not owned by this wave). |
| FR-35.9 webhook dispatcher (HTTPS POST, 8 retries, dead-letter, replay), subscription store | later slice of PSG-35 | Only the pure policy (sign, verify, schedule, ordering) is in slice 1. No HTTP client, store or outbox consumer yet. |
| FR-35.9 Kafka topic subscriptions `hub.<tenant>.<client>` with ACLs | later slice of PSG-35 | Needs Strimzi KafkaUser/KafkaTopic CRs (deploy/, handoff) and the consumer. |
| FR-35.9 replay from timestamp as a Temporal workflow (BR-35.9, cap 1,000,000) | later slice of PSG-35 | Needs Temporal wiring and the event archive. |
| FR-35.10 message monitor and replay (hub side) | later slice of PSG-35 | Shared monitor lives in the edi service; hub side depends on subscriptions. |
| FR-35.11 reference connectors Salesforce, Shopify, Stripe | later slice of PSG-35 | Separate Helm charts and connector repos (open question 3, decide by wave 46). |
| FR-35.15 PLM reference connector (Teamcenter, Windchill, file drop) | later slice of PSG-35 | Built on the hub's public APIs; blocked on the hub API slice. |
| FR-35.8 API catalog (Backstage-backed `GET /hub/v1/catalog`) | later slice of PSG-35 | Needs the developer portal decision (see apps/developer-portal). |
| BR-35.7 HTTP 2xx, 410, retry schedule, signature | done in slice 1 | `src/webhook.ts`, tested in `test/webhook.test.ts`. |
| BR-35.7 per-aggregate ordering | done in slice 1 (policy only) | `deliverableHeads`; dispatcher wiring later. |
| Node runtime typecheck (`tsc --noEmit`) | handoff | `typescript` and `@types/node` are not installed; the test runs through Node's native type stripping. |
| HUB-0116 replay progress message | later slice of PSG-35 | Depends on the replay workflow. |
| Helm chart `passage-integration-hub` | later slice of PSG-35 | No deployable exists yet. The chart lands with the dispatcher. Envoy Gateway quota policies go to `deploy/` (handoff to the deployment-cicd owner). |
