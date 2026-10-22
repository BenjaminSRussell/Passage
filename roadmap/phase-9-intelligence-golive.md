# Phase 9: Intelligence, Compliance & Go-Live (waves 46-50)

Data platform and analytics, AI copilots and agents, globalization and compliance, then the migration factory, scale hardening and production cutover off AX/SAP.

## Wave 46: Data Platform, Analytics & Embedded BI

**Phase 9 — Intelligence, Compliance & Go-Live**

**Goal.** Every domain streams into a governed lakehouse with semantic models; users get embedded dashboards, self-service analytics and operational KPIs without touching OLTP.

**Replaces**

- *AX / D365:* Entity store / BYOD, Power BI embedded workspaces, SSRS reports, cubes
- *SAP:* BW/4HANA, embedded analytics (CDS views), SAP Analytics Cloud, Datasphere

**Deliverable**

- CDC from every Postgres domain via Debezium → Kafka → Apache Iceberg lakehouse; Trino/ClickHouse query layer; dbt models
- Semantic layer (Cube) with governed metrics (revenue, margin, DSO, OTIF, OEE, inventory turns) and row-level security mirrored from OPA
- Embedded dashboards in every workspace; self-service explorer; scheduled reports; export to Excel/Power BI/Tableau
- Operational report designer (replaces SSRS layouts) for documents: invoices, POs, packing slips, checks
- Data catalog and lineage (OpenMetadata)

**Proof (testable acceptance)**

- [ ] Lakehouse freshness: p95 OLTP-to-dashboard lag < 60 s under load
- [ ] Metric reconciliation: revenue in the semantic layer equals the GL to the cent each day (automated check)
- [ ] RLS test: a user sees only their entity's data in dashboards
- [ ] Document designer renders all standard documents pixel-stable (visual regression)

**Deploy**

Analytics stack and embedded dashboards live across all workspaces.

**References**

- [Apache Iceberg](https://iceberg.apache.org/docs/latest/)
- [Debezium](https://debezium.io/documentation/)
- [Cube semantic layer](https://cube.dev/docs)
- [OpenMetadata](https://docs.open-metadata.org/)
- [D365 analytics / entity store](https://learn.microsoft.com/en-us/dynamics365/fin-ops-core/dev-itpro/analytics/power-bi-integration-entity-store)

**Depends on:** wave 45

---

## Wave 47: AI Copilots, Agents & Automation

**Phase 9 — Intelligence, Compliance & Go-Live**

**Goal.** Users ask questions and delegate work in natural language; governed agents execute multi-step processes (cash application, planning exceptions, AP coding) with human-in-the-loop and full audit.

**Replaces**

- *AX / D365:* Copilot in D365 F&O / SCM, Power Automate flows
- *SAP:* Joule, SAP Build Process Automation, Business AI

**Deliverable**

- MCP server exposing every Passage API/entity/report as governed tools, respecting OPA permissions and tenant isolation
- Copilot UI in the shell: ask-the-data (semantic layer), explain-this-record, draft documents, summarize workflows
- Agent runtime (Temporal-backed) for autonomous tasks with approval gates: AP invoice coding, cash application suggestions, MRP exception triage, collections outreach, anomaly detection on journals
- Eval harness: golden datasets per agent, accuracy/precision thresholds gating deploys; prompt/version registry; cost/token budgets per tenant
- Responsible-AI controls: PII redaction, action logging, kill switch, model-agnostic provider layer (Claude default, Bedrock/Azure/open-weights options)

**Proof (testable acceptance)**

- [ ] Agent evals: AP coding ≥ 92% top-1 accuracy, cash application suggestions ≥ 90% precision on held-out sets; evals run in CI
- [ ] Authorization: an agent acting for a user can never exceed that user's permissions (contract tests)
- [ ] Every agent action produces an audit entry with inputs, tool calls, outputs and approver
- [ ] Cost guard: per-tenant token budget enforced and alerted

**Deploy**

Copilot enabled in all workspaces; two agents (AP coding, cash application) running in suggest-mode for the demo entity.

**References**

- [Model Context Protocol](https://modelcontextprotocol.io/)
- [Anthropic Claude API](https://docs.anthropic.com/)
- [Temporal for agent orchestration](https://docs.temporal.io/)
- [D365 Copilot overview](https://learn.microsoft.com/en-us/dynamics365/fin-ops-core/fin-ops/copilot/copilot-for-finance-operations)
- [NIST AI RMF](https://www.nist.gov/itl/ai-risk-management-framework)

**Depends on:** wave 46

---

## Wave 48: Globalization, Localization & Compliance Controls

**Phase 9 — Intelligence, Compliance & Go-Live**

**Goal.** Passage runs legally in target countries (statutory reports, e-invoicing mandates, localized documents) and passes SOC 2 / ISO 27001-style controls with segregation of duties.

**Replaces**

- *AX / D365:* Country/region localizations, electronic reporting configurations, segregation of duties, database log, Regulatory Configuration Service
- *SAP:* Country versions, Document and Reporting Compliance, GRC Access Control (SoD), audit information system

**Deliverable**

- Localization framework: country packs (US, UK, DE, FR, IN, MX, CA as first set) covering tax, statutory reports, document layouts, bank formats, calendars
- Electronic reporting designer: configurable XML/JSON/CSV outputs with versioning and regulator mandates registry
- Segregation-of-duties rules engine with conflict detection, mitigation workflow, access certification campaigns
- Security hardening: SOC 2 control mapping, evidence automation, pen-test remediation, secrets rotation, data residency per tenant, backup/restore drills
- Data privacy: GDPR/CCPA subject requests (export/erasure), retention policies, legal holds

**Proof (testable acceptance)**

- [ ] Each country pack ships with a statutory-report fixture validated against the regulator's schema
- [ ] SoD: 50 planted conflicting role combinations are all detected; mitigation workflow records approvals
- [ ] Backup/restore drill restores a tenant to a point in time within RPO 5 min / RTO 1 h
- [ ] External penetration test findings at high/critical are zero at wave exit

**Deploy**

Localization packs installed per demo entity; compliance workspace with SoD and evidence collection live.

**References**

- [D365 localization and regulatory features](https://learn.microsoft.com/en-us/dynamics365/finance/localizations/global/regulatory-updates)
- [SAP Document and Reporting Compliance](https://help.sap.com/docs/SAP_DOCUMENT_AND_REPORTING_COMPLIANCE)
- [SOC 2 Trust Services Criteria](https://www.aicpa-cima.com/topic/audit-assurance/audit-and-assurance-greater-than-soc-2)
- [ISO/IEC 27001](https://www.iso.org/standard/27001)
- [GDPR](https://gdpr.eu/)

**Depends on:** wave 47

---

## Wave 49: Migration Factory: Full AX/SAP Transactional Cutover Tooling

**Phase 9 — Intelligence, Compliance & Go-Live**

**Goal.** A customer on AX 2012/D365 F&O or SAP ECC/S4 can be migrated — master data, open transactions, balances, history — with automated reconciliation and parallel-run comparison.

**Replaces**

- *AX / D365:* Upgrade/migration tooling (AX 2012 → D365 code and data upgrade)
- *SAP:* SAP S/4HANA conversion tools, Migration Cockpit, Selective Data Transition

**Deliverable**

- Transactional migration packs: open AP/AR items, open POs/SOs, on-hand by dimension, in-process production orders, fixed asset NBVs, GL balances by dimension, project WIP, employee/payroll YTD
- Historical data load into the lakehouse (read-only history with cross-reference to legacy ids)
- Parallel-run harness: replay a legacy period's transactions into Passage and diff trial balances, inventory values and subledgers
- Cutover runbook generator: freeze, extract, load, reconcile, go/no-go checklist, rollback plan
- Legacy coexistence adapters for phased cutovers (entity-by-entity)

**Proof (testable acceptance)**

- [ ] Synthetic AX and SAP datasets (full year, 3 entities) migrate with 100% reconciliation on all control totals
- [ ] Parallel-run diff on a month of transactions shows zero unexplained variances
- [ ] Cutover rehearsal completes within the planned window (< 48 h for the synthetic dataset)
- [ ] Rollback rehearsal restores legacy coexistence cleanly

**Deploy**

Migration factory workspace with packs for AX and SAP; rehearsal environment provisioned by pipeline.

**References**

- [D365 F&O upgrade from AX 2012](https://learn.microsoft.com/en-us/dynamics365/fin-ops-core/dev-itpro/migration-upgrade/upgrade-overview-2012)
- [SAP S/4HANA conversion guide](https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE/conversion-guide)
- [Great Expectations](https://docs.greatexpectations.io/)

**Depends on:** wave 48

---

## Wave 50: Scale Hardening, Multi-Region & Production Go-Live

**Phase 9 — Intelligence, Compliance & Go-Live**

**Goal.** Passage runs a real customer in production at enterprise scale with multi-region resilience, documented operations and a public release.

**Replaces**

- *AX / D365:* LCS production environments, Microsoft-managed SLAs
- *SAP:* SAP Basis operations, HA/DR, RISE with SAP

**Deliverable**

- Performance campaign: 10k concurrent users, 1M journal lines/hour, 100k order lines/hour targets; tuning of Postgres (partitioning, CloudNativePG), Kafka, caches
- Multi-region active/passive with cross-region Postgres replication, Kafka MirrorMaker, DNS failover; DR runbooks and game days
- Operations: SRE runbooks, capacity model, cost model per tenant, upgrade strategy (zero-downtime migrations), tenant provisioning automation
- Documentation: administrator guide, implementer guide, API reference, functional guides per module, training curriculum
- Production go-live of the first customer (manufacturer) and public v1.0 release with licensing/pricing

**Proof (testable acceptance)**

- [ ] Load test results published meeting all targets with p99 latencies within SLOs
- [ ] DR game day: region failover completes within RTO 1 h / RPO 5 min with zero data loss
- [ ] First customer runs a full month-end on Passage in production with reconciled close
- [ ] v1.0 tagged, signed, release notes and docs published; roadmap for v2 opened

**Deploy**

Production multi-region environment; first customer live; public release.

**References**

- [CloudNativePG](https://cloudnative-pg.io/documentation/)
- [Kafka MirrorMaker 2](https://kafka.apache.org/documentation/#georeplication)
- [Google SRE workbook](https://sre.google/workbook/table-of-contents/)
- [k6 load testing](https://grafana.com/docs/k6/latest/)

**Depends on:** wave 49

---
