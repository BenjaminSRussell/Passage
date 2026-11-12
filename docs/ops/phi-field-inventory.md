# PHI field inventory: HR, payroll, expense and field service

Reviewed for the HIPAA overlay (#606, acceptance criteria 2 and 3). It lists every column in `services/hr`, `services/payroll`, `services/expense` and `services/fieldservice` that holds, or can be made to hold, protected health information (PHI), says how Passage protects it today and what is still to do. A test (`services/metadata/internal/phiinventory`) parses the table below and fails when a column it lists is not in the service's migrations, or when a row that claims a value is sealed names a column that is not `bytea`, so this review cannot rot.

## How to read it

Decisions:

- `phi`: the column holds health information about an identifiable person (a health-plan election, an injury or illness, a health-plan deduction). It must be tagged `phi: true` and `encrypted: true` in the entity metadata (`services/metadata`, MET-0010) once the table is modelled there, and a read that returns it is written to the PHI read-access log (`services/audit`, `POST /v1/audit/reads`).
- `free text`: a free-text, JSON or binary column that Passage does not classify but that a customer (a hospital system, a clinic, a health plan, a medical device maker) may fill with health data. The control is not a column tag: it is the customer notice below, the tenant row-level security, and the ability to seal or mask the column on request.
- `not phi`: reviewed and does not hold health information (identifiers, codes, catalogue data, amounts). Some of these are other PII (for example HR national id) and are protected as PII.

HIPAA exempts employment records that a covered entity holds in its role as employer. Passage still classifies them as `phi`, because a customer's HR data is the place where a health-plan election, a medical leave or an injury report sits next to a name, and the overlay must not depend on the customer's legal role.

Protection today, for every row below unless the row says more: every table has `tenant_id` and `FORCE ROW LEVEL SECURITY`; tables that carry `legal_entity_id` also have a legal-entity scope policy; routes are gated by duties (`policies/authz/data.json`). Storage and transport encryption are platform controls, not column controls; they belong to the safeguard matrix in `docs/ops/hipaa.md`. A row that says "Sealed" is stored as a `platform/go/fieldcrypt` envelope, per tenant and field.

## Inventory

| Service | Table.column | Decision | Protected today | Action |
|---|---|---|---|---|
| hr | `benefit_enrollment.plan_id` | phi | Row-level security and duties (`hr.benefits.self` reads one's own, `hr.worker.inquire` anyone's). Plaintext. Reads of a health-plan enrollment are written to the PHI read log when the hipaa overlay is on (`GET /v1/hr/workers/{id}/benefits`). | Tag `phi` + `encrypted` when benefits are modelled in metadata. Sealing the column is a follow-up migration (not done here). |
| hr | `benefit_enrollment.option_code` | phi | As `benefit_enrollment.plan_id`. | As `benefit_enrollment.plan_id`. |
| hr | `benefit_enrollment.dependent_count` | phi | As `benefit_enrollment.plan_id`. | As `benefit_enrollment.plan_id`. |
| hr | `benefit_enrollment.cobra_qualifying_event` | phi | As `benefit_enrollment.plan_id`. | As `benefit_enrollment.plan_id`. |
| hr | `benefit_enrollment.end_reason` | phi | As `benefit_enrollment.plan_id`. | As `benefit_enrollment.plan_id`. |
| hr | `benefit_life_event.kind` | phi | Row-level security and duties. Plaintext. Read and audited with the enrollments. | Tag `phi`; seal in a follow-up migration. |
| hr | `benefit_life_event.event_date` | phi | As `benefit_life_event.kind`. | As `benefit_life_event.kind`. |
| hr | `benefit_plan.plan_type` | not phi | Plan catalogue (MEDICAL, DENTAL, 401K); names no person. | None. It tells the read audit which enrollments are health plans. |
| hr | `benefit_plan.payroll_deduction_code` | not phi | Catalogue code of a plan; names no person. | None. |
| hr | `benefit_plan_option.coverage_level` | not phi | Catalogue label ("Employee + family"); names no person. | None. |
| hr | `benefit_plan_option.employee_rate` | not phi | Catalogue price of an option. | None. |
| hr | `absence_request.reason` | free text | Row-level security and duties. Plaintext, at most 500 characters. Employees type medical leave reasons here. | Customer notice (below). Seal on request. Not in events. |
| hr | `absence_request.plan_id` | phi | As `absence_request.reason`. A sick-leave, medical or maternity plan says why a person was away; plan codes are tenant-defined. | Treat requests on a tenant's sick-leave plans as PHI; tag when absence is modelled in metadata. |
| hr | `hr_incident.details_sealed` | phi | Sealed (fieldcrypt): description, witnesses, treatments, root cause, corrective action and the override reason are one sealed JSON value. Reads by id open it and write an HR audit row; events carry no medical text. | None. Already meets the encrypted rule. |
| hr | `hr_incident.incident_type` | phi | Row-level security, legal-entity scope and incident duties. Plaintext (injury, respiratory, poisoning). A privacy case hides the worker's name and id in lists and the OSHA extract. | Tag `phi`. Sealing breaks the OSHA 300 log queries, so decide with the customer: restrict by duty first. |
| hr | `hr_incident.body_parts` | phi | As `hr_incident.incident_type`. A fixed code list. | As `hr_incident.incident_type`. |
| hr | `hr_incident.medical_treatment` | phi | As `hr_incident.incident_type`. | As `hr_incident.incident_type`. |
| hr | `hr_incident.loss_of_consciousness` | phi | As `hr_incident.incident_type`. | As `hr_incident.incident_type`. |
| hr | `hr_incident.death` | phi | As `hr_incident.incident_type`. | As `hr_incident.incident_type`. |
| hr | `hr_incident.days_away` | phi | As `hr_incident.incident_type`. | As `hr_incident.incident_type`. |
| hr | `hr_incident.outcome` | phi | As `hr_incident.incident_type`. | As `hr_incident.incident_type`. |
| hr | `hr_incident.location_text` | not phi | Where it happened (a building or area), at most 200 characters; no health content. | None. |
| hr | `worker_pii.national_id_sealed` | not phi | Sealed (fieldcrypt). PII, a HIPAA identifier when it sits with PHI. Unmasked reads need `hr.pii.read` and a reason and are audited. | None. |
| hr | `worker_pii.bank_account_sealed` | not phi | Sealed (fieldcrypt). PII. | None. |
| hr | `worker_pii.home_address_sealed` | not phi | Sealed (fieldcrypt). PII, a HIPAA identifier. | None. |
| hr | `hr_profile_change.value_sealed` | not phi | Sealed (fieldcrypt) pending bank or address value; dropped once the change is decided. | None. |
| hr | `hr_profile_change.decision_note` | free text | Row-level security and duties. Plaintext. | Customer notice (below). |
| hr | `hr_personnel_action.reason` | free text | Row-level security and duties. Plaintext, at most 500 characters. | Customer notice (below). |
| hr | `employment.termination_reason` | not phi | A code (`^[A-Z0-9][A-Z0-9_-]{0,19}$`), not free text. A tenant could define a medical code. | Tell customers not to define health reasons as termination codes. |
| hr | `hr_review.manager_comments` | free text | Row-level security and duties. Plaintext. | Customer notice (below). |
| hr | `hr_review.self_achievements` | free text | As `hr_review.manager_comments`. | As `hr_review.manager_comments`. |
| hr | `hr_review.self_development` | free text | As `hr_review.manager_comments`. | As `hr_review.manager_comments`. |
| hr | `hr_goal.description` | free text | Row-level security and duties. Plaintext. | Customer notice (below). |
| hr | `hr_questionnaire_session.answers` | free text | Row-level security. TEXT questions take free text; anonymous sessions store no worker id and a date only. Results are released only above group size 5. | Customer notice (below). Do not use questionnaires for health screening without a BAA review. |
| hr | `audit_event.reason` | free text | Row-level security. Plaintext reveal reasons, 10 to 500 characters. | Customer notice (below). |
| payroll | `pay_statement.lines` | phi | Row-level security and legal-entity scope. Plaintext JSON. Holds one line per deduction, including health-plan premiums (Section 125 medical, HSA) per worker. | Tag `phi` when modelled. A jsonb column cannot be `encrypted` (MET-0010 allows string, text and bytes only): move health-plan lines to a sealed `bytea` column or store a reference, in a follow-up migration. Reads should be audited when a read route is added. |
| payroll | `pay_statement.gross` | not phi | Amount. | None. |
| payroll | `pay_statement.net` | not phi | Amount. | None. |
| payroll | `pay_run.posting_lines` | not phi | Totals per liability account for the whole run; no worker. | None. |
| payroll | `pay_posting_account.account` | not phi | Ledger account configuration. | None. |
| expense | `expense_line.merchant` | free text | Row-level security and legal-entity scope. Plaintext, at most 200 characters. A pharmacy, clinic or hospital name reveals care. | Customer notice (below). |
| expense | `expense_line.justification` | free text | As `expense_line.merchant`, at most 500 characters. | Customer notice (below). |
| expense | `expense_line.category` | not phi | A policy category code, at most 32 characters. | None. |
| expense | `expense_line.has_receipt` | not phi | A flag. No receipt image or file is stored in expense: attachments would live in the documents service, which is outside this review. | When receipts are stored, seal them and add them here. |
| expense | `expense_report.purpose` | free text | Row-level security and legal-entity scope. Plaintext. | Customer notice (below). |
| expense | `travel_requisition.purpose` | free text | As `expense_report.purpose`. | Customer notice (below). |
| expense | `travel_requisition.destinations` | free text | As `expense_report.purpose`. JSON destinations; a trip to a clinic is health data. | Customer notice (below). |
| expense | `cash_advance.purpose` | free text | As `expense_report.purpose`. | Customer notice (below). |
| expense | `card_transaction.merchant` | free text | Row-level security and legal-entity scope. Plaintext from the card feed. | Customer notice (below). |
| expense | `card_transaction.mcc` | not phi | A four-digit merchant category code. Care-provider codes show spend, not a diagnosis. | None. |
| fieldservice | `service_case.description` | free text | Row-level security and legal-entity scope. Plaintext, at most 4000 characters. A health-system customer can describe a patient or a clinical event. | Customer notice (below). Seal on request. |
| fieldservice | `service_generic_case.subject` | free text | As `service_case.description`. | As `service_case.description`. |
| fieldservice | `service_generic_case.description` | free text | As `service_case.description`. | As `service_case.description`. |
| fieldservice | `service_generic_case.resolution_text` | free text | As `service_case.description`. | As `service_case.description`. |
| fieldservice | `service_case_log.text` | free text | As `service_case.description`. Append-only log of case notes. | As `service_case.description`. |
| fieldservice | `service_inbound_message.body` | free text | As `service_case.description`. Inbound e-mail text from outside the tenant. | As `service_case.description`. |
| fieldservice | `service_inbound_message.subject` | free text | As `service_inbound_message.body`. | As `service_inbound_message.body`. |
| fieldservice | `service_inbound_message.from_address` | free text | As `service_inbound_message.body`. | As `service_inbound_message.body`. |
| fieldservice | `service_inbound_message.attachments` | free text | As `service_inbound_message.body`. JSON references to attachments that a sender chose. | As `service_inbound_message.body`. |
| fieldservice | `service_work_order.technician_note` | free text | Row-level security and legal-entity scope. Plaintext. | Customer notice (below). Seal on request. |
| fieldservice | `service_work_order.address` | free text | As `service_work_order.technician_note`. A device repaired at a patient's home puts the patient's address here. | Customer notice (below). |
| fieldservice | `service_work_order.signed_by` | free text | As `service_work_order.technician_note`. The signer can be a patient. | Customer notice (below). |
| fieldservice | `service_work_order.checklist` | free text | As `service_work_order.technician_note`. JSON checklist with technician notes. | Customer notice (below). |
| fieldservice | `service_wo_capture.data` | free text | Row-level security and legal-entity scope. Plaintext image bytes (photo or signature, PNG or JPEG) kept in Postgres until an object store takes over. A photo can show a patient, a record or a clinical setting. | Seal with fieldcrypt before the object store takes over; until then the customer notice applies. |
| fieldservice | `service_agreement_visit.note` | free text | As `service_work_order.technician_note`. | Customer notice (below). |
| fieldservice | `service_wo_action.reason` | free text | As `service_work_order.technician_note`. | Customer notice (below). |
| fieldservice | `service_activity.subject` | free text | Row-level security. Plaintext. | Customer notice (below). |
| fieldservice | `service_object.serial` | not phi | A device serial number. For an implanted or home medical device it can be linked to a patient outside Passage. | None. Do not put a patient name in an asset record. |
| fieldservice | `service_kb_article.symptom` | not phi | Knowledge article about a product, written once for all customers; no person. | None. |

## Customer notice for free text

The `free text` rows are the customer's to keep clean. Passage does not scan free text for PHI (the agent service refuses to send `phi`-tagged values to a model, but it cannot tag what it cannot see). The BAA and the customer guide (`docs/ops/hipaa.md`) tell a covered entity or business associate to keep health details out of these fields, and to use the sealed incident details, the benefits module and the attachment store that carries the overlay's controls instead. A tenant that cannot rely on that asks for a column to be sealed or masked: that is a per-column migration, listed in the Action column.

## Keeping this current

Add a row when a migration adds a column that can hold health data, and change the Decision when the design changes. `go test ./...` in `services/metadata` runs `internal/phiinventory`: it fails when a listed column does not exist, when a row names an unknown service or decision, when a row is listed twice, and when a "Sealed" row is not a `bytea` column.
