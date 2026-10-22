# Phase 8: Projects, People & Service (waves 41-45)

Project operations, HR core, payroll and expenses, enterprise asset management and field service.

## Wave 41: Project Operations: WBS, Time, Expense, Billing & Accounting

**Phase 8 — Projects, People & Service**

**Goal.** Project-centric businesses plan WBS and resources, capture time/expense/items, bill by T&M/fixed-price/milestone, and recognize revenue and WIP.

**Replaces**

- *AX / D365:* Project management and accounting: project types, WBS, forecasts, hour/expense/item journals, invoice proposals, estimates (completed-contract/percentage), grants
- *SAP:* PS: WBS elements, networks, project budgets, results analysis (RA), resource-related billing (DP90), settlement

**Deliverable**

- Project hierarchy, WBS with scheduling, resource roles and bookings, forecasts/budgets
- Transactions: hour, expense, item, fee journals; project purchase orders; subcontracts
- Billing: invoice proposals by T&M, fixed-price milestones, retainers, billing rules; customer funding/grants
- Project accounting: WIP/accrued revenue, cost-to-complete, percentage-of-completion estimates, ledger posting profiles
- Project reporting: committed costs, EVM (CPI/SPI), profitability

**Proof (testable acceptance)**

- [ ] Estimate fixtures: percentage-of-completion revenue matches an accountant-reviewed workbook
- [ ] Invoice proposals capture exactly the billable transactions per billing rule (property test)
- [ ] WBS scheduling respects dependencies and resource calendars
- [ ] Project subledger reconciles to GL WIP/revenue accounts

**Deploy**

Projects workspace; a demo engineering-services entity runs projects alongside the manufacturer.

**References**

- [AX 2012 project management and accounting](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/project-management-and-accounting)
- [D365 Project Operations](https://learn.microsoft.com/en-us/dynamics365/project-operations/)
- [SAP PS](https://help.sap.com/docs/SAP_ERP)

**Depends on:** wave 40

---

## Wave 42: Human Resources Core: Workers, Positions, Absence & Talent

**Phase 8 — Projects, People & Service**

**Goal.** Workers, positions, jobs and departments are managed with effective dating; employees self-serve absence, benefits enrollment and personal data; managers handle reviews.

**Replaces**

- *AX / D365:* Human resources: workers, positions, jobs, departments, benefits, leave and absence, performance, skills, recruitment
- *SAP:* HCM PA/OM: personnel actions, org management, time (PT), benefits, SuccessFactors Employee Central

**Deliverable**

- Worker/employee/contractor records with effective-dated employment, positions, jobs, compensation plans, org assignments
- Absence/leave: plans, accruals, requests, approvals, calendars; integration with time (wave 25)
- Benefits: plans, eligibility, open enrollment, life events; employee self-service and manager self-service
- Talent: skills, certifications, performance reviews, goals; recruitment pipeline basics
- HR compliance: PII vaulting, consent, retention, country-specific fields framework

**Proof (testable acceptance)**

- [ ] Effective-dated queries return correct position/manager as-of any date (property test)
- [ ] Accrual fixtures: plans accrue correctly across hire dates and carry-over rules
- [ ] Enrollment eligibility rules evaluated correctly for 100 fixture employees
- [ ] PII access logged and masked per role (security contract tests)

**Deploy**

HR workspace and employee self-service portal; demo workforce loaded.

**References**

- [D365 Human Resources](https://learn.microsoft.com/en-us/dynamics365/human-resources/hr-admin-overview)
- [SAP SuccessFactors Employee Central](https://help.sap.com/docs/SAP_SUCCESSFACTORS_EMPLOYEE_CENTRAL)
- [Frappe HRMS (open-source reference)](https://docs.frappe.io/hr)

**Depends on:** wave 41

---

## Wave 43: Payroll Engine & Expense Management

**Phase 8 — Projects, People & Service**

**Goal.** A country-pluggable gross-to-net payroll engine runs pay cycles from time and HR data and posts to the GL; employees submit expenses with policy checks and reimbursement.

**Replaces**

- *AX / D365:* AX 2012 payroll (US), expense management (travel & expense, policies, per diems, credit card import)
- *SAP:* SAP Payroll (PY) schemas/rules, Concur expense

**Deliverable**

- Payroll engine: pay cycles, earnings/deductions/taxes as versioned rule sets, retro pay, garnishments, gross-to-net, pay statements, GL posting
- Country packs: US (federal/state tables) and one EU country as reference; third-party payroll provider adapter (ADP/Gusto-style) as alternative
- Expense management: reports, receipts (OCR), policies/per diems, approvals, credit-card feed import, reimbursement via AP
- Year-end forms framework (W-2/1099 structures)
- Payroll audit and reconciliation reports

**Proof (testable acceptance)**

- [ ] Gross-to-net fixtures: 200 US pay scenarios match IRS Publication 15-T computations
- [ ] Retro pay recalculation fixtures match expected deltas
- [ ] Expense policy violations are flagged exactly per rule set; reimbursement posts to AP and pays in a payment run
- [ ] Payroll posting reconciles to GL payroll accounts

**Deploy**

Payroll and expense workspaces; demo entity runs a payroll cycle in sandbox.

**References**

- [AX 2012 payroll](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/payroll)
- [IRS Publication 15-T](https://www.irs.gov/publications/p15t)
- [D365 expense management](https://learn.microsoft.com/en-us/dynamics365/project-operations/prod-exp/expense-overview)
- [SAP Payroll](https://help.sap.com/docs/SAP_ERP)

**Depends on:** wave 42

---

## Wave 44: Enterprise Asset Management (Maintenance)

**Phase 8 — Projects, People & Service**

**Goal.** Maintain plant assets with preventive plans, condition-based triggers from IoT, work orders with parts and labor, and asset lifecycle costing.

**Replaces**

- *AX / D365:* D365 Asset Management: functional locations, assets, maintenance plans, work orders, counters, maintenance requests
- *SAP:* PM: functional locations, equipment, maintenance plans (IP10), notifications (IW21), maintenance orders (IW31), measuring points

**Deliverable**

- Functional locations, assets/equipment hierarchy, BOMs/spares, counters and measurement points
- Maintenance plans (time/counter/condition-based) generating work orders; maintenance requests; checklists
- Work orders: planning, scheduling against resources (wave 23), parts reservation, labor, costs → asset and GL
- Condition monitoring from IoT (wave 25) with thresholds and predictive hooks
- Asset lifecycle KPIs: MTBF/MTTR, cost per asset, downtime

**Proof (testable acceptance)**

- [ ] Plan fixtures: time/counter plans generate work orders on the expected dates/readings
- [ ] Work order costs (parts + labor) post to the asset and GL maintenance accounts and reconcile
- [ ] IoT threshold breach creates a maintenance request within 5 s
- [ ] Downtime recorded on the production resource affects scheduling capacity

**Deploy**

Asset management workspace; demo plant's machines maintained with preventive plans.

**References**

- [D365 Asset Management](https://learn.microsoft.com/en-us/dynamics365/supply-chain/asset-management/)
- [SAP PM](https://help.sap.com/docs/SAP_ERP)
- [openMAINT (open-source CMMS)](https://www.openmaint.org/en/)

**Depends on:** wave 43

---

## Wave 45: Service Management & Field Service

**Phase 8 — Projects, People & Service**

**Goal.** Service agreements, cases and work orders are dispatched to field technicians with parts, skills, SLA tracking and mobile execution; warranty and installed base are tracked.

**Replaces**

- *AX / D365:* Service management: service agreements, service orders, dispatch board, service objects, repair, subscriptions
- *SAP:* CS/Customer Service: service notifications, service orders, warranties, installed base (IB), SAP Field Service Management

**Deliverable**

- Installed base/service objects with warranty terms, serial tracking from shipments
- Service agreements (entitlements, SLAs, pricing), cases → service work orders, repair orders
- Dispatch board and technician scheduling (skills, territory, travel) with OR-Tools; mobile technician app (offline, photos, signatures, parts)
- Parts consumption from technician trucks (mobile inventory locations), billing to customer or warranty
- Service KPIs: SLA attainment, first-time fix

**Proof (testable acceptance)**

- [ ] Entitlement fixtures: cases resolve to the correct agreement and SLA clock
- [ ] Dispatch optimization is feasible and respects skills/time windows (property test)
- [ ] Work order billing posts correctly for billable vs warranty lines
- [ ] Phase 8 exit: demo group runs projects, HR, payroll, maintenance and field service in a recorded walkthrough

**Deploy**

Service workspace and technician mobile app; demo service objects derived from shipped serials.

**References**

- [AX 2012 service management](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/service-management)
- [D365 Field Service](https://learn.microsoft.com/en-us/dynamics365/field-service/)
- [SAP Field Service Management](https://help.sap.com/docs/SAP_FIELD_SERVICE_MANAGEMENT)

**Depends on:** wave 44

---
