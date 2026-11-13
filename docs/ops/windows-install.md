# Installing Passage on Windows Server

Passage ships for Windows as a signed MSI that installs one Windows Service, `Passage`, running `passage-server.exe`.
Kubernetes (ADR-0001) stays the primary target; this is the on-premises Windows channel. The decision and the
alternatives are in [ADR-0004](../adr/0004-windows-packaging.md). Not yet proven on a real Windows VM: see
[Known limits](#known-limits).

## What gets installed

| Item | Where |
|---|---|
| `passage-server.exe` and one `<service>.exe` per Go service | `C:\Program Files\Passage\bin` |
| Optional private runtimes (Node, Python) | `C:\Program Files\Passage\runtime\node`, `...\python` |
| Config, logs, service signing keys, certificates | `C:\ProgramData\Passage` (`passage.yaml`, `conf.d\`, `logs\`, `keys\`, `certs\`) |
| Install-time settings | `HKLM\SOFTWARE\Passage\Config` (string values named like the MSI properties) |
| Windows service | `Passage`, automatic start, virtual account `NT SERVICE\Passage`, restart on failure (30 s, three times, counter reset after a day) |
| Event Log source | `Passage` in the Application log (warnings and errors; the full log is in the files) |

`passage-server serve` starts every `bin\*.exe` as a child on `127.0.0.1` (HTTP port from 18100 upward, admin port from
19100 upward, in alphabetical order of service name), derives each service's `DATABASE_URL` from `PG_DSN` (database
`passage_<service>`), and serves the web app and `/api/v1/<service>/...` on `LISTEN`. Service-to-service signing keys are
created on first start in `keys\` (readable only by SYSTEM, Administrators and the service account).

## Prerequisites (external, not installed by the MSI)

| Dependency | Property | Notes |
|---|---|---|
| PostgreSQL 16 or later (Windows, Linux or Azure Database for PostgreSQL) | `PG_DSN`, `PG_APP_DSN` | Two roles, as in `deploy/postgres`. `PG_DSN` is the **owner** role (needs `CREATEDB`; it owns the tables, runs migrations and is used by `passage-server migrate` only). `PG_APP_DSN` is the `passage_app` role (`LOGIN NOSUPERUSER NOBYPASSRLS`, not an owner) that every running service connects as. `migrate` creates each missing `passage_<service>` database as the owner and grants `passage_app` connect, usage on `public` and read/write on the owner's tables (by default privileges); to do it by hand: `CREATE DATABASE passage_<service> OWNER <owner>;` then in that database `GRANT USAGE ON SCHEMA public TO passage_app; ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO passage_app; ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO passage_app;`. Doctor fails when the two URLs use the same role or the app role has `BYPASSRLS`. SQL Server is not supported. |
| OIDC issuer (Keycloak realm) | `OIDC_ISSUER` | Discovery document at `<issuer>/.well-known/openid-configuration` must report the same `issuer`. |
| OPA | `OPA_URL` | An OPA server loaded with `policies/authz`. Embedding is a follow-up. |
| Kafka-compatible broker with Debezium outbox | `KAFKA_BOOTSTRAP` | Not bundled (Kafka on Windows is not supported upstream). |
| Temporal | `TEMPORAL_HOSTPORT` | Existing cluster, or the optional `PassageTemporal` package of the bundle. |

For a small single-site install the bundle `PassageSetup.exe` can chain PostgreSQL (`InstallPostgres=1`) and Temporal
(`InstallTemporal=1`). Each prerequisite is its own package and can be installed on its own, for example
`msiexec /i PassageTemporal-<version>-x64.msi /qn`.

## MSI property table

Pass these on the `msiexec` command line (or as Intune/SCCM install arguments, or as bundle variables of the same
name). Values are stored under `HKLM\SOFTWARE\Passage\Config`; a property you omit on an upgrade keeps its stored value.
`PG_DSN` and `PG_APP_DSN` are hidden in the MSI log.

| Property | Default | Meaning |
|---|---|---|
| `PG_DSN` | none (required) | Postgres **owner** URL: creates and migrates the service databases (the database name in it is only where it connects first). A secret: the key is ACL-restricted, see [Secrets](#secrets). |
| `PG_APP_DSN` | none (required) | `passage_app` URL every running service uses (`DATABASE_URL`). A secret. Services started by `serve` get `MIGRATE_ON_START=false`; `migrate` starts them with the owner URL as `DATABASE_URL` and `MIGRATE_DATABASE_URL` and `MIGRATE_ON_START=true`. |
| `OIDC_ISSUER` | none (required) | Keycloak realm URL. |
| `OIDC_AUDIENCE` | `passage-api` | Required token audience. |
| `OIDC_JWKS_URL` | issuer default | Set when the JWKS is not at the issuer's discovered address. |
| `TENANT_ID` | empty | The tenant id the gateway accepts; empty means the verifier's default rule. |
| `OPA_URL` | none (required) | OPA base URL. |
| `KAFKA_BOOTSTRAP` | none (required) | Comma-separated bootstrap servers; services receive it as `KAFKA_BROKERS`. |
| `TEMPORAL_HOSTPORT` | none (required) | Temporal frontend `host:port`; services receive it as `TEMPORAL_ADDRESS`. |
| `LISTEN` | `127.0.0.1:8443` | Gateway address. Anything that is not loopback needs `TLS_CERT_FILE` and `TLS_KEY_FILE`: otherwise `passage-server` refuses to start (and doctor fails). |
| `ALLOW_PLAINTEXT` | `0` | `1` lets a non-loopback `LISTEN` serve plain HTTP, only for a TLS-terminating proxy in front. |
| `LISTEN_PORT` | `8443` | Port used for the firewall rule; keep equal to the port in `LISTEN`. |
| `OPEN_FIREWALL` | `1` | `0` skips the inbound firewall rule. The rule is only created when `LISTEN` is not loopback. |
| `TLS_CERT_FILE`, `TLS_KEY_FILE` | none | PEM certificate (with chain) and private key. |
| `CLOUD_PROFILE` | none | `PASSAGE_CLOUD_PROFILE` for every child (`commercial`, `gcc`, `gcc-high`, `dod`, `china`, overlays) (#599). |
| `REQUIRE_FIPS` | `0` (`1` in the FIPS MSI, a separate product with its own upgrade code) | `1` makes the server refuse to start unless `crypto/fips140.Enabled()`. |
| `SERVICE_ACCOUNT` | `NT SERVICE\Passage` | Virtual account; pass `DOMAIN\gmsa$` for a group managed service account. `SERVICE_ACCOUNT`, `LISTEN_PORT` and `OPEN_FIREWALL` are remembered like the other properties. |
| `SKIP_DOCTOR` | `0` | `1` skips the dependency checks only. |
| `SKIP_MIGRATE` | `0` | `1` skips the migration step only. To stage an install before its dependencies exist, set both to 1. Doctor and migrate run on a first install, a major upgrade and a repair, not on modify. |
| `BACKUP_CONFIRMED` | `0` | An upgrade is refused unless this is `1`: take the backup first (below). |
| `REMOVE_DATA` | `0` | On uninstall, `1` also deletes `%ProgramData%\Passage`. By default config, keys, certificates and logs stay. |
| `ADDLOCAL` | Core | Add `NodeServices` and/or `PythonServices` to install the optional runtimes. |

Other `passage.yaml` keys (`basePort`, `databasePrefix`, `appDsn`, `services:`, `features:`) are documented in
`tools/passage-server/config.go`. Precedence, lowest to highest: `passage.yaml`, `conf.d\*.yaml` services,
`HKLM\SOFTWARE\Passage\Config`, `HKLM\SOFTWARE\Policies\Passage`. The policy key accepts the same value names as the
property table, so a GPO or an Intune registry (OMA-URI/ADMX or Settings catalog) policy can set or override them
without reinstalling; restart the service afterwards.

Example silent install:

```
msiexec /i Passage-1.0.0-x64.msi /qn /l*v C:\Windows\Temp\passage-install.log ^
  PG_DSN="postgres://passage_owner:SECRET@db01:5432/postgres?sslmode=verify-full" ^
  PG_APP_DSN="postgres://passage_app:SECRET2@db01:5432/postgres?sslmode=verify-full" ^
  OIDC_ISSUER="https://id.example.com/realms/passage" OPA_URL="http://opa01:8181" ^
  KAFKA_BOOTSTRAP="k1:9092,k2:9092" TEMPORAL_HOSTPORT="temporal01:7233" ^
  LISTEN="0.0.0.0:8443" LISTEN_PORT=8443 TLS_CERT_FILE="C:\ProgramData\Passage\certs\passage.pem" TLS_KEY_FILE="C:\ProgramData\Passage\certs\passage.key"
```

The installer runs `passage-server doctor` after the files and the service are in place. A failing check aborts the
install and rolls it back; the MSI log (`/l*v`) holds one line per problem, for example
`FAIL postgres-role: role passage_app has BYPASSRLS or SUPERUSER ...` or `FAIL oidc: issuer metadata unreachable ...`.
Run `"C:\Program Files\Passage\bin\passage-server.exe" doctor` (add `-json` for machine output) at any time; it also
reports which optional features are active (`feature-node`, `feature-python`), whether the web app is embedded and
whether FIPS mode is on. Exit code 0 means no check failed (warnings such as "not migrated yet" do not fail it).

## Optional TypeScript and Python services

`ADDLOCAL=NodeServices` installs the private Node runtime with the workflow-ts (Temporal worker, x64 only) and agents-ts bundles under
`runtime\node\apps\`; `ADDLOCAL=PythonServices` installs the embeddable Python with routing-py and scheduler-py in
`runtime\python\Lib\site-packages`. Each feature drops its service list into `%ProgramData%\Passage\conf.d` (`node.yaml`,
`python.yaml`), so removing the feature removes its services. The start commands, with the ports the supervisor assigns, are
printed by `passage-server services` (JSON; database URLs left out). The windows-msi workflow builds these bundles
(`deploy/windows/scripts/Build-ServiceBundles.ps1`) and starts each of them with exactly those commands before packaging
(`Test-ServiceBundles.ps1`): the Python services must answer `/healthz`, workflow-ts must reach its Temporal connect, and agents-ts's
module graph must load.

When a feature is off, the web app hides its screens: `passage-server` serves `/installation.json` (`{"features":{"node":..,"python":..}}`,
no sign-in needed, it names only the installed runtimes) and the Windows web build reads it. Node off hides the copilot, the tool catalog
and the agent kill switch; Python off hides transport route plans and load plans.

## Deployment tools

All three consume the MSI natively; sign it and distribute the same file.

**Intune (Win32 app or LOB).** Package `Passage-<version>-x64.msi` as a Line-of-business app, or wrap it with the Win32
content prep tool for the bundle. Install command: the `msiexec /i ... /qn` line above (properties in the command line,
or use a PowerShell script that reads secrets from Key Vault). Uninstall: `msiexec /x {ProductCode} /qn`. Detection rule:
the MSI product code, or the registry value `HKLM\SOFTWARE\Passage\FirewallRule`. Requirements: 64-bit Windows Server 2019 or later,
arm64 for the arm64 package. Set `OIDC_ISSUER`, `OPA_URL` and the rest with an Intune registry policy on
`HKLM\SOFTWARE\Policies\Passage` instead of the install line when you want to change them without reinstalling.

**Configuration Manager (SCCM).** Create an Application with the MSI deployment type. Installation program as above;
detection method is the Windows Installer product code (SCCM reads it from the MSI). Use a Configuration Item or Baseline to
keep the policy registry values in place. Task sequences can pass the properties as variables.

**Group Policy software installation.** Assign the MSI to computers (not users) from a share readable by the computer
accounts. GPO software installation cannot pass properties, so deliver them first with Group Policy Preferences
(Registry) into `HKLM\SOFTWARE\Policies\Passage`, plus `PG_DSN` through a secret-safe channel; or use a transform (.mst) built
with the properties, and keep it ACL-restricted because it would hold the DSN. `SKIP_DOCTOR=1` is useful for the first
assignment if the dependencies come up later; run `passage-server doctor` and start the service afterwards.

## Ports and firewall

| Port | Direction | What | Opened by |
|---|---|---|---|
| `LISTEN` (default 8443/tcp) | inbound | Gateway: web app and `/api` | The MSI (rule "Passage gateway", all profiles; `OPEN_FIREWALL=0` to skip) |
| 18100 + n / 19100 + n (loopback only) | local | Service HTTP and admin ports of child `n` | Not opened; bound to 127.0.0.1 |
| 5432/tcp | outbound | Postgres | Your network |
| 9092/tcp (broker ports) | outbound | Kafka | Your network |
| 7233/tcp | outbound (or local) | Temporal frontend | Your network |
| 8181/tcp | outbound | OPA | Your network |
| 443 or 8443/tcp | outbound | OIDC issuer (Keycloak) | Your network |

If services are numerous, `basePort` in `passage.yaml` moves the loopback range; check it does not collide with other software
(doctor reports a busy gateway port but not the child ports; a collision shows as a child that keeps restarting in the log).

## Certificates

The gateway needs a PEM certificate chain and key readable by the service account (put them in `%ProgramData%\Passage\certs`,
which the installer ACL-restricts, and set `TLS_CERT_FILE` and `TLS_KEY_FILE`). Export a certificate from the Windows store or
from your CA to PEM with `openssl pkcs12` or `certutil`. Doctor fails when the pair does not load. Renew by replacing the files and
restarting the service (`Restart-Service Passage`); see [runbooks/certificate-expiry.md](runbooks/certificate-expiry.md). Reading a
certificate straight from the Windows store (by thumbprint) is not supported yet. Trust for the OIDC issuer's CA uses the Windows
trust store (Go on Windows verifies against it).

### Secrets

`PG_DSN` is stored in `HKLM\SOFTWARE\Passage\Config`. After the service exists the installer restricts that key and the data folder
to SYSTEM, Administrators and the service account (`scripts\Set-PassageAcl.ps1`). Prefer a database role with a password stored only
there, TLS to Postgres (`sslmode=verify-full`), and rotate by setting a new `PG_DSN` (policy key or reinstall with the property) and
restarting the service. The per-service signing keys in `keys\` are generated on first start; back them up with the config.

## Logs

| Log | Location |
|---|---|
| passage-server (service) | `%ProgramData%\Passage\logs\Passage-service.log` (JSON lines, 50 MB x 5 files) |
| Each service | `%ProgramData%\Passage\logs\<service>.log` (same rotation) |
| Warnings and errors of the supervisor | Event Viewer, Windows Logs, Application, source `Passage` |
| MSI install | the file given to `msiexec /l*v` |
| Readiness | `https://<host>:<port>/readyz` returns `{"ready":true|false}` (503 when not ready) with no per-service detail; `/healthz` is liveness. Per-service state is in the logs and in `doctor`. |

A required service that exits more than five times in five minutes makes `passage-server` stop with a non-zero exit code; the service's failure
actions are set to run on a non-zero exit as well as on a crash, so the service restarts after 30 seconds. Children are stopped hard on Windows
(there is no console to deliver a graceful signal to); the services commit in database transactions and replay from the outbox. Optional services (the Node and Python features) failing do not stop it.

## Upgrade

1. Back up (below) and run `passage-server doctor` on the current version.
2. Run the new MSI with the same properties plus `BACKUP_CONFIRMED=1`. Properties you leave out keep their stored values.
3. The installer stops the service, lays down the new files, runs `doctor`, runs `migrate` (starts each service once with
   `MIGRATE_ON_START=true`, waits until all report ready, stops them) and starts the service. Downtime is the stop to start window;
   the target is under 2 minutes on the reference VM and has not been measured yet.
4. If `doctor` or `migrate` fails, Windows Installer rolls back: the previous files return and the old service starts again.
   Migrations are expand/contract-safe, so the previous version keeps working with the expanded schema. To go back after a
   successful upgrade, uninstall and install the old MSI and restore the databases from the backup taken in step 1.

## Backup and restore

Back up, in one maintenance window: every `passage_<service>` database (`pg_dump -Fc` per database, or your Postgres
backup product; see `tools/tenant-restore` for per-tenant restore and `docs/ops/runbooks/backup-drill-failure.md`), and the
`%ProgramData%\Passage` folder (`passage.yaml`, `conf.d`, `keys`, `certs`) plus an export of `HKLM\SOFTWARE\Passage` and
`HKLM\SOFTWARE\Policies\Passage`. Kafka offsets and Temporal history follow your own procedures for those systems.

Restore: stop the service (`Stop-Service Passage`); restore the databases (`pg_restore --clean`), the `Passage` data folder
and the registry export; start the service. Restoring the `keys` folder matters: service tokens are signed with those keys. Run
`passage-server doctor` after a restore.

## Uninstall

`msiexec /x Passage-<version>-x64.msi /qn` stops and removes the service and the program files and leaves
`%ProgramData%\Passage` (and the databases). Add `REMOVE_DATA=1` to delete the data folder as well. Databases are never dropped.

## FIPS variant

`Passage-<version>-fips-x64.msi` (and arm64) is built with `GOFIPS140=v1.0.0` and sets `REQUIRE_FIPS=1`. Enable the Windows policy
(`HKLM\SYSTEM\CurrentControlSet\Control\Lsa\FIPSAlgorithmPolicy`, "System cryptography: use FIPS compliant algorithms") and make sure
`GODEBUG=fips140=off` is not set. The server and each service refuse to start when FIPS mode is off; `doctor` reports it. Use it with
`CLOUD_PROFILE=gcc` (or another FIPS profile). Only x64 and arm64 are built (the Go module does not support 32-bit Windows).

## Releases: signing, SBOM, CI secrets

`.github/workflows/windows-msi.yml` has two build jobs on `windows-latest`. `build` has no secrets and no `id-token`: it cross-builds, packages unsigned
and lists unsigned files; on a pull request it builds only amd64 (when `deploy/windows`, `tools/passage-server`, `platform/go`, `services/*/cmd` or the workflow change), on tags and manual runs all four
legs (amd64 and arm64, standard and FIPS). `release` runs only for `v*` tags and manual runs with `sign: true`, in the `platform-release` environment: it signs the executables
before packaging, then the MSIs and the bundle, fails on any unsigned or untimestamped binary, writes the SBOM and the cosign bundles and uploads them. The secrets are mapped only onto
the signing steps and `id-token: write` exists only in that job.

Secrets the workflow references (none is defined in the repository; put them in the `platform-release` environment):

| Secret | Content |
|---|---|
| `WINDOWS_SIGNING_PFX_BASE64` | Base64 of the Authenticode code-signing certificate (.pfx) |
| `WINDOWS_SIGNING_PFX_PASSWORD` | Its password |

Variables (optional): `WINDOWS_TIMESTAMP_URL`, `WIX_VERSION`, `WIX_OSMF_ACCEPTED`, and for the optional packages `NODE_WIN_URL`/`NODE_WIN_SHA256`,
`PYTHON_WIN_URL`/`PYTHON_WIN_SHA256`, `TEMPORAL_WIN_URL`/`TEMPORAL_WIN_SHA256`, `POSTGRES_WIN_URL`/`POSTGRES_WIN_SHA256`. Nothing third-party is
downloaded without its hash. Each build publishes the MSI(s), the bundle, an SPDX SBOM and, on tags, a keyless cosign bundle for each file.
Verify with `cosign verify-blob --bundle <file>.cosign.bundle <file>` and `Get-AuthenticodeSignature <file>`.

## Trust boundary on the host

Every service listens on `127.0.0.1` and trusts the `X-Passage-*` identity headers, so on a host where other people can log on (a shared
Windows Server) a local user could skip authentication by calling a loopback port. `passage-server` closes that: each start it generates a random
secret, gives it to every child as `PASSAGE_GATEWAY_SECRET`, and adds it to every proxied request; `platform/go/tenant` then rejects any
identity-header request without it (service tokens are signed and unaffected). Only the gateway process, which verifies the JWT and asks OPA, can
speak for a user. The secret lives only in process memory and environments; processes of other users cannot read it. Still treat the server as
a dedicated host, and keep the data folder ACLs as installed.

## Temporal (optional package)

`PassageTemporal-<version>-x64.msi` installs `temporal-server.exe` and runs it as the `PassageTemporal` service through `passage-server wrap`
(temporal-server does not speak the Windows service protocol itself, so it runs as a supervised child with the same restart policy, log rotation and
failure actions). Edit `%ProgramData%\PassageTemporal\config\passage.yaml` (Postgres address, database names, and the placeholder password
`CHANGE_ME`, in a folder only SYSTEM, Administrators and the service account can read), create the two databases, run Temporal's schema setup
(`temporal-sql-tool`) against them, then `Start-Service PassageTemporal`. Point `TEMPORAL_HOSTPORT` at `127.0.0.1:7233`. The config is kept on upgrade and uninstall.
The bundle installs it with `InstallTemporal=1`, and PostgreSQL with `InstallPostgres=1 PostgresOptionFile=<file>`, where the file is one you created (Administrators only)
holding the EDB installer's option lines such as `superpassword=...`; the password never goes on a command line.

## Known limits

- Not run on a real Windows VM yet (no installer test, no upgrade downtime measurement, no `tools/e2e` live suite against `passage-server.exe`): tracked on #213.
- TLS from the Windows certificate store, embedded OPA and a Postgres-backed outbox relay (to drop external Kafka/Debezium) are follow-ups.
- agents-ts is bundled and registered, but it does not serve users under `passage-server` yet: it verifies the caller's bearer token itself
  and the gateway removes `Authorization` before proxying, and it needs `MCP_URL` (mcp-ts, not bundled), `AGENTS_JWT_ISSUER` and
  `AGENTS_JWKS_URL` in a `services:` entry of `passage.yaml`. workflow-ts is not bundled for arm64 (no Temporal native module for Windows arm64).
- transport reads the route optimizer from `TMS_OPTIMIZER_URL`; set it in a `services:` entry for `transport` to the `routing-py` port
  (`passage-server services` prints it) until the supervisor passes it.
- The Python services' OR-Tools wheels need the Microsoft Visual C++ runtime; Windows Server images normally have it.
- The WiX source is validated as XML here; it needs its first real `wix build` on a Windows runner.
