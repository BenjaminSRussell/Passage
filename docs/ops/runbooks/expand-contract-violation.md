# Runbook: expand/contract violation (OPS-1707)

Message: OPS-1707 "Migration drops column {col} still referenced in this release."
Rule: BR-50.7. A migration that drops a column cannot ship in the same release as the code that stops using it.
Gate: the expand/contract linter `deploy/multi-region/tools/expand_contract_lint.py` (PRD test plan, unit).
Severity: error. The release is blocked. Running it on every release in CI is still a handoff to the CI owner
(NOT-IMPLEMENTED.md), so until then the release manager runs it by hand:

    python3 deploy/multi-region/tools/expand_contract_lint.py --migrations <this release's new migrations> \
        --code services/<name> --previous-code <checkout of the previous release tag>/services/<name>

## What the rule means
Schema change happens in four separate releases:
1. **Expand:** add the new column or table. Nullable or with a default. Code writes both shapes.
2. **Backfill:** populate the new shape for existing rows, in batches, idempotently.
3. **Switch:** code reads only the new shape. The old column is no longer referenced by any service in the release.
4. **Contract:** drop the old column, in a release after the switch has run in every region and ring.

During a rolling deploy release N-1 still runs against the migrated schema, so the linter fails a drop or a
rename of a column, table or view whose name still appears in this release's code or in N-1's code. A drop and
the last reference to the column in the same release is the violation.

## Detect
- The linter exits 1 with one line per violation: `OPS-1707 <migration>: migration drops column <name> still
  referenced in this release (<files>)` or `... in the previous release (N-1) (<files>)`.
- A line ending `DROP or RENAME statement could not be parsed` means a statement the linter cannot read (for
  example `DROP SCHEMA`); split the release by hand and record why in the release notes.

## Fix
1. Remove the drop from the migration. Keep the column.
2. Move the drop to a new migration file in the next release, after the switch release is live in all regions.
3. If the only reference is in N-1's code, the drop is still a violation: N-1 keeps running during the rollout.
   Wait one more release.
4. If the column is referenced only by a stale test fixture, update the fixture in the same change. Do not
   weaken the linter. A false positive (the name is an unrelated word in the code) is fixed by renaming, not
   by skipping the check.
5. If the column is referenced by a customer integration (AX/SAP coexistence feed), confirm the feed no longer
   reads it before the contract step, and record the confirmation in the release notes.

## No bypass
BR-50.7 has no exception and the linter has no skip flag. Data that must stop being exposed is handled by a
code change in this release (stop reading or writing the column, or null it) and the drop follows in the next.

## Rollback
Rollback goes back to the previous image digest through Argo CD. Because the column was kept in the expand
release, the old code still works. Do not run contract migrations during a rollback window.
