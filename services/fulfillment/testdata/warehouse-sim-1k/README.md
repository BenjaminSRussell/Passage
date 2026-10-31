# Fixture: warehouse-sim-1k

Simulated warehouse for the pick-pack-ship E2E check (AC-28.7, section 5.28.9).

The dataset is generated from a fixed seed, not stored, so it cannot drift from
the code that reads it. The generator and the run are
`internal/store/sim_test.go` (`TestWarehouseSim1k_ZeroInventoryMismatch`). It
runs against Postgres through the store, as the application role with
row-level security, and is skipped without `PASSAGE_TEST_DATABASE_URL` or with
`go test -short`.

| Parameter | Value |
|---|---|
| Confirmed orders | 1,000, 1 to 3 lines each, all ATP-OK, ship dates today to today + 2, one warehouse per order |
| Products | 120 |
| Warehouses | 3 |
| Customers | 150 |
| Legal entities | 2 (orders alternate) |
| Seed | 28 (`math/rand`) |
| Quantities and costs | 1 to 100 units per line; unit cost 1.00 to 49.99 USD |
| Pick behaviour | every tenth shipment short-picks its first line by a tenth (at least one unit); the shipped part posts on one slip, the remainder is re-picked and posts on a second slip |
| Workers | 8 shipments processed in parallel |

Pass criteria checked by the test:

- every order's shipment ends SHIPPED;
- per product, the quantities in `packingslip.posted` events (what inventory
  issues from, wave 16) equal the stored packing slip lines and the released
  quantities: zero mismatch;
- Dr 1405 equals the shipped cost and Cr 1400 its negative; no other account
  is posted (physical posting);
- one slip per order plus one per re-picked remainder;
- the short picks and backorders report lists every short pick, all closed,
  and `pick.confirmed` carried each backordered quantity.

## Simulated warehouse on-hand (batch/serial mix)

`internal/store/siminventory_test.go` (`TestWarehouseSim1k_SimulatedWarehouseOnHand`)
generates the same shape from seed 2828 and adds the inventory side:

| Parameter | Value |
|---|---|
| Tracking | per product from the seed: 7 in 10 untracked, 2 batch, 1 serial (92 / 20 / 8) |
| Stock | demand plus 0 to 20 units per warehouse and product (15 have no spare unit); batches split 1 to 3 ways, one serial per unit |
| Picking | oldest batch first, lowest serial first; entries captured after each pick |
| Cancellations | every seventh shipment cancels its first slip and posts it again (143) |
| Short picks | every tenth shipment, as above (100) |

Pass criteria: every order ends SHIPPED; the floor ledger (what the pickers took
and got back) equals an on-hand projection rebuilt only from
`packingslip.posted` and `packingslip.cancelled` on every warehouse, product,
batch and serial; no balance goes negative during the replay; and each
warehouse and product ends at seeded stock minus demand.

Not in this fixture: inventory's own consumer (wave 16), the "all SHIPPED
within 60 minutes" timing on production hardware, and carrier calls
(carrier-gateway).
