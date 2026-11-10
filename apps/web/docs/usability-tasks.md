# Usability task scripts

The tasks a moderator gives to a participant in a usability session (`usability-baseline-report.md` records the results; `usability-gate.md` says how they gate a release). There is one workspace per persona, defined in `src/shell/workspaces/workspaces.json`. Each persona has the tasks that person does on a normal day, in the order they usually happen.

## Running a session

- **Participants** come from the target market: five per persona, none of them developers or people who built the screens. Someone who does the job today (or did within the last two years) is the best fit.
- **Environment.** A demo company (the "Demo company" in the admin area) in a fresh browser profile signed in as the persona's role. Reset the data before every participant so everyone starts from the same state.
- **Moderation.** Read the scenario aloud and hand over the starting page only. Don't name menu items or buttons. Ask the participant to think aloud. Don't help; if they are stuck for 3 minutes, note the time, say "Let's move on", and score the task as failed.
- **Record** for each task: success (see the rule below), time on task in seconds, the number of errors (wrong turns that needed backing out, rejected submits, messages the participant said they didn't understand) and anything they said.
- **Success** is one of three values. *Pass:* the participant reached the stated result alone. *Pass with help:* only after a hint from the moderator. *Fail:* gave up, ran out of time, or finished with a wrong result (for example a wrong amount or the wrong record).
- **Time limit** is 5 minutes per task unless the task says otherwise.
- After the tasks, ask the participant to fill in the System Usability Scale (SUS) questionnaire (see `usability-baseline-report.md`).

## Tasks

Each row gives the scenario, where the participant starts, and what counts as done. "Starts at" is the page the moderator opens; "Done when" is checked against the data, not against what the participant says.

### Accounts payable clerk (`ap-clerk`)

| # | Scenario | Starts at | Done when |
|---|---|---|---|
| AP1 | A vendor invoice arrived for goods you already received. Post it so it matches the receipt. | Post vendor invoice | The invoice is posted, matched to the receipt, and the posted amount equals the invoice total. |
| AP2 | The invoice total is higher than the received value. Find out why it didn't match. | Post vendor invoice | The participant names the difference (quantity or price) and where it is shown. |
| AP3 | Find the goods received but not yet invoiced for one vendor. | Accounts payable | The correct list for that vendor is on screen and the participant can name its total. |
| AP4 | Pay two posted invoices for one vendor in a single payment. | Accounts payable | One payment exists that covers both invoices. |
| AP5 | A vendor says an invoice is unpaid. Check its status. | Accounts payable | The participant states the right status and, if paid, the payment date. |
| AP6 | Find who a pending approval is waiting on. | Inbox | The participant names the approver. |

### Accounts receivable clerk (`ar-clerk`)

| # | Scenario | Starts at | Done when |
|---|---|---|---|
| AR1 | A customer paid a single invoice by bank transfer. Record the payment. | Record payment | The payment is applied and the invoice shows as paid. |
| AR2 | A customer paid less than the invoice. Record it so the rest stays open. | Record payment | The payment is applied and the remaining amount is open. |
| AR3 | Find all open items for one customer that are overdue. | Open items | The correct list is on screen, and the participant names the total overdue. |
| AR4 | A customer promised to pay next Friday. Note it. | Collections worklist | A payment promise with the right date exists. |
| AR5 | A customer disputes an invoice. Open a dispute. | Collections worklist | A dispute is recorded against that invoice. |

### Buyer (`buyer`)

| # | Scenario | Starts at | Done when |
|---|---|---|---|
| BU1 | Raise a purchase order for 100 units of a product from a known vendor. | Purchase orders | The order exists with the right vendor, product, quantity and price. |
| BU2 | Send the order for approval. | Purchase orders | The order is submitted and shows who has to approve it. |
| BU3 | A vendor asks for a later delivery date on a line. Change it. | Purchase orders | The line shows the new date. |
| BU4 | Find which orders for one vendor are still waiting for goods. | Purchase orders | The participant names the open orders and quantities. |
| BU5 | Approve a requisition and turn it into an order. | Requisitions | An order exists that came from the requisition. |
| BU6 | Request quotes for a product from three vendors. | Requests for quotation | A request for quotation exists with three vendors. |

### Receiving and warehouse (`warehouse`)

| # | Scenario | Starts at | Done when |
|---|---|---|---|
| WH1 | A delivery arrived against a purchase order. Receive it. | Receive goods | A receipt is posted with the right quantities. |
| WH2 | Only part of the order arrived. Receive what came. | Receive goods | The receipt shows the partial quantity and the order still shows the rest open. |
| WH3 | Check how much of a product is on hand at your warehouse. | On hand | The participant states the right quantity. |
| WH4 | Pick, pack and ship a customer order. | Fulfilment workbench | A shipment is created and marked shipped. |
| WH5 | Count a shelf and enter the counts. | Count sheets | The counts are saved and any difference is visible. |

### Sales representative (`sales-rep`)

| # | Scenario | Starts at | Done when |
|---|---|---|---|
| SR1 | Take a phone order from a customer for two products. | New sales order | The order exists with the right customer, products and quantities. |
| SR2 | Check whether you can promise 50 units by a date. | Availability | The participant states yes or no and the earliest date. |
| SR3 | The order went on credit hold. Find out why. | Credit holds | The participant names the reason and the amount over the limit. |
| SR4 | Find where a customer's order is in delivery. | Order progress | The participant states the right stage. |
| SR5 | Give a customer a quote for a product. | Quotations | A quotation exists with the price the price list gives. |

### Production planner (`production-planner`)

| # | Scenario | Starts at | Done when |
|---|---|---|---|
| PP1 | Run planning for the next four weeks. | Planning | A run completes and its results are visible. |
| PP2 | Find the products the plan says to order or make this week. | Planned orders | The participant names the products and quantities. |
| PP3 | Release a planned order as a production order. | Planned orders | A production order exists from that planned order. |
| PP4 | Find which materials a production order is short of. | Material requirements | The participant names the short materials. |
| PP5 | Move a job to a different day and see whether the schedule still works. | Scheduling | The job is on the new day and the participant states whether the load is feasible. |

### Shop floor supervisor (`shopfloor-supervisor`)

| # | Scenario | Starts at | Done when |
|---|---|---|---|
| SF1 | See who is clocked in right now. | Clocked in | The participant states the right names or count. |
| SF2 | Find the machine that has been down the longest today. | Machines | The participant names the machine and the downtime. |
| SF3 | Respond to an andon call. | Andon | The call is acknowledged and closed with a reason. |
| SF4 | Fix a worker's time entry that has the wrong end time. | Time corrections | The correction is saved with the right time. |
| SF5 | Check how a production order is doing against plan. | Production | The participant states progress and the variance. |

### Controller (`controller`)

| # | Scenario | Starts at | Done when |
|---|---|---|---|
| CT1 | Post a manual journal that moves 1,000 between two accounts. | Journal batches | The batch is posted and balanced. |
| CT2 | Set up a monthly recurring journal. | Recurring journals | The recurring journal exists with the right schedule. |
| CT3 | Check that the trial balance balances for last month. | Trial balance | The participant states the totals and that they are equal. |
| CT4 | Close last month. | Close | The period shows closed, or the participant names what blocks it. |
| CT5 | Find the journal behind a number on a report. | Financial reports | The participant reaches the journal entry. |
| CT6 | Run the depreciation for last month. | Depreciation runs | A run exists for the month. |

### People and payroll administrator (`hr-admin`)

| # | Scenario | Starts at | Done when |
|---|---|---|---|
| HR1 | Hire a new employee. | Workers | The employee exists with the right start date and position. |
| HR2 | Approve a time-off request. | Time off requests | The request shows approved. |
| HR3 | Change an employee's job title. | Workers | The title is changed with an effective date. |
| HR4 | Find certifications that expire in the next 60 days. | Expiring certifications | The participant names the right people. |
| HR5 | Open a position to hire for. | Requisitions | A requisition exists for the position. |

### Administrator (`tenant-admin`)

| # | Scenario | Starts at | Done when |
|---|---|---|---|
| TA1 | Add a legal entity for a new subsidiary. | Legal entities | The entity exists with the right country and currency. |
| TA2 | Add a new user who can approve purchase orders but not post invoices. | Users | The user exists with exactly those permissions. |
| TA3 | Find who changed a setting last week. | Audit trail | The participant names the person and the change. |
| TA4 | Add a warehouse to a site. | Warehouses | The warehouse exists under the right site. |
| TA5 | Import a list of products from a file. | Import | The import finishes and the participant can state how many rows loaded and how many failed. |

## Keeping the scripts current

- When a screen on a task's path changes name or route, update the "Starts at" column the same week.
- Add a task when a persona gains a daily job. Don't remove a task that has a baseline; retire it by marking it "retired" in the baseline report so its trend is kept.
- Task ids (AP1, BU2, ...) never change once a baseline exists.
