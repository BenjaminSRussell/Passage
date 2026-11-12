# DoD IL5 (Azure Government) and IL6 readiness

Passage for US Department of Defense customers at Impact Level 5, on Microsoft 365 DoD and Azure Government (#603). IL5 builds on [GCC High](gcc-high.md): the same Azure Government endpoints, FIPS builds, private cluster, in-boundary egress and HSM-wrapped keys, plus compute isolation and customer-managed keys everywhere they apply. The last section covers IL6 (SECRET, Azure Government Secret), which is built only when a classified customer is under contract.

## What the `dod` profile adds to `gcc-high`

| Setting | Value | Where it is enforced |
|---|---|---|
| Graph | `https://dod-graph.microsoft.us` (authority stays `login.microsoftonline.us`) | Profile table. The render test fails on any commercial authority, `*.windows.net`, `*.azure.com` or `ghcr.io`. |
| `allowedRegions` | `usdodeast`, `usdodcentral`, `usgovvirginia`, `usgovarizona`, `usgovtexas` | `check_region_pair.py --cloud-profile dod` and the OpenTofu `azure_location` validation. |
| Compute isolation | `isolatedNodes` in `usgovvirginia`, `usgovarizona`, `usgovtexas` | US DoD East and Central are reserved for DoD and need nothing extra. In the US Gov regions every node pool must set `isolated = true` with an isolated VM size, or `dedicated_host_group`. Otherwise the plan fails a precondition on the cluster that names the non-isolated pools (`deploy/tofu/cluster/modules/aks/main.tf`; the `dod_*` runs in `tests/aks.tftest.hcl`). |
| Customer-managed keys | on | See below. |

Node pools in `deploy/tofu/cluster` take `name`, `count`, `vm_size`, `isolated` and `dedicated_host_group`. Azure's isolated sizes are listed in the module (`isolated_vm_sizes`). Add a size there only after Microsoft lists it as isolated.

## Customer-managed keys

With `customer_managed_keys = true` (`deploy/tofu/cluster/modules/aks/cmk.tf`), everything the IL5 guidance names is encrypted with keys the customer controls:

| Resource | Key | Key store |
|---|---|---|
| AKS etcd (KMS plugin) | `etcd` | Key Vault Premium (RSA-HSM); the KMS plugin takes only a Key Vault key id |
| AKS OS and data disks | disk encryption set | Managed HSM when `managed_hsm`, else Key Vault Premium |
| Backup storage account | `storage`, set when the account is created, before any data is written | Managed HSM when `managed_hsm`, else Key Vault Premium |
| Container registry | `acr` | Key Vault Premium |
| Azure Database for PostgreSQL | `postgres` | Key Vault Premium |
| Log Analytics (dedicated cluster) | `loganalytics` | Key Vault Premium |
| fieldcrypt master-key wrapping | `fieldcrypt` | Managed HSM when `managed_hsm` |

Every key has a rotation policy (one year, rotated 90 days before expiry). The resources read the versionless key id, so a new version is picked up without an apply or a restart. The module's offline tests (`gcc_high_builds_a_private_cluster_with_customer_managed_keys_in_a_managed_hsm` and the commercial negative case) check that the keys and the encrypted resources are planned. Rotation on a real subscription is a live proof (#213).

## Deploying

As for [GCC High](gcc-high.md), with `-var-file=../../cloud-profiles/tofu/dod.tfvars.json` and `-f deploy/cloud-profiles/values/dod.yaml`. In a US Gov region, give every node pool an isolated size or a dedicated host group. Use hardened node images (the CIS-hardened AKS node image checked against the DISA STIG, or a mission-owner image) and apply the DISA Kubernetes STIG to the cluster configuration.

## RMF package inputs

Passage as a cloud service for DoD needs a DoD provisional authorization, or a mission-owner ATO under DoD RMF (DoDI 8510.01) with FedRAMP High plus the DoD FedRAMP+ controls as the baseline. This repository supplies:

- **System boundary.** The AKS cluster, its private endpoints (Key Vault, Managed HSM, Blob, ACR, PostgreSQL) and the in-boundary alert receiver. Outside the boundary: the customer's Entra ID tenant (Microsoft 365 DoD) and their browsers. Nothing else, because the egress allowlist render test proves the charts call no other host.
- **Data flows.** Browser to the gateway (TLS) to services. Services to PostgreSQL (TLS `verify-full`), Kafka and Temporal inside the cluster. Backups to Blob in the same region. Keycloak brokers to Entra ID (`login.microsoftonline.us`). `agents-ts` calls Azure OpenAI in Azure Government only. Alerts go to the in-boundary webhook.
- **Ports, protocols and services.** HTTPS 443 at the gateway. Inside the cluster, each service's HTTP and metrics ports as its chart declares them, PostgreSQL 5432 with TLS, and Kafka and Temporal on cluster-internal services. Egress is HTTPS 443 to the profile's Azure Government endpoints only. Generate the PPSM table for the package from the rendered charts.
- **Hardening.** STIG-hardened node images, the Kubernetes STIG, FIPS mode on nodes and binaries, signed images verified by Kyverno at admission.

## Control inheritance

| Area | Azure Government (PA) | Passage | Mission owner |
|---|---|---|---|
| Physical, environmental, platform maintenance | inherited | | |
| Hypervisor and compute isolation | dedicated hosts or isolated sizes | selects isolated pools (plan fails otherwise) | approves region choice |
| Encryption at rest | HSM (FIPS 140 Level 3) | customer-managed keys on every resource, field-level encryption | holds key administration (Managed HSM security domain) |
| Encryption in transit | TLS endpoints | TLS everywhere, FIPS builds | |
| Identity | Entra ID (Azure Government) | Keycloak broker, IdP MFA trust, duties and RLS | CAC/PIV, user lifecycle, Conditional Access |
| Audit | platform logs | change trail, PHI read log, Keycloak events, retention | log review, retention period |
| Configuration and supply chain | | everything as code, signed images with SBOM, drift tests | change approval |
| Incident response | platform incidents | runbooks, alerting in the boundary | DoD reporting chain |

## Personnel

Microsoft staff with elevated access to DoD IL5 data hold DoD IT-2 adjudication (OPM Tier 3). Passage operators with production access need comparable vetting, set in the customer contract: at least the GCC High US-person policy plus the adjudication level the contract names. Access is by named identity with MFA and just-in-time elevation, and it is logged.

## IL6 readiness (gate: classified customer signed)

IL6 runs in Azure Government Secret, which connects only to SIPRNet and is operated by cleared US citizens. There is no public internet: no GitHub, ghcr, npm, PyPI, Docker Hub or SaaS. Service availability comes from the Microsoft account team, so every dependency must be confirmed against what Azure Government Secret offers before work starts.

**Offline install bundle** (`deploy/offline/offline_bundle.py`, tests in `make -C deploy test-offline`):

```
docker run -d -p 5000:5000 registry:2                      # throwaway staging registry
make offline-bundle KEY=cosign.key OUT=dist/offline       # on a connected build machine
python3 deploy/offline/offline_bundle.py verify dist/offline/passage-offline.tar.gz --key cosign.pub
python3 deploy/offline/offline_bundle.py install dist/offline/passage-offline.tar.gz \
    --registry registry.enclave:5000 --git-url https://git.enclave/passage.git --key cosign.pub
```

- `build` collects every image the in-repo charts render plus `deploy/offline/extra-images.txt` (operator images of third-party charts) and pins each by digest. For each image it checks the publisher's signature against `deploy/offline/provenance.json` (the expected keyless identity, or a key; an image marked `unsignedUpstream` records the reason instead). An image or chart with no entry fails the build. It then copies the image into a throwaway staging registry, writes an SPDX SBOM with `syft`, signs the digest and attests the SBOM with the offline key (`cosign sign` and `cosign attest --type spdxjson`, no transparency log), and saves the image with its signature and attestation (`cosign save`). Every in-repo chart is packaged. Third-party charts are pulled with `helm pull --verify --keyring` when `provenance.json` names a keyring, or checked against a pinned sha256. None of them has a confirmed keyring yet, so they are marked `unverified` until `offline_bundle.py pin-charts` pins their archives on the connected build machine. Any failing step stops the build. The archive also holds the offline Kyverno policies, the OPA bundle, `manifest.json` (with each image's upstream state and each chart's provenance) and `SHA256SUMS`, and it is signed with `cosign sign-blob`.
- `verify` checks the archive signature against the offline public key (no transparency log), then every hash, that no file is unlisted, and that every image is digest-pinned with its signature and SBOM. Extraction refuses paths outside the target and links.
- `install` verifies, then pushes images (with signatures) and charts into the enclave registry. It writes the Argo CD roots and Applications with every Git source rewritten to the in-enclave mirror and every chart repository to the enclave OCI registry, and renders the offline Kyverno policy with the same public key.
- **Kyverno offline trust root**: `deploy/policies/admission-offline/verify-signed-images.yaml` replaces the keyless policy (which needs Fulcio, Rekor and GitHub's OIDC issuer). It verifies the image signature and the SBOM attestation against a static public key, requires digest-pinned images from the enclave registry, and enforces. `offline_bundle.py render-policies --key cosign.pub --registry <host> --out <dir>` renders it on its own.
- **In-enclave Git mirror**: push this repository to the enclave Git server and give its URL as `--git-url`. Argo CD then syncs only from there.

Still live, tracked on #213: a CI run that installs the bundle on a cluster with egress blocked, and an install inside an enclave. A signing key held in an HSM (`KEY` may be a KMS or HSM URI for `cosign`) and the classified-environment dependency review are part of the customer engagement.

## Not proven here

A real Azure Government apply with isolated pools, key rotation on live resources, CAC/PIV sign-in through Microsoft 365 DoD, the RMF package and the authorization (#213, #512).
