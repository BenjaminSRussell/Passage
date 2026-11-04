# Wave 10 AC1 reconciliation

Seeded load of 50k customers, 20k vendors, 100k items and 10k BOMs through the migration pipeline. The cross-reference is Postgres `migration.xref`; the target is the test table `ac1.target` (not the party/product batch APIs, which are unmeasured: #475). Reconciliation compares the raw extract, mapped independently, with a fresh read of `ac1.target`.

**Result: PASS**

- Commit: 20ae238e3acdfb484a4345653eddb00d9c7a45d7, host: Zacharys-MacBook-Pro, database: PostgreSQL 16.15 (Homebrew) on aarch64-apple-darwin27.0.0, compiled by Apple clang version 21.0.0 (clang-2100.3.34.2), 64-bit, role: passage_app
- Elapsed: 1m59.93299425s (budget 30m0s)
- Volumes: customers 50000, vendors 20000, items 100000, boms 10000

## customer

- status succeeded extracted 50000 created 50000 updated 0 rejected 0 in 1m17.389866208s
- hash total: source d26807c380db0dde3939580b6ea0f81c93ea8644d822739776cb3c9a26a8c5c8 target d26807c380db0dde3939580b6ea0f81c93ea8644d822739776cb3c9a26a8c5c8
- reconciliation: OK
  - row_count: ok=true source 50000 target 50000
  - hash_total: ok=true order-independent SHA-256 over key and content hash
  - sample_compare: ok=true 0 sampled keys differ
- re-run: created 0 updated 0 unchanged 50000

## vendor

- status succeeded extracted 20000 created 20000 updated 0 rejected 0 in 4.773680375s
- hash total: source fbf90d20f689ccfcc7528c124a0dc728b55f7a023f334b72a162b2a8d50a6059 target fbf90d20f689ccfcc7528c124a0dc728b55f7a023f334b72a162b2a8d50a6059
- reconciliation: OK
  - row_count: ok=true source 20000 target 20000
  - hash_total: ok=true order-independent SHA-256 over key and content hash
  - sample_compare: ok=true 0 sampled keys differ
- re-run: created 0 updated 0 unchanged 20000

## item

- status succeeded extracted 100000 created 100000 updated 0 rejected 0 in 29.732894833s
- hash total: source 8f97f176cfa9f6af215d680a645215cbc1c2cb1d3c34b81052dc1e2ce6cf8d4a target 8f97f176cfa9f6af215d680a645215cbc1c2cb1d3c34b81052dc1e2ce6cf8d4a
- reconciliation: OK
  - row_count: ok=true source 100000 target 100000
  - hash_total: ok=true order-independent SHA-256 over key and content hash
  - sample_compare: ok=true 0 sampled keys differ
- re-run: created 0 updated 0 unchanged 100000

## bom

- status succeeded extracted 10000 created 10000 updated 0 rejected 0 in 2.285180583s
- hash total: source 53b29cae4df4fefb168b7785d764b4683955052d2d5fa80a7ced53d33e2b7d04 target 53b29cae4df4fefb168b7785d764b4683955052d2d5fa80a7ced53d33e2b7d04
- reconciliation: OK
  - row_count: ok=true source 10000 target 10000
  - hash_total: ok=true order-independent SHA-256 over key and content hash
  - sample_compare: ok=true 0 sampled keys differ
- re-run: created 0 updated 0 unchanged 10000

