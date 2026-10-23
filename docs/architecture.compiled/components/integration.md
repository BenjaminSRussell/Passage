# Integration (supporting context)

## Purpose & Boundaries
Context `integration` (TS only: `integration-ts`; database `passage_integration`, operational tables only; waves 6, 9, 10, 35, 49). Owns webhook subscriptions/deliveries, EDI/Peppol partner profiles and message log, API catalog, tenant egress relay, e-invoice, migration pipeline (see migration-framework). Never a source of business truth.

## Interfaces
Prefix `/v1/integration/`: `catalog`; `webhook-subscriptions` (POST returns HMAC secret once; `:test`); `webhook-deliveries` (+`:redrive`); `inbound/{endpointId}` (Sig:HMAC); `message-monitor`; `event-egress/{context}:subscribe` (provision tenant egress topic + Kafka principal); `event-replays` (fiscal consumers need controller approval); `partner-profiles`; `edi-mappings/{id}:test`; `edi-messages` (850/855/856/810/820/214) + `/{id}` + `:resend`; `POST /as2/{partnerId}` (Sig:AS2 certificate, MDN returned); `e-invoices` (Peppol UBL, XRechnung, Factur-X, CFDI, IRN); `migration-runs`, `parallel-run-diffs`.
- Webhooks: dispatcher consumes subscribed topics; inserts `webhook_delivery` (unique (subscription_id, event_id)) in one tx with inbox row; one Temporal workflow per delivery (id `webhook:<subscriptionId>:<eventId>`); POST CloudEvent with `X-Passage-Signature` (HMAC-SHA256 over timestamp+body, per-endpoint secret, key id), `ce-id`, `ce-type`, `ce-time`; 2xx success; retries exponential for 24 h honoring Retry-After; 4xx other than 408/429 terminal; then DLQ `passage.integration.webhook.dlq.dispatcher` [P]; p95 first delivery < 10 s; rate per tier 60/300/1,200 per min. Partners dedupe on `id`, order on `sequence`.

## Data Ownership
Operational tables: webhook_subscription (secret in Vault ref, optional mTLS cert + IP allow-list), webhook_delivery, partner profiles, EDI message log, tenant egress relay config; `migration` schema (OQ-08: here by default, vs platform). Egress topics: backend names `passage.egress.<tenantSlug>.<context>` (3 partitions, 30 d); eventing section names `passage.ext.<tenantslug>.<context>.<entity>` — NAME CONFLICT, undecided (needs ADR amendment).

## Events Produced
absent — the source document does not address this (produces no principal domain events; publishes to egress topics and webhook DLQ).

## Events Consumed
`integration-webhook-dispatcher` (any topic with active subscription), `integration-edi-outbound` (purchaseorder.confirmed 850, salesorder.confirmed 855, shipment.posted 856, invoice.posted 810/Peppol, load.shipped 214), tenant-filtering egress relay (one consumer per source topic, routing by tenantid and subscription list).

## Dependencies
internal: platform, eventing-backbone, sales, procurement, warehouse, migration-framework. external: EDI/AS2/SFTP partners (997/CONTRL tracking, SLA alerts), Peppol access point / PAC / IRP / PDP (adapter per provider; none assumed), Temporal, Vault.

## Gotchas & Constraints
- No P3 in webhook payloads; `legalentityid` filter enforced server-side; subscription cannot select types the creator lacks read privilege for.
- Temporal-per-delivery at 1,200/min Enterprise ≈ 20 workflows/s per tenant: load-test at wave 35, fallback DB scheduler (OQ-9).
- Partner webhook egress through egress gateway: block RFC 1918/link-local/loopback/metadata, resolve-then-connect pinning, HTTPS only, per-tenant destination allow-list.
- Replay procedure per C.6: ticket, pause, snapshot inbox, reset offsets, resume, verify.
- Provider selections (OCR, e-invoice AP, registry) open; adapter interfaces only.

## Source Sections
- Backend 5.17 (923-951), 7.7 (1199-1203); Events 11 (4658-4674), 7.6 replay (4393-4403), consumer rows (4318-4319); Cross-Cutting network egress (7767)
