# migration

Data migration framework for Passage (wave 10). Slice 1 ships the pure domain engine in `internal/domain`: the declarative mapping DSL, the extract-map-validate-load-reconcile pipeline behind interfaces with in-package fakes, idempotent re-run, dry run, exception queue with re-run, run ledger with lineage, and reconciliation. Everything else is listed in `NOT-IMPLEMENTED.md`.

Test: `go vet ./... && go test ./...`.

## Mapping DSL

Mappings are JSON (a subset of the YAML the architecture names; no YAML library is on the allowed dependency list, so the stored text stays valid if a parser is approved later).

```json
{"entity":"customer","version":1,"fields":[
  {"source":"CustGroup","target":"segment","type":"string","enum":{"10":"retail"},"required":true},
  {"source":"CreditMax","target":"creditLimit","type":"decimal","default":"0"}]}
```

Types: `string`, `int`, `decimal`, `date`, `bool`. Absent, null and blank values are treated alike: default fill, else `required` rejects, else the field is omitted. Decimals never pass through float; scale above 6 or magnitude at or above 1e14 is rejected, not rounded. Reject details carry the field name and code, never the value.

## Error codes

| Code | Meaning |
|---|---|
| MIG-MAP-INVALID | mapping document unknown field, wrong type, unknown type, duplicate target or bad default |
| MIG-MAP-REQUIRED | required source value absent |
| MIG-MAP-COERCE | value not convertible to the declared type |
| MIG-MAP-ENUM | value has no enum mapping |
| MIG-MAP-RANGE | decimal scale above 6 or magnitude at or above 1e14 |
| MIG-ROW-DUPLICATE-KEY | source key repeated within one extract |
| MIG-LOAD-FAILED | target loader error for the batch |

## Pipeline rules

- Idempotency key is `sha256(source_system|source_entity|source_key|content_hash)`, independent of the run.
- The target id is minted once and stored in the xref before the loader is called; the content hash is recorded only after a successful load, so a failed row retries with the same target id.
- Loads go through `TargetLoader` in batches of at most 1000; no direct SQL into target contexts.
- Source rows missing since the last run are reported in `Report.Absent`, never deleted.
- `Rerun` reprocesses only the keys in the previous report's exception queue and skips reconciliation.

## Dependencies

`github.com/shopspring/decimal v1.4.0` (exact money arithmetic) and `pgregory.net/rapid v1.1.0` (property tests), both on the platform allow-list.

## Unverified

AX and SAP table mappings (DirParty, CustTable, VendTable, InventTable, EcoResProduct, BOM, Route, KNA1, LFA1, MARA, MAST, STKO, PLKO) are not shipped; they must be verified against coverage oracles that are not available in this repository.
