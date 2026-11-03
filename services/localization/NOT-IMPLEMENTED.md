# Not implemented in PSG-48 (after slice 2)

Slice 1 shipped the pack loader and validator, the seven first country packs as metadata, install checks,
tax-id validators, the UK MTD VAT statutory check, the service skeleton and the OpenAPI contract. Slice 2
adds the India TDS calculation (part of FR-48.27) with its endpoint, contract and the compliance workspace
calculator screen.

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC1: each pack ships a statutory fixture validated against the regulator schema | partial | all seven packs ship a fixture that validates with 0 errors (`TestEveryPackShipsAValidStatutoryFixture`, README "Statutory fixtures"). Decision: where no public XSD exists (DE, IN, CA, and the FR and MX text layouts), a validator built from the regulator's published layout satisfies AC1; only MX CFDI stays XSD-checked, opt-in through `PASSAGE_SAT_CFDI_XSD` because SAT's catalogue file is about 6 MB. `MX_DIOT`, `FR_CA3` and `IN_26Q` now have validators (`stat_mx_diot.go`, `stat_fr_ca3.go`, `stat_in_26q.go`), a valid fixture in the pack and an invalid one under `internal/domain/testdata/invalid/`. Open: (1) the three new validators encode a reduced column set, not every column of the regulator file: DIOT the eleven leading columns (SAT's 2025 batch layout is not machine-published), CA3 the boxes 01, 02, 08, 09, 9B, 16, 19, 20, 22, 23, 25, 28 as `code;value` rows without the EDI-TVA EDIFACT envelope, 26Q the FH, BH, CD and DD records with the identity, period and money columns, so the files are not yet loadable by SAT's loader, DGFiP's EDI-TVA gateway or the FVU; (2) AC-48.1 "when the format runs": the fixtures are outputs, not generated from postings, which needs the ER designer; (3) US covers 1099-NEC only |
| MX pack: `MX_CFDI40` in the statutory catalogue | added (Wave 48 proof) | the PRD's fx-statutory-7 names CFDI 4.0 for MX. The DIOT batch layout changed for 2025 periods and SAT publishes no machine schema for it, so the DIOT validator encodes a reduced layout (see AC1). CFDI stamping and PAC clearance stay with wave 35 |
| AC-48.14: GENERATE → VALIDATE → SEND to the HMRC sandbox with a receipt id | later slice of PSG-48 | the electronic messaging lifecycle and the HMRC adapter (OAuth, fraud-prevention headers) are not built |
| Tax-id check digits for GB VAT, DE USt-IdNr, FR TVA | later slice of PSG-48 | those validators check the format only; GSTIN verifies its check character |
| FR-48.1: install per legal entity, primary pack store, upgrade preview | later slice of PSG-48 | needs the store, migrations and `pack.version_pin`; the install check endpoint is the pure rule set |
| FR-48.1: tax rules, bank file generation (NACHA, BACS, SEPA, NEFT/RTGS, SPEI, EFT), holiday calendars, document layouts | later slice of PSG-48 | packs carry the declarations only; generators and calendars are wave 6, 19 and 46 work |
| FR-48.2: statutory outputs for US, DE, FR, IN, MX, CA | later slice of PSG-48 | output formats are electronic reporting (`er_format`) and the messaging lifecycle, owned by the ereporting work |
| FR-48.2: e-invoice mandates (CFDI 4.0, IRP, Peppol) | handoff | clearance adapters are wave 35 |
| FR-48.27: India pack depth remainder (GST components and place of supply, reverse charge, ITC and GSTR-2B reconciliation, TCS 206C(1H) and 27EQ, challan ITNS 281 generation with CIN, 26Q/27Q FVU file, Form 16A, 26AS/AIS import) | later slice of PSG-48 | slice 2 built the TDS calculation for 194C, 194J, 194I, 194H and 194Q (`internal/domain/in_tds.go`, `POST /v1/localization/in/tds:withhold`): thresholds with catch-up of the year's untaxed credits, 206AA without PAN, section 197 lower rates, the 194Q excess basis |
| FR-48.27: lower-deduction certificate limits and validity dates, 194J technical services (2%) and directors' fees, 194I plant and machinery (2%), 194Q buyer turnover test, 195 and 27Q for non-residents | later slice of PSG-48 | the calculation takes a certificate rate as given and models one rate per section (two for 194C) |
| FR-48.27: TDS rates and thresholds as pack data with effective dates; Income-tax Act 2025 section numbers | later slice of PSG-48 | the table is in code at FY 2025-26 values (Finance Act 2025), keyed by the 1961 Act numbers the PRD uses, and is ASSUMED until tax counsel signs it off; effective-dated rates belong with the wave-13 withholding engine (BR-48.10 handoff below) |
| AC-48.21: the posted invoice withholds 20% and the next 26Q FVU file includes the deduction | partial | the 20% (206AA) calculation is built and tested; posting is the wave-19 AP path and the FVU file is a later slice |
| FR-48.22, FR-48.31: US workforce compliance and statutory outputs | handoff | HR records are wave 42; outputs follow wave 42 fields |
| FR-48.28: HR legal content per pack | later slice of PSG-48 | needs the wave-42 country-specific field framework |
| FR-48.11: retention policies and purge | later slice of PSG-48 | the minimums are in the packs and `CheckRetention` enforces them; the `retention_policy` store and purge job are not built |
| FR-48.1 and BR-48.10: effective-dated tax rates | handoff | tax rates are wave 13 |
| Currency rounding, calendars and address formats per pack | handoff | wave 11 and wave 6 own these |
| AC6: AsyncAPI `events/asyncapi.yaml` | not applicable yet | the service publishes and consumes no events in slice 1; the file arrives with pack installs (`localization.pack.installed`) |
| Store, RLS and `DATABASE_URL` readiness | later slice of PSG-48 | slice 1 is stateless; readiness reports pack count, not Postgres |
| JWT validation and OPA duties | wave 2 | the gateway validates tokens; the service trusts tenant headers |
| `serviceCode` on 401 and 403 bodies | kernel handoff | tenant and authz middleware live in `platform/go` |
| Web: pack install and upgrade preview screens | later slice of PSG-48 | the compliance workspace lists packs, shows their contents and runs the install and tax-id checks; installs need the store |
| Electronic messages and ER designer | later slice of PSG-48 | owned by the ereporting work in this wave |
