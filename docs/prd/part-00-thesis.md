# Part 0A — The 10× Thesis: What Top-Tier Manufacturing ERPs Get Wrong and How Passage Fixes It

Passage is not a cheaper AX or a friendlier SAP. It emulates the full capability surface of the best manufacturing ERPs — SAP S/4HANA, Dynamics 365 Finance & Supply Chain, Infor LN / CloudSuite Industrial, Epicor Kinetic, IFS Cloud and Plex — and then removes the structural reasons those systems are slow, error-prone, expensive to use and closed. "10×" is measured, per wave, on four axes:

| Axis | What we measure | Baseline (typical AX 2012 / D365 / S/4 deployment) | Passage target |
|---|---|---|---|
| **Easier** | Clicks and screens per task; training hours to competence; share of tasks completed without a super-user | Sales order with 5 lines: 40–60 clicks across 4 forms; AP clerk training 3–5 days | ≤ 1/10 of clicks; a new user completes a guided task in < 30 min |
| **Faster** | Latency of the business answer (not the screen): MRP, inventory close, ATP, period close, report | MRP regeneration 2–8 h nightly; inventory close 1–6 h; month-end close 5–10 business days | MRP incremental < 2 min, full < 10 min; close continuous; statements real-time |
| **More accurate** | Reconciliation breaks per month; silent rounding/FX errors; duplicate master data; manual journal share | Subledger-to-GL breaks every close; 3–8% duplicate customers/vendors; 20–40% of journals are corrections | Zero unexplained breaks (checked daily by machine); < 0.5% duplicates; corrections < 5% |
| **Open** | Days to connect a new system; share of data reachable by API/event; integration projects per year | Weeks per interface (AIF/IDoc mapping projects); large share of logic reachable only from the UI | Any system on the network connects in < 1 day with no middleware; 100% of entities and events reachable |

## 0A.1 The structural pain-point ledger

Each pain point below names the AX/SAP mechanism that causes it, the root cause, the Passage design that removes it, and the wave where it ships. Wave files repeat this at wave granularity under "Pain points solved (10× lens)".

### A. Usability

| # | Pain point today | Where it lives in AX / SAP | Root cause | Passage answer | Wave |
|---|---|---|---|---|---|
| U1 | Dozens of forms to complete one business task; fields scattered across tabs and FastTabs | AX `SalesTable` form with 20+ tabs; SAP `VA01` header/item/schedule-line screens | Forms mirror tables, not tasks | Task-oriented screens generated from metadata with the *task* as the unit (order intake, receipt, close task); line grids with inline edit; the full document on one scrollable page | 5, 27 |
| U2 | Lookups require knowing the code (vendor account, main account, item number) | `MainAccount` lookup in `LedgerJournalTrans`; SAP GL account search help | Keys are the primary identifier; search is secondary | Global typeahead on name/description/any attribute, ranked by recency and role; keys shown but never typed | 5 |
| U3 | Personalization is per-user, fragile, lost on upgrade | AX usage data (`SysLastValue`); SAP GUI variants | Personalization stored outside metadata | Saved views are first-class, versioned, shareable, role-defaulted; survive upgrades because they target metadata ids | 5 |
| U4 | Batch jobs hide status; users poll "Batch jobs" form to see if posting finished | `BatchJob`/`BatchJobHistory`; SAP `SM37` | Fire-and-forget batch framework with no UI contract | Every long-running action returns a tracked workflow with progress, ETA and a notification; the originating screen updates live | 4, 5 |
| U5 | Error messages name internal objects ("Account structure does not allow this combination") | AX infolog; SAP message classes | Validation written for developers | Validation messages are metadata with user text, remediation link and the exact field; errors shown at the field as you type | 3, 5 |
| U6 | Shop-floor and warehouse UIs are RF text menus or require a PC | AX `JmgRegistration` terminal; SAP ITSmobile/RF | UI frameworks predate touch and offline | Touch-first PWA kiosk and scanner apps with offline queues; one scan completes a step | 25, 32 |
| U7 | Excel round-trips to do bulk edits | AX Excel add-in; SAP GUI copy/paste | No bulk-edit grid | Every list page supports multi-row inline edit, paste from spreadsheet with validation preview, and undo | 5 |
| U8 | Approval requires logging in to the ERP | AX workflow work items; SAP Business Workplace (`SBWP`) | Workflow inbox is inside the client | Approvals in email/Slack/Teams/mobile with one-tap approve, full context and the same audit trail | 4 |
| U9 | Copilot features are bolted on and read-only | D365 Copilot summaries; SAP Joule Q&A | AI layer has no safe action surface | Every API is an MCP tool under the user's permissions; agents draft, users approve; explain-this-record everywhere | 47 |

### B. Speed

| # | Pain point today | Where it lives in AX / SAP | Root cause | Passage answer | Wave |
|---|---|---|---|---|---|
| S1 | MRP runs overnight; planners work on stale data; a changed order waits until tomorrow | AX `ReqCalc` regeneration; SAP `MD01` total planning | Full regeneration over all items; pegging recomputed from scratch | Net-change planning engine keyed by events (order/receipt/BOM change) with incremental re-plan of affected low-level codes; full regen < 10 min for 50k items | 22 |
| S2 | Inventory close/recalculation blocks posting for hours | AX `InventClosing`; SAP material ledger `CKMLCP` | Settlement of issues to receipts is a batch over all transactions | Running costing (continuous settlement on each issue for FIFO/avg) so close is a verification pass, not a computation; partitioned by item | 20 |
| S3 | ATP is computed per line with locks; order entry slows under load | AX `InventOnHand`/CTP; SAP ATP with `VBBE` requirements | Row-level locking on on-hand and requirement tables | Cumulative ATP projection maintained from the inventory event stream; reads lock-free; promise computed in memory | 27 |
| S4 | Month-end close takes 5–10 days of manual steps | AX period close checklist is a list; SAP closing cockpit sequences manual transactions | Postings are batch; reconciliations are manual | Continuous close: every subledger reconciles to the universal journal daily by machine; close checklist tasks auto-complete when their check passes | 14, 16+ |
| S5 | Reports run against OLTP and time out, or against a cube that is a day old | AX SSRS on `GeneralJournalAccountEntry`; SAP BW loads | No streaming path from transaction to analytics | CDC → Iceberg lakehouse with p95 < 60 s lag; statements served from pre-aggregated balances rebuilt from the journal | 11, 46 |
| S6 | Posting a 10k-line journal takes minutes and locks the period | `LedgerJournalCheckPost`; SAP `FB50` with `BSEG` inserts | Row-by-row posting through class hierarchies; synchronous number sequences | Append-only journal with batch insert, idempotency keys and asynchronous balance projection: 5,000 lines/s sustained | 11 |
| S7 | Deploying a customization takes a release window | AX model store deploy with AOS restart; SAP transports with downtime | Code and metadata are compiled into the runtime | Metadata extensions apply live; code ships via GitOps with digest rollback in < 15 min | 1, 3 |

### C. Accuracy

| # | Pain point today | Where it lives in AX / SAP | Root cause | Passage answer | Wave |
|---|---|---|---|---|---|
| A1 | Subledgers drift from the GL (inventory value ≠ inventory account; AR aging ≠ AR control) | `InventSum` vs `GeneralJournalAccountEntry`; SAP `BSID` vs `SKB1` balances | Subledgers post to the GL through separate code paths with their own rounding | One posting API; subledgers are projections of the same event stream; daily machine reconciliation with zero unexplained breaks as a release gate | 11, 16, 19, 30 |
| A2 | Rounding and FX differences appear as "penny" entries | AX `LedgerJournalTrans` rounding to accounting currency; SAP `BSEG` rounding differences | Rounding at line level then summing | Three-currency amounts stored per line; rounding applied once at document level with explicit rounding lines; ISO 4217 minor units enforced | 12 |
| A3 | Duplicate customers/vendors/items | `CustTable` created freely; SAP `KNA1` duplicates across sales orgs | No dedup at creation; roles not layered on one party | One party per real-world entity with fuzzy-match blocking at creation (precision ≥ 0.95) and governed merge | 7 |
| A4 | Wrong tax because tax group ↔ item tax group intersections are configured by hand | `TaxGroup`/`TaxItemGroup` intersection; SAP `FTXP` condition records | Determination is a configuration lattice with no test harness | Determination engine with a 500-scenario fixture pack reviewed by accountants; explain-this-tax on every line | 13 |
| A5 | Negative inventory and phantom on-hand | `InventSum` allows negative physical when `InventModelGroup` permits; SAP `MMBE` vs `MSEG` timing | Projection updated by application code that can skip | On-hand is a projection of immutable inventory events; policy enforced at the event write; projection rebuilt from events and compared nightly | 16 |
| A6 | Costing variances nobody can explain | `InventCostTrans` variances; SAP `KKS1` | Variance types computed after the fact with no linkage | Variance decomposition attached to each ended order with lot-size, quantity, price and substitution components that sum exactly | 21, 39 |
| A7 | Master data changed without approval or trace | Direct edit of `VendTable` bank account; SAP `XK02` | Audit log optional (`SysDatabaseLog`) and slow | Field-level immutable audit from CDC for every entity; sensitive fields (bank accounts, credit limits) require workflow | 5, 7 |

### D. Openness and interoperability

| # | Pain point today | Where it lives in AX / SAP | Root cause | Passage answer | Wave |
|---|---|---|---|---|---|
| O1 | Each interface is a project (AIF service, IDoc mapping, custom data entity) | AX AIF/`DMF` entities; SAP `WE20` partner profiles, PI/PO | Integration surface is bespoke per object | Every entity and event is published automatically (OpenAPI, AsyncAPI, MCP) from metadata; new entity ⇒ new API with no work | 3, 4, 35 |
| O2 | External systems poll for changes | `DMF` change tracking; SAP change pointers (`BDCP2`) | No event stream | Kafka topics per entity with CloudEvents, replay from any offset, webhooks for partners | 4 |
| O3 | MES/PLM/SCADA need a separate middleware product | SAP MII, PI/PO; AX via BizTalk/Logic Apps | ERP speaks only its own protocols | Native OPC-UA/MQTT ingestion, EDI engine, Peppol, SFTP, MCP — eight integration surfaces with one auth model | 25, 35 |
| O4 | Vendor cloud lock-in (LCS, RISE) | D365 only on Microsoft-managed Azure; S/4 Cloud on RISE | Platform tied to the vendor's operations | Any conformant Kubernetes; Helm charts; self-host or managed | 1 |
| O5 | Customizations block upgrades | X++ overlayering; ABAP modifications | Extension model is code in the vendor's layer | Extensions are metadata and sanctioned hooks; additive API guarantees; upgrades are image promotions | 3 |
| O6 | Analytics requires exporting to BYOD/BW | Entity store; BW extractors | No open analytical surface | Iceberg tables readable by any engine (Trino, Spark, DuckDB, Power BI, Tableau) with governed metrics | 46 |

## 0A.2 Interoperability contract ("all systems on the network can use it")

Passage treats the ERP as a service on the customer's network, not a destination. Every capability is reachable through the same eight surfaces with one identity and one policy engine:

| Surface | Used by | Auth | Guarantee |
|---|---|---|---|
| REST (OpenAPI 3.1, generated per entity) | Apps, scripts, partners | OIDC bearer token or API key, OPA-authorized | p95 < 50 ms simple reads; additive versioning |
| GraphQL (read model) | BI tools, portals | Same | Cross-entity reads without N+1 |
| Kafka topics (CloudEvents, AsyncAPI) | MES, WMS, data platforms, other ERPs | mTLS + ACLs per tenant | At-least-once, ordered per key, 7-day replay |
| Webhooks | SaaS partners | Signed (HMAC) deliveries | Retries with backoff, dead-letter, replay |
| MCP | AI agents (Claude and others) | Delegated user token; agent ≤ user permissions | Every tool call audited |
| EDI (X12/EDIFACT via AS2/SFTP) and Peppol | Trading partners, tax authorities | Partner certificates | Acknowledgments, SLA monitor |
| OPC-UA / MQTT | Machines, PLCs, SCADA | Device certificates | 10k events/min per plant, no loss on broker restart |
| File / SFTP | Banks, legacy systems | Keys in Vault | ISO 20022, BAI2, NACHA, CSV with schema validation |

Rule: **if it is not reachable through these surfaces, it does not ship.** Each wave's API/event section is reviewed against this rule before close.

## 0A.3 How "deep knowledge of AX and SAP" is applied

We deliberately keep what those systems got right and name it in every wave's parity map:

- **From AX:** the role/duty/privilege security model; inventory dimensions (product/storage/tracking) as a first-class concept; `InventTrans` status semantics; trade-agreement search order; number sequences per legal entity; financial dimensions with account structures; coverage groups; lean production flows.
- **From SAP:** the universal journal (ACDOCA) as single source of truth; parallel ledgers and currency types; condition technique for pricing and tax; posting periods per module; EWM's location-directive rigor; QM inspection lots and usage decisions; results analysis for projects; Group Reporting eliminations.

And we remove the parts that exist only because of their era: batch-first processing, table-shaped forms, code-based extensions, closed integration layers, vendor-bound operations.
