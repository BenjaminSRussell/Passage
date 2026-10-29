# Runbook: object-store replication lag (lakehouse and documents, FR-50.4)

Object-store replication copies the lakehouse buckets (Iceberg tables) and the document store to the
passive region. Object Lock buckets (WORM evidence, wave 48) are replicated with the same rule set.

Symptoms: replication backlog age above 15 minutes, a rising count of objects pending replication,
or S7 freshness (lakehouse lag) above 60 s p95 while the source region is healthy.

## 1. Locate the backlog
1. Read the replication metrics for the source bucket: pending bytes, pending objects, oldest pending age.
2. Split by prefix. Lakehouse prefixes are per tenant and per namespace; a single tenant with a large
   backfill shows as one prefix.
3. Check the replication role and IAM-equivalent credential (from Vault, file-projected). An expired
   credential stops replication silently; the pending count keeps rising with no errors.

## 2. Common causes
- Bulk load (month-end export, lakehouse backfill): expected. Confirm the job is progressing, then wait.
- Credential expiry: rotate from Vault, then confirm the next replication cycle succeeds.
- Object Lock retention on the destination bucket with mismatched retention settings: replication
  fails per object. Fix the destination policy; do not disable Object Lock.

## 3. Failover interaction
Lakehouse tables are rebuilt from CDC in the new primary if a replicated object is missing, but the
document store is not. Before flipping DNS (failover step 5), confirm the document backlog is zero or
that the remaining objects are listed in the failover record with their keys.

## 4. Verify
1. Oldest pending age under 15 minutes for every prefix.
2. Sample five recent documents: checksum in the destination equals the source.
3. Lakehouse freshness (S7) back under 60 s p95.

## Record
Backlog at fence time and any objects listed as not replicated. These are RPO evidence for documents.
