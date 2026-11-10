# NOT-IMPLEMENTED: ops (PSG-50, part 1)

| Item | Reason |
|---|---|
| Executing the failover steps (fence, CNPG promote, MirrorMaker direction, Temporal activation, DNS flip, hash-chain verify) | part 2; they need the clusters and the DNS provider. This service records the plan and flips the region roles; `deploy/multi-region` and the runbooks hold the step mechanics |
| `passage-ops failover --to <region>` command line | part 2; the route exists, the CLI that calls it and reads the plan does not |
| 503 `Retry-After: 30` on every service of a fenced region (BR-50.2, OPS-1703) | each service needs the region role; `/healthz/region` is the signal. Not wired into `platform/go/httpx` |
| A pending-approval flow for the second approver (OPS-1702) | part 1 takes the approver's identity in the request and checks it differs from the caller; it does not yet make the approver sign in and confirm within a window |
| Break-glass for `ops.failover.execute` | duty seeded like any other; the break-glass elevation belongs to identity |
| `POST /v1/ops/gameday`, `GET /v1/ops/slo`, tenant provisioning, tenant cost, license install, masked clone, backup restore drill | later parts of the control plane |
| Lag reporting from the real replicas | `PUT /v1/ops/regions/{name}/lag` accepts it; the reporter that reads CNPG `replay_lag` and MirrorMaker lag is not built |
| `/healthz/region` through the public gateway | the API Gateway requires a JWT; DNS health checks must reach the service through a listener without one, which `deploy/gateway` does not have yet |
| Live proof of any of the above | needs two regions (#213) |
