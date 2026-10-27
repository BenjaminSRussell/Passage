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

Everything that touches the network, the database or Kafka is later work. See
`NOT-IMPLEMENTED.md`.

## Run and test

```sh
cd services/integration-hub-ts
npm test        # node --test test/webhook.test.ts (Node 24+, native TypeScript type stripping)
```

There are no runtime dependencies. Strict typechecking needs `@types/node` and
`typescript`, which are not added in this slice.

## Error codes

| Code | Meaning |
| --- | --- |
| HUB-0113 | Quota exceeded (429), enforced by the gateway, not in this package |
| HUB-0114 | Subscription paused after a 410 Gone response |
| HUB-0115 | Event dead-lettered after the retry schedule is exhausted |
