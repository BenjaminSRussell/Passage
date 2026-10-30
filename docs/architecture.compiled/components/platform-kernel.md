# Platform Kernel

## Purpose & Boundaries
Go library `platform/go` compiled into every Go service (httpx, dbx, tenant, authz, events, outbox, money, ids, testkit) plus an equivalent TS package for `*-ts` services. Implements tenancy (`SET LOCAL`), JWT verification, OPA calls, idempotency, problem details, outbox/inbox, OTel, health. Services contain domain logic only. Waves may NOT edit `platform/` (ownership rule); needs are requests to the kernel owner. New kernel work not in requirements: `sequence` package, `ledgerclient`, `passage.ctx` logical message, `_internal` realm client, egress topics.

## Interfaces
- Kernel-standard endpoints on every deployable: `GET /healthz` (liveness, no dependency checks), `GET /readyz` (own Postgres SELECT 1 <500 ms as `passage_app`, migrations current, OPA sidecar has current bundle, TS workers also Temporal, startup-only event-type registry check; NOT Valkey, NOT Kafka), `GET /metrics` (admin port 9090), `GET /v1/{context}/openapi.yaml`, `/asyncapi.yaml`, `/error-codes` (User:`apidocs.read`).
- `httpx` idempotency middleware: table `idempotency_keys` from `dbx.IdempotencyDDL` (cols tenant_id, principal, method, path, key, request_hash, state in_progress|completed, locked_until, response_status, response_headers, response_body, created_at, expires_at 24 h; unique (tenant_id, principal, method, path, key)). In-progress lease 60 s; only 2xx stored (4xx incl. ledger 422 NOT cached); concurrent -> 409 `idempotency_in_progress`; different body hash -> 422 `idempotency_key_reuse`; replay returns stored response + `Idempotent-Replayed: true`. Required on creating/posting POST and every custom method; optional on PUT/PATCH. Engines also persist `idempotency_key` on the document (unique per tenant).
- `If-Match` required on PUT/PATCH/DELETE/custom methods (428 if absent, 412 `conflict_version` on mismatch); `ETag` on every read. Problem details `application/problem+json`: type (`https://passage.dev/problems/<code>`), title, status, detail, instance, code (snake_case D.4), serviceCode (e.g. `LED-0001`), traceId, errors[].
- `authz`: verify JWT (RS256 only, reject none/HS*; JWKS cached by kid with rate-limited refetch; iss in issuer registry; aud contains `passage-api`; exp/nbf 30 s skew; typ=Bearer; azp allowed); then POST OPA sidecar `/v1/data/passage/authz/allow` / document `data.passage.authz.decision` -> `{allow, legalEntityScopes[], obligations{maskFields[], requiresAcr, approvalWorkflow}}`. 401 `unauthenticated`, 403 `forbidden_duty`. No hand-rolled JWT code (`jose` in TS).
- `tenant` middleware builds `Tenant{id, legalEntityIDs, actor}`; optional `Passage-Legal-Entity: <uuid|*>` header narrows, never widens.
- Kernel HTTP client: connect 500 ms, request 2 s (3 s for `ledger.Post`), retries only idempotent / key-carrying posts (max 3, jittered), circuit breaker + bulkhead per upstream, propagates `traceparent`, `X-Correlation-Id`.
- `ledgerclient` (new): caller protocol of ledger.Post (see ledger sheet).

## Data Ownership
Per-database infrastructure tables in every context DB (EN-012): `<ctx>.outbox` (C.4 DDL; excluded from RLS; REPLICA IDENTITY FULL; 256 KiB cap), `consumer_inbox` (PK (consumer, event_id); pruned 30 d), `idempotency_key`, `number_sequence_counter` (PK (tenant_id, sequence_id, legal_entity_id, fiscal_period), fillfactor 50), `audit_event` (tenant_id, id, occurred_at, actor, action in p3_change|p3_reveal|export|break_glass, entity, entity_id, fields text[], pii_payload bytea, request_id, trace_id). `dbx` generates the fiscal-immutability trigger and RLS DDL.

## Events Produced
`outbox.Enqueue(ctx, tx, evt)` validates envelope + data against registry schema INSIDE the business transaction; failure rolls back. `sequence` = aggregate `version`. `dbx` emits transactional logical-decoding message `passage.ctx` `{actor, traceId, correlationId}`.

## Events Consumed
Consume side (C.5): in one tx SET LOCAL tenant/legal entities from event `tenantid`/`legalentityid`; INSERT consumer_inbox ON CONFLICT DO NOTHING; apply effect + outbox rows; commit; then ack offset. Event without `tenantid` -> DLQ.

## Dependencies
internal: none (everything depends on it). external: Postgres (CNPG), OPA sidecar, Keycloak JWKS, Vault (file-projected dynamic creds), OTel Collector.

## Gotchas & Constraints
- `dbx` sets GUCs `passage.tenant_id` and `passage.legal_entity_scopes` per transaction. `platform/go/ids` switches ULID -> UUIDv7 in wave 3.
- Pool role `passage_app` has no BYPASSRLS; missing tenant GUC yields zero rows. SET LOCAL only (PgBouncer transaction mode); no session state or session advisory locks in request paths.
- Config: 12-factor env vars validated at startup (exit non-zero): PASSAGE_ENV, SERVICE_NAME, SERVICE_VERSION, HTTP_ADDR, ADMIN_ADDR, DATABASE_URL|DATABASE_URL_FILE, DATABASE_MIGRATOR_URL, MIGRATE_ON_START (dev only), KAFKA_*, SCHEMA_REGISTRY_URL, OPA_URL (127.0.0.1:8181), OIDC_ISSUER_REGISTRY_URL, OIDC_AUDIENCE=passage-api, TEMPORAL_*, OTEL_*, LEDGER_URL, VALKEY_URL (optional). Rotating creds/certs projected as files and re-read (file watch).
- Logging: JSON lines; required timestamp, level, message, service, correlationId; never log P2/P3 values or bodies; no console.log/fmt.Println (lint bans). Go `log/slog`, TS `pino`, Python structlog.
- Go stack: chi, pgx/v5, shopspring/decimal, oklog/ulid, otelhttp, rapid; floor Go 1.25. TS: Node 24 LTS, Fastify 5 + zod, pino, Kysely, @confluentinc/kafka-javascript (not kafkajs). Python 3.12+, FastAPI, Pydantic v2.
- Money `{amount: decimal string, currency}`; never float. Optimistic concurrency `UPDATE ... WHERE id=$1 AND version=$2`.
- Atlas versioned migrations, expand/contract only; advisory lock serializes starters; stage/prod migrations run as Argo PreSync Job with owner role.
- Rate-limit headers `RateLimit-Limit/Remaining/Reset`, `Retry-After`, `X-Passage-Api-Build` on responses.

## Source Sections
- Backend 1-2 (lines 103-141), 5.1-5.4 (369-420), 7.1 authn validation (1133), 7.3 (1147-1154), 7.5 (1181-1187), 7.6 (1189-1197), 7.8 (1205-1224), 7.11 (1243-1251), 7.12 kernel client (1255), 7.13 (1288-1292), 7.18 (1328-1337)
- Data 4.1-4.6 (1504-1584), EN-012 (2111-2119); Events 2.4 (3942-3949), 7.2 (4334-4348); Cross-Cutting Security (7746-7748)
