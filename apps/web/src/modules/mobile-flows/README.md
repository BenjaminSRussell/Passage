# mobile-flows (wave 32, PSG-32)

Flow designer for warehouse handheld flows, plus the pure domain shared with
`apps/warehouse-mobile`.

## What is here

- `domain/gs1.ts`: GS1 application identifier parser (BR-32.1). Covers AI 00, 01, 10, 11,
  17, 21, 37, 400, 410, 412, 310n and 91 to 99.
  Check digits, YYMMDD dates (DD=00 is last day of month, century window -49/+50), set 82
  charset, FNC1 substitutes, and the GS1 symbology identifiers `]C1`, `]d2`, `]Q3`. Any other
  symbology identifier (`]C0`, `]E0`, ...) is stripped and the rest is a plain value.
  Scan classes (LP, GTIN, LOCATION, ANY), BR-32.2 routing (`matchScanClass`) and `checkScan`,
  which reports a label's parse error (MOB-0102..0104) before a class mismatch (MOB-0101).
- `domain/flowGraph.ts`: flow graph validation for publish (FR-32.2, BR-32.9). Rejects
  duplicate or badly formed ids, missing targets, unreachable steps, scan steps without a
  class, branches with one outcome, and steps that cannot reach an end.
- `domain/offlineQueue.ts`: offline confirmation queue (FR-32.5, BR-32.3, BR-32.4). Seq is
  monotonic with no gaps. Idempotency key is `device:seq`. Caps are 500 transactions and 24 h
  (MOB-0106). Replay stops at the first failure, and an unresolved CONFLICT blocks every later
  transaction until a supervisor resolves it. Applied transactions are never resent.
  `restore(txs, nextSeq)` reloads a persisted queue and refuses gaps, foreign keys and a
  `nextSeq` that would reissue a used key.
- `domain/offlineStore.ts`: encrypted persistence for the queue (FR-32.5). AES-256-GCM under a
  non-extractable device key kept in IndexedDB; the whole queue, with its next seq, is one sealed
  document. `saveQueue` and `loadQueue` refuse another device's or a newer-format document.
  The device app does not call it yet (see `apps/warehouse-mobile/NOT-IMPLEMENTED.md`).
- `domain/scanRouting.ts`: scan acceptance (FR-32.10, BR-32.2, AC-32.7). A scan counts only
  on the focused scan step; on any other step it is ignored for the caller to log. A step with
  several expected classes accepts the first one the scan satisfies.
- `domain/flowRunner.ts`: the runner the device and the designer simulator share (FR-32.4).
  `startFlow` refuses a graph that fails validation; `submitStep` takes a scan, a typed value,
  a branch outcome or a confirmation and returns the next state and a `StepTiming`; `goBack`
  returns one step. GS1 fields of scanned labels are kept per step.
- `domain/coreFlows.ts`: the bundled receive/put-away flow and the `ConfirmWorkLine` payload.
- `domain/telemetry.ts`: step timing (FR-32.11): validation, nearest-rank median and p95
  rollups by flow and step (by user only on request, §6.32.20), and the 30 to 365 day
  retention (90 by default).
- `domain/exceptions.ts`: on-device exceptions (FR-32.8) and the supervisor override
  (BR-32.7): one action, one worker, 60 s, never for the supervisor's own work (MOB-0110).
- `domain/scanBridge.ts`: the page side of the scanner intent bridge (`passage:scan` events
  from the Android shell, DataWedge and Honeywell extras, keyboard wedge).
- `domain/layout.ts`: device layout rules (320 px, 16 px font floor, 48 and 64 px targets,
  RTL locales), applied by `apps/warehouse-mobile`.
- `domain/cluster.ts`: cluster picking (BR-32.5). At most 8 works, one order's LP per cart
  position, MOB-0109 on a wrong position, and a short pick that leaves the other positions open.
- `domain/session.ts`: session lock (BR-32.6, AC-32.8). A session is STALE after the idle
  timeout (30 min by default) and no longer holds its work; OPEN work, or ASSIGNED work whose
  holder went stale (BR-31.3), is assignable; resuming re-locks only if nobody else took it.
- `api/client.ts`: calls to `POST /api/v1/mobile/flows` and `POST .../flows/{id}:publish`
  (both mobile service endpoints, §6.32.5), and the supervisor conflict calls
  `GET /api/v1/mobile/offline/conflicts` and `POST .../offline/conflicts/{key}:resolve`. The
  mobile service is a handoff, so these pages report that it is unavailable.
- `api/print.ts`: client for `services/print` through the BFF at `/api/v1/print`.
- `pages.tsx`: designer screen. It opens on the bundled receive/put-away flow, edits the flow
  as JSON, validates, saves a draft, and runs a virtual scanner that uses the GS1 parser.
- `FlowSimulator.tsx`: on the designer screen, runs the edited flow step by step with the
  device runner (§6.32.6 "designer simulation"): a virtual scanner feeds every step (scans on
  non-scan steps are listed as ignored), values and branch outcomes are entered on their steps,
  and a step timing table shows attempts, errors, median and p95.
- Print management (FR-32.7, FR-32.13): `PrintersPage` and `NewPrinterPage`,
  `RoutingRulesPage` and `NewRoutingRulePage`, `PrintJobsPage` (job log with status and queue
  filters), `NewPrintJobPage` (print a built-in label on a queue or by routing rule) and
  `PrintJobDetailPage` (outcome, MOB-0111/0112 explanation, "Print again" with `reprintOf`).
  The label form covers the LP, location, GS1 item (GTIN, optional lot, expiry and serial) and
  GS1 carton labels, with the service's check-digit, YYMMDD and set-82 rules; empty optional
  fields are left out of the request. Printers may leave the port empty: the service uses
  9100 for ZPL II and PCL 6 and 631 for PDF over IPP.
- `PrinterStatusPage` (§6.32.14): state, queue depth, last error and last job per printer.
- `OfflineConflictsPage` (BR-32.4, AC-32.6): the supervisor queue of CONFLICT transactions.
  Opening a row shows the transaction and a resolution choice ("Apply as movement" or
  "Reject") that needs an explicit confirm; the call sends `If-Match` with the conflict version
  and one Idempotency-Key per conflict and choice.
- `module.ts`: registered as `mobile-flows`. The designer is gated by `WMS_MOBILE_ADMIN`, the
  print pages by `WMS_PRINT`, and the conflict queue by `WMS_WORK_SUPERVISE`.

## Run and test

```sh
cd apps/web
npx vitest --run src/modules/mobile-flows
npx tsc --noEmit
```

The GS1 AI vectors and the queue property tests are deterministic (seeded generator), so a
failure reproduces exactly.

## Notes

- The PRD's BR-32.1 example GTIN `09501234567893` has a wrong mod-10 check digit (the correct
  digit is 1). The tests use `09501234567891`. The PRD should be corrected.
- The parser is the reference for the server-side `scan:parse` endpoint, which is a handoff.

## Needs

Nothing outside `apps/web` is needed. The module uses `react-router-dom` and `@testing-library`
already present in `apps/web`. No npm dependencies were added.

## Not yet (see also `apps/warehouse-mobile/NOT-IMPLEMENTED.md` and `services/print/NOT-IMPLEMENTED.md`)

- The simulator runs a flow by hand; smoke scripts that replay every core path before publish
  (BR-32.9) are not built.
- Flow list, version history, diff, and the four-eyes approval UI are not built (§6.32.14).
- GS1 AIs 420 and 710 to 715 are not in the parser table yet.
  Unknown AIs report MOB-0103 with their position. The GS1 AI test suite vectors (the external
  suite file `gs1-ai-vectors.json`) are not imported yet; the vectors are inline in the tests.
