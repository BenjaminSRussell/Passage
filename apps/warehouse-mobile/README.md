# warehouse-mobile

Handheld warehouse app (wave 32, PSG-32). A Vite + React PWA for Zebra and Honeywell
scanners and any PWA-capable browser. The flow runtime runs the same step graphs the
designer publishes (`apps/web/src/modules/mobile-flows`), so flows change without an app
release.

## Current slice

- `src/App.tsx`: one scan step. Scans are classified and checked against the expected
  class (MOB-0101, BR-32.2). An accepted scan is queued locally as a `ConfirmWorkLine`.
  Sync replays the queue in `seq` order (BR-32.3).
- Shared domain, imported from the web module rather than copied (one source of truth,
  tested by the web toolchain):
  - `apps/web/src/modules/mobile-flows/domain/gs1.ts`: GS1 AI parser (BR-32.1).
  - `apps/web/src/modules/mobile-flows/domain/offlineQueue.ts`: offline queue, cap and
    age limits, conflict blocking, exactly-once replay.

## Run

```sh
pnpm install            # not yet run for this app; no lockfile is committed in this slice
pnpm dev                # http://localhost:5173, API proxied by the BFF in deployment
pnpm test               # vitest over this app (no tests yet; shared domain tests run in apps/web)
pnpm build
```

The device id is read from `localStorage["passage.deviceId"]`. Provisioning sets it.

## Scanner input

Keyboard wedge: the scan input is focused and Enter submits. DataWedge and Honeywell
intent bridges belong to the Android shell (not in this slice).

## Notes

- The GS1 parser runs on the device and in the designer. The server must apply the same
  rules on `POST /mobile/v1/scan:parse` (handoff; see NOT-IMPLEMENTED.md).
- Pure domain tests live under `apps/web/src/modules/mobile-flows/domain` and run with
  `npx vitest --run` in `apps/web`.
