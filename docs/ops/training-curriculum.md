# Training curriculum (FR-50.11)

Six role-based tracks. Each track lists the audience, the duties it covers (PRD 9.50.17), the modules it
uses, hands-on labs, and the assessment that must be passed before access is granted. Tracks are
sequenced: a learner takes Track 1 before any other track, and PlatformAdmin and ReleaseManager learners
pass Track 5 before Track 6.

Assessment rule: a practical lab is passed only when the learner completes it in the sandbox tier, with
the expected end state checked by the lab's verifier, not by self-report. Written questions are a
supplement, never the pass criterion for a duty that changes data.

| # | Track | Audience | Duties covered | Length |
|---|---|---|---|---|
| 1 | Platform foundations | everyone with production or sandbox access | all (common) | 1 day |
| 2 | Functional key user | finance, procurement, sales, warehouse key users | business duties per module | 3 days |
| 3 | Tenant administrator | customer administrators | TenantAdmin | 2 days |
| 4 | Implementer | partner and internal implementers | migration, localization, cutover | 5 days |
| 5 | SRE and on-call | operations, on-call responders | OnCallResponder, PlatformView | 3 days |
| 6 | Platform operator and release | platform operators, release managers, commercial operations | PlatformAdmin, ReleaseManager, LicenseIssuer, StatusPagePublisher, CutoverDecisionOwner | 4 days |

---

## Track 1: Platform foundations (1 day)

**Audience:** every person with an account in a production or sandbox tenant.

**Covers**
- Tenancy and legal entities: what a tenant is, what a legal entity scope narrows, why the
  `Passage-Legal-Entity` header can only narrow access, never widen it.
- Duties and sign-in: duties are granted by policy, not by job title; a session can expire during a
  change; step-up sign-in for sensitive actions.
- Idempotency and retries: why a retried posting is safe when it carries the same `Idempotency-Key`,
  and why a changed body with the same key is rejected.
- Errors: reading an RFC 9457 problem response, the `code` and `serviceCode` fields, and the error
  code list in each service README.
- Posted documents are immutable: corrections are reversals or credit notes, never edits.
- Support basics: how to report an incident with the correlation ID, what not to paste (personal data).

**Lab 1.1 (sandbox):** sign in, change the legal entity scope, and post a balanced journal. Verifier
checks the journal status and the scope recorded on the document.
**Lab 1.2:** submit a duplicate post with the same key and confirm the replay header; then submit a
changed body with the same key and read the rejection code.

**Assessment:** both labs passed by verifier; 10 multiple-choice questions, pass at 8.

---

## Track 2: Functional key user (3 days)

**Audience:** finance, procurement, sales, and warehouse key users who configure and support a module.

**Covers, one block per functional guide** (one guide per phase 1–8 module, FR-50.11):
- Ledger: journal entry and posting, periods and close, reconciliation of subledgers to the journal.
- Procurement: purchase order to payment, three-way matching, payment runs.
- Sales: pricing, available-to-promise, order to cash, credit checks.
- Inventory and warehouse: receipts, shipments, on-hand, cycle counts, pick and pack from mobile devices.
- Manufacturing: production orders, material and capacity planning, settlement to finished goods.
- Treasury: bank statements, reconciliation, fixed assets and depreciation runs.
- Workforce and projects: payroll run review, expense approval, project billing.
- Analytics and copilot: reading a dashboard, asking the copilot a question, what suggest mode means
  (the copilot proposes; a person approves every write).

**Labs**
- 2.1 Close a sample month: post the period's journals, run subledger reconciliation, and resolve the
  listed differences. Verifier: all subledgers equal the journal control accounts.
- 2.2 Order to cash in the sandbox: create an order, reserve stock, ship, invoice, and apply the receipt.
  Verifier: smoke process P02 passes.
- 2.3 Explain a variance: use the analytics explorer to trace a revenue difference to its journal lines.

**Assessment:** labs 2.1 and 2.2 passed by verifier; a 15-minute oral walkthrough of one module with an
assessor.

---

## Track 3: Tenant administrator (2 days)

**Audience:** customer administrators (TenantAdmin duty). Not for operators (Track 6).
**Guide:** [tenant administrator guide](tenant-administrator-guide.md).

**Covers**
- Users and roles: assigning roles within the license user types (full, operational, self-service).
  Reading the license state banner: GRACE allows sign-in with a warning; EXPIRED blocks new sign-ins but
  not data export or admin access (BR-50.8).
- Segregation of duties: reading an SoD conflict, why the conflict is HIGH for some combinations, and how
  a mitigation is recorded. A user with a HIGH unmitigated conflict is not a go-live user.
- Cost and capacity: reading the monthly tenant cost statement (USD and EUR, ECB month-average FX) and
  the capacity estimate for planned growth.
- Release rings: the weekly, biweekly, and monthly rings; when a pinned ring is used; what a release note
  tells the administrator to do (migrations are automatic; any manual step is listed).
- Data export and decommission: what a retention export contains, who must approve a decommission (a
  second approver is required), and how long the export is kept (country pack retention, BR-50.9).
- Incident communication: subscribing to the status page; reading an incident notice.

**Labs**
- 3.1 Assign a user a self-service role, then read the resulting license usage line.
- 3.2 Read a sample tenant cost statement and reconcile one line to the metered usage.
- 3.3 Plan a pinned release ring and write the notice to users.

**Assessment:** labs 3.1 and 3.2 passed by verifier; a written case study on an SoD conflict (pass at 80%).

---

## Track 4: Implementer (5 days)

**Audience:** partner and internal implementers who migrate a customer from AX or SAP.
**Guide:** [implementer guide](implementer-guide.md).

**Covers**
- Migration pipeline: extraction, mapping to Passage metadata, validation, load, and reconciliation.
  Migration packs for AX and SAP and their reconciliation statuses (RECONCILED, MISMATCH, SIGNED_OFF).
- Localization: installing country packs, running one statutory fixture per legal entity, tax and
  e-invoicing configuration. Localization is verified, not assumed.
- Parallel run: running a month in both systems, explaining each unexplained difference, and the sign-off.
- Cutover: the 14-item go/no-go checklist (9.50.1), the T+24 h decision record, the rollback plan and
  when to use it. Opening trial balance signed by the customer controller.
- Hypercare: 24×5 response targets, the daily stand-up, change freeze rules, and the exit criteria.
- Environments and the sandbox: re-creating sandboxes from masked production through provisioning.
- Extensions: metadata extensions instead of code changes; what release notes map to customer changes.

**Labs**
- 4.1 Migrate a sample AX company fixture and reach RECONCILED with 0 MISMATCH.
- 4.2 Run a parallel-run month on fixtures and explain every difference (0 unexplained).
- 4.3 Run a mock cutover: complete the checklist in a sandbox, record a GO decision, then a NO-GO decision
  for an injected open item.
- 4.4 Rehearse the rollback plan and time it against the 15-minute rollback target.

**Assessment:** labs 4.1 to 4.4 passed by verifier; a peer review of one migration mapping.

---

## Track 5: SRE and on-call (3 days)

**Audience:** operations engineers and on-call responders (OnCallResponder, PlatformView duties).

**Covers**
- Service levels: the SLO table (FR-50.1), error budgets, fast and slow burn alerts (BR-50.6), and why a
  release with a fast-burn alert in the first 30 minutes rolls back automatically.
- Severity definitions, response times (Sev-1: 15 minutes; Sev-2: 1 hour), escalation, and the status page
  update rules.
- Runbooks: every alert links to its runbook. Practice the failover, fail-back, Postgres primary loss,
  Kafka broker replacement, cache stampede, Temporal replication lag, certificate expiry, object-store
  replication lag, and release rollback runbooks.
- Capacity and cost signals: what a capacity model input means, and how a sudden change in journal
  lines per hour shows up in dashboards before it becomes an SLO miss.
- Reconciliation as a signal: the daily subledger-to-journal check and the lakehouse-to-GL check; what a
  red result means and who is paged.
- Reading the game-day report and the baseline manifest; why the hash chain head check is the Sev-1 gate
  (BR-50.3).

**Labs**
- 5.1 Triage a replication-lag alert from a dashboard to the runbook step that resolves it.
- 5.2 Run the baseline and verify steps of a game day on the sandbox fixture (`gameday/baseline.py`) and
  explain each failed check if one is injected.
- 5.3 Perform a Kafka broker replacement drill on the sandbox cluster and record the recovery time.

**Assessment:** labs 5.1 and 5.2 passed by verifier; a timed incident simulation (Sev-2, 60 minutes) graded
against the runbook steps.

---

## Track 6: Platform operator and release (4 days)

**Audience:** platform operators with PlatformAdmin, ReleaseManager, LicenseIssuer, StatusPagePublisher,
or CutoverDecisionOwner duties. The track teaches these duties together, but the grants still follow
9.50.17: LicenseIssuer and PlatformAdmin are a HIGH conflict, so passing the track never grants both to
one person, and each learner is assessed only for the duties they will hold.

**Covers**
- Two-person rules: failover needs two distinct PlatformAdmin confirmations within the window
  (OPS-1702); tenant decommission needs a second approver; no operator posts business documents
  (OPS-001 HIGH conflict).
- Failover and fail-back: the procedure, the RTO and RPO targets, the lag gate (`FAILOVER_MAX_LAG_S`) and
  when `--accept-data-loss` is allowed.
- Release management: promoting by digest, pausing a ring, the four-eyes rule (CHG-001), automatic
  rollback, and the expand/contract rule (a column is dropped only after the code that used it has
  shipped in an earlier release; the linter enforces this).
- Licensing: installing a license file, reading its signature, GRACE and EXPIRED behaviour, and why a
  signature failure (LIC-1804) means reinstall the issued file, not edit it.
- Provisioning and decommission: the provisioning request, residency rules (a region must be deployed),
  the 15-minute target, and the retention export.
- Cutover decision: recording the go/no-go decision and its 14 evidence links.
- Public communication: release notes, the status page, and translated customer-facing incident notices.

**Labs**
- 6.1 Provision a sandbox tenant in the target residency region and record the timing.
- 6.2 Run a failover in the sandbox with two confirmations; verify the timeline and the RTO and RPO fields.
- 6.3 Promote a release by digest, trigger a rollback in the sandbox, and confirm the previous digest is
  running and the ring is paused.
- 6.4 Install a license file, then read the state for a tenant in GRACE.

**Assessment:** labs 6.1 to 6.4 passed by verifier; a review board sign-off on one release plan.

---

## Delivery and records

- Each track ends with the assessment. Results are recorded against the learner's role grant, not in a
  separate spreadsheet.
- Labs run on the sandbox tier only. No lab uses production data; sandboxes are re-created from masked
  production (BR-50.5).
- The curriculum is versioned with the docs site. A release that changes a duty or a runbook step updates
  the affected track in the same release.
