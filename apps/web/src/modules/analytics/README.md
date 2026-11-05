# analytics (web module)

Wave 46 (PSG-46), slices 1 to 4. Screens over `services/analytics/api/openapi.yaml`:

| Route | Page | API | Duty |
|---|---|---|---|
| `/analytics`, `/analytics/metrics` | Governed metrics list | `GET /api/v1/analytics/metrics` | analytics.view |
| `/analytics/metrics/:id` | Metric definition: formula, grain, dimensions, reconciliation source | `GET /api/v1/analytics/metrics` | analytics.view |
| `/analytics/dashboards` | Dashboards with tile count, refresh interval and version | `GET /api/v1/analytics/dashboards` | analytics.view |
| `/analytics/dashboards/new` | Create a dashboard (no `If-Match`): 1 to 12 tiles, each a published metric and a period | `PUT /api/v1/analytics/dashboards/{id}` | analytics.author |
| `/analytics/dashboards/:id` | Tiles with their governed metric and formula; edit and save with `If-Match` | `GET/PUT /api/v1/analytics/dashboards/{id}`, `GET /api/v1/analytics/metrics` | analytics.view (read), analytics.author (save) |
| `/analytics/explorer` | Explorer: choose metrics, dimensions and a range, see the Cube plan with the legal-entity scope filter; `NO_SCOPE` shown as an empty result; export the planned query to XLSX or CSV (one `Idempotency-Key` per request, reused on retry) | `POST /api/v1/analytics/query`, `POST /api/v1/analytics/exports` | analytics.view, analytics.export (export) |
| `/analytics/exports` | Export log filtered by user, format and day | `GET /api/v1/analytics/exports` | analytics.export |
| `/analytics/reconciliation` | Daily reconciliation per metric and legal entity, failure banner, trust counter | `GET /api/v1/analytics/reconciliation` | analytics.finance.view |
| `/analytics/data-quality` | Data-quality scorecard: rules with latest pass rate, failed rows, below-threshold streak and escalation; filter by asset, rule type and owner | `GET /api/v1/catalog/dq-rules`, `GET /api/v1/catalog/dq-rules/scores` | analytics.catalog.steward |
| `/analytics/data-quality/new` | Create a rule (no `If-Match`) | `PUT /api/v1/catalog/dq-rules/{ruleId}` | analytics.catalog.steward |
| `/analytics/data-quality/:ruleId` | Rule definition, save with `If-Match`, mute for 7 days, score history | `GET/PUT /api/v1/catalog/dq-rules/{ruleId}`, `GET /api/v1/catalog/dq-rules/scores?rule=` | analytics.catalog.steward |
| `/analytics/schedules` | Scheduled reports with next run | `GET /api/v1/analytics/schedules` | analytics.author |
| `/analytics/schedules/new` | Create a schedule; one `Idempotency-Key` per filled-in form, reused on retry | `POST /api/v1/analytics/schedules` | analytics.author |
| `/analytics/schedules/:id` | Schedule detail and delivery attempts (filter by status), `ANA-0108` banner when the latest run failed after its retries | `GET /api/v1/analytics/schedules/{id}`, `GET .../deliveries` | analytics.author |
| `/analytics/lineage?asset=&depth=` | Upstream and downstream assets with hops, walk to a neighbour; record an edge | `GET /api/v1/catalog/lineage/{asset_fqn}`, `POST /api/v1/catalog/lineage/edges` | analytics.catalog.view, analytics.catalog.steward (record) |
| `/analytics/archive` | Archive partitions by domain, period and status, verification-failure banner | `GET /api/v1/archive/partitions` | analytics.archive.admin |
| `/analytics/archive/:id` | Partition counts, hashes, legal hold; restore for 30 days after confirmation | `GET /api/v1/archive/partitions/{id}`, `POST .../restore` | analytics.archive.admin |

- `module.ts` exports the module contract; the shell discovers it.
- `api/client.ts` calls the API with `fetch` and validates every response with
  zod. Problems keep their code and field errors.
- Pages use the shell primitives from `src/shell/ui` and `pages.module.css`;
  `useLoad.ts` handles loading, error and retry without react-query.
- `pages.test.tsx` (slice 1), `slice2.test.tsx` (slice 2), `slice3.test.tsx`
  (slice 3: inquiry filters) and `slice4.test.tsx` (dashboards, exports,
  deliveries, lineage) cover loading,
  empty and error states, the client checks (which mirror the service),
  `If-Match` and `Idempotency-Key` handling and the in-repo WCAG checks.

No new npm dependencies.

Not yet: tile values, sparklines and the freshness badge (they need Cube
execution), embedding the dashboard host in other workspaces, running a plan
for rows, saved views, downloading an export file, "run now", the process
variant explorer, pipeline health and the document designer (see
`services/analytics/NOT-IMPLEMENTED.md`).

## Needs

- The shell (wave 5) must send the `analytics.view`, `analytics.finance.view`,
  `analytics.catalog.steward`, `analytics.catalog.view`, `analytics.author`, `analytics.export` and
  `analytics.archive.admin` duties to the API
  through the BFF. Pages do not check duties; nav entries are hidden by
  duty.
