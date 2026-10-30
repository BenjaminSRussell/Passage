# Functional guide: Phase 8, Projects, People and Service

Waves 41-45. Audience: project managers and project accountants, HR and payroll administrators, managers doing
reviews, maintenance planners, service dispatchers and field technicians. The phase covers project accounting, HR and
its effective-dated records, payroll and expenses, maintenance of plant assets, and field service. At the end of the
phase a demo group runs projects, HR, payroll, maintenance and field service side by side.

This guide describes intended behaviour from the phase 8 PRD (`docs/prd/phase-8-projects-people-service.md`). It is
not a build status. Each wave has its own service and web module, named at the top of its section; what is built is
in that service's `README.md` and what is not is in its `NOT-IMPLEMENTED.md`, which is authoritative where it and
this guide differ. Where a later wave completes an item (statutory filing in the wave 48 country packs), the text says
so.

## Wave 41: Project operations (WBS, time, expense, billing and accounting)

Owning area: `services/projects`. Screens: `apps/web/src/modules/projects`.

What you do here:

- Set up the project hierarchy and the work breakdown structure (WBS) with its schedule. Assign resource roles and
  bookings, and enter forecasts and budgets.
- Record hour, expense, item and fee journals. Raise project purchase orders and subcontracts against a project.
- Generate invoice proposals for T&M, fixed-price milestones and retainers. A billing rule decides which transactions
  the proposal takes. Track customer funding and grants.
- Run the project accounting: WIP and accrued revenue, cost-to-complete, and percentage-of-completion estimates. The
  posting profile sends each entry to the right GL account.
- Read the project reports: committed costs, earned value (CPI and SPI) and profitability.
- Let staff fill in weekly timesheets by period, with favourites and delegates. A missing timesheet shows on the
  report. A project quotation converts into a project.
- Manage retention terms, change orders, claims, and the risk and issue register.

Rules you can rely on:

- A WBS schedule respects its dependencies and the resource calendars.

## Wave 42: Human resources core

Owning area: `services/hr`, with employee self-service in `apps/ess`. Screens: `apps/web/src/modules/hr`.

What you do here:

- Create worker, employee and contractor records. Employment, position, job, compensation plan and organisation
  assignments are effective-dated, so you can see the record as it stood on any date.
- Set up absence plans with accruals, carry-over rules and calendars. Employees request leave and managers approve it.
  Approved absence is published to time and payroll,
  and balances reconcile to the wave 25 time registrations.
- Run benefit plans with eligibility rules, open enrolment and life events. Employees use self-service for personal
  data and enrolment; managers use manager self-service for reviews and approvals.
- Keep skills, certifications, performance reviews and goals. The recruitment pipeline covers requisitions, candidates
  and offers.
- Manage consent, retention and the country-specific fields. Personal data is kept in a vault. Each access to personal
  data is logged and masked by role.
- Run courses and learning. Completion updates the skills and certifications of the worker.
- Keep the injury and illness register. The OSHA 300, 300A and 301 extracts are produced from it.
- Run onboarding and offboarding checklists and HR reminders. Compensation planning cycles run in the same place.

Check before sign-off: the position and manager shown for a worker on a past date match what was in force then; PII
access appears in the audit log.

## Wave 43: Payroll engine and expense management

Owning area: `services/payroll` and `services/expense`. Screens: `apps/web/src/modules/payroll` and
`apps/web/src/modules/expense`.

What you do here:

- Set up a pay cycle. Earnings, deductions and taxes are versioned rule sets, and the country pack supplies the tax
  tables. The US (federal and reference states) and Germany ship as reference
  packs; an entity that outsources payroll exports a provider file and imports the results instead.
- Run the cycle from time and HR data. Gross-to-net produces the pay statements. Retro pay recalculates earlier periods
  and posts the difference.
- Run off-cycle payments for bonuses, final pay, manual checks and check replacements. Supplemental tax applies to the
  bonus. Deduction arrears and overpayment claims are recovered in later runs.
- Post the payroll to the GL payroll accounts and reconcile the posting to the payroll register.
- Produce the year-end form data (W-2 and 1099 structures) and the quarterly and annual statutory wage totals (941,
  940, state UI), reconciled to the GL liability accounts. The wave 48 country packs render and file them. Workers'
  compensation class codes carry the premium base.
- Submit expense reports with receipts (captured by OCR), check them against policy and per diem rules, and approve
  them. Import the credit card feed. A reimbursement goes to AP and pays in a payment run.
- Take cash advances before a trip. Settlement against the expense report shows the variance.

Check before sign-off: gross-to-net matches the reference computations for the sample scenarios; the payroll posting
reconciles to the GL; each policy violation is flagged according to its rule.

## Wave 44: Enterprise asset management (maintenance)

Owning area: `services/eam`. Screens: `apps/web/src/modules/eam`.

What you do here:

- Set up functional locations and the asset hierarchy. Each asset has its spares, its counters and its measuring
  points.
- Define maintenance plans by time, by counter or by condition. A plan creates work orders on its due dates or
  readings. Maintenance requests and checklists come in from the floor.
- Plan a work order: schedule it against the resources from wave 23, reserve the parts, record the labour and track
  the cost. The cost posts to the asset and to the GL maintenance accounts.
- Read condition monitoring from the IoT connector. A threshold breach creates a maintenance request. Downtime on a
  production resource reduces the scheduled capacity.
- Read the asset KPIs: MTBF, MTTR, cost per asset and downtime. Track warranties and check them before a repair.
- Issue permits to work and lockout/tagout (isolation points and personal locks). Work cannot start until the permit is
  in place.
- Refurbish rotables against their condition valuation. Manage fleet attributes and fuel logs, and rate asset
  criticality.

Check before sign-off: maintenance costs posted to the asset and GL accounts reconcile; a permit gate stops work
without isolation.

## Wave 45: Service management and field service

Owning area: `services/fieldservice`, with dispatch optimisation in `services/dispatch-py` and the technician app in
`apps/technician`. Screens: `apps/web/src/modules/service`.

What you do here:

- Keep the installed base: each service object with its serial, its warranty terms and its location. Serials from
  shipments create the objects.
- Set up service agreements with entitlements, SLAs and prices. A case resolves to its agreement, and the SLA clock
  starts from the agreement.
- Open cases and raise service work orders and repair orders from them. Cases can be categorised, carry stages, link to
  any record, and are restricted by category security.
- Search the knowledge base for symptom and solution articles. Suggestions appear on the case.
- Dispatch technicians on the board. The optimiser assigns jobs by skill, territory and travel time, and respects the
  time windows.
- Technicians use the mobile app offline: they take photos, get signatures, and record the parts used. Parts come from
  the technician's truck stock.
- Bill the customer for billable lines and the warranty for warranty lines. Claim back from the supplier where a part
  was under warranty.
- Read the service KPIs: SLA attainment and first-time fix.

Check before sign-off (phase 8 exit): the demo group runs projects, HR, payroll, maintenance and field service in a
recorded walkthrough.

## Month-end sequence for this phase

1. Close the project periods: approve timesheets, post the time and expense journals, run the WIP and revenue
   calculation, and reconcile the project subledger to the GL WIP and revenue accounts.
2. Run the payroll cycle, review the exceptions, and post the payroll to the GL.
3. Run the maintenance plans for the period and post the work order costs.
4. Review the service KPIs and the open SLA breaches.

Related: [phase 4 guide](phase-4-manufacturing.md) for the machines and the time registrations that maintenance and
payroll use, [phase 7 guide](phase-7-treasury-controlling.md) for the budgets that project and maintenance spend
draws on, and the [implementer guide](../implementer-guide.md) for the payroll parallel run before cutover.
