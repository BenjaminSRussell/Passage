# Passage

Passage is an open-architecture **ERP** (enterprise resource planning) system: the software a company runs its finance, purchasing, inventory, manufacturing, sales and HR on. It is being built from scratch to replace Microsoft Dynamics AX / D365 and SAP ECC / S/4HANA, in **50 sequential waves**. Every wave ships something that can be tested and deployed.

**Status:** early build. Most domains exist as Go services with tests and a web screen. The API gateway and login are not wired yet, so there is no hosted demo. This site publishes the design documents; the code is on [GitHub](https://github.com/BenjaminSRussell/Passage).

## Where to start

| If you want to... | Read |
|---|---|
| Understand what Passage is and why | [PRD overview](PRD.md) and [the 10x thesis](prd/part-00-thesis.md) |
| See every requirement in one page | [Full PRD](PRD-full.html) |
| See how it is built | [Architecture](architecture.md) and [ADR-0001: platform stack](adr/0001-platform-stack.md) |
| Compare scope with AX or SAP | [Coverage vs Dynamics AX](prd/coverage-ax.md), [coverage vs SAP](prd/coverage-sap.md) |
| Run or operate it | [Operations](ops/README.md) and [local delivery](runbooks/delivery/deploy-local.md) |
| Contribute code | [Engineering conventions](engineering/CONVENTIONS.md) and the [README](https://github.com/BenjaminSRussell/Passage#readme) |
| Track progress by wave | [Roadmap](https://github.com/BenjaminSRussell/Passage/blob/main/roadmap/README.md) |

## Stack

Kubernetes · Go, TypeScript, Python · Postgres per domain · Kafka + Debezium · Temporal · Keycloak + OPA · React. See [ADR-0001](adr/0001-platform-stack.md).
