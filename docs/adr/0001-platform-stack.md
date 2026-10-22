# ADR-0001: Cloud-agnostic Kubernetes microservices stack

**Status:** Accepted — 2026-10-21
**Deciders:** Ben Russell, Zach Demanche

## Context

Passage must replace AX/SAP for customers who will not accept vendor lock-in to a single cloud, and must be self-hostable. The alternatives considered were (a) AWS serverless (Lambda/DynamoDB/Pulumi — the Moonaan default), (b) a Postgres-first modular monolith on ECS, and (c) Kubernetes microservices with Postgres per domain and Kafka.

## Decision

Option (c):

| Concern | Choice | Why |
|---|---|---|
| Runtime | Kubernetes (any conformant distro), Helm, Argo CD, Envoy Gateway | Portable across EKS/GKE/AKS/bare-metal/k3d; GitOps rollback by digest |
| IaC | Terraform/OpenTofu per provider (thin), Helm for everything in-cluster | Keeps provider-specific code minimal |
| Services | Go (ledgers, inventory, pricing, MRP, scheduling), TypeScript (workflow, integration, BFFs), Python (forecasting, ML, agent evals) | Go for throughput-critical engines; TS shared with the frontend; Python for numerical work |
| Data | Postgres per bounded context (CloudNativePG), RLS for tenancy, Atlas migrations generated from entity metadata | Relational semantics match ERP data; per-domain isolation; no distributed 2PC — sagas via Temporal |
| Eventing | Kafka (Strimzi), transactional outbox via Debezium, CloudEvents, AsyncAPI, schema registry | Exactly-once effect without dual writes; replayable |
| Workflow | Temporal | Durable approvals, batch and agent orchestration with timers and human tasks |
| Identity/authz | Keycloak (OIDC/SAML/SCIM), OPA/Rego | AX-style roles/duties/privileges compiled to policy |
| Frontend | React, module federation shell, PWA for mobile/kiosk | Metadata-driven forms; one shell for all modules |
| Analytics | Debezium → Iceberg → Trino/ClickHouse, Cube semantic layer | Keeps OLTP clean; governed metrics |
| AI | MCP server over Passage APIs; Claude default via a provider-agnostic layer | Agents act under the user's OPA permissions |
| Observability | OpenTelemetry → Prometheus/Tempo/Loki/Grafana | Vendor-neutral |

## Consequences

- Higher operational surface than serverless; mitigated by an all-CNCF reference stack with Helm charts and the wave-1 pipeline.
- Cross-domain consistency is eventual outside the universal journal; the journal posting API is the single synchronous financial truth.
- The Moonaan sdlc plugin's defaults (DynamoDB, Pulumi, Lambda) are **not** used; its architecture/backlog/code generators are driven with this ADR as a constraint.
