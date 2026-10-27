# apps/ess — Employee self-service (wave 42)

Status: **not built in slice 1 of PSG-42** (later slice of PSG-42).

Decision (PRD 8.42.12, open question 2): ESS ships as a shell module under
`apps/web/src/modules/hr/` rather than a separate PWA. The HR module built in
slice 1 is that shell module. It has the HR staff screens (workers, positions,
employments, assignments, the audited PII reveal and the as-of view); the
self-service screens below are not built.

What an ESS slice adds here, when it starts:

- Personal data, address change and benefit enrollment screens (EssBase,
  EssBenefits). These need the benefits endpoints.
- Absence requests and balances (EssAbsence) and "My questionnaires".
- Pay statements (wave 43) and checklist tasks (EssBase).

Needs from the shell owner (wave 5): a self-service route that does not
require the HR workspace duties, and an accessible layout that works at 360 px
width (PRD 8.42.6).

Consumes `services/hr/api/openapi.yaml`. The browser uses `/api/v1/hr/...`,
which the web BFF rewrites to the HR service.
