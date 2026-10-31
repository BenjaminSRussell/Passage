# Passage operations documentation — PSG-50, wave 50

Operator-facing documents for scale, DR, release and go-live.

| Document | Purpose |
|---|---|
| [administrator-guide.md](administrator-guide.md) | Operator guide: duties, regions, failover, tenants, licenses, releases, alerts, error codes (FR-50.11) |
| [tenant-administrator-guide.md](tenant-administrator-guide.md) | Customer administrator guide: duties, licence states, user types, SoD, cost statement, release rings, export (FR-50.11) |
| [implementer-guide.md](implementer-guide.md) | Migration, localization, parallel run, cutover and hypercare for implementers (FR-50.11) |
| [capacity-model.md](capacity-model.md) | Workload inputs to nodes, Postgres, Kafka (FR-50.9) |
| [cost-model.md](cost-model.md) | Per-tenant metering, USD/EUR, accuracy target (BR-50.10) |
| [upgrade-strategy.md](upgrade-strategy.md) | Rolling deploys, expand/contract, N/N-1 compatibility (FR-50.7) |
| [tenant-provisioning.md](tenant-provisioning.md) | Provision and decommission steps (FR-50.8, BR-50.9) |
| [go-live-checklist.md](go-live-checklist.md) | 14-item cutover go/no-go and hypercare exit (9.50.1) |
| [functional-guides/](functional-guides/) | Functional guides per phase, each wave with its owning service and web module: [phase 1](functional-guides/phase-1-master-data.md) master data, [phase 2](functional-guides/phase-2-financial-core.md) financial core, [phase 3](functional-guides/phase-3-procure-to-pay.md) procure-to-pay, [phase 4](functional-guides/phase-4-manufacturing.md) manufacturing, [phase 5](functional-guides/phase-5-order-to-cash.md) order-to-cash, [phase 6](functional-guides/phase-6-logistics.md) warehouse, quality and logistics, [phase 7](functional-guides/phase-7-treasury-controlling.md) treasury, assets and controlling, [phase 8](functional-guides/phase-8-projects-people-service.md) projects, people and service (FR-50.11) |
| [training-curriculum.md](training-curriculum.md) | Six role-based training tracks with labs and assessments (FR-50.11) |
| [runbooks/](runbooks/) | Failover, fail-back, game day, replication lag, MM2 lag, slot bloat, SLO burn, availability burn, p95 latency, license expiry, release rollback; Kafka broker replacement, Postgres primary loss, cache stampede, Temporal replication lag, certificate expiry, object-store lag; ledger hash chain break, backup drill failure, cost meter drift, expand/contract violation |
| [release-notes-template.md](release-notes-template.md) | Release notes layout (digests, features by module, migrations, breaking changes, CVE fixes, rollback), gated by `tools/check_release_notes.py` (PRD 9.50.15) |
| [tools/](tools/) | `check_docs.py` (links, anchors, alert runbooks, repository paths named in the docs), `api_reference.py` (API reference pages and the 0-undocumented gate), `asyncapi_reference.py` (event reference pages and the undocumented-event gate), `check_release_notes.py`, `cost_meter.py` (tenant statements, COST-1901), `license_state.py` (VALID/GRACE/EXPIRED rules), `capacity_estimate.py` (capacity model); tests: `python3 -m unittest discover -s docs/ops/tools` |
| [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md) | Deliverables not built yet (slices 1 to 9 shipped), with status and reason |

Related: [deploy/multi-region](../../deploy/multi-region/README.md) (chart and region values),
[load](../../load/README.md) (k6 campaign and checker).

Every alert in `deploy/multi-region/chart/templates/prometheusrule.yaml` links to a page under `runbooks/`;
`deploy/multi-region/test_chart.py` fails if a linked page is missing.
