# Services

One directory per bounded context. Go services follow the layout in
[docs/engineering/CONVENTIONS.md](../docs/engineering/CONVENTIONS.md); `-ts` and `-py`
suffixes mark TypeScript and Python services.

Minimal Go service skeleton (copy, do not share code between services):

```
services/<name>/
  go.mod                 module github.com/BenjaminSRussell/passage/services/<name>
                         replace github.com/BenjaminSRussell/passage/platform => ../../platform/go
  cmd/<name>/main.go
  internal/domain/
  internal/store/
  internal/api/
  internal/events/
  migrations/0001_init.sql
  api/openapi.yaml
  events/asyncapi.yaml
  charts/<name>/
  README.md
```

`main.go` shape:

```go
func main() {
    ctx := context.Background()
    pool, err := dbx.Connect(ctx, os.Getenv("DATABASE_URL"))
    if err != nil { log.Fatal(err) }
    if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil { log.Fatal(err) }
    r := httpx.NewRouter()
    api.Register(r, store.New(pool))
    log.Fatal(httpx.Serve(ctx, ":8080", r))
}
```
