# edi

The EDI engine of Passage (PSG-35, wave 35, PRD §6.35). It replaces AX AIF
ports, SAP IDoc/ALE and the PI/PO EDI mappings. Partners exchange orders, ship
notices and invoices as X12 and EDIFACT interchanges.

## Slice 1 scope

Built in this slice:

- **X12 parser and serializer** (`internal/domain/x12.go`): ISA/GS/ST envelopes for
  versions 00401 and 00501 (ISA11 repetition separator), configurable delimiters,
  count checks (SE01, GE01, IEA01), round-trip property tests.
- **Acknowledgments**: 997 functional acknowledgments built per group with
  AK1/AK2/AK5/AK9 codes A, E, P, R (`internal/domain/ack.go`). The interchange
  status follows AK9: all rejected is REJECTED, partial or with errors is
  ACCEPTED_WITH_ERRORS.
- **850 to canonical Order** (BR-35.1 subset): BEG, CUR (ISO 4217 check), REF*DP/IA,
  DTM*002/010, N1*ST and N1*BT with GLN, PO1 with item cross-reference BP, VP, UP, EN,
  PID*F, SAC charges and allowances (SAC05 N2), CTT count check (`internal/domain/map850.go`).
- **Partner profiles**: ISA identity, environment TEST/PROD, ack SLA, create with
  `Idempotency-Key` (stored in `idempotency_keys` with a request hash), list and read.
- **Inbound X12 receipt**: partner lookup by ISA sender, duplicate ISA13 window
  (BR-35.5, `EDI_DUP_WINDOW_DAYS`; a number may return after the window), gap-free
  outbound control numbers for the 997 (`control_number_counter`), messages with
  outbox `message.received` events in one transaction.
- **Interchange list and detail**: newest first, filter by partner and ack status;
  the detail shows each message's canonical document and the 997. The raw payload
  is not returned (BR-35.12 masking belongs to the message monitor).
- **Map test harness**: runs the mapping over a sample without persisting (FR-35.3).
- **UBL arithmetic checks** (BR-35.4 rules before schematron) as pure domain code
  (`internal/domain/ubl.go`). No HTTP route yet.

## Slice 2 scope

- **Outbound builders** (`internal/domain/outbound*.go`, all pure):
  - 856 ship notice in structure S/O/P/I (BR-35.2): BSN, TD1 with lading quantity and
    gross weight, TD5 with the SCAC under qualifier 2, REF*BM/CN, DTM*011, N1*ST/SF with
    GLN, PRF per order, MAN*GM SSCC-18 per pack (GS1 mod-10 check digit, unique in the
    notice, `sscc.go`), LIN item references and SN1 per item, CTT01 = HL count.
  - 810 invoice (BR-35.3) in 004010 segment order: BIG (DI or CR), REF*BM, N1*RE/BT/ST,
    ITD, DTM*011, IT1 with item references, then TDS, TXI, SAC charges and allowances, CTT.
    The total must equal the line extensions (each rounded to the minor unit) plus taxes
    plus charges minus allowances; TDS01 and SAC05 are N2.
  - 855 acknowledgment: BAK02 AD (no change, all lines IA), AC (with change), RD (all IR),
    AK and RJ (no lines); the acknowledgment date is BAK09; PO1/ACK per line.
- **Outbound from the owning services**: `POST /edi/v1/partners/{id}/outbound:from-source`
  with `{"transactionSet":"856"|"810","sourceId"}` reads the fulfillment shipment (856) or the
  billing invoice (810), plus the sales order for the customer PO, from their services under
  the service grant `edi` -> `fulfillment`/`billing`/`sales` (duties `fulfillment.shipment.read`,
  `billing.invoice.read`, `sales.order.lookup`, nothing else) for the legal entity of the
  partner's trade profile (`PUT /edi/v1/partners/{id}/trade-profile`: legal entity, customer,
  ship-from/ship-to/bill-to/remit-to names and GLNs, default SCAC). The document must belong to
  the profile's legal entity and customer (422 EDI-0011) and be final: shipment shipped with
  shipped containers, invoice posted (409 EDI-0013). It is an explicit API call: the
  `wms.load.shipped` and `invoice.posted` consumers are not built (the shared eventing consumer
  needs a Kafka wiring and service principal that the source services' events do not yet
  justify). Idempotent per source document: a repeat returns the recorded interchange (200) and
  takes no control number; a source that changed after it was sent is `idempotency_key_reuse`.
  Line discounts become one 810 allowance (SAC C310) and header charges one SAC ZZZZ.
- **Outbound generation from a request body**: `POST /edi/v1/partners/{id}/outbound`, served only
  with `EDI_ALLOW_BODY_OUTBOUND=true` (tests and development), builds one of the above
  from its canonical JSON, envelopes it from our identity to the partner with the next
  number of the same gap-free counter the 997s use (ISA13 and GS06, BR-35.5) and records
  an OUT interchange (ack status PENDING, message MAPPED). `Idempotency-Key` is required.
- **997 matching**: a 997 group (GS01 FA) received on `/inbound/x12` is read
  (`Parse997`), matched by AK102 to the outbound interchange with that control number,
  and the answered messages become ACKED or FAILED with one
  `dev.passage.edi.message.acked.v1` per message in the outbox. A 997 is never itself
  acknowledged.
- **Interchange list** filters by direction (IN/OUT).

## Slice 3 scope

- **Message monitor** (FR-35.10, §6.35.14): `GET /edi/v1/messages` lists every stored
  transaction in both directions, newest first, with its interchange envelope (partner,
  ISA13, 997 status) and failing rule; filters by partner, direction, status, transaction
  set and ISA13; keyset pages with an opaque `nextCursor`. `GET /edi/v1/messages/{id}`
  adds the canonical document. Neither returns the raw payload (BR-35.12).
- **Reprocess** (`POST /edi/v1/interchanges/{id}:reprocess`): maps every FAILED
  transaction of an inbound interchange again from its stored raw payload, under a lock
  on the interchange. A transaction that now maps becomes MAPPED and is announced again
  with `message.received` (status MAPPED) so posting consumers pick it up; one that fails
  with another error is updated with `message.failed` (`reprocessed: true`). Unchanged
  outcomes write nothing, the 997 already sent and the interchange status stay as they
  were, and received 997s are not reprocessed. `?dryRun=true` returns the outcome without
  writing and needs no key; otherwise `Idempotency-Key` is required and a replay returns
  the first result. An outbound interchange is refused with EDI-0009.
- **`dev.passage.edi.message.failed.v1`**: published in the receiving transaction for
  every transaction that fails mapping and every 997 that cannot be read or matched, and
  after a reprocess as above. Stored errors now carry the catalog code
  (`EDI-0103: CTT01 2 does not match PO1 count 1`).
- **Partner SLA dashboard** (`GET /edi/v1/partners/sla?days=`): per partner with traffic
  in the window, transactions in both directions, failures and failure rate, outbound
  interchanges with the partner's 997 matched, p95 997 latency (from recording our
  interchange to matching the 997, `interchange.acked_at`, migration 0002), and acks late
  or still overdue against the profile's `ackSlaMinutes`.
- Web: message monitor, message detail, a reprocess card with preview on inbound
  interchange details, and the partner SLA dashboard.

## Slice 4 scope

- **Raw payload viewer** (`GET /edi/v1/interchanges/{id}/payload`, PRD §6.35.6): the
  stored X12 text of an interchange in either direction, parsed into interchange (ISA),
  group (GS), transaction (ST) and segment nodes (`internal/domain/tree.go`). Every element
  carries its X12 reference (for example `N102`) and its 004010 dictionary name for hover;
  trailers are left out. The raw text itself is never returned.
- **PII masking** (BR-35.12): without `EDI_PII_VIEW` the party name (N102), N2 names, every
  N3 and N4 element and the PER contact name and numbers come back as `***` and are flagged
  `masked`; entity codes, qualifiers and the GLN stay visible. Party names in canonical
  documents (`shipToName`, `billToName`, location `name`) are masked the same way on
  `GET /messages/{id}` and `GET /interchanges/{id}`; the message says `piiMasked`.
- **Bulk reprocess** (`POST /edi/v1/interchanges:reprocess?dryRun=`, §6.35.6 "≤ 200 with a
  dry run"): body `{"interchangeIds": [...]}`, 1 to 200 distinct ids. Each interchange
  follows the single-reprocess rules; the whole request is one transaction under one
  `Idempotency-Key` (not needed for a dry run), and interchanges are locked in id order.
  An unknown (`NOT_FOUND`), outbound (`NOT_INBOUND`) or unreadable (`UNREADABLE`)
  interchange is reported in its item and does not stop the others.
- **Fixtures**: `testdata/x12/850-basic.x12` and `850-ctt-mismatch.x12`, used by the domain
  and API tests.
- Web: the raw payload card on the interchange detail, the masking badge on the message,
  and a bulk reprocess card (preview and run) on the message monitor.

## Slice 5 scope

Pure rules with stateless dry-run routes. Nothing in this slice reads or writes the
database or publishes an event, so these routes also run without a store.

- **Peppol BIS Billing 3.0 UBL 2.1** (`internal/domain/ubl_build.go`, FR-35.6, BR-35.4):
  invoices (380) and credit notes (381) with elements in schema order. Line amounts are
  quantity times price rounded to the minor unit; there is one VAT breakdown per category
  and rate (two German standard rates give two S breakdowns); document allowances and
  charges carry their own VAT category and lower or raise its taxable amount. Checked
  before any XML: the BR-35.4 required elements, category codes, rate 0 for Z/E/AE/K/G and
  no rate for O, an exemption reason for E/AE/K/G/O, the supplier VAT id with S and both
  VAT ids with AE/K, a due date not before the issue date, then `CheckArithmetic`. A
  credit note carries the due date as `PaymentMeans/PaymentDueDate`, because UBL 2.1
  CreditNote has no `DueDate`. Dry run: `POST /edi/v1/peppol/invoices:build`.
- **cXML codec** (`internal/domain/cxml.go`, FR-35.14, BR-35.14): envelope and document
  type for ConfirmationRequest, ShipNoticeRequest and InvoiceDetailRequest; the payloadID
  decision (new, replay with the original response, conflict with EDI-0135) on a content
  hash; the Response builder and status classification (200/201 accepted, other 4xx
  rejected, the rest retried). No route yet.
- **Punchout cart validation** (`internal/domain/punchout.go`, FR-35.13, BR-35.13): a cXML
  PunchOutOrderMessage or an SAP OCI 4.0/5.0 HOOK_URL form post (`NEW_ITEM-VENDORMAT`,
  `DESCRIPTION`, `QUANTITY`, `UNIT`, `PRICE`, `PRICEUNIT`, `CURRENCY`, `LEADTIME`, and the
  UNSPSC code from `EXT_CATEGORY_ID` when `EXT_SCHEMA_TYPE` is UNSPSC). Invalid lines are
  dropped with EDI-0133, a cart whose stated total differs from the line sum by more than
  0.01 is rejected whole with EDI-0134, units map through UN/ECE Rec 20 (supplier table
  first), and a price above reference price plus `tolerancePct` flags the line for
  approval. Dry run: `POST /edi/v1/punchout/returns:parse`.
- **Self-billing match** (`internal/domain/selfbill.go`, FR-35.16, BR-35.15): key
  (delivery note, PO line, item); MATCHED, DIFFERENCE (quantity or unit price beyond
  tolerance, or VAT more than 0.01 off the VAT recomputed at the shipment's rate on the
  line as billed) and UNMATCHED; shipments unbilled `waitDays` after shipping raise
  EDI-0138. Dry run: `POST /edi/v1/self-billing:match`.
- **Web**: "Punchout cart check", "Self-billing match" and "Peppol invoice" screens in
  `apps/web/src/modules/integration`.

## Wave 35 proof

- **Partner simulator round trip** (`internal/api/partnersim_test.go`, AC-35.1): the demo
  distributor's translator, written independently of `internal/domain`, exchanges 850/997,
  855, 856 and 810 with the engine and answers with 997s (A, E, R, group-only).
- **peppol-invoices-50** (`testdata/peppol`, AC-35.2): 50 demo invoices and credit notes,
  reference amounts from `gen_peppol50.py`, the engine's XML, and `schematron-report.json`
  from `validate.py` (official Peppol BIS 3.0.21 schematron with Saxon-HE, UBL 2.1 XSD with
  xmllint). Regenerate with `EDI_PEPPOL50_UPDATE=1 go test ./internal/api -run TestPeppol50`,
  then run `validate.py` (instructions in its docstring).
- **UBL builder**: refuses inputs the schematron rejects (endpoint check digits for 0088 and
  0192, supplier VAT id with every category but O, category O alone and without VAT ids,
  BR-CO-26, K without delivery date or country, the DE-R rules between German parties) and
  writes `streetName`, `cityName`, `postalZone`, `legalId`, the seller contact and the
  delivery date and country.

Not built yet: see `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/edi/            entrypoint (dbx.Connect, optional dbx.Migrate, httpx.Serve)
internal/domain/    X12 codec, 997 out and in, 850 mapping, 855/856/810 builders, SSCC, segment tree and PII masking, partner rules, UBL builder and arithmetic, cXML envelope, punchout and self-billing rules (pure)
internal/store/     Postgres repositories (RLS via dbx.WithTenantTx, outbox in the same tx)
internal/api/       HTTP handlers, DTOs, duty checks
internal/events/    event type constants and payloads
migrations/         0001_init.sql (trading_partner, control_number_counter, interchange, message, outbox, idempotency_keys), 0002_monitor.sql (interchange.acked_at, monitor indexes)
api/openapi.yaml    OpenAPI 3.1 for the routes above
events/asyncapi.yaml AsyncAPI 3 for dev.passage.edi.message.received.v1, message.acked.v1 and message.failed.v1
charts/edi/         Helm chart (api deployment, service, optional ServiceMonitor)
testdata/x12/       X12 fixtures for the domain and API tests
```

## Endpoints

| Method | Path | Duty | Notes |
| --- | --- | --- | --- |
| POST | /edi/v1/partners | EDI_ADMIN | Requires `Idempotency-Key`; replay returns 200 with the stored partner, another body under the key is 422 |
| GET | /edi/v1/partners?environment= | EDI_ADMIN | `{items}` by name |
| GET | /edi/v1/partners/{id} | EDI_ADMIN | `ETag` carries the version |
| PUT | /edi/v1/partners/{id}/trade-profile | EDI_ADMIN | Legal entity, customer, party GLNs and default SCAC the sources do not carry; 200 |
| POST | /edi/v1/partners/{id}/outbound:from-source | EDI_OPERATE | Body `{"transactionSet":"856"\|"810","sourceId"}`; 201 with the X12, repeat 200; 409 EDI-0012 without a profile, 503 EDI-0010 without source services |
| POST | /edi/v1/partners/{id}/outbound | EDI_OPERATE | Only with `EDI_ALLOW_BODY_OUTBOUND=true`. Body `{"transactionSet":"856"\|"810"\|"855","document":{...}}`; requires `Idempotency-Key`; 201 with the X12, replay 200 |
| POST | /edi/v1/maps:test | EDI_ADMIN | Body `{"raw": "<X12>"}`; nothing is stored |
| POST | /edi/v1/inbound/x12?environment=TEST\|PROD | EDI_OPERATE | `text/plain` interchange; returns 201 with 997 text (none for an interchange of 997s) |
| GET | /edi/v1/interchanges?partnerId=&ackStatus=&direction=&limit= | EDI_OPERATE | `{items}` newest first, limit 1 to 200 (default 50) |
| GET | /edi/v1/interchanges/{id} | EDI_OPERATE | Messages with canonical documents (party names masked without `EDI_PII_VIEW`) and the 997 |
| GET | /edi/v1/interchanges/{id}/payload | EDI_OPERATE | Raw payload as a segment tree; PII masked without `EDI_PII_VIEW` |
| POST | /edi/v1/interchanges/{id}:reprocess?dryRun= | EDI_OPERATE | Re-map FAILED inbound transactions from the raw payload; `Idempotency-Key` unless `dryRun=true` |
| POST | /edi/v1/interchanges:reprocess?dryRun= | EDI_OPERATE | Bulk reprocess of 1 to 200 distinct interchange ids; `Idempotency-Key` unless `dryRun=true` |
| GET | /edi/v1/messages?partnerId=&direction=&status=&transactionSet=&controlNumber=&limit=&cursor= | EDI_OPERATE | `{items, nextCursor}` newest first, limit 1 to 200 (default 50) |
| GET | /edi/v1/messages/{id} | EDI_OPERATE | One message with envelope and canonical document (party names masked without `EDI_PII_VIEW`) |
| GET | /edi/v1/partners/sla?days= | EDI_OPERATE | `{days, items}`, days 1 to 365 (default 30) |
| POST | /edi/v1/punchout/returns:parse | PUR_REQUISITION_CREATE | Dry run of a returned cart (`format` CXML with `document`, or OCI with `fields`); 200 with lines, issues and the total check |
| POST | /edi/v1/self-billing:match | SELF_BILLING_RECONCILE | Dry run of BR-35.15 over the supplied shipments and self-billed lines; 200 with outcomes and unbilled shipments |
| POST | /edi/v1/peppol/invoices:build | EINV_OPERATE | Dry run of the UBL build; 200 with `xml` when `valid`, otherwise the blocking issues and no XML |

The web screens live in `apps/web/src/modules/integration` (partners, interchanges,
receive, send EDI document, message monitor with single and bulk reprocess, raw
payload viewer, partner SLA, map tester).

## Run and test

```sh
cd services/edi
go test ./...                       # store tests skip without a database
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # runs the store and API database tests
# add PASSAGE_TEST_APP_DATABASE_URL (a NOSUPERUSER NOBYPASSRLS role) to run the RLS test
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/edi
```

Configuration: `DATABASE_URL` (required), `HTTP_ADDR` (default `:8080`),
`MIGRATE_ON_START` (dev only), `EDI_DUP_WINDOW_DAYS` (default 30), `EDI_ISA_QUALIFIER`
and `EDI_ISA_ID` (our ISA06/GS02 identity on outbound documents, default ZZ/PASSAGE),
`PASSAGE_SERVICE_NAME=edi` with `PASSAGE_SERVICE_SIGNING_KEY` or `_FILE` (and `PASSAGE_SERVICE_KEY_ID`),
`EDI_FULFILLMENT_URL`, `EDI_BILLING_URL`, `EDI_SALES_URL` (without a signing key the from-source
route answers 503), `EDI_ALLOW_BODY_OUTBOUND` (default off).

## Dependencies

Allowed set only: platform kernel, chi, pgx/v5, shopspring/decimal, rapid (tests).
No new third-party dependencies in slices 1 to 5.

## Error codes

| Code | HTTP | Meaning |
| --- | --- | --- |
| EDI-0001 | 400 | Request failed validation; `errors` lists the fields |
| EDI-0002 | 400 | `Idempotency-Key` header missing or longer than 255 characters |
| EDI-0003 | 400 / 413 / 422 | Malformed X12 interchange, empty interchange, or over 4 MiB; 422 on reprocess when the stored payload no longer parses; 400 for a punchout document that is not a PunchOutOrderMessage or an OCI post without NEW_ITEM fields |
| EDI-0004 | 409 | Partner with this ISA identity already exists in the environment |
| EDI-0005 | 404 | Partner, interchange or message not found |
| EDI-0006 | (in 200 map result) | Transaction set not supported yet; the transaction is rejected (AK5 R) |
| EDI-0007 | 422 | Outbound document breaks a mapping rule (invalid SSCC, totals that do not add up, unknown field, a value holding a delimiter) |
| EDI-0008 | 409 | The partner is inactive; nothing can be sent to it |
| EDI-0009 | 409 | Only inbound interchanges are reprocessed (an outbound one is resent) |
| EDI-0160 | (in 200 map result) | A required 850 segment or element is missing or invalid; rejected |
| EDI-0161 | (message error) | A received 997 answers no outbound interchange of the partner, or its AK101 does not match |
| idempotency_key_reuse | 422 | `Idempotency-Key` already used with a different request body |
| EDI-0101 | 422 | Interchange sender is not configured for any partner in the environment |
| EDI-0102 | 409 | Duplicate ISA13 inside the duplicate window (TA1 025 semantics; no TA1 interchange is sent yet) |
| EDI-0103 | (in 200 map result) | CTT01 does not match the PO1 count; the transaction is rejected |
| EDI-0105 | (in 200 map result) | No item cross-reference for the line; accepted with errors |
| EDI-0133 | (in 200 punchout result) | Punchout line dropped: missing part id, quantity, price, price unit, allowed currency or mapped unit |
| EDI-0134 | (in 200 punchout result) | Punchout cart total differs from the line sum by more than 0.01; the cart is rejected |
| EDI-0135 | (domain only) | cXML payloadID reused with different content (no route yet) |
| EDI-0136 | (in 200 self-billing result) | Self-billed line differs in quantity, unit price or VAT |
| EDI-0137 | (in 200 self-billing result) | Self-billed line has no matching shipment (UNMATCHED) |
| EDI-0138 | (in 200 self-billing result) | Shipment not self-billed after `waitDays` |
| EINV-0110 | (in 200 UBL result) | A BR-35.4 element or VAT category rule is missing or broken; no XML |
| EINV-0111 | (in 200 UBL result) | An arithmetic rule failed; no XML |

Catalog codes EDI-0104, EDI-0106 to EDI-0109, EINV-0112 and HUB-0113 to HUB-0116 are
defined in PRD §6.35.18 and not raised yet. EDI-0139 is raised by the PLM precedence in
`integration-hub-ts`. EINV-0110 is used for required-element failures before schematron
exists; the PRD text names it for schematron failures.
