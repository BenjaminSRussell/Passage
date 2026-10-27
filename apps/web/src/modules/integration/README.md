# integration (web module, PSG-35)

Screens for the edi service (`services/edi/api/openapi.yaml`), wired through `api/client.ts` to `/api/v1/edi/...`.

| Route | Page | Service call |
| --- | --- | --- |
| `/integration/partners` | Trading partners list, environment filter | `GET /partners` |
| `/integration/partners/new` | Create a partner (one `Idempotency-Key` per attempt) | `POST /partners` |
| `/integration/partners/:id` | Profile and its recent interchanges | `GET /partners/{id}`, `GET /interchanges?partnerId=` |
| `/integration/interchanges` | Interchanges, partner, direction (`?direction=OUT`) and 997 status filters | `GET /interchanges` |
| `/integration/interchanges/receive` | Record a pasted X12 interchange (an 850, or a partner 997 that is matched to an outbound document) | `POST /inbound/x12` |
| `/integration/interchanges/outbound` | Send EDI document: 856, 810 or 855 from a canonical JSON document prefilled with an example; shows the generated X12 (`?partnerId=` preselects) | `GET /partners`, `POST /partners/{id}/outbound` |
| `/integration/interchanges/:id` | Transactions with their canonical document (850 order, 856 packs, 810 invoice, 855 lines, 997 answers), the 997 sent or the partner 997 status | `GET /interchanges/{id}` |
| `/integration/interchanges/:id` (inbound with failures) | Reprocess card: Preview (dry run, no key) and Reprocess (one `Idempotency-Key` per attempt), outcome per transaction | `POST /interchanges/{id}:reprocess` |
| `/integration/interchanges/:id` (raw payload card) | Segment tree of the raw payload: ISA, GS, ST levels nested, each element with its X12 reference and dictionary name on hover; "Personal data masked" badge without `EDI_PII_VIEW` | `GET /interchanges/{id}/payload` |
| `/integration/messages` | Message monitor: every transaction newest first with its failing rule; partner, direction, status, set and ISA13 filters (`?status=`, `?direction=`, `?partnerId=`); Load more by cursor | `GET /messages` |
| `/integration/messages` (bulk reprocess card) | Preview (dry run, no key) and Reprocess (one `Idempotency-Key` per attempt) for the failed inbound interchanges in the current list, at most 200; outcome per interchange | `POST /interchanges:reprocess` |
| `/integration/messages/:id` | One message: envelope, failing rule, canonical document (masked badge without `EDI_PII_VIEW`), link to its interchange to reprocess and see the raw payload | `GET /messages/{id}` |
| `/integration/partner-sla` | Partner SLA: transactions, failure rate, 997s back, p95 997 latency, late and overdue acks per partner; 7/30/90/365-day period | `GET /partners/sla` |
| `/integration/edi/map-tester` | Dry-run the 850 map | `POST /maps:test` |

The outbound document is edited as JSON, like the map tester takes raw X12: structured editors for ASN packs and invoice lines are not built, because these documents normally come from the wave-31 and wave-29 events. Not here yet (later slices of PSG-35): resend, per-row selection for bulk reprocess (the filters choose the interchanges), map designer, e-invoice workspace, hub admin. Tests: `npx vitest --run src/modules/integration`.
