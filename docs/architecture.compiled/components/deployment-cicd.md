# Deployment, CI/CD and Runtime Infrastructure

## Purpose & Boundaries
Monorepo topology and Kubernetes delivery: Kubernetes on any conformant distribution, Helm + Argo CD GitOps, Terraform/OpenTofu provider modules in `deploy/`, CloudNativePG Postgres, Strimzi Kafka, Vault + External Secrets, OpenTelemetry stack. 31 deployable images in 15 bounded contexts. No Pulumi, no ECS/Lambda, no per-service repos.

## Interfaces
- Repo layout: `platform/go/`, `services/<context>/` (Go module per context; `<name>-ts/`, `<name>-py/` siblings), `apps/web/`, `apps/<pwa>/`, `packages/@passage/*` (proposed), `deploy/` (umbrella Helm chart, Argo app-of-apps, OpenTofu modules, k3d), `data/` (lakehouse; proposed), `docs/`, `roadmap/` (waves.json, ownership.json), `Makefile`, `.github/workflows/`. Service layout: `cmd/`, `internal/{domain,store,api,events}`, `migrations/`, `api/openapi.yaml` (3.1), `events/asyncapi.yaml` (3), `charts/<name>/` (co-located under `services/<name>/charts/<name>`), `README.md` with error codes. TS: pnpm, vitest, `src/{api,domain,store,events,workflows,activities}`; each TS image has `api` and `worker` entrypoints deployed as separate Deployments. Python: uv, pytest, FastAPI.
- CI: component-prefixed path-filtered workflows (`platform-ci.yml`, `services-ci.yml`, `web-ci.yml`, `pwa-ci.yml`, `charts-ci.yml`, `data-ci.yml`), `gate.yml` with `dorny/paths-filter` and an always-running `gate` job as the ONLY required branch-protection check; root `release.yml`. Immutable image tags (git SHA + digest), cosign signing, SBOM (NFR-SEC3), Argo CD syncs digest-pinned manifests (rollback <= 15 min, NFR-A5), Argo Rollouts canary with k6 traffic.
- Ownership rule: a wave edits only directories in `roadmap/ownership.json`; never `platform/`; code against another wave's OpenAPI/AsyncAPI with an in-package fake; enforced by CI check on changed-path set.
- Infra: CNPG Pooler (PgBouncer transaction mode), Atlas migrations, Postgres 17+, Kafka 4.x KRaft, Temporal self-hosted (own CNPG cluster, SQL advanced visibility, history shard count 512), Keycloak Operator, S3-compatible store with Object Lock (product Q13), ClamAV, `make deploy-local` on k3d in < 10 min (NFR-PT2).

## Data Ownership
absent — the source document does not address this (see data conventions in spine; backups: CNPG continuous WAL archive + base backups to object storage, per-tenant envelope encryption, RPO <= 5 min, RTO <= 1 h, quarterly restore drill; base backups/WAL expire after 30 days).

## Events Produced
absent — the source document does not address this

## Events Consumed
absent — the source document does not address this

## Dependencies
internal: all components. external: GitHub Actions + GHCR/OCI registry (assumed, Q12), Argo CD, Argo Rollouts, cert-manager, Helm, OpenTofu, k3d, Vault, ESO, Prometheus/Tempo/Loki/Grafana/Alertmanager, KEDA (optional).

## Gotchas & Constraints
- Runtime scaling: HPA on CPU/request rate (Go) or event-loop lag (TS); Temporal workers on task-queue backlog; PDBs everywhere; ledger >= 3 replicas across zones.
- Migrations: PreSync Job in stage/prod; on-start migrate in dev only; advisory-locked; destructive steps one release later. Secrets rotate without restart (files, file watch); Vault dynamic DB creds 24 h leases; cert-manager 30-day rotation.
- Encryption at rest per datastore table (LUKS+Clevis/Tang on bare metal; Vault Shamir 3-of-5 unseal on bare metal).
- Probes: `/healthz` liveness, `/readyz` readiness (hard deps only), startup probe on `/healthz`; ServiceMonitor toggle.
- SLOs: availability >= 99.9%/30 d (99.95% after wave 50; ledger.Post >= 99.95%), error rate < 0.1%; burn-rate alerts 14.4x/1 h, 6x/6 h, 1x/3 d.
- Noisy-neighbor: per-tenant rate limits, Temporal task-queue partitioning per tier, large tenants pinned to dedicated CNPG clusters.
- CI auth to registry by OIDC, no long-lived secrets; gitleaks gate.
- Moonaan overrides: repo topology, `/healthz` not `/health/*`, OTel not X-Ray/CloudWatch.

## Source Sections
- Backend 1.1 (114-133), 2 (135-141), 4 (346-366), 7.8 (1205-1224), 7.15 (1300-1317), 7.20 (1345-1350); Cross-Cutting Repository Topology (7700-7723), Observability (7725-7735), encryption table (7750-7765); Data 3 (1486-1500), 12.8 (3459-3464); Testing T10 (6578-6693)
