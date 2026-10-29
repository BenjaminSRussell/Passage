# analytics (web module)

Wave 46 (PSG-46), slices 1 and 2. Screens over `services/analytics/api/openapi.yaml`:

| Route | Page | API | Duty |
|---|---|---|---|
| `/analytics`, `/analytics/metrics` | Governed metrics list | `GET /api/v1/analytics/metrics` | AnalyticsView |
| `/analytics/metrics/:id` | Metric definition: formula, grain, dimensions, reconciliation source | `GET /api/v1/analytics/metrics` | AnalyticsView |
| `/analytics/explorer` | Explorer: choose metrics, dimensions and a range, see the Cube plan with the legal-entity scope filter; `NO_SCOPE` shown as an empty result | `POST /api/v1/analytics/query` | AnalyticsView |
| `/analytics/reconciliation` | Daily reconciliation per metric and legal entity, failure banner, trust counter | `GET /api/v1/analytics/reconciliation` | FinanceView |
| `/analytics/data-quality` | Data-quality scorecard: rules with latest pass rate, failed rows, below-threshold streak and escalation | `GET /api/v1/catalog/dq-rules`, `GET /api/v1/catalog/dq-rules/scores` | CatalogSteward |
| `/analytics/data-quality/new` | Create a rule (no `If-Match`) | `PUT /api/v1/catalog/dq-rules/{ruleId}` | CatalogSteward |
| `/analytics/data-quality/:ruleId` | Rule definition, save with `If-Match`, mute for 7 days, score history | `GET/PUT /api/v1/catalog/dq-rules/{ruleId}`, `GET /api/v1/catalog/dq-rules/scores?rule=` | CatalogSteward |
| `/analytics/schedules` | Scheduled reports with next run | `GET /api/v1/analytics/schedules` | AnalyticsAuthor |
| `/analytics/schedules/new` | Create a schedule; one `Idempotency-Key` per filled-in form, reused on retry | `POST /api/v1/analytics/schedules` | AnalyticsAuthor |
| `/analytics/schedules/:id` | Schedule detail | `GET /api/v1/analytics/schedules/{id}` | AnalyticsAuthor |
| `/analytics/archive` | Archive partitions by domain and status, verification-failure banner | `GET /api/v1/archive/partitions` | ArchiveAdmin |
| `/analytics/archive/:id` | Partition counts, hashes, legal hold; restore for 30 days after confirmation | `GET /api/v1/archive/partitions/{id}`, `POST .../restore` | ArchiveAdmin |

- `module.ts` exports the module contract; the shell discovers it.
- `api/client.ts` calls the API with `fetch` and validates every response with
  zod. Problems keep their code and field errors.
- Pages use the shell primitives from `src/shell/ui` and `pages.module.css`;
  `useLoad.ts` handles loading, error and retry without react-query.
- `pages.test.tsx` (slice 1) and `slice2.test.tsx` (slice 2) cover loading,
  empty and error states, the client checks (which mirror the service),
  `If-Match` and `Idempotency-Key` handling and the in-repo WCAG checks.

No new npm dependencies.

Not yet: dashboards, running a plan for rows, saved views, exports, schedule
deliveries and "run now", the process variant explorer, pipeline health,
document designer (see `services/analytics/NOT-IMPLEMENTED.md`).

## Needs

- The shell (wave 5) must send the `AnalyticsView`, `FinanceView`,
  `CatalogSteward`, `AnalyticsAuthor` and `ArchiveAdmin` duties to the API
  through the BFF. Pages do not check duties; nav entries are hidden by
  duty.
