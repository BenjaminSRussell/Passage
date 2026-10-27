# NOT-IMPLEMENTED: edi (PSG-35, wave 35)

Slice 1 covers the X12 codec (00401 and 00501), 997 acknowledgments, the 850 mapping, partner profiles (create, list, read), inbound X12 receipt with duplicate detection, a read-only interchange list and detail, the map test harness and the web screens for them. Slice 2 adds the outbound 856 (S/O/P/I with SSCC-18), 810 and 855 builders, outbound generation through `POST /edi/v1/partners/{id}/outbound` on the gap-free control sequence, matching of the partner's 997 to the outbound interchange with the `dev.passage.edi.message.acked.v1` event, a direction filter on the interchange list, and the "Send EDI document" screen. Everything else in PSG-35 is listed here.

## Ticket deliverables

| Deliverable | Status | Reason |
| --- | --- | --- |
| 1. EDI engine: X12 850/855/856/810/820/214 and EDIFACT ORDERS/DESADV/INVOIC; AS2/SFTP transport; partner profiles; acknowledgments | partly built, rest later slice of PSG-35 | Built: X12 codec, 997 out and in (inbound 997s matched to outbound interchanges), 850 mapping, 855/856/810 builders with operator-triggered generation, partner profiles, inbound receipt. Missing: generation driven by `wms.load.shipped` and `invoice.posted` consumers (FR-35.4, needs the wave-31 and wave-29 events); 820/214/204/990 maps; all EDIFACT; CONTRL; AS2 (RFC 4130) and SFTP transports (FR-35.5); MDN handling (BR-35.11). |
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
| AC-35.1 round trip against the partner simulator for every transaction set | partly built | In-process round trip tested: 850 in with 997 out, 856 out, partner 997 in, 856 ACKED (`TestOutboundAPI_GenerateReplayAndMatchPartner997`). No partner simulator (FR-35.12), no transport, and 820/214/EDIFACT are not mapped. |
| AC-35.2 50 demo invoices pass CEN and Peppol schematron | later slice of PSG-35 | No UBL builder or schematron (see deliverable 2). |
| AC-35.3 webhook at-least-once, retries and ordering | partly built | Policy and the dispatch state machine (`nextStep`) are built and tested in `integration-hub-ts`. The HTTP dispatcher, subscription store, DLQ and `next_retry_at` polling are missing. |
| AC-35.4 replay rebuilds projection | partly built | Replay planning (time filter, per-aggregate sequence order under clock skew, cap with a lossless cursor) is pure and tested in `integration-hub-ts`. No Temporal replay workflow, event archive or projection-rebuild test. |
| AC-35.5 duplicate ISA13 within 10 days rejected with TA1 025 and no order | built, with deviation | Rejected with EDI-0102 (HTTP 409) and nothing is written (tested 10 days back; a number older than `EDI_DUP_WINDOW_DAYS` is accepted again). The response is a problem document, not a TA1 interchange. |
| AC-35.6 quota 600/min with 429 | later slice of PSG-35 | Quotas are a gateway policy. Not built. |
| AC-35.7 850 with PO104 price differing 3% is placed on price hold with a task | later slice of PSG-35 | Order posting into wave 26 is not built (handoff). EDI-0104 is not raised. |
| AC-35.8 856 HL levels S/O/P/I with SSCCs | built, input by hand | `Build856` writes S(1), O per order, P per pack with MAN*GM SSCC-18 (GS1 mod-10 checked, unique), I per item, and CTT01 = HL count; tested on the 2-order, 2-pallet, 3-case shape and round-tripped through `Serialize`/`Parse`. The load comes from the request body: the `wms.load.shipped` consumer and the wave-31 LP lookup are not built. |
| AC-35.9 phase exit: demo distributor and manufacturer | later slice of PSG-35 | Needs the flow above, the simulator and waves 26 to 31. |
| AC-35.10 to AC-35.14 punchout, cXML, self-billing, PLM | later slice of PSG-35 | See deliverables 6 to 9. |

## Gaps inside slice 1 (to fix in the next slice)

| Item | Status | Reason |
| --- | --- | --- |
| TA1 interchange acknowledgment | later slice of PSG-35 | Duplicates and envelope errors return a problem document. A TA1 (code 025 for duplicates) needs its own outbound control number outside the rolled-back receipt. |
| BR-35.1 rules that need other waves | handoff to wave 26 / wave 7 | Duplicate BEG03 per customer, price verification against wave-26 pricing (EDI-0104), ship-to GLN resolution (EDI-0106), UoM qualifier table, default partner currency and partner item cross-reference lookups. The map keeps the values in the canonical order. |
| GS02/GS03 and ISA08 checks | later slice of PSG-35 | Only ISA05/ISA06 resolve the partner. The profile does not yet store a GS id or our own receiver id. |
| Our ISA identity per legal entity | later slice of PSG-35 | Outbound documents are sent from one service-wide identity (`EDI_ISA_QUALIFIER`/`EDI_ISA_ID`, chart `isaSender`, default ZZ/PASSAGE). 997s answer with the receiver id the partner used. |
| Outbound version and delimiters per partner | later slice of PSG-35 | Outbound interchanges are always 00401 with `*`, `~`, `>` (PRD §6.35.13 defaults). `x12_version` and `delimiters` are not partner parameters yet. |
| Outbound transmission and `sent` status | later slice of PSG-35 | Generated messages stay MAPPED and the interchange PENDING until a 997 arrives; nothing is transmitted. The X12 text is returned on creation only (the detail view does not show raw payloads, BR-35.12). |
| 997 matching rules | built, with limits | AK102 is matched to the outbound ISA13 (we write the same number to GS06); AK101 must equal the group's functional id. A 997 for an interchange already answered changes nothing. Missing 997 after `ack_sla_minutes` (EDI-0107, resend once) and the TIMEOUT status are not built: there is no SLA sweep. |
| 855 from the order | handoff to wave 26 | The 855 is built from the request body. Building it from the wave-26 order decision (accept, change, reject per line) needs that service. |
| 810 from the invoice | handoff to wave 29 / wave 13 | The 810 is built from the request body, and its total must equal lines plus TXI taxes plus SAC charges minus allowances. Building it from `invoice.posted` and wave-13 tax lines needs those services. CAD (carrier, PRO) is not written. |
| Payloads stored inline | later slice of PSG-35 | `raw_payload` is a text column. Section 6.35.4 specifies object storage keys with 13-month retention. |
| Partner profile fields | later slice of PSG-35 | No transports, maps, delimiters, Peppol id, certificates or `edi.enabled`. Only identity, environment and SLA are stored. No update or deactivate route yet (the version column is ready for If-Match). |
| Interchange-level events | partly built | `received` and `acked` are published through the outbox and declared in AsyncAPI (a test keeps the two in step). `posted` and `failed` need posting into waves 26/19, and `sent` needs the transports; they are neither published nor declared. |
| Kernel-standard endpoints | handoff to platform owner | `/healthz` and `/readyz` are served by the platform router. `/metrics` on the admin port (the ServiceMonitor target), `/v1/edi/openapi.yaml`, `/asyncapi.yaml` and `/error-codes` are not. |
| Test fixtures under `testdata/` | later slice of PSG-35 | The x12-samples, edifact-samples and peppol-invoices-50 fixtures are not created. Tests build interchanges with `domain.Serialize` and the outbound builders. |
| Partner environment and `edi.enabled` flag | later slice of PSG-35 | No per-partner enable switch. Rollback (queue inbound) is not built. |
| UBL arithmetic has no route | later slice of PSG-35 | `CheckArithmetic` (BR-35.4, per category and rate) is pure domain code until the UBL builder and `/einvoice/v1/invoices/{id}:validate` land. |

## Handoffs

| To | What |
| --- | --- |
| wave 26 (sales) | Consume `dev.passage.edi.message.received.v1` for 850 posting, price hold and BEG01 change semantics (BR-35.1, BR-35.6). Expose the order decision the 855 needs. |
| wave 29 (billing) | Add the `SELF_BILLED` billing rule and expose the invoice fields needed by 810 and UBL. |
| wave 31 (warehouse) | Publish `wms.load.shipped` with SSCC (LP id), pack contents, gross weight and carrier so the 856 is generated from the event instead of a request body (FR-35.4). |
| wave 19 (procurement) | Accept cXML and EDI vendor invoices. |
| platform owner | Serve kernel-standard endpoints listed above. |
| deployment-cicd | Build and publish the image from `services/edi/Dockerfile`, add the Strimzi/Envoy policies and the `integration-db` CloudNativePG cluster. |
