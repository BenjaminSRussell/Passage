# Quality web module (wave 33, PSG-33 slices 1 to 5)

Auto-discovered by the shell through `module.ts`. Pages use the shell primitives from
`src/shell/ui` and `pages.module.css`; every list has loading, empty and error states.

| Route | Page | Service calls | Duty |
|---|---|---|---|
| `/quality`, `/quality/orders` | `OrdersPage`: quality order worklist, filter by status and site | `GET /quality-orders` | quality.view |
| `/quality/orders/new` | `NewOrderPage`: manual order (test group + sampling plan) or a trigger matched to an association | `GET /test-groups`, `POST /quality-orders` | quality.order.maintain |
| `/quality/orders/:id` | `OrderDetailPage`: lot, sample, Ac/Re, evaluation, results; `ResultsEntry` (one sample, a row per test) and `UsageDecisionForm` (split lines that sum to the lot, `If-Match`); a decided order links to the NCR its decision raised and to an amending NCR; `CertificatesPanel` and `SamplesPanel` (slice 4); on a closed order `CoAPanel` lists the certificate of analysis revisions (batch and per shipment), previews one with its lines and limits, and issues the next revision (a reason once one exists) | `GET /quality-orders/{id}`, `GET /instruments`, `POST …/results`, `POST …:usage-decision`, `GET …/coas`, `POST /coa:generate` | quality.view, quality.result.record, quality.usage.decide, quality.coa.generate |
| `/quality/test-groups`, `/new` | Test group versions; create the next version with variable and attribute tests | `GET`/`POST /test-groups` | quality.view, quality.setup.maintain |
| `/quality/instruments`, `/new` | Instruments with calibration state, "Calibrated today", and the calibration due list ("Due within" 14, 30 or 90 days, overdue included); register | `GET /instruments?dueWithinDays=`, `POST /instruments`, `POST /instruments/{id}:calibrate` | quality.view, quality.setup.maintain |
| `/quality/associations`, `/new` | Quality associations with scope, test group and sampling | `GET`/`POST /associations` | quality.view, quality.setup.maintain |
| `/quality/sampling` | Sampling calculator (Z1.4 level II, fixed, percent) | `POST /sampling-plans:size` | quality.view |
| `/quality/compliance/lists`, `/new` | Compliance list versions (restricted and reporting); create the next version with CAS numbers and thresholds (CAS check digit validated) | `GET`/`POST /compliance-lists` | quality.view, quality.compliance.maintain |
| `/quality/compliance/items`, `/:id` | Item lookup; an item's current composition (substances and components), a form for its next version, and evaluation for a region with hits, block state and undeclared components | `GET`/`POST /items/{id}/composition`, `POST /items/{id}:evaluate-compliance` | quality.view, quality.compliance.maintain |
| `/quality/stability`, `/new`, `/:id` | Stability studies by status with the next pull; plan a study (ICH condition, test group, pull months, lead and window days); the study with its pull schedule and its lifecycle (start, complete, terminate with a reason, under `If-Match`) | `GET`/`POST /stability-studies`, `GET /stability-studies/{id}`, `POST …:transition`, `GET /test-groups` | quality.view, quality.stability.maintain |
| `/quality/samples` | `SamplesPage`: physical samples of every order, filtered to those due for disposal | `GET /samples` | quality.view |
| `/quality/receiving` | `ReceivingChecksPage`: check a receipt line against accepted source inspection (skip with QM-0128, or the quantity a receiving order must inspect) and the checked receipt lines by PO line | `GET`/`POST /receipt-checks` | quality.view, quality.order.maintain |
| `/quality/nonconformances` | `NonconformancesPage`: NCRs newest first, filter by status, source and severity, links to the quality order | `GET /nonconformances` | quality.view |
| `/quality/nonconformances/new` | `NewNonconformancePage`: raise an NCR; `?qualityOrderId=` prefills the order (item and unit then default to the order's) | `POST /nonconformances` | quality.ncr.maintain |
| `/quality/nonconformances/:id` | `NonconformanceDetailPage`: report, approve or refuse (`NcrActions`), start the 8D, the D1 to D8 steps with due dates and overdue flags, complete the current step and record the effectiveness check (`CorrectionPanel`); every change sends `If-Match` | `GET /nonconformances/{id}`, `POST …:approve`, `…:refuse`, `…/corrections`, `…:complete-step`, `…:check-effectiveness` | quality.view, quality.ncr.maintain, quality.capa.maintain |
| `/quality/spc` | SPC dashboard: X-bar/R or p (and np) charts as inline SVG with limits and flagged points, and a table of points with Western Electric rule hits | `GET /spc`, `GET /test-groups` | quality.view |

`api/client.ts` is the typed `fetch` client for `services/quality/api/openapi.yaml`.
Decimals are strings, never numbers; decision totals are summed exactly. Failures throw
`QualityApiError` with the stable problem `code` and field errors. Forms keep one
`Idempotency-Key` per attempt so a retried submit cannot post twice.

## Test

```
cd apps/web && npx vitest --run src/modules/quality && npx tsc --noEmit
```

## Needs (for the shell owner, wave 5)

- The BFF must rewrite `/api/v1/quality/*` to the quality service (`/v1/quality/*`).
- No npm dependencies were added.

## Not yet in this module

Cost of quality, an overdue-CAPA filter on the NCR list, the CoA PDF, supplier scorecard, SDS,
stability pull recording, a certificate receipt overview across orders, and the compliance reporting extract.
See `services/quality/NOT-IMPLEMENTED.md`.
