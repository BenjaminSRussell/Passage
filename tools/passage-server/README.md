# passage-server

The single Windows binary of Passage (ADR-0004, issue #607). It supervises the service executables in `<install>\bin` as
child processes on loopback ports, runs the built-in gateway (JWT verification, OPA decision, path routing, the embedded web
app) and runs as the `Passage` Windows service.

```
passage-server serve      # run in the foreground (console) or, under the Service Control Manager, as the service
passage-server migrate    # start every service once with MIGRATE_ON_START=true, wait until ready, stop
passage-server doctor     # dependency checks; -json for machine output; exit 1 on any FAIL
passage-server version
```

Configuration: `%ProgramData%\Passage\passage.yaml` (or `PASSAGE_CONFIG`, `PASSAGE_DATA_DIR` off Windows), `conf.d\*.yaml`, then registry
values (Windows). The property and key names are in [docs/ops/windows-install.md](../../docs/ops/windows-install.md).

Build (Linux or Windows):

```
cd apps/web && npm ci && npm run build
cp -r apps/web/dist tools/passage-server/internal/web/dist        # not committed
cd tools/passage-server
GOOS=windows GOARCH=amd64 go build -tags webdist -ldflags "-s -w -X main.version=1.0.0" -o passage-server.exe .
GOFIPS140=v1.0.0 ...                                                # FIPS variant (set requireFips in passage.yaml)
```

Without `-tags webdist` the gateway serves the API only (the build and tests need no web build). Windows-only code
(`service_windows.go`, `policy_windows.go`, `job_windows.go`) sits behind build tags, so Linux builds and tests run without
`golang.org/x/sys/windows`. Tests use fakes for the process runner, Postgres, HTTP and network dials;
`TestPgInspectorAgainstPostgres` uses `PASSAGE_TEST_DATABASE_URL` when set.
