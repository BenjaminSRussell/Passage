# apps/ess — Employee self-service (wave 42)

Status: **partly built (#267).** The shell module has `/hr/my/profile` (hr.profile.self), `/hr/my/team` and
`/hr/my/team/:id` (hr.team.manage). The service scopes them server-side: `GET /v1/hr/me`, `/v1/hr/me/team`,
`/v1/hr/me/team/{id}`; hr.team.approve absence decide, list and balances are limited to direct reports.
Still open: address change, pay statements, questionnaires, checklist tasks, team calendar,
manager-scoped course approval.

Decision (PRD 8.42.12, open question 2): ESS ships as a shell module under
`apps/web/src/modules/hr/` rather than a separate PWA. The HR module built in
slice 1 is that shell module. It has the HR staff screens (workers, positions,
employments, assignments, the audited PII reveal and the as-of view); the
self-service screens below are not built.

What an ESS slice adds here, when it starts:

- Personal data, address change and benefit enrollment screens (hr.profile.self,
  hr.benefits.self). These need the benefits endpoints.
- Absence requests and balances (hr.absence.self) and "My questionnaires".
- Pay statements (wave 43) and checklist tasks (hr.profile.self).

Needs from the shell owner (wave 5): a self-service route that does not
require the HR workspace duties, and an accessible layout that works at 360 px
width (PRD 8.42.6).

Consumes `services/hr/api/openapi.yaml`. The browser uses `/api/v1/hr/...`,
which the web BFF rewrites to the HR service.
