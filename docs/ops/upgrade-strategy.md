# Upgrade strategy: zero-downtime releases (FR-50.7)

Goal: upgrade downtime 0 (from 4-8 h in AX/SAP), cadence weekly (from quarterly).

## Rolling deploys
- Every service runs at least two replicas; Argo CD rolls pods by digest with readiness on `/readyz`.
- Release rings: `release.cadence` per tenant (WEEKLY, BIWEEKLY, MONTHLY pinned ring).
- Auto-rollback window: 30 minutes (`release.auto_rollback_window_min`). A fast-burn alert inside the
  window reverts the release automatically (BR-50.6, OPS-1706). See [release rollback](runbooks/release-rollback.md).

## Expand / contract migrations
Each schema change is four releases or fewer, never one:
1. **Expand**: add the column or table, nullable or with default. Old and new code both work.
2. **Backfill**: populate in batches with a checkpoint; idempotent.
3. **Switch**: code reads and writes the new shape.
4. **Contract**: drop the old column in a later release.

Linter rule (BR-50.7, OPS-1707): a migration that drops a column cannot ship in the same release as the
code that stops using it. `deploy/multi-region/tools/expand_contract_lint.py` checks a release's new migrations
against this release's code and the previous release's; the CI job that runs it on every release is a handoff
(see [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md)). Rollbacks never run a contract step; it waits for the next release.

## API compatibility
- Contract tests run N (new) against N-1 (previous) clients on every release candidate.
- Static check per service: `python3 deploy/multi-region/tools/openapi_compat.py --old n1.json --new n.json`
  compares the previous release's OpenAPI (converted to JSON) with the candidate's and fails on any change an
  N-1 client would notice (removed operations, parameters or fields, new required inputs, narrowed request or
  widened response types). New response enum values are warnings the release manager checks against the N-1
  web client.
- Additive-only change within a major version; removed fields need a `Sunset` header of at least 12 months.
- Acceptance: 100% of contract tests pass (test plan, "upgrade compatibility").

## Acceptance evidence
- AC-50.5: release N+1 with an expand migration rolls out with zero failed requests and N clients keep working.
- The upgrade path from the last stage build is tested before v1.0.
