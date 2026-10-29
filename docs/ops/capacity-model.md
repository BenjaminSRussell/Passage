# Capacity model (FR-50.9)

Maps workload inputs to node counts, Postgres sizes and Kafka brokers. Model version is
`capacity.model_version` (latest published). Estimates are served by `GET /api/v1/ops/capacity/estimate`;
this document is the human-readable form.

## Inputs
| Input | Symbol | Reference (10k users) |
|---|---|---|
| Concurrent users | U | 10,000 |
| Think time | T | 10 s (interactive), 5 s (scanners) |
| Journal lines per hour, peak | J | 1,000,000 |
| Order lines per hour | O | 100,000 |
| Scanners | S | 2,000 at 1 scan / 5 s |
| Storage growth per year | G | 1.2 TB ledger, 200 GB orders, 100 GB scans |

## Derived load
- Interactive requests/s = U / T = 10,000 / 10 = **1,000 req/s** (matches the FR-50.1 budget).
- Journal lines/s = J / 3,600 = 278 lines/s average; sustained target **5,000 lines/s** gives 18x headroom.
- Journal batches/h = J / 500 = 2,000 batches/h (S1 runs 34 batches/min, about 1.02M lines/h).
- Order lines/s = O / 3,600 = 27.8 lines/s; per-pod budget 30 lines/s, so 1 pod carries the load and 3 are deployed for availability.
- Scans/s = S / 5 = **400 scans/s**.
- Kafka peak = 60,000 msgs/s (budget); brokers sized for 6 with `min.insync.replicas=2`.

## Node and pod sizing
Pods needed per region = ceil(peak rate / per-pod rate) x 1.5 headroom. The passive region runs a warm 50%
of that and scales to 100% during failover inside the RTO budget, so the pair costs 1.5x one region.
- Interactive API: 1,000 req/s / per-pod budget [ASSUMED 80 req/s] = 13 pods, x1.5 = 20 pods.
- Journal ledger writers: 5,000 lines/s / per-pod budget [ASSUMED 600 lines/s] = 9 pods, x1.5 = 14 pods.
- Nodes = pods x request size / usable node capacity [ASSUMED 6 vCPU, 24 GiB usable per node].

## Postgres
- One CNPG cluster per context database; 1 primary + 2 standbys in the primary region (FR-50.2).
- Ledger: 500 GiB initial volume per region (`postgres.storageSize`), monthly range partitions on the journal line
  table, hash by tenant (16). Expect about 100 GiB per month at peak [ASSUMED].
- Passive region: same instance class; async replica clusters.

## Kafka
- Strimzi, 6 brokers, RF 3, `min.insync.replicas=2`, `acks=all`.
- Partitions per topic >= 2 x consumer parallelism.
- Retention 7 days, tiered storage for older data; 30 TB retained (budget).

## How to use
Run the model with the workload inputs; compare against the last load campaign's measured
throughput per pod, then update `capacity.model_version`. Any change to a per-pod budget
requires a new campaign run.
