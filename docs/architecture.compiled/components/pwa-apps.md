# PWA Apps (shopfloor, warehouse-mobile, technician, supplier-portal, customer-portal)

## Purpose & Boundaries
Five separate Vite builds under `apps/<app>`, each with own service worker, manifest and nginx image; none federates into the shell; they share `@passage/*` packages at build time. Shopfloor kiosk (`apps/shopfloor`, wave 25), Warehouse mobile (`apps/warehouse-mobile`, wave 32), Technician (`apps/technician`, wave 45), Supplier portal (`apps/supplier-portal`, wave 20 extended 35), Customer portal (`apps/customer-portal`, wave 30). Device apps use device-bff; portals use portal-bff.

## Interfaces
Routes (39 total): shopfloor `/enroll`, `/`, `/jobs`, `/jobs/:operationId`, `/time`, `/oee/:resourceId`, `/sync`; warehouse-mobile `/sign-in`, `/`, `/flow/:flowId/:step` (generic FlowRunner from metadata), `/work/:workId`, `/lp/:lpNumber`, `/item/:gtin`, `/print`, `/sync`; technician `/sign-in`, `/`, `/work-orders/:id`, `/work-orders/:id/report`, `/assets/:id`, `/sync`; supplier-portal `/`, `/purchase-orders`, `/purchase-orders/:id`, `/asns/new`, `/invoices`, `/invoices/new`, `/payments`, `/scorecard`, `/profile`; customer-portal `/`, `/orders`, `/orders/:id`, `/invoices`, `/invoices/:id/pay`, `/statements`, `/returns/new`, `/cases`, `/profile`.
- Offline engine `@passage/offline`: IndexedDB `passage-offline`, store `commands`; QueuedCommand `{id (UUIDv7 = Idempotency-Key), actorSub, tenantId, legalEntityId, method POST|PATCH, path, body, ifMatch?, aggregateKey, occurredAt, attempts, status pending|sending|conflict|failed|done, lastProblem?}`; FIFO per aggregateKey, <= 4 aggregates in flight; replay via `POST /bff/v1/sync` batches of 50.

## Data Ownership
Device-local IndexedDB: unsent commands (survive sign-out), cached packets (cleared on sign-out). Allowed: P0/P1 and P2 operator display names only; NEVER P3.

## Events Produced
absent — the source document does not address this

## Events Consumed
absent — the source document does not address this

## Dependencies
internal: device-bff, portal-bff, manufacturing, warehouse, projects, procurement, sales. external: browser APIs (BarcodeDetector, Background Sync, navigator.storage.persist), HID keyboard-wedge scanners, label printers.

## Gotchas & Constraints
- Targets: kiosk 30 min offline, queue survives power loss; warehouse 200 offline transactions; technician 10 h shift packet (A6); scan-to-acknowledge <= 300 ms (local enqueue, doesn't wait for network); PWA initial JS <= 250 KB gzip; touch targets >= 44x44 CSS px (kiosk default 56 px).
- Kiosk worker session ends after 60 s idle (warning 40 s); shared-device idle lock 15 min.
- Replay property tests (fast-check): idempotent, independent aggregates commute, per-aggregate order preserved.
- Service worker: precache shell, prompt-to-reload updates (never mid-task), network-first API GETs.
- Portals online-only writes; drafts kept in IndexedDB as convenience, never P3.
- Camera barcode manual entry always available; GS1 parsing via `@passage/gs1` (AIs 00, 01, 10, 17, 21, 37, 310x).

## Source Sections
- Frontend 2.3 (4837-4847), 5.4 PWA routes (5337-5346), 7.6 (5455-5547), 8.5-8.6 (5678-5688)
