# Phase 4: Manufacturing & Planning (waves 21-25)

Production orders and WIP, MRP/MPS, capacity and scheduling, process and lean manufacturing, shop-floor execution and IoT. A discrete or process manufacturer can build product on Passage.

## Wave 21: Production Orders, WIP & Order-Based Costing

**Phase 4 — Manufacturing & Planning**

**Goal.** Create, estimate, release, start, report and end production orders with material issues and route consumption posting WIP and variances to the GL.

**Replaces**

- *AX / D365:* Production orders (create/estimate/schedule/release/start/report-as-finished/end), picking lists, route cards, job cards, production posting profiles
- *SAP:* Production orders (CO01), order confirmation (CO11N), goods issue/receipt for orders, settlement (KO88), WIP calculation (KKAX)

**Deliverable**

- Production order lifecycle with status transitions and BOM/route explosion at creation; estimation (planned cost)
- Picking lists (backflush/manual), route cards, job cards; report as finished with by-/co-products and scrap
- WIP postings and order end with cost settlement: actual vs planned → variances (material, labor, overhead)
- Subcontracting operations with vendor PO linkage
- Production order inquiry: cost roll, status log, transactions
- Production order split/merge with proportional WIP transfer, order change management (ECO re-explosion with change cost) and order-specific BOM/route edits
- Trigger points on operation feedback (insert rework operations, create rework orders), rework orders, and as-built serial/batch genealogy captured at picking and RAF

**Proof (testable acceptance)**

- [ ] Lifecycle state machine property test: every illegal transition rejected, every legal one posts the expected inventory/GL entries
- [ ] Costing fixtures: 50 orders with known consumptions produce expected WIP and variance postings
- [ ] Backflush test: RAF of 100 units consumes BOM quantities × (1 + scrap%) exactly
- [ ] Order end with partial RAF leaves remaining WIP reconciled to the GL WIP account

**Deploy**

Production control workspace; demo plant runs its first production order end-to-end.

**References**

- [AX 2012 production control](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/production-control)
- [SAP PP production orders](https://help.sap.com/docs/SAP_ERP)
- [Odoo MRP (reference flows)](https://www.odoo.com/documentation/18.0/applications/inventory_and_mrp/manufacturing.html)

**Depends on:** wave 20

---

## Wave 22: Master Planning: MRP, MPS & Forecasting

**Phase 4 — Manufacturing & Planning**

**Goal.** Net requirements calculation across demand, supply and BOM levels generates planned purchase/production/transfer orders with action and delay messages.

**Replaces**

- *AX / D365:* Master planning (static/dynamic plans), coverage groups, planned orders, action/futures messages, demand forecasting
- *SAP:* MRP (MD01/MD02), MRP types, planned orders, exception messages, MPS, flexible planning / SOP

**Deliverable**

- Planning engine (Go): low-level code pegging, lead times, coverage groups (requirement/period/min-max), safety stock, planning horizons
- Demand: sales orders, forecasts (with reduction), safety stock, dependent demand; supply: on-hand, POs, production orders, transfers
- Planned orders with firming, grouping, action messages (advance/postpone/cancel) and futures/delay messages
- Forecast model: statistical baseline (ETS/Croston via a Python service) with manual override and consensus workflow
- Explain-this-plan: pegging tree from any planned order to its driving demand
- Simulation plans / planning scenarios (long-term planning, predictive MRP-style rough-cut) with capacity and purchase-volume evaluations and supplier forecast publishing
- DDMRP buffers (decoupling points, buffer profiles, net-flow equation) and priority-based planning
- Quota arrangements across vendors/own production/transfers, warehouse- and subcontractor-level planning scopes (MRP areas), and planning strategies (MTS net/gross, MTO, assemble-to-order, planning without final assembly, subassembly)
- Shelf-life aware pegging, supply schedule workbench and inventory forecast projection

**Proof (testable acceptance)**

- [ ] MRP correctness fixtures: 40 textbook scenarios (APICS CPIM-style) match expected planned orders and dates
- [ ] Scale: 50k items × 12 periods × 5-level BOM plans in < 10 min; results deterministic across runs
- [ ] Pegging tree for every planned order explains 100% of its quantity
- [ ] Optional comparison: results match frePPLe on a shared dataset within tolerance

**Deploy**

Master planning workspace with a nightly plan for the demo plant.

**References**

- [AX 2012 master planning](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/master-planning)
- [SAP MRP](https://help.sap.com/docs/SAP_ERP)
- [frePPLe (open-source planning)](https://frepple.com/docs/current/)
- [statsforecast (ETS/Croston)](https://nixtlaverse.nixtla.io/statsforecast/)

**Depends on:** wave 21

---

## Wave 23: Capacity Planning & Finite Scheduling

**Phase 4 — Manufacturing & Planning**

**Goal.** Operations are scheduled against finite resource capacity with calendars, Gantt visualization and rescheduling, feeding realistic dates back to planning.

**Replaces**

- *AX / D365:* Operations/job scheduling, finite/infinite capacity, Gantt chart, resource load, scheduling direction
- *SAP:* Capacity planning (CM01), capacity leveling, PP/DS (APO/S4 embedded), detailed scheduling board

**Deliverable**

- Scheduler service: operation and job scheduling, forward/backward, finite/infinite capacity, primary/secondary resources, setup optimization
- Interactive Gantt (resources × time) with drag-reschedule and constraint warnings
- Capacity load views by resource/group/period; bottleneck detection
- Scheduling optimization option via OR-Tools CP-SAT (minimize lateness/makespan)
- Integration: schedule dates flow to production orders and master planning
- Resource networks (allowed resource transitions with max wait) as scheduler constraints
- Campaign planning with setup matrix sequencing, changeover/cleanout orders and campaign cleanout cost allocation
- Block planning: capacity blocks restricted by product/batch attribute conditions

**Proof (testable acceptance)**

- [ ] Scheduling fixtures: 30 job-shop instances (OR-Library) produce feasible schedules; CP-SAT mode is within 5% of known optima on small instances
- [ ] No resource double-booking (property test over 10k scheduled jobs)
- [ ] Rescheduling an order on the Gantt persists and re-plans dependents correctly
- [ ] Performance: 5k operations scheduled in < 60 s

**Deploy**

Scheduling workspace with Gantt for the demo plant.

**References**

- [AX 2012 scheduling](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/about-scheduling)
- [SAP PP/DS](https://help.sap.com/docs/SAP_S4HANA_ON-PREMISE)
- [OR-Tools job-shop scheduling](https://developers.google.com/optimization/scheduling/job_shop)
- [OR-Library job-shop instances](http://people.brunel.ac.uk/~mastjjb/jeb/orlib/jobshopinfo.html)

**Depends on:** wave 22

---

## Wave 24: Process Manufacturing & Lean (Formulas, Batches, Kanban)

**Phase 4 — Manufacturing & Planning**

**Goal.** Process manufacturers run formula-based batch orders with potency, co/by-products and catch weight; lean plants run kanban and production flows.

**Replaces**

- *AX / D365:* Process industries: formulas, batch orders, co/by-products, potency, catch weight, batch attributes, shelf life; Lean manufacturing: production flows, kanban rules, kanban boards
- *SAP:* PP-PI process orders, recipes, batch management, PP-REM repetitive manufacturing, KANBAN (PK*)

**Deliverable**

- Batch orders from formulas with scalable quantities, potency-based consumption, co/by-product costing (allocation methods)
- Batch attributes, shelf life, best-before/expiry, batch disposition, catch weight dual UoM
- Lean: value streams, production flows, activities, kanban rules (manufacturing/withdrawal/fixed/event), kanban board, cards
- Backflush costing for lean (periodic)
- Recipe/formula version control integrated with engineering change
- Batch attribute and shelf-life inheritance (batch derivation) with derivation monitor, batch merge, and documentary batches
- Consolidated bulk/packed batch orders, rework batch orders, same-batch reservation and customer sellable days / batch attribute requirements
- Lean subcontracting: service activities on vendor work cells priced by purchase agreements with auto PO and receipt
- Electronic batch record with review by exception, sealed PDF/A + JSON archive, and a 21 CFR Part 11 electronic signature service (step-up re-authentication, signature strategies, signature log)
- Characteristic-dependent planning: attribute-constrained pegging of supply to demand

**Proof (testable acceptance)**

- [ ] Potency fixtures: consumption scales correctly with active-ingredient percentage
- [ ] Co/by-product cost allocation sums to batch cost exactly across methods
- [ ] Kanban simulation: event kanbans are generated from a sales order and complete through the board
- [ ] Catch-weight receipts and sales post correct inventory and financial quantities

**Deploy**

Process and lean workspaces enabled for a second demo plant (food/chem line).

**References**

- [AX 2012 process manufacturing](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/process-manufacturing-production-and-logistics)
- [AX 2012 lean manufacturing](https://learn.microsoft.com/en-us/dynamicsax-2012/appuser-itpro/lean-manufacturing)
- [SAP PP-PI](https://help.sap.com/docs/SAP_ERP)

**Depends on:** wave 23

---

## Wave 25: Shop Floor Execution, Time Registration & IoT

**Phase 4 — Manufacturing & Planning**

**Goal.** Operators clock on jobs from a touch UI, machines report counts via OPC-UA/MQTT, and labor/machine time flows to production costing and payroll.

**Replaces**

- *AX / D365:* Manufacturing execution (job registration, time and attendance), production floor execution (D365)
- *SAP:* SAP MII / Digital Manufacturing, confirmations (CO11N), shop floor control, PP-PDC

**Deliverable**

- Shop-floor UI (tablet/kiosk): clock in/out, start/stop jobs, report quantity/scrap, quality checks, instructions and drawings
- Time & attendance core: registrations, pay-type calculation (overtime, shifts), approval, export to payroll (wave 43)
- IoT ingestion: OPC-UA/MQTT edge connector → Kafka → machine events (counts, downtime, states); OEE calculation
- Andon/alerts, downtime reason codes, operator feedback loop
- Offline-tolerant kiosk (PWA) with sync
- Control recipes (ISA-88 structure) sent to kiosk or process control systems via the edge connector, with control recipe monitor
- PI sheet designer with versioned step library (XSteps equivalent), kiosk PI sheet execution, and PI sheet archive feeding the EBR
- Process messages with categories, destinations (journals, quality results, EBR, Kafka, webhook), retry and monitor
- iot-edge connector as a deployable: signed image/Helm chart/static binary, token enrollment with mTLS certificates, versioned config, heartbeat, ringed rollout with auto-rollback, allowlisted write-back

**Proof (testable acceptance)**

- [ ] Kiosk E2E (Playwright): a shift of operators clocks jobs and the production order cost reflects their time at correct pay rates
- [ ] IoT: simulated machine publishes 10k events/min; OEE dashboard updates within 5 s; no loss on broker restart
- [ ] Offline test: kiosk works disconnected for 30 min and syncs with conflict resolution
- [ ] Phase 4 exit: demo plant manufactures a configured product from planned order to finished goods with full costing

**Deploy**

Shop-floor kiosk app and IoT edge connector Helm chart deployed to the demo plant environment.

**References**

- [D365 production floor execution](https://learn.microsoft.com/en-us/dynamics365/supply-chain/production-control/production-floor-execution-use)
- [OPC UA](https://opcfoundation.org/about/opc-technologies/opc-ua/)
- [Eclipse Mosquitto / MQTT](https://mqtt.org/)
- [OEE definition (ISO 22400)](https://www.iso.org/standard/54497.html)

**Depends on:** wave 24

---
