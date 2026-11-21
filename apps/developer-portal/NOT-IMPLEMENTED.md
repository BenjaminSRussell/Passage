# NOT-IMPLEMENTED: developer-portal (DPT-35, wave 35)

No portal code exists in slice 1. The portal is a Backstage-based app and needs a decision before it is scaffolded.

| Deliverable / criterion | Status | Reason |
| --- | --- | --- |
| Developer portal at `developers.<env>.depot.dev` (FR-35.8) | later slice of DPT-35 | Scaffolding Backstage pulls a large npm dependency tree. That needs the licensing decision (PRD §12 Q1) and an owner for the package set. |
| Backstage API catalog from OpenAPI and AsyncAPI: Public APIs and developer portal (FR-35.8, §6.35.10) | later slice of DPT-35 | The hub catalog registry and its routes (`/v1/hub/catalog`, slice 11 of DPT-35) are built; the Backstage entity provider that reads them is not, and needs the portal scaffold and its licensing decision. |
| Browse APIs and events, create keys, test console, subscriptions, usage (§6.35.6) | later slice of DPT-35 | Depends on the hub API clients and quotas. |
| "Try it" executes against the client's sandbox tenant only (§6.35.6) | later slice of DPT-35 | Sandbox tenants are not provisioned yet. |
| Portal terms page templated (§6.35.12 licensing risk) | handoff | Legal text is owned outside the engineering waves. |
| Chart `depot-devportal` (§6.35.10) | later slice of DPT-35 | Depends on the app build. |
