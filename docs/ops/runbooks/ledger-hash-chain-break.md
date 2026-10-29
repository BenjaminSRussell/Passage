# Runbook: ledger hash chain break (Sev-1)

Code: OPS-1704 ("Ledger hash chain broken for tenant {tenant} at journal {id}; user access blocked.").
Rule: BR-50.3. Each journal carries `prev_hash`; the chain head per tenant must verify after every promotion.
Severity: Sev-1. Response: 15 minutes. Target: user access restored only after the chain verifies or the
break is contained by a documented decision.

## Detect
- Failover verification step 6 fails (see [failover](failover.md)).
- On a game day, `deploy/multi-region/gameday/baseline.py verify` fails its `ledger` check against the baseline
  manifest (`fx-gameday`), with the digest recorded at capture.
- OPS-1704 raised by the ledger verifier for a tenant.

## Contain (first 15 minutes)
1. Keep user access blocked for the tenant. Do not restart or re-promote anything yet; capture state first.
2. Record: tenant id, first broken journal id, region in use, time of last clean verification, and whether a
   failover or fail-back is in progress.
3. Freeze deploys (`release` ring paused) and announce on the status page as "investigating", no detail.

## Diagnose
1. Compare the chain head per tenant with the last recorded head (failover record or game-day baseline).
   - Head matches or exceeds baseline: the break is in a later journal. Walk forward from the baseline.
   - Head is behind baseline: committed journals are missing on this side. This is a data-loss event; finish
     step 4, then go to "Journals genuinely lost" under Recover.
2. Find the first journal whose `prev_hash` does not match the hash of its predecessor. Check whether its
   predecessor was written by the other region (asynchronous replication gap) or by this region.
3. Check the CNPG cluster history: a promotion during the gap, or a timeline switch, explains most breaks.
4. Check that gap-free fiscal sequences are continuous across the same window (BR-50.4). Both must be checked together.

## Recover
- **Gap from replication lag (journals exist on the other region):** restore the missing journal rows from the
  other region's WAL or the MirrorMaker 2 topic, replay them through the ledger in original order, and verify again.
  Never edit a posted journal; the hash chain is the evidence.
- **Journals genuinely lost:** do not fabricate them. Escalate to the ledger owner and the customer controller.
  Record the loss in `failover_event.notes`, set `ledger_hash_ok=false`, and follow the customer's incident process.
- Re-run verification for the tenant. Only when it passes may user access be restored.

## Close
1. Verification output and timeline attached to the incident.
2. Restore user access and lift the freeze. Announce resolution on the status page.
3. Post-incident review within 5 working days. If the break came from a failover, update [failover](failover.md)
   and the game-day checklist.
