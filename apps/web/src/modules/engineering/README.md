# engineering web module

Engineering workspace for wave 9. Discovered by the shell via `module.ts`.

- Where-used report over `GET /api/v1/engineering/boms:where-used` (`/engineering/where-used`; optional `initialComponent` prefill).
- Routings list at `/engineering/routes` over `GET /route-versions` (filters product, site, status, as-of; "Load more" follows `nextCursor`).
- Route version detail at `/engineering/routes/:id` over `GET /route-versions/{id}`: operations in sequence with resource requirements, and a lead-time panel that posts the operations to the stateless `POST /routes:lead-time`. An Activate button shows only while the version is a draft (`POST /route-versions/{id}:activate`): it disables while the request runs, announces success in a polite live region, shows the problem title on a refusal and keeps the draft, and explains a 403 as missing permission.
- Not yet built: BOM version list and detail screens (follow-up slice of PSG-907), and resource, group, capability and calendar list and detail screens, client calls, nav entries and module routes (slice 9 of PSG-908; the list endpoints are delivered).

`api/client.ts` routes every call through one `request` helper that turns a problem document into an `ApiError` carrying `code` and `title`.

Needs: none beyond the shell lockfile.
