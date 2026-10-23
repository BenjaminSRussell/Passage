# device-bff

## Purpose & Boundaries
TS edge service `services/device-bff-ts` (wave 8 allocation: first user wave 25, then 32, 45). Session BFF for shopfloor kiosk, warehouse-mobile and technician PWAs: device enrollment, worker sessions, offline packets, ordered command sync. No database of its own.

## Interfaces
- Same session endpoints as web-bff plus: `POST /bff/v1/device/enroll`, `POST /bff/v1/device/worker-session` (badge + PIN), `POST /bff/v1/sync`, `GET /bff/v1/packets/:kind` (`roster`, `work-orders`, `pick-lists`, `reference`; versioned, ETag, size-capped; e.g. `/bff/v1/packets/shopfloor?resource=`).
- `/bff/v1/sync`: ordered replay of queued commands, each `(deviceId, commandId)`; idempotent on that pair (repeat returns stored result); applied in queue order under the worker's token; batches up to 50; per-command status returned; BFF replays with `Idempotency-Key = id`.
- Platform side: `POST /v1/platform/devices` (+`:revoke`) register kiosk/scanner/edge; device-flow enrolment code.
- Envoy `device` rate class 120 req/min/device.

## Data Ownership
absent — the source document does not address this (stateless; packets assembled from context APIs).

## Events Produced
absent — the source document does not address this

## Events Consumed
absent — the source document does not address this (`manufacturing-shopfloor-bff` consumer lives in manufacturing and pushes to kiosks).

## Dependencies
internal: platform (devices), manufacturing, warehouse, projects, edge-identity. external: Keycloak (per-device client `passage-kiosk-<deviceId>`, `passage-device`), OPA.

## Gotchas & Constraints
- Kiosk identity: supervisor with duty `EnrollDevices` enrolls; per-device Keycloak client; device cookie `__Host-passage_device` (SameSite=Strict, 90-day rotation); worker identity via RFC 8693 token exchange (device client -> worker subject), scoped to `ReportProduction` + time-registration duties; actor = `user:<sub>`. 5 failed PINs lock badge 5 min with audit entry.
- Offline sign-in with cached Argon2id verifiers only for offline-safe duties (time/operation feedback), 5 attempts, TTL 24 h; security sign-off pending (Q13 blocks wave 25).
- Queue entries replay only under session whose `sub` == `actorSub`; others locked. Conflict outcomes (412 `conflict_version`, `state_transition_invalid`, `inventory_insufficient`, `period_closed`) -> conflict inbox, never auto-discarded; 429 honors Retry-After; 5xx backoff 1-60 s; 401 pauses.
- Never store P3 on device; clock skew > 5 min warning.
- Device sessions: 12 h shift for warehouse-mobile/technician, idle lock 15 min.

## Source Sections
- Frontend 2.4 (4856), 8.5-8.6 (5678-5688), 7.6 offline engine (5457-5510); Backend 5.20 (987-994), 3.2 (301)
