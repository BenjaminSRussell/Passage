# integration-hub-ts

The integration hub is the governed public surface for external systems: API clients,
webhook and Kafka subscriptions, quotas, the API catalog and reference connectors
(PSG-35, wave 35, FR-35.8 to FR-35.11, FR-35.15).

## Slice 1 scope

This slice ships the webhook delivery **policy** as pure TypeScript with no npm
dependencies (`src/webhook.ts`):

- `sign` / `verify`: `Passage-Signature: t=<unix>,v1=<hmac>` over `t.body`, HMAC-SHA256,
  verified within 5 minutes of `t` (BR-35.7).
- `retryDelaySeconds`: the 8-step schedule 10 s, 1 m, 5 m, 30 m, 2 h, 6 h, 12 h, 24 h;
  `null` means dead-letter (HUB-0115).
- `decide`: 2xx delivers, 410 Gone pauses the subscription (HUB-0114), anything else retries.
- `deliverableHeads`: per-aggregate ordering; a failing event blocks only later events of
  the same aggregate.

## Slice 2 scope

`src/dispatch.ts`, also pure:

- `nextStep`: the state machine the dispatcher asks after each attempt. 2xx is
  delivered, 410 pauses the subscription, no response or any other status retries at
  `now + delay`, and a failure after the 8th retry dead-letters the event (HUB-0115).
- `deliveryOrder`: replay order by time, with each aggregate's events in sequence order
  even when a producer clock stamped a later sequence earlier (BR-35.7, AC-35.4).
- `planReplay`: one page of a replay from a timestamp, capped (default 1,000,000, allowed
  1 to 10,000,000, BR-35.9), with a `{fromSeconds, skip}` cursor for the next page.
- `replayHeaders`: `ce-passagereplay: true` on replayed deliveries.

## Slice 3 scope

Two more pure modules:

- `src/quota.ts`: the per-client token bucket (BR-35.8, FR-35.8). `take` refills
  continuously at `ratePerMinute`, caps at `burst`, and on refusal returns
  `retryAfterSeconds` for the 429 `Retry-After` (HUB-0113). Defaults 600/min and burst
  100; both must be integers in 10..100,000 and 1..100,000 (§6.35.13). A clock that
  moves backwards refills nothing. `graphqlCost` charges one unit per started 100
  GraphQL complexity points. The gateway policy enforces the same numbers at the edge.
- `src/filter.ts`: CloudEvents type patterns for subscriptions (FR-35.9). `*` matches
  one segment, a trailing `**` one or more; `validatePattern` explains a bad pattern,
  `matchesType` / `matchesAny` decide delivery.

## Slice 5 scope

`src/plm.ts`, the PLM import precedence the reference connector will apply (FR-35.15,
BR-35.16), pure like the rest:

- `decideEcoImport`: for one engineering change order, in this order: DUPLICATE when the
  same part and revision were already imported (a second sync changes nothing,
  AC-35.14); STALE when the revision is older than the latest imported one; TASK when the
  ECO targets a BOM version already released in Passage (never overwritten); otherwise
  CREATE_ECR, with an effectivity before today moved to today and EDI-0139.
- `compareRevisions`: alpha revisions (A < Z < AA) and numeric revisions by value.
- `validateSyncInterval`: `plm_sync_interval_min` must be an integer in 1..1440.

## Wave 35 proof: dispatcher

`src/dispatcher.ts` is the webhook dispatcher over those rules (AC-35.3, AC-35.4):
`Dispatcher.tick` sends each aggregate's lowest pending event when due, signed, as a
CloudEvents JSON body over HTTPS (`httpTransport`, node:https with a timeout; no response is
`null`), applies `nextStep`, and records the outcome; an event leaves the pending set
only after a 2xx was recorded, so delivery is at-least-once. After the 8th retry the
event goes to the dead-letter list (HUB-0115); a 410 pauses the subscription until
`resume`. `replay(archive, from, cap)` enqueues every `planReplay` page with
`ce-passagereplay: true`. Storage is the `DeliveryStore` interface with an in-memory
implementation. `test/dispatcher.test.ts` runs it against a real HTTP receiver on
localhost that keeps an order projection (failures, a dropped connection, a lost 2xx,
a crash before recording, dead-letter after 9 attempts on the BR-35.7 schedule,
replay into an empty consumer and from a timestamp).

The Postgres store, the Kafka consumer and the `next_retry_at` poller are slice 6 (below).
See `NOT-IMPLEMENTED.md`.

### Webhook SSRF guard

`src/ssrf.ts` `validateWebhookUrl` runs on every delivery attempt (and must run when a
subscription is created): https only (`http` only with `INTEGRATION_HUB_ALLOW_HTTP=1`,
default off), no URL credentials, every address the host resolves to must be public
(loopback, 10/8, 172.16/12, 192.168/16, fc00::/7, 169.254/16 incl. cloud metadata,
fe80::/10, CGNAT 100.64/10, unspecified, multicast and reserved ranges, IPv4-mapped
IPv6 and NAT64 are rejected). The connection is pinned to the validated address, so a
DNS change between check and connect has no effect; SNI and Host stay the hostname.
Redirects are never followed: a 3xx is a failed delivery and retries on the schedule.
A rejected URL is "no response". `test/ssrf.test.ts` covers each range with a stub resolver.

## Slice 6: the running hub

`src/main.ts` starts everything: the API on :8080, the admin listener (`/metrics`,
`/healthz`, `/readyz`) on :9090, the retry poller and, when `KAFKA_BROKERS` and
`HUB_TOPICS` are set, the Kafka consumer. The Dispatcher is reused unchanged.

- `src/pgStore.ts`: Postgres store. `migrations/0001_hub.sql` creates `hub_subscription`,
  `hub_delivery` and `hub_attempt`, each with `tenant_id`, ENABLE + FORCE row-level
  security, the tenant policy and the `passage_app` grants. Every query runs in a
  transaction that sets `passage.tenant_id`. `PgDeliveryStore` implements the
  Dispatcher's synchronous `DeliveryStore` over one subscription: it is loaded from
  Postgres, the Dispatcher mutates it, and `flush()` writes the changes in order. It
  loads, per aggregate, the first 100 pending events in sequence order (what one pass
  can deliver), at most 5,000 in all, aggregates with the earliest due head first, so
  a large backlog or replay neither breaks per-aggregate order nor fills memory.
- `src/crypt.ts`: field encryption at rest, a node:crypto port of
  `platform/go/fieldcrypt` envelope v1 (HKDF-SHA256 with the tenant id as salt,
  AES-256-GCM, tenant id and field name as additional data, the master key id in the
  envelope for rotation). Keys come from `FIELDCRYPT_KEYS_FILE` (or `FIELDCRYPT_KEYS`)
  and `FIELDCRYPT_CURRENT_KEY_ID`. The signing secret is stored only as
  `hub_subscription.secret_sealed` (field `hub_subscription.secret`) and opened only
  by the poller to sign. Without keys, creating a subscription answers 501 HUB-0501.
  `testdata/fieldcrypt-vectors.json` holds values sealed by the Go package;
  `test/crypt.test.ts` opens them and reproduces their bytes.
- `src/poller.ts`: every `HUB_POLL_INTERVAL_MS` (default 2000) asks `hub_due_subscriptions`
  (SECURITY DEFINER, ids only, search_path pinned to the schema with `pg_temp` last,
  executable by `passage_app` only, because the poller has no tenant until it finds one) for
  subscriptions with a delivery due, takes a lease on each (`lease_until`, so replicas do
  not double-send; a replica starts no send within 15 s of its lease ending, and gives
  the lease back only if it still holds it) and runs `Dispatcher.tick`. The recorded outcome of each send is
  flushed before the next send; a crash in between leaves the event pending and it is
  sent again (at-least-once). Every delivery carries `Idempotency-Key: <event id>`,
  the same on retries, resends and replays, so a receiver can apply it once. A 410
  pauses the subscription (HUB-0114); `:resume` reactivates it.
- `src/consumer.ts`: kafkajs consumer. Each message must be a CloudEvents 1.0 JSON object
  with `id`, `type` and `tenantid`; its type is matched with `matchesAny` against the
  tenant's subscriptions (paused ones keep collecting) and a delivery is enqueued. The
  offset is committed after the enqueue transaction commits; the unique
  (subscription, event id) makes a redelivered message a no-op. Ordering key: the
  `passagesequence` extension, else the partition offset; the aggregate is `subject`.
  A message that is not a usable CloudEvent is published to `HUB_DLQ_TOPIC` with
  `x-dlq-reason` and the source topic, partition and offset, then committed.
- `src/server.ts`: `/v1/hub/subscriptions` (create, list, get, patch, delete),
  `/v1/hub/subscriptions/{id}:resume`, `:replay`, `/v1/hub/deliveries?status=`,
  `/v1/hub/dead-letters`. Identity comes from `x-passage-tenant`, `x-passage-user` and
  `x-passage-duties` set by the gateway (the same contract as the agents API). Duties:
  `hub.subscription.write` for create, update, delete, resume and replay;
  `hub.delivery.read` for lists and reads. Create and update run `validateWebhookUrl`
  (422 HUB-0422). The signing secret is returned once, on create. `:replay` re-queues
  the subscription's own delivered or dead-lettered events from `from` whose type
  still matches its patterns, oldest first, up to `cap`, inside the caller's tenant,
  and answers `{enqueued, truncated}`. `api/openapi.yaml` describes the routes.
- `charts/integration-hub`: deployment (numeric non-root user), service, DB URL secret,
  Kafka values, `fieldCrypt.keysSecret` / `fieldCrypt.currentKeyId`, a NetworkPolicy
  (ingress to :8080 from the edge gateway only, :9090 from monitoring; egress to DNS,
  Postgres, Kafka and public https), ServiceMonitor toggle. `Dockerfile` runs `node src/main.ts` (Node 24
  strips types; no build step).

Configuration: `DATABASE_URL` (required), `MIGRATE_ON_START=true`, `HUB_POLL_INTERVAL_MS`,
`INTEGRATION_HUB_ALLOW_HTTP`, `KAFKA_BROKERS`, `KAFKA_GROUP` (default `integration-hub`),
`HUB_TOPICS` (comma separated), `HUB_DLQ_TOPIC`, `KAFKA_USERNAME`, `KAFKA_PASSWORD_FILE`,
`KAFKA_TLS_CA_FILE` (SASL SCRAM-SHA-512 over TLS), `FIELDCRYPT_KEYS_FILE`,
`FIELDCRYPT_CURRENT_KEY_ID`. Migrations must run as the owner role, never `passage_app`
(the migration refuses).

## Run and test

```sh
cd services/integration-hub-ts
npm ci
npm run typecheck   # tsc --noEmit
npm test            # vitest; the Postgres tests skip without PASSAGE_TEST_DATABASE_URL
```

Dependencies (approved for this service): `pg`, `kafkajs`; dev: `typescript`,
`@types/node`, `@types/pg`, `vitest`. The pure modules stay dependency-free.
The Postgres tests need a database owner role in `PASSAGE_TEST_DATABASE_URL` and, to
exercise row-level security as production does, a `passage_app` role (NOSUPERUSER
NOBYPASSRLS) in `PASSAGE_TEST_APP_DATABASE_URL`; each test file works in its own schema.

## Error codes

| Code | Meaning |
| --- | --- |
| HUB-0113 | Quota exceeded (429); `take` in `src/quota.ts` computes it and `Retry-After`, the gateway enforces it |
| HUB-0114 | Subscription paused after a 410 Gone response |
| HUB-0115 | Event dead-lettered after the retry schedule is exhausted |
| HUB-0400 / 0401 / 0403 / 0404 / 0405 / 0409 / 0413 / 0422 / 0500 | API: malformed request / no identity / missing duty / not found / method not allowed / stale version / body too large / unsafe webhook URL / unexpected error |
| EDI-0139 | PLM ECO effectivity earlier than today, moved to today (`decideEcoImport` in `src/plm.ts`) |
