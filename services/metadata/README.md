# metadata (PSG-3 slice 1)

Entity definition language, code generator and compatibility classifier for the metadata kernel. Slice 1 is a library plus an offline CLI; the registry service, extension apply, chart and web module are slice 2 (see `NOT-IMPLEMENTED.md`).

## Run

```
go run ./cmd/entitygen -in testdata/entities/_reference -out /tmp/gen
go run ./cmd/entitygen -in testdata/entities/_reference -out testdata/golden -check
UPDATE_GOLDEN=1 go test ./internal/gen
```

The CLI lives here rather than in `tools/entitygen` because the root Makefile only vets and tests Go modules under `platform/go` and `services`.

Per entity it writes `<Name>.sql` (plain `NNNN_name.sql`-style DDL for `dbx.Migrate`, RLS from `dbx.RLSPolicy` and `dbx.LegalEntityScopePolicy`), `<Name>.openapi.json` (OpenAPI 3.1) and `<Name>.client.ts` (fetch-based typed client). Table names are the plural snake_case of the entity name. Money columns are `numeric(20,6)` plus `<field>_currency char(3)` per the architecture.

## Dependency

`gopkg.in/yaml.v3` v3.0.1: parses the YAML entity definitions FR-3.1 requires; the standard library has no YAML parser. It is already in the platform module graph.

## Error codes

| Code | Meaning |
|---|---|
| MET-0001 | malformed, empty or wrong-typed YAML |
| MET-0002 | unknown key |
| MET-0003 | identifier does not match its pattern |
| MET-0004 | unknown type, table_group, hook or attribute value; no fields |
| MET-0005 | invalid default |
| MET-0006 | string length or decimal precision/scale out of range |
| MET-0007 | enum missing, duplicate, empty or code out of range |
| MET-0008 | title field is not a declared string field |
| MET-0009 | duplicate or reserved field name, bad relation or index reference |
| MET-0201 | breaking change without `breaking: true` |
