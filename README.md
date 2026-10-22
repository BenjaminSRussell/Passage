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

## Working the plan

1. Each wave is a GitHub issue labeled `wave` and `phase-N`; every 5th wave is `phase-exit`.
2. A wave closes only when all **Proof** boxes are checked with CI links and the Argo CD app is healthy in `stage`.
3. Per wave, the Moonaan SDLC pipeline is: `/sdlc:generate-architecture` → `/sdlc:generate-backlog` → `/sdlc:generate-code` → `/sdlc:review-code`.
4. Re-sync issues/project after editing the plan: `python3 scripts/build_roadmap.py --github` (idempotent).
