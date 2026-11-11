# Microsoft 365 GCC

Passage for customers on Microsoft 365 GCC (Government Community Cloud): US federal, state, local and tribal governments and their contractors (#601). GCC is a Microsoft 365 environment, not a separate Azure cloud. A GCC customer's identity is in the worldwide Entra ID, so Microsoft's endpoints are the commercial ones, while the customer brings US-government data-handling expectations. The `gcc` [cloud profile](cloud-profiles.md) encodes that.

## What the `gcc` profile does

| Setting | Value | Why |
|---|---|---|
| Entra authority, portal, Graph | `login.microsoftonline.com`, `portal.azure.com`, `graph.microsoft.com` | Microsoft Graph's documentation says to keep the worldwide endpoints in a Microsoft 365 GCC environment. The profile must not switch to `.us`; a test fails if a gcc render names a `.us` authority. |
| `allowedRegions` | US Azure commercial regions (`eastus`, `eastus2`, `centralus`, `northcentralus`, `southcentralus`, `westcentralus`, `westus`, `westus2`, `westus3`) | Data, backups, logs, telemetry and LLM calls stay in the US. `check_region_pair.py --cloud-profile gcc` and the OpenTofu module reject anything else. |
| `fips` | true | FedRAMP baselines (SC-13) need FIPS-validated cryptography. Go images build with `GOFIPS140=v1.0.0` and refuse to start with FIPS off; Keycloak runs BouncyCastle FIPS in strict mode. |
| Registry | mirror only | Charts refuse to render without `global.imageRegistry`; `ghcr.io` is not in the allowlist. |
| Egress | Entra, Graph, ARM, Key Vault, Blob, ACR, Postgres, Azure OpenAI | No PagerDuty, Anthropic, OpenAI or Bedrock. A test renders every chart under `gcc` and fails on any other host. |
| LLM | Azure OpenAI in a US region | `agents-ts` refuses any provider endpoint outside the allowlist, and an endpoint whose region is not in `allowedRegions`. |
| Keycloak | `pbkdf2-sha512`, 14-character passwords, login and admin events kept 365 days | Keycloak's default argon2 is not supported in FIPS mode and strict mode needs 14 characters. |

## Deploying

1. `tofu apply` with `-var provider_kind=aks -var-file=deploy/cloud-profiles/tofu/gcc.tfvars.json` and a US `azure_location` ([azure-aks.md](azure-aks.md)).
2. Mirror the images into the ACR the module created, build the Go images with `--build-arg GOFIPS140=v1.0.0` and Keycloak from `deploy/keycloak/Dockerfile.fips`, and deploy `deploy/keycloak-fips`.
3. Install every chart with `-f deploy/cloud-profiles/values/gcc.yaml --set global.imageRegistry=<acr>.azurecr.io`. Observability renders no PagerDuty receiver under this profile: route alerts to a receiver inside the boundary (for example an Azure Monitor action group or an internal webhook).
4. Use the US pair `deploy/multi-region/values/us` (East US 2 primary, Central US passive); backups go to Blob storage in each region (`python3 deploy/multi-region/tools/check_region_pair.py --values-dir deploy/multi-region/values/us --cloud-profile gcc`).
5. Federate the customer's Entra tenant ([entra-id-federation.md](entra-id-federation.md)).

## Authorization path

Using a GCC customer's tenant gives Passage none of Microsoft's authorization. Microsoft documents Office 365 Government (GCC) as supporting FedRAMP at the High impact level; many state and local systems are categorized Moderate. Passage needs its own decision, and the plan this repository is built for is:

1. **Agency ATO first.** A sponsoring agency authorizes Passage for its own use: a System Security Plan (SSP) scoped to the `gcc` profile, the control-responsibility matrix below, a 3PAO or agency assessment, and a POA&M. This is the fastest route to a first GCC customer.
2. **FedRAMP Moderate (then High if a customer needs it)** for Passage as SaaS once an agency sponsor exists, reusing the same SSP and evidence.

The human steps (choosing the sponsor, the 3PAO assessment, the authorizing official's signature) are tracked on #512; this repository supplies the technical baseline and evidence (FIPS builds, region pinning, egress allowlist, audit logging, tests in CI).

## Operator access policy (draft for approval on #512)

- Only **US persons** (citizens or lawful permanent residents) who have passed a background check may have access to production data or the production cluster. Microsoft's GCC screening (US citizenship, seven-year background check, FBI fingerprinting) applies to Microsoft staff; GCC support personnel are not covered by it, so Passage keeps its own list.
- Access is by named Entra identity with MFA and just-in-time elevation, logged to the audit chain (`services/audit`) and Keycloak admin events.
- **CJIS option:** for tenants handling criminal-justice information, only staff who have completed the state's CJIS fingerprint-based screening and security awareness training may access that tenant's data; a tenant flag records the requirement. Passage does not claim CJIS compliance until a state agreement is in place.
- Operators of other nationalities may work on the code and on non-production systems with synthetic data only.

## Logging and retention

FedRAMP AU controls need centralized audit logs with retention the agency sets. Passage keeps its own audit chain (`services/audit`) and the realm's login and admin events (`eventsEnabled`, `adminEventsEnabled`, `eventsExpiration` 365 days, covering every profile; the `hipaa` overlay's six years needs a realm patch, see [cloud-profiles.md](cloud-profiles.md)). Entra sign-in and audit logs live in the customer's tenant. Ship Keycloak's `jboss-logging` events and service logs to a store inside the boundary and set retention to the agency's value.

## NIST SP 800-53 control responsibility (Rev. 5, Moderate baseline families)

M = Microsoft (Azure, Microsoft 365 GCC), P = Passage, C = customer. A control with more than one letter is shared; the SSP will split each into its parts.

| Family | Responsibility | Passage's part in this repository |
|---|---|---|
| AC Access control | P, C, M | Role and duty authorization (OPA, `policies/authz`), tenant isolation by RLS and one realm per tenant, SCIM deprovisioning; the customer manages its Entra users and Conditional Access; Microsoft secures the platform's own access. |
| AT Awareness and training | P, C, M | Operator training policy (US persons, CJIS option); the customer trains its users. |
| AU Audit and accountability | P, C, M | Audit chain, Keycloak login and admin events with set retention; the customer retains Entra logs; Microsoft logs the platform. |
| CA Assessment and authorization | P | SSP, assessment, POA&M (#512). |
| CM Configuration management | P, M | Everything is code: charts, OpenTofu, profile table, signed images, drift tests. |
| CP Contingency planning | P, M | Region pair with PRIMARY/PASSIVE roles and failover runbooks, backups to US Blob storage, restore drills. |
| IA Identification and authentication | P, C, M | Keycloak with a required second factor after brokered login, PBKDF2-SHA-512 hashing, 14-character minimum; the customer's Entra identity proofing. |
| IR Incident response | P, C, M | Runbooks, alerting inside the boundary; the customer reports incidents on its side. |
| MA Maintenance | M, P | Microsoft maintains the platform; Passage patches images through the mirror. |
| MP Media protection | M, P | Encrypted Blob storage and Key Vault HSM keys; no removable media. |
| PE Physical and environmental | M | Azure datacenters. |
| PL Planning | P | SSP. |
| PS Personnel security | P, M | Operator access policy above; Microsoft screens its personnel. |
| RA Risk assessment | P, M | Vulnerability scanning in CI (image scan, Trivy). |
| SA System and services acquisition | P, M | Dependency and licence checks, signed images with SBOM. |
| SC System and communications protection | P, M | TLS everywhere, FIPS-validated cryptography (SC-13) via GOFIPS140 and BCFIPS, private endpoints, egress allowlist (SC-7), field-level encryption with Key Vault-wrapped keys (SC-28). |
| SI System and information integrity | P, M | Ledger hash chain, input validation, patching, alerts. |
| SR Supply chain risk management | P, M | Pinned and signed images, checksum-pinned BCFIPS jars, mirror-only registry. |

There is no formal SSP in this repository yet; this matrix is its starting point.

## Not proven here

A live GCC tenant login, a real subscription apply, restore drills on Azure and the 3PAO or agency assessment need infrastructure or sign-off outside the repository (#213, #512).
