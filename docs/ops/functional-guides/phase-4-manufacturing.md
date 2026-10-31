# Functional guide: Phase 4, Manufacturing & Planning

Waves 21-25. Audience: production planners, schedulers, shop-floor supervisors and operators, cost accountants and
the plant controller. A discrete or process manufacturer builds product on Passage: production orders consume
material and labour, master planning proposes the orders, scheduling sets their dates against capacity, and the shop
floor reports what happened. The guide follows the order in which a planner works.

This guide describes intended behaviour from the phase 4 PRD (`docs/prd/phase-4-manufacturing.md`). It is not a
build status. Each wave has its own service and web module, named at the top of its section; what is built is in
that service's `README.md` and what is not is in its `NOT-IMPLEMENTED.md`, which is authoritative where it and this
guide differ. Where a later wave completes an item (standard-cost roll-up in wave 39, payroll in wave 43), the text
says so.

## Wave 21: Production orders, WIP and order-based costing

Owning area: `services/production`. Screens: `apps/web/src/modules/production`.

What you do here:

- Create a production order for a released product. The BOM and route versions in force on the delivery date are
  exploded into the order's BOM lines, operations and jobs when it is created.
- Move the order along its status path: estimate (planned material, labour, overhead and subcontract cost), schedule,
  release (picking list and job cards as PDF), start, report as finished, end. Each step is one command and can be
  applied to up to 500 selected orders at once.
- Post picking-list journals (issue to WIP) and route-card or job-card journals (hours valued at the resource's cost
  category rate). Each BOM line's flushing principle decides whether it is consumed by hand, at start, at report as
  finished, or when the job reports.
- Report as finished (RAF) with by-products, co-products and an error quantity posted as scrap. Required quantities
  include the BOM line scrap percentage and are rounded at posting.
- Send an operation to a subcontractor. A vendor-type resource creates a linked purchase order line (wave 17).
- End the order. Actual cost settles against planned cost, WIP is reversed, and the variances post to the GL.
- Split or merge orders, apply an engineering change (order change management, with the change cost shown before you
  apply it), edit an order-specific BOM or route before start, and create rework orders.
- Read the cost view: planned against actual by cost group, with a drill from each variance to its journal entry.
  As-built genealogy records which component serials or batches went into each finished serial or batch.

Rules you can rely on:

- An order moves only along its status path, and each command is idempotent. Reset to an earlier status reverses the
  later postings, and is refused once a reversed receipt has been consumed downstream (BR-21.5).
- A journal posts only into an open inventory and GL period (BR-21.3).
- Ending an order leaves its WIP balance at 0.00. After a partial RAF, the remaining WIP reconciles to the GL WIP
  account (AC-21.6, AC-21.7).

## Wave 22: Master planning (MRP, MPS and forecasting)

Owning area: `services/planning`, with the statistical forecast in `services/forecast-py`.
Screens: `apps/web/src/modules/planning`.

What you do here:

- Run a master plan in a plan version. Net requirements come from sales orders, forecasts (after forecast reduction),
  safety stock and dependent demand. Supply comes from on-hand stock, purchase orders, production orders and transfers.
- Review the planned orders. Firm the ones you accept and work the action messages (advance, postpone, cancel) and the
  delay messages.
- Use coverage groups (requirement, period or min-max), lot sizing, planning strategies (make to stock, make to order,
  assemble to order) and quota arrangements between vendors.
- Use demand-driven buffers (DDMRP) for decoupling points where the policy calls for them.
- Ask "why is this order here?". The pegging tree from any planned order shows the demand that drives it.
- Generate a statistical baseline forecast (ETS, or Croston/TSB for intermittent items) with its error measures.
- Run simulation plans that do not touch the operational plan.

Behaviour to know: the same inputs produce the same plan on every run (FR-22.14). Batches that would expire before the
required remaining shelf life are not pegged to the demand (BR-22.15). Simulation versions never firm orders or send
messages to purchasing and sales (BR-22.14).

## Wave 23: Capacity planning and finite scheduling

Owning area: `services/scheduling`, with the optimiser in `services/scheduler-py`.
Screens: `apps/web/src/modules/scheduling`.

What you do here:

- Schedule operations and jobs against resource calendars: forward or backward, finite or infinite capacity, with
  primary and secondary resources.
- Use the Gantt board. Drag a job to move it or assign it to another capable resource; constraint violations show
  before you commit. Moving a job reschedules the later operations of the same order, and optionally the orders
  pegged to it, with a preview first.
- Read the load view by resource, resource group and period.
- Sequence campaigns by a setup matrix, add changeover or cleanout operations, and settle the campaign cost across
  its orders.
- Limit allowed resource transitions and the maximum wait between operations with resource networks.
- Ask for an optimised schedule that minimises lateness and makespan (the CP-SAT mode). If the solver finds no
  result, the heuristic schedule is kept.

Behaviour to know: scheduled dates flow back to the production orders and to master planning, so the next plan uses
realistic lead times (FR-23.10).

## Wave 24: Process manufacturing and lean

Owning area: `services/processmfg` (formulas, batch orders, batch records) and `services/lean` (production flows and
kanbans). Screens: `apps/web/src/modules/processmfg` and `apps/web/src/modules/lean`.

What you do here:

- Create batch orders from a formula. Scalable lines scale with the batch size; the rounding residual goes to the
  largest line, so the lines add up to the batch size.
- Consume active ingredients by potency: with a 50% standard potency and a batch picked at 40%, 100 kg planned
  becomes 125 kg, and the compensating filler line is reduced by 25 kg (BR-24.2).
- Allocate co-product and by-product costs. The allocated costs add up to the batch cost.
- Set batch attributes, shelf life, best-before and expiry dates, and batch disposition. Attributes and shelf life pass
  from input batches to output batches (batch derivation).
- Handle catch-weight items, which carry a count and a weight.
- Run lean production with production flows and kanban rules (manufacturing or withdrawal; fixed, scheduled or event
  replenishment) on the kanban board. Flow periods settle with backflush costing.
- Keep the formula versions under engineering change control.
- Review the electronic batch record by exception, then approve it. Signatures need step-up re-authentication and are
  bound to the signed record by a hash.

Behaviour to know: QA approval is refused while any exception in the batch record has no disposition (BR-24.15).
After approval the record is sealed and archived as PDF/A-2b plus JSON (FR-24.21).

## Wave 25: Shop-floor execution, time registration and IoT

Owning area: `services/shopfloor` and the edge connector `services/iot-edge`. Screens:
`apps/web/src/modules/shopfloor` (kiosk and supervisor pages). The PRD also plans a separate kiosk PWA, which is not
on main.

What you do here:

- Sign in at the kiosk, clock in and out, start and stop jobs (alone or bundled), and report quantity, scrap and
  quality checks. The kiosk queues its actions while it is offline and replays them in order when the link returns.
- Approve time registrations per worker and day. Overtime and shift pay types come from the rules. Approved days are
  locked and exported for payroll (wave 43); job-card hours post to production at the worker's cost rate.
- Read machine states and counts from the OPC-UA or MQTT edge connector. Record downtime reasons and read the OEE
  figure (availability × performance × quality).
- Get andon alerts for machine faults and operator requests.
- Run control recipes and PI sheets on the kiosk. Process messages go to their destinations with their retry history.
- Enrol and run the edge connector as a signed image with a Helm chart. New connector versions roll out in rings
  (canary, ring 1, all).

Check before sign-off: the production order cost includes the operators' time at the correct rates; OEE matches the
machine counts for a sample shift; no machine event is lost or duplicated when a Kafka broker restarts (AC-25.3).

## Month-end sequence for this phase

1. Report finished production and end the orders that are complete. Review the open orders with WIP.
2. Settle the production variances and confirm that the remaining WIP reconciles to the GL WIP account.
3. Approve the time registrations for the period and check that they reached production costing.
4. Run the planning and scheduling cycle for the next period and work the action messages.

Related: [phase 3 guide](phase-3-procure-to-pay.md) for the material and receipts a production order consumes,
[phase 5 guide](phase-5-order-to-cash.md) for the sales orders that create demand, and the
[implementer guide](../implementer-guide.md) for the parallel-run checks before cutover.
