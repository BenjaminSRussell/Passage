# Passage operations documentation — PSG-50, wave 50

Operator-facing documents for scale, DR, release and go-live.

| Document | Purpose |
|---|---|
| [administrator-guide.md](administrator-guide.md) | Operator guide: duties, regions, failover, tenants, licenses, releases, alerts, error codes (FR-50.11) |
| [capacity-model.md](capacity-model.md) | Workload inputs to nodes, Postgres, Kafka (FR-50.9) |
| [cost-model.md](cost-model.md) | Per-tenant metering, USD/EUR, accuracy target (BR-50.10) |
| [upgrade-strategy.md](upgrade-strategy.md) | Rolling deploys, expand/contract, N/N-1 compatibility (FR-50.7) |
| [tenant-provisioning.md](tenant-provisioning.md) | Provision and decommission steps (FR-50.8, BR-50.9) |
| [go-live-checklist.md](go-live-checklist.md) | 14-item cutover go/no-go and hypercare exit (9.50.1) |
| [training-curriculum.md](training-curriculum.md) | Six role-based training tracks with labs and assessments (FR-50.11) |
| [runbooks/](runbooks/) | Failover, fail-back, game day, replication lag, MM2 lag, slot bloat, SLO burn, license expiry, release rollback; Kafka broker replacement, Postgres primary loss, cache stampede, Temporal replication lag, certificate expiry, object-store lag |
| [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md) | Deliverables not built yet (slices 1 to 3 shipped), with status and reason |

Related: [deploy/multi-region](../../deploy/multi-region/README.md) (chart and region values),
[load](../../load/README.md) (k6 campaign and checker).

Every alert in `deploy/multi-region/chart/templates/prometheusrule.yaml` links to a page under `runbooks/`;
`deploy/multi-region/test_chart.py` fails if a linked page is missing.
