# integration (web module, PSG-35)

Screens for the edi service (`services/edi/api/openapi.yaml`), wired through `api/client.ts` to `/api/v1/edi/...`.

| Route | Page | Service call |
| --- | --- | --- |
| `/integration/partners` | Trading partners list, environment filter | `GET /partners` |
| `/integration/partners/new` | Create a partner (one `Idempotency-Key` per attempt) | `POST /partners` |
| `/integration/partners/:id` | Profile and its recent interchanges | `GET /partners/{id}`, `GET /interchanges?partnerId=` |
| `/integration/interchanges` | Interchanges, partner and 997 status filters | `GET /interchanges` |
| `/integration/interchanges/receive` | Record a pasted X12 interchange | `POST /inbound/x12` |
| `/integration/interchanges/:id` | Transactions, canonical orders, the 997 | `GET /interchanges/{id}` |
| `/integration/edi/map-tester` | Dry-run the 850 map | `POST /maps:test` |

Not here yet (later slices of PSG-35): reprocess and resend, the masked raw payload viewer, partner SLA dashboard, map designer, e-invoice workspace, hub admin. Tests: `npx vitest --run src/modules/integration`.
