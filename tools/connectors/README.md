# connectors

Legacy source specs for the migration framework (wave 10). `AX()` lists the AX 2012 and D365 F&O master-data tables (DirParty, CustTable, VendTable, InventTable, EcoResProduct, BOM, Route); `SAP()` lists the SAP ECC and S/4 tables (KNA1, LFA1, MARA, MAST, STKO, PLKO). Each `Entity` names its key columns, the columns extracted and the delta cursor column (AX `ModifiedDateTime`, SAP `AEDAT`, `LAEDA` or `ERDAT`).

`RowSource` returns rows as `Record{Key, Fields}`. The only implementation, `FlatFiles`, reads `<Table>.csv` extracts with a header row from an `fs.FS`, keeps only rows whose cursor is after `since`, and returns an error naming the table and line for a short row or an empty key, without partial rows.

The column lists come from public data dictionaries and are flagged `Unverified`: they must be checked against coverage oracles that are not available here. ODBC, SAP RFC and OData drivers are not included (extraction method is open question OQ-07; no new dependencies). Credentials never appear in this module; sources are configured through Vault-backed secrets in the Helm sub-chart.

Test: `cd tools/connectors && go vet ./... && go test ./...`. The root Makefile does not discover this module.
