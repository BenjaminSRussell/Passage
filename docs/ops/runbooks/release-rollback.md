# Runbook: release rollback

Trigger: fast-burn alert in the 30-minute post-release window (automatic, OPS-1706), or a manual
decision by a ReleaseManager.

1. Pause the release ring (ReleaseManager duty).
2. Roll back the application by digest in Argo CD (previous digest; digests are signed and SBOM-attested).
3. Database: expand/contract means the previous code runs against the expanded schema. Do not run a
   contract step in a rollback; it is deferred to the next release (see [upgrade strategy](../upgrade-strategy.md)).
4. Restore DNS weight if it was changed for canarying.
5. Re-run the smoke suite; record the rollback in the release history with the trigger.
