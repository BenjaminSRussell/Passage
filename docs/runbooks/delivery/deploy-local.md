# Deploy locally

Brings up a k3d cluster (1 server, 3 agents), cert-manager, Argo CD, a dev Postgres and hello-ledger.

## Prerequisites

Docker, k3d, OpenTofu (`tofu`), kubectl, openssl, and a 16 GB machine. Ports 80 and 443 must be free. Argo CD syncs from GitHub, so the chart must exist on the revision you deploy.

## Run

```
make -C deploy deploy-local
make -C deploy deploy-local REVISION=my-branch
```

The target builds the image from `services/platform-health/Dockerfile`, imports it into k3d, creates the `hello-ledger-db` Secret with a random password (never committed), and applies the local root Application.

## Reach the service

Add `127.0.0.1 app.local.passage.dev` to `/etc/hosts`, then open `https://app.local.passage.dev/readyz`. The ingress exposes only `/healthz` and `/readyz`; call the entries API with `kubectl -n hello-ledger port-forward svc/hello-ledger 8080:8080` and an `X-Passage-Tenant` header.

Trust the local CA to remove the browser warning:

```
kubectl -n cert-manager get secret passage-local-ca -o jsonpath='{.data.ca\.crt}' | base64 -d > passage-local-ca.crt
```

## Troubleshoot

| Symptom | Check |
|---|---|
| `tofu apply` fails on provider | `provider_kind` other than `k3d` is not implemented |
| pod `CreateContainerConfigError` | Secret `hello-ledger-db` missing in namespace `hello-ledger` |
| Argo app `Unknown` | the revision is not pushed to GitHub, or the repository is private and needs credentials in Argo CD |
| readiness 503 | Postgres pod not ready: `kubectl -n hello-ledger get pods` |

## Tear down

`make -C deploy destroy-local`
