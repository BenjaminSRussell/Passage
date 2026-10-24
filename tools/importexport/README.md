# importexport

CSV import/export against any entity metadata shape (wave 5). Excel opens the CSV. XLSX is listed in the wave ledger as a follow-up because `apps/web` and this tool cannot take extra dependencies in this slice.

```
go test ./...
```

The catalog is a `Field`/`Entity` interface so wave 3 can supply live definitions without this tool importing `services/metadata`.
