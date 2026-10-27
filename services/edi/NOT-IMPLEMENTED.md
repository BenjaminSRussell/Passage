# NOT-IMPLEMENTED: edi (PSG-35, wave 35)

Slice 1 covers the X12 codec (00401 and 00501), 997 acknowledgments, the 850 mapping, partner profiles (create, list, read), inbound X12 receipt with duplicate detection, a read-only interchange list and detail, the map test harness and the web screens for them. Everything else in PSG-35 is listed here.

## Ticket deliverables

| Deliverable | Status | Reason |
| --- | --- | --- |
| 1. EDI engine: X12 850/855/856/810/820/214 and EDIFACT ORDERS/DESADV/INVOIC; AS2/SFTP transport; partner profiles; acknowledgments | partly built, rest later slice of PSG-35 | Built: X12 codec, 997, 850 mapping, partner profiles, inbound receipt. Missing: 855/856/810/820/214 maps and outbound generation from `wms.load.shipped` and `invoice.posted` (FR-35.4); all EDIFACT; CONTRL; AS2 (RFC 4130) and SFTP transports (FR-35.5); MDN handling (BR-35.11). |
| 2. e-Invoicing: Peppol UBL 2.1 via access point, Factur-X, mandate framework (IT SDI, MX CFDI hooks) | later slice of PSG-35 | Only the BR-35.4 arithmetic rules are in code (`ubl.go`). Missing: UBL builder, CEN and Peppol schematron, AP adapter (open decision on provider), EInvoice store, `MandateProvider`, Factur-X PDF/A-3. |
| 3. Integration hub: public REST/GraphQL APIs, webhooks, Kafka subscriptions, Backstage catalog, developer portal, keys and quotas | later slice of PSG-35 | See `services/integration-hub-ts/NOT-IMPLEMENTED.md` and `apps/developer-portal/NOT-IMPLEMENTED.md`. |
| 4. iPaaS connectors: Salesforce, Shopify, Stripe | later slice of PSG-35 | Separate charts; depend on the hub public APIs. |
| 5. Message monitor with replay and partner SLAs | partly built, rest later slice of PSG-35 | Built: interchange list (filter by partner and ack status) and detail with messages, canonical documents and the 997, plus their web screens. Missing: reprocess and resend (`:reprocess`, `:resend`), bulk reprocess with dry run, raw payload viewer with BR-35.12 PII masking (`EDI_PII_VIEW`), segment-tree viewer, partner SLA dashboard, 13-month payload retention. |
| 6. Catalog punchout (cXML and SAP OCI) returning requisition lines to wave 17 | later slice of PSG-35 | FR-35.13. Needs the PunchOut session model and the wave-17 requisition interface. |
| 7. cXML document exchange (OrderRequest, confirmations, ship notices, invoices) | later slice of PSG-35 | FR-35.14. The `payloadID` idempotency rule (BR-35.14) and `/edi/v1/inbound/cxml` are not built. |
| 8. PLM reference connector (Teamcenter, Windchill, file drop) | later slice of PSG-35 | FR-35.15. Built on hub APIs. |
| 9. Customer self-billing intake (EDIFACT INVOIC 389, VDA 4908), matching, dispute worklist | later slice of PSG-35 | FR-35.16 and BR-35.15. The `SELF_BILLED` billing rule belongs to wave 29 (handoff). |

## Acceptance criteria

| Criterion | Status | Reason |
| --- | --- | --- |
| AC-35.1 round trip against the partner simulator for every transaction set | later slice of PSG-35 | No simulator (FR-35.12) and only 850 is mapped. |
| AC-35.2 50 demo invoices pass CEN and Peppol schematron | later slice of PSG-35 | No UBL builder or schematron (see deliverable 2). |
| AC-35.3 webhook at-least-once, retries and ordering | partly built | Policy is built and tested (`integration-hub-ts`). Dispatcher and DLQ are missing. |
| AC-35.4 replay rebuilds projection | later slice of PSG-35 | Replay is not built. |
| AC-35.5 duplicate ISA13 within 10 days rejected with TA1 025 and no order | built, with deviation | Rejected with EDI-0102 (HTTP 409) and nothing is written (tested 10 days back; a number older than `EDI_DUP_WINDOW_DAYS` is accepted again). The response is a problem document, not a TA1 interchange. |
| AC-35.6 quota 600/min with 429 | later slice of PSG-35 | Quotas are a gateway policy. Not built. |
| AC-35.7 850 with PO104 price differing 3% is placed on price hold with a task | later slice of PSG-35 | Order posting into wave 26 is not built (handoff). EDI-0104 is not raised. |
| AC-35.8 856 HL levels S/O/P/I with SSCCs | later slice of PSG-35 | No 856 generation. |
| AC-35.9 phase exit: demo distributor and manufacturer | later slice of PSG-35 | Needs the flow above, the simulator and waves 26 to 31. |
| AC-35.10 to AC-35.14 punchout, cXML, self-billing, PLM | later slice of PSG-35 | See deliverables 6 to 9. |

## Gaps inside slice 1 (to fix in the next slice)

| Item | Status | Reason |
| --- | --- | --- |
| TA1 interchange acknowledgment | later slice of PSG-35 | Duplicates and envelope errors return a problem document. A TA1 (code 025 for duplicates) needs its own outbound control number outside the rolled-back receipt. |
| BR-35.1 rules that need other waves | handoff to wave 26 / wave 7 | Duplicate BEG03 per customer, price verification against wave-26 pricing (EDI-0104), ship-to GLN resolution (EDI-0106), UoM qualifier table, default partner currency and partner item cross-reference lookups. The map keeps the values in the canonical order. |
| GS02/GS03 and ISA08 checks | later slice of PSG-35 | Only ISA05/ISA06 resolve the partner. The profile does not yet store a GS id or our own receiver id. |
| Payloads stored inline | later slice of PSG-35 | `raw_payload` is a text column. Section 6.35.4 specifies object storage keys with 13-month retention. |
| Partner profile fields | later slice of PSG-35 | No transports, maps, delimiters, Peppol id, certificates or `edi.enabled`. Only identity, environment and SLA are stored. No update or deactivate route yet (the version column is ready for If-Match). |
| Interchange-level events | later slice of PSG-35 | Only `dev.passage.edi.message.received.v1` is published. `posted`, `failed`, `sent` and `acked` are not. |
| Kernel-standard endpoints | handoff to platform owner | `/healthz` and `/readyz` are served by the platform router. `/metrics` on the admin port (the ServiceMonitor target), `/v1/edi/openapi.yaml`, `/asyncapi.yaml` and `/error-codes` are not. |
| Test fixtures under `testdata/` | later slice of PSG-35 | The x12-samples, edifact-samples and peppol-invoices-50 fixtures are not created. Tests build interchanges with `domain.Serialize`. |
| Partner environment and `edi.enabled` flag | later slice of PSG-35 | No per-partner enable switch. Rollback (queue inbound) is not built. |
| UBL arithmetic has no route | later slice of PSG-35 | `CheckArithmetic` (BR-35.4, per category and rate) is pure domain code until the UBL builder and `/einvoice/v1/invoices/{id}:validate` land. |

## Handoffs

| To | What |
| --- | --- |
| wave 26 (sales) | Consume `dev.passage.edi.message.received.v1` for 850 posting, price hold and BEG01 change semantics (BR-35.1, BR-35.6). |
| wave 29 (billing) | Add the `SELF_BILLED` billing rule and expose the invoice fields needed by 810 and UBL. |
| wave 31 (warehouse) | Provide SSCC and load data for the 856 (FR-35.4). |
| wave 19 (procurement) | Accept cXML and EDI vendor invoices. |
| platform owner | Serve kernel-standard endpoints listed above. |
| deployment-cicd | Build and publish the image from `services/edi/Dockerfile`, add the Strimzi/Envoy policies and the `integration-db` CloudNativePG cluster. |
