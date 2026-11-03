# Party module (web, PSG-7 AC6)

Workspace for the party service (`services/party/api/openapi.yaml`). The shell discovers `module.ts`. Nav and routes are gated on duties already seeded in `policies/authz/data.json`. `party.merge` is not granted to any role and is not on these screens (issue #450).

| Route | Page | Service calls | Duty |
|---|---|---|---|
| `/party` | Look up a party by UUID | `GET /parties/{id}` | `party.read` |
| `/party/:id` | Party detail with vendor roles | `GET /parties/{id}`, `GET /parties/{id}/vendors` | `party.read` |
| `/party/tax-registrations` | Validate a VAT, EIN or GSTIN | `POST /tax-registrations:validate` | `party.write` |
| `/party/duplicates` | Compare two parties | `GET /parties/{id}` twice | `party.read` |
| `/party/pending-changes` | Open a credit-limit change by id | — | `party.read` |
| `/party/pending-changes/:id` | Approve or reject a held increase | `GET /pending-changes/{id}`, `POST ...:approve` / `:reject` | view `party.read`; decide `party.approve` |

There is no party list or name-search route, no duplicate-candidate queue, and no customer-role list on the current API, so those screens look up by id. Do not call `POST /parties/{id}:merge` from this module.
