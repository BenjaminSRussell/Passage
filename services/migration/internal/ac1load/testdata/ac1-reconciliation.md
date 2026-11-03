# Wave 10 AC1 reconciliation

Seeded AX-shaped load of 50k customers, 20k vendors, 100k items and 10k BOMs through the migration pipeline. Persistence is Postgres `migration.xref`; the target is the in-process `MemLoader` (owning-service batch APIs are a later slice).

**Result: PASS**

- Elapsed: 12.458680717s (budget 30m0s)
- Volumes: customers 50000, vendors 20000, items 100000, boms 10000

## customer

- status succeeded extracted 50000 created 50000 updated 0 rejected 0 in 3.336143156s
- reconciliation: OK
  - row_count: ok=true source 50000 target 50000
  - hash_total: ok=true order-independent SHA-256 over key and content hash
  - sample_compare: ok=true 0 sampled keys differ
- re-run: created 0 updated 0 unchanged 50000

## vendor

- status succeeded extracted 20000 created 20000 updated 0 rejected 0 in 1.105919901s
- reconciliation: OK
  - row_count: ok=true source 20000 target 20000
  - hash_total: ok=true order-independent SHA-256 over key and content hash
  - sample_compare: ok=true 0 sampled keys differ
- re-run: created 0 updated 0 unchanged 20000

## item

- status succeeded extracted 100000 created 100000 updated 0 rejected 0 in 5.530187898s
- reconciliation: OK
  - row_count: ok=true source 100000 target 100000
  - hash_total: ok=true order-independent SHA-256 over key and content hash
  - sample_compare: ok=true 0 sampled keys differ
- re-run: created 0 updated 0 unchanged 100000

## bom

- status succeeded extracted 10000 created 10000 updated 0 rejected 0 in 594.280903ms
- reconciliation: OK
  - row_count: ok=true source 10000 target 10000
  - hash_total: ok=true order-independent SHA-256 over key and content hash
  - sample_compare: ok=true 0 sampled keys differ
- re-run: created 0 updated 0 unchanged 10000

