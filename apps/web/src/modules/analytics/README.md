# analytics (web module)

Wave 46 (PSG-46), slice 1. Screens over `services/analytics/api/openapi.yaml`:

| Route | Page | API | Duty |
|---|---|---|---|
| `/analytics`, `/analytics/metrics` | Governed metrics list | `GET /api/v1/analytics/metrics` | AnalyticsView |
| `/analytics/metrics/:id` | Metric definition: formula, grain, dimensions, reconciliation source | `GET /api/v1/analytics/metrics` | AnalyticsView |
| `/analytics/explorer` | Explorer: choose metrics, dimensions and a range, see the Cube plan with the legal-entity scope filter; `NO_SCOPE` shown as an empty result | `POST /api/v1/analytics/query` | AnalyticsView |
| `/analytics/reconciliation` | Daily reconciliation per metric and legal entity, failure banner, trust counter | `GET /api/v1/analytics/reconciliation` | FinanceView |

- `module.ts` exports the module contract; the shell discovers it.
- `api/client.ts` calls the API with `fetch` and validates every response with
  zod. Problems keep their code and field errors.
- Pages use the shell primitives from `src/shell/ui` and `pages.module.css`;
  `useLoad.ts` handles loading, error and retry without react-query.
- `pages.test.tsx` covers loading, empty and error states, the explorer's
  client checks (which mirror the service) and the in-repo WCAG checks.

No new npm dependencies.

Not yet: dashboards, running a plan for rows, saved views, exports, schedules,
pipeline health, document designer (see `services/analytics/NOT-IMPLEMENTED.md`).

## Needs

- The shell (wave 5) must send the `AnalyticsView` and `FinanceView` duties to
  the API through the BFF. Pages do not check duties; nav entries are hidden by
  duty.
