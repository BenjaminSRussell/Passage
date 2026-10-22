# PRD section template (one file per phase, one block per wave)

Every phase file `docs/prd/phase-N-<slug>.md` follows this layout exactly so the
master PRD can concatenate them. Headings are fixed; content is specific.

```
# Part N — Phase N: <Phase name> (waves A–B)

## N.0 Phase overview
- Business outcome at phase exit (1 paragraph)
- Phase-exit sign-off criteria (bullet list, each measurable)
- Cross-wave data model summary for this phase (table: entity | owning service | key relationships)
- Cross-wave event catalog for this phase (table: event name | producer | consumers | payload summary)
- Phase-level risks (table: risk | likelihood | impact | mitigation | owner)

## N.W Wave W: <Wave name>            ← repeat for each wave in the phase

### N.W.1 Scope and business context
Why this wave exists, what a user can do at the end of it that they could not before,
and what is explicitly out of scope for this wave.

### N.W.2 AX / SAP parity map
Table: Capability | AX 2012 / D365 F&SCM (form, table, class, menu item) | SAP ECC / S/4 (transaction code, table, module) | Passage equivalent (service / entity / screen) | Parity level (full / partial / improved / dropped-with-reason)
Minimum 8 rows. Use real object names (e.g. CustTable, LedgerJournalTrans, InventTrans, BKPF/BSEG, MARA, VA01, ME21N, F110).

### N.W.3 Functional requirements
Numbered FR-W.n (e.g. FR-11.1 … FR-11.14). Each requirement:
- **FR-W.n <title>** — MoSCoW priority (Must/Should/Could).
  Statement in "The system shall …" form, then 2–5 bullet "Notes" with the specific
  behaviors, defaults, limits, and edge cases. Minimum 10 FRs per wave.

### N.W.4 Data model
Table per core entity: Field | Type | Required | Notes (constraints, defaults, FK).
Then invariants as bullets (what must always be true), and indexing/partitioning notes.
Minimum 3 entities per wave, 6+ fields each.

### N.W.5 APIs and events
- REST/GraphQL endpoints table: Method | Path | Purpose | Idempotency | Auth duty
- Commands/queries (if CQRS) and their validation rules
- Events table: Event (CloudEvents type) | Trigger | Payload fields | Consumers
Minimum 6 endpoints and 3 events per wave.

### N.W.6 User experience
Screens/workspaces table: Screen | Primary actions | Key fields/columns | Duty required.
Then notable interaction rules (keyboard, bulk edit, validation timing, personalization).

### N.W.7 Business rules and edge cases
Numbered BR-W.n. Minimum 8, each one precise (rounding, dates, concurrency, status
transitions, negative cases). Include a state machine where the wave has a lifecycle.

### N.W.8 Acceptance criteria
Given/When/Then scenarios, numbered AC-W.n, minimum 8, each mapped to FR ids.

### N.W.9 Test plan and fixtures
- Fixture datasets (name, size, what it covers, where it comes from)
- Unit / contract / property / E2E / performance tests (table: Test | Type | Target | Pass criterion)
- Reconciliation checks (what totals must tie to what)

### N.W.10 Deployment and operations
Helm chart(s), config/env, database migrations, feature flags, scheduled jobs,
SLOs, alerts, runbook entries, rollback procedure.

### N.W.11 Migration from AX / SAP
Source tables → Passage entities mapping table; transformation notes; validation rules;
what is NOT migrated and why.

### N.W.12 Dependencies, risks, open questions
- Depends on (waves and specific capabilities)
- Risks (table)
- Open questions (numbered, with proposed default and decision deadline by wave)

### N.W.13 Parameters and setup
Every configuration option the wave introduces, in the style of AX "<Module> parameters" forms and
SAP SPRO nodes. Table: Parameter | Scope (tenant / legal entity / site / warehouse / user) | Type |
Default | Allowed values | Effect | AX equivalent | SAP equivalent. Minimum 12 rows per wave.

### N.W.14 Inquiries and reports
Table: Name | Type (list inquiry / drill-down / operational report / statutory report / KPI) |
Filters & parameters | Columns / measures | Source projection | AX equivalent (form or SSRS report) |
SAP equivalent (transaction / report). Minimum 8 rows per wave.

### N.W.15 Printed and electronic documents
Table: Document | Trigger | Layout elements (header/line/footer fields) | Formats (PDF / e-invoice /
EDI / email) | Numbering sequence | Legal / retention requirement. Include "none" explicitly if the wave
produces no documents.

### N.W.16 Periodic and batch jobs
Table: Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target |
Failure handling | Monitor / alert. Minimum 4 rows per wave.

### N.W.17 Security duties and privileges
Table: Duty | Privileges (entry points: API paths / screens / jobs) | Segregation-of-duties conflicts |
Default roles that include it. Minimum 6 duties per wave.

### N.W.18 Message catalog
Table: Code (e.g. INV-0421) | Severity (error / warning / info) | User text | Cause | Remediation |
Raised by (FR / BR id). Minimum 10 rows per wave.

### N.W.19 Performance budgets and capacity
Table: Operation | Volume assumption (per tenant per day / peak per minute) | Latency target (p50 / p95 / p99) |
Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load.

### N.W.20 Localization and compliance hooks
What in this wave varies by country or regulation (tax, formats, statutory reports, retention, language,
calendar, currency rounding, privacy), which country packs touch it (wave 48), and the extension point used.
```

Style rules
- Specific over generic: name tables, transactions, fields, statuses, limits, formulas.
- Numbers wherever possible (sizes, latencies, tolerances, retention).
- British/American spelling: American. Dates ISO 8601. Money examples in USD and EUR.
- No marketing language. No "robust", "seamless", "leverage".
- Stack is fixed by docs/adr/0001-platform-stack.md (Kubernetes, Go/TS/Python, Postgres per domain, Kafka+Debezium outbox, Temporal, Keycloak+OPA, React shell). Do not propose other infrastructure.
- Target length per phase file: 6,000–7,500 words.
