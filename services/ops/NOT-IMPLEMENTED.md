# NOT-IMPLEMENTED: ops (PSG-50, parts 1 to 3)

Built in part 2: failover step executors behind interfaces with a dry run, the `passage-ops` CLI, the fenced-region 503 in
`platform/go/httpx`, break-glass for `ops.failover.execute`, the lag reporter, `/healthz/region` through its own gateway listener, the
failover scorecard, tenant licences (signed install and state), the `passage_ops_*` and `passage_license_days_to_expiry` metrics, and the restore drill
and masked clone in `tools/tenant-restore`. Built in part 3: `GET /v1/ops/slo`, tenant provisioning (residency check, plan and step
tracking), tenant cost statements, licence user counts with the hourly licence check, the ops outbox and its four events, and the web operations
console. See the README.

| Item | Reason |
|---|---|
| Running the executors against real clusters (CNPG, Strimzi, Temporal, the DNS provider) | needs two live regions (#213). The adapters are tested against a fake Kubernetes API, a fake command runner and a fake webhook only; the CNPG patch shape (`spec.replica: null`, `cnpg.io/fencedInstances`) follows `deploy/multi-region/chart` but has not met a real operator |
| A DNS provider adapter | the provider is not chosen (cloud-agnostic ADR); the executor posts to a provider-neutral webhook (`OPS_DNS_WEBHOOK_URL`) that the provider's Terraform or a small function must serve |
| A ledger hash-chain verification endpoint | the ledger has no verification route. The step runs a configured command (`OPS_CHAIN_VERIFY_CMD`, for example `baseline.py verify`); until the ledger owner ships a per-tenant chain check the real verifier is the game-day tooling |
| Scaling application pods down and back up during a failover | the fence is the CNPG fence plus the 503 in every service; the runbook's pod scale steps stay manual (they need the Argo CD apps of both regions) |
| Rolling `global.regionFenceUrl` into every environment's values | the helper in `passage.cloudEnv` is in every service chart; each environment sets `global.regionFenceUrl` to its ops `/healthz/region`. Not set here: it needs the ops Service URL per environment |
| Break-glass reaching identity's elevated sessions | the window is checked in this service and logged in `ops_break_glass_use`; it is not a role assignment, so identity's effective-permissions view does not show it |
| Game-day scheduling (Temporal workflow with human tasks), fail-back dry run | needs two live regions; the scorecard measures a recorded failover |
| The fence for Kafka consumers and background workers | the 503 covers the HTTP API of services on `httpx`; finreport's share-link download is wrapped in `httpx.FenceMiddleware`; consumers and background workers are stopped by the database fence and by scaling them to zero (runbook "What the fence does not cover") |
| Fence at start-up when the role cannot be read | a pod that starts while `/healthz/region` is unreachable or answers no role serves as not fenced (fail open) and keeps the last state afterwards; failing closed would take a healthy region down whenever ops restarts |
| Licence rollback beyond `issuedAt` | an older licence cannot replace a newer one (issuedAt is the sequence); a licence reissued the same day, or a signed licence with a later issuedAt but worse terms, is accepted: the signer's discipline is the control |
| Provisioning step executors (Keycloak realm, databases and RLS schemas, Kafka ACLs, lakehouse prefixes) and tenant decommission | the API records the plan, enforces the residency (`platform/go/residency`, OPS-1705) and tracks the steps a runner reports; the runner that performs them needs the Keycloak admin client, the database owner role and the Kafka admin API of a live cluster. Decommission (export, second approver, drop) is not started |
| The monthly metering job | `PUT /v1/ops/tenants/{id}/costs/{month}` takes the six quantities; the job that reads them from Prometheus (container CPU and memory by tenant label, CNPG volume per schema, topic bytes, object-store prefixes, model tokens) needs those series labelled by tenant in a live cluster. PDF statements are not produced (JSON and CSV are) |
| The licence user-count reporter | `PUT /v1/ops/tenants/{id}/license/usage` takes the counts; identity holds the users but has no cross-tenant count, and no licence classes (full, operational, self-service) on a user yet. Until a reporter runs, the hourly check evaluates the dates only (a tenant never counted has zero active users) |
| SLO targets per operation (FR-50.1 rows) | the report is per service (Prometheus job) with one availability and one p95 target, and per-service p95 overrides (`SLO_LATENCY_P95_BY`); per-route objectives need the recording rules of the observability chart |
| Relaying the ops outbox | `failover.completed`, `tenant.provisioned`, `slo.breached` and `license.changed` are written to the ops outbox in the transaction of the change, but `deploy/kafka/connect.yaml` has no `outbox-ops` connector yet (nor its `debezium-cdc-ops` secret and topic ACLs), so nothing publishes them. Adding it needs the ops database's publication and CDC role in each environment |
| Masked-clone scheduling | not started; the masked clone itself is `tools/tenant-restore` |
| Live proof of any of the above | needs two regions (#213) |
