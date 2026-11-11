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
| PostgreSQL 16 or later (Windows, Linux or Azure Database for PostgreSQL) | `PG_DSN` | Roles and RLS as in `deploy/postgres`. The URL must use the `passage_app` role, which must be `NOBYPASSRLS` and not a superuser. Each `passage_<service>` database must exist (`CREATE DATABASE`); migrations are applied by `passage-server migrate`. SQL Server is not supported. |
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
| `PG_DSN` | none (required) | Postgres URL for the services, role `passage_app`. A secret: the key is ACL-restricted, see [Secrets](#secrets). |
| `PG_APP_DSN` | `PG_DSN` | Use when `PG_DSN` is an admin URL and the services need a separate `passage_app` URL. |
| `OIDC_ISSUER` | none (required) | Keycloak realm URL. |
| `OIDC_AUDIENCE` | `passage-api` | Required token audience. |
| `OIDC_JWKS_URL` | issuer default | Set when the JWKS is not at the issuer's discovered address. |
| `TENANT_ID` | empty | The tenant id the gateway accepts; empty means the verifier's default rule. |
| `OPA_URL` | none (required) | OPA base URL. |
| `KAFKA_BOOTSTRAP` | none (required) | Comma-separated bootstrap servers. |
| `TEMPORAL_HOSTPORT` | none (required) | Temporal frontend `host:port`. |
| `LISTEN` | `127.0.0.1:8443` | Gateway address. Anything that is not loopback needs `TLS_CERT_FILE` and `TLS_KEY_FILE` (doctor fails otherwise). |
| `LISTEN_PORT` | `8443` | Port used for the firewall rule; keep equal to the port in `LISTEN`. |
| `OPEN_FIREWALL` | `1` | `0` skips the inbound firewall rule. |
| `TLS_CERT_FILE`, `TLS_KEY_FILE` | none | PEM certificate (with chain) and private key. |
| `CLOUD_PROFILE` | none | `PASSAGE_CLOUD_PROFILE` for every child (`commercial`, `gcc`, `gcc-high`, `dod`, `china`, overlays) (#599). |
| `REQUIRE_FIPS` | `0` (`1` in the FIPS MSI) | `1` makes the server refuse to start unless `crypto/fips140.Enabled()`. |
| `SERVICE_ACCOUNT` | `NT SERVICE\Passage` | Virtual account; pass `DOMAIN\gmsa$` for a group managed service account. |
| `SKIP_DOCTOR` | `0` | `1` skips the dependency checks, to stage an install before the dependencies exist. |
| `SKIP_MIGRATE` | `0` | `1` skips the migration step. |
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
  PG_DSN="postgres://passage_app:SECRET@db01:5432/postgres?sslmode=verify-full" ^
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
| passage-server | `%ProgramData%\Passage\logs\passage-server.log` (JSON lines, 50 MB x 5 files) |
| Each service | `%ProgramData%\Passage\logs\<service>.log` (same rotation) |
| Warnings and errors of the supervisor | Event Viewer, Windows Logs, Application, source `Passage` |
| MSI install | the file given to `msiexec /l*v` |
| Readiness | `https://<host>:<port>/readyz` returns 503 and lists the failing services; `/healthz` is liveness |

A required service that exits more than five times in five minutes makes `passage-server` stop with exit code 1, and the service
recovery actions restart it after 30 seconds. Optional services (the Node and Python features) failing do not stop it.

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

`.github/workflows/windows-msi.yml` builds for amd64 and arm64, standard and FIPS, on `windows-latest`. It runs on pull requests that touch
`deploy/windows`, `tools/passage-server` or `platform/go/cloudprofile`, on `v*` tags and by hand. It signs and fails on unsigned binaries for tags and for
dispatches with `sign: true`; pull requests build unsigned (they have no secrets) and the verification step lists the unsigned files.

Secrets the workflow references (none is defined in the repository; put them in the `platform-release` environment):

| Secret | Content |
|---|---|
| `WINDOWS_SIGNING_PFX_BASE64` | Base64 of the Authenticode code-signing certificate (.pfx) |
| `WINDOWS_SIGNING_PFX_PASSWORD` | Its password |

Variables (optional): `WINDOWS_TIMESTAMP_URL`, `WIX_VERSION`, `WIX_OSMF_ACCEPTED`, and for the optional packages `NODE_WIN_URL`/`NODE_WIN_SHA256`,
`PYTHON_WIN_URL`/`PYTHON_WIN_SHA256`, `TEMPORAL_WIN_URL`/`TEMPORAL_WIN_SHA256`, `POSTGRES_WIN_URL`/`POSTGRES_WIN_SHA256`. Nothing third-party is
downloaded without its hash. Each build publishes the MSI(s), the bundle, an SPDX SBOM and, on tags, a keyless cosign bundle for each file.
Verify with `cosign verify-blob --bundle <file>.cosign.bundle <file>` and `Get-AuthenticodeSignature <file>`.

## Known limits

- Not run on a real Windows VM yet (no installer test, no upgrade downtime measurement, no `tools/e2e` live suite against `passage-server.exe`): tracked on #213.
- TLS from the Windows certificate store, embedded OPA and a Postgres-backed outbox relay (to drop external Kafka/Debezium) are follow-ups.
- The TypeScript and Python services are laid out as optional features, but their per-service start commands in `features\node.yaml` and `python.yaml`
  and their application bundles still need the packaging step for those services; hiding their screens in the web app when a feature is off is not wired.
- The WiX source is validated as XML here; it needs its first real `wix build` on a Windows runner.
