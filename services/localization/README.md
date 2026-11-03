# localization (PSG-48, wave 48, slices 1 and 2)

Country localization for Passage. Packs are metadata, never code forks (FR-48.1). Slice 1 loads the
seven first country packs from `countrypacks/` at start-up, validates them, and exposes:

- the pack catalogue and per-pack detail;
- install checks (country match, one primary pack per legal entity, fiscal calendar, pack minimum retention);
- tax-id validation for the pack schemes (GB VAT, DE USt-IdNr, FR TVA, IN GSTIN with check character, MX RFC, CA BN, US EIN);
- the UK MTD VAT return check against HMRC's VAT (MTD) API 1.0 submit-return body (`periodKey`,
  `vatDueSales` … `totalAcquisitionsExVAT`, `finalised`): every box present within HMRC's range and scale
  (boxes 1 to 4 signed with pence, box 5 non-negative, boxes 6 to 9 signed whole pounds), `finalised` true,
  box 3 = box 1 + box 2 and box 5 = |box 3 - box 4|. Amounts are decimals, never floats;
- (slice 2) the India TDS calculation for one payment, `POST /v1/localization/in/tds:withhold` (see Wave reference);
- (Wave 48 proof, AC1) a statutory validator per pack, `domain.ValidateStatutory(report, fileName, raw)`, and
  one statutory-report fixture per pack under `countrypacks/<code>/fixtures/` (fx-statutory-7). No endpoint;
  the validators are pure and run in the tests. See "Statutory fixtures" below.

The compliance workspace (`apps/web/src/modules/compliance`, routes `/compliance/packs` and
`/compliance/packs/:code`) lists the packs, shows a pack's contents and runs the install and tax-id checks;
`/compliance/tds` is the India TDS calculator.

Installs per legal entity, pack versions and upgrade preview need the store (later slice). The service is
stateless in this slice and needs no database.

## Run and test

```
COUNTRY_PACKS_DIR=../../countrypacks go run ./cmd/localization
go vet ./... && go test ./...
```

The tests read `countrypacks/` directly; they do not need a database. The service refuses to start when
any pack fails validation, or when the directory does not hold the seven packs.

## Statutory fixtures (AC1, fx-statutory-7)

| Pack | Report | Fixture | Checked against |
|---|---|---|---|
| UK | `UK_MTD_VAT` | `uk/fixtures/mtd-vat-26a1.json` | HMRC VAT (MTD) API 1.0 submit-return body and its two submission rules |
| US | `US_1099_FIRE` | `us/fixtures/fire-1099nec-2025.txt` | IRS Publication 1220 (Rev. 05-2026, TY 2025) T/A/B/C/F record layouts for Form 1099-NEC: 750 positions, blank and numeric fields, sequence numbers, payee counts, C control totals |
| DE | `DE_UStVA` | `de/fixtures/ustva-2025-01.xml` | the ELSTER UStVA Nutzdaten conventions: Zeitraum, 13-digit Steuernummer, whole-euro bases, Kz35/Kz36 pair, Kz83 recomputed |
| FR | `FR_FEC` | `fr/fixtures/732829320FEC20251231.txt` | article A47 A-1 LPF: file name with SIREN check digit, 18 named columns, dates, comma amounts, paired columns, validation order, balanced entries |
| IN | `IN_GSTR1` | `in/fixtures/gstr1-082025.json` | GSTN offline-tool GSTR-1 JSON, b2b and hsn: GSTIN check characters, period, invoice number and date, place of supply, rate slabs, IGST or CGST = SGST, HSN summary reconciled |
| MX | `MX_CFDI40` | `mx/fixtures/cfdi40-ingreso.xml` | SAT cfdv40.xsd and tdCFDI.xsd patterns and the Anexo 20 arithmetic; also SAT's own XSD with `xmllint` when `PASSAGE_SAT_CFDI_XSD` points at a local copy (`TestMXFixtureAgainstSATXSD`) |
| CA | `CA_GST34` | `ca/fixtures/gst34-2025q3.xml` | the GST34 form: BN with Luhn check digit and RT account, period, line 101 whole dollars, 105 = 103 + 104, 108 = 106 + 107, 109 = 105 - 108 |
| MX | `MX_DIOT` | `mx/fixtures/diot-2025-01.txt` | pipe-delimited SAT DIOT lines: tipoTercero/tipoOperacion catalogues, RFC shape, date and mod-11 homoclave, foreign-supplier fields, whole-peso amounts, IVA acreditable at most 16% of the base |
| FR | `FR_CA3` | `fr/fixtures/ca3-2025-01.csv` | DGFiP 3310-CA3 boxes as `code;value`: SIREN Luhn, period, whole euros, 16 = 08 + 09 + 9B, 23 = 19 + 20 + 22, 28 and 25 as the positive and negative difference |
| IN | `IN_26Q` | `in/fixtures/26q-2025-26-q2.txt` | caret-delimited FH/BH/CD/DD records: TAN and PAN formats, FY and quarter, dates inside the quarter, BSR code, section, record counts, deducted = deposited TDS = batch total |

`TestEveryPackShipsAValidStatutoryFixture` validates the seven fixtures with 0 errors;
`stat_mutation_test.go` holds about 120 adversarial mutations (one regulator-rejected change each, plus
legal variations that must pass). `internal/domain/testdata/statutory_ref.py` (stdlib, `python3 -I`)
rebuilds the FIRE file from Pub 1220 positions and recomputes every derived value of the other fixtures
without the Go code; `TestStatutoryReferenceScriptAgrees` runs it. The validators encode the published
layouts; only MX has a regulator-published machine schema we can run, and its catalogue file (about 6 MB)
is not committed, so that check is opt-in. What stays open is in `NOT-IMPLEMENTED.md`.

## Deploy

`charts/localization` (`helm lint` clean): Deployment, Service, optional ServiceMonitor; no database. The
`Dockerfile` builds from the repository root and copies `countrypacks/` into the image at `/countrypacks`.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `COUNTRY_PACKS_DIR` | `countrypacks` | directory of `<code>/pack.json` |
| `HTTP_ADDR` / `ADMIN_ADDR` | `:8080` / `:9090` | listeners |

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `AUTH-0001` | 401 | missing or malformed tenant |
| `AUTH-0003` | 403 | missing duty `LocalizationAdmin` |
| `COM-0002` | 400 | request failed validation |
| `LOC-0501` | 422 | fiscal calendar has open periods incompatible with the pack |
| `LOC-0502` | 422 | retention below the statutory pack minimum |
| `LOC-0503` | 422 | pack country does not match the legal entity |
| `LOC-0504` | 422 | legal entity already has a primary pack |
| `LOC-0505` | 422 | invalid pack metadata or tax id |
| `LOC-0506` | 404 | no pack with that code |
| `ERP-0801` | 422 | statutory output fails its schema (`errors` lists fields) |
| `LOC-0510` | 422 | TDS section is not in the India table |
| `LOC-0511` | 422 | TDS amount not positive, year-to-date paid or taxed negative, taxed above paid, or certificate rate outside 0..100 |
| `LOC-0512` | 422 | 194C payee type missing or not INDIVIDUAL_HUF or COMPANY |

## Wave reference

PSG-48 slice 1 covers FR-48.1 (framework and validation), FR-48.2 (seven packs, metadata only) and the
UK part of AC1. Slice 2 adds the India TDS calculation (part of FR-48.27, AC-48.21):
`POST /v1/localization/in/tds:withhold` (duty `LocalizationAdmin`) for sections 194C, 194J, 194I, 194H and
194Q. Crossing an annual threshold also deducts on the year's earlier untaxed credits (`ytdPaid - ytdTaxed`);
without a PAN the rate is the highest of the section rate, twice that rate and 20% (5% for 194Q) under
206AA; a section 197 certificate lowers the rate only with a PAN; 194Q deducts on the excess over 50 lakh.
Amounts are decimal strings in INR, rounded to the paisa through `platform/money`. Rates and thresholds
are FY 2025-26 values and ASSUMED until tax counsel signs them off. See `NOT-IMPLEMENTED.md`.
