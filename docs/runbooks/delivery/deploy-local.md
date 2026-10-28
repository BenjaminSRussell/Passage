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

## Promote or roll back stage

Stage is a separate Argo CD root application at `deploy/argocd/root-stage.yaml`.
Its child Application reads the chart at the Git revision selected by `main`
and pins the container by SHA-256 digest. A promotion or rollback edits only
`deploy/envs/stage/apps/hello-ledger.yaml`; commit that change and merge it
through a reviewed PR so Argo CD records the merged Git revision in Application
history.

After verifying that a digest was built, scanned, signed, and healthy in dev:

```sh
make -C deploy promote-stage IMAGE_DIGEST=sha256:<64-lowercase-hex-characters>
git diff --check
# Commit the stage manifest change on a branch and merge its PR.
argocd app history hello-ledger-stage
argocd app get hello-ledger-stage
```

To restore the previous distinct, non-placeholder digest recorded in committed
stage-manifest history:

```sh
make -C deploy rollback-stage
git diff --check
# Commit the rollback manifest change on a branch and merge its PR.
argocd app history hello-ledger-stage
argocd app get hello-ledger-stage
```

Both commands refuse an uncommitted stage manifest. Promotion accepts only a
lowercase `sha256:<64 hex characters>` digest. Rollback refuses when Git history
has no prior non-placeholder digest. The stage manifests use a self-signed CA
only if one is provisioned for internal/local proof. The stage cluster must be
registered in Argo CD as `stage`, provide the `passage-stage-ca` ClusterIssuer
and `hello-ledger-db` Secret outside Git, and use a trusted certificate issuer
for real deployments.

## Promote or roll back prod

Prod is the Argo CD root application at `deploy/argocd/root-prod.yaml`, reading
`deploy/envs/prod/apps`. Like stage, it syncs automatically with prune and
self-heal; the reviewed PR merge is the approval gate. Prod promotes only the
digest stage pins on `origin/main`, once stage has pinned it for at least 60
minutes (the script refuses otherwise), so stage and prod consume the same signed
release artifact. `--skip-verify` is refused for prod. The check for zero
`SLOBurnRateHigh` alerts remains manual until the Release table lands.

Prerequisite: register the prod cluster in Argo CD only after a repo admin enables
branch protection on `main` (required review plus code-owner review).
`.github/CODEOWNERS` already covers everything via `*`. Until then any push to
`main` auto-deploys prod.

The 60-minute soak is measured from the committer date of the `origin/main`
commit that pinned the digest in stage. That is reliable for GitHub merges but not
for direct pushes of old commits, which keep their original committer date.

```sh
make -C deploy promote-prod IMAGE_DIGEST=<digest pinned in deploy/envs/stage on main>
# Commit the prod manifest change on a branch and merge its PR.
# Argo syncs on merge: watch root-prod -> hello-ledger-prod until Healthy.
argocd app get hello-ledger-prod
argocd app history hello-ledger-prod
```

To roll back, restore the prior distinct digest from the prod manifest history on
`origin/main` (its signature is re-verified first):

```sh
make -C deploy rollback-prod
# Commit the rollback manifest change on a branch and merge its PR; Argo syncs on merge.
argocd app history hello-ledger-prod
```

The FR-1.10 target is the prior digest active within 5 minutes of the merge.

The prod cluster must be registered in Argo CD as `prod` and provide the
`passage-prod-ca` ClusterIssuer and `hello-ledger-db` Secret outside Git.
