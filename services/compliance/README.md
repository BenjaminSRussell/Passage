# compliance (PSG-48, wave 48, slices 1 to 5)

Compliance controls for Passage. Slice 1 ships segregation of duties (SoD): the rules engine, pre-save
simulation, evaluation of a user's effective roles with stored conflicts, and the mitigation and
risk-acceptance workflow, plus the compliance workspace screens in `apps/web/src/modules/compliance`.
Slice 2 adds the rules of three later features as pure, tested functions (breach notification clocks,
electronic signatures, the DUPLICATE audit rule), one read-only endpoint for the breach clocks and the
breach clock and India TDS calculator screens. Slice 3 completes the audit rule kernel (all seven rule
types), adds audit case grouping and the case status machine, and break-glass session planning, each
behind a stateless endpoint, with the audit policy run, audit case and break-glass screens. Slice 4
adds denied-party and sanctions screening with stored hits and the officer decision, and the export
license determination of a delivery, with the screening and export check screens. Slice 5 adds stored
GDPR and CCPA subject requests with their state machine, the erasure preview and the retention purge
check, and four stateless rule endpoints: certification campaign planning and close, the incident
lifecycle step, export governance and read-log classification, with the subject request, certification
campaign, incident step and export policy screens.

## What it does now

- `internal/domain/sod.go` (pure): rule schema validation, the 11 seed rules of FR-48.4, `Detect`
  (duty patterns such as `*Post*`, distinct duties, retired rules ignored), `Simulate` and the BR-48.1
  conflict state machine: `Mitigate` (approver is not the conflicted user, reason and control required,
  expiry after now and within min(rule cap, tenant cap)), `Expire`, `Resolve`, `AcceptRisk` (OPEN LOW
  and MEDIUM only, with a reason).
- `internal/store` (Postgres, RLS through `dbx.WithTenantTx`): `sod_conflict` and the append-only
  `sod_mitigation` approval record. Every read and write first reopens mitigations whose expiry has
  passed (MITIGATED → OPEN). Events go to the outbox in the same transaction.
- HTTP (all under the gateway's `/api` prefix):

| Method and path | Duty | What |
|---|---|---|
| `GET /v1/sod/rules` | SoDReview | the seed rules |
| `POST /v1/sod/simulate` | SecurityAdmin | conflicts an assignment would add; `blocked` with `SOD-1101` when the resulting roles hold a HIGH conflict the service has not recorded as MITIGATED (new or existing, AC-48.3) and `sod.block_high` is on |
| `POST /v1/sod/evaluations` | SecurityAdmin | records the outcome for a user's effective roles: new conflicts OPEN (`sod.conflict.detected`), RESOLVED ones detected again reopened, conflicts no longer detected RESOLVED; repeating it changes nothing |
| `GET /v1/sod/conflicts` | SoDReview | filter by state, severity, user, rule; keyset paging with `cursor` |
| `GET /v1/sod/conflicts/{id}` | SoDReview | conflict with its mitigation history; `ETag` is the version |
| `POST /v1/sod/conflicts/{id}/mitigations` | SoDApprove | `If-Match` and `Idempotency-Key` required; the caller is the approver; emits `sod.mitigation.approved` |
| `POST /v1/sod/conflicts/{id}/accept-risk` | SoDAdmin | `If-Match` required; LOW and MEDIUM only |
| `POST /v1/security/incidents:breach-clocks` | SecurityIncidentManage | slice 2, read-only: the notification clocks of a confirmed personal-data breach with their status at server time |
| `POST /v1/compliance/audit-policies:evaluate` | AuditPolicyRun | slice 3, read-only: runs one audit rule over the document window in the body; findings keyed on (policy, version, document); `code: AUD-0701` when any document violates |
| `POST /v1/compliance/audit-cases:group` | AuditCaseManage | slice 3, read-only: case drafts from violations (one per vendor for DUPLICATE, one per policy otherwise) |
| `POST /v1/compliance/audit-cases:transition` | AuditCaseManage | slice 3, read-only: one action of the case status machine, judged at server time with `audit.case_archive_days` |
| `POST /v1/security/elevated-sessions:plan` | BreakGlassRequest | slice 3, read-only: the break-glass session the calling user's request would open; no session is stored and no token issued |
| `POST /v1/trade/screening/check` | TradeComplianceView | slice 4: screens a party or document against the list entries in the body at `screening.threshold`; new hits are stored OPEN and emit `trade.screening.hit`; answers the subject's blocking hits (OPEN and CONFIRMED) and `blocked` |
| `GET /v1/trade/screening/hits` | TradeComplianceView | slice 4: filter by state, list, subject type and subject; keyset paging with `cursor` |
| `GET /v1/trade/screening/hits/{id}` | TradeComplianceView | slice 4: one hit; `ETag` is the version |
| `POST /v1/trade/screening/hits/{id}/decision` | TradeComplianceOfficer | slice 4: `If-Match` and `Idempotency-Key` required; `CLEAR_FALSE_POSITIVE` or `CONFIRM` with a reason; the caller is the decider |
| `POST /v1/trade/export-checks` | TradeComplianceView | slice 4, read-only: license determination per delivery line; `officerReleaseLicenseRef` (embargo release) needs TradeComplianceOfficer |
| `POST /v1/privacy/requests` | PrivacyIntake | slice 5: `Idempotency-Key` required; stores a subject request in RECEIVED with its deadline (GDPR 30, CCPA 45 days from `receivedAt`, shorter override only) |
| `GET /v1/privacy/requests` | PrivacyOfficer | slice 5: filter by state and type, nearest deadline first; keyset paging with `cursor` |
| `GET /v1/privacy/requests/{id}` | PrivacyOfficer | slice 5: one request; `ETag` is the version |
| `POST /v1/privacy/requests/{id}/actions` | PrivacyOfficer | slice 5: `If-Match` required; VERIFY, DISCOVER, RELEASE_HOLD, APPROVE, DELIVER, CLOSE or REJECT on the stored state; CLOSE emits `privacy.request.completed` |
| `POST /v1/privacy/requests/{id}/erasure-preview` | PrivacyOfficer | slice 5, read-only: the BR-48.5 pseudonymization of one record of the subject, refused under a legal hold |
| `POST /v1/privacy/retention:purge-check` | PrivacyOfficer | slice 5, read-only: BR-48.6 skip reasons for one record |
| `POST /v1/privacy/holds` | `compliance.legal_hold.place` | slice 6: `Idempotency-Key` required; places a legal hold on a subject, a data class, a record or a combination in one legal entity of the caller's scope; emits `privacy.hold.placed` |
| `GET /v1/privacy/holds`, `GET /v1/privacy/holds/{id}` | `compliance.legal_hold.read` | slice 6: filter by state, legal entity, subject, data class; keyset paging; `ETag` is the version |
| `POST /v1/privacy/holds/{id}/release` | `compliance.legal_hold.release` | slice 6: `If-Match` and a reason of 10+ characters; emits `privacy.hold.released`; the row is kept |
| `GET /v1/privacy/holds:check` | `compliance.legal_hold.check` (service only) | slice 6: the hold check participants call, see "Legal holds" below |
| `POST /v1/privacy/erasures` | `compliance.erasure.request` | slice 6: `Idempotency-Key` required; 422 `PRV-1201` and no event when a hold covers the subject, otherwise stores the command and emits `privacy.erasure.requested` |
| `GET /v1/privacy/retention-policies` | `compliance.retention.read` | slice 6: the default retention per data class |
| `POST /v1/sod/campaigns:plan` | SoDAdmin | slice 5, read-only: campaign close instant (14 days default, 3..60) |
| `POST /v1/sod/campaigns:close` | SoDAdmin | slice 5, read-only: progress, and after `closesAt` the BR-48.9 revocations and flags |
| `POST /v1/security/incidents:transition` | SecurityIncidentManage | slice 5, read-only: one incident lifecycle step with the breach clocks and closure rule |
| `POST /v1/compliance/export-governance:evaluate` | ComplianceView | slice 5, read-only: the export policy decision for one download |
| `POST /v1/compliance/read-log:classify` | ComplianceView | slice 5, read-only: which fields of a read the read-log policy covers |

- Events (`events/asyncapi.yaml`): `dev.passage.sod.conflict.detected.v1`, `dev.passage.sod.mitigation.approved.v1`,
  `dev.passage.trade.screening.hit.v1` (slice 4, keyed by hit id, written with the hit),
  `dev.passage.privacy.request.completed.v1` (slice 5, keyed by request id, written with the close),
  `dev.passage.privacy.hold.placed.v1` and `.released.v1` (slice 6, keyed by hold id; the reason is not in the event),
  `dev.passage.privacy.erasure.requested.v1` (slice 6, keyed by erasure id, topic `passage.privacy.erasure`). The
  slice 2 and 3 endpoints, the export check and the slice 5 rule endpoints store nothing and emit nothing.
- `GET /readyz` pings Postgres; admin `/metrics` counts simulations and blocked simulations.

Slice 2 domain rules (pure; no store yet, see `NOT-IMPLEMENTED.md`):

- `internal/domain/breach.go` (FR-48.29, BR-48.18): clocks start at `awareAt`, deadlines in UTC. GDPR
  Art. 33 authority 72 h and Art. 34 subjects without undue delay (no fixed hour), UK ICO 72 h, US state
  30 to 60 days (default 30), PIPEDA and LFPDPPP without a fixed hour, CERT-In 6 h. Status `NOT_DUE`,
  `AT_RISK` (last 12 h), `OVERDUE`, `NO_FIXED_DEADLINE`. `ValidateClosure` refuses closing a missed
  deadline without a late-notification justification of at least 10 characters (`INC-1302`).
- `internal/domain/esign.go` (FR-48.13, BR-48.11, BR-48.12): SHA-256 record version hash of the canonical
  JSON, `ESG-0602` on a changed record, two components at the first signing and one within the reuse
  window (`esign.session_reuse_min`, default 15), `ESG-0603` lockout after three failures for 30 minutes.
- `internal/domain/audit_duplicate.go` (FR-48.14, BR-48.14): the DUPLICATE rule (vendor, currency, amount
  within max(0.01, 0.5%), dates within 30 days, normalized numbers equal or one edit apart, posted credit
  notes excluded), one violation per later document, `ViolationKey` on (policy, version, document) and
  one case group per vendor. `TestFindDuplicates_PlantedFixture` covers AC-48.13 on 1,000 invoices with
  12 planted duplicates, including that a rerun adds no keys.

Slice 3 domain rules (pure):

- `internal/domain/audit_rules.go` (FR-48.14): `EvaluateAudit` runs DUPLICATE, KEYWORD (whole words,
  case and punctuation folded), LIST_SEARCH (normalized watch list against the party), THRESHOLD
  (positive limit per category, same currency; evidence carries the share of the limit), WEEKEND_HOLIDAY
  (Saturday, Sunday or a listed YYYY-MM-DD holiday, UTC day), AGGREGATE (sum per party per calendar month
  above the limit) and SAMPLING (seeded percent or every k-th; the same seed selects the same documents
  whatever the input order). Findings are sorted by document and keyed by `ViolationKey`.
  `TestEvaluateAudit_DuplicatePlantedFixture` runs AC-48.13 through the kernel and the case grouping.
- `internal/domain/audit_case.go` (FR-48.15): `ApplyCaseAction` walks OPEN → IN_REVIEW →
  RESOLVED_NO_ISSUE or RESOLVED_ISSUE (resolution code of the matching kind; recovery amount, never
  negative, with a currency) → CLOSED (needs a resolution code) → ARCHIVED (only `audit.case_archive_days`,
  30..3650, after closing). `GroupViolations` builds one draft per vendor for DUPLICATE and one per policy
  otherwise, skipping violations already in a case.
- `internal/domain/breakglass.go` (FR-48.23, BR-48.19): `PlanElevatedSession` checks the reason code and
  ticket, trims and de-duplicates roles, caps the length at `breakglass.max_minutes` (default 60, at most
  240), refuses a protected role (SecurityAdmin, PlatformAdmin) for yourself (`SEC-0707`), routes a
  protected role for another user to a second PlatformAdmin, and otherwise follows
  `breakglass.self_activate` (BreakGlassApprove approval, or self-activation with mandatory post-review).
  The review is due `breakglass.review_days` business days after the end; `ReviewOverdue` is the SEC-0708
  check.

Simulation reads the user's recorded conflict states; a caller cannot claim a mitigation in the request
(the draft's `mitigatedRuleIds` field is gone and an unknown field is a 400). The break-glass requester is
the calling user (`X-Passage-User`), and the tenant settings, not the request, decide the length cap and
self-activation.

Slice 4 (FR-48.19, FR-48.20):

- `internal/domain/screening.go`: names and addresses are lower-cased, Latin accents and Cyrillic
  transliterated and punctuation dropped; names compare by token-set ratio (best of primary name and
  aliases; an empty side scores 0), the address weighs 20% when both sides have one, and a country
  mismatch scales the score by 0.9. Hits are at or above `screening.threshold` (0.88, 0.70..0.99).
  `DecideHit` takes an OPEN hit to FALSE_POSITIVE or CONFIRMED with a reason and a decider.
- `internal/store/screening.go` and `0004_screening.sql`: `screening_hit` with RLS. One OPEN or
  CONFIRMED hit per subject and list entry (partial unique index), so screening again refreshes an OPEN
  hit instead of adding one. A FALSE_POSITIVE hit is the BR-48.15 whitelist: it holds the list entry
  version and the fingerprint of the subject's normalized name, address and country, and the entry is
  not flagged again until one of them changes. Decisions use `If-Match` and `Idempotency-Key`.
- `internal/domain/export_control.go`: BR-48.16 comprehensive embargo (CU, IR, KP, SY and the regions
  UA-43 Crimea, UA-40 Sevastopol, UA-14 Donetsk, UA-09 Luhansk) blocks every line with `TRD-1002`, even
  with license checks off, unless a TradeComplianceOfficer releases it with a license reference. Otherwise
  a line that the decision table says needs a license must find one for its regime, classification and
  destination, unexpired at server time, with quantity and value left after the earlier lines of the same
  delivery; if none, `TRD-1003` (blocked under `trade.license_check` BLOCK, a warning under WARN). The
  table (`DefaultLicenseTable`) is illustrative until the packs ship theirs. Quantities and values are
  decimals, never floats.

The threshold, the license check mode, the decision table and the clock are the server's: the request
bodies have no fields for them, and an unknown field is a 400.

The slice 3 screens are in `apps/web/src/modules/compliance`: `/compliance/audit/run` (run an audit
rule), `/compliance/audit/cases` (group violations into cases and check a case action) and
`/compliance/break-glass` (plan a break-glass session). Slice 4 adds `/compliance/screening/hits` (the
screening workbench, filter by state and list), `/compliance/screening/hits/:id` (compare the subject
with the list entry, clear or confirm), `/compliance/screening/check` (screen a party or document) and
`/compliance/export-checks` (license determination for a delivery).

Slice 5 (FR-48.6, FR-48.10, FR-48.11, FR-48.24, FR-48.25, FR-48.29):

- `internal/domain/privacy.go`: `PlanPrivacyRequest` (deadline, days left rounded up, due soon at 5
  days or fewer), `ApplyPrivacyTransition` (VERIFY needs two distinct matched attributes and the e-mail
  link; an ERASURE with holds goes to HELD at DISCOVER and cannot leave it or be approved while a hold
  stands, AC-48.7; a close after the deadline needs a 10-character justification and is marked late;
  REJECT needs a reason), `PseudonymizeSubject` (BR-48.5: the seven PII fields become a deterministic
  `ERASED-` token (HMAC-SHA256 under a per-tenant key, 128 bits) per tenant, subject and field, everything else is kept and `erased` is set) and `PurgeCheck`
  (BR-48.6).
- `internal/store/privacy.go` and `0005_privacy.sql`: `subject_request` with RLS; intake under
  `Idempotency-Key`, actions under the row lock and `If-Match`, the stored type, state, holds and
  deadline (never the caller's), the approving officer, and the completed event in the close transaction.
- `internal/domain/campaign.go` (BR-48.9): before `closesAt` only progress; after it explicit REVOKE is
  revoked, PENDING is revoked under `autoRevoke` and flagged otherwise, and the manager counts as
  overdue. A manager deciding their own roles is `SOD-1121`.
- `internal/domain/incident.go` (FR-48.29, BR-48.18): TRIAGE → CONTAINED → ERADICATED → RECOVERED →
  CLOSED one step at a time; Sev-1 pages on-call; breach clocks from `awareAt`; a clock notified by its
  deadline (`notifiedAt` per regime) is met, and closing needs a justification for every clock sent late
  or still unsent past its deadline.
- `internal/domain/governance.go` (FR-48.24, FR-48.25): `DecideExport` (PRD default actions, server-side overrides only: the request body cannot carry a policy or read rules,
  DENY, REASON, APPROVAL and the 10,000-row bulk hold, audit always) and `ClassifyRead` (default
  read-logged fields, exact and `*.name` rules, 90..3650 day retention, names only).

Slice 5 screens: `/compliance/privacy/requests` (list by deadline, days left red at 5 or fewer),
`/compliance/privacy/requests/new` (intake), `/compliance/privacy/requests/:id` (the next action, the
erasure preview), `/compliance/campaigns` (plan and close check), `/compliance/incidents/step`
(incident lifecycle check) and `/compliance/export-policy` (export decision and read-log check).

Slice 6 (FR-48.11, FR-48.10, AC-48.7; `0006_legal_hold.sql`, `0007_service_token_replay.sql`):

## Legal holds, retention and erasure

- `legal_hold` (RLS on tenant and, restrictively, on legal-entity scope; `passage_app` has no DELETE): a hold names a
  legal entity and at least one of `subjectRef`, `dataClass`, `recordRef`. Every field set on the hold must equal the
  target's, and a hold on a data class also covers a target that names no class (an erasure of a subject reaches every
  class). A target without a `subjectRef` is not covered by a subject hold, so the holder names the record or class too
  when the subject is unknown. A hold is ACTIVE until released with a reason; holds are per legal entity, so a subject
  held in two entities needs two holds. Placing and erasing serialize per (tenant, legal entity) under an advisory lock,
  so a hold cannot slip in between an erasure's check and its command.
- Retention defaults (`GET /v1/privacy/retention-policies`, `domain.DefaultRetention`; per-entity overrides
  `retention.<record_class>` and the `LOC-0502` statutory floor are not stored yet):

| Data class | Retention | Counted from | Source |
|---|---|---|---|
| `AP_PAYEE` | 84 months | last activity | owner decision 2026-11: 7 years after last activity |
| `FINANCIAL_RECORD` | 84 months | last activity | owner decision 2026-11: 7 years after last activity |
| `EMPLOYMENT` | 72 months | termination | PRD FR-48.11: HR 6 years post-termination |
| `CANDIDATE` | 6 months | rejection | PRD wave 42: candidate data deleted 6 months after rejection, retention class `CANDIDATE` |
| `SYSTEM_LOG` | 12 months | creation | PRD FR-48.11: logs 1 year |

- Erasure: `POST /v1/privacy/erasures` takes `legalEntityId`, `subjectRef`, optional `dataClasses` (none means every
  class) and an optional `requestId` of an approved or delivered ERASURE subject request of the same subject. A covering
  hold refuses it (`PRV-1201`, nothing stored, nothing emitted). Otherwise the append-only `erasure_command` and
  `privacy.erasure.requested.v1` commit together (`erasure_id`, `request_id`, `legal_entity_id`, `subject_ref`,
  `data_classes`, `requested_at`). The event says only that no hold covered the subject then. Participants must apply
  their own retention (a record inside it is kept and picked up by their retention sweep) and re-check holds through
  `holds:check` before they change anything, and must not erase when the check cannot be answered.
- Hold check contract (for the HR candidate purge, #266, and any other participant):
  `GET /v1/privacy/holds:check?legalEntityId=&subjectRef=&dataClass=&recordRef=` answers `{"held": bool, "holdIds": [...]}`.
  The caller signs a service token for audience `compliance` with the duty `compliance.legal_hold.check` (grant
  `service_grants.<caller>.compliance` in `policies/authz/data.json`, mirrored in `platform/go/tenant`), the legal entity
  must be in the token's scope, and any non-200 or unparsable answer means held. HR would call it per candidate with
  `dataClass=CANDIDATE`, the candidate's `recordRef` and `subjectRef`, and keep the candidate when `held` is true.
- Participants today: `receiving` (one-time payees, `AP_PAYEE`): consumer `receiving-erasure` on
  `passage.privacy.erasure` and `POST /v1/receiving/one-time-payees:erase`.
- Verifying service tokens needs `PASSAGE_SERVICE_NAME=compliance` and `PASSAGE_SERVICE_PUBLIC_KEYS` (chart
  `serviceIdentity.publicKeys`); the shared replay cache is `service_token_replay` (`0007`).

## Run and test

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/compliance
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://admin... PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app... go test ./...
```

Without `PASSAGE_TEST_DATABASE_URL` the store and migration tests skip. With it they apply the migrations,
assert RLS on every tenant table, and, with `PASSAGE_TEST_APP_DATABASE_URL`, run the store as
`passage_app` (NOBYPASSRLS) on the real grants of `0003_grants.sql` and `0004_screening.sql`, probe cross-tenant isolation and
check the append-only tables refuse UPDATE and DELETE.

Fixture `testdata/sod-50.json` (fx-sod-50) plants 50 users across the seed rules.
`TestDetect_PlantedFixtureNoFalseNegatives` asserts every planted conflict is detected and nothing else
fires; `TestEvaluate_PlantedFiftyReconcileWithRecomputation` stores them and reconciles the OPEN count and
the `conflict.detected` events with an independent recomputation (PRD §9.48.9). Fixture
`testdata/sod-50-distinct.json` plants 50 pairwise distinct role combinations (roles holding both duties
of a rule, several conflicts per user, all 11 seed rules; 81 conflicts) and 12 near misses;
`testdata/sod_ref.py` (stdlib, `python3 -I`) recomputes its expectations from the seed rules.
`TestDetect_FiftyDistinctCombinationsAndNearMisses` checks detection and the simulate block, and
`TestMitigate_FiftyDistinctCombinationsRecordEveryApproval` stores them, refuses self-approval and records
a second approver's mitigation on all 81. Rapid property tests
assert a role set holding both duties of any seed rule is never missed, sampling at 100% selects every
document, the case machine never skips a state and no break-glass session exceeds 240 minutes.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | required | Postgres (`passage_app` role, RLS enforced) |
| `MIGRATE_ON_START` | unset | `true` applies migrations at start (dev only) |
| `SOD_BLOCK_HIGH` | `true` | tenant policy `sod.block_high`; `false` reports without blocking |
| `SOD_MAX_MITIGATION_DAYS` | `365` | tenant policy `sod.max_mitigation_days`, 1..730 |
| `AUDIT_CASE_ARCHIVE_DAYS` | `365` | tenant policy `audit.case_archive_days`, 30..3650 |
| `BREAKGLASS_MAX_MINUTES` | `60` | tenant policy `breakglass.max_minutes`, 15..240 |
| `BREAKGLASS_SELF_ACTIVATE` | `false` | tenant policy `breakglass.self_activate` |
| `BREAKGLASS_REVIEW_DAYS` | `3` | tenant policy `breakglass.review_days`, 1..10 business days |
| `SCREENING_THRESHOLD` | `0.88` | tenant policy `screening.threshold`, 0.70..0.99 |
| `TRADE_LICENSE_CHECK` | `BLOCK` | legal entity policy `trade.license_check`: `BLOCK`, `WARN` or `OFF` |
| `FIELDCRYPT_KEYS_FILE` / `FIELDCRYPT_KEYS`, `FIELDCRYPT_CURRENT_KEY_ID` | `id=base64(32 bytes)` lines (a mounted secret; Helm `fieldCrypt.keysSecret` and `fieldCrypt.currentKeyId`) and the key id in use. Erasure pseudonyms are an HMAC under a tenant key derived from the current key, so the key must stay outside the database; without it the erasure preview is a 503 `PRV-1211` and nothing changes. Rotating the current key changes the tokens of later erasures, so do not rotate `FIELDCRYPT_CURRENT_KEY_ID` while erasure requests are open |
| `HTTP_ADDR` / `ADMIN_ADDR` | `:8080` / `:9090` | listeners |

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `AUTH-0001` | 401 | missing or malformed tenant |
| `AUTH-0003` | 403 | missing duty |
| `COM-0001` | 400 | malformed JSON body or unknown field |
| `COM-0002` | 400 | request failed validation (`errors` lists fields) |
| `SOD-1101` | 200 (`code` in the simulate body) | assignment blocked by an unmitigated HIGH conflict |
| `SOD-1102` | 422 | self-approval of a mitigation refused |
| `SOD-1103` | 422 | rule does not allow mitigation |
| `SOD-1104` | 422 | conflict not in the required state |
| `SOD-1105` | 422 | reason, control or approver missing |
| `SOD-1106` | 422 | mitigation expiry not in the future or past the cap |
| `SOD-1107` | 422 | risk acceptance on a HIGH conflict |
| `SOD-1108` | 404 | no conflict with that id |
| `SOD-1109` | 412 / 428 | stale `If-Match` (412) or missing `If-Match` (428) |
| `SOD-1110` | 422 | `Idempotency-Key` reused with a different request |
| `SOD-1111` | 422 | the conflict's rule is no longer in the rule set |
| `INC-1302` | 422 (domain) | a missed breach deadline closed without a late-notification justification |
| `INC-1303` | 422 | breach clocks requested for an incident not confirmed as a personal-data breach |
| `INC-1310` / `INC-1311` / `INC-1312` | 422 (domain) | awareness time missing, US state window outside 30..60 days, unknown regime; the endpoint reports the first and last as `COM-0002` field errors |
| `ESG-0602` | domain | record changed since the signing dialog opened (BR-48.11) |
| `ESG-0603` | domain | signing locked after three failed re-authentications |
| `AUD-0701` | 200 (`code` in the evaluate body) | warning: documents violate the policy |
| `AUD-0711` | 422 | unknown audit rule type, or a violation whose rule has no case category |
| `AUD-0712` | 422 | audit rule configuration missing or out of range (KEYWORD without words, THRESHOLD without a currency or a positive limit, a holiday that is not YYYY-MM-DD, AGGREGATE without a limit, SAMPLING without a seed) |
| `AUD-0713` | 422 | case action not allowed from the current status |
| `AUD-0714` | 422 | resolution code missing or of the wrong kind; recovery amount negative or without currency |
| `AUD-0715` | 422 | archive refused: the archive window has not passed since closing |
| `SEC-0707` | 422 | break-glass cannot grant SecurityAdmin or PlatformAdmin to yourself (BR-48.19) |
| `SEC-0708` | domain | break-glass session not reviewed within `breakglass.review_days` (`ReviewOverdue`; no endpoint yet) |
| `SEC-0713` | 422 | break-glass request incomplete or out of range (reason, ticket, roles, minutes) |
| `TRD-1002` | 200 (`code` per export check line) | destination under comprehensive embargo; delivery blocked (BR-48.16) |
| `TRD-1003` | 200 (`code` per export check line) | export license required and none valid (blocked under BLOCK, a warning under WARN) |
| `TRD-1004` | 422 | screening subject, threshold or list entry invalid |
| `TRD-1005` | 422 | screening decision without a reason or decider |
| `TRD-1006` | 422 | decision on a hit that is not OPEN |
| `TRD-1007` | 422 | export check input invalid (destination, region, item classification, quantities, licenses) |
| `TRD-1008` | 404 | no screening hit with that id |
| `TRD-1009` | 412 | stale `If-Match` on a screening decision (missing `If-Match` is 428 `SOD-1109`) |
| `PRV-1201` | 422 | erasure blocked by a legal hold; nothing changed (AC-48.7) |
| `PRV-1202` | 422 | subject request type, regulation or receipt invalid |
| `PRV-1203` | 422 | identity not verified: two attributes and the e-mail link are required |
| `PRV-1204` | 422 | action not allowed from the request's state, or erasure preview on a request that is not an ERASURE past discovery |
| `PRV-1205` | 422 | closing after the deadline without a justification |
| `PRV-1206` | 422 | deadline override longer than the regulation allows |
| `PRV-1207` | 422 | rejection without a reason |
| `PRV-1208` | 200 (`code` in the purge check body) | record skipped by the retention purge (BR-48.6) |
| `PRV-1209` | 404 | no subject request with that id |
| `PRV-1210` | 412 | stale `If-Match` on a subject request action (missing `If-Match` is 428 `SOD-1109`) |
| `PRV-1211` | 503 | no pseudonymization key configured; the erasure changed nothing |
| `PRV-1220` | 422 | legal hold without a scope or with an unknown data class, or a reason under 10 characters |
| `PRV-1221` | 404 | no legal hold with that id (or outside the caller's legal-entity scope) |
| `PRV-1222` | 422 | the hold is already released |
| `PRV-1223` | 412 | stale `If-Match` on a hold release (missing `If-Match` is 428 `SOD-1109`) |
| `SOD-1120` | 422 | campaign start or close missing, or window outside 3..60 days |
| `SOD-1121` | 422 | a manager cannot certify their own roles |
| `SOD-1122` | 422 | campaign line incomplete, duplicated or with an unknown decision |
| `INC-1320` / `INC-1321` / `INC-1322` / `INC-1323` | 422 | incident severity or source unknown, a skipped or unknown step, a breach naming no regime |
| `SEC-0709` | 422 | download denied by export policy |
| `SEC-0710` | 422 | export policy requires a reason of at least 10 characters |
| `SEC-0711` | 422 | unknown export classification, channel or action |
| `SEC-0714` | 422 | read-log retention outside 90..3650 days |
| `SEC-0715` | 422 | negative export row count |
| `COM-0500` | 500 | internal error |

## Deploy

`charts/compliance` (`helm lint` clean): Deployment, Service, optional ServiceMonitor; `DATABASE_URL`
from the `passage-compliance-db` secret; the tenant policies (`SOD_*`, `AUDIT_CASE_ARCHIVE_DAYS`,
`BREAKGLASS_*`, `SCREENING_THRESHOLD`, `TRADE_LICENSE_CHECK`) from values. `Dockerfile` builds from the repository root.

## Wave reference

PSG-48 slice 1: FR-48.4, FR-48.5 and BR-48.1 for the seed rule set; AC2, AC-48.2, AC-48.3, AC-48.4.
Slice 2: the rules of FR-48.13 (BR-48.11, BR-48.12; AC-48.12 lockout), FR-48.14 (BR-48.14; AC-48.13 on
the pure rule) and FR-48.29 (BR-48.18; AC-48.19 deadline). Slice 3: the rule kernel of FR-48.14 (all
rule types), the case machine and grouping of FR-48.15, and the planning part of FR-48.23 (BR-48.19;
AC-48.18 review deadline). Slice 4: FR-48.19 (BR-48.15; AC-48.16 at the store: blocked, OPEN, cleared,
no recurrence) and the determination of FR-48.20 (BR-48.16; AC-48.17 `TRD-1003`). Slice 5: the
subject request lifecycle of FR-48.10 with storage (AC-48.7 at the store; the 30 and 45 day deadlines),
BR-48.5 and BR-48.6 of FR-48.11, the close rules of FR-48.6 (BR-48.9), the incident lifecycle of FR-48.29
(BR-48.18 closure with notification times) and the policy kernels of FR-48.24 and FR-48.25. Everything else in the ticket is listed in `NOT-IMPLEMENTED.md`.
