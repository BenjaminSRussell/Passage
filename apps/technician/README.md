# technician (wave 45 PWA)

The field technician's app (FR-45.8, issue #45). A Vite + React PWA, built like `apps/warehouse-mobile`:
the app is a thin host and the code lives in `apps/web/src/modules/technician`, where its tests run with
the web checks (`npx vitest run src/modules/technician`). The web app also serves it at `/technician`
(development and the Pages demo, with fake data from `apps/web/src/demo/handlers/technician.ts`).

```
npm install
npm run dev        # http://localhost:5173, API calls need the web BFF in front (see apps/web)
npm run build      # tsc --noEmit && vite build
```

## What it does

- Today's route (`GET /v1/service/technicians/me/day`): each visit with the customer's name, address, time,
  status and the case description; a Navigate link to the maps app; the day is kept on the device.
- Start travel, start work, hold, resume and complete, with what blocks completion in plain words (checklist,
  parts, signature).
- Checklist, parts used (with what the truck holds, from inventory's truck stock levels), photos (taken or
  chosen, scaled to 1,600 px on the long edge, at most 2 MB), a note, the customer's signature drawn on screen
  with the device position, and a summary page for the customer.
- Offline: everything the technician does is applied to the cached day at once and queued. The queue is
  sealed with AES-GCM under a non-extractable device key in IndexedDB (images under their own keys), sent in
  order when there is a connection (an image before the action that names it), and cleared entry by entry as
  the server answers. The header shows online or offline and how many entries wait; a rejection (a missing
  signature, a stale checklist item) stays on screen as a note until dismissed. The server merges per BR-45.6.
- `public/sw.js` caches the shell, its build assets and the manifest so the app opens offline; `/api/` calls and
  writes never go through it (`serviceWorker.test.ts` runs it against fakes).

## Needs

- Sign-in: the app sends the browser's session like the web shell; the technician's token must carry the
  worker id as the user id (`service_technician.worker_id`), see `services/fieldservice/NOT-IMPLEMENTED.md`.
- The web BFF must route `/api/v1/service/...` and `/api/v1/inventory/truck-stock-levels` (same rules as the web app).
- Not built: push of published routes (the app polls every minute and on reconnect), live position tracking,
  PWA icons, a Playwright offline run against real devices (hardware is out of scope).
