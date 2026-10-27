# Quality web module (wave 33, PSG-33 slices 1 to 5)

Auto-discovered by the shell through `module.ts`. Pages use the shell primitives from
`src/shell/ui` and `pages.module.css`; every list has loading, empty and error states.

| Route | Page | Service calls | Duty |
|---|---|---|---|
| `/quality`, `/quality/orders` | `OrdersPage`: quality order worklist, filter by status and site | `GET /quality-orders` | QM_VIEW |
| `/quality/orders/new` | `NewOrderPage`: manual order (test group + sampling plan) or a trigger matched to an association | `GET /test-groups`, `POST /quality-orders` | QM_ORDER_MAINTAIN |
| `/quality/orders/:id` | `OrderDetailPage`: lot, sample, Ac/Re, evaluation, results; `ResultsEntry` (one sample, a row per test) and `UsageDecisionForm` (split lines that sum to the lot, `If-Match`); a decided order links to the NCR its decision raised and to an amending NCR; `CertificatesPanel` and `SamplesPanel` (slice 4); on a closed order `CoAPanel` lists the certificate of analysis revisions (batch and per shipment), previews one with its lines and limits, and issues the next revision (a reason once one exists) | `GET /quality-orders/{id}`, `GET /instruments`, `POST …/results`, `POST …:usage-decision`, `GET …/coas`, `POST /coa:generate` | QM_VIEW, QM_RESULTS_RECORD, QM_USAGE_DECISION, QM_COA |
| `/quality/test-groups`, `/new` | Test group versions; create the next version with variable and attribute tests | `GET`/`POST /test-groups` | QM_VIEW, QM_SETUP_MAINTAIN |
| `/quality/instruments`, `/new` | Instruments with calibration state, "Calibrated today", and the calibration due list ("Due within" 14, 30 or 90 days, overdue included); register | `GET /instruments?dueWithinDays=`, `POST /instruments`, `POST /instruments/{id}:calibrate` | QM_VIEW, QM_SETUP_MAINTAIN |
| `/quality/associations`, `/new` | Quality associations with scope, test group and sampling | `GET`/`POST /associations` | QM_VIEW, QM_SETUP_MAINTAIN |
| `/quality/sampling` | Sampling calculator (Z1.4 level II, fixed, percent) | `POST /sampling-plans:size` | QM_VIEW |
| `/quality/compliance/lists`, `/new` | Compliance list versions (restricted and reporting); create the next version with CAS numbers and thresholds (CAS check digit validated) | `GET`/`POST /compliance-lists` | QM_VIEW, QM_COMPLIANCE_MAINTAIN |
| `/quality/compliance/items`, `/:id` | Item lookup; an item's current composition (substances and components), a form for its next version, and evaluation for a region with hits, block state and undeclared components | `GET`/`POST /items/{id}/composition`, `POST /items/{id}:evaluate-compliance` | QM_VIEW, QM_COMPLIANCE_MAINTAIN |
| `/quality/stability`, `/new`, `/:id` | Stability studies by status with the next pull; plan a study (ICH condition, test group, pull months, lead and window days); the study with its pull schedule and its lifecycle (start, complete, terminate with a reason, under `If-Match`) | `GET`/`POST /stability-studies`, `GET /stability-studies/{id}`, `POST …:transition`, `GET /test-groups` | QM_VIEW, QM_STABILITY_MAINTAIN |
| `/quality/samples` | `SamplesPage`: physical samples of every order, filtered to those due for disposal | `GET /samples` | QM_VIEW |
| `/quality/receiving` | `ReceivingChecksPage`: check a receipt line against accepted source inspection (skip with QM-0128, or the quantity a receiving order must inspect) and the checked receipt lines by PO line | `GET`/`POST /receipt-checks` | QM_VIEW, QM_ORDER_MAINTAIN |
| `/quality/nonconformances` | `NonconformancesPage`: NCRs newest first, filter by status, source and severity, links to the quality order | `GET /nonconformances` | QM_VIEW |
| `/quality/nonconformances/new` | `NewNonconformancePage`: raise an NCR; `?qualityOrderId=` prefills the order (item and unit then default to the order's) | `POST /nonconformances` | QM_NCR_MAINTAIN |
| `/quality/nonconformances/:id` | `NonconformanceDetailPage`: report, approve or refuse (`NcrActions`), start the 8D, the D1 to D8 steps with due dates and overdue flags, complete the current step and record the effectiveness check (`CorrectionPanel`); every change sends `If-Match` | `GET /nonconformances/{id}`, `POST …:approve`, `…:refuse`, `…/corrections`, `…:complete-step`, `…:check-effectiveness` | QM_VIEW, QM_NCR_MAINTAIN, QM_CAPA_MAINTAIN |
| `/quality/spc` | SPC dashboard: X-bar/R or p (and np) charts as inline SVG with limits and flagged points, and a table of points with Western Electric rule hits | `GET /spc`, `GET /test-groups` | QM_VIEW |

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
