# Not implemented in PSG-1 slice 1

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: TypeScript services, React web, Python layout | not built | `web modules: none` and no TS or Python code exists in the owned paths |
| Deliverable 2: providers `eks`, `gke`, `aks`, `baremetal` | interface accepts them, apply fails a precondition | no cloud account, credentials or provider is named by the ticket |
| Deploy: one cloud dev cluster | not built | same reason; `deploy/envs/dev` and `deploy/argocd/root-dev.yaml` are ready for it |
| Deliverable 3: stage and prod app-of-apps, promotion by digest, rollback (AC3) | slice 2 of PSG-1 | line budget; needs a second cluster to exercise |
| Deliverable 4: blocking gate for unsigned images and its negative test (AC4) | slice 2 of PSG-1 | line budget; CI still builds the SBOM, signs and attests every pushed image |
| sigstore policy-controller, `ClusterImagePolicy` | slice 2 of PSG-1 | part of the AC4 gate |
| HIGH-severity-older-than-14-days gate and `.vuln-exceptions.yaml` (FR-1.6) | not built | CI fails on CRITICAL only; the exception file format is slice 2 |
| gitleaks gate (FR-1.11) | not built | slice 2 |
| Vault and External Secrets Operator | not built | dev and local use a Secret created by `deploy-local`; no secret value is in Git |
| CloudNativePG, `passage_app` role without BYPASSRLS | not built | dev uses a single in-cluster Postgres whose superuser bypasses RLS; RLS is exercised by store tests only against a non-superuser role |
| Root `make deploy-local` | handoff | root `Makefile` is outside wave 1 ownership; add `deploy-local: ; $(MAKE) -C deploy deploy-local` there |
| Branch protection requiring `gate` | handoff | repository setting |
| `deploy/envs/dev/apps/hello-ledger.yaml` digest | placeholder | the all-zero digest is not a real image; CI pushes `ghcr.io/<owner>/hello-ledger` and the promotion work in slice 2 writes the real digest |
| Idempotency-Key reuse with a different body (422) | not built | replay returns the original entry |
| Envoy Gateway, JWT validation | wave 2 | ingress exposes only `/healthz` and `/readyz` because tenant headers are trusted only behind the gateway |
| Argo CD image registry `ghcr.io/passage` | open question | the architecture lists the registry as undecided; images go to the repository owner namespace |
