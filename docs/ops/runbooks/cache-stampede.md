# Runbook: cache stampede (pricing and policy caches, FR-50.3)

Symptoms: pricing p95 above 20 ms (SLO) with database CPU spiking at the same moment, or a burst of
identical policy-bundle fetches to OPA. Caches hold pricing and policy bundles with invalidation at
5 seconds or less. A mass expiry, or an invalidation for a popular key, sends every pod to the database
at once.

## Design rules this runbook relies on
These rules are not built yet: the caches are pricing and platform service code, a handoff listed in
`docs/ops/NOT-IMPLEMENTED.md`. Until they exist, section 2 step 2 has no knob to turn; scale the
pricing service out instead.
- Each cache entry has a TTL with random jitter (up to 10%) so entries do not expire together.
- Refill is single-flight per key within a pod. Other requests wait for the in-flight load up to 200 ms,
  then fall back to the database with a per-pod concurrency limit.
- Invalidation is driven by events (`pricing` and `policy` topics); the 5-second bound is measured from
  the event timestamp to the last pod's eviction.
- Caches are never the system of record. A cache miss must produce the same answer as a warm hit.

## 1. Confirm it is a stampede
1. Compare cache hit ratio and database query rate for the pricing and policy keys in Grafana. A stampede
   shows a hit ratio collapse with a short spike in identical queries (`pg_stat_statements`, same query id).
2. Check for a recent bulk price or policy change. A bulk change should invalidate by price-list key,
   not flush the whole cache. A full flush is the usual cause.

## 2. Stop the amplification
1. If a full flush happened, do not flush again. Let entries refill over the jitter window.
2. Lower the per-pod database concurrency limit for the pricing service (config only, no restart needed
   on services that read it through the config watcher). Expect higher latency, not errors.
3. For OPA bundle storms: confirm pods are on the current bundle. Pods without a current bundle fail
   readiness and receive no traffic, so the storm is not an outage. Do not disable the bundle check.

## 3. Verify
1. Hit ratio back above 90% within 5 minutes.
2. Pricing p95 below 20 ms; order intake p99 below 500 ms (FR-50.1).
3. Record the trigger (which change, which keys) so the next bulk change uses key-scoped invalidation.

## Cache tier
The in-pod cache is tier 1. A shared tier 2 on Valkey is proposed and not yet in ADR-0001 (architecture
Q4). If it is adopted, the same steps apply to it; never flush the shared tier to fix one key.
