# warehouse-mobile

Handheld warehouse app (wave 32, PSG-32). A Vite + React PWA for Zebra and Honeywell
scanners and any PWA-capable browser. The flow runtime runs the same step graphs the
designer publishes (`apps/web/src/modules/mobile-flows`), so flows change without an app
release.

## What is here

- `src/App.tsx`: re-exports `DeviceApp` from the web module. Its menu loads published flows
  (`GET /api/v1/mobile/menus/{id}`) and falls back to the seven bundled core flows
  (`coreFlows.ts`: receive/put-away, pick, cluster pick, pack, move, count, inquiry); Menu
  leaves the current flow. A publish arrives over SSE (and a 30 s poll) without an app
  update. After a confirm the next run of the same code uses the latest published graph;
  an in-flight run keeps the graph it started (BR-32.9). The shared runner decides every step:
  one input per screen, Enter or a hardware scan advances, Back returns one step (§6.32.6).
  Scan steps check the expected class and the GS1 label (MOB-0101..0104, BR-32.1, BR-32.2);
  input, select and branch steps take a value or an outcome. A finished flow is queued as a
  `ConfirmWorkLine` with the captured values and GS1 fields; an inquiry queues nothing, and a
  count is refused offline and stays on its last step until the device is connected. The queue is sealed with
  AES-256-GCM under the non-extractable device key in IndexedDB after every enqueue and
  sync (FR-32.5), and reloaded on start. Sync replays it in `seq` order (BR-32.3) and prunes
  applied transactions. The header shows the queue against its 500-transaction cap.
- `public/sw.js`: the app-shell service worker (FR-32.5), registered by `main.tsx` in
  production builds. It caches the shell, its build assets and the manifest at install so the
  app starts offline, and never caches writes or `/api/`, `/mobile/`, `/print/`, `/rfid/` and
  `/wms/` requests. The policy and its tests are `shellCache.ts` in the web module.
- Scanner intents (FR-32.10): the native shell forwards each decoded scan as a
  `passage:scan` event (`scanBridge.ts`). A scan that arrives while a confirm, display or
  other non-scan step has focus is ignored and logged on the device (AC-32.7).
- Step telemetry (FR-32.11): every evaluated step records its duration and outcome, kept
  for 90 days (`telemetry.ts`). It stays on the device until the KPI ingest exists.
- Layout (`src/app.css`, `layout.ts`): 320 px and up, text never below 16 px, 48 px targets
  (64 px in large-target mode, `localStorage["passage.largeTargets"] = "true"`), and
  right-to-left direction for Arabic, Hebrew, Persian and Urdu locales.
- Shared domain, imported from the web module rather than copied (one source of truth,
  tested by the web toolchain), all under `apps/web/src/modules/mobile-flows/domain`:
  `gs1.ts`, `scanRouting.ts`, `flowGraph.ts`, `flowRunner.ts`, `coreFlows.ts`,
  `offlineQueue.ts`, `offlineStore.ts`, `scanBridge.ts`, `telemetry.ts`, `layout.ts`, and
  `exceptions.ts` (short pick, skip, damage and the one-use 60 s supervisor override,
  FR-32.8 and BR-32.7; no device screen uses it yet).

## Run

```sh
pnpm install            # not yet run for this app; no lockfile is committed in this slice
pnpm dev                # http://localhost:5173, API proxied by the BFF in deployment
pnpm test               # vitest over this app (no tests yet; the shared domain is tested in apps/web)
pnpm build
```

The device id and user id are read from `localStorage["passage.deviceId"]` and
`localStorage["passage.userId"]`. Provisioning sets them until device sign-in exists.

## Scanner input

Keyboard wedge: the scan input is focused on scan steps and Enter submits. DataWedge and
Honeywell scans arrive from the Android shell as `passage:scan` events whose detail is
`{ source: "DATAWEDGE" | "HONEYWELL_DC", extras }`; the data extra is
`com.symbol.datawedge.data_string` for DataWedge and `data` for Honeywell, with the AIM
symbology prefix (for example `]C1`) left in place. The extra names must be confirmed
against the provisioned device profiles when the shell is built.

## Notes

- The GS1 parser runs on the device and in the designer. The server must apply the same
  rules on `POST /mobile/v1/scan:parse` (handoff; see NOT-IMPLEMENTED.md).
- Pure domain tests live under `apps/web/src/modules/mobile-flows/domain` and run with
  `npx vitest --run` in `apps/web`.
