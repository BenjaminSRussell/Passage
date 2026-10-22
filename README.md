# Passage

A modern, open-architecture ERP rebuilt from the ground up — replacing Microsoft Dynamics AX / D365 F&SCM and SAP ECC / S/4HANA feature-for-feature, delivered in **50 sequential waves** where every wave ships something provable, testable and deployable.

- **[docs/PRD.md](docs/PRD.md)** — product requirements (what and why, scope map, NFRs, delivery model).
- **[roadmap/README.md](roadmap/README.md)** — the 50-wave plan, 10 phases, with deliverables, proofs, deploys and references per wave.
- **[roadmap/waves.json](roadmap/waves.json)** — the machine-readable plan of record. Edit this, then run `python3 scripts/build_roadmap.py`.
- **[docs/adr/](docs/adr/)** — architecture decision records.
- **GitHub Project** — https://github.com/users/BenjaminSRussell/projects/5 (one issue per wave, milestones per phase).

## Stack (ADR-0001)

Kubernetes (cloud-agnostic) · Go + TypeScript + Python services · Postgres per domain · Kafka + Debezium · Temporal · Keycloak + OPA · React module-federation shell · Apache Iceberg lakehouse · MCP-exposed AI agents.

## Milestones

| Wave | Milestone |
|---|---|
| 5 | Platform kernel demo |
| 15 | First period closed in Passage with statements tying to the trial balance |
| 30 | **Go-live candidate**: manufacturer runs buy → make → sell → collect for a month |
| 40 | Finance parity: group consolidation |
| 50 | **Production go-live + v1.0** |

## Develop

Wave 1 is the delivery baseline: `hello-ledger` (Go), `platform-bff` (Node), the React shell, Helm, Argo CD app-of-apps, and an OpenTofu cluster module.

```bash
make tools         # helm, tofu, k3d, kubectl, syft, trivy, cosign
make ci            # lint, unit tests, shell build, chart render, tofu validate, negative gate
make run           # hello-ledger on :8080
make deploy-local  # k3d + Argo CD + hello-ledger (Docker required)
```

`curl -s localhost:8080/healthz` returns the placeholder service. `make run` listens on 127.0.0.1 and requires `Authorization: Bearer $LEDGER_TOKEN` (`local-dev-token` when the variable is unset) on `/v1/*`. Promote a digest with `python3 tools/promotion.py promote stage sha256:<64 hex>`. That checks the image signature and SPDX attestation, then writes `values-<env>.yaml` and the Argo CD annotation. Argo CD syncs those files. `history/<env>.txt` is the rollback log. `make deploy-local` is the laptop cluster.

## Working the plan

1. Each wave is a GitHub issue labeled `wave` and `phase-N`; every 5th wave is `phase-exit`.
2. A wave closes only when all **Proof** boxes are checked with CI links and the Argo CD app is healthy in `stage`.
3. Per wave, the Moonaan SDLC pipeline is: `/sdlc:generate-architecture` → `/sdlc:generate-backlog` → `/sdlc:generate-code` → `/sdlc:review-code`.
4. Re-sync issues/project after editing the plan: `python3 scripts/build_roadmap.py --github` (idempotent).
