# ADR-0004: Windows distribution as a signed WiX MSI running one Go supervisor service

**Status:** Proposed — 2026-11-11
**Issues:** [#607](https://github.com/BenjaminSRussell/Passage/issues/607), related #599, #50, #213

The #607 acceptance criteria name this record `0003-windows-packaging.md`; ADR-0003 was already taken by the setup checklist
decision, so the Windows packaging decision is ADR-0004.

## Context

Some on-premises customers (AX 2009 shops especially) run Windows Server and deploy software with Intune,
Configuration Manager (SCCM) or Group Policy, and ask for "an installer". Kubernetes (ADR-0001) stays the primary
production target; this is an additional delivery channel.

Passage has about 60 Go service binaries (static, `CGO_ENABLED=0`), a Vite web app, six TypeScript services, five
Python services, and infrastructure: Postgres with a database per bounded context and the `passage_app` role (RLS),
Keycloak, OPA, Kafka with Debezium for the outbox, and Temporal. `make dev` already runs the Go services on loopback
ports behind `services/identity/cmd/devgateway` (`tools/e2e/cmd/passage-dev`). The Windows design grows from that.

## Decision

Ship an Authenticode-signed MSI built with the WiX Toolset that installs **one** Windows Service, `Passage`, running
`passage-server.exe` (`tools/passage-server`).

- **`passage-server`** is a single Go binary with `serve`, `migrate`, `doctor` and `version`. `serve` supervises the
  service executables installed under `bin\` as child processes on loopback ports (one `HTTP_ADDR` and `ADMIN_ADDR`
  per service, `DATABASE_URL` derived from the configured DSN per service database), restarts them with backoff,
  reports aggregated health on `/readyz`, and runs the **built-in gateway**: the devgateway logic (JWT verification,
  OPA decision, header stripping, `/api/v1/<service>` routing) plus the web app embedded with `go:embed`. Children are
  tied to a Windows job object, so they die with the supervisor. The gateway code is a copy of the devgateway's
  (which is a `package main`); the shared parts are the identity verifier and OPA client it imports.
- **Trust boundary.** Services trust `X-Passage-*` identity headers on their loopback ports. On a host with other logons that would let a
  local user skip the JWT and OPA checks, so `passage-server` generates a per-start random secret, passes it to every child as
  `PASSAGE_GATEWAY_SECRET`, and the gateway adds it to every proxied request. `platform/go/tenant` (opt-in by that environment variable, off in
  Kubernetes where NetworkPolicy is the boundary) rejects a header identity without it; service tokens are signed and unaffected. The
  supported shape is still a dedicated server.
- **Database roles.** `PG_DSN` is the owner role (creates the `passage_<service>` databases, owns the tables, runs migrations with
  `MIGRATE_DATABASE_URL`); `PG_APP_DSN` is `passage_app` (NOBYPASSRLS, not an owner) for every serving child with `MIGRATE_ON_START=false`.
  Doctor checks both roles.
- **Temporal.** `temporal-server.exe` has no service-control handler, so the Temporal MSI installs `passage-server wrap`, which runs it as a
  supervised child with the same restart policy and logs.
- **One service, not sixty.** The service runs under a virtual service account (`NT SERVICE\Passage`, or a gMSA via
  `SERVICE_ACCOUNT`), starts automatically, has restart-on-failure recovery actions, writes warnings and errors to the
  Windows Event Log and everything to rotating files in `%ProgramData%\Passage\logs`.
- **Configuration** is `%ProgramData%\Passage\passage.yaml`, overridden by `HKLM\SOFTWARE\Passage\Config` (written by
  the MSI from its properties) and then by the policy key `HKLM\SOFTWARE\Policies\Passage`, so GPO and Intune can
  manage it. Optional features add their services through `conf.d\*.yaml`.
- **External dependencies by default:** Postgres (`PG_DSN`), OIDC issuer (`OIDC_ISSUER`), OPA (`OPA_URL`), Kafka
  (`KAFKA_BOOTSTRAP`), Temporal (`TEMPORAL_HOSTPORT`). `passage-server doctor` checks each one before the service
  starts and the MSI runs it as a custom action: a bad DSN, a `BYPASSRLS` or superuser role, an unreachable or
  mismatching issuer, a closed Kafka or Temporal port, a missing certificate or runtime fails the install with one
  readable `FAIL name: detail` line per problem in the MSI log.
- **Optional components through a Burn bundle** (`PassageSetup.exe`): the EDB PostgreSQL installer and a Temporal
  server MSI (`PassageTemporal`, a second service), each switched on by a variable and each also installable alone.
  Never `temporal server start-dev`. Keycloak (a JRE) and Kafka are **not** bundled in v1; Kafka's own documentation says
  Windows is not well supported, so a Windows install points at an external broker.
- **TypeScript and Python services** are optional MSI features with private pinned runtimes (the official `node.exe`,
  the python.org embeddable distribution) under `runtime\`. Node single executable applications are not used (Node
  documents them as experimental). A feature that is off is reported by `doctor`.
- **Upgrades** use `MajorUpgrade` with the new files laid down before the old product is removed, then a deferred
  `migrate` custom action. If it fails the transaction rolls back, N-1 files come back and the old service is started
  again; schema changes are expand/contract-safe (`deploy/multi-region/tools/expand_contract_lint.py`), so N-1 keeps
  working against the expanded schema. Database rollback is a documented restore. An upgrade asks for a confirmed backup
  (`BACKUP_CONFIRMED=1`).
- **Signing and supply chain.** CI signs every executable, the MSI and the bundle (and its Burn engine) with a
  timestamped Authenticode signature, fails on any unsigned binary in a release build, writes an SPDX SBOM and a keyless
  cosign signature next to each release file.
- **FIPS.** The FIPS variant is built with `GOFIPS140=v1.0.0` for the whole tree and sets `REQUIRE_FIPS=1`.
  `passage-server` runs `cloudprofile.CheckFIPS` (#599) at start and refuses to start when `crypto/fips140.Enabled()` is
  false; children do the same through `PASSAGE_CLOUD_PROFILE` and their own `httpx.Serve` check. Only amd64 and arm64
  are built, because the Go module's FIPS mode is not supported on 32-bit Windows.

## Alternatives rejected

- **MSIX.** Services need a restricted capability and Windows 10 2004+, cannot depend on services outside the package,
  HKLM and install-directory writes are virtualized, and GPO software installation cannot deploy it.
- **Inno Setup or NSIS.** No Windows Installer repair, rollback or upgrade codes, and GPO cannot deploy an EXE natively.
- **Docker Desktop or WSL2.** Paid licence at larger companies, virtualization is often disabled on hardened images, and
  it only moves the Kubernetes problem to a desktop.
- **One MSI per service.** Upgrade ordering, ports and shared configuration would be unmanageable.
- **Bundling Postgres, Keycloak with a JRE, Kafka and Connect, and Temporal in one MSI.** Large, a wide CVE patch
  surface, and Kafka on Windows is unsupported upstream.
- **Linking all services into one process.** Not done in v1: the services are separate `package main` programs with
  their own configuration and migrations. Supervising them keeps each one unchanged and its logs separate, at the
  price of about 60 processes on one host. Revisit if memory per host is a problem.

## WiX licensing

WiX binary releases from v6 carry an Open Source Maintenance Fee (OSMF) for revenue-generating users (from v7:
organizations with $10k or more in annual revenue). Either budget the fee and set the repository variable
`WIX_OSMF_ACCEPTED=true` (the workflow refuses WiX 7 and later without it), or build WiX from source and point
`WIX_VERSION` at that package feed. The default is WiX 6.0.2. This is a business decision, not made here.

## Consequences

- Windows is a supported on-premises target next to Kubernetes, with its own CI workflow
  (`.github/workflows/windows-msi.yml`) that runs only when its paths change, on a tag, or by hand.
- TLS uses PEM files (`TLS_CERT_FILE`, `TLS_KEY_FILE`). Reading a certificate from the Windows certificate store needs a
  crypto.Signer over CNG/NCrypt and is a follow-up.
- OPA runs as an external server (`OPA_URL`) in v1; embedding it as a library is a follow-up.
- Debezium and Kafka Connect remain external; a Postgres-backed outbox relay for single-host mode is a later decision.
- Not proven here: running the MSI on a Windows VM, the live-stack suite of `tools/e2e` against `passage-server.exe`
  on `windows-latest`, the upgrade downtime figure. Those need a Windows runner with external Postgres, Keycloak, Kafka
  and Temporal endpoints and are tracked on #213.
