# metadata (PSG-3)

Metadata kernel for Passage entities, fields, extensions, generated contracts, decision tables and usage reporting. Entity definitions are published as immutable tenant-scoped versions. Customer extensions produce a new additive effective version; extension fields remain in metadata-backed JSON records, so applying one does not lock or rewrite a generated business table.

## Run

```
go run ./cmd/entitygen -in testdata/entities/_reference -out /tmp/gen
go run ./cmd/entitygen -in testdata/entities/_reference -out testdata/golden -check
UPDATE_GOLDEN=1 go test ./internal/gen

DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/metadata
```

The CLI lives here rather than in `tools/entitygen` because the root Makefile only vets and tests Go modules under `platform/go` and `services`.

Per entity it writes:

- `<Name>.sql`: `dbx.Migrate`-compatible Postgres DDL with tenant and legal-entity RLS
- `<Name>.openapi.json`: OpenAPI 3.1 CRUD contract
- `<Name>.client.ts`: typed fetch client
- `<Name>.schema.json`: JSON Schema 2020-12 validation and form contract
- `<Name>.domain.go`, `<Name>.repository.go`, `<Name>.handlers.go`: generated Go runtime contracts

The registry API publishes and diffs definitions, atomically applies customer extensions, returns the effective JSON Schema, validates generated records, and evaluates effective-dated decision tables with first/collect hit policies and matched-row explanations. `apps/web/src/modules/metadata` renders forms from the same effective schema.

`internal/usage` collects named-user/principal dimensions, API request and row counts, and writes stable CSV licensing input. It never enforces licensing.

## Dependencies

- `gopkg.in/yaml.v3` v3.0.1 parses the YAML entity definitions; the standard library has no YAML parser.
- `github.com/go-chi/chi/v5` and `github.com/jackc/pgx/v5` are existing platform dependencies imported directly by the registry API and Postgres store.

## CDC

Migration `0004_cdc_publication` pre-creates the `passage_outbox_metadata` publication (the connector runs with `publication.autocreate.mode: disabled`, so the CDC role needs no CREATE or ownership) and `0006_cdc_role_grants` gives the `debezium_cdc` role SELECT on `outbox` when that role exists. The migrating role needs CREATE on the database and ownership of `outbox`; the Debezium connector in `deploy/kafka` must start after migrations 0004 and 0006. The database, role and `wal_level=logical` come from the `deploy/postgres` chart.

## Duties

| Duty | Access |
|---|---|
| `metadata.read` | Effective schemas and rule evaluation |
| `metadata.write` | Definition publishing and extension apply |
| `metadata.data.read` | Generated record reads |
| `metadata.data.write` | Generated record creates |

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
| MET-0101 | invalid extension identity or target |
| MET-0102 | extension field collision |
| MET-0103 | invalid extension enum value or code |
| MET-0201 | breaking change without `breaking: true` |
| MET-0301 | invalid decision table, condition or typed input |
| MET-0304 | no effective decision row matched |
| MET-0401 | generated record failed validation |
| MET-0404 | entity definition was not found |

## Tests

```
go test ./...
go vet ./...
helm lint charts/metadata --set image.tag=test
cd ../../apps/web && npm test -- --run src/modules/metadata
```

The generated API load contract starts three handler nodes over a shared registry and asserts 1,000 simple reads/second with p95 below 50 ms.
