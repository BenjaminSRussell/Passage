# AC-12.4 fixture: 23 documents across one ledger set

`ac124_fixture.json` holds the documents (1-20 as before; 21-23 use `ledger_lines`, below) and the expected per-ledger trial balances. Every figure below was worked out by hand, not read back from the store.

Set `ALL`: leading ledger `GL` (chart STD), parallel ledgers `LOC` (chart LOC) and `TAX` (chart STD). USD throughout.

Account mappings (applied to parallel ledgers only, by posting date):

| Ledger | Source | Target | Effective from |
|---|---|---|---|
| LOC | 6010 | 7010 | 2026-01-01 |
| LOC | 6020 | 7020 | 2026-02-01 |
| TAX | 6020 | 6010 | 2026-02-01 |

Documents 1-4, 6-8, 10, 11, 13-15, 17, 19, 20 post through the set (one entry per ledger, mappings applied). Documents 5, 12, 18 post to LOC only and 9, 16 to TAX only (ledger-specific, no mapping applied, duty `ledger.journal.postledger`).

## Set documents, source accounts (the GL view)

| Doc | Date | Debit | Credit | Amount |
|---|---|---|---|---|
| 1 | 01-05 | 6010 | 2100 | 100.00 |
| 2 | 01-12 | 1000 | 4000 | 5000.00 |
| 3 | 01-20 | 6020 | 1000 | 800.00 |
| 4 | 01-28 | 6010 | 2100 | 250.50 |
| 6 | 02-01 | 6020 | 1000 | 800.00 |
| 7 | 02-03 | 6010 | 2100 | 1200.00 |
| 8 | 02-10 | 1000 | 4000 | 3000.00 |
| 10 | 02-20 | 2100 | 1000 | 1000.00 |
| 11 | 02-25 | 6020 | 2100 | 99.99 |
| 13 | 03-02 | 6010 | 2100 | 600.00 |
| 14 | 03-05 | 1000 | 4000 | 2500.00 |
| 15 | 03-07 | 6010 300.00 + 6020 200.00 | 2100 | 500.00 |
| 17 | 03-10 | 2100 | 1000 | 500.00 |
| 19 | 03-13 | 6010 | 2100 | 0.01 |
| 20 | 03-14 | 6020 | 1000 | 1000.00 |

## GL (leading, no mappings)

- 6010 debit: 100.00 + 250.50 + 1200.00 + 600.00 + 300.00 + 0.01 = 2450.51
- 6020 debit: 800.00 + 800.00 + 99.99 + 200.00 + 1000.00 = 2899.99
- 1000 debit: 5000.00 + 3000.00 + 2500.00 = 10500.00; credit: 800.00 + 800.00 + 1000.00 + 500.00 + 1000.00 = 4100.00; net 6400.00
- 4000 credit: 5000.00 + 3000.00 + 2500.00 = 10500.00
- 2100 debit: 1000.00 + 500.00 = 1500.00; credit: 100.00 + 250.50 + 1200.00 + 99.99 + 600.00 + 500.00 + 0.01 = 2750.50; net -1250.50
- Totals: debit 2450.51 + 2899.99 + 10500.00 + 1500.00 = 17350.50; credit 4100.00 + 10500.00 + 2750.50 = 17350.50. Net 0.00.

## LOC (6010 to 7010 always; 6020 to 7020 from 02-01, so docs 6, 11, 15, 20 map and doc 3 does not)

- 7010 debit: 2450.51 (all GL 6010 lines) + 75.25 (doc 5, direct) = 2525.76
- 6020 debit: 800.00 (doc 3, before 02-01) + 33.33 (doc 18, direct) = 833.33
- 7020 debit: 800.00 (6) + 99.99 (11) + 200.00 (15) + 1000.00 (20) + 12.00 (doc 12, direct) = 2111.99
- 1000 debit: 10500.00; credit: 4100.00 + 12.00 (doc 12) = 4112.00; net 6388.00
- 4000 credit: 10500.00
- 2100 debit: 1500.00; credit: 2750.50 + 75.25 (5) + 33.33 (18) = 2859.08; net -1359.08
- Totals: debit 2525.76 + 833.33 + 2111.99 + 10500.00 + 1500.00 = 17471.08; credit 4112.00 + 10500.00 + 2859.08 = 17471.08. Net 0.00.

## TAX (6020 to 6010 from 02-01, so docs 6, 11, 15, 20 map and doc 3 does not)

- 6010 debit: 2450.51 (GL 6010 lines) + 800.00 + 99.99 + 200.00 + 1000.00 (mapped from 6020) + 40.00 (doc 9, direct) = 4590.50
- 6020 debit: 800.00 (doc 3)
- 1000 debit: 10500.00 + 10.00 (doc 16, direct) = 10510.00; credit: 4100.00; net 6410.00
- 4000 credit: 10500.00 + 10.00 (doc 16) = 10510.00
- 2100 debit: 1500.00; credit: 2750.50 + 40.00 (doc 9) = 2790.50; net -1290.50
- Totals: debit 4590.50 + 800.00 + 10510.00 + 1500.00 = 17400.50; credit 4100.00 + 10510.00 + 2790.50 = 17400.50. Net 0.00.

## Ledger-lines documents 21-23 (set post with common lines plus per-ledger lines)

Common lines are mapped per ledger like any set document; per-ledger lines go to that ledger only, unmapped (they are written in that ledger's own accounts). Each ledger's resulting entry must balance by itself. Documents with lines for LOC or TAX need `ledger.journal.postledger`.

| Doc | Date | Common lines | GL adds | LOC adds | TAX adds |
|---|---|---|---|---|---|
| 21 | 03-16 | D 1000 1000.00, C 4000 900.00 (short 100.00 on credit) | C 2100 100.00 | C 4000 100.00 | C 2100 100.00 |
| 22 | 03-17 | D 6020 400.00, C 1000 400.00 (balanced) | none | D 7020 50.00, C 2100 50.00 | none |
| 23 | 03-18 | D 6010 300.00 (one-sided) | C 2100 300.00 | C 2100 300.00 | C 1000 300.00 |

Resulting entries (6020 maps to 7020 on LOC and 6010 on TAX from 02-01; 6010 maps to 7010 on LOC):

- Doc 21: GL D 1000 1000.00 / C 4000 900.00 + C 2100 100.00; LOC D 1000 1000.00 / C 4000 900.00 + C 4000 100.00; TAX same as GL. Each totals 1000.00 each side.
- Doc 22: GL D 6020 400.00 / C 1000 400.00; LOC D 7020 400.00 + D 7020 50.00 / C 1000 400.00 + C 2100 50.00; TAX D 6010 400.00 / C 1000 400.00. Totals 400.00, 450.00, 400.00.
- Doc 23: GL D 6010 300.00 / C 2100 300.00; LOC D 7010 300.00 / C 2100 300.00; TAX D 6010 300.00 / C 1000 300.00. Each totals 300.00.

Balances after documents 1-23 (previous figure plus the new lines):

- GL: 1000 debit 10500.00 + 1000.00 = 11500.00, credit 4100.00 + 400.00 = 4500.00; 2100 credit 2750.50 + 100.00 + 300.00 = 3150.50; 4000 credit 10500.00 + 900.00 = 11400.00; 6010 debit 2450.51 + 300.00 = 2750.51; 6020 debit 2899.99 + 400.00 = 3299.99. Totals debit 11500.00 + 1500.00 + 2750.51 + 3299.99 = 19050.50; credit 4500.00 + 3150.50 + 11400.00 = 19050.50.
- LOC: 1000 debit 11500.00, credit 4112.00 + 400.00 = 4512.00; 2100 credit 2859.08 + 50.00 + 300.00 = 3209.08; 4000 credit 10500.00 + 900.00 + 100.00 = 11500.00; 7010 debit 2525.76 + 300.00 = 2825.76; 7020 debit 2111.99 + 400.00 + 50.00 = 2561.99; 6020 debit 833.33. Totals debit 11500.00 + 1500.00 + 833.33 + 2825.76 + 2561.99 = 19221.08; credit 4512.00 + 3209.08 + 11500.00 = 19221.08.
- TAX: 1000 debit 10510.00 + 1000.00 = 11510.00, credit 4100.00 + 400.00 + 300.00 = 4800.00; 2100 credit 2790.50 + 100.00 = 2890.50; 4000 credit 10510.00 + 900.00 = 11410.00; 6010 debit 4590.50 + 400.00 + 300.00 = 5290.50; 6020 debit 800.00. Totals debit 11510.00 + 1500.00 + 5290.50 + 800.00 = 19100.50; credit 4800.00 + 2890.50 + 11410.00 = 19100.50.
