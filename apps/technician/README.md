# technician (wave 45 PWA) — not yet built

Status: **handoff / later slice of PSG-45.** No code in this directory yet.

Planned scope (PRD 8.45.3 FR-45.8, 8.45.6, BR-45.6, BR-45.7): an offline-capable
PWA for field technicians: today's route, navigate, start travel and work,
checklists, photos (max 5 per action, 1,600 px long edge), truck-stock parts,
signature capture (PNG with timestamp and geo), and the customer summary. Offline
actions go to an append-only action log with client UUIDs and device timestamps,
merged server-side per BR-45.6.

Why it is not in this slice: the PWA needs its own Vite toolchain, a lockfile and a
service worker. Those cannot be added to `apps/web` (shared lockfile rule), and the
server action-log endpoint (`POST /v1/service/work-orders/{id}/actions`) is not built
yet. See `services/fieldservice/NOT-IMPLEMENTED.md`.

## Needs

- `services/fieldservice`: work-order action log endpoint and truck-stock consumption (wave 16 TRUCK locations).
- Platform/shell: PWA manifest and service-worker hosting in the edge/BFF (wave 5 shell owner).
