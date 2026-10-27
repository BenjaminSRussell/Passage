# shopfloor

Shop floor execution service. Wave reference: **PSG-25 / Wave 25**
(Shop Floor Execution, Time Registration & IoT), PRD section 4.25.

## Slice 5: retry sweep, delete with reason, monitor paging

- Retry sweep (`POST process-messages:retry-due?limit=`, duty `shopfloor.process.deliver`,
  limit 1 to 500, default 100; FR-25.17 job "every 1 min"): takes the destinations that
  failed at least once, are still pending and whose `nextAttemptAt` has passed, oldest
  first, and publishes `processmessage.received` again once per message naming only those
  destinations. Each is leased until its next scheduled attempt (the same 1, 5, 15 or 60
  minute wait), so a consumer that does not answer is published to on the schedule, not
  every minute; its next report replaces the lease. Messages another transaction holds (a
  sweep, report, reprocess or delete) are skipped (`for update skip locked`), so two
  sweeps never publish the same retry. Destinations that never reported wait for their
  consumer, destinations in error wait for a supervisor, and deleted messages are never
  retried. The sweep runs under one tenant's row-level security: the platform scheduler
  calls it every minute per tenant with a service identity (handoff, see NOT-IMPLEMENTED).
  Counted in `shopfloor_process_deliveries_retried_total`.
- Delete (`POST process-messages/{id}:delete`, duty `shopfloor.process.create`,
  `Idempotency-Key`, reason 1 to 500 characters, authenticated user): FR-25.17 lets a
  destination in error be reprocessed or the message deleted with a reason. Only a message
  with a destination in `error` can be deleted (SF-0119); a deleted one cannot be deleted
  again, reported or reprocessed (SF-0120). The row is kept for audit with `deletedAt`,
  `deletedBy` and `deleteReason` (migration 0005), leaves the monitor and is still read by
  id. `dev.passage.shopfloor.processmessage.deleted.v1` names the destinations that never
  received it, so the EBR consumer can record the missed message (BR-24.15).
- Monitor paging (`GET process-messages?cursor=`): keyset on (received time, id), newest
  first. The response carries `nextCursor`, null on the last page; a cursor the list did
  not return is COM-0001 naming `cursor`.
- Web: the monitor loads further pages with *Load more*; the message detail shows a
  deleted message's audit fields and offers *Delete message* (with a reason) when a
  destination is in error.

## Slice 4: process messages

- Record (`POST process-messages`, duty `shopfloor.process.create`, FR-25.17): messages from
  kiosk PI sheets, the edge connector or manual entry, each with a category (phase-start,
  phase-end, consumption, yield, process-data, quality-result, deviation, order-status,
  custom), an order, an optional ISA-88 phase path and characteristic values (numeric,
  text or boolean). Idempotent by `(source, sourceMessageId)`: a replay answers 200 with
  the stored message and `duplicate: true` and writes nothing.
- Validation (BR-25.13): numeric values are decimal strings checked against their inline
  `min`/`max`; an out-of-limit value is accepted and flagged (`outOfLimit` on the value and
  the message, and in the event so the EBR consumer records an exception). A category that
  rejects out-of-limit values refuses the message with SF-0114 (no default category does
  yet). A malformed value is SF-0115.
- Routing: the category decides the destinations (`domain.CategoryByCode`): EBR, journal
  (w21), quality (w33), Kafka and webhook (w35). One delivery row per destination is
  written with the message and `dev.passage.shopfloor.processmessage.received.v1` in one
  transaction. Kafka is that outbox event, so it is delivered at commit.
- Delivery reports (`POST process-messages/{id}/deliveries/{destination}:report`, duty
  `shopfloor.process.deliver`, held by the consuming contexts, `Idempotency-Key`): a failure
  waits 1, 5, 15 and then 60 minutes (`nextAttemptAt`); the fifth failed attempt leaves the
  destination in `error`. A delivered destination is final. Message status: `delivered`
  when every destination is, `partially-delivered` when some are, `received` while none is
  delivered and some are pending, `error` when none is delivered and none can retry. The
  first time a message becomes delivered, `processmessage.delivered.v1` is published.
  Failures are logged as SF-0016 warnings.
- Reprocess (`POST process-messages/{id}:reprocess`, duty `shopfloor.process.create`,
  `Idempotency-Key`): a destination in `error` returns to pending with its attempts reset
  and the reason kept; `processmessage.received` is published again naming that
  destination alone, so the other consumers do not write twice (AC-25.13).
- Monitor (`GET process-messages`, `GET process-messages/{id}`, duty `shopfloor.view`):
  newest first, filtered by status, category, destination and order, with per-destination
  attempts, last error and next attempt.
- Web: process message monitor (filters, status, out-of-limit flag), message detail with
  values and deliveries and a reprocess form for destinations in error, and a manual
  record form.

## Slice 3: job bundles and offline replay

- Bundle split (FR-25.3, BR-25.1) in `POST registrations`: the closed job registrations of
  a batch that share a `bundleId` must belong to one worker, cover the same interval and
  name each job once. The interval's hours are split across them by good quantity
  (`domain.SplitBundleHours`; equal split when no good quantity is reported) in 0.01 h
  shares that sum exactly to the rounded total (largest remainder, exact integer
  arithmetic on the decimal quantities). The bundle therefore counts its time once, and
  `registration.posted` carries each job's share with its `bundleId`. Every closed job of
  a bundle must arrive in one batch: a later batch adding a closed job to a bundle is
  SF-0113 (a resend of stored items is still a no-op). `GET registrations?bundleId=`
  reads a bundle back.
- Offline clock skew (FR-25.12, BR-25.7): a batch may carry `deviceSentAt`, the kiosk's
  own clock when it sent the batch. The server measures its clock minus the device clock;
  above 5 minutes the offset is added to every item's times and stored on each
  registration (`clock_offset_s`, `clockOffsetSeconds`). The response returns the measured
  offset and whether it was applied. Client-timestamp ordering and duplicate client UUIDs
  were already handled by the store (slice 1).
- Domain rules the kiosk and job paths will reuse: `OrderReplay`, `FinishedConflicts`
  (`job-finished-conflict`, needs the job model) and `CheckQueueCapacity` (SF-0010, the
  PWA queue cap). They have no endpoint yet; see NOT-IMPLEMENTED.md.
- Web: the worker detail registrations show the bundle link and any clock correction; a
  bundle page lists the jobs with their hours share, the bundle total and the worker.

## Slice 2: machines, floor monitoring and payroll export

- Machines (`POST/GET machines`): an edge machine code mapped to one resource with its ideal
  cycle time (and optional site). One machine per resource and one resource per code in
  this slice (SF-0109).
- Machine-event ingest (`POST machine-events`, called by the iot-edge gateway with the
  `shopfloor.machine.ingest` duty): batches of up to 500 events, classified per machine by
  BR-25.4. Events with seq at or below the machine's last stored seq and events older than
  7 days are dropped and counted; missing seq runs are returned in `gaps` (SF-0008, a
  warning) so the edge can replay them. Accepted events are stored under
  `(tenant, machine, seq)` with the machine row locked, so a replayed batch never double
  counts. An unknown machine code rejects the whole batch (SF-0108).
- Downtime and andon (FR-25.11, BR-25.10): a DOWN state opens a downtime and any other
  state ends it. When an ingest finds a downtime open for more than 10 minutes (server
  time), it raises one andon per (resource, kind) and emits
  `dev.passage.shopfloor.andon.raised.v1` in the same transaction. The alert clears on a
  later ingest once the downtime has been over for 5 minutes. Downtimes are listed
  (`GET downtimes`) and coded (`downtimes/{id}:code`, reason and optional planned or
  unplanned category); coding does not clear the alert (AC-25.6).
- OEE series (`GET oee`): ISO 22400 buckets of 5, 15 or 60 minutes computed on read from
  the machine events (BR-25.5). Until the w23 calendar supplies planned time, every known
  state except OFF counts as scheduled; RUNNING is run time. Minutes and counts are summed
  to roll up, never ratios.
- Payroll export (`pay:export`): every approved pay line in a range of up to 31 days that is
  not yet exported joins one new batch (a line is exported once). The response carries the
  CSV (batch id on every row) and `dev.passage.shopfloor.payday.exported.v1` carries the
  lines, written in the same transaction. Nothing new is SF-0111. Batches are listed
  (`GET pay-exports`) and read back with their CSV (`GET pay-exports/{id}`).
- Web: machines list and registration, downtimes with reason coding, andon board, OEE by
  day and bucket, payroll export list, new export and batch detail with CSV download.

## Slice 1: time and attendance core

- Workers (shop floor projection of an employee): create with name, badge, sign-in user,
  cost rate and currency; list and get. A badge is unique per tenant (SF-0105).
- Batch registrations from the kiosk: clock-in/out, job start/stop, absence and indirect
  time, each identified by a client UUID so an offline replay is idempotent (BR-25.7).
  A new item emits `dev.passage.shopfloor.registration.posted.v1` in the same transaction.
- Pay calculation per worker and date range (`pay:calculate`): rounding and tolerances
  (BR-25.2), break deduction (BR-25.1), daily overtime over 8.00 h at 1.5x, weekly overtime
  over 40.00 h at 1.5x per pay week (Monday start) without double counting (BR-25.8; the
  earlier days of the week are read even when outside the range), Sunday at 2.0x, missing
  clock-out flags (BR-25.3), 24.00 h per day cap (SF-0007). Amounts use the worker's cost
  rate, rounded once to the currency's minor unit through `platform/money`.
- Approval (`pay:approve`): locks calculated days, refuses days with open flags (SF-0006),
  days whose registrations arrived after calculation (SF-0104) and approval of one's own day
  (SF-0106); already approved days are returned unchanged, and each newly approved day emits
  `dev.passage.shopfloor.payday.approved.v1` (consumed by payroll, wave 43).
- Review lists for the approval screen: worker-days with pay lines and flags, and a
  worker's registrations.
- `Idempotency-Key` on every command except the registration batch and the machine-event
  batch (both idempotent per item):
  the outcome is stored with the change and a repeated key returns it.
- OEE arithmetic (ISO 22400 aligned, BR-25.5) as a pure function, verified against the
  PRD shift example (the formula gives 0.7521; the PRD text says 0.7522, see NOT-IMPLEMENTED).
- Web: `apps/web/src/modules/shopfloor` (workers list, new worker, worker detail with
  registrations and days, time approval).

Everything else in PSG-25 is listed in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## Layout

```
cmd/shopfloor          entrypoint: dbx.Connect -> (dbx.Migrate) -> httpx routes -> serve
internal/domain        pure rules: punch pairing, rounding, pay lines, OEE (no I/O)
internal/store         pgx repository, tenant-scoped transactions, outbox writes
internal/api           HTTP handlers, validation, problem details
internal/events        event payload structs
migrations/            0001_init.sql (worker, time_registration, worker_day, pay_line, idempotency_keys, outbox)
                       0002_floor.sql (machine, machine_event, downtime_record, andon_alert,
                       pay_export_batch, pay_export_line)
                       0003_bundles_offline.sql (clock offset, bundle index)
                       0004_process_messages.sql (process_message, process_message_delivery)
                       0005_process_message_delete.sql (deletion audit columns, monitor keyset index)
api/openapi.yaml       OpenAPI 3.1 for every endpoint
events/asyncapi.yaml   AsyncAPI 3 for every event
charts/shopfloor       Helm chart (3 replicas, /healthz and /readyz probes, ServiceMonitor toggle)
```

## Run

```
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_shopfloor
export MIGRATE_ON_START=true   # dev only; stage/prod run migrations as an Argo PreSync Job
go run ./cmd/shopfloor
```

Ports: `HTTP_ADDR` (default `:8080`), `ADMIN_ADDR` (default `:9090`, serves `/metrics`).

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app... go test ./...
```

Domain tests are pure table-driven tests plus `pgregory.net/rapid` properties. Store tests
and the HTTP end-to-end test use `platform/go/testkit` (`DBWithApp`: migrations as the admin
role, queries as `passage_app`, so RLS is enforced) and skip when
`PASSAGE_TEST_DATABASE_URL` is unset. `openapi_test.go` and `events_test.go` keep the
OpenAPI and AsyncAPI files in step with the router and the published event types.

## Authorization

| Route | Duty (PRD 4.25.17) |
|---|---|
| POST `/api/v1/shopfloor/workers` | `shopfloor.setup.maintain` (Maintain shop floor setup) |
| GET `/api/v1/shopfloor/workers`, `/workers/{id}` | `shopfloor.view` (View shop floor) |
| POST `/api/v1/shopfloor/registrations` | `shopfloor.time.register` (Register time) |
| GET `/api/v1/shopfloor/registrations` | `shopfloor.view` |
| POST `/api/v1/shopfloor/pay:calculate` | `shopfloor.pay.calculate` (Calculate time registrations) |
| POST `/api/v1/shopfloor/pay:approve` | `shopfloor.pay.approve` (Approve time registrations) |
| GET `/api/v1/shopfloor/worker-days` | `shopfloor.view` |
| POST `/api/v1/shopfloor/machines` | `shopfloor.setup.maintain` |
| GET `/api/v1/shopfloor/machines`, `/downtimes`, `/andon`, `/oee` | `shopfloor.view` |
| POST `/api/v1/shopfloor/machine-events` | `shopfloor.machine.ingest` (edge gateway service identity) |
| POST `/api/v1/shopfloor/downtimes/{id}:code` | `shopfloor.time.register` |
| POST `/api/v1/shopfloor/pay:export`, GET `/pay-exports`, `/pay-exports/{id}` | `shopfloor.pay.export` (Export time to payroll) |
| POST `/api/v1/shopfloor/process-messages`, `/process-messages/{id}:reprocess`, `/process-messages/{id}:delete` | `shopfloor.process.create` (Create process messages: manual, reprocess/delete with reason) |
| GET `/api/v1/shopfloor/process-messages`, `/process-messages/{id}` | `shopfloor.view` |
| POST `/api/v1/shopfloor/process-messages/{id}/deliveries/{destination}:report`, `/process-messages:retry-due` | `shopfloor.process.deliver` (consuming context and scheduler service identities) |

Duties are checked by the platform `authz` middleware. The OPA decision point replaces the
local check in a later wave, behind the same interface.

## Error codes

Problem details use `application/problem+json` with `type` `https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| `AUTH-0001` | 401 | Missing or malformed tenant, or no authenticated user for approval, export or deletion |
| `COM-0001` | 400 | Request failed validation (or no `Idempotency-Key`); `errors` lists the fields |
| `AUTH-0003` | 403 | Missing duty |
| `SF-0006` | 422 | Day cannot be approved: flags are open |
| `SF-0007` | 422 | Worker-day exceeds 24.00 hours |
| `SF-0101` | 422 (404 on GET) | Worker not found or inactive |
| `SF-0102` | 422 | Approved days cannot be recalculated; a correction is required |
| `SF-0103` | 422 | No calculated days in the approval range |
| `SF-0104` | 422 | Registrations arrived after calculation; recalculate before approving |
| `SF-0105` | 409 | Badge already assigned to another worker |
| `SF-0106` | 403 | You cannot approve your own time registrations (segregation of duties) |
| `SF-0107` | 409 | A concurrent request used the same `Idempotency-Key`; retry |
| `SF-0108` | 422 | Machine code is not registered (the ingest batch is rejected) |
| `SF-0109` | 409 | Machine code or resource is already registered |
| `SF-0110` | 404 | Downtime not found |
| `SF-0111` | 422 | No approved pay lines left to export in the range |
| `SF-0112` | 404 | Export batch not found |
| `SF-0113` | 422 | A bundle already has closed jobs from another batch; send every job of a bundle in one batch |
| `SF-0114` | 422 | A value is outside its limits and the category rejects out-of-limit values |
| `SF-0115` | 422 | A characteristic value does not match its type, or its limits are malformed |
| `SF-0116` | 422 | Only a destination in `error` can be reprocessed |
| `SF-0117` | 404 | Process message not found |
| `SF-0118` | 404 | The message has no delivery to that destination |
| `SF-0119` | 422 | Only a message with a destination in `error` can be deleted |
| `SF-0120` | 422 | The process message was deleted; it takes no report, reprocess or second delete |
| `idempotency_key_reuse` | 422 | `Idempotency-Key` already used with a different request |

Catalog entries SF-0001..SF-0020 (PRD 4.25.18) are reserved; only the codes above are raised.
SF-0008 (sequence gap) is reported in the ingest response `gaps` and counted in
`shopfloor_machine_seq_gaps_total`, not raised as an error. SF-0016 (delivery failed) is a
logged warning on a failed delivery report, not an error response.

## Dependencies

Only the platform module and what it already pulls in (chi, pgx/v5, shopspring/decimal,
google/uuid, rapid for tests). No new dependencies in this slice.
