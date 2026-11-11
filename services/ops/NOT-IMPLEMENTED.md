# NOT-IMPLEMENTED: ops (PSG-50, parts 1 and 2)

Built in part 2: failover step executors behind interfaces with a dry run, the `passage-ops` CLI, the fenced-region 503 in
`platform/go/httpx`, break-glass for `ops.failover.execute`, the lag reporter, `/healthz/region` through its own gateway listener, the
failover scorecard, tenant licences (signed install and state), the `passage_ops_*` and `passage_license_days_to_expiry` metrics, and the restore drill
and masked clone in `tools/tenant-restore`. See the README.

| Item | Reason |
|---|---|
| Running the executors against real clusters (CNPG, Strimzi, Temporal, the DNS provider) | needs two live regions (#213). The adapters are tested against a fake Kubernetes API, a fake command runner and a fake webhook only; the CNPG patch shape (`spec.replica: null`, `cnpg.io/fencedInstances`) follows `deploy/multi-region/chart` but has not met a real operator |
| A DNS provider adapter | the provider is not chosen (cloud-agnostic ADR); the executor posts to a provider-neutral webhook (`OPS_DNS_WEBHOOK_URL`) that the provider's Terraform or a small function must serve |
| A ledger hash-chain verification endpoint | the ledger has no verification route. The step runs a configured command (`OPS_CHAIN_VERIFY_CMD`, for example `baseline.py verify`); until the ledger owner ships a per-tenant chain check the real verifier is the game-day tooling |
| Scaling application pods down and back up during a failover | the fence is the CNPG fence plus the 503 in every service; the runbook's pod scale steps stay manual (they need the Argo CD apps of both regions) |
| Rolling `global.regionFenceUrl` into every environment's values | the helper in `passage.cloudEnv` is in every service chart; each environment sets `global.regionFenceUrl` to its ops `/healthz/region`. Not set here: it needs the ops Service URL per environment |
| Break-glass reaching identity's elevated sessions | the window is checked in this service and logged in `ops_break_glass_use`; it is not a role assignment, so identity's effective-permissions view does not show it |
| Game-day scheduling (Temporal workflow with human tasks), fail-back dry run | needs two live regions; the scorecard measures a recorded failover |
| `GET /v1/ops/slo`, tenant provisioning automation, tenant cost statements, masked-clone scheduling | not started. The rules exist as `docs/ops/tools/cost_meter.py` (cost) and `docs/ops/tenant-provisioning.md` (provisioning steps); the SLO report needs per-route histograms from a Prometheus query layer |
| Licence user counts | the ops service holds the terms, not the tenant's active users; the caller passes `activeFull` and the hourly licence check is not scheduled |
| Events `dev.passage.ops.failover.completed.v1`, `tenant.provisioned.v1`, `slo.breached.v1`, `license.state_changed.v1` | not emitted; the ops service has no outbox yet |
| Live proof of any of the above | needs two regions (#213) |
