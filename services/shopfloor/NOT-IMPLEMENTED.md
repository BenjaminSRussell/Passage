# NOT-IMPLEMENTED — PSG-25 (Wave 25) slice 1

Every PSG-25 deliverable and acceptance criterion that slice 1 does not build, with its
status and reason. Slice 1 covers the time and attendance core of the shopfloor service
(workers, registrations, pay calculation, approval, review lists, OEE arithmetic), the
supervisor web pages in `apps/web/src/modules/shopfloor/`, and the iot-edge domain core.
The edge connector's own gaps are in [../iot-edge/NOT-IMPLEMENTED.md](../iot-edge/NOT-IMPLEMENTED.md).

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| D1 | Shop-floor kiosk UI (clock in/out, start/stop jobs, quantities, quality checks, instructions and drawings) | later slice of PSG-25 | The kiosk is `apps/shopfloor/`, a separate Vite PWA, not started. This slice builds the supervisor pages in `apps/web/src/modules/shopfloor/`: workers list, new worker, worker detail (registrations and worker-days) and time approval (calculate, review, approve). |
| D2a | Registrations: clock in/out, absence, indirect, job start/stop | built (API and store) | Kinds are stored and validated; closed registrations carry elapsed hours; scrap needs a reason code. Absence and indirect registrations are stored but do not yet produce pay lines (`absence-paid`/`absence-unpaid` need absence codes). |
| D2a′ | Workers (shop floor projection of Party/Employee) | partial | Create, list and get with name, badge, sign-in user, cost rate and currency. Not built: `pin_hash` and kiosk login (FR-25.1), `default_resource_id`, `work_cell_id`, `profile_id`, `pay_agreement_id`, and update/deactivate (the `version` column is there for optimistic `If-Match` when update lands). |
| D2b | Pay-type calculation: overtime, shift premium, flex | partial | Base, daily/weekly overtime and Sunday are built. Shift premium and flex balances are not (needs profile and pay agreement persistence, below). |
| D2c | Approval | built | `pay:approve` locks days, refuses open flags (SF-0006), stale days (SF-0104) and approval of one's own day (SF-0106), returns already approved days unchanged, emits the payday event. Bulk approval by team is a loop over workers in the web page; a server-side team endpoint needs teams (w42). |
| D2d | Export to payroll (CSV and event batch) | later slice of PSG-25 | `pay:export` and export batch ids are not built. The `payday.approved` event is emitted for wave 43. |
| D2e | Job bundling and split by quantity or equally (BR-25.1) | later slice of PSG-25 | Needs job and bundle tables from w23 and job-card posting. The `bundle_id` column is stored only. |
| D2f | Profiles, pay agreements, worker profile link (ProfileDay, PayRule config) | later slice of PSG-25 | Calculation uses `domain.DefaultProfile()` and `domain.DefaultRules()`. Persisted profile and pay agreement tables and their admin endpoint are not built. |
| D2g | Recalculate on edit with reversal, supervisor edit with reason, missing clock-out confirmation | later slice of PSG-25 | Approved days are immutable (trigger). Registration edits, correction lines and reversal are not built; a registration that arrives after calculation blocks approval until the day is recalculated (SF-0104). |
| D2h | Auto clock-out job (hourly) and pay calculation nightly job | later slice of PSG-25 | Scheduler is not wired. Calculation runs on request only. |
| D3a | IoT ingestion: OPC-UA and MQTT adapters, Kafka publish, MachineEvent store, sequence-gap monitor | later slice of PSG-25 | Adapters and publish are edge work (`iot-edge`, see its NOT-IMPLEMENTED). The shopfloor consumer and MachineEvent table are not built. |
| D3b | OEE per resource per 15-minute bucket and shift; roll-up; OEE endpoint | partial | The ISO 22400 arithmetic (BR-25.5) is built in `domain/oee.go` and tested against the PRD example. The OeeBucket table, rollup job, and `GET /api/v1/shopfloor/oee` are not built. |
| D4 | Andon alerts, downtime reason codes, operator feedback | later slice of PSG-25 | Needs machine events (D3a) and kiosk (D1). Endpoints `downtimes/{id}:code` and `andon` are not built. |
| D5 | Offline-tolerant kiosk (PWA) with sync | partial | Server side is built: client-UUID idempotent batch replay (≤ 500 items, duplicate reported, stored in client timestamp order). The IndexedDB queue and PWA are part of D1 (`apps/shopfloor/`), not built. `chunk()` in the web client splits offline queues. Clock-skew correction and `job-finished-conflict` (BR-25.7) need jobs. |
| D6 | Control recipes (ISA-88) to kiosk or PCS, monitor with resend | later slice of PSG-25 | Needs batch orders (w24) and the edge write path (`iot-edge`). |
| D7 | PI sheet designer, versioned StepLibrary, kiosk execution, PI sheet archive | later slice of PSG-25 | Not started. Depends on ECO (w9) for library release and on w5 document store for archives. |
| D8 | Process messages with categories, destinations, retry and monitor | later slice of PSG-25 | Not started. Destinations depend on w21 journals, w33 quality and w35 webhooks. |
| D9 | iot-edge as a deployable (signed image, Helm, static binary, enrollment, mTLS, config, heartbeat, ring rollout, write-back) | partial | Domain core, config validation, Helm chart and admin endpoint are built. The rest is listed in `../iot-edge/NOT-IMPLEMENTED.md`. |
| Deploy | Kiosk app and edge chart deployed to the demo plant | later slice of PSG-25 | Depends on D1 and on the edge enrollment work. Edge chart exists with demo-plant defaults. |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 Kiosk E2E (Playwright), labor cost ties to order | later slice of PSG-25 | Needs D1 kiosk, job-card posting (w21) and order costing. The labor cost arithmetic (hours × rate per worker-day) is covered by unit tests. |
| AC2 10k events/min, OEE tile < 5 s, no loss on broker restart | later slice of PSG-25 | Needs D3a and the Kafka path. Sequence logic (no duplicates, gap detection) is unit tested in iot-edge. |
| AC3 Offline 30 min, 40 queued actions replay once in order | later slice of PSG-25 | Needs the PWA queue (D5 client). The server side replays by client UUID. |
| AC4 Phase exit: demo plant from planned order to finished goods | later slice of PSG-25 | Depends on waves 21–24 and the whole of PSG-25. |
| AC5 Every deliverable has code or is listed here | partial | This file lists every gap. Code exists only for the parts marked built or partial. |
| AC6 `go vet` and `go test` in each Go module; web `vitest` and `tsc`; OpenAPI and AsyncAPI match; chart lints | built | Verified by the checks listed in the README. |

## Acceptance criteria from PRD §4.25.8

| AC | Status | Reason |
|---|---|---|
| AC-25.1 Labor cost ties to the cent after approval | partial | Pay-line arithmetic is built. Production order cost postings are not (D2e, job-card posting). |
| AC-25.2 09:00–19:30 with 30-min break on a Tuesday → base 8.00 h, overtime-1.5 2.00 h | built | Covered by `TestCalculate_AC25_2_TuesdayOvertime` and `TestNetHours_BreakIsDeducted_AC25_2`. |
| AC-25.3 Machine events, broker restart, no loss or duplicates | later slice of PSG-25 | Needs D3a. |
| AC-25.4 OEE shift example 0.7522 | built (arithmetic) | `TestCompute_BR25_5_ShiftExample` asserts 0.7521, which is what the BR-25.5 formula gives (see "Requirements questions"). The tile update within 5 s needs D3b. |
| AC-25.5 Offline replay of 40 actions, once and in order | partial | Server dedup by client UUID and storage in client timestamp order are built. Conflict flags (`job-finished-conflict`) are not. |
| AC-25.6 Andon at 11 min DOWN | later slice of PSG-25 | Needs D3a and D4. |
| AC-25.7 Clock-in rounds to shift start (06:53 → 07:00) | built | Tolerance rule in `domain/time.go`, covered by `TestRound_BR25_2`. |
| AC-25.8 Approved day edit creates reversal and new line | later slice of PSG-25 | Needs D2g. Immutability is enforced by the trigger. |
| AC-25.9 Mandatory check blocks `finished` | later slice of PSG-25 | Needs D1 quality checks and feedback endpoint. |
| AC-25.10 Phase exit with configured product | later slice of PSG-25 | Same as AC4. |
| AC-25.11 Setpoint write to `pcs-opcua` | later slice of PSG-25 | Needs D6 and the edge write path. |
| AC-25.12 PI sheet completion rules | later slice of PSG-25 | Needs D7. |
| AC-25.13 Process message retry 1/5/15/60 min | later slice of PSG-25 | Needs D8. |
| AC-25.14 PI sheet archive PDF/A-2b with hash | later slice of PSG-25 | Needs D7 and w5 document store. |
| AC-25.15 Edge enrollment, config v1, canary rollback | later slice of PSG-25 | Needs the edge work in `../iot-edge/NOT-IMPLEMENTED.md`. |

## API and platform gaps

| Item | Status | Reason |
|---|---|---|
| `Idempotency-Key` on `workers`, `pay:calculate` and `pay:approve` | built | Stored in `idempotency_keys` in the command's transaction; a repeated key returns the stored outcome, a different request under the same key is `idempotency_key_reuse`. |
| Remaining endpoints: `sessions:badge`, `jobs`, `jobs/{id}:feedback`, `operations/{id}/instructions`, `pay:export`, `oee`, `downtimes/{id}:code`, `andon`, `machines` | later slice of PSG-25 | Need the kiosk, machine events and job dispatch from w23/w24; export needs export batches (D2d). |
| `ETag` and `If-Match` | not applicable in this slice | No PUT, PATCH, DELETE or update custom method exists yet; `worker.version` is ready for it. |
| FR-25.14 supervisor workspace (who is clocked in, current jobs, open downtimes, OEE tiles, pending approvals) | partial | Pending approvals are built (time approval page, `GET worker-days?status=calculated`). Who-is-clocked-in, jobs, downtimes and OEE tiles need D1, D3 and D4. |
| `legal_entity_scope` restrictive RLS policy | not applicable | No table in slice 1 carries `legal_entity_id`. Add when legal entity scoping reaches shop floor data. |
| Migration `0001` applied to an empty database | verified in review | Applied to an empty PostgreSQL 16 schema, and the store and HTTP tests ran as `passage_app` with RLS forced. CI runs them when `PASSAGE_TEST_DATABASE_URL` is set; they skip without it. |
| Access to `ProfileDay`, `PayRule`, `pay_agreement`, `flex_balance` tables | later slice of PSG-25 | Persisted configuration is not in slice 1. |
| `shopfloor-bff` (TS, SSE) | handoff | Owned by the BFF wave (web-bff / device-bff). Not in this service's owned paths. |
| Kafka consumer group `shopfloor-oee`, rollup and partition jobs | later slice of PSG-25 | Need D3a and D3b. |
| Feature flags `shopfloor.iot`, `shopfloor.oee`, `shopfloor.pay-export` | later slice of PSG-25 | Flag service is not wired. |
| Catalog codes SF-0001..SF-0020 (except SF-0006 and SF-0007, raised here) | later slice of PSG-25 | Each code is raised by the feature that needs it. |
| Operator analytics switch `shopfloor.operator-analytics` | later slice of PSG-25 | Relevant when OEE per operator is built (FR-25.13). |

## Requirements questions

| Item | Question |
|---|---|
| BR-25.5 / AC-25.4 shift example | The PRD says OEE = 0.7522, but A × P × Q = 0.875 × 0.904762 × 0.95 = 0.752083, which rounds to 0.7521 (the rounded factors give 0.7521 as well). The code follows the formula; the requirements owner should correct the example. |
| Pay week start | BR-25.8 says the pay week start is profile-defined. Until profiles are persisted (D2f) it is Monday (`domain.DefaultRules().WeekStart`). |
