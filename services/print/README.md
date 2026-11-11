# print

Passage print service (wave 32, PSG-32). One per site network. It renders ZPL label
templates, sends them to printers (ZPL II and PCL 6 over raw TCP 9100, PDF over IPP), fails over between the devices of
a queue, and resolves document routing rules. Printers on site networks are reached only
from this pod; nothing connects inbound to the cluster.

## What it does

- **Printer registry** (`POST/GET /print/v1/printers`): device code, device type, dpi, media
  size, host, port, queue and failover rank. `ZPL_II` devices take label media (`2X1_IN`,
  `3X1_IN`, `4X2_IN`, `4X6_IN`, default `4X6_IN`); `PDF_IPP` and `PCL_6` take sheets (`A4`,
  `LETTER`, default `A4`). A database constraint (`0004`) enforces the pairing. Codes are unique per tenant, and a failover rank is unique
  within its queue. A registration without a port gets 9100 for `ZPL_II` and `PCL_6` and 631
  for `PDF_IPP`.
- **Document routing** (`POST/GET /print/v1/routing-rules`): a document type goes to a queue
  by the most specific matching scope. Precedence is user, then zone, then warehouse, then
  the document-type default. Ties go to the lowest `sequence` (BR-32.12).
- **Print jobs** (`POST /print/v1/jobs`, `GET /print/v1/jobs/{id}`): give a `queueCode`, or a
  `documentType` (with optional `zone`/`warehouseId`) and the rules pick the queue. A label job
  renders ZPL from a built-in template and goes to the queue's `ZPL_II` devices; a PDF job
  (`format: PDF`, the document base64 in `documentBase64`, at most 2 MiB, starting `%PDF-`)
  goes to the queue's `PDF_IPP` devices. Devices are tried in failover order (`PRINT_RETRY` + 1
  attempts each, two by default; devices of the other type are skipped, and a queue with none
  of the right type fails with MOB-0112), and the job ends PRINTED or FAILED. The PDF bytes are
  stored in `print_job.document` for reprints and are never returned or listed.
- **Label geometry** (`internal/domain/geometry.go`): templates are laid out at 203 dpi. At
  dispatch each label is sized for the device that prints it: coordinates, fonts and bar codes
  are scaled to its dpi (300 or 600, with `^BY` for the bar module), `^PW`/`^LL` are set from
  its media, and a label whose fields do not fit is refused there with PRN-0007. The fit check
  covers each field's origin and height and each Code 128 bar code's width with its 10-module
  quiet zone on both sides. A refusal is not retried: the next device is tried at once, and the
  job fails with PRN-0007 when no device fits. The stored ZPL stays unsized, so a reprint is
  sized again for its device.
- **1 in layouts** (`internal/domain/layouts.go`): on a 2 x 1 or 3 x 1 in device, a new
  `LP_LABEL`, `LOCATION_LABEL` or `ITEM_LABEL` job prints a compact layout (50-dot bars, the
  human-readable value as its own text line) rendered from the job's data; 4 x 2 and 4 x 6 in
  devices print the standard layout. The GS1 carton label is 4 x 6 in only. An item label with
  lot, expiry and serial is too wide for 1 in devices and fails over. A reprint reuses the
  stored standard payload, so a 1 in device refuses it (see NOT-IMPLEMENTED.md).
  The LP label's SSCC barcode is GS1-128: FNC1, AI 00, then the 18 digits. Completion writes `dev.passage.print.job.completed.v1` to the outbox
  in the same transaction.
- **Templates**: `LP_LABEL`, `LOCATION_LABEL`, `ITEM_LABEL` (gtin, optional lot, expiry
  `YYMMDD` and serial) and `GS1_CARTON_LABEL` (sscc and shipToName, optional poNumber and
  shipToPostal; 4 x 6 in). The two GS1 labels compose their element string from the fields
  (AI 01, 10, 17, 21 and AI 00, 400, 420; `internal/domain/gs1label.go`). Check digits, dates
  (`DD=00` is the month end, BR-32.10), lengths and GS1 set 82 are checked before render, and
  FNC1 (`>8`) follows every variable-length element that another element follows. The bar
  code packs digit runs two per character in Code 128 subset C and switches to subset B
  (`>6`, back with `>5`) for other characters, so the carton and item bar codes fit a 4 in label.
- **Device transports** (`internal/transport`): `Router` picks the transport by device type.
  `ZPL_II` and `PCL_6` are written to raw TCP. `PDF_IPP` posts an RFC 8010 Print-Job
  (`application/pdf`) to `http://<host>:<port>/ipp/print` and fails on any IPP status above
  `0x00FF`. A PDF payload never goes to a raw TCP device and a non-PDF payload never goes to
  an IPP device (`ErrWrongDocumentFormat`). No job carries PCL yet, so `PCL_6` devices are
  never offered a job; see NOT-IMPLEMENTED.md.
- **Business documents as PDF** (`POST /print/v1/documents:render`, duty `print.document.render`,
  issue #530; `internal/domain/document.go`): the caller sends a purchase order, customer invoice,
  packing slip or remittance advice (`REMITTANCE_ADVICE`, sent by the payables payment run page,
  #528: the vendor as counterparty, one line per invoice the payment settled with the discount,
  tax withheld and amount paid, and the total paid) as data and gets an A4 PDF back. Nothing is stored and no printer is used. The
  layout: the legal entity's name and address with the title and document number; the
  counterparty (`counterparty`: the vendor, or the customer as bill-to or ship-to) with its
  address, up to two `additionalParties` (deliver to, ship from) and the header fields; the line
  table; the totals block at the right with the last total in bold under a rule; the notes.
  Columns with `align: R` (numbers) are right-aligned and keep the width of their widest value;
  the other columns share the rest and wrap long text at spaces (a word wider than its column is
  broken). A row is never split: a row that does not fit goes to the next page, which repeats a
  short running header and the table header. Every page ends with "Page n of m". The response
  carries the page count in `X-Passage-Document-Pages`.
  - **Text** is Unicode. It is put in NFC, every kind of space (no-break, narrow no-break, thin)
    and the tab print as a plain space, and invisible format characters (soft hyphen, zero-width
    space, byte order mark) are dropped. The PDF embeds subsets of two fonts, Go Regular and Go
    Bold (`internal/domain/docfont.go`), as Type0 fonts with a CIDFontType2 descendant,
    Identity-H encoding and a ToUnicode CMap, so the text can be selected, copied and searched.
    The fonts cover Latin with its extended letters (Polish, Czech, Slovak, Hungarian, Romanian,
    Turkish, the Nordic and Baltic languages and so on), Greek and the Cyrillic of the Slavic
    languages, and the signs €, £ and ¥.
  - **Limitation**: the fonts have no Chinese, Japanese, Korean, Arabic, Hebrew, Thai, Armenian,
    Georgian or Vietnamese letters, not the extended Cyrillic letters of Kazakh and similar
    alphabets, no emoji, no combining accents that do not compose into one
    letter, and not the currency signs ₹, ₽, ₺, ₩ and ₪. Such a character does not fail the
    document: it prints as the replacement character (U+FFFD, a question mark in a diamond), the
    original text is kept in the PDF as ActualText so copying and searching still find it, and
    the response counts the characters in `X-Passage-Unprintable-Characters` so the caller can
    tell the user. Text also runs left to right only. Covering those scripts needs a font that
    has them (see NOT-IMPLEMENTED.md).
  - **Deterministic**: no dates, no random ids, no compressed streams, a subset tag derived from
    the glyphs used and a fixed object order. The same request gives the same bytes, which the
    golden files in `testdata/golden` pin.
  - **Limits** (a breach is refused with PRN-0008 naming the field): names, labels, values and
    address lines 200 characters, a table cell 500, the notes 2000, 8 address lines per party, 20
    header fields, 20 totals, 8 columns. More than 500 lines is refused with PRN-0009. Fields and
    limits added after the first release are optional or wider, so earlier callers keep working.
- **Docgen: templates, text blocks, correspondence and output routing** (issue #46;
  `internal/domain/docgen.go`, `output.go`, migration `0007`). All of it only changes the data the
  renderer lays out, so the output stays deterministic and the golden PDFs pin it.
  - **Templates** (`PUT /print/v1/document-templates/{code}`, print.admin; `GET` list and one, print.job.submit): per
    document type a tenant sets the title, a footer line, renamed columns and hidden columns (by the header the caller
    sends, with their cells), the text block that is the letter body and text blocks appended to the notes (form notes).
    Each save raises the version; one template per type can be the default and is used when `documentType` has no
    `templateCode`. A request's own `title`, `footerText` and `body` win over the template.
  - **Text blocks** (`PUT /print/v1/text-blocks/{code}/{language}`, `GET /print/v1/text-blocks`): wording with
    `{{placeholders}}`, one version per language tag (`en`, `de-CH`). A render picks the requested language, then
    its base language, then English. `documentNumber`, `legalEntityName` and `counterpartyName` are filled in; the
    request's `values` give the rest, and a placeholder without a value is refused (PRN-0014) rather than printed.
  - **Correspondence**: `documentType` may be `PERIODIC_STATEMENT`, `PAYMENT_NOTICE` or `BALANCE_CONFIRMATION`: a letter
    with a required `body` (or a `bodyBlock` text block) printed after the parties, and an optional table and totals.
    `render` takes `templateCode`, `language`, `bodyBlock` and `values` besides the document data.
  - **Output rules** (`POST/GET /print/v1/output-rules`, `DELETE /print/v1/output-rules/{id}`, print.admin to change):
    a rule sends the ORIGINAL or a COPY of a document type to a `PRINTER` queue (1 to 9 copies), an `EMAIL` address, the
    `EINVOICE` network, the `ARCHIVE` or the `PORTAL`, optionally for one legal entity and/or account.
    `GET /print/v1/output-plan?documentType=&legalEntityId=&accountId=` returns, for each copy kind and channel, the
    rules of the most specific scope that matches (entity and account, then account, then entity, then the default).
    The service only answers where a document goes; sending it there is the caller's job (see NOT-IMPLEMENTED.md).
- **Queue management** (print.admin): `PATCH /print/v1/printers/{id}` edits a printer or moves it to
  another queue or failover rank (PRN-0006 when the rank is held; the code and device type never
  change); `POST /print/v1/printers/{id}:retire` takes it out of its queue (the row stays for the
  job log, its rank is free again, `GET /printers?includeRetired=true` lists it); `PUT` and `DELETE
  /print/v1/routing-rules/{id}` edit and remove a rule. Migration `0006` adds `retired_at`, makes the
  rank unique among active printers, and grants the app role UPDATE on printers and UPDATE/DELETE on
  rules (printers are never deleted).
- **Stale job sweep** (`POST /print/v1/jobs:sweep`, and every 15 s for each tenant in
  `PRINT_PURGE_TENANT_IDS`): a job still QUEUED after a minute (its process died between insert and
  completion) is claimed (its `updated_at` is stamped, so a second sweep skips it) and dispatched in
  failover order. A swept job prints the stored layout, like a reprint.
- **Reprints**: `reprintOf` reuses the stored ZPL or PDF and records the source job.
- **Job log** (`GET /print/v1/jobs?status=&queueCode=&printerCode=&limit=`): newest first (§6.32.14).
- **Printer status** (`GET /print/v1/printers/status`): per printer, the state from its last job
  (IDLE, BUSY, READY, ERROR), the queue depth (QUEUED jobs on its queue) and the last error
  (§6.32.14). No device is probed.
- **Retention purge** (`POST /print/v1/jobs:purge`, print.admin): deletes the tenant's
  PRINTED and FAILED jobs older than `PRINT_JOB_RETENTION_DAYS`; QUEUED jobs are kept (§6.32.19).
  The service also runs it daily at 01:00 UTC for each tenant in `PRINT_PURGE_TENANT_IDS`
  (`internal/jobs`); a failing tenant is logged and retried the next day without stopping the
  others. Both paths add to `print_jobs_purged_total`.
- **Metrics** on the admin port (`ADMIN_ADDR`, default `:9090`, `GET /metrics`):
  `print_jobs_printed_total`, `print_jobs_failed_total`, `print_jobs_purged_total`.
- Multi-tenant: every table, the outbox included, carries `tenant_id` with ENABLE and FORCE
  row-level security (`0001`, `0002`); `0003` adds the partial index for queue depth; `0004`
  adds media sizes and PDF job payloads.

The web screens (printers with media size, printer status, routing rules, print job log,
print a label or a PDF document, job detail with "print again") live in `apps/web/src/modules/mobile-flows`.

## Run

```sh
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_print
export MIGRATE_ON_START=true     # dev only; stage and prod run migrations as a PreSync Job
export HTTP_ADDR=:8080           # default
export ADMIN_ADDR=:9090          # default, serves /metrics
export PRINT_RETRY=1             # retries per printer before failover, 0-10 (default 1)
export PRINT_JOB_RETENTION_DAYS=30  # job log retention, 1-365 (default 30)
export PRINT_PURGE_TENANT_IDS=   # comma-separated tenant UUIDs for the daily purge (empty: off)
go run ./cmd/print
```

An invalid `PRINT_RETRY`, `PRINT_JOB_RETENTION_DAYS` or `PRINT_PURGE_TENANT_IDS` stops the
process at startup.
`PRINT_RETRY` counts retries after the first attempt, so the default of 1 gives the two
attempts per printer of FR-32.13; the PRD's "default 3" conflicts with that (see
NOT-IMPLEMENTED.md).

Health: `GET /healthz` (liveness), `GET /readyz` (database ping). Contract: `api/openapi.yaml`.
Events: `events/asyncapi.yaml`.

## Test

```sh
go vet ./... && go test ./...
```

Domain tests (`internal/domain`) are pure and table-driven, with rapid property tests for
failover and for the GS1 label round trip (compose, then parse back). The ZPL golden test
compares the LP label with `testdata/golden/lp-label.zpl`; the item label is compared byte
for byte in `gs1label_test.go`. Transport tests decode the IPP request independently against
an `httptest` server and send PCL 6 to a local TCP listener. `migrations/rls_test.go` is the
static RLS check: every table has `tenant_id`, ENABLE and FORCE row-level security and the
`passage.tenant_id` policy. API tests use an in-memory store.
The document renderer has golden PDFs in `testdata/golden` (also `statement-of-account.pdf`, `balance-confirmation.pdf` and `invoice-with-template.pdf` for docgen), among them: `purchase-order.pdf`,
`invoice-unicode.pdf` (Polish, Czech, Greek and Cyrillic names, a wrapped description, an
additional party, and CJK text the fonts lack) and `packing-slip-3-pages.pdf` (page breaks with
repeated headers). `UPDATE_GOLDEN=1 go test ./internal/domain` rewrites them after an intended
layout change, and `DOCUMENT_PDF_DIR=<dir>` also writes each rendered PDF there to look at. Further
tests check the PDF structure (xref offsets, stream lengths, the embedded font programs parse),
and a rapid property test lays out random documents and checks that every piece of text stays
between the margins and above the footer and that every page is numbered. Store tests (`internal/store`) run against Postgres through
`platform/go/testkit` and are skipped without `PASSAGE_TEST_DATABASE_URL`; the RLS test also
needs `PASSAGE_TEST_APP_DATABASE_URL` (a role without BYPASSRLS).

## Error codes

| Code | HTTP | Meaning |
| --- | --- | --- |
| PRN-0001 | 400 | Request or template data invalid |
| PRN-0002 | 400 | Template code is not built in |
| PRN-0003 | 409 | Printer code already registered |
| PRN-0004 | 409 | Job already completed (terminal) |
| PRN-0005 | 404 | Print job not found |
| PRN-0006 | 409 | The queue already has a printer at this failover rank |
| PRN-0010 | 404 | Printer not found, or already retired |
| PRN-0011 | 404 | Routing rule not found |
| PRN-0007 | 201 (job FAILED) | The label does not fit the media of any device in the queue |
| PRN-0008 | 422 | A business document cannot be rendered; the detail names the field |
| PRN-0009 | 422 | A business document has more than 500 lines |
| PRN-0012 | 404 | Document template not found |
| PRN-0013 | 404 | Text block not found, or it has no version for the language and none in English |
| PRN-0014 | 422 | A text block has a placeholder the document gave no value for |
| PRN-0015 | 404 | Output rule not found |
| PRN-0016 | 503 | Templates, text blocks and output rules are not available (no store configured) |
| MOB-0111 | 201 (job FAILED) | Printer unreachable after the attempts |
| MOB-0112 | 422 / 201 (job FAILED) | No routing rule matches the document, or the queue has no devices |
| idempotency_key_reuse | 422 | Same Idempotency-Key with a different request body |

Problem responses are RFC 9457 with `type` `https://passage.dev/problems/<code>`.

## Dependencies

The platform kernel (`platform/go`) and its transitive dependencies (chi, pgx/v5), plus two
`golang.org/x` modules for the document renderer: `golang.org/x/image` for the Go Regular and Go
Bold font data and the `sfnt` parser (character to glyph lookup), and `golang.org/x/text` for
Unicode normalization. Both are compiled in; nothing is fetched at run time.

The Go fonts are by Bigelow & Holmes and are distributed under a BSD-style 3-clause licence,
the same terms as the Go project's software. The licence text is in
`third_party/go-fonts-LICENSE` and is copied into the container image at `/licenses`, because
the binary and every PDF it renders contain (subsets of) the fonts.
