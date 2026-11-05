# mobile-flows (wave 32, PSG-32)

Flow designer for warehouse handheld flows, plus the pure domain shared with
`apps/warehouse-mobile`.

## What is here

- `domain/gs1.ts`: GS1 application identifier parser (BR-32.1). Covers AI 00, 01, 10, 11,
  17, 21, 37, 400, 410, 412, 420 (ship-to postal code, on the print service's carton label),
  310n and 91 to 99.
  Check digits (AI 00, 01, 410, 412), YYMMDD dates (DD=00 is last day of month, century window -49/+50), set 82
  charset, FNC1 substitutes, and the GS1 symbology identifiers `]C1`, `]d2`, `]Q3`. Any other
  symbology identifier (`]C0`, `]E0`, ...) is stripped and the rest is a plain value.
  An AI repeated with a different element string, and a barcode with no element string, are
  MOB-0103 (GS1 General Specifications 4.14).
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
- `domain/coreFlows.ts`: the seven bundled core flows (FR-32.4: receive/put-away, pick with a
  short-pick reason, cluster pick, pack, move, count, inquiry), the `ConfirmWorkLine` payload,
  and `completionFor`, which queues work flows, never queues an inquiry and keeps a count
  online only (§6.32.13 `offline_enabled`).
- `domain/shellCache.ts`: the device app-shell cache policy that
  `apps/warehouse-mobile/public/sw.js` applies (FR-32.5 offline start); its test runs `sw.js`.
- `domain/telemetry.ts`: step timing (FR-32.11): validation, nearest-rank median and p95
  rollups by flow and step (by user only on request, §6.32.20), and the 30 to 365 day
  retention (90 by default).
- `domain/otlp.ts`: step timings as OpenTelemetry `mobile.step` spans (FR-32.11), from the
  step's start to the moment the runner recorded it, with flow, step, device and outcome and
  never the user id (§6.32.20); timings past the retention window are dropped. `exportSpans`
  posts OTLP/HTTP JSON to `{endpoint}/v1/traces`. The device app does not call it yet.
- `domain/deviceProfiles.ts`: the `zebra-tc52` and `honeywell-ct45` device profiles (§6.32.9
  fixtures) with their `scanner_profile` and capture bridge, and the Android WebView 100 minimum.
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
  (both mobile service endpoints, §6.32.5), `GET /api/v1/mobile/menus/{id}` and SSE
  `GET /api/v1/mobile/events` for `flow.published`, and the supervisor conflict calls
  `GET /api/v1/mobile/offline/conflicts` and `POST .../offline/conflicts/{key}:resolve`.
  `vite serve` stands in for the flow/menu/SSE paths (`api/devStub.ts`); a missing service
  still reports that it is unavailable.
- `DeviceApp.tsx`: the handheld shell shared with `apps/warehouse-mobile`. It loads the
  published menu (`useDeviceFlows.ts`) and keeps an in-flight flow on the version it started
  (BR-32.9). `/device` hosts it in dev builds only, for Playwright Pixel 5 (AC-32.2, AC-32.4); the in-process mobile stub runs only with PASSAGE_MOBILE_STUB=1.
- `api/print.ts`: client for `services/print` through the BFF at `/api/v1/print`.
- `pages.tsx`: designer screen. It opens on the bundled receive/put-away flow (any core flow can
  be loaded with "Start from"), edits the flow
  as JSON, validates, saves a draft, publishes, and runs a virtual scanner that uses the GS1 parser.
- `FlowSimulator.tsx`: on the designer screen, runs the edited flow step by step with the
  device runner (§6.32.6 "designer simulation"): a virtual scanner feeds every step (scans on
  non-scan steps are listed as ignored), values and branch outcomes are entered on their steps,
  and a step timing table shows attempts, errors, median and p95.
- Print management (FR-32.7, FR-32.13): `PrintersPage` and `NewPrinterPage`,
  `RoutingRulesPage` and `NewRoutingRulePage`, `PrintJobsPage` (job log with status and queue
  filters), `NewPrintJobPage` (print a built-in label on a queue or by routing rule),
  `NewPrintDocumentPage` (send a PDF of up to 2 MB to the PDF over IPP printers of a queue or
  routing rule; with no matching rule it offers the browser's print dialog, BR-32.12) and
  `PrintJobDetailPage` (outcome, MOB-0111/0112 and PRN-0007 explanation, "Print again" with `reprintOf`).
  The label form covers the LP, location, GS1 item (GTIN, optional lot, expiry and serial) and
  GS1 carton labels, with the service's check-digit, YYMMDD and set-82 rules; empty optional
  fields are left out of the request. Printers carry a media size that follows the device type
  (label sizes for ZPL II, A4 or Letter otherwise); the list and the form say what each media
  prints (2 x 1 and 3 x 1 in print the 1 in LP, location and item layouts, not the carton label). Printers may leave the port empty: the service uses
  9100 for ZPL II and PCL 6 and 631 for PDF over IPP.
- `PrinterStatusPage` (§6.32.14): state, queue depth, last error and last job per printer.
- `OfflineConflictsPage` (BR-32.4, AC-32.6): the supervisor queue of CONFLICT transactions.
  Opening a row shows the transaction and a resolution choice ("Apply as movement" or
  "Reject") that needs an explicit confirm; the call sends `If-Match` with the conflict version
  and one Idempotency-Key per conflict and choice.
- Device screens rendered by `apps/warehouse-mobile` (they use its `app.css` classes, not the
  shell design system, and are not shell routes): `ExceptionsPanel` (FR-32.8, BR-32.7: short
  pick, skip and damage logged with user, device, location and time; a supervisor PIN override
  that authorizes exactly the exception on screen, once, within 60 s, MOB-0110, never for the
  supervisor's own work; the PIN check is a `verifyPin` callback the mobile service will back)
  and `ClusterPanel` (BR-32.5: a cart of up to 8 works, one order's LP per position, MOB-0109 on
  a wrong position, complete or short only after the right position is scanned).
- `module.ts`: registered as `mobile-flows`. The designer is gated by `warehouse.mobile-flow.admin`, the
  print pages by `print.job.submit`, and the conflict queue by `warehouse.offline-conflict.resolve`.

## Run and test

```sh
cd apps/web
npx vitest --run src/modules/mobile-flows
npx tsc --noEmit
```

The GS1 AI vectors and the queue property tests are deterministic (seeded generator), so a
failure reproduces exactly.

`domain/testdata/gs1-ai-vectors.json` (AC-32.1, §6.32.9) holds 459 vectors from the GS1
Barcode Syntax Engine unit tests (commit pinned in the file) that fall within the BR-32.1 AI
table, plus 60 Passage edge vectors; `gs1.vectors.test.ts` parses each without a symbology
identifier and with `]C1`, `]d2` and `]Q3`. Regenerate it with
`python3 -I domain/testdata/gen_gs1_ai_vectors.py > domain/testdata/gs1-ai-vectors.json`: the
script has its own reference parser, written from BR-32.1 and the GS1 General Specifications,
and stops if that parser disagrees with GS1's verdict on any suite vector. Suite tests left out
are listed in the file with the reason (AIs outside the table, AI association rules, encoder
limits, bracketed-input syntax).

## Notes

- The PRD's BR-32.1 example GTIN `09501234567893` has a wrong mod-10 check digit (the correct
  digit is 1). The tests use `09501234567891`. The PRD should be corrected.
- The parser is the reference for the server-side `scan:parse` endpoint, which is a handoff.

## Needs

Nothing outside `apps/web` is needed. The module uses `react-router-dom` and `@testing-library`
already present in `apps/web`. No npm dependencies were added.

## Not yet (see also `apps/warehouse-mobile/NOT-IMPLEMENTED.md` and `services/print/NOT-IMPLEMENTED.md`)

- Publish is wired; smoke scripts that replay every core path before publish (BR-32.9) are
  not built.
- Flow list, version history, diff, and the four-eyes approval UI are not built (§6.32.14).
- GS1 AIs 710 to 715, and every other AI outside BR-32.1, are not in the parser table yet.
  Unknown AIs report MOB-0103 with their position, so suite vectors for those AIs are excluded
  from `gs1-ai-vectors.json`, not passed.
- AI association rules (GS1 General Specifications 4.13 mandatory pairs and 4.14 invalid pairs,
  for example AI 21 without AI 01) are not checked: the device parses each label on its own.
