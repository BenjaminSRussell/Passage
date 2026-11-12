# HIPAA overlay

Passage for HIPAA-regulated customers: covered entities and business associates such as hospital systems, clinics, health plans and medical device makers with patient data (#606). This is an overlay, not a separate cloud. It runs on Azure commercial (`commercial+hipaa`) or Azure Government (`gcc-high+hipaa`) and adds Passage's own safeguards on top of Microsoft's BAA. Set `PASSAGE_CLOUD_PROFILE=<profile>+hipaa`, `global.cloudOverlays: [hipaa]` in the charts, and `-var 'cloud_overlays=["hipaa"]' -var hipaa=true` in OpenTofu (see the [cloud profiles](cloud-profiles.md)).

HHS approves no HIPAA certification, and a BAA does not make Passage compliant. What this page lists is what Passage does, what it inherits, and what the customer must do.

## What the overlay does

| Safeguard | Setting | Where |
|---|---|---|
| Only BAA-covered services hold PHI | `baaCoveredServices` in the profile (Microsoft's HIPAA offering page is the source). The PHI-bearing services are `hr`, `payroll`, `expense` and `fieldservice`. | `deploy/cloud-profiles/test_tofu_profiles.py` maps every Azure resource type the OpenTofu module creates to a service and fails when one is not on the list. |
| No SaaS without a BAA | Egress to PagerDuty, Anthropic, Bedrock and OpenAI is removed, and provider endpoints are enforced (Azure OpenAI only). Alerts go to the in-boundary webhook. | `test_egress_render.py` under every profile with the overlay. `agents-ts` refuses other endpoints. |
| PHI classification | Entity fields carry `phi: true`, and `entitygen` fails (MET-0010) when a `phi` field is not `encrypted: true` (a fieldcrypt envelope per tenant and field). HR, payroll, expense and field-service columns are reviewed in [phi-field-inventory.md](phi-field-inventory.md). | `services/metadata` validation and golden tests; `internal/phiinventory` fails when a listed column disappears from the migrations. |
| No PHI to a model | `agents-ts` refuses text carrying a value under a `phi`-tagged field before any model call (AGT-0215), whatever the provider. | `services/agents-ts/test/cloud-envs.test.ts`. |
| Read audit | Under the overlay, `hr` records every read that returns PHI (health-plan elections, life events) in the audit service before returning it: who, which record and fields, when, and why. If the record cannot be written, the data is not returned. An auditor lists and exports the log per tenant, and the export verifies the hash chain first. | `services/audit` (`phi_read`, hash-chained, append-only), `services/hr` PHI tests, `platform/go/phiaudit`. |
| Emergency access | `hr.benefits.break-glass` reads anyone's benefits only with a `BREAK-GLASS` purpose and only while read auditing is on. Each such read is queued for review by a second person (`GET /v1/audit/reads:break-glass`, `POST /v1/audit/reads/{id}:review`), and the reader cannot review their own access. | `services/audit` and `services/hr` break-glass tests. |
| Automatic logoff | The web app signs out after 15 idle minutes, with a warning one minute before. Activity in any tab keeps the session. Build with `PASSAGE_IDLE_TIMEOUT_MINUTES=$(python3 deploy/cloud-profiles/cloudprofile.py idle-timeout commercial+hipaa)`. The realm patch sets the Keycloak SSO idle timeout to the same value, so a reload does not sign the user back in. | `apps/web/src/shell/auth/idle.test.tsx`; `platform/go/cloudprofile` realm tests. |
| Audit retention | Keycloak login and admin events are kept six years (2,190 days) through the realm patch (`go run ./cloudprofile/cmd/cloudprofile realm-patch -profile commercial+hipaa`). | CI imports the `gcc-high+hipaa` patch into strict FIPS Keycloak. |
| Azure Policy | OpenTofu assigns the built-in HIPAA HITRUST initiative to the subscription (`deploy/tofu/cluster/modules/aks/policy.tf`). Its compliance report (Azure Policy, Compliance, export) is part of the evidence. | `hipaa_*` runs in the module's offline tests. |

## BAA chain

1. **Microsoft to Passage.** Microsoft offers the HIPAA BAA through the Microsoft Product Terms and DPA to customers who are covered entities or business associates. It covers only the in-scope Azure services. Passage holds it for its own subscription and puts PHI only in services on `baaCoveredServices`.
2. **Passage to the customer.** As the SaaS provider, Passage signs a BAA with each healthcare customer. The customer does not need one with Microsoft for Passage's subscription.
3. **Passage's sub-processors.** None outside Microsoft under the overlay: the egress allowlist removes every SaaS without a BAA.

The BAA template and the human legal sign-off are tracked on #512.

## Security Rule safeguard matrix

M = Microsoft (Azure), P = Passage, C = customer.

| Safeguard (45 CFR 164) | M | P | C |
|---|---|---|---|
| 164.308 Security management, risk analysis | platform risk program | risk analysis of Passage (this page, the PHI inventory, CI scans) | its own risk analysis |
| 164.308 Workforce security, access management | Microsoft staff | operator access by named identity, MFA, just-in-time elevation, logged | assigns roles and duties to its users, removes leavers (SCIM) |
| 164.308 Security incident procedures | platform incidents | breach runbook below | its own response and notices |
| 164.308 Contingency plan | region pairs, storage redundancy | backups, region pair failover, restore drills | its downtime procedures |
| 164.310 Facility, workstation and device controls | datacenters | no PHI on operator devices; support uses ids, not records | its workstations and devices |
| 164.312(a)(1) Access control: unique user id | | one identity per user (Keycloak, Entra federation), duties, RLS per tenant | one account per person |
| 164.312(a)(2)(ii) Emergency access procedure | | break-glass duty, always logged, reviewed by a second person | keeps the break-glass role unassigned until needed, does the reviews |
| 164.312(a)(2)(iii) Automatic logoff | | 15-minute idle sign-out in the web app and the SSO session | |
| 164.312(a)(2)(iv) Encryption and decryption | storage and disk encryption, HSM | fieldcrypt envelopes for `phi` fields, customer-managed keys where the profile has them | |
| 164.312(b) Audit controls | platform logs | change trail, PHI read log with per-tenant export, Keycloak events for six years | reviews the logs |
| 164.312(c) Integrity | | hash chains on the audit trail and read log, ledger hash chain | |
| 164.312(d) Person or entity authentication | Entra ID | Keycloak with a second factor, or the IdP's MFA when trusted; service tokens between services | its identity proofing and MFA |
| 164.312(e) Transmission security | TLS endpoints | TLS everywhere, Postgres `verify-full` under government profiles | its network |

## Breach-notification runbook

A breach is an unauthorized acquisition, access, use or disclosure of unsecured PHI. PHI sealed with fieldcrypt is encrypted, so a disclosure of the envelope alone is not a breach of unsecured PHI if the keys were not exposed.

1. **Detect and contain.** Sources: alerts, the PHI read log (unexpected readers, FLAGGED break-glass reviews), Keycloak events, customer reports. Revoke the access (disable the identity, rotate tokens and keys), preserve logs and images, and open an incident with a timeline.
2. **Assess.** As fast as the facts allow (target: five business days), decide whether unsecured PHI was involved, using the four-factor risk assessment of 164.402(2): nature and extent of the PHI, who received it, whether it was actually acquired or viewed, and how far the risk was mitigated. Use the read-log export (with its chain verification) to list the records and fields that were read.
3. **Notify the covered entity.** As a business associate, Passage tells each affected customer without unreasonable delay, and no later than 60 days after discovery (164.410). The BAA may set a shorter period, and the shorter one applies. The notice lists the individuals affected, what happened, when, the PHI involved and what Passage has done.
4. **Support the customer's notices.** The covered entity notifies individuals within 60 days, HHS (within the same 60 days for 500 or more people, otherwise in a yearly report due within 60 days after the end of the calendar year), and the media for more than 500 residents of one state. Passage supplies the facts and the per-record list.
5. **Close.** Root cause, fixes, an updated risk analysis, and a record of the incident kept six years.

## HITRUST decision

Many healthcare buyers ask for HITRUST r2 or i1. Azure's HIPAA HITRUST policy initiative covers the Azure layer only, so Passage's own controls would need their own assessment. Decision: Passage does not pursue HITRUST until a customer contract requires it. Until then, the evidence package is this page, the PHI field inventory, the Azure Policy compliance report and the CI test results. When a contract requires it, an i1 assessment comes first, reusing this evidence. Approval is tracked on #512.

## Not proven here

A live deployment under the overlay, the Azure Policy compliance report from a real subscription, the BAA template and its legal sign-off need infrastructure or people outside the repository (#213, #512).
