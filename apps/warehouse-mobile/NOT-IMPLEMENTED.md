# NOT-IMPLEMENTED: warehouse-mobile (PSG-32, wave 32, slice 1)

| Item | Source | Status | Reason |
| --- | --- | --- | --- |
| Android native shell (WebView, Zebra DataWedge and Honeywell Data Collection intents, signed APK, MDM distribution) | FR-32.1, FR-32.10, §6.32.10 | later slice of PSG-32 | Slice 1 is the PWA only. The shell needs an Android project and device profiles (`zebra-tc52`, `honeywell-ct45`). Scans come from the keyboard wedge. |
| Focused-step scan acceptance and DataWedge `]C1` ignore on non-scan steps (AC-32.7) | FR-32.10 | later slice of PSG-32 | Needs the shell intent bridge. The keyboard-wedge input is accepted only on the scan step. |
| Keycloak device-flow login, PIN re-auth after 8 h | FR-32.1, §6.32.13 `pin_reauth_hours` | handoff | Client registration belongs to edge-identity (wave 2). The app has no auth yet. |
| Full flow runner for the seven core flows (receive, put-away, pick, cluster pick, pack, move, count, inquiry) | FR-32.4 | later slice of PSG-32 | Slice 1 has one scan step. Flow definitions are loaded from the server, which does not exist yet (handoff). |
| Menu load, flow version fetch and SSE push of publishes (≤ 60 s) | FR-32.2, AC-32.4 | handoff | Needs the mobile service endpoints `GET /mobile/v1/menus/{id}` and the SSE channel. |
| Offline persistence in IndexedDB, encrypted with a device key; service worker for asset caching | FR-32.5 | later slice of PSG-32 | The queue logic is built and tested in the web module. Persistence and the encryption key are not. |
| Conflict surfacing UI and supervisor resolution (BR-32.4, AC-32.6) | FR-32.5, §6.32.6 | later slice of PSG-32 | The queue records CONFLICT and blocks later entries. The supervisor screen and `resolve` wiring are not built. |
| Replay against the server (`POST /mobile/v1/offline:replay`) and the server-side ordering and idempotency | FR-32.5, BR-32.3 | handoff | The mobile service is outside the owned paths. The client sends `Idempotency-Key = device_id:seq`. |
| Server GS1 parse endpoint `POST /mobile/v1/scan:parse` | §6.32.5 | handoff | The mobile service owns it. The parser is in `apps/web/src/modules/mobile-flows/domain/gs1.ts`. |
| Cluster picking (BR-32.5), short pick, skip, damage, PIN override (FR-32.8, BR-32.7) | FR-32.4, FR-32.8 | later slice of PSG-32 | Flow runner work. |
| Voice adapter (`VoiceAdapter`) and EPC/RFID ingest | FR-32.9 | handoff | RFID ingest `POST /rfid/v1/reads` and the voice vendor are outside the owned paths. |
| Per-step telemetry and OpenTelemetry spans (FR-32.11) | FR-32.11 | later slice of PSG-32 | Telemetry instrumentation is not added to the app yet. |
| Automation (AS/RS, conveyor) telegram handling (FR-32.12) | FR-32.12 | handoff | Owned by the wms automation service and the wave-25 edge gateway. |
| Label printing trigger on receive (AC-32.5) | FR-32.7, AC-32.5 | handoff | Print jobs are created through `services/print`, but the flow runner that calls them is not built yet. |
| Playwright E2E on an emulated Android device (AC1, AC-32.2) and the device-profile fixtures | Test plan §6.32.9 | later slice of PSG-32 | No Playwright harness or emulator config in this slice. |
| Right-to-left layout, 320 px layout checks, font size checks (§6.32.6, §6.32.20) | §6.32.6, §6.32.20 | later slice of PSG-32 | Not measured. Layout is minimal inline style. |
| i18n bundles (wave-5 i18n) | §6.32.20 | handoff | Owned by the shell (wave 5). |
| Unit tests for the app itself | Test plan | later slice of PSG-32 | Shared domain tests run in `apps/web` (verified). The app has no test files yet. |
| Install and lockfile for this app (`pnpm install`) | Conventions, web | later slice of PSG-32 | Not run in this slice. `package.json` pins the same versions as `apps/web`. The app also needs `pnpm` workspace support, which the root owns. |
| Publish the app to the demo warehouse devices; PWA assets on the shell CDN path; signed APK via MDM | Ticket deploy line, §6.32.10 | handoff | Lives in `deploy/` and the CI pipeline, owned by deployment-cicd. |
| Browser-side type-check and build of this app in CI (no `run_check` kind covers `apps/warehouse-mobile`) | DoD 2 | handoff | The shared domain it imports is verified by the `apps/web` checks. The app's `tsc --noEmit` passed in the slice-1 review using the `apps/web` toolchain, but CI does not run it and `vite build` has not been run. |
| Device management (register devices, active sessions, force sign-out, bind to warehouses and menus; heartbeat, STALE sweep) | FR-32.6, BR-32.6, AC-32.8, §6.32.16 | handoff | Device sessions belong to the mobile service (`/mobile/v1/sessions`), which is outside the owned paths. No web screen is built for it. |
| Flow designer: flow list, version history and diff, publish with simulation smoke scripts, four-eyes approval | FR-32.2, BR-32.9, §6.32.13 `flow_publish_four_eyes`, §6.32.14 | later slice of PSG-32 | The web designer validates a graph, saves a draft and simulates single scans. Publish needs the mobile service (handoff). |
| Offline conflict queue screen for supervisors and the daily conflict report | §6.32.6, §6.32.14, §6.32.15 | later slice of PSG-32 | The queue domain supports `resolve(seq, "reapply" | "reject")`; no screen calls it. |
| GS1 AI test suite fixture `gs1-ai-vectors.json` (GS1 suite plus 60 Passage vectors), AI 420 and 710 to 715 | AC-32.1, §6.32.9, §6.32.20 | later slice of PSG-32 | The parser vectors are inline in `gs1.test.ts`; the external suite file is not imported. AI 00, 01, 10, 11, 17, 21, 37, 310n, 400, 410, 412 and 91 to 99 are supported. |
