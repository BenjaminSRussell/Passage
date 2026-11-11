# Not implemented in PSG-1 slice 1

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: TypeScript services, React web, Python layout | not built | `web modules: none` and no TS or Python code exists in the owned paths |
| Deliverable 2: providers `eks`, `gke`, `baremetal` (`aks` is built, see docs/ops/azure-aks.md) | interface accepts them, apply fails a precondition | no cloud account, credentials or provider is named by the ticket |
| Deploy: one cloud dev cluster | not built | same reason; `deploy/envs/dev` and `deploy/argocd/root-dev.yaml` are ready for it |
| Deliverable 3: stage app-of-apps and digest promotion/rollback CLI (AC3) | declarative/local tooling added in slice 2 | no stage cluster/cloud credentials to prove sync or Argo history live; Release rows and `release.*` events are recorded after merge through `stage_release.py record` and `POST /v1/platform/releases` (issue #75); Argo `Healthy` confirmation and the live stage proof stay on #213 |
| Deliverable 4: blocking gate for unsigned images and its negative test (AC4) | slice 2 of PSG-1 | line budget; CI still builds the SBOM, signs and attests every pushed image |
| sigstore policy-controller, `ClusterImagePolicy` | slice 2 of PSG-1 | part of the AC4 gate |
| HIGH-severity-older-than-14-days gate and `.vuln-exceptions.yaml` (FR-1.6) | not built | CI fails on CRITICAL only; the exception file format is slice 2 |
| gitleaks gate (FR-1.11) | not built | slice 2 |
| Vault and External Secrets Operator | not built | dev and local use a Secret created by `deploy-local`; no secret value is in Git |
| CloudNativePG | not built | dev uses a single in-cluster Postgres; the service connects as the non-superuser `passage_app` role (no BYPASSRLS) created by an init script, and List also filters by tenant_id |
| Root `make deploy-local` | handoff | root `Makefile` is outside wave 1 ownership; add `deploy-local: ; $(MAKE) -C deploy deploy-local` there |
| Branch protection requiring `gate` | handoff | repository setting |
| `deploy/envs/dev/apps/hello-ledger.yaml` digest | placeholder | the all-zero digest is not a real image; CI pushes `ghcr.io/<owner>/hello-ledger` and the promotion work in slice 2 writes the real digest |
| Idempotency-Key reuse with a different body on hello-ledger entries | not built | replay returns the original entry; `POST /v1/platform/releases` already answers 409 |
| Envoy Gateway, JWT validation | wave 2 | ingress exposes only `/healthz` and `/readyz` because tenant headers are trusted only behind the gateway |
| Argo CD image registry `ghcr.io/passage` | open question | the architecture lists the registry as undecided; images go to the repository owner namespace |
| Debezium connector for platform-health | not built | `release.*` events are written to the `outbox` table in the same transaction, but no Debezium connector or Kafka topic exists yet for this service, so they are not published; the connector lands with the eventing wave |
| Release model §0.1.4: `chart_version`, `sbom_url`, `status` enum | deviation | a row records one promote or rollback `action` for an environment, not the build lifecycle (built, scanned, signed, deployed_*, superseded, rolled_back); `chart_version` and `sbom_url` are not stored, `signed` is the cosign result at record time |
| Release model §0.1.4: unique `(service, image_digest)` and one `deployed_<env>` per service per environment | deviation | not enforced: a rollback re-records an earlier digest and the same digest is promoted to several environments; history is append-only and the newest row per environment is current |
| Release model §0.1.4: `git_sha char(40)` | text with a 40-hex check | same constraint, stored as `text` |
| Release model additions | deviation | `environment`, `action`, `previous_digest`, `recorded_by_tenant`, `recorded_by_user`, `reason`, `idempotency_key`; rows live under the nil system tenant and are written only by `PLATFORM_OPERATOR_TENANT_ID` |
| Environment model §0.1.4: `cluster_provider` required | nullable | the column is `NULL` for the seeded dev, stage and prod rows because no cluster provider is decided (see providers above); the CHECK still limits non-null values to k3d, eks, gke, aks, baremetal |
| Release reconciliation and the `deployed_<env>` status transitions (BR-1.1) | not built | needs live Argo history; tracked on #213 |
