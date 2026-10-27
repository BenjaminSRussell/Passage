# NOT-IMPLEMENTED: developer-portal (PSG-35, wave 35)

No portal code exists in slice 1. The portal is a Backstage-based app and needs a decision before it is scaffolded.

| Deliverable / criterion | Status | Reason |
| --- | --- | --- |
| Developer portal at `developers.<env>.passage.dev` (FR-35.8) | later slice of PSG-35 | Scaffolding Backstage pulls a large npm dependency tree. That needs the licensing decision (PRD §12 Q1) and an owner for the package set. |
| Backstage API catalog from OpenAPI and AsyncAPI (FR-35.8, §6.35.10) | later slice of PSG-35 | Depends on the hub catalog endpoint (`GET /hub/v1/catalog`), which is not built yet. |
| Browse APIs and events, create keys, test console, subscriptions, usage (§6.35.6) | later slice of PSG-35 | Depends on the hub API clients and quotas. |
| "Try it" executes against the client's sandbox tenant only (§6.35.6) | later slice of PSG-35 | Sandbox tenants are not provisioned yet. |
| Portal terms page templated (§6.35.12 licensing risk) | handoff | Legal text is owned outside the engineering waves. |
| Chart `passage-devportal` (§6.35.10) | later slice of PSG-35 | Depends on the app build. |
