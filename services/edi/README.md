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
- **Outbound generation**: `POST /edi/v1/partners/{id}/outbound` builds one of the above
  from its canonical JSON, envelopes it from our identity to the partner with the next
  number of the same gap-free counter the 997s use (ISA13 and GS06, BR-35.5) and records
  an OUT interchange (ack status PENDING, message MAPPED). `Idempotency-Key` is required.
- **997 matching**: a 997 group (GS01 FA) received on `/inbound/x12` is read
  (`Parse997`), matched by AK102 to the outbound interchange with that control number,
  and the answered messages become ACKED or FAILED with one
  `dev.passage.edi.message.acked.v1` per message in the outbox. A 997 is never itself
  acknowledged.
- **Interchange list** filters by direction (IN/OUT).

Not built yet: see `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/edi/            entrypoint (dbx.Connect, optional dbx.Migrate, httpx.Serve)
internal/domain/    X12 codec, 997 out and in, 850 mapping, 855/856/810 builders, SSCC, partner rules, UBL arithmetic (pure)
internal/store/     Postgres repositories (RLS via dbx.WithTenantTx, outbox in the same tx)
internal/api/       HTTP handlers, DTOs, duty checks
internal/events/    event type constants and payloads
migrations/         0001_init.sql (trading_partner, control_number_counter, interchange, message, outbox, idempotency_keys)
api/openapi.yaml    OpenAPI 3.1 for the routes above
events/asyncapi.yaml AsyncAPI 3 for dev.passage.edi.message.received.v1 and message.acked.v1
charts/edi/         Helm chart (api deployment, service, optional ServiceMonitor)
```

## Endpoints

| Method | Path | Duty | Notes |
| --- | --- | --- | --- |
| POST | /edi/v1/partners | EDI_ADMIN | Requires `Idempotency-Key`; replay returns 200 with the stored partner, another body under the key is 422 |
| GET | /edi/v1/partners?environment= | EDI_ADMIN | `{items}` by name |
| GET | /edi/v1/partners/{id} | EDI_ADMIN | `ETag` carries the version |
| POST | /edi/v1/partners/{id}/outbound | EDI_OPERATE | Body `{"transactionSet":"856"\|"810"\|"855","document":{...}}`; requires `Idempotency-Key`; 201 with the X12, replay 200 |
| POST | /edi/v1/maps:test | EDI_ADMIN | Body `{"raw": "<X12>"}`; nothing is stored |
| POST | /edi/v1/inbound/x12?environment=TEST\|PROD | EDI_OPERATE | `text/plain` interchange; returns 201 with 997 text (none for an interchange of 997s) |
| GET | /edi/v1/interchanges?partnerId=&ackStatus=&direction=&limit= | EDI_OPERATE | `{items}` newest first, limit 1 to 200 (default 50) |
| GET | /edi/v1/interchanges/{id} | EDI_OPERATE | Messages with canonical documents and the 997 |

The web screens live in `apps/web/src/modules/integration` (partners, interchanges,
receive, send EDI document, map tester).

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
and `EDI_ISA_ID` (our ISA06/GS02 identity on outbound documents, default ZZ/PASSAGE).

## Dependencies

Allowed set only: platform kernel, chi, pgx/v5, shopspring/decimal, rapid (tests).
No new third-party dependencies in slices 1 and 2.

## Error codes

| Code | HTTP | Meaning |
| --- | --- | --- |
| EDI-0001 | 400 | Request failed validation; `errors` lists the fields |
| EDI-0002 | 400 | `Idempotency-Key` header missing or longer than 255 characters |
| EDI-0003 | 400 / 413 | Malformed X12 interchange, empty interchange, or over 4 MiB |
| EDI-0004 | 409 | Partner with this ISA identity already exists in the environment |
| EDI-0005 | 404 | Partner or interchange not found |
| EDI-0006 | (in 200 map result) | Transaction set not supported yet; the transaction is rejected (AK5 R) |
| EDI-0007 | 422 | Outbound document breaks a mapping rule (invalid SSCC, totals that do not add up, unknown field, a value holding a delimiter) |
| EDI-0008 | 409 | The partner is inactive; nothing can be sent to it |
| EDI-0160 | (in 200 map result) | A required 850 segment or element is missing or invalid; rejected |
| EDI-0161 | (message error) | A received 997 answers no outbound interchange of the partner, or its AK101 does not match |
| idempotency_key_reuse | 422 | `Idempotency-Key` already used with a different request body |
| EDI-0101 | 422 | Interchange sender is not configured for any partner in the environment |
| EDI-0102 | 409 | Duplicate ISA13 inside the duplicate window (TA1 025 semantics; no TA1 interchange is sent yet) |
| EDI-0103 | (in 200 map result) | CTT01 does not match the PO1 count; the transaction is rejected |
| EDI-0105 | (in 200 map result) | No item cross-reference for the line; accepted with errors |

Catalog codes EDI-0104, EDI-0106 to EDI-0109, EDI-0133 to EDI-0139, EINV-0110 to
EINV-0112 and HUB-0113 to HUB-0116 are defined in PRD §6.35.18 and not raised yet.
