# engineering web module

Engineering workspace for wave 9. Discovered by the shell via `module.ts`.

- Where-used report over `GET /api/v1/engineering/boms:where-used` (`/engineering/where-used`; optional `initialComponent` prefill).
- Routings list at `/engineering/routes` over `GET /route-versions` (filters product, site, status, as-of; "Load more" follows `nextCursor`).
- Route version detail at `/engineering/routes/:id` over `GET /route-versions/{id}`: operations in sequence with resource requirements, and a lead-time panel that posts the operations to the stateless `POST /routes:lead-time`. An Activate button shows only while the version is a draft (`POST /route-versions/{id}:activate`): it disables while the request runs, announces success in a polite live region, shows the problem title on a refusal and keeps the draft, and explains a 403 as missing permission.
- Resources list at `/engineering/resources` over `GET /resources` (filters type, site, group, capability; the query string seeds them, so `?groupId=` and `?capabilityId=` links work; "Load more" follows `nextCursor`). A non-UUID filter shows the API's 422 problem title in the alert.
- Resource detail at `/engineering/resources/:id` over `GET /resources/{id}`: header facts, capabilities with level and effective dates, and links to the group's resources and the effective calendar days. A 404 shows its own message without Retry.
- Resource groups at `/engineering/resource-groups` (site filter) and capabilities at `/engineering/capabilities`; their code links open the resources list filtered by that group or capability.
- Calendar days at `/engineering/calendars/:id/days` over `GET /calendars/{id}/days`: from and to dates (default: the current month) refetch on change; days show open or closed, shifts and total hours.
- Resource group detail at `/engineering/resource-groups/:id`, capability detail at `/engineering/capabilities/:id`, work calendar list at `/engineering/calendars` and calendar detail at `/engineering/calendars/:id` (weekly template with hours per weekday, closed dates, generated range). Group and capability list codes link to their detail pages, which link on to the filtered resources list.
- Bills of material at `/engineering/boms` over `GET /bom-versions` (filters product, site, effective-on date and status; status applies at once, the others on "Apply filters"; "Load more" follows `nextCursor`). Rows show site (blank is "All sites"), effective range, quantity range and status.
- BOM version detail at `/engineering/boms/:id` over `GET /bom-versions/{id}`: header facts and lines ordered by line number; each line links to the where-used report prefilled with its component (`/engineering/where-used?component=`). A 404 shows its own message without Retry.
- New BOM version at `/engineering/boms/new` posting `POST /bom-versions` (product, site, dates, draft or active, and line rows of component, quantity and variable scrap). The `Idempotency-Key` is tied to the request body, so resubmitting the same BOM after a lost response replays instead of creating a second version; a changed BOM gets a new key. COM-0001 field errors land on the matching field or line; a 403 is explained as missing `engineering.bom.write`.
- Not yet built: BOM edit, activate and copy screens, and the write screens for resources, groups, capabilities and calendars (create forms, the calendar template editor and generate range); the service has the POST routes.

`api/client.ts` routes every call through one `request` helper that turns a problem document into an `ApiError` carrying `code`, the problem `detail` as its message (falling back to `title`, which is only the HTTP status text), and the per-field `errors`.

Needs: none beyond the shell lockfile.
- Change requests at `/engineering/ecrs` (list with state and product filters), `/engineering/ecrs/new` and `/engineering/ecrs/:id` (workflow actions: submit, withdraw, start review, approve, reject; decisions gated on `engineering.ecr.review` in the UI, enforced by the service). The New ECO form takes an optional change request id and the ECR detail links to and from the ECO.
