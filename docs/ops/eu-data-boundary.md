# EU Data Boundary

Passage for EU and EFTA customers whose data must stay in Europe (#605). This is not a separate cloud. It is global Azure in EU/EFTA regions, with Microsoft's EU Data Boundary commitment for the Microsoft side and the same guarantee from Passage for everything Passage runs or calls. EU/EFTA means the 27 EU member states plus Iceland, Liechtenstein, Norway and Switzerland. Use the `eu-data-boundary` overlay on the `commercial` [cloud profile](cloud-profiles.md): `PASSAGE_CLOUD_PROFILE=commercial+eu-data-boundary`, or `global.cloudOverlays: [eu-data-boundary]` in the charts. It cannot be combined with `gcc`, `gcc-high`, `dod` or `china`, whose regions are all outside the EU.

## What the overlay does

| Requirement | How Passage meets it | Proof in the repository |
|---|---|---|
| Data in EU/EFTA regions only | `allowedRegions` is the Azure EU/EFTA regions (`northeurope`, `westeurope`, `francecentral`, `francesouth`, `germanywestcentral`, `germanynorth`, `swedencentral`, `swedensouth`, `italynorth`, `polandcentral`, `spaincentral`, `norwayeast`, `norwaywest`, `switzerlandnorth`, `switzerlandwest`). | `check_region_pair.py --cloud-profile commercial+eu-data-boundary` refuses any other region, refuses an s3 backup destination (a bucket's region cannot be proven), and requires the Blob backup account to sit in the region itself. The OpenTofu `azure_location` validation refuses other regions. The EU pair is `deploy/multi-region/values/eu-azure` (West Europe primary, North Europe passive). |
| Per-tenant residency | Every tenant has a `residency` (`EU`, `US`, `UK`, `GLOBAL`) and a `region`. Under the overlay a tenant created without a residency is `EU`, a region is required, and identity refuses a region outside the residency or outside the overlay's `allowedRegions` (422 `OPS-1705`) before writing anything. | `services/identity` tenant tests; the residency table `platform/go/residency/residency.json` is checked against the overlay's region list in Go and Python. See [tenant-provisioning.md](tenant-provisioning.md#tenant-residency-605). |
| Evidence for an auditor | `deploy/multi-region/tools/residency_report.py` maps each tenant to the regions of its database, object store, Kafka and backups, including the pair peer, and flags every store outside the tenant's residency. | `test_residency_report.py`; usage in [tenant-provisioning.md](tenant-provisioning.md#residency-evidence-report-605). |
| LLM calls in the EU | Provider endpoints are enforced: only Azure OpenAI over https, deployed in an allowed EU region (`AGENTS_PROVIDER_REGION`). Anthropic, Bedrock and OpenAI are removed from egress. Every agent run records provider, host and region (`agent_run.model_provider`, `model_host`, `model_region`). | `services/agents-ts/test/cloud-envs.test.ts`, `model-endpoint-pg.test.ts`. |
| No US SaaS on the data path | PagerDuty, the hosted LLM providers and `docker.io` are removed from egress. Images come from a mirror (`requireMirror`), and alerts go to the in-boundary webhook. | `test_egress_render.py` renders every chart under the overlay and fails on any host outside the combined allowlist. |
| Pseudonymized logs | Under the overlay (or with `LOG_PSEUDONYMIZE=true`), the Go services (through `httpx.Serve`), `agents-ts` and `dispatch-py` replace values under person-like keys (`user`, `email`, `name`, `person`, `subject`, `ip`, ...) and every email address in any string with `pn_` + HMAC-SHA256 under the tenant's fieldcrypt MAC key. The pseudonym is the same within a tenant, differs between tenants, and cannot be reversed without the key. Without a key or a tenant, the value becomes `[redacted]` and is never passed through. A bad key or profile configuration stops the service rather than log raw values. | `platform/go/logpseudo` (with a known-answer test shared by all three languages), `platform/go/httpx/logpseudo_test.go` (through the real request log), `services/agents-ts/test/logging.test.ts`, `services/dispatch-py/tests/test_logpseudo.py`. |

Traces: no service exports spans today (there is no OpenTelemetry SDK in the Go, TypeScript or Python services), so Tempo receives no user identifiers from Passage. When tracing is added, span attributes must go through the same pseudonymizer before export. Prometheus metrics carry no user identifiers.

Limits of the pseudonymizer: a person's name in free text, or under a key that does not name a person, cannot be recognised. Log names under `name`, `displayName` or `user`. In TypeScript and Python, a fieldcrypt key entry wrapped by Key Vault (`wrapped:...`) cannot be unwrapped, so those services redact instead of pseudonymizing.

Not covered yet: the other TypeScript services (`services/carrier-gateway-ts`, `services/customer-portal-ts`, `services/integration-hub-ts`, `services/mcp-ts`, `services/supplier-portal-ts`, `services/workflow-ts`) and the other Python services (`services/configurator-py`, `services/forecast-py`, `services/prediction-py`, `services/routing-py`, `services/scheduler-py`) do not use the pseudonymizer. Before an EU deployment, each must adopt `services/agents-ts/src/logging/pseudonymize.ts` or `services/dispatch-py/src/passage_dispatch/logpseudo.py`, or be shown to log no user identifiers.

## Deploying

1. `tofu apply` with `-var provider_kind=aks -var-file=../../cloud-profiles/tofu/commercial.tfvars.json -var 'cloud_overlays=["eu-data-boundary"]'` and the overlay's region list as `allowed_regions`. Use an EU `azure_location` ([azure-aks.md](azure-aks.md)).
2. Configure Azure Resource Manager for the EU Data Boundary in the subscription, so support (Professional Services) data stays in the boundary. This is a subscription setting, not part of the module.
3. Install the charts with `-f deploy/cloud-profiles/values/commercial.yaml --set 'global.cloudOverlays={eu-data-boundary}' --set global.imageRegistry=<acr>.azurecr.io`. Give every service its fieldcrypt keys (the log pseudonyms need the MAC key).
4. Use the EU pair: `python3 deploy/multi-region/tools/check_region_pair.py --values-dir deploy/multi-region/values/eu-azure --cloud-profile commercial+eu-data-boundary`.
5. Create EU tenants with `residency: EU` and an EU `region`.

The customer's Entra tenant stays on the global authority (`login.microsoftonline.com`). A tenant whose sign-up country is in the EU or EFTA is in Microsoft's EU Data Boundary scope; a tenant with Multi-Geo is not, even if it is listed in the EU.

## Sub-processors

Every third party that receives tenant data under the overlay, with where it processes:

| Sub-processor | Purpose | Processing location | Notes |
|---|---|---|---|
| Microsoft Azure (AKS, Database for PostgreSQL, Storage, Key Vault / Managed HSM, Container Registry, Monitor) | Hosting, database, backups, keys, images, logs | The deployment's EU region and its EU pair peer | Regional services in an EU Data Boundary region are in Microsoft's scope. |
| Azure OpenAI Service | Copilots and agents | The EU region the model deployment is in (enforced by `AGENTS_PROVIDER_REGION`) | Disabled when no endpoint is configured. Prompts carry no PHI-tagged values, and identifiers in logs are pseudonymized. |
| Microsoft Entra ID | Customer sign-in (federation) | Microsoft's EU Data Boundary for EU/EFTA tenants | The customer's own tenant; the authority is global. |
| Customer's SMTP relay (batch `smtp`, off by default) | Report and notification email | Where the customer runs it | Passage ships no email SaaS. Configure an EU relay. |

Removed under the overlay, and so not sub-processors: PagerDuty, Anthropic, AWS Bedrock, OpenAI, Docker Hub, and GitHub Container Registry (images are mirrored into the EU registry, with registry metadata only on the build side).

## Azure non-regional services in use

| Service | EU Data Boundary configuration |
|---|---|
| Microsoft Entra ID | In scope for tenants with an EU/EFTA sign-up country; nothing to configure in Passage. |
| Azure Resource Manager | Configure the subscription for the EU Data Boundary (step 2 above), so support data stays in the boundary. |
| Azure Policy | Policy definitions and assignments are metadata; no customer data. |
| Azure Private DNS | Zone records hold resource names only. Keep personal data and customer names out of resource names. |

## Operator and support access

Operators with production access work from the EU/EFTA, through named Entra identities with MFA and just-in-time elevation, logged to the audit trail and Keycloak admin events. Support uses tenant and request ids and pseudonymized logs, never raw records. Follow-the-sun support from outside the EU is a documented exception: it gets pseudonymized logs and metrics only, with no database or record access, and it is listed as a transfer with its legal basis (Standard Contractual Clauses) in the customer DPA. Approval of that wording is tracked on #512.

GDPR duties stay with Passage and the customer: subject requests, retention and breach notification are covered by the wave 48 deliverables (#48).

## Not proven here

A live EU deployment, the ARM EU Data Boundary setting on a real subscription and the DPA wording need infrastructure or sign-off outside the repository (#213, #512).
