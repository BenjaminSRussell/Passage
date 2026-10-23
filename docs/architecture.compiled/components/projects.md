# Projects (project accounting, maintenance, field service)

## Purpose & Boundaries
Context `projects` (database `passage_projects`; waves 41, 44-45). Deployables `projects-ts` and `projects-py` (technician dispatch optimization; stateless). No Go service. Owns projects/WBS, project transactions, functional locations, maintenance, installed base, service agreements/cases/work orders. Posts project WIP/POC/billing proposals via `ledger.Post`.

## Interfaces
Prefix `/v1/projects/`: `hour-journals/{id}:post` (ledger.Post WIP; emits `transaction.posted.v1`), `expense-journals/{id}:post`, `invoice-proposals/{id}:approve` (sales creates the invoice from the event), `wip-runs` (202), `projects/{id}/evm`, `maintenance-plans/{id}:generate` (202), `maintenance-work-orders/{id}:release|:complete`, `condition-triggers` (5 s IoT trigger), portal `portal/cases` (customer scope GET/POST), `cases/{id}:escalate|:resolve`, `service-agreements/{id}:check-entitlement`, `dispatch:optimize` (202 via projects-py), `technicians/me/work-orders` (device class), `service-work-orders/{id}:complete` (offline replay idempotent), `GET /reconciliation/{check}` (wip).

## Data Ownership
EN-075/076: project (project_type time_material|fixed_price|milestone|internal|investment), wbs_element (UQ (project_id, wbs_code)), project_transaction (IL; billing derived from `project_transaction_billing` rows inserted by `projects-billing` — table stays insert-only), estimate, invoice_proposal(+line; invoice_id set when invoice created), functional_location, maintenance_asset, maintenance_plan, maintenance_work_order, installed_base (UQ (product_id, serial_number)), service_agreement, service_case, service_work_order (billing_invoice_id, billing_status unbilled|billed; GiST on scheduled_window). Indexes IX-PJ-01..14.

## Events Produced
`projects.transaction.posted.v1`, `projects.invoiceproposal.approved.v1` (billingMode timeMaterial|fixedPrice|milestone|poc), `projects.serviceworkorder.completed.v1`.

## Events Consumed
`projects-condition` (manufacturing.machineevent.received), `projects-billing` (projects.transaction.posted, sales.invoice.posted filter sourceDocument.type=invoice_proposal -> sets invoice_id, inserts project_transaction_billing), `projects-service-billing` (sales.invoice.posted filter service_work_order -> billing_status billed), `projects-laborcost` (workforce.payrollrun.posted), `projects-changeorder`, `projects-partymerge`, `projects-periodcache`, `projects-numbersequence`.

## Dependencies
internal: ledger, sales, workforce, manufacturing (machine events, time), inventory (parts), party, device-bff. external: OR-Tools (dispatch).

## Gotchas & Constraints
- Technician PWA offline for a 10 h shift (assumption A6); entitlement checks not in packet show "will verify on sync".
- Service invoices created by sales; projects never writes into sales.
- IX-PJ-05 column fix pending (R4-6): billing_status is derived, not a column of project_transaction.
- Field service is part of `projects`; service cases surface in customer portal.

## Source Sections
- Backend 5.16 (899-921); Data EN-075/076 (3160-3174); Events 3.9 (4076-4079), consumer rows (4229, 4294, 4308, 4316-4317); Frontend 7.6.3 (5536-5543)
