# Platform (platform Go + platform-ts)

## Purpose & Boundaries
Context `platform` (database `passage_platform`; phases 0-1, waves 1-6, 48). Go deployable `platform` (number sequences, audit chain, org structure); TS deployable `platform-ts` (metadata registry, workflow, jobs, policy compiler, admin, SCIM facade, reconciliation-run workflow, notifications). Owns tenants, users, roles/duties/privileges, SoD, entity metadata, number sequences (DEFINITIONS only), audit chain, workflow/job definitions, attachments, legal entities, operating units, sites, warehouses, hierarchies, fiscal calendars, ISO data, UoM, parameters, country packs, privacy requests. `web-bff` sessions are stored in `passage_platform`.

## Interfaces
Prefix `/v1/platform/...` (see also `/bff/...` in web-bff, `/scim/v2/*`). Key groups:
- Tenants: `GET/POST /tenants` (POST = 202, provisions realm, RLS seeds, Kafka principals, Vault namespace, buckets), `/tenants/current`, `/tenants/{id}:suspend|:resume|:offboard` (Svc:`tenant.admin`; offboard = export then crypto-shred).
- Users/service-accounts/devices: `/users` (+`:deactivate|:reactivate`), `/service-accounts` (+`:rotate-secret`), `/devices` (+`:revoke`); SCIM 2.0 `/scim/v2/Users|Groups|ServiceProviderConfig|ResourceTypes|Schemas` (Svc:`scim.provision`).
- Authz: `/privileges`, `/duties`, `PUT /duties/{code}/privileges`, `/roles`, `PUT /roles/{code}/duties` (SoD gate: block high, warn medium/low), `/user-roles` (on SoD conflict `403 sod_conflict` or mitigation workflow, assignment stays inactive), `/user-roles/effective`, `POST /authz:check` (batch), `GET /policy-bundles/current`, `/sod-rules` (+`:evaluate`), `/sod-conflicts`, `/access-certifications` (uncertified roles expire after 30 days).
- Metadata: `/entity-definitions` (+`/{name}:publish` additive-only, emits `entitydefinition.changed.v1`), `/entity-extensions`, `/enum-definitions`, `/form-definitions`, `/saved-views` (+`:share`), `/message-catalog`.
- Numbering/org: `/number-sequences` (+`:stop|:resume`, `/preview`, `:allocate-block` blocks of 100 non-continuous), `/legal-entities`, `/hierarchies`, `/fiscal-calendars/{id}:generate-periods`, `/uom:convert`, `/reference-data/{set}`, `/parameters/{module}`, `/feature-flags`.
- Workflow/tasks/jobs: `/workflow-definitions` (+`:publish`), `/workflow-instances` (+`:cancel|:signal`), `/tasks` (`:claim|:complete|:delegate|:complete-by-token` Sig single-use token), `/notifications` (+`:stream` SSE), `/notification-channels/{kind}`, `/jobs` (+`/errors`, `/events` SSE, `:cancel`, `:retry`), `/batch-schedules` (+`:run-now`).
- Audit/attachments/privacy: `/audit-entries` (+`:export`), `/audit-chain:verify` (202), `/audit-chain/head`, `/attachments` (<=25 MiB, ClamAV scan before visible, pre-signed PUT), `/privacy-requests` (30-day SLA, `:fulfill`), `/country-packs`, `/electronic-reporting-formats`, `/compliance/controls`, `POST /graphql` (read-only, depth <=8, cost-limited), `GET /search?q=`.
- Reconciliation: `reconciliation-run` Temporal workflow in platform-ts calls each context's `/reconciliation/{check}`.

## Data Ownership
Tables (EN-001..012): tenant (id=tenant id, slug, region, tier starter|professional|enterprise, keycloak_realm, vault_namespace), app_user (principal_type human|service|agent), privilege (PK code), duty/role/duty_privilege/role_duty/sod_rule (PK (tenant_id, code); shipped rows use nil tenant `00000000-0000-0000-0000-000000000000`), user_role (surrogate id; UQ with COALESCE(legal_entity_id, nil-uuid)), entity_definition/field/enum/extension, number_sequence (scope tenant|legal_entity|legal_entity_fy|site; mode continuous|non_continuous|external), audit_entry (partitioned monthly on occurred_on; chain_seq gap-free per tenant; hash = sha256(prev_hash || canonical_json || sha256(pii_payload)); retention_class fiscal_7y|standard_3y|agent_7y), audit_chain_head, legal_entity, operating_unit/hierarchy/hierarchy_node (GiST exclusion), site/warehouse, fiscal_calendar/period, uom/uom_conversion, iso_currency/country, workflow_definition, attachment (size_bytes <= 26214400), job (job_type import|export|batch|migration|projection_rebuild|partition_maintenance|retention; status queued|running|succeeded|failed|cancelled).

## Events Produced
`platform.entitydefinition.changed.v1` (+snapshot), `platform.user.provisioned.v1`, `platform.workflowtask.completed.v1`, `platform.legalentity.created.v1` (+snapshot), `platform.numbersequence.changed.v1` (+snapshot), `ledger.reconciliation.completed.v1` (emitted by platform-ts workflow `reconciliation-run`, source `/passage/ledger/reconciliation`).

## Events Consumed
`platform-codegen`/`platform-shell-bff` (entitydefinition.changed), `platform-audit-events` (user, journalentry, agents.*), `platform-numbersequence` (legalentity.created -> default sequences), `platform-notifications` (workflowtask.completed, intercompanypair.mismatched, inventory.transaction.rejected, agents.approval.requested, agents.budget.threshold, ledger.reconciliation.completed), `platform-valuation-rejections` (creates workflowtask type `valuation_rejection`, assignee cost accountant, SLA 1 business day; completes on cancelled / posted with matching reservationId). Audit consumer reads Debezium CDC + `<ctx>.audit_event`; remains SINGLE writer of audit_entry chain_seq and hash.

## Dependencies
internal: platform-kernel, edge-identity, eventing-backbone, ledger (period/posting refs). external: Keycloak Admin API, Temporal, Vault, S3-compatible object storage (object-lock anchors), ClamAV, Kafka.

## Gotchas & Constraints
- Gap-free counters do NOT live here: each posting context keeps `<ctx>.number_sequence_counter`; platform owns definitions and propagates via `numbersequence.changed`. Non-continuous uses block allocation.
- Period control belongs to ledger, calendars to platform (Q1 settled).
- P3 audit: `*_secure` excluded from CDC; P3 changes/reveals/exports audited via `<ctx>.audit_event` with encrypted `pii_payload` (per-subject key); erasure = crypto-shred key, chain still verifies.
- Metadata additive-only; extension fields stored in `ext jsonb`, validated at runtime against published EntityDefinition version.
- Job statuses seen by API: queued|running|succeeded|failed|cancelled|partial. Long ops return 202 `{jobId, statusUrl: /v1/platform/jobs/{id}, estimatedCompletionSeconds}` + Location.
- Policy bundle compiles roles, duties, privileges, grants, SoD rules, agent allow-lists/kill switches, posting-source grants and route->privilege table (from `x-passage-duty`).
- Security detections job over audit_entry emits `passage_actor_anomaly_total` and `passage_cross_le_access_total`.
- Temporal history shard count 512 fixed at install.

## Source Sections
- Backend 3.2 inventory row (283-285), 5.5 (422-570), 7.1-7.2, 7.10 (1237-1241), 7.16 (1319-1322), 7.18 (1328-1337)
- Data EN-001..012 (1915-2119), 6.1 audit (2017-2053); Events 3.2 (3984-3992), consumer rows (4257-4261, 4300); Backend 5.20 reconciliation (1004)
