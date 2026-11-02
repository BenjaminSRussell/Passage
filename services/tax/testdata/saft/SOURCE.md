# SAF-T test schema

`saft-2.00-tax-extract.xsd` is NOT the official OECD SAF-T 2.0 schema. It is a hand-written subset that keeps the OECD namespace (`urn:OECD:StandardAuditFile-Tax:2.00`), the `AuditFile` root, the `Header` fields the exporter fills (AuditFileVersion, AuditFileCountry, AuditFileDateCreated, Software*, Company, DefaultCurrencyCode, SelectionCriteria) and a `MasterFiles/TaxTable` of per-code period totals.

Why: the OECD publishes the full XSD on oecd.org, which answered HTTP 403 to every automated download when this was built, and no verbatim mirror of the 2.0 schema could be found. Nothing here was invented to look official: the file says what it is.

To close AC4 fully: download the official 2.0 XSD from the OECD guidance page (https://www.oecd.org/tax/forum-on-tax-administration/publications-and-products/guidance-for-the-standard-audit-file-tax-version-2-0.htm), commit it verbatim next to this file with its sha256 and retrieval date recorded here, point `TestExport_ValidatesAgainstTheSchema` at it, then extend the exporter with the GeneralLedgerEntries, Customers and Suppliers sections that need ledger and party data.
