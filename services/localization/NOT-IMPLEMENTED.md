# Not implemented in PSG-48 slice 1

Slice 1 ships the pack loader and validator, the seven first country packs as metadata, install checks,
tax-id validators, the UK MTD VAT statutory check, the service skeleton and the OpenAPI contract.

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC1: each pack ships a statutory fixture validated against the regulator schema | partial | only UK (`UK_MTD_VAT`) has a fixture, checked against HMRC's VAT (MTD) API request schema and its two submission rules in code; US, DE, FR, IN, MX and CA need fixtures and the regulators' XSD/JSON schemas (later slice of PSG-48, with the ER designer) |
| AC-48.14: GENERATE → VALIDATE → SEND to the HMRC sandbox with a receipt id | later slice of PSG-48 | the electronic messaging lifecycle and the HMRC adapter (OAuth, fraud-prevention headers) are not built |
| Tax-id check digits for GB VAT, DE USt-IdNr, FR TVA | later slice of PSG-48 | those validators check the format only; GSTIN verifies its check character |
| FR-48.1: install per legal entity, primary pack store, upgrade preview | later slice of PSG-48 | needs the store, migrations and `pack.version_pin`; the install check endpoint is the pure rule set |
| FR-48.1: tax rules, bank file generation (NACHA, BACS, SEPA, NEFT/RTGS, SPEI, EFT), holiday calendars, document layouts | later slice of PSG-48 | packs carry the declarations only; generators and calendars are wave 6, 19 and 46 work |
| FR-48.2: statutory outputs for US, DE, FR, IN, MX, CA | later slice of PSG-48 | output formats are electronic reporting (`er_format`) and the messaging lifecycle, owned by the ereporting work |
| FR-48.2: e-invoice mandates (CFDI 4.0, IRP, Peppol) | handoff | clearance adapters are wave 35 |
| FR-48.27: India pack depth (GST components, TDS/TCS sections, challans, 26Q/27Q, 16A, 26AS/AIS) | later slice of PSG-48 | only GSTIN validation and the IN pack metadata exist |
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
