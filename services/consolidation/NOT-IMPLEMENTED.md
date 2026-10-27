# NOT-IMPLEMENTED: consolidation (PSG-40, wave 40)

Slice 1 is stateless: pure consolidation rules, the elimination and
translation previews, and the web workspace for both.
Every deliverable and acceptance criterion not listed as built in the README
is below, with its status and reason.

| Item | Status | Reason |
|---|---|---|
| Persistence: `consolidation_group`, `consolidation_unit`, `consolidation_run`, `consolidation_entry`, `investment`, `group_journal`, `consolidation_task`, `validation_rule`, `reclassification_rule`, `number_sequence_counter`, migrations, RLS, `idempotency_key`, `outbox` | later slice of PSG-40 | store slice; needs the kernel `dbx` tenant GUC change (`passage.tenant_id`) before RLS can be tested |
| FR-40.7 Group structure: `POST /api/v1/consol/groups`, units, effective-dated ownership history, chart mapping (main account → consolidation account) | later slice of PSG-40 | needs persistence and `Idempotency-Key`. Ownership percentage rules are built (`ownership.go`) |
| FR-40.8 Collection of unit trial balances from the wave-11 projection and translation over a run | later slice of PSG-40 | needs wave-11 balance projection and wave-12 rates. Translation and CTA arithmetic is built (`translate.go`) |
| FR-40.9 E6 dividends within the group | later slice of PSG-40 | not in the E1–E5 worked example; the rule is not built |
| FR-40.9 E3 per-layer unrealized profit (FR-40.16) | later slice of PSG-40 | depends on wave-39 group costing. E3 is built on a single period-level margin input (`UnsoldShare`) |
| FR-40.9 ConsolidationEntry rows with `source_refs` linking to unit balances | later slice of PSG-40 | needs persistence; the preview returns entries without source references |
| FR-40.10 Versioned consolidation run, partial recompute, rerun idempotency (BR-40.10), `POST /api/v1/consol/runs`, status machine | later slice of PSG-40 | needs persistence and runs |
| FR-40.10 Performance run 50 units × 10k accounts < 5 min | later slice of PSG-40 | needs runs and the collection path |
| FR-40.11 Consolidated trial balance and drill (`GET /api/v1/consol/runs/{id}/trial-balance`, `/entries?rule=`) | later slice of PSG-40 | needs runs |
| FR-40.12 / FR-40.6 gate: run rejects unresolved IC mismatches (CNS-0901) | later slice of PSG-40 | needs the intercompany reconciliation service; this gate is a handoff row below |
| FR-40.17 Reclassification and regrouping rules (entity and group level), two-sided IC elimination with threshold (BR-40.15, CNS-0905) | later slice of PSG-40 | rule engine and runs |
| FR-40.18 Investments, CoI activities, equity method, divestiture (CNS-0910), `POST /api/v1/consol/investments` | later slice of PSG-40 | needs persistence. The E4 first-consolidation arithmetic is built; the equity-method and divestiture arithmetic is not |
| FR-40.19 Top-side group journals, approval threshold, carry-forward (`POST /api/v1/consol/group-journals`, CNS-0909) | later slice of PSG-40 | needs persistence and approval workflow (wave 17) |
| FR-40.20 Consolidation monitor (`GET /api/v1/consol/monitor`), task dependencies, unit release lock (`POST /api/v1/consol/units/{id}/release`, CNS-0906, CNS-0907) | later slice of PSG-40 | needs persistence and the close checklist (wave 14) |
| FR-40.20 validation rules as expressions over FS items (balance, IC partner present, retained earnings roll) | later slice of PSG-40 | needs the consolidated trial balance |
| FR-40.8 imports `POST /api/v1/consol/imports` (external unit TB, file hash) | later slice of PSG-40 | needs persistence |
| BR-40.8 pro-rata ownership change within a period | later slice of PSG-40 | needs effective-dated ownership history |
| BR-40.9 per-rule rounding line | partial | entries are rounded to the group currency minor unit per line, and each rule balances by construction because its legs reuse the rounded values; the per-rule rounding line that keeps each rule balanced after rounding is not built |
| Message catalog CNS-0902 (missing rate), CNS-0908 (group cost unknown fallback) | later slice of PSG-40 | rate loading and FR-40.16 not built. The preview rejects a zero closing rate with COM-0001 |
| CNS-0920 bargain purchase | not in PRD catalog | raised by the E4 domain rule; listed for the catalog owner. A proposal for PRD 7.40.18 is an open item |
| AC-40.6 CTA worked example (38,000.00 USD) | partial | built and tested in the domain and through `POST /v1/consol/translations:preview`; translation inside a collected, versioned run is later |
| AC-40.8 3-entity, 2-currency demo group consolidated to the cent | later slice of PSG-40 | needs the group fixture `consol-group-3` from the accountant workbook (testdata not built), runs and statements |
| AC-40.9 drill from consolidated line to unit, E1 entry, journal line within 3 clicks | later slice of PSG-40 | needs runs and the wave-15 designer |
| AC-40.12 unrealized profit per receipt layer (800.00) | later slice of PSG-40 | depends on FR-40.16 |
| AC-40.13 vendor debit balance regrouped to other receivables and reversed next period | later slice of PSG-40 | depends on FR-40.17 and the wave-11 journal |
| AC-40.14 equity method carrying amount 340,000.00 | later slice of PSG-40 | equity method not built |
| AC-40.15 unit balance check fails at 12.00 and group translation Blocked | later slice of PSG-40 | needs the monitor |
| Events `dev.passage.consol.run.completed.v1`, `unit.released.v1`, `validation.failed.v1` | later slice of PSG-40 | published with runs and the monitor; `events/asyncapi.yaml` has no channels yet |
| Inquiries: consolidated TB, elimination detail, monitor KPI, CTA and NCI roll-forward, investment holdings | later slice of PSG-40 | needs persistence and runs |
| Printed statements (wave-15 designer, iXBRL / ESEF) | later slice of PSG-40 | depends on wave 15 designer |
| Hyperinflation (IAS 29), proportional consolidation | out of scope | PRD 7.40.20 and 7.40.21 |
| Migration from Group Reporting / ECCS (7.40.11) | later slice of PSG-40 | needs the migration framework |
| Helm: `DATABASE_URL` from secret and a `/readyz` that checks the database | later slice of PSG-40 | the service has no database yet; the chart already has startup, liveness and readiness probes on `/healthz` and `/readyz`, the admin port and an optional ServiceMonitor for `/metrics` |
| Parameter `consol.maxUnits` / env `CONSOL_MAX_UNITS` | later slice of PSG-40 | no units in slice 1 |
| Feature flag `consol.external-import` | later slice of PSG-40 | imports not built |
| Handoff: IC reconciliation gate (CNS-0901) reads `ic_reconciliation_item` from the intercompany service | handoff | owned by intercompany (this wave) and the store slice; cross-service read path to be decided by the eventing-backbone owner |
| Handoff: `ledger` read of consolidated balances and wave-11 trial balance projection | handoff | owned by ledger (wave 11) and the platform kernel; contract to be agreed before FR-40.8 |
| Handoff: wave-15 designer drill integration (`consolidated` badge, drill menu) | handoff | owned by reporting (wave 15) |
| Workspaces: consolidation workspace with group definition, run start, validation and statements; elimination entries filtered by rule and unit with drill to source | later slice of PSG-40 | the web module has the workspace landing page, the elimination preview and the translation preview; stored groups, runs and drill need persistence |
| BR-40.6 historical rate per equity movement date | later slice of PSG-40 | translation takes one opening rate and E4 one historical rate; equity movements by date need the collected balances |
