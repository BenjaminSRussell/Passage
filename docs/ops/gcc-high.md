# GCC High (Azure Government)

Passage for customers on Microsoft 365 GCC High: defense contractors and others holding CUI or ITAR/EAR-controlled data (#602). Their identity is in Entra ID in Azure Government, and Passage runs in Azure Government with a FedRAMP High-equivalent posture. Tokens from Azure Government are not accepted by the commercial cloud and an app is registered separately in each cloud's portal, so a GCC High deployment uses the `gcc-high` [cloud profile](cloud-profiles.md) end to end. DoD IL5 builds on this page: [dod-il5.md](dod-il5.md).

## What the `gcc-high` profile does

| Setting | Value | Where it is enforced |
|---|---|---|
| Entra authority, portal, Graph | `login.microsoftonline.us`, `portal.azure.us`, `graph.microsoft.us` | `deploy/keycloak/idp/entra-oidc.gcc-high.json` is rendered from the profile. `test_egress_render.py` renders every chart under `gcc-high` and fails on `login.microsoftonline.com`, any `*.windows.net` or `*.azure.com` host, and `ghcr.io`. |
| Azure endpoints | ARM `management.usgovcloudapi.net`, Key Vault `*.vault.usgovcloudapi.net`, Blob `*.blob.core.usgovcloudapi.net`, ACR `*.azurecr.us`, PostgreSQL `*.postgres.database.usgovcloudapi.net`, Azure OpenAI `*.openai.azure.us` | The profile table, `deploy/cloud-profiles/profiles.json`, with a Microsoft Learn source per value. The Managed HSM suffix `managedhsm.usgovcloudapi.net` is marked `unverified`: Microsoft's private-endpoint DNS page lists Managed HSM only for the commercial cloud. Confirm it in a Government subscription before relying on it. |
| `allowedRegions` | `usgovvirginia`, `usgovarizona`, `usgovtexas` | `check_region_pair.py --cloud-profile gcc-high` and the OpenTofu `azure_location` validation. |
| `fips` | true | Go images build with `GOFIPS140=v1.0.0` (CMVP certificate #5247) and refuse to start with FIPS off (`cloudprofile.CheckFIPS` in `httpx.Serve`). Keycloak runs `--features=fips --fips-mode=strict` with BouncyCastle FIPS (`deploy/keycloak/Dockerfile.fips`). The realm uses `pbkdf2-sha512` with 14-character passwords. |
| Deployment | private cluster, customer-managed keys, Managed HSM, Postgres `sslmode=verify-full` | `deploy/cloud-profiles/tofu/gcc-high.tfvars.json` turns these on in `deploy/tofu/cluster`. `platform/go/dbx` refuses a database URL weaker than `verify-full` under the profile (`Profile.CheckPostgresConn` in `platform/go/cloudprofile`). |
| Registry | mirror only | Charts refuse to render without `global.imageRegistry`. `ghcr.io` is not in the allowlist. |
| Egress | Azure Government endpoints only | No PagerDuty, Anthropic, OpenAI or Bedrock. Observability renders an in-boundary alert webhook (`alertWebhook` in the observability chart) instead of PagerDuty. |
| LLM | Azure OpenAI in Azure Government (`*.openai.azure.us`) in an allowed region | `agents-ts` refuses any other provider endpoint, a plain-http endpoint, and an endpoint without `AGENTS_PROVIDER_REGION`. Every run records provider, host and region (`agent_run.model_*`). |

## Deploying

1. `tofu apply` in `deploy/tofu/cluster` with `-var provider_kind=aks -var-file=../../cloud-profiles/tofu/gcc-high.tfvars.json`, an `azure_location` from the list above and an Azure Government subscription. The module sets `environment = "usgovernment"` on the provider and creates a private AKS cluster with FIPS node pools, private endpoints for Key Vault, Blob, ACR, the Managed HSM and (with `azure_postgres_flexible_server`) PostgreSQL, the backup storage account and registry with customer-managed keys, and a Managed HSM for the disk, storage and fieldcrypt keys (see [azure-aks.md](azure-aks.md); `azure_managed_hsm_admin_object_ids` and `azure_managed_hsm_security_domain_certificate_ids` are required). A private cluster has no public API endpoint: run the apply from inside the VNet, or set `install_cluster_addons = false` and install cert-manager and Argo CD from a runner that can reach it.
2. Mirror the images into the registry the module created (`<acr>.azurecr.us`) with their cosign signatures, and build the Go images with `--build-arg GOFIPS140=v1.0.0`.
3. Install every chart with `-f deploy/cloud-profiles/values/gcc-high.yaml --set global.imageRegistry=<acr>.azurecr.us`. Set `global.workloadIdentity.fieldcryptClientId` to the module's `fieldcrypt_identity_client_id` output, so the services that unwrap fieldcrypt keys (hr, party, payables, receiving, sales, compliance, migration) run as the federated service accounts. Point `alertWebhook.url` at an in-boundary receiver, such as an Azure Monitor action group webhook or a cluster-internal bridge.
4. Render the realm for the profile, with the option that trusts the IdP's MFA: `cd platform/go && go run ./cloudprofile/cmd/cloudprofile realm-patch -profile gcc-high -trust-idp-mfa -realm ../../deploy/keycloak/realm-passage.json`. The patch copies the IdP's `amr` and `acr` claims into user attributes at every login. The post-broker flow then skips Passage's second factor when `amr` shows MFA (`mfa` or `hwk` by default; `-amr-regex` sets another pattern, for example one that also accepts a certificate sign-in), and runs it otherwise. CI imports this patch into strict FIPS Keycloak.
5. Federate the customer's Entra tenant through `login.microsoftonline.us` ([entra-id-federation.md](entra-id-federation.md)). Entra Trusted IPs and Azure AD B2C are not available in Azure Government: use Conditional Access named locations. GCC High users can share only with other GCC High organizations, so Passage does not rely on cross-cloud guest users.

fieldcrypt master keys are stored wrapped (`FIELDCRYPT_KEYS` entries `id=wrapped:...`) by the key at `FIELDCRYPT_AZURE_KEY_ID`, which `platform/go/fieldcrypt/azurekv` accepts on the profile's Key Vault or Managed HSM suffix. To rotate without downtime, add the new master key id to `FIELDCRYPT_KEYS`, make it current (`FIELDCRYPT_CURRENT_KEY_ID`), and reseal where a service offers it (`hr reseal-pii`). New values seal under the new id, and old values still open with the old id, so nothing has to stop. The HSM key itself rotates by policy (one year, 90 days before expiry), and services read the versionless key id.

## Authorization path

Under DFARS 252.204-7012, a cloud service that stores, processes or transmits covered defense information must meet the FedRAMP Moderate baseline or an equivalent. Phase 1 of the CMMC DFARS rule took effect on 2025-11-10. Passage hosted for a contractor is such a service. The plan:

1. **FedRAMP Moderate equivalency** (DoD CIO memorandum): a System Security Plan scoped to the `gcc-high` profile, a 3PAO assessment against the full Moderate baseline, and a POA&M. A contractor can show this to its assessor.
2. **FedRAMP High** for a customer that needs it, reusing the same evidence.

The 3PAO assessment and the sign-off are tracked on #512. The live deployment proof (a real Azure Government apply, CAC/PIV sign-in in a test tenant) is tracked on #213.

## NIST SP 800-171 Rev. 2: the parts Passage owns

Passage provides these. The contractor still owns its own users, endpoints and policies, and Microsoft owns the Azure Government platform.

| Family | Requirement (examples) | Passage control |
|---|---|---|
| 3.1 Access control | 3.1.1, 3.1.2 authorized users and functions; 3.1.5 least privilege; 3.1.12 remote access | Per-tenant realms, duty-based authorization (`policies/authz`), RLS on every table, private cluster and private endpoints. |
| 3.3 Audit and accountability | 3.3.1 audit records; 3.3.2 traceability to a user; 3.3.8 protect audit information | Hash-chained change trail and PHI read log (`services/audit`), Keycloak login and admin events kept 365 days, logs inside the boundary. |
| 3.4 Configuration management | 3.4.1 baselines; 3.4.2 security settings | Everything is code: charts, OpenTofu, profile table with drift tests, signed images. |
| 3.5 Identification and authentication | 3.5.3 MFA; 3.5.7 to 3.5.10 password rules, cryptographically protected passwords | Second factor after brokered login, or the IdP's MFA when trusted. PBKDF2-SHA-512 with 14-character minimum. |
| 3.8 Media protection | 3.8.9 protect backups | Backups to Blob storage in the region, encrypted with customer-managed keys. |
| 3.13 System and communications protection | 3.13.1 boundary protection; 3.13.8 encryption in transit; 3.13.11 FIPS-validated cryptography; 3.13.16 data at rest | Egress allowlist render test, TLS with `verify-full` to Postgres, GOFIPS140 and BCFIPS, field-level encryption with HSM-wrapped keys. |
| 3.14 System and information integrity | 3.14.1 flaw remediation; 3.14.6 monitoring | Image scanning in CI, alerting to an in-boundary receiver. |

## Operator and support policy (draft for approval on #512)

- Only **US persons** who have passed a background check have access to production data or the production cluster. Microsoft screens its own staff with access to GCC High content (US citizenship, seven-year background check, fingerprints, OFAC/BIS/DDTC list checks). Passage keeps an equivalent list for its operators.
- Support tickets must not carry CUI. The support process asks for tenant and request ids, never record contents, and screenshots are refused.
- Operators of other nationalities work on code and non-production systems with synthetic data only.

## Naming and incident reporting

- **CUI-free resource names.** Microsoft notes that Azure resource names are not covered by the data-handling commitments. Name resources by environment and purpose (`passage-prod-usgovvirginia`), never by customer program or contract. The OpenTofu `name` variable is the only free-text part.
- **Incident reporting.** DFARS 252.204-7012 requires a cyber incident affecting covered defense information to be reported to DoD within 72 hours of discovery, through the DIBNet portal. Passage's part is to tell the affected contractor at once, with what is known, so they can report in time, and to preserve images and logs for 90 days.

## Not proven here

A real Azure Government apply, the Managed HSM suffix, CAC/PIV sign-in through a test tenant, the 3PAO assessment and the sign-off all need infrastructure or people outside this repository (#213, #512).
