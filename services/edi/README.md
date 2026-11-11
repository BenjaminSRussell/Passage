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
  ship-from/ship-to/bill-to/remit-to names and GLNs, default SCAC; the legal entity must be one of the
  caller's, and RLS shows the profile only to callers scoped to it). The document must belong to
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
- **PII masking** (BR-35.12): without `edi.pii.view` the party name (N102), N2 names, every
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

## Slice 6 scope (#483)

- **X12 820 and 214** (`internal/domain/x12_820.go`, `x12_214.go`): `Build820` (BPR, TRN,
  CUR, N1 PR/PE, ENT with RMR IV and DTM 003; BPR02 must equal the RMR04 total) and `Map820`;
  `Build214` (B10, L11 BM/PO, N1 SF/ST, LX with AT7 and MS1) and `Map214`. Both are outbound
  sets of `POST /edi/v1/partners/{id}/outbound` (GS01 RA and QM). Goldens
  `testdata/x12/820-remittance.x12`, `214-status.x12`; inbound fixture `820-inbound-00501.x12`.
- **EDIFACT** (`internal/domain/edifact.go`): UNA/UNB/UNH/UNT/UNZ codec for syntax 3 and 4
  with the release character, custom service characters and decimal comma; UNT01/UNT02 and
  UNZ01/UNZ02 checks. D.96A mappings onto the canonical documents (`edifact_msgs.go`):
  `MapORDERS`/`BuildORDERS` (Order, with the 850 count and item-reference issues),
  `BuildDESADV`/`MapDESADV` (ASN, held to the 856 rules) and `BuildINVOIC`/`MapINVOIC`
  (Invoice, held to the 810 rules). Goldens under `testdata/edifact/`.
- **CONTRL** (`edifact_contrl.go`): `BuildCONTRL` (UCI 7 or 4, UCM 4 and UCS per rejected
  message, back to the sender, never requesting a CONTRL), `RejectInterchangeCONTRL`,
  `ParseCONTRL` and `ContrlAck.Status` onto the interchange ack status.
- **Transports** (`internal/transport`): the `Sender` interface and `Message`/`Receipt`;
  AS2 (RFC 4130) client and server with a detached CMS SignedData signature (`cms.go`,
  stdlib `encoding/asn1`, RSA or ECDSA with SHA-256; checked against `openssl cms` both ways
  when openssl is installed), synchronous MDN signed on request, MIC check, disposition
  modifiers (authentication-failed, insufficient-message-security, decryption-failed,
  unexpected-processing-error); SFTP with a pinned host key, `.part` upload and rename, and
  an inbound `Pull` that archives each accepted file; an in-memory `Loopback`. Tested over
  `httptest` and an in-process SSH/SFTP server. Slice 7 wires them into the service (below).

## Slice 7 scope: partner transports (#483)

- **Transport per partner** (`PUT|GET /edi/v1/partners/{id}/transport`, migration
  `0006_partner_transport.sql`, `internal/domain/transportcfg.go`): `kind` API (default, nothing
  is transmitted), AS2 (partner and our AS2 ids, the partner's https URL, its pinned PEM
  certificate, signing key reference, `sha-256|384|512`, encryption `none|aes128-cbc|aes192-cbc|aes256-cbc`,
  MDN `sync|async`) or SFTP (host, port, user, pinned `SHA256:` host key, `key|password` with a
  credential reference, outbound and inbound directories, archive and failed subdirectories, poll
  interval). The row hangs off the trade profile and has the same RLS: tenant plus a restrictive
  legal-entity policy, and repointing the profile carries the transport along. **No secret is
  stored**: `signingKeyRef` and `credentialRef` are file names in `EDI_SECRETS_DIR` (a Secret the
  chart mounts read-only and an ExternalSecret fills from Vault), read when a message is sent, so a
  rotation needs no restart; the PUT refuses a reference that does not resolve to a usable secret,
  the database checks refuse anything that is not a plain name (and PEM private keys in the
  certificate column), and the GET reports `ok|missing|malformed` per reference, never content.
- **Outbound delivery** (`internal/delivery`): an outbound document to a partner with an AS2 or SFTP
  transport is queued (`transport_delivery`, PENDING) in the transaction that records it and the
  route returns the delivery. The dispatcher (`EDI_TRANSPORT_WORKERS=true`) finds due tenants
  through a SECURITY DEFINER index, leases rows with skip locked (several replicas never send a row
  twice; a crash re-sends with the same Message-ID after the lease), signs (and encrypts) AS2
  messages, requires a signed MDN with our MIC, and records DELIVERED (messages become SENT,
  `dev.passage.edi.message.sent.v1`), AWAITING_MDN (asynchronous MDN), REJECTED (error
  disposition, not retried) or PENDING with back-off from one minute to an hour, FAILED after 8
  attempts. SFTP uploads as `.part` and renames. `POST /edi/v1/interchanges/{id}:resend` queues a
  new delivery of a FAILED or REJECTED one; the interchange detail lists the deliveries.
- **Asynchronous MDN timeout** (migration `0007_as2_mdn_queue.sql`): an AWAITING_MDN delivery waits
  until `mdnDueAt`, `EDI_AS2_MDN_TIMEOUT_MINUTES` (default 1440, 5 to 10080) after it was sent. The
  dispatcher then moves it back to PENDING to be sent again with the same Message-ID (the partner
  drops the duplicate and answers it) with the usual back-off, or to FAILED once the 8 attempts are
  used up. The delivery counts the waits (`mdnTimeouts`) and says why in `lastError`, the dispatcher
  logs `edi delivery MDN timeout`, and a MDN arriving after the wait finds nothing waiting (404).
  Every delivery that ends FAILED (attempts used up, MDN wait, transport gone) publishes
  `dev.passage.edi.delivery.failed.v1` in the same transaction, for the monitor and the task queue;
  the operator resends it.
- **Asynchronous MDNs we owe** (`transport_mdn`): when a partner asks for its MDN at a
  `Receipt-Delivery-Option` on its configured AS2 host, the signed MDN is stored, not posted from
  memory: the processed MDN in the transaction that records the interchange (so it exists exactly
  when the interchange does), an error or duplicate MDN on its own; the request is answered 200 only
  once the MDN is stored (503 otherwise, so the partner retransmits). The dispatcher leases due MDNs
  like deliveries (skip locked, attempts, back-off from a minute to an hour, FAILED after 8
  attempts), checks the target against the partner's current AS2 URL again, and posts with a client
  that follows no redirects. MDNs survive restarts and are sent by any replica running the workers.
- **AS2 listener** (`EDI_AS2_ADDR`, `api.AS2Routes`): `POST /as2/v1/{endpoint}` and
  `POST /as2/v1/{endpoint}/mdn`, on their own port and Service. See "AS2 listener security" below.
  A verified payload goes through the `/inbound/x12` or `/inbound/edifact` pipeline (told by its
  first segment), whose ISA or UNB sender must be the endpoint's partner; the 997 or CONTRL is
  queued back over the partner's transport (slice 8). Retransmissions
  (same Message-ID and content, or an interchange inside the duplicate window) answer
  `processed/warning: duplicate-document` and record no receipt (a new message repeating an
  interchange is also answered with a TA1 025 or CONTRL 26, slice 9).
- **SFTP poller**: every due partner with an inbound directory is polled once per interval, at
  most 50 files of at most 4 MiB in name order, skipping `.part` files; a file is known by name and
  SHA-256 (`transport_inbound`), is marked DONE in the receipt transaction and then moved to
  `<inbound>/<archive>/<UTC stamp>_<name>`; a re-sent identical file is only archived. A file that
  can never be accepted (neither X12 nor EDIFACT, unknown or foreign sender, too large) is PARKED at once and moved
  to the failed directory; a transient failure backs off and is parked after 5 attempts. A server
  that cannot be reached backs the partner's next poll off up to six hours.

### AS2 listener security

Partners call the AS2 listener without a Passage JWT, so it does not run the tenant middleware and
ignores `X-Passage-*` headers. The endpoint id in the path (random, per partner transport, kept
across updates) is resolved by the SECURITY DEFINER `edi_as2_endpoint()` to a tenant and partner
and nothing else; the work then runs as the edi service in the one legal entity of the partner's
trade profile. The caller is authenticated by its CMS signature against the certificate pinned on
the transport (signatures are always required, encryption when selected; with trust roots the
certificate must also chain to them and not be revoked, 403 otherwise), with AS2-From and AS2-To
matching the profile; nothing is parsed before the signature verifies; bodies are capped at 8 MiB
(1 MiB for MDNs) and the payload at 4 MiB. Unknown endpoints, other transports and inactive
partners all answer 404. Asynchronous MDNs we owe are stored and posted only to the scheme and host
of the partner's configured AS2 URL, whatever `Receipt-Delivery-Option` says, and outbound HTTP
follows no redirects. The main API router is unchanged and still requires a tenant.

Each endpoint id has a token bucket (`EDI_AS2_RATE_PER_MINUTE`, default 120, and
`EDI_AS2_RATE_BURST`, default 60; 0 a minute turns it off) shared by both routes and checked before
anything else: no database lookup, key loading or signature work happens for a request over the
limit, which gets 429 with `Retry-After` (logged once per episode). Only well-formed ids get a
bucket, and at most 10000 are kept (refilled buckets are dropped first), so made-up ids cannot grow
memory; evicting a bucket only resets it to full.

The edge is the AS2 Gateway `passage-as2` (`deploy/gateway/as2-*.yaml`, see `deploy/gateway/README.md`):
`https://as2.<env>.passage.dev`, its own Gateway so the passage-api JWT SecurityPolicy cannot apply,
one route (`POST /as2/v1/` to the `edi-as2` Service), TLS 1.2 or newer, an 8 MiB request buffer and
a rate limit per client address. The chart's NetworkPolicy admits only that Gateway's Envoy proxies to
the AS2 port (`networkPolicy.as2Gateway`); the API and metrics ports keep their previous reach unless
`networkPolicy.apiFrom` names peers.

## Slice 8 scope: inbound EDIFACT and refusal acknowledgments (#483)

- **EDIFACT receipt** (`internal/api/edifact.go`, `POST /edi/v1/inbound/edifact`): the transports
  and the receipt tell the syntax by the first segment (`domain.DetectSyntax`: ISA is X12, UNA or
  UNB is EDIFACT) and run the X12 or the EDIFACT pipeline; the inbound routes take their own
  syntax only. `domain.ReadEdifact` reads UNA (custom service characters), UNB, optional UNG..UNE
  functional groups (`EdifactGroup`, never mixed with bare messages), UNH..UNT and UNZ, checking
  UNT01/UNT02, UNE01/UNE02 and UNZ01/UNZ02 (UNZ01 counts groups when there are groups); every error
  is an `*EdifactError` with its ISO 9735 0085 code and place. A message whose UNT count or
  reference is wrong is kept and rejected alone; any other envelope error refuses the interchange.
  The partner is resolved by UNB sender (S002 id and qualifier, both must match, like ISA05/ISA06)
  against the new partner fields `edifactId`, `edifactQualifier` (migration
  `0008_edifact_inbound.sql`, unique per tenant and environment); with a transport the sender must
  be the transport's partner. The UNB 0020 reference is checked against the duplicate window per
  partner and standard (EDI-0102). ORDERS is mapped like the 850 (`MapORDERS`, issues and
  accepted-with-errors as for the 850), DESADV is held to the 856 rules and INVOIC to the 810 rules;
  each message is stored with its canonical document under the UNH type and reference and gets
  `dev.passage.edi.message.received.v1` (standard EDIFACT), failed ones also `message.failed`. Other
  message types are FAILED (EDI-0006). A received CONTRL is read, matched to our outbound EDIFACT
  interchange (slice 9) and never acknowledged. Reprocess reads stored EDIFACT and re-maps by type
  and UNH reference.
- **CONTRL answers**: a partner's `edifactContrl` is REQUESTED (default: when UNB 0031 = 1), ALWAYS
  or NEVER. `BuildCONTRL` goes back from the recipient to the sender in the sender's syntax and
  service characters, on the partner's own gap-free EDIFACT counter (`control_number_counter`
  standard EDIFACT), never requesting a CONTRL: UCI 7 when a message is accepted, 4 when none is; per
  rejected message UCM 4 with its 0085 code, UCS with the segment position and UCD with the data
  element and component (ORDERS issues carry their place, `Issue.Contrl`: BGM 1001/1225, DTM 137,
  QTY 21, PRI, item references, CNT 2 with 29; an unsupported type is UCS 1 / UCD 12 at 2:1; a bad
  UNT is 29 or 28 at its position); in an interchange with groups each group holding a rejected
  message gets a UCF. The CONTRL is returned by the route, shown as `contrl` on the interchange, and
  queued over the partner's AS2 or SFTP transport like the 997 (`CONTRL_<ref>_<id>.edi`,
  `application/edifact`).
- **Refusals answered** (`api.refuse`, `store.RecordRefusal`): when a transport brought a payload
  from an authenticated partner (AS2 signature verified, or the pinned SFTP host) and the receipt
  refuses it as a whole for its syntax (EDI-0003) or its sender (EDI-0101), the partner still gets
  the error MDN or the parked file, and also a negative acknowledgment when the envelope allows one:
  for X12 a 997 per group (`ReadRefusedX12` reads ISA, GS and ST headers leniently; AK5 R with AK502
  2/3/4 for a missing SE, a wrong SE02 or SE01, AK9 R with AK905 3/4/5 for a missing GE, a wrong
  GE02 or GE01), for EDIFACT a CONTRL UCI 4 (`RefusalCONTRL`: the 0085 code on the UCI, on a UCF for
  a group error; 23 for an unknown or foreign sender), subject to the partner's `edifactContrl`. The
  refused interchange is recorded with `refusal`, ack status REJECTED, no messages and the
  acknowledgment, its `transport_inbound` row is PARKED with it (an AS2 retransmission is refused
  again without a second acknowledgment), and the acknowledgment is queued in the same transaction.
  A refused interchange never counts in the duplicate window, so the corrected one is received. No
  acknowledgment is built for traffic that is not authenticated (unknown endpoint, bad signature),
  for payloads over 4 MiB, for an ISA or UNB that cannot be read, or for an interchange of
  acknowledgments only. Slice 9 adds the TA1 for X12 envelope errors and duplicates (below).

## Slice 9 scope: #483 follow-ups

- **Outbound EDIFACT** (`internal/domain/edifact_outbound.go`): `POST /partners/{id}/outbound`
  takes `ORDERS`, `DESADV` and `INVOIC` next to the X12 sets, and `outbound:from-source` takes
  `DESADV` (from the fulfillment shipment, like the 856) and `INVOIC` (from the billing invoice,
  like the 810) under the same service grant. `BuildEdifactOutbound` holds DESADV to the 856 rules
  and INVOIC to the 810 rules; the message goes in a UNOC syntax 3 interchange (UNA, default service
  characters, UNB 0035 test indicator for a TEST partner) from our UNB identity (`EDI_EDIFACT_ID`,
  `EDI_EDIFACT_QUALIFIER`, default PASSAGE/ZZZ, chart `edifactSender`) to the partner's
  `edifactId`/`edifactQualifier` (409 EDI-0017 when it has none), asks for a CONTRL (UNB 0031 = 1)
  and takes UNB 0020 from the partner's gap-free EDIFACT counter, the one the CONTRLs we send it
  use. It is stored as an OUT interchange (standard EDIFACT, version UNOC:3, UNH reference 1),
  returned as `edifact` with `standard`, and queued over the partner's AS2 or SFTP transport like
  X12 (`application/edifact`, `<type>_<ref>_<id>.edi`), becoming SENT on the MDN or upload.
- **CONTRL matching**: a CONTRL from the partner is matched by its UCI 0020 to our outbound EDIFACT
  interchange with that reference, the way a 997 is matched by AK102 (`store.matchContrl`, sharing
  `applyAnswers` with the 997): each message still MAPPED or SENT becomes ACKED, or FAILED when the
  UCI is 4 or a UCM 4 names it (the error says which, with the 0085 code); UCI 8 (received, nothing
  checked) acknowledges; the interchange takes the CONTRL's status (`ContrlAck.Status`) and
  `acked_at` unless an earlier CONTRL answered it; `dev.passage.edi.message.acked.v1` is published
  per answered message. The CONTRL is POSTED (with notes for UCMs naming no message), or FAILED with
  EDI-0161 when it answers nothing. The SLA dashboard counts EDIFACT acknowledgments like 997s.
- **Inbound 820 and 214**: the X12 receipt maps the 820 onto the canonical remittance (`Map820`:
  BPR, TRN, CUR, N1 PR/PE, RMR IV lines; BPR02 must equal the RMR04 total) and the 214 onto the
  shipment status (`Map214`: B10, L11, N1 SF/ST, LX/AT7/MS1) next to the 850, with the same 997 codes
  (AK5 A, E, or R with the failing rule), `message.received` and, when it fails, `message.failed`;
  reprocess and `maps:test` (which returns `document`) cover them, and payer, payee, ship-from and
  ship-to names are masked without `edi.pii.view`. Inbound X12 856 and 810 are still rejected
  (EDI-0006).
- **TA1 and duplicate acknowledgments** (`internal/domain/x12_ta1.go`): the X12 codec reads and
  writes TA1 segments (between ISA and the first GS). An interchange a transport brought from the
  authenticated partner that fails at the interchange level (`TA1Cause`: IEA missing 023, IEA02 not
  ISA13 001, IEA01 not the group count 021, no group or content after IEA 024) is answered with a
  TA1 R carrying that TA105 code instead of a 997; group and transaction errors keep the negative
  997. A duplicate interchange (EDI-0102) over AS2 or SFTP is answered with a TA1 R 025 (X12) or a
  CONTRL UCI 4 with code 26 (EDIFACT, when the partner wants CONTRLs), next to the
  `processed/warning: duplicate-document` MDN or the archived file. The answer is recorded like a
  refusal (`store.RecordRefusal`): its own transaction after the rolled-back receipt, the next
  number of the partner's X12 (or EDIFACT) counter, an interchange with `refusal` EDI-0102, no
  messages and the TA1 (shown as `ta1` on the interchange), queued as `TA1_<isa13>_<id>.x12`, and
  the AS2 message or file DONE with it so a retransmission is not answered again. Interchanges of
  acknowledgments only (997s, TA1s, CONTRLs) are never answered; the operator routes still answer a
  duplicate with 409 and record nothing. A TA1 from the partner is matched by TA101 to our outbound
  interchange: R rejects its messages (FAILED, REJECTED, `message.acked`), A or E is noted on the
  TA1 (POSTED) and the 997 answers the groups; an unmatched TA1 is FAILED with EDI-0161.
- **Partner update** (`PATCH /edi/v1/partners/{id}`, duty `edi.admin` like the other partner
  writes, migration `0009_partner_update.sql`): a JSON merge patch of `name`, `isaQualifier`,
  `isaId`, `ackSlaMinutes`, `status` (`inactive` stops receipt, the AS2 listener and outbound
  documents) and the EDIFACT identity (`edifactId`, `edifactQualifier`, `edifactContrl`; null or ""
  for `edifactId` removes it). `If-Match` must carry the version from the partner's ETag (428
  `COM-0428` without it, 412 `conflict_version` with the current ETag when stale); the response
  carries the new ETag. The environment is not a member (400). An identity another partner holds
  in the environment is 409 EDI-0004. Once a trade profile binds the partner to a legal entity, only
  callers scoped to that entity (or to every entity) may change it (403 EDI-0018), as for the
  profile and the transport: the profile's restrictive RLS hides it from other callers, so the
  binding is read through `edi_partner_in_entity_scope()`, a SECURITY DEFINER function over the
  `partner_entity_index` a trigger on `partner_trade_profile` keeps (no RLS, no `passage_app`
  grant, listed in `tools/rlscheck`). `passage_app` gets UPDATE on the changed columns only:
  tenant, id, environment and creation time stay write-once and partners are never deleted.
- **Partner certificate trust** (`internal/certtrust`): the partner AS2 certificate stays pinned
  (signatures are verified against it alone, so another certificate of the same CA cannot sign for
  the partner). The `partnerCertificate` field may carry up to five intermediate CA certificates after
  it. With `EDI_AS2_TRUST_ROOTS` (a PEM bundle of CA certificates) the certificate must also chain to
  one of them; with `EDI_AS2_CRL_DIR` (a directory of CRLs, PEM or DER, dot files skipped) every
  certificate of the chain below the root must be covered by the newest current CRL its issuer
  signed (thisUpdate passed, nextUpdate set and ahead) and not be listed. Both fail closed, at
  `PUT /transport` (400 on `as2.partnerCertificate`) and at every use: an inbound AS2 message from a
  partner whose certificate is untrusted or revoked gets 403, an outbound delivery FAILS at once
  (`delivery.failed`); when an issuer has no current CRL the listener answers 503 and deliveries back
  off until the CRL is refreshed. The files are read again when one changes (size or mtime), so a
  rotated CRL needs no restart. `GET /transport` shows `partnerCertificateTrust` (`pinned`, `trusted`,
  `untrusted`, `revocation-unknown`) with the reason. Nothing is fetched from the network: OCSP and
  CRL distribution points are not used.
- **Posting inbound documents (decision)**: only an inbound 850 or ORDERS is posted into another
  service: to sales, with the partner item cross-references and sales defaults (`/partners/{id}/item-xrefs`,
  `/partners/{id}/sales-defaults`), by `POST /messages/{id}:post` and, for a partner whose `autoPost` is not
  turned off, by the background poster, which runs with the transport workers (`EDI_TRANSPORT_WORKERS=true`) when
  the sales URL and service signing key are set. The poster signs as the service principal `edi-sales` (only
  `sales.channel.intake`), takes MAPPED orders under `skip locked`, backs off a minute doubling to an hour after a
  failure it can retry (sales down, no trade profile), and parks what cannot be mapped or sales refuses; a parked
  order is posted again only by the route, and reprocess leaves it alone. The body sent under a message's
  Idempotency-Key is pinned at the first attempt. Transport's
  `POST /v1/transport/tracking-events` needs a load id and stop number the 214 does not carry;
  receivables has no remittance-advice route. Consumers read `dev.passage.edi.message.received.v1`
  and the canonical document; `NOT-IMPLEMENTED.md` (Handoffs) lists what each posting needs.

## Slice 10 scope: the B2B batch (#35, 35-1 to 35-5)

Migration `0012_b2b_exchange.sql`. Every table is tenant-scoped with the legal-entity scope policy of the trade profile.

- **35-1 Event-driven 856, DESADV, 810 and INVOIC.** `edi consume` (chart `consumer.enabled`, off by default) is a Kafka consumer
  (`internal/autosend`, group and principal `edi-outbound`, one duty `edi.outbound.from-event`) of fulfillment's
  `asn.issued` (now carrying `legal_entity_id`) and billing's `invoice.posted`. For each active partner of the event's legal
  entity whose `PUT /partners/{id}/auto-outbound` lists the set (`856`, `DESADV` for a shipment, `810`, `INVOIC` for an invoice) it
  generates the document through the same path as `outbound:from-source` (`API.SendFromSource`), under the same idempotency key
  (partner, set, source document): a redelivered event, a replay from `passage.edi.outbound.dlq` and a route call made by hand
  yield one interchange. A document that is not this partner's (another customer, EDI-0011), cannot be mapped (a partly shipped
  shipment, EDI-0007), or a partner without a profile or EDIFACT identity is logged and skipped; a source outage is retried by the
  consumer (2 s doubling, 10 attempts) and then dead-lettered. One 856 per shipment: a second packing slip of a shipment replays the
  first. (Now keyed by shipment and slip, see Slice 11.)
- **35-2 E-invoice store and mandate framework.** `POST /einvoice/invoices {legalEntityId, invoiceId, mandate}` reads the posted
  billing invoice (or credit note), maps it (`EInvoiceFromBilling`: the VAT rate is derived per line from billing's tax amounts (several
  rates per invoice give one VAT breakdown per rate); line discounts become one document allowance per rate, header charges one charge; payable must equal
  billing's gross, EINV-0112), builds it with the mandate and stores it VALID with its XML or INVALID with the blocking issues, one
  per (invoice, mandate); a repeat returns the stored one and a repeat of an INVALID one builds again. Seller and buyer profiles
  (address, VAT id, endpoint, IBAN, buyer reference) come from `PUT /einvoice/parties/{role}/{subjectId}`. Mandates
  (`domain.Mandates`): PEPPOL (UBL 2.1, Peppol BIS Billing 3.0, built), IT_SDI (FatturaPA) and MX_CFDI (CFDI 4.0) as hooks that refuse
  with EINV-0150 until a country pack installs a generator (`HookMandate.Generate`). `:submit` hands a VALID or rejected e-invoice
  to the `AccessPoint` (a seam; 503 EDI-0029 while none is configured) and `:response` records the network's ACCEPTED or REJECTED;
  `einvoice.submitted`, `.accepted` and `.rejected` are published.
- **35-3 Punchout sessions to requisitions.** `PUT /partners/{id}/punchout` holds the supplier's setup URL, network identities,
  shared-secret reference and the BR-35.13 inputs. `POST /punchout/sessions` opens the catalog for the caller: it stores an OPEN
  session (secret hashed, BuyerCookie, expiry), sends the cXML `PunchOutSetupRequest` (OCI: builds the supplier URL with `HOOK_URL`)
  and returns the start URL. The cart comes back to `POST /punchout/v1/return/{session}?t=<secret>` on the public listener (cXML
  raw, `cxml-urlencoded` or `cxml-base64`, or the OCI form post), is validated (EDI-0133 lines, EDI-0134 total) and kept on the
  session (RETURNED, or REJECTED) with one `punchout.returned` event. `POST /punchout/sessions/{id}:create-requisition` creates a
  DRAFT requisition of the requester in purchasing (service grant `edi` -> `purchasing`, duty `procurement.requisition.create`, the
  requester as `act`, Idempotency-Key `punchout:<session>`).
- **35-4 cXML inbound exchange.** `PUT /partners/{id}/cxml` sets the partner's network identity and shared secret (only its hash is
  kept) and returns its URL `POST /cxml/v1/{endpoint}` on the public listener. ConfirmationRequest, ShipNoticeRequest and
  InvoiceDetailRequest are authenticated by the Sender credential, recorded as an interchange of standard CXML with a canonical
  message (`CXMLCanonical`) and `message.received`, and answered with a cXML Response. The payloadID decision is BR-35.14: the same
  payloadID with the same content returns the first answer and records nothing, other content is 409 EDI-0135. The message monitor
  lists and filters them by document name.
- **35-5 Self-billing intake.** An inbound EDIFACT INVOIC 389 (or 390) is mapped with `selfBilled`, the despatch advice (RFF AAK) and
  order line (RFF LI) of each line and its line tax. The shipments a partner may bill itself for are registered when its 856 or
  DESADV is generated (delivery note = shipment number, PO line, item = product id, price from the sales order line, VAT rate from
  `PUT /partners/{id}/self-billing`). Each document is matched (BR-35.15) right after receipt, and by `POST /self-billing:sweep`
  for any that missed it: claimed shipments are marked billed, a document number is accepted once, every DIFFERENCE (EDI-0136) or
  UNMATCHED (EDI-0137) line opens a dispute, and the sweep opens one UNBILLED dispute (EDI-0138) per shipment past the wait days.
  `GET /self-billing/disputes` is the worklist and `:resolve` accepts or rejects; the document becomes RESOLVED when its last dispute is
  decided. `selfbilling.reconciled`, `dispute.opened` and `dispute.resolved` are published. Nothing is posted to billing or
  receivables (wave 29 handoff).

Sample files: `testdata/cxml/` (the three network documents, a punchout cart and setup response) and
`testdata/edifact/invoic-389-selfbilled.edi`. Tests run against fakes (in-test supplier and access point, fake sources and purchasing).

## Slice 11 scope: B2B round 2 (#35)

- **35-1 per packing slip.** The `asn.issued` event names its packing slip and the SSCCs of the containers that left with it. The consumer
  generates one 856 or DESADV per shipment and slip (idempotency key `source:partner:set:shipment:slip`), listing only that slip's
  containers, with the delivery note `<shipment number>-<slip number>`; a partly shipped shipment is accepted for a slip notice. The route
  `outbound:from-source` still sends the whole shipment under the shipment's key. Self-billing lines are registered per delivery note.
- **35-2.** `POST /einvoice/invoices/{id}:submit` claims the e-invoice (VALID or REJECTED to SUBMITTING in one statement; a concurrent submit
  gets 409 EDI-0030), calls the access point, then records SUBMITTED; a failure releases the claim, and a claim older than ten minutes can
  be taken over. An invoice may carry several VAT rates (the rate is derived per line from billing's tax and snapped to the nearest half
  percent when that reproduces the tax; discounts are allowed per rate, header charges at the rate billing's tax total implies).
  `POST /einvoice/invoices/{id}:validate` runs the Go-native Peppol rule subset (`CheckPeppolRules`), which also gates every Peppol build.
  The mandate `FACTURX` stores the EN 16931 CII XML; `GET /einvoice/invoices/{id}/facturx.pdf` serves a PDF with it embedded. A customer
  registered under a mandate (`PUT /einvoice/mandates/{customerId}/{mandate}`, optional `autoSubmit`) is e-invoiced by `edi consume` when
  its invoice posts. Web: `/integration/einvoices` and its detail.
- **35-4.** `POST /partners/{id}/cxml:send` builds OrderRequest (from a purchase order read from purchasing; orderID is the PO number, kept in
  `cxml_order`), ConfirmationRequest, ShipNoticeRequest and InvoiceDetailRequest, records them as OUT interchanges and posts them to the
  network of `PUT /partners/{id}/cxml/outbound` (https only, our identity, the secret by reference). Inbound confirmations and ship notices
  are put onto purchasing (`:confirm`) and receiving (`POST /v1/receiving/asns`) as `service:edi-cxml`, right after receipt and with
  `POST /messages/{id}:post`; what cannot be mapped is parked with EDI-0036 to EDI-0039 and EDI-0042. A FAILED cXML message is reprocessed
  with `:reprocess` like any inbound interchange.
- **35-5.** The self-billing sweep runs on a schedule with the transport workers (`EDI_SELFBILL_SWEEP_MINUTES`). Web:
  `/integration/self-billing/disputes`.

## Wave 35 proof

- **Partner simulator round trip** (`internal/api/partnersim_test.go`,
  `partnersim_transport_test.go`, AC-35.1): the demo distributor's translator, with its own X12
  and EDIFACT readers and writers (nothing from `internal/domain`), trades every set the
  service maps or sends. Over the routes: 850/997, 855, 856 and 810 answered with 997s (A, E,
  R, group-only). Over AS2: its 820 and 214 mapped and answered with 997s, a 204 rejected as
  unsupported, our 820 and 214 read and answered with a 997 and with TA1 A and R, a duplicate
  answered with TA1 R 025 and a broken IEA with TA1 R 001. Over SFTP: its ORDERS, DESADV and
  INVOIC (UNA with a decimal comma, released service characters) answered with a CONTRL that
  rejects a CNT mismatch (UCM 29), a duplicate answered with CONTRL UCI 4 26, and our DESADV,
  INVOIC and ORDERS read (CPS tree, SSCCs, its own total check) and answered by an interchange
  of CONTRLs that the service matches (ACKED, and FAILED for a UCM 4).
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
internal/domain/    X12 codec, 997 out and in (and the negative 997 of a refused interchange), 850 mapping, 855/856/810/820/214 builders, 820/214 maps, EDIFACT codec with groups, ORDERS/DESADV/INVOIC (in and out) and CONTRL (accept, reject, refusal, matching our outbound), SSCC, segment tree and PII masking, partner rules, UBL builder and arithmetic, cXML envelope, punchout and self-billing rules (pure)
internal/store/     Postgres repositories (RLS via dbx.WithTenantTx, outbox in the same tx)
internal/api/       HTTP handlers, DTOs, duty checks
internal/transport/ AS2 (CMS signing and encryption, sync and async MDN), SFTP and loopback senders; transporttest has the in-process partner endpoints
internal/delivery/  dispatcher (outbound AS2/SFTP deliveries) and SFTP poller
internal/secrets/   resolves transport secret references from EDI_SECRETS_DIR
internal/certtrust/ partner AS2 certificate chain and CRL checks (EDI_AS2_TRUST_ROOTS, EDI_AS2_CRL_DIR)
internal/autosend/  the outbound consumer's handlers (asn.issued, invoice.posted) and the Sender seam (35-1)
internal/events/    event type constants and payloads
migrations/         0001_init.sql (trading_partner, control_number_counter, interchange, message, outbox, idempotency_keys), 0002_monitor.sql (interchange.acked_at, monitor indexes), 0004/0005 trade profile, 0006_partner_transport.sql (partner_transport, transport_delivery, transport_inbound, transport_work index), 0007_as2_mdn_queue.sql (transport_mdn, the MDN wait on transport_delivery), 0008_edifact_inbound.sql (partner EDIFACT identity and CONTRL mode, interchange.refusal), 0009_partner_update.sql (partner UPDATE column grant, partner_entity_index and edi_partner_in_entity_scope())
api/openapi.yaml    OpenAPI 3.1 for the routes above
events/asyncapi.yaml AsyncAPI 3 for dev.passage.edi.message.received.v1, message.acked.v1, message.failed.v1, message.sent.v1 and delivery.failed.v1
charts/edi/         Helm chart (api deployment, service, optional AS2 service, NetworkPolicy, transport ExternalSecret, ServiceMonitor); test-as2.sh checks the AS2 port's NetworkPolicy
testdata/x12/       X12 fixtures for the domain and API tests
testdata/edifact/   EDIFACT fixtures and goldens (ORDERS in, DESADV, INVOIC, CONTRL out)
testdata/as2/       a third-party MDN
```

## Endpoints

| Method | Path | Duty | Notes |
| --- | --- | --- | --- |
| POST | /edi/v1/partners | edi.admin | Requires `Idempotency-Key`; replay returns 200 with the stored partner, another body under the key is 422; optional `edifactId`, `edifactQualifier`, `edifactContrl` for EDIFACT senders |
| GET | /edi/v1/partners?environment= | edi.admin | `{items}` by name |
| GET | /edi/v1/partners/{id} | edi.admin | `ETag` carries the version |
| PATCH | /edi/v1/partners/{id} | edi.admin | JSON merge patch (name, ISA identity, ack SLA, status, EDIFACT identity); `If-Match` with the ETag (428, 412 `conflict_version`); 403 EDI-0018 when the trade profile binds it to another legal entity; 200 with the new ETag |
| PUT | /edi/v1/partners/{id}/trade-profile | edi.admin | Legal entity, customer, party GLNs and default SCAC the sources do not carry; 200 |
| PUT | /edi/v1/partners/{id}/transport | edi.admin | Transport API, AS2 or SFTP with secret references; 409 EDI-0012 without a trade profile in the caller's entities |
| GET | /edi/v1/partners/{id}/transport | edi.admin | The transport, the AS2 inbound and MDN URLs, the partner certificate fingerprint and whether each secret reference resolves |
| GET, POST | /edi/v1/partners/{id}/item-xrefs | edi.admin | Item cross-references of the partner: `{"qualifier":"BP"\|"VP"\|"UP"\|"EN","partnerItem","productId","siteId","warehouseId"}`; 201, 409 EDI-0019 when the item reference is mapped, 409 EDI-0012 without a trade profile; the legal entity is the profile's |
| PUT, DELETE | /edi/v1/partners/{id}/item-xrefs/{xrefId} | edi.admin | Replace or delete one cross-reference (404 outside the caller's legal entities) |
| GET, PUT | /edi/v1/partners/{id}/sales-defaults | edi.admin | `{"invoiceAccountId","currency","autoPost","unitMap"}`: the invoice account of posted orders, the currency of an order without CUR, whether the background poster posts the partner's orders (default true) and the partner unit (EA, CA, PCE) to our unit map (with no map units pass through; with one, a unit with no entry parks the order, EDI-0024); 404 for an unknown partner |
| POST | /edi/v1/messages/{id}:post | edi.operate | Post an inbound 850 or ORDERS to sales (service grant `sales.channel.intake`, channel EDI, key = message id, externalRef = partner id and PO number); 200 `{messageId,status:POSTED,salesOrderId}`, repeat 200; 422 EDI-0105, EDI-0022, EDI-0023 or EDI-0024 parks the message (FAILED, park code, reason in the monitor); 409 EDI-0021 (not an inbound 850 or ORDERS, or its mapping failed), EDI-0012, EDI-0020; 502 EDI-0010 when sales is down (retry); 503 without sales configured |
| POST | /edi/v1/partners/{id}/outbound:from-source | edi.operate | Body `{"transactionSet":"856"\|"810"\|"DESADV"\|"INVOIC","sourceId"}`; 201 with the X12 or EDIFACT, repeat 200; 409 EDI-0012 without a profile, EDI-0017 EDIFACT without `edifactId`; 503 EDI-0010 without source services |
| POST | /edi/v1/partners/{id}/outbound | edi.operate | Only with `EDI_ALLOW_BODY_OUTBOUND=true`. Body `{"transactionSet":"855"\|"856"\|"810"\|"820"\|"214"\|"ORDERS"\|"DESADV"\|"INVOIC","document":{...}}`; requires `Idempotency-Key`; 201 with the X12 or EDIFACT, replay 200 |
| POST | /edi/v1/maps:test | edi.admin | Body `{"raw": "<X12>"}`; nothing is stored |
| POST | /edi/v1/inbound/x12?environment=TEST\|PROD | edi.operate | `text/plain` interchange; 850, 820 and 214 are mapped, 997s matched; returns 201 with 997 text (none for an interchange of 997s) |
| POST | /edi/v1/inbound/edifact?environment=TEST\|PROD | edi.operate | `text/plain` EDIFACT interchange; returns 201 with the CONTRL text when the partner gets one; a received CONTRL is matched to our outbound EDIFACT |
| GET | /edi/v1/interchanges?partnerId=&ackStatus=&direction=&limit= | edi.operate | `{items}` newest first, limit 1 to 200 (default 50) |
| GET | /edi/v1/interchanges/{id} | edi.operate | Messages with canonical documents (party names masked without `edi.pii.view`), the 997 or CONTRL, and `refusal` for a refused interchange |
| GET | /edi/v1/interchanges/{id}/payload | edi.operate | Raw payload as a segment tree; PII masked without `edi.pii.view`; X12 only (422 EDI-0006 for EDIFACT) |
| POST | /edi/v1/interchanges/{id}:reprocess?dryRun= | edi.operate | Re-map FAILED inbound transactions from the raw payload; `Idempotency-Key` unless `dryRun=true` |
| POST | /edi/v1/interchanges:reprocess?dryRun= | edi.operate | Bulk reprocess of 1 to 200 distinct interchange ids; `Idempotency-Key` unless `dryRun=true` |
| POST | /edi/v1/interchanges/{id}:resend | edi.operate | 202 with a new delivery of an outbound interchange whose last delivery FAILED or was REJECTED; 409 EDI-0015 otherwise |
| POST | /as2/v1/{endpoint} | none (AS2 signature) | AS2 listener only (`EDI_AS2_ADDR`, edge `https://as2.<env>.passage.dev`): a partner's AS2 message, answered with an MDN (an asynchronous one is stored and posted by the dispatcher); 429 with `Retry-After` over the endpoint's rate limit |
| POST | /as2/v1/{endpoint}/mdn | none (AS2 signature) | AS2 listener only: the asynchronous MDN for one of our deliveries (404 once the wait ran out); same rate limit |
| GET | /edi/v1/messages?partnerId=&direction=&status=&transactionSet=&controlNumber=&limit=&cursor= | edi.operate | `{items, nextCursor}` newest first, limit 1 to 200 (default 50) |
| GET | /edi/v1/messages/{id} | edi.operate | One message with envelope and canonical document (party names masked without `edi.pii.view`) |
| GET | /edi/v1/partners/sla?days= | edi.operate | `{days, items}`, days 1 to 365 (default 30) |
| POST | /edi/v1/punchout/returns:parse | edi.requisition.create | Dry run of a returned cart (`format` CXML with `document`, or OCI with `fields`); 200 with lines, issues and the total check |
| POST | /edi/v1/self-billing:match | edi.self-billing.reconcile | Dry run of BR-35.15 over the supplied shipments and self-billed lines; 200 with outcomes and unbilled shipments |
| POST | /edi/v1/peppol/invoices:build | edi.einvoice.operate | Dry run of the UBL build; 200 with `xml` when `valid`, otherwise the blocking issues and no XML |
| PUT, GET | /edi/v1/partners/{id}/auto-outbound | edi.admin | `{"transactionSets":["856","810","DESADV","INVOIC"]}`: what the partner receives when a shipment ships or an invoice posts; 409 EDI-0012 without a profile |
| PUT, GET | /edi/v1/partners/{id}/self-billing | edi.admin | Tolerances, wait days and VAT rate of the partner's self-billing |
| PUT, GET | /edi/v1/partners/{id}/punchout | edi.admin | Supplier setup URL, identities, secret reference and BR-35.13 inputs |
| PUT, GET | /edi/v1/partners/{id}/cxml | edi.admin | Network identity and (write-only) shared secret; returns the partner's cXML URL |
| PUT, GET | /edi/v1/einvoice/parties/{role}/{subjectId} | edi.einvoice.operate | Seller (legal entity) or buyer (customer) profile for e-invoices |
| POST, GET | /edi/v1/einvoice/invoices | edi.einvoice.operate | Build and store the e-invoice of a billing invoice for a mandate; list |
| GET | /edi/v1/einvoice/invoices/{id} | edi.einvoice.operate | One e-invoice with its XML and issues |
| POST | /edi/v1/einvoice/invoices/{id}:submit | edi.einvoice.operate | Hand it to the access point; 503 EDI-0029 without one |
| POST | /edi/v1/einvoice/invoices/{id}:response | edi.einvoice.operate | The network's ACCEPTED or REJECTED |
| POST, GET | /edi/v1/punchout/sessions | edi.requisition.create | Open a supplier's catalog; the caller's sessions |
| GET | /edi/v1/punchout/sessions/{id} | edi.requisition.create | One of the caller's sessions with its validated cart |
| POST | /edi/v1/punchout/sessions/{id}:cancel | edi.requisition.create | Cancel an open session |
| POST | /edi/v1/punchout/sessions/{id}:create-requisition | edi.requisition.create | DRAFT requisition of the requester in purchasing from a returned cart |
| POST | /punchout/v1/return/{session}?t= | none (session secret) | Public listener only: the supplier returns the cart |
| POST | /cxml/v1/{endpoint} | none (network identity and shared secret) | Public listener only: a cXML document from a network |
| POST | /edi/v1/self-billing:sweep | edi.self-billing.reconcile | Match pending self-billed invoices; open UNBILLED disputes |
| GET | /edi/v1/self-billing/documents, /edi/v1/self-billing/disputes | edi.self-billing.reconcile | Received self-billed documents; the dispute worklist |
| POST | /edi/v1/self-billing/disputes/{id}:resolve | edi.self-billing.reconcile | ACCEPTED or REJECTED |

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
`EDI_EDIFACT_ID` and `EDI_EDIFACT_QUALIFIER` (our UNB S002 identity on outbound EDIFACT, default
PASSAGE/ZZZ; the qualifier may be empty),
`PASSAGE_SERVICE_NAME=edi` with `PASSAGE_SERVICE_SIGNING_KEY` or `_FILE` (and `PASSAGE_SERVICE_KEY_ID`),
`EDI_PURCHASING_URL` (optional; punchout requisitions, 35-3), `EDI_FULFILLMENT_URL`, `EDI_BILLING_URL`, `EDI_SALES_URL` (without a signing key the from-source
or any source URL is unset the from-source route answers 503), `EDI_ALLOW_BODY_OUTBOUND` (off in the binary; the chart
sets `allowBodyOutbound: true` so the integration screen and the 855 keep working).
Transports: `EDI_SECRETS_DIR` (the mounted transport secrets; without it AS2 and SFTP transports
are refused), `EDI_AS2_ADDR` (AS2 listener address, off when empty, must differ from `HTTP_ADDR`),
`EDI_AS2_PUBLIC_URL` (https base URL partners reach the listener on; needed for asynchronous MDNs),
`EDI_AS2_ALLOW_HTTP` (development only: http partner URLs), `EDI_AS2_RATE_PER_MINUTE` and
`EDI_AS2_RATE_BURST` (the listener's limit per endpoint, default 120 and 60; 0 a minute is off),
`EDI_TRANSPORT_WORKERS=true` (run the dispatcher, which also posts the asynchronous MDNs we owe, and
the SFTP poller; with the AS2 listener on, some replica must run them),
`EDI_TRANSPORT_INTERVAL_SECONDS` (default 15), `EDI_AS2_MDN_TIMEOUT_MINUTES` (how long a delivery
waits for its asynchronous MDN, default 1440), `EDI_AS2_TRUST_ROOTS` (PEM bundle of the CAs partner AS2
certificates must chain to; unset pins certificates only) and `EDI_AS2_CRL_DIR` (CRLs of those CAs,
needs the roots; chart `transports.trust.rootsConfigMap`/`rootsKey` and `crlConfigMap`, mounted under
`/etc/passage/edi-trust`). `edi consume` (the outbound consumer, 35-1) reads `DATABASE_URL`, the service identity and the three source URLs like the API, plus
`KAFKA_BROKERS`, `KAFKA_USERNAME`, `KAFKA_PASSWORD_FILE`, `KAFKA_TLS_CA_FILE` and `ADMIN_ADDR`; chart `consumer.*`. Chart values
under `transports` set them, mount the Secret `transports.secretName` at
`/var/run/passage/edi-secrets` and, with `transports.externalSecret.enabled`, create it from the
Vault path `transports.externalSecret.key` (every property becomes one reference).
The fulfillment, billing and sales charts verify edi's token from `serviceIdentity.publicKeys`
(`edi=<base64 public key>`); until it is set they refuse every edi call.

## Dependencies

Allowed set only: platform kernel, chi, pgx/v5, shopspring/decimal, rapid (tests).
No new third-party dependencies in slices 1 to 5. Slice 6 adds `github.com/pkg/sftp` and
`golang.org/x/crypto/ssh` for the SFTP transport (the versions treasury already uses); AS2
signing uses the standard library only.

## Error codes

| Code | HTTP | Meaning |
| --- | --- | --- |
| EDI-0001 | 400 | Request failed validation; `errors` lists the fields |
| EDI-0002 | 400 | `Idempotency-Key` header missing or longer than 255 characters |
| EDI-0003 | 400 / 413 / 422 | Malformed X12 or EDIFACT interchange (or the other syntax on an inbound route), empty interchange, or over 4 MiB; 422 on reprocess when the stored payload no longer parses; 400 for a punchout document that is not a PunchOutOrderMessage or an OCI post without NEW_ITEM fields |
| EDI-0004 | 409 | Partner with this ISA or EDIFACT identity already exists in the environment |
| EDI-0005 | 404 | Partner, interchange or message not found |
| EDI-0006 | (in 200 map result) / 422 | Transaction set (other than 850, 820, 214 and 997) or EDIFACT message type not supported yet; the transaction is rejected (AK5 R, UCM 4); 422 for the raw viewer of an EDIFACT payload |
| EDI-0007 | 422 | Outbound document breaks a mapping rule (invalid SSCC, totals that do not add up, unknown field, a value holding a delimiter) |
| EDI-0008 | 409 | The partner is inactive; nothing can be sent to it |
| EDI-0009 | 409 | Only inbound interchanges are reprocessed (an outbound one is resent) |
| EDI-0015 | 409 | Nothing to resend: not outbound, its last delivery is not FAILED or REJECTED, or the partner has no AS2 or SFTP transport in the caller's entities |
| EDI-0016 | 503 | No secret directory is configured (`EDI_SECRETS_DIR`), so AS2 and SFTP transports cannot be saved |
| EDI-0017 | 409 | An EDIFACT ORDERS, DESADV or INVOIC for a partner without an EDIFACT identity (`edifactId`) |
| EDI-0019 | 409 | The partner already maps this item reference (qualifier and partner item) |
| EDI-0020 | 409 | An order is posted for a partner without sales defaults (`PUT /partners/{id}/sales-defaults`) |
| EDI-0021 | 409 | The message is not an inbound 850 or ORDERS, or it failed mapping and is reprocessed, not posted |
| EDI-0022 | 422 | The order is parked: a change or cancellation (BEG01), a blanket or release (BEG02), or no requested ship or delivery date |
| EDI-0023 | 422 | The order is parked: sales refused it (its code and reason are in the message error) |
| EDI-0024 | 422 | The order is parked: the partner has a `unitMap` and a line's unit of measure is not in it |
| EDI-0018 | 403 | The partner's trade profile binds it to a legal entity outside the caller's scope; it cannot be changed |
| COM-0428 | 428 | `If-Match` with the partner version is required (PATCH /partners/{id}) |
| conflict_version | 412 | `If-Match` is not the partner's current version; the response `ETag` is |
| EDI-0160 | (in 200 map result) | A required 850 segment or element is missing or invalid; rejected |
| EDI-0161 | (message error) | A received 997 or TA1 answers no outbound interchange of the partner, or the 997's AK101 does not match; a received CONTRL answers no outbound EDIFACT interchange of the partner |
| idempotency_key_reuse | 422 | `Idempotency-Key` already used with a different request body |
| EDI-0101 | 422 | Interchange sender (ISA05/ISA06 or UNB S002) is not configured for any partner in the environment, or is not the transport's partner |
| EDI-0102 | 409 | Duplicate ISA13 or UNB 0020 inside the duplicate window; over AS2 or SFTP also answered with a TA1 R 025 or a CONTRL code 26 |
| EDI-0103 | (in 200 map result) | CTT01 does not match the PO1 count; the transaction is rejected |
| EDI-0105 | (in 200 map result) | No item cross-reference for the line; accepted with errors |
| EDI-0025 to EDI-0026 | | Self-billing: EDI-0026 the dispute was already decided (EDI-0025 is reserved) |
| EDI-0027 | 409 | E-invoice: the seller or buyer profile is missing |
| EDI-0028 | 400 | Unknown e-invoice mandate |
| EDI-0029 | 502 / 503 | No access point configured, or it did not take the document |
| EDI-0030, EDI-0031 | 409 | E-invoice not in the right status, or INVALID and not submittable |
| EDI-0032 | 503 | Punchout needs the public URL (`EDI_AS2_PUBLIC_URL`) |
| EDI-0033 | 502 | The supplier refused or could not be reached for the punchout setup |
| EDI-0034 | 422 | Purchasing refused the requisition of a returned cart |
| EDI-0035 | 409 | The punchout session is not open, or its cart was not accepted |
| EINV-0112 | (in issues) | The e-invoice payable differs from billing's gross |
| EINV-0150 | (in issues) | The mandate is a hook and no generator is installed |
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
