# Runbook: backup verification or PITR drill failure (Sev-2)

Source: weekly backup verification on production and the monthly point-in-time-recovery (PITR) drill
(PRD 9.50.16, go-live checklist item 8: PITR drill on production data within 7 days before cutover).
Severity: Sev-2 by default. Escalate to Sev-1 if the failing cluster is the ledger and no clean backup is newer
than the RPO target (5 min, FR-50.5).

## Detect
- The drill job reports a failed restore, a checksum mismatch, a restore that does not reach the target LSN,
  or row counts that differ from the source snapshot by more than the drill tolerance.
- The last successful base backup or WAL archive is older than the RPO target. `PassageBackupStale` and
  `PassageWalArchiveFailing` (multi-region chart) fire on the same conditions.

## Triage (first hour)
1. Identify the cluster (ledger, inventory, sales, and so on), the drill id, and the target recovery time.
2. Check the object-store bucket: is the WAL archive continuous up to now? A gap is the most common cause.
3. Check the CNPG backup status and the last base backup timestamp.
4. Confirm the source cluster is healthy. A failing source with a good backup is a different incident.

## Fix
- **WAL gap:** restart archiving (CNPG `barman-cloud` plugin), take a fresh base backup, and re-run the drill.
  The drill must restore from a backup taken after the fix.
- **Corrupt base backup:** mark it unusable, take a new base backup, verify its checksum, re-run.
- **Restore timeout:** raise the drill's restore resources in a test namespace only. Do not change production
  resources to make the drill pass.
- **Row-count mismatch:** compare the drill's source snapshot with the live source for the same LSN range before
  blaming the backup. Tolerance breaches on live tables without an LSN pin are not evidence of a backup fault.

## Restore one tenant to a point in time
1. `tenant-restore scratch-manifest --name passage-pg-restore --target-time <RFC 3339>` and apply the output:
   CNPG recovers a scratch cluster from the object store. Wait until it is healthy.
2. Dry run: `tenant-restore copy --tenant <uuid> --schema <schema>` with `PASSAGE_RESTORE_SCRATCH_URL` and
   `PASSAGE_RESTORE_LIVE_URL` set. It runs the whole copy in one live transaction and rolls it back, then prints
   inserted, updated, unchanged and live-only counts per table.
3. Review the counts with the tenant's administrator, rerun with `--apply`, then delete the scratch cluster.

## Evidence
Record the drill id, restore duration, final LSN reached, checksum result and the reviewer. Store the record with
the wave-48 evidence (SOC 2 A1.2/A1.3 availability criteria).

## Exit
A passing drill from a backup taken after the fix. If a production-data drill cannot run within 7 days of
cutover, cutover is NO-GO (go-live checklist item 8).
