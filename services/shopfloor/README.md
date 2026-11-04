# shopfloor

Shop floor execution service. Wave reference: **PSG-25 / Wave 25**
(Shop Floor Execution, Time Registration & IoT), PRD section 4.25.

## Slice 23: idle downtime and dated cost rates (FR-25.11, BR-25.6, D2a')

**Idle downtime** (migration `0022_idle_downtime.sql`: `downtime_record.cause`, `down` or `idle`, default `down`; no new table or
grant). `POST andon:sweep` first opens an idle downtime for every machine whose latest state report is RUNNING and that has
sent no count for its idle threshold, starting at the last count (or at the start of that RUNNING stretch, the first RUNNING
report after the latest other state), unless the machine already has a downtime open (one open downtime per machine) or its
events are being ingested right now (`for update skip locked`). The result carries `idleOpened`. The record is an ordinary
downtime: it can be coded with a reason, the 10-minute andon of FR-25.11 applies to it, and it feeds nothing into OEE a second
time (OEE already counts the gap as IDLE from the events). At ingest, a count or a state report that leaves RUNNING (DOWN, SETUP,
OFF) ends an open idle downtime at that event's time; a DOWN report ends it and opens a down downtime; an alarm, a measurement,
a repeated RUNNING report or an explicit IDLE report leaves it open (a RUNNING heartbeat is not production, and ending the
record there would let the next sweep open a second one over the same gap). The threshold is the machine's
`idle_threshold_s` (default 300 s, 30 to 3600), the same one OEE uses. The sweep locks the candidate machines first
(`skip locked`) and reads their events again after the lock, so an ingest that committed in between is seen. On the web the downtime list shows an open idle downtime as Idle.

**Dated cost rates** (migration `0023_worker_cost_rate.sql`, table `worker_cost_rate`: tenant-scoped with forced row-level
security, SELECT and INSERT grants because rows are append-only). `POST workers/{id}/cost-rates` (`effectiveFrom` date between
2000-01-01 and 2099-12-31, `costRate` non-negative decimal with up to 6 decimals, optional `note`; `Idempotency-Key`; duty
`shopfloor.setup.maintain`) and `GET workers/{id}/cost-rates` (newest first; `shopfloor.view`). One rate per worker and date
(SF-0170, 409): a change takes effect on a later date. The worker's own sign-in user cannot set it (SF-0106, 403). Each rate
publishes `dev.passage.shopfloor.workercostrate.set.v1`. `pay:calculate` (and so `pay:revise`) prices every line of a day (base,
overtime, shift premium, paid absence) at the latest rate dated on or before that day, and a day before the first dated rate at the
worker's own `costRate`, so a raise reprices only the days from its date when they are recalculated. The currency stays on the
worker. Nothing is rewritten when a rate is added. A calculated day priced by a rate added after its calculation (the
latest rate dated on or before it is newer than the calculation) is stale for `pay:approve` (SF-0104) until it is
recalculated, so a past-dated rate is never approved over by mistake. Approved days keep their lines and stay approved
(slice 22 rule); `pay:calculate` refuses them (SF-0102) and only an explicit `pay:revise` reprices them. On the web the worker page
has a cost rate history card (list and add form).

## Slice 22: worker maintenance (D2a')

A supervisor edits a worker and deactivates one who left (migration `0021_worker_maintenance.sql`: the
`deactivated_at`, `deactivated_by` and `deactivation_reason` columns of `worker`; no new table, so no new grant).
All three routes use the existing duty `shopfloor.setup.maintain`, `If-Match` on the worker version (SF-0143, 412)
and an `Idempotency-Key`, and publish `dev.passage.shopfloor.worker.saved.v1` through the outbox.

- `PUT workers/{id}` replaces name, badge, sign-in user, cost rate and currency (the party link never changes).
  A badge held by another worker is SF-0105; a null badge removes it. An inactive worker can be edited. The worker's
  own sign-in user cannot edit the worker (SF-0106, 403): unlinking the user would get round the own-approval rule, and
  the cost rate is the worker's pay. The currency cannot change once a day of the worker is approved (SF-0169, 422),
  since a revision of that day would mix currencies in it.
- `POST workers/{id}:deactivate` (body `reason`, 1 to 500 characters) marks the worker inactive and records who, when and
  why. The badge then fails with SF-0001 and registrations with SF-0101. A worker who is clocked in or has an open job
  start is SF-0166 (close the open time first); an inactive worker is SF-0167.
- `POST workers/{id}:reactivate` makes the worker active again and clears the record; an active worker is SF-0168.
- The pay of days already worked is not blocked by deactivation: `pay:calculate`, `pay:approve`, `pay:revise` and the
  export still run for an inactive worker (earlier they refused it with SF-0101, which would have stranded the final pay).
  A change of cost rate rewrites no pay line: approved days keep their lines and stay approved (the edit does not make
  them stale), and days already calculated keep theirs until a supervisor recalculates them. Slice 23 adds dated rates:
  a recalculation, or a `pay:revise` of an approved day, prices each day at the rate effective on its own date.
- Registrations lock the row of every worker in the batch (for share; for update when the batch opens a job), so a
  punch that races a deactivation either is seen by it (SF-0166) or sees the inactive worker (SF-0101); an inactive
  worker is never left clocked in.

## Slice 16: a worker's home resource and "my jobs" (D2a')

A worker has an optional home resource, the machine or work centre the worker normally runs
(`worker.default_resource_id`, migration `0015_worker_home_resource.sql`; no new table, so no new grant).
`POST workers/{id}:assign-resource` (duty `shopfloor.setup.maintain`, `If-Match` on the worker version,
`Idempotency-Key`) sets or clears it and publishes `dev.passage.shopfloor.worker.assigned.v1`, whose payload
now also carries `defaultResourceId`. `GET jobs?workerId=` (the PRD filter) returns the jobs of the worker's
home resource; a worker without one sees every job, an unknown worker is SF-0101 (404), and `workerId`
cannot be combined with `resourceId`. Resources belong to engineering data (w9), so the store accepts only a
resource this tenant's shop floor knows, a registered machine's resource or one with dispatch jobs (SF-0156,
422); the migration adds a nullable column under a `lock_timeout` and no index. The badge session carries
`defaultResourceId` too. On the web the kiosk lists the signed-in worker's jobs and, for a worker without a
home resource, says the list holds every job; the worker page has a *Home resource* card (choices are the
resources of registered machines and of the jobs on the dispatch list). The OpenAPI file also lost a stray duplicate `PayRange` block that made
it invalid YAML, so the doc gates run with no FAIL line again.

## Slice 17: production status and downtime reasons

`cmd/shopfloor consume` runs the `shopfloor-production` consumer of `operation.statuschanged.v1` (production, w21). It stores
the latest status per production order id and operation number in `operation_production_status` (migration `0016`),
ordered by the order version then `occurredAt`, and dead-letters an event whose source is not `production`. An online
job-start on a job of a finished operation, at or after the finish, is SF-0157; an offline replay of one is accepted and
flagged `job-finished-conflict` (BR-25.7). It starts only once
its service principal is seeded (see NOT-IMPLEMENTED.md). Migration `0017` adds the downtime reason code master
(`downtime-reasons`); coding a downtime then takes an active reason and its category (SF-0158 to SF-0160).

## Slice 18: production-closed jobs

When production finishes an operation, the consumer also closes every worker's open `job-start` on the jobs of that
operation in the same transaction: a `job-stop` from the start to the finish (zero hours for a start after the
finish, 24 hours at most and then also `auto-stop-capped`), flagged `production-finished`, with `registration.posted`.
These are job (labour) hours for the job-card journal; pay is calculated from clock punches and does not change.
Starts of a bundle stay open (slice 19 closes them), and so does a start dated before the finish but stored after it; both show
`productionFinishedAt` on the kiosk session's `openJobs` and on `GET clocked-in`, where the supervisor's page marks
them. A kiosk `job-stop` that arrives later for the same start supersedes the server's (`registration.corrected`, the
server's stop becomes 0.00 h), so the hours are not counted twice; production's reopen does not reopen the closed start.
A registration batch takes a shared transaction lock on each job it names before its worker locks, and the finish
takes the same locks exclusively on the operation's jobs (migration `0018` indexes the lookup) before it reads the open
starts, so a start or stop sent while the finish is applied is either seen by it or sees it. `POST jobs/{id}:feedback`
on a job whose operation production finished is SF-0161 until production reopens it; a resend of stored feedback is
still a duplicate.

## Slice 19: production finish closes bundles

Production's finish also closes a bundle with an open member on the finished operation, whole, in the same
transaction: every member gets a `production-finished` job-stop and the bundle's time from its earliest start to the
finish is split again over all its members by BR-25.1 (good quantity on the starts, equal when none), so members on
other operations keep their share up to the finish and are started again by the worker. The finish takes the bundle
advisory locks (as a batch closing a bundle does) before the job locks. The worker's own stop of the bundle sent after
the finish (an offline replay) is not SF-0113: it is stored and the server's bundle stops are corrected to zero length
(`registration.corrected`), so the bundle's hours count once. A start that began after the finish but was stored
before the event arrived is closed at 0.00 h and its stop also carries `job-finished-conflict` for supervisor review.

## Slice 21: process message categories (FR-25.17, BR-25.13)

`process_category` (migration `0020`) is the tenant's category master. A row overrides the built-in default of the same
code (`phase-start` through `custom`) or adds a code of its own (lowercase letters, digits, hyphens, up to 32), and carries
the destinations (`ebr`, `journal`, `quality`, `kafka`, `webhook`), `rejectOutOfLimit` and characteristic definitions (name,
type, unit, limits, required). `POST`/`GET process-categories` and `GET`/`PUT process-categories/{id}` (create with an
`Idempotency-Key`, update with `If-Match`; the code never changes; duties `shopfloor.setup.maintain` and `shopfloor.view`)
publish `dev.passage.shopfloor.processcategory.saved.v1`. The list returns the tenant's rows and the built-in defaults no row
overrides (`builtIn: true`). `POST process-messages` resolves the category first: a master row wins, else the default,
else SF-0164; an inactive row is SF-0165. The definitions then govern the values they name (unit and limits come from the
definition, a different type or a missing required value is SF-0115), and a category that rejects out-of-limit values
refuses a value outside the limits with SF-0114. Messages already stored keep the routing they were received with, and
a replay of a stored (source, sourceMessageId) still answers 200 with the stored message after its category was deactivated
or tightened. The database checks `process_message.category_code` against the code format (it no longer lists the built-in
codes), so the API's resolve step is what keeps an unknown code out.

## Slice 20: andon sweep, scrap rate and quality fail alerts (FR-25.11)

`POST /v1/shopfloor/andon:sweep` (duty `shopfloor.oee.rollup`, the scheduler identity that runs the OEE rollup; no
person role holds it) is the scheduled andon job. It raises the downtime alert for every downtime open more than 10
minutes (a machine that fell silent while DOWN never ingests again), clears downtime alerts 5 minutes after the downtime
ended, and reads the latest completed `oee_bucket` of each machine's resource: more than 5 % scrap in at least 10 pieces
raises a `scrap-rate` alert; a later bucket at or under 5 % that ended 5 minutes ago clears it. It locks the machine rows
it raises for and skips a machine whose events are being ingested (that ingest raises the alert itself).
A feedback posting with a failed check raises a `quality-fail` alert naming the job; a later posting that leaves the job
without a failed result clears it, and `POST andon/{id}:clear` (duty `shopfloor.check.override`, the supervisor's
authority over a failed check; reason, `Idempotency-Key`) clears it by hand. One open alert per resource and kind
(BR-25.10), so a failure another job on the resource records while the alert is open is folded into it; when that alert
clears (either way) such a failure raises a new alert for its job. Each raise publishes `andon.raised` (kind and `jobId`).
Migration 0019 widens `andon_alert.kind` and adds `job_id`, `detail` (cut to 500 characters), `cleared_by` and
`clear_reason`. The sweep is idempotent and takes no key; the scheduler that calls it is a handoff.

## Slice 15: supervisor reopen of a finished job (FR-25.3)

A finished job takes no feedback (SF-0122) and leaves the dispatch list. `POST jobs/{id}:reopen` (duty
`shopfloor.job.reopen`, a supervisor; not in the PRD duty table) puts it back to `started` with a reason
(1 to 500 characters) when more must be reported: a late count, a rework pass, a finish keyed by mistake.
`If-Match` carries the job version the supervisor read (SF-0143, 412, the version moves with every
report), `Idempotency-Key` is required (a repeat returns the stored job and publishes nothing), and the
caller must be an authenticated user, who is recorded. A job that is not finished is SF-0155 (422). The
job keeps its quantities and check results, so a later finish passes the mandatory-check gate again
(SF-0005, and the override with its own duty). The reopening is a row of `job_reopening` (migration
`0014_job_reopenings.sql`, tenant scoped, forced RLS, append only for `passage_app`) with who, why, when,
the finish it undid and the totals at that moment, and `dev.passage.shopfloor.job.reopened.v1` is written
through the outbox in the same transaction, so production (w21) and quality (w33) can reopen what they
closed on the finish. `GET jobs/{id}/reopenings` (duty `shopfloor.view`) lists them newest first.
Counted in `shopfloor_jobs_reopened_total`. On the web the job page of a finished job shows a *Reopen
this job* form instead of the report form to a holder of the duty (others see the finished notice), and
the job page lists its reopenings to a holder of `shopfloor.view`. A reopening reverses nothing: time
registrations stay, and later `job.reported` postings carry only what they add (`qtyGood`, `qtyScrap`),
so consumers that post those deltas report nothing twice on the second finish.

## Slice 14: edge fleet and versioned connector configuration (FR-25.19)

An edge node is one iot-edge connector. `POST /v1/shopfloor/edge-nodes` registers it (platform k3s,
windows or linux; ring `all` by default, the PRD parameter default; name unique, SF-0153), `PUT` replaces
name, site, ring (promotion), connector identity and the active flag with `If-Match`. Configuration
documents are versioned per node: `POST edge-nodes/{id}/config-versions` validates the document
(machines with a UUID resource, tags that point at a listed machine with a known kind, a write allowlist
with finite min <= max; other keys pass through), writes the server's version number into it and stores
it. A published version is immutable: `passage_app` may update only `applied_at`, and a trigger refuses
any other change, a second `applied_at` stamp and deletes, even for the table owner.

Fleet writes (register, update, publish) need `shopfloor.edge.configure`, not the machine setup duty,
because the write allowlist decides which machine nodes a connector may write. The publish event
`edgeconfig.published` is the audit record of a publish: who, the SHA-256 of the stored document
(`documentSha256`, also on every configuration read) and the allowlist nodes.

**Connector identity.** Each node is bound to the identity its connector authenticates as
(`connectorSubject`, the subject the gateway forwards in `X-Passage-User`; unique per tenant). The
connector holds `shopfloor.edge.connect` and pulls `GET edge-nodes/{id}/config` and reports every 30 s
with `POST edge-nodes/{id}:heartbeat`; both refuse any caller other than the bound identity, a signed
service token and an unbound node with SF-0154 (403) before anything is read or written. The machine-event
ingest duty does not grant either. Until token enrollment exists (see NOT-IMPLEMENTED D9) the binding is set
by hand on the node and the gateway's authentication of the connector's credential is what makes the
subject trustworthy; nothing yet ties it to the mTLS certificate the connector reports.

The heartbeat carries connector version, buffer fill, per-machine states, certificate serial and expiry,
and the version the connector runs. A reported version other than the applied one becomes the applied one
(one at a time), stamps `applied_at` on that version the first time and publishes `edgeconfig.applied`; a
version never published to the node is SF-0151 (422). Status is derived when read: `pending` (never
reported), `online` (heartbeat within 90 s), `offline`, `inactive`; the list filters by ring and status. A
heartbeat updates the node but not its version and publishes no event. Events: `edgenode.saved`,
`edgeconfig.published`, `edgeconfig.applied`. Web: edge fleet list with ring and status filters, register
form, and a node page with the heartbeat, machine states, the ring, identity and status form,
configuration versions (with their documents) and a publish form; the write actions show only to holders
of `shopfloor.edge.configure` (migration 0013).

## Slice 13: absence codes and absence pay lines (FR-25.2, pay types `absence-paid` and `absence-unpaid`)

An absence code (migration `0012_absence.sql`, table `absence_code`; `POST/GET/PUT /absence-codes`,
`If-Match` on the update, duty `shopfloor.setup.maintain` to write and `shopfloor.view` to read) says
whether absence time is paid and at what share of the worker's rate (`payMultiplier`, above 0 up to 5
with up to 4 decimals, default 1). An unpaid code has no rate effect: its multiplier is 0 (sending
another value is a validation error). Codes are tenant-wide, like time profiles and pay agreements.
A registration of kind `absence` names an active code (`absenceCodeId`, SF-0145 unknown, SF-0146
inactive) and must be closed (`stoppedAt` after `startedAt`, at most 24 hours like every
registration, so a multi-day leave is one absence per day). `pay:calculate` and `pay:revise` turn
each absence into one pay line on the local day it starts, the same rule as a worked segment that
crosses midnight (a sick night shift 22:00 to 06:00 stays on the shift's day): `absence-paid` with
hours x rate x multiplier rounded once to the currency's minor unit through `platform/money`, or
`absence-unpaid` with the hours, multiplier 0 and amount 0. Absence hours are not worked hours: they
do not count toward the daily or weekly overtime thresholds and are not in the worker-day's `hours`,
but worked plus absence hours cannot pass 24.00 in a day (SF-0007). An absence that overlaps clocked
time (also a night shift from the day before) or another absence flags its day `absence-overlap`, so
the day cannot be approved (SF-0006) until a supervisor corrects the times and recalculates. A day
with only an absence is created. An absence registered or corrected after a day was calculated makes
the day stale at approval (SF-0104), like a clock registration. The pay terms of a code (`paid`,
`payMultiplier`) are fixed once an absence names it (SF-0147, 409): a recalculated or revised past
day pays the absence as it was registered; to change the terms, create a new code and deactivate the
old one. Name and status can always change. `absencecode.saved` is published with every create and
update, `registration.posted` carries `absenceCodeId`. On the web: the absence code list, the create
form and a *Record absence* form (worker, code, day, start time, hours).

## Slice 12: shift premiums on the pay agreement (FR-25.7, pay type `shift-premium`)

A pay agreement carries up to six premium windows `{name, from, to, premium}` (migration
`0011_shift_premium.sql`, column `pay_agreement.shift_premiums`). `pay:calculate` and `pay:revise`
add one `shift-premium` pay line per window and day: its hours are the paid hours (unpaid breaks
taken out) that fall inside the window, to 0.01 h, and its multiplier is the premium, so the amount
is rate x hours x premium. The line supplements the base or overtime lines of the same hours; the
day's hours are not counted twice. A window whose end is not after its start crosses midnight (the
part of a night window before 06:00 counts for a shift that starts after midnight), and a shift that
runs past midnight earns the windows of the next morning too (a 20:00-04:00 shift has four hours in
a 00:00-06:00 window), all on the day the shift started. Window times are local wall-clock times.
Windows of an agreement must not overlap. A `PUT` replaces the agreement, so it must send the
windows it keeps. `payagreement.saved` carries `shiftPremiums`. On the web the pay
agreement list shows the windows and the create form adds and removes them.

## Slice 11: time profiles, pay agreements and the worker link (FR-25.7, BR-25.2, BR-25.8)

- `time-profiles` (`POST`, `GET`, `GET {id}`, `PUT {id}`): a profile holds one `days` entry per
  day type (`weekday`, `saturday`, `sunday`) with `shiftStart`/`shiftEnd` ("HH:MM", both or
  neither), unpaid `breaks`, `roundingMinutes` (1 to 15), `toleranceBeforeMin` and
  `toleranceAfterMin` (0 to 60) and `flexAllowed` (stored; the flex balance is not calculated).
  A day type the profile does not define uses `DefaultProfile` (10 before, 5 after, 1 minute
  rounding, no breaks).
- `pay-agreements` (same four routes): `dailyThreshold` (0 to 24 h), `weeklyThreshold` (0 to
  168 h), `overtimeMultiplier` and `sundayMultiplier` (1 to 5), `weekStart` (0 Sunday to 6).
  A worker without an agreement is calculated with `DefaultRules`.
- `POST workers/{id}:assign-setup` links a worker to a profile and an agreement (null clears a
  link). A newly chosen profile or agreement must exist (SF-0140, SF-0141) and be active
  (SF-0144); the one the worker already has may stay linked after it was deactivated, so the
  other link can still be changed.
- Creates take an `Idempotency-Key`. `PUT` and the assignment take `If-Match` with the version
  read as an entity tag (`"3"`); every read and write returns the new `ETag`, and a stale
  version is SF-0143 (412). The code of a profile or agreement never changes (SF-0142 on a
  duplicate at create).
- `pay:calculate` and `pay:revise` read the worker's setup first: the week start decides which
  earlier days the weekly threshold sees, the profile of the day a segment starts on rounds its
  punches and takes its breaks off, and the agreement sets the thresholds and multipliers.
  Deactivating a profile or agreement stops new assignments but keeps applying to workers
  already linked, so pay never changes silently. Days already calculated keep their lines until
  recalculated (an approved day through `pay:revise`).
- Events `dev.passage.shopfloor.timeprofile.saved.v1`, `payagreement.saved.v1` and
  `worker.assigned.v1` are written through the outbox in the same transaction.
- Duty: `shopfloor.setup.maintain` writes, `shopfloor.view` reads (both exist already).

## Slice 8: job start and stop from registrations

- Job-start dispatch (FR-25.3) in `POST registrations`: a new `job-start` for a job on the
  dispatch list moves a `released` job to `started` (version + 1). A worker has one open
  unit of work, one job or the jobs of one bundle: an open `job-start` (no `stoppedAt`)
  stops the worker's other open job, or every open job of another bundle, by writing a
  closed `job-stop` per job from that job's start to the new start, with its hours, the
  note "Stopped automatically..." and flag `auto-stopped`. An auto-stopped bundle's time
  runs from its earliest start and is split by the good quantity on its starts, or equally
  (BR-25.1, `domain.SplitBundleHours`). Starts sharing the incoming `bundleId` stay open,
  so a bundle starts in one batch. A start older than an open unit (a late offline replay)
  is itself closed at that unit's start, or earlier at the worker's next job-start after
  it, so it never overlaps work registered in between. A member of that unit with its own
  job-stop (same job, starting at or after it and before the next start of that job) stored
  or in the same batch is left out: the stop carries its time and the server writes none.
  When the own stop arrives in a later batch, the server's stop is corrected to zero length
  (`registration_correction` by `system:offline-replay`, `registration.corrected`), so a
  replayed start and stop are never counted twice. A unit open for more than 24 hours is stopped at
  24 hours and also flagged `auto-stop-capped`. Each auto-stop emits
  `registration.posted` (kind `job-stop`, `hours`, `flags`) in the same transaction and
  comes back in the item's `autoStopped` with warning SF-0002. The worker row is locked
  for the batch, so two batches cannot leave two open units.
- Finished-job conflict (BR-25.7): a `job-start` for a job the server already finished is
  stored with flag `job-finished-conflict` (new `flags` column, migration 0008), returned
  with warning SF-0004 and carried in `registration.posted`; the job stays finished. A job
  the dispatch list does not hold yet is accepted without dispatch effects, so an offline
  replay never loses a start. A duplicate item has no flags, auto-stops or warnings.
- Stopping work: the kiosk sends a closed `job-stop` per open job (startedAt = the job's
  start, stoppedAt = now), one batch per bundle, which the slice 3 bundle split covers.
- `sessions:badge` returns the worker's `openJobs`; stored registrations carry `flags`.
- Pure rules in `internal/domain/dispatch.go` (`PlanAutoStop`, `StopUnit.Hours`) with a
  property test that at most one unit stays open. Store tests in `store/dispatch_test.go`,
  HTTP test `TestHTTP_JobStartAutoStopAndFinishedConflict`.
- Web: on the kiosk (`/shopfloor/kiosk`), a clocked-in worker sees the current job or
  bundle with *Stop job* or *Stop bundle*, and the dispatch list with *Start job* and
  *Start as bundle*; auto-stops and finished-job starts are reported with their codes. The
  worker and bundle pages show the registration flags.

## Slice 7: dispatch list, job feedback and quality checks

- Dispatch projection (`POST jobs`, duty `shopfloor.job.schedule`, the w23 schedule's
  service identity): writes a job's plan (order and operation references, resource,
  planned quantity, sequence start and the operation's check definitions). The job id is
  the key: 201 when new, 200 when the plan changes; plan fields are last-writer-wins and
  reported quantities, status and check results are never overwritten.
- Dispatch list (`GET jobs?resourceId&status&limit`, duty `shopfloor.time.register`,
  FR-25.3): jobs in sequence-start order; finished jobs are hidden unless `status=finished`.
  Job detail (`GET jobs/{id}`) adds the latest result per check.
- Feedback (`POST jobs/{id}:feedback`, duty `shopfloor.time.register`, FR-25.4): good and
  scrap quantities are increments (numeric(18,4), at most 4 decimal places); scrap above
  zero needs a `scrapReasonId`; something must be reported (a quantity, a check value or
  `finished`). The client `feedbackId` is the idempotency key: a resend returns 200 with
  `duplicate: true` and changes nothing; the same id on another job is SF-0124. A finished
  job takes no feedback (SF-0122). Each stored posting writes
  `dev.passage.shopfloor.job.reported.v1` to the outbox in the same transaction, with the
  posting's quantities, the job's totals, the finish and the failed checks.
- Quality checks (FR-25.5, AC-25.9): a check is numeric (inclusive `min`/`max`),
  pass-fail or text. Values are evaluated against the job's definitions (unknown code
  SF-0123, a value that cannot be evaluated SF-0125); the latest value per check is kept
  in `job_check_result` and every posting's values stay on `job_feedback` for audit.
  `finished: true` is refused with SF-0005 while a mandatory check has no passing result;
  the refused posting stores nothing, so the same `feedbackId` can be resent with
  `override.reason`, which needs the duty `shopfloor.check.override` and an authenticated
  user (recorded as `recorded_by`). Counted in `shopfloor_job_feedback_stored_total`,
  `shopfloor_job_feedback_replays_total` and `shopfloor_job_finish_blocked_total`.
- Pure rules in `internal/domain/checks.go` (`ValidateCheck`, `EvaluateCheck`,
  `FinishBlockers`), with a property test that a numeric check passes exactly within its
  limits. Tables `shop_job`, `job_feedback`, `job_check_result` (migration 0007, forced RLS,
  granted to `passage_app`).
- Web: the dispatch list (`/shopfloor/jobs`, resource and status filters) and the job
  detail (`/shopfloor/jobs/:id`) with checks and their results, the report form (quantities,
  scrap reason, check values, *Save report* and *Report and finish*) and the supervisor
  override form shown when a finish is blocked.

## Slice 9: supervisor corrections and pay revision with reversal (FR-25.8, AC-25.8)

- `POST registrations/{id}:correct` (duty `shopfloor.time.correct`, `Idempotency-Key`, a reason
  of 1 to 500 characters) changes `startedAt` and/or `stoppedAt` of one registration. Rules
  in `domain.PlanCorrection`: a clock-in or clock-out takes no stop, a closed registration
  lasts at most 24 hours and its hours are recomputed, a bundled job registration is refused
  (SF-0127), a job-start already closed by a job-stop is refused (SF-0127; the job-stop
  carries the interval and hours, so it is the one to correct), a missing reason or no change is SF-0128, and the worker's own user cannot
  correct (SF-0106). The before and after are kept in `registration_correction`
  (migration 0009) and `dev.passage.shopfloor.registration.corrected.v1` is written through
  the outbox. A day calculated before a correction is stale for approval (SF-0104).
- `POST pay:revise` (duty `shopfloor.pay.revise`) recalculates a range and applies it:
  days that are not approved are replaced as in `pay:calculate`; an approved day keeps its
  lines. `domain.PlanRevision` compares the effective lines with the recalculation; lines
  that differ are cancelled by reversal lines (`reversal_of`, negative hours and amount) and
  the new lines are added, all with the day's next `revision`. The pay of a day is the sum of
  its lines. The `pay_line` trigger still refuses updates and deletes of approved lines and
  only admits inserts that carry a revision. Reversal and new lines join the next payroll
  export. Event `dev.passage.shopfloor.payday.revised.v1`. No difference in any day is
  SF-0129; an approved day that would carry open flags is SF-0006.
- `GET registration-corrections?from&to[&workerId]` lists the audit trail.
- `POST registrations:auto-clock-out[?afterHours=16&limit=100]` (duty `shopfloor.time.autoclockout`)
  is the hourly auto clock-out job (D2h). Each worker whose latest clock punch is a clock-in
  older than `afterHours` (4 to 24, else SF-0130) gets a clock-out at clock-in plus
  `afterHours`, flagged `auto-clock-out`, with `registration.posted` in the same transaction.
  Workers are locked `skip locked` (concurrent sweeps close a shift once) and the insert
  re-checks that no later clock punch exists. Repeating is safe. BR-25.3: the calculated day
  counts the hours but carries `missing-clock-out` (approval is SF-0006) until a supervisor
  confirms the clock-out (`registrations/{id}:correct` with its stored `startedAt`, allowed
  once for an unreviewed auto clock-out) or corrects its time; then recalculate and approve.
  `GET auto-clock-outs?from&to[&timeZone][&workerId]` lists the flagged clock-outs with
  `reviewed` once a supervisor confirmed or corrected them.
  Web: *Auto clock-outs* (`/shopfloor/auto-clock-outs`) with the run-now button and a
  confirm-or-correct link per row.
- Web: *Time corrections* list (`/shopfloor/corrections`), the correction form
  (`/shopfloor/registrations/:id/correct`, linked from the worker's registrations), and the
  revise form with the reversed and added lines (`/shopfloor/workers/:id/revise`).

## Slice 6: idle detection, OEE rollup, kiosk badge login, who is clocked in

- Idle detection (BR-25.6): each machine has an idle threshold (`idleThresholdSeconds` on
  `POST machines`, 30 to 3,600, default 300; migration 0006). When a RUNNING stretch has
  no count for longer than the threshold, the whole gap, from the last count (or the start
  of RUNNING) to the next count or state change, is IDLE downtime; a later count closes the
  gap. The store reads one threshold of look-back before the window so a window that starts
  mid-gap knows the last count, and the first count or state event after the window (the
  horizon) so the last gap is measured to where it really ends. With no later event the gap
  runs to now, and time after now is not scheduled, so `GET oee` for today no longer books
  the rest of the day as run time.
- OEE rollup (`POST oee:rollup`, duty `shopfloor.oee.rollup`, PRD job "OEE rollup" every
  15 minutes): computes the buckets of a window (start on a bucket boundary in UTC, whole
  buckets, at most 24 hours, not in the future) exactly as `GET oee` does, for the given
  resources or every registered machine, and upserts them into `oee_bucket` by (resource,
  bucket width, bucket start). A new or changed bucket emits
  `dev.passage.shopfloor.oeebucket.updated.v1` with its minutes, counts and ratios; a stored
  bucket that lost all planned time (its downtime later coded as planned) is removed with
  `dev.passage.shopfloor.oeebucket.removed.v1`; an unchanged bucket is not rewritten and
  emits nothing, so the job is safe to repeat and takes no `Idempotency-Key`. Counted in
  `shopfloor_oee_buckets_changed_total` and `shopfloor_oee_buckets_removed_total`.
- Kiosk badge login (`POST sessions:badge`, duty `shopfloor.kiosk.login`, FR-25.1): a
  scanned badge resolves to the active worker with name, party, sign-in user, the open
  clock-in (or null) and the idle timeout (10 minutes, the PRD default). An unknown badge and
  the badge of a deactivated worker are both SF-0001, counted in
  `shopfloor_kiosk_badge_rejected_total`; the badge is never logged.
- Who is clocked in (`GET clocked-in`, duty `shopfloor.view`, FR-25.14 and inquiry 4.25.14):
  workers whose latest clock punch is a clock-in, longest present first, with the jobs they
  started since that clock-in and have not stopped (no `stoppedAt` and no later
  `job-stop` for the job; every job of a bundle is listed).
- Migration 0006 also grants `process_message` and `process_message_delivery` to
  `passage_app`: migration 0004 created them without the grant, so the service could not
  use them under the application role.
- Web: a kiosk page (badge sign-in, clock in and out, sign-out after the idle timeout), the
  "Who is clocked in" board, and the idle threshold on machine registration and the
  machines list.

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
  BR-25.4. Events older than 7 days and events whose `(machine, seq)` is already stored
  are dropped and counted; missing seq runs above the last stored seq are returned in
  `gaps` (SF-0008, a warning) so the edge can replay them. An event at or below the last
  stored seq that is not stored yet (a gap replay, or a batch that commits after a later
  one across a broker restart) is stored and counted as accepted; it feeds counts and OEE
  but not the downtime record, which follows the newest state. Accepted events are stored
  under `(tenant, machine, seq)` with the machine row locked, so a replayed batch never
  double counts. An unknown machine code rejects the whole batch (SF-0108).
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
- Live OEE tile (`GET oee/stream?resourceId=&bucket=`, duty `shopfloor.view`, #481): a
  `text/event-stream` of `oee` events with the resource's bucket that holds now, computed as
  `GET oee` computes it, sent at once and again when it changes (polled every 2 s, AC-25.4).
  The stream ends before the 30 s request timeout with `retry: 1000`, so EventSource reconnects.
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
                       0006_idle_oee_buckets.sql (machine idle threshold, oee_bucket, process message grants)
                       0007_jobs.sql (shop_job, job_feedback, job_check_result)
                       0008_job_dispatch.sql (registration flags, open job index)
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
| POST `/v1/shopfloor/workers` | `shopfloor.setup.maintain` (Maintain shop floor setup) |
| GET `/v1/shopfloor/workers`, `/workers/{id}` | `shopfloor.view` (View shop floor) |
| POST and PUT `/time-profiles`, `/pay-agreements`, `/absence-codes`; PUT `/workers/{id}`; POST `/workers/{id}:assign-setup`, `:assign-resource`, `:deactivate` and `:reactivate` | `shopfloor.setup.maintain` |
| POST `/workers/{id}/cost-rates` | `shopfloor.setup.maintain` |
| GET `/time-profiles`, `/pay-agreements`, `/absence-codes` and their `{id}`; GET `/workers/{id}/cost-rates` | `shopfloor.view` |
| POST `/v1/shopfloor/registrations` | `shopfloor.time.register` (Register time) |
| GET `/v1/shopfloor/registrations` | `shopfloor.view` |
| POST `/v1/shopfloor/pay:calculate` | `shopfloor.pay.calculate` (Calculate time registrations) |
| POST `/v1/shopfloor/pay:approve` | `shopfloor.pay.approve` (Approve time registrations) |
| GET `/v1/shopfloor/worker-days` | `shopfloor.view` |
| POST `/v1/shopfloor/machines` | `shopfloor.setup.maintain` |
| GET `/v1/shopfloor/machines`, `/downtimes`, `/andon`, `/oee`, `/clocked-in` | `shopfloor.view` |
| POST `/v1/shopfloor/oee:rollup` | `shopfloor.oee.rollup` (scheduler service identity; not in the PRD duty table) |
| POST `/v1/shopfloor/sessions:badge` | `shopfloor.kiosk.login` (Kiosk login) |
| POST `/v1/shopfloor/machine-events` | `shopfloor.machine.ingest` (edge gateway service identity) |
| POST `/v1/shopfloor/edge-nodes`, PUT `/edge-nodes/{id}`, POST `/edge-nodes/{id}/config-versions` | `shopfloor.edge.configure` (edge fleet administrator; not in the PRD duty table) |
| GET `/v1/shopfloor/edge-nodes`, `/edge-nodes/{id}`, `/edge-nodes/{id}/config-versions` | `shopfloor.view` |
| GET `/v1/shopfloor/edge-nodes/{id}/config`, POST `/edge-nodes/{id}:heartbeat` | `shopfloor.edge.connect` (the connector's own identity, which must be the one bound to the node; not in the PRD duty table) |
| POST `/v1/shopfloor/downtimes/{id}:code` | `shopfloor.time.register` |
| POST `/v1/shopfloor/pay:export`, GET `/pay-exports`, `/pay-exports/{id}` | `shopfloor.pay.export` (Export time to payroll) |
| POST `/v1/shopfloor/registrations/{id}:correct` | `shopfloor.time.correct` (supervisor; not in the PRD duty table) |
| POST `/v1/shopfloor/pay:revise` | `shopfloor.pay.revise` (supervisor; not in the PRD duty table) |
| POST `/v1/shopfloor/registrations:auto-clock-out` | `shopfloor.time.autoclockout` (platform scheduler identity or supervisor; not in the PRD duty table) |
| GET `/v1/shopfloor/registration-corrections` | `shopfloor.view` |
| POST `/v1/shopfloor/jobs` | `shopfloor.job.schedule` (w23 schedule service identity; not in the PRD duty table) |
| POST `/v1/shopfloor/jobs/{id}:reopen` | `shopfloor.job.reopen` (shop floor supervisor; not in the PRD duty table) |
| GET `/v1/shopfloor/jobs/{id}/reopenings` | `shopfloor.view` |
| GET `/v1/shopfloor/jobs`, `/jobs/{id}`; POST `/jobs/{id}:feedback` | `shopfloor.time.register`; an `override` also needs `shopfloor.check.override` (shop floor supervisor; not in the PRD duty table) |
| POST `/v1/shopfloor/process-messages`, `/process-messages/{id}:reprocess`, `/process-messages/{id}:delete` | `shopfloor.process.create` (Create process messages: manual, reprocess/delete with reason) |
| GET `/v1/shopfloor/process-messages`, `/process-messages/{id}` | `shopfloor.view` |
| POST `/v1/shopfloor/process-categories`, PUT `/process-categories/{id}` | `shopfloor.setup.maintain` |
| GET `/v1/shopfloor/process-categories`, `/process-categories/{id}` | `shopfloor.view` |
| POST `/v1/shopfloor/process-messages/{id}/deliveries/{destination}:report`, `/process-messages:retry-due` | `shopfloor.process.deliver` (consuming context and scheduler service identities) |

Duties are checked by the platform `authz` middleware. The OPA decision point replaces the
local check in a later wave, behind the same interface.

## Error codes

Problem details use `application/problem+json` with `type` `https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| `AUTH-0001` | 401 | Missing or malformed tenant, or no authenticated user for approval, export or deletion |
| `COM-0001` | 400 | Request failed validation (or no `Idempotency-Key`); `errors` lists the fields |
| `AUTH-0003` | 403 | Missing duty |
| `SF-0001` | 422 | Badge is not recognized (unknown, or the worker is deactivated) |
| `SF-0005` | 422 | Mandatory check failed or not recorded; the job cannot be finished without a supervisor override |
| `SF-0006` | 422 | Day cannot be approved: flags are open |
| `SF-0007` | 422 | Worker-day exceeds 24.00 hours |
| `SF-0101` | 422 (404 on GET) | Worker not found or inactive |
| `SF-0102` | 422 | Approved days cannot be recalculated; a correction is required |
| `SF-0103` | 422 | No calculated days in the approval range |
| `SF-0104` | 422 | Registrations, or the dated cost rate pricing the day, changed after calculation; recalculate before approving |
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
| `SF-0121` | 404 | Job not found |
| `SF-0122` | 422 | The job is finished; it takes no more feedback |
| `SF-0123` | 422 | A check value names a check that is not defined on the job |
| `SF-0124` | 422 | The `feedbackId` was already used for another job |
| `SF-0125` | 422 | A check value cannot be evaluated (not a number, not pass or fail, or blank); `errors` names it |
| `SF-0126` | 404 | The registration to correct does not exist |
| `SF-0127` | 422 | The registration cannot be corrected (a bundled job registration, a job-start already closed by a job-stop, or a stop time on a clock-in or clock-out) |
| `SF-0128` | 422 | The correction is invalid (no reason, stop before start, over 24 hours, or nothing changes; keeping the time is allowed once, to confirm an unreviewed auto clock-out) |
| `SF-0129` | 422 | `pay:revise` found the recalculated pay equal to the stored pay in every day of the range |
| `SF-0130` | 422 | `registrations:auto-clock-out` got `afterHours` outside 4 to 24 |
| `SF-0140` | 404 or 422 | Time profile not found (422 when named by an assignment) |
| `SF-0141` | 404 or 422 | Pay agreement not found (422 when named by an assignment) |
| `SF-0142` | 409 | The profile or agreement code is already used |
| `SF-0143` | 412 | `If-Match` is not the stored version of the profile, agreement, worker or job |
| `SF-0144` | 422 | The profile or agreement newly chosen by an assignment is inactive |
| `SF-0145` | 404 or 422 | Absence code not found (422 when named by an absence registration) |
| `SF-0146` | 422 | The absence code named by a new absence registration is inactive |
| `SF-0147` | 409 | The paid flag or pay multiplier of an absence code that an absence already names cannot change |
| `SF-0150` | 404 | Edge node not found |
| `SF-0151` | 404 or 422 | No configuration published for the node (404 on a pull); a heartbeat reported a version never published to it (422) |
| `SF-0152` | 409 | The edge node is switched off: it refuses heartbeats and configuration pulls |
| `SF-0153` | 409 | An edge node with this name, or bound to this connector identity, already exists |
| `SF-0154` | 403 | The caller is not the connector bound to this edge node (or the node has none): configuration pull and heartbeat refused |
| `SF-0155` | 422 | The job to reopen is not finished |
| `SF-0156` | 422 | The home resource has no registered machine and no dispatch jobs on this tenant's shop floor |
| `SF-0157` | 422 | An online job-start on a job whose operation production finished (at or after the finish); reopen the operation in production. Offline replays are accepted and flagged instead |
| `SF-0158` | 404 or 422 | The downtime reason does not exist (404 on a read or change; 422 when coding a downtime with an unknown code) |
| `SF-0159` | 422 | The downtime reason is inactive |
| `SF-0160` | 422 | The category sent when coding a downtime is not the reason's category |
| `SF-0161` | 422 | Quantity feedback on a job whose operation production finished; reopen the operation in production |
| `SF-0162` | 404 | No open andon alert has that id (unknown, or already cleared) |
| `SF-0163` | 422 | Only a quality-fail alert is cleared by hand; a downtime or scrap-rate alert clears itself after 5 minutes |
| `SF-0164` | 404 or 422 | The process category does not exist (404 on a read or change; 422 when a message names a code that is neither a built-in default nor a master row) |
| `SF-0165` | 422 | The process category is inactive; its messages are refused |
| `SF-0166` | 409 | The worker is clocked in or has an open job start and cannot be deactivated |
| `SF-0167` | 409 | The worker is already inactive |
| `SF-0168` | 409 | The worker is already active |
| `SF-0169` | 422 | The worker's currency cannot change once a day of the worker is approved |
| `SF-0170` | 409 | The worker already has a cost rate effective on that date; set the change from a later date |
| `idempotency_key_reuse` | 422 | `Idempotency-Key` already used with a different request |

Warnings on an accepted registration (`items[].warnings`, the request succeeds):

| Code | Meaning |
|---|---|
| `SF-0002` | Previous job was stopped automatically (FR-25.3); one per auto-stopped job |
| `SF-0004` | The job was finished on the server while the kiosk was offline (BR-25.7, flag `job-finished-conflict`) |

Catalog entries SF-0001..SF-0020 (PRD 4.25.18) are reserved; only the codes above are raised.
SF-0008 (sequence gap) is reported in the ingest response `gaps` and counted in
`shopfloor_machine_seq_gaps_total`, not raised as an error. SF-0016 (delivery failed) is a
logged warning on a failed delivery report, not an error response.

## Dependencies

Only the platform module and what it already pulls in (chi, pgx/v5, shopspring/decimal,
google/uuid, rapid for tests). No new dependencies in this slice.
