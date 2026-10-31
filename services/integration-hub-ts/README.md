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
CloudEvents JSON body over HTTP (`httpTransport`, fetch with a timeout; no response is
`null`), applies `nextStep`, and records the outcome; an event leaves the pending set
only after a 2xx was recorded, so delivery is at-least-once. After the 8th retry the
event goes to the dead-letter list (HUB-0115); a 410 pauses the subscription until
`resume`. `replay(archive, from, cap)` enqueues every `planReplay` page with
`ce-passagereplay: true`. Storage is the `DeliveryStore` interface with an in-memory
implementation. `test/dispatcher.test.ts` runs it against a real HTTP receiver on
localhost that keeps an order projection (failures, a dropped connection, a lost 2xx,
a crash before recording, dead-letter after 9 attempts on the BR-35.7 schedule,
replay into an empty consumer and from a timestamp).

The Postgres store, the outbox consumer, the `next_retry_at` poller and Kafka are
later work. See `NOT-IMPLEMENTED.md`.

## Run and test

```sh
cd services/integration-hub-ts
npm test        # node --test on every file in test/ (Node 24+, native type stripping)
```

There are no runtime dependencies. Strict typechecking needs `@types/node` and
`typescript`, which are not added in this slice.

## Error codes

| Code | Meaning |
| --- | --- |
| HUB-0113 | Quota exceeded (429); `take` in `src/quota.ts` computes it and `Retry-After`, the gateway enforces it |
| HUB-0114 | Subscription paused after a 410 Gone response |
| HUB-0115 | Event dead-lettered after the retry schedule is exhausted |
| EDI-0139 | PLM ECO effectivity earlier than today, moved to today (`decideEcoImport` in `src/plm.ts`) |
