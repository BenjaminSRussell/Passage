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

Everything that touches the network, the database or Kafka is later work. See
`NOT-IMPLEMENTED.md`.

## Run and test

```sh
cd services/integration-hub-ts
npm test        # node --test on test/webhook.test.ts and test/dispatch.test.ts (Node 24+, native type stripping)
```

There are no runtime dependencies. Strict typechecking needs `@types/node` and
`typescript`, which are not added in this slice.

## Error codes

| Code | Meaning |
| --- | --- |
| HUB-0113 | Quota exceeded (429), enforced by the gateway, not in this package |
| HUB-0114 | Subscription paused after a 410 Gone response |
| HUB-0115 | Event dead-lettered after the retry schedule is exhausted |
