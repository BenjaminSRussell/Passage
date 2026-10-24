# Batch service

Wave 04 owns the event relay and the checkpointed batch runner.

`internal/jobs.Runner` splits a finite row range into deterministic partitions.
Each row effect, idempotency marker, and partition checkpoint is committed in
one store transaction. `PgStore` enforces tenant RLS; `MemoryStore` supports
tests and local dry runs.

The 1,000,000-row acceptance test kills the first run at row 400,000, resumes
from persisted partition checkpoints, and asserts every synthetic row was
applied exactly once.

## Test

```sh
go vet ./...
go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...
```
