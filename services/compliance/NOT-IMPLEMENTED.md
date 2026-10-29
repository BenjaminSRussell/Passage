# Not implemented in PSG-48 (after slice 2)

Slice 1 ships segregation of duties: the engine (detection, simulation, conflict state machine), the
conflict store with the mitigation approval record, the evaluation, listing, mitigation and
risk-acceptance endpoints with outbox events, the chart, and the compliance workspace screens (SoD
conflicts, conflict detail with mitigation and risk acceptance, role assignment check, SoD rules,
country packs). Slice 2 adds pure, tested rules for breach clocks (FR-48.29), electronic signatures
(FR-48.13) and the DUPLICATE audit rule (FR-48.14), the read-only `POST /v1/security/incidents:breach-clocks`
endpoint, and the breach clock and India TDS calculator screens. Everything else in the ticket is a later
slice of PSG-48 unless marked handoff.

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC2: 50 planted conflicts detected; mitigation records approvals | built | domain fixture test, store reconciliation test and the `sod_mitigation` record |
| AC-48.3: an OPEN HIGH conflict blocks the save | partial | `POST /v1/sod/simulate` answers `blocked` with `SOD-1101`; refusing the save itself is the wave-2 role-assignment service's job, which calls simulate before it saves |
| AC1: statutory fixtures per pack | later slice of PSG-48 | owned by `services/localization` and `countrypacks/`; see that service's NOT-IMPLEMENTED |
| AC3: backup and restore drill (RPO 5 min, RTO 1 h) | handoff | CNPG backup configuration and the drill job live in `deploy/` and the ops platform (wave 1 / wave 50) |
| AC4: external pen-test high/critical = 0 | handoff | a wave-exit activity owned by the security lead; not a code deliverable |
| Deliverable 3 / FR-48.4: nightly SoD evaluation at 00:30 (`sod.nightly_time`) | later slice of PSG-48 | `POST /v1/sod/evaluations` is the per-user evaluation; the nightly job needs the wave-2 role assignment store to enumerate users |
| BR-48.2: effective duties from inherited roles and temporary delegations; refuse a delegation creating a HIGH conflict | later slice of PSG-48 | roles carry their duties inline in the request until the wave-2 role catalogue and delegations can be read |
| FR-48.4: 50-rule seed set and tenant-maintained rules (`sod_rule`), GRC ruleset import | partial | the 11 rule ids the PRD lists are seeded in code; the other 39, the `sod_rule` table and the import come later |
| FR-48.4: privilege-level rules | later slice of PSG-48 | rules relate duties only |
| FR-48.5: `sod.block_high` and `sod.max_mitigation_days` per tenant | partial | read once per process from `SOD_BLOCK_HIGH` and `SOD_MAX_MITIGATION_DAYS`; per-tenant settings need the tenant settings store |
| PRD §9.48.6: bulk mitigation of up to 25 conflicts with one approver; conflict export | later slice of PSG-48 | one conflict per call in this slice |
| PRD §9.48.10: alert "HIGH conflict unmitigated > 7 days"; SoD evaluation SLO metric | later slice of PSG-48 | the conflict age is exposed per row; the alert rule ships with the deploy monitoring work |
| FR-48.6: access certification campaigns (`POST /v1/sod/campaigns`, BR-48.9) | later slice of PSG-48 | needs the manager hierarchy and the auto-revoke job |
| FR-48.7 to FR-48.9: SOC 2 control library, evidence collectors, security hardening, residency (BR-48.7, BR-48.8) | later slice of PSG-48 | evidence store and collectors (FR-48.30) come after this slice |
| FR-48.10, FR-48.11: subject requests, retention policies, legal holds (BR-48.5, BR-48.6) | later slice of PSG-48 | needs PII classification (wave 42) and Temporal fan-out |
| FR-48.12: database log configuration | later slice of PSG-48 | depends on wave 5 CDC audit |
| FR-48.13: `esign_requirement`, `esign_record` and `esign_reason` store, `GET/PUT /v1/compliance/esign/requirements`, `POST /v1/compliance/esign/sign` and `/{id}/revoke`, signature log, `dev.passage.compliance.esign.signed.v1`, optional certificate signing, BR-48.11 single transaction with the guarded action, BR-48.13 printout block | later slice of PSG-48 | slice 2 built the rules only (`internal/domain/esign.go`: record version hash, `ESG-0602`, BR-48.12 component count, `ESG-0603` lockout); the store, Keycloak step-up `acr=esign` and the lockout counter's persistence come next |
| AC-48.11, AC-48.12: release with password plus TOTP; the lockout logs a security event | partial | the lockout rule and the 30-minute window are tested; the dialog, the stored counter and the security event need the e-signature store and step-up flow |
| FR-48.14: `audit_policy`, runs (`POST /v1/compliance/audit-policies/{id}/runs`), `audit_violation` persistence, FR-3.13 decision-table binding, org scope and effective dates; rule types KEYWORD, LIST_SEARCH, SAMPLING, THRESHOLD, WEEKEND_HOLIDAY, AGGREGATE; `AUD-0701` | later slice of PSG-48 | slice 2 built the DUPLICATE rule (BR-48.14), one violation per later document, the violation key and per-vendor grouping as pure functions; documents come from waves 11, 17, 19 and 43 |
| AC-48.13 end to end: a stored run creates 12 violations, a rerun none, one case per vendor | partial | proven on the pure rule with the 1,000-invoice planted fixture; the stored run and cases follow the store |
| FR-48.15: `audit_case`, statuses, resolution codes, archive (`POST /v1/compliance/audit-cases`) | later slice of PSG-48 | needs the case store and the archive tier (FR-46.18) |
| FR-48.16: process, risk and control library | later slice of PSG-48 | needs store and wave-5 document store |
| FR-48.3, FR-48.17: ER designer, mandates registry, electronic messaging lifecycle (BR-48.3, BR-48.17) | later slice of PSG-48 | `er_format`, `mandate` and `e_message` belong with the ereporting work (the PRD's `passage-ereporting`) |
| FR-48.18: XBRL and iXBRL | later slice of PSG-48 | Arelle job in Python, taxonomy registry |
| FR-48.19: sanctions screening (BR-48.15) | later slice of PSG-48 | screening lists refresh and matcher |
| FR-48.20, FR-48.21: export control, origin determination (BR-48.16) | later slice of PSG-48 | depends on waves 21, 34 and 35 |
| FR-48.22, FR-48.31: US workforce compliance and statutory outputs (BR-48.20) | handoff | the HR records and fields framework is wave 42; outputs follow those fields |
| FR-48.23: break-glass elevated sessions (BR-48.19) | later slice of PSG-48 | needs the Keycloak `elev` claim flow |
| FR-48.24, FR-48.25: export governance and read-access logging | later slice of PSG-48 | needs the API layer hooks and PII classification |
| FR-48.26: masked tenant clone | later slice of PSG-48 | clone pipeline; used by waves 49 and 50 |
| FR-48.27, FR-48.28: India pack depth and HR legal content | later slice of PSG-48 / handoff | the TDS calculation is in `services/localization` (see its NOT-IMPLEMENTED); the rest of the IN pack is a later slice; HR fields belong to wave 42 |
| FR-48.29: `security_incident` store, `POST /v1/security/incidents` with `Idempotency-Key`, timeline, containment and post-incident review, notification tasks and templates, record of what was sent, `INC-1301` deadline monitor, `dev.passage.security.incident.breach_confirmed.v1`, per-state US table, `incident.breach_regimes` tenant setting | later slice of PSG-48 | slice 2 built the clocks (`internal/domain/breach.go`) and the read-only `POST /v1/security/incidents:breach-clocks`; the caller names the regimes and the US state window |
| AC-48.19: saving the incident creates the GDPR deadline and a notification task | partial | the 2026-11-05T09:00Z deadline is computed and tested; saving and the task need the incident store |
| BR-48.18: deadlines displayed in the tenant time zone | later slice of PSG-48 | the breach clock screen shows UTC; the tenant time zone comes with the tenant settings store |
| FR-48.30: evidence store with object lock and hash chain | later slice of PSG-48 | needs the object store and bucket policy from `deploy/` |
| Web: privacy, signatures, signature log, audit policies and cases, screening, break-glass and incident record screens | later slice of PSG-48 | slice 1 screens cover SoD and country packs; slice 2 adds the breach clock calculator and the India TDS calculator; e-signature and audit screens wait for their endpoints |
| JWT validation and OPA duties | wave 2 | the gateway validates tokens; the service trusts tenant headers and the local `authz.DutyPolicy` |
| `serviceCode` on problem bodies | kernel handoff | `httpx.Problem` in `platform/go` has no `serviceCode`; the OpenAPI documents the body as served |
| Readiness with migrations current and OPA bundle | handoff | the kernel `/readyz` contract checks OPA; the slice only pings Postgres |
