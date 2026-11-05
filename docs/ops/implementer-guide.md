# Implementer guide: migrate, localize and cut over a customer (PSG-50, FR-50.11)

Audience: partner and internal implementers who move a customer from AX 2012 / D365 F&O or SAP ECC /
S/4HANA to Passage. This guide covers discovery, migration, localization, parallel run, cutover and
hypercare in order, and links the operator documents the implementer hands over to. Module configuration
is in the functional guides of the owning modules, not here. Track 4 of the
[training curriculum](training-curriculum.md) is the course for this guide.

## Status of the tools this guide uses

The procedure and its gates are fixed by PRD 9.50.1 and 9.50.11. The tools come from other waves; this
table says what is on main today, so a step that cannot run yet is visible before the engagement starts.

| Tool | Where | On main | Not yet (owner) |
|---|---|---|---|
| Master-data migration (mapping DSL, map, validate, load, reconcile) | `services/migration`, `POST /v1/integration/migration-runs` (wave 10) | pipeline, dry run, idempotent re-run, exception queue, run ledger | the route answers 501 `MIG-BACKEND-NOT-CONFIGURED` until the connectors and the batch loader are wired (wave 10, slice 3) |
| Transactional packs with control totals | migration factory, `POST /api/v1/migration/packs/{pack}/runs` ([README](../../tools/migration-factory/README.md), wave 49) | pack catalog (`/migration-factory/packs`), `open_items` and `project_wip` packs, quarantine and release, legacy cross-reference | `gl_balances`, `onhand`, `open_orders`, `wip_prod`, `fixed_assets`, `payroll_ytd`, `bank_balances` answer MIG-0005 (wave 49); AX and SAP extractors (wave 35 integration hub) |
| Parallel-run review | `POST /api/v1/migration/parallel-runs`, explain, sign-off (wave 49) | diff, classification, explain, sign-off gate | replaying the legacy month through the posting APIs: the caller supplies the Passage figures today (wave 49) |
| Cutover runbook | `POST /api/v1/cutover/plans/generate`, step start and completion, rollback with re-keying files, the `/migration-factory/cutover` screens (wave 49) | timeline T-14 d to T+30 d, step timer, go/no-go sequencing, abort deadline, generated rollback plan, two-approver rollback | the go/no-go decision endpoint and `dev.passage.cutover.decision.recorded.v1` (wave 49) |
| Country packs | `services/localization`, the `/compliance/packs` screens (wave 48) | pack catalogue, install checks, tax-id checks | stored installs per legal entity with pack versions (wave 48) |
| Sandbox from masked production | [tenant provisioning](tenant-provisioning.md), FR-48.26 masking | procedure | provisioning API and automation (ops service), masking profile (wave 48) |

Until the go/no-go endpoint ships, record the decision as the PDF decision record (9.50.15) signed by the
CutoverDecisionOwner, and complete the runbook's "Go/no-go meeting" step.

## 1. Engagement shape

The phase lengths below are planning guidance from AX and SAP projects, not PRD targets. The exit gates
are the PRD's.

| Phase | Typical length | Exit gate |
|---|---|---|
| Discovery and scope | 2 to 4 weeks | in-scope processes, legal entities and user list signed off by the customer |
| Environment | 1 week | sandbox re-created from masked production through provisioning |
| Migration rehearsals | 3 to 6 cycles | every pack RECONCILED (no MISMATCH control total) |
| Localization | alongside the rehearsals | country packs installed and one statutory fixture executed per legal entity |
| Parallel run | one full month | prior month SIGNED_OFF with 0 unexplained variances |
| Cutover | T-14 d to T+36 h | all 14 go/no-go items done at T+24 h ([go-live checklist](go-live-checklist.md)) |
| Hypercare | T+1 d to T+30 d (`hypercare.days`, 14 to 90) | exit criteria in the go-live checklist |

Do not shorten the parallel run. A month is the unit of evidence for the close.

## 2. Discovery

1. Inventory the legacy landscape. For AX: companies (legal entities), production and sandbox
   environments (LCS), batch groups and job schedules (`BatchJob`), number sequences, and the reports the
   customer relies on. For SAP: company codes, plants, storage locations, the user list with roles, and the
   scheduled jobs (`SM37`).
2. Map each in-scope process to a Passage module. A process with no equivalent is a gap for the customer to
   accept or to close with a metadata extension, never a code change (section 8).
3. Record the customer's decisions as their sign-off, not as implementer notes: chart of accounts, number
   ranges, tax codes, units of measure and the fiscal calendar.
4. Name the support rota, the escalation path and the rollback decision owner now. They are go/no-go item 12.

How the legacy operating objects move (PRD 9.50.11):

| Legacy object | In Passage | What the implementer does |
|---|---|---|
| LCS environment inventory (production, sandboxes) | tenant and environment tiers | list them; sandboxes are re-created from masked production through provisioning |
| LCS deployable packages, Basis transports (`STMS`) | GitOps releases | not migrated; list each customer change and map it to a metadata extension |
| Batch groups and job schedules (`BatchJob`, `SM37`) | Temporal schedules | imported in wave 4; verify each schedule's production cadence before cutover |
| Monitoring thresholds (CCMS `RZ20`, LCS alerts) | Prometheus alert rules | map each threshold to an alert category |
| D365 / SAP user licences | licence user types | full = Dynamics full user / SAP professional; operational = activity or functional; self-service = team member or productivity |

Not migrated, and the customer must be told so in writing: Microsoft and SAP SLAs and support contracts
(replaced by Passage support terms) and proprietary performance traces.

## 3. Migration

Master data goes through the migration service (wave 10). Open items and the other transactional packs go
through the migration factory (wave 49). Both are idempotent: a re-run of the same extract loads nothing new.
The factory keys every loaded record in the legacy cross-reference by (system, company, table, legacy id) and
reports a re-run that loaded nothing new as MIG-1505. The codes below are the factory's.

1. **Extract** with the approved method for the source. Keep the raw extract unchanged and hashed; never edit
   it by hand. An extract whose cutoff is after the legacy freeze is refused (MIG-1501).
2. **Map** legacy fields to Passage with the mapping DSL. Every mapping has a reviewer; the Track 4
   assessment is a peer review of one mapping.
3. **Validate**: referential (the party exists in master data), domain (ISO 4217 currency, control account
   present) and uniqueness rules. Failing records are quarantined and listed, never dropped. When the
   quarantined share exceeds `quarantine_max_pct` (default 1%), the load stops (MIG-1504).
4. **Load** in dependency order: master data, then balances, then open transactions. Correct a quarantined
   record and release it (`POST /api/v1/migration/runs/{id}/quarantine/{rec}/release`); it is re-validated,
   loaded and added to the run's control totals.
5. **Reconcile**. Each control total (count, amount per currency, amount per company, account and currency)
   is MATCH, WITHIN_TOLERANCE (a tolerance needs a reason) or MISMATCH. A pack run moves EXTRACTED,
   VALIDATED, LOADED, RECONCILED; it can also end FAILED or ROLLED_BACK. A run reaches RECONCILED only with
   no MISMATCH total. The control-total board on the pack run screen (`/migration-factory`) shows each one.

Go/no-go item 1 needs every pack RECONCILED with 0 MISMATCH.

Opening balances: the customer controller signs the opening trial balance before the first live period
(go/no-go item 3). The signed trial balance is the evidence; the implementer prepares it but does not sign.

## 4. Localization

1. Install the country pack for each legal entity's country (tax, statutory reports, e-invoicing). The
   install check refuses a country mismatch, a second primary pack on one legal entity, a fiscal calendar
   that does not fit, and a retention below the pack minimum. Record the pack versions in the implementation
   record; the service does not store installs yet.
2. Run one statutory fixture per legal entity and keep the run log (go/no-go item 5). A fixture that fails
   blocks cutover for that entity. Localization is verified by the fixture run, not assumed from the version.
3. Configure e-invoicing and bank formats for each entity. Smoke-test the bank, EDI, e-invoicing and carrier
   endpoints in production before cutover (go/no-go item 10).
4. Residency: the tenant's residency region decides where data lives. EU tenants run primary and passive in
   EU regions only (for example `eu-west` and `eu-central`); IN tenants stay in-country; failover never
   crosses a residency boundary (9.50.20).
5. Time zones: maintenance windows, game days and hypercare stand-ups are scheduled in the tenant's time
   zone. SLO reports use UTC.
6. Notice languages: status page and release notes are in English. Customer-facing incident notices are also
   published in German and French; the tenant's contract sets which language it receives.

## 5. Parallel run

1. Run the prior month in both systems on the same cut-off, then start a parallel run for the period and
   source system (`POST /api/v1/migration/parallel-runs`, idempotent by period and system).
2. Every variance is classified: EXPECTED by a rule (RND-001: at most 0.01 per document), EXPLAINED by a
   reviewer's reason, or UNEXPLAINED.
3. A reviewer with migration.factory.review explains each remaining variance. The user who started the run cannot
   explain its variances or sign it off (MIG-001, answered as MIG-0008).
4. Sign-off needs 0 unexplained variances and explained variances of at most 0.1% of the period's revenue
   (MIG-1509). The run is then SIGNED_OFF (go/no-go item 2). The customer controller owns the sign-off; the
   implementer supplies the variance log.
5. Repeat the parallel run when a pack or a mapping changed after the first one.

## 6. Cutover

Generate the runbook with `POST /api/v1/cutover/plans/generate` (migration.cutover.manage), passing the measured
step durations of the last rehearsal. Rehearse it in the sandbox first; lab 4.3 is the mock cutover and lab
4.4 the timed rollback rehearsal. Each step owner starts and completes their step on the runbook screen; the
timer shows elapsed against planned, amber at 110% and red at 125% (CUT-1602). Step durations measured in a
rehearsal are the planned durations of the next plan.

| When | Step (runbook) | Gate and evidence |
|---|---|---|
| T-14 d | freeze legacy master-data changes; final pack dry run | rehearsal results; load test at 50% of target within SLOs (item 6); game day within 30 days (item 7) |
| T-7 d | second rehearsal; sign off the variance report | parallel run SIGNED_OFF (item 2); UAT sign-off for the 12 core processes (item 11) |
| T-3 d | freeze configuration in Passage | PITR drill on production data within 7 days (item 8); pen-test high/critical 0, secrets rotated, images signed (item 9) |
| T-1 d 18:00 (T-6 h) | legacy transaction freeze; final master-data extracts | freeze notice and rehearsed rollback plan (item 14) |
| T-0 to T+12 h | extract and load the transactional packs | packs RECONCILED (item 1) |
| T+24 h | reconcile; go/no-go meeting | dashboards and alerts green for 24 h (item 13); all 14 items done; the CutoverDecisionOwner decides |
| T+30 h | open user access | refused while the runbook's go/no-go steps (reconciliation complete, go/no-go meeting) are open (CUT-1601) |
| T+36 h | abort deadline (`cutover.abort_offset_h`, 12 to 72) | open go/no-go items within the hour before it raise CUT-1604 |

Any open go/no-go item is NO-GO. A waived item needs the CutoverDecisionOwner's signature, and items 1 to 4,
8 and 9 cannot be waived ([go-live checklist](go-live-checklist.md)).

Rollback: on NO-GO, or at the abort deadline with items still open, the migration.cutover.manage holder executes the rollback
with two distinct approvers (CUT-1603). It runs once and follows the rollback plan generated with the
runbook: stop Passage access, restore legacy write access, resume the coexistence adapters, export the
postings since T-0 and re-key them. The Passage postings since T-0 go with the rollback and
come back as re-keying files (`journals.csv` and `documents.csv`) to enter in the legacy system. The platform
rollback target in lab 4.4 is 15 minutes; the data rollback is planned in the cutover plan.

## 7. Hypercare

Hypercare runs from T+1 d to T+30 d: 24x5 on-call with a 15-minute response for Sev-1 and 1 hour for Sev-2,
a daily 09:00 customer stand-up in the tenant's time zone, daily reconciliation of every subledger to the
journal and of the lakehouse metrics to the GL, and weekly reconciliation against any legacy coexistence feed.
Agents stay in suggest mode. The change freeze (`hypercare.change_freeze`) allows Sev-1 fixes only.

Exit needs the first month-end closed and reconciled, 0 Sev-1 for 14 consecutive days, a Sev-2 backlog of at
most 5, SLOs met for 30 days and the customer's sign-off (AC-50.10). The hypercare exit report (9.50.15) records
them. Use the [administrator guide](administrator-guide.md) for operator procedures during hypercare.

## 8. Extensions, not forks

Customer changes go into metadata extensions and configuration. A code change to the platform is not an
implementation option. Release notes map each change that affects a customer to the extension or
configuration it touches ([release notes template](release-notes-template.md)). Releases are zero-downtime
([upgrade strategy](upgrade-strategy.md)), so an extension must keep working across the weekly release.

## 9. Handover checklist

- Reconciliation report and control totals for every pack, each RECONCILED.
- Parallel-run sign-off and the variance log with every explanation.
- Signed opening trial balance (held by the customer).
- Statutory fixture run log per legal entity and the installed country pack versions.
- Tenant inventory: region, tier, residency and release ring.
- Named rota, escalation path and rollback decision owner.
- The go/no-go decision record with its evidence links and approvers.
- The hypercare exit report at the end of hypercare.
