#!/usr/bin/env python3
# Regenerates inv-fefo-100.json, the PRD 3.16 fixture `inv-fefo-100` for the Wave 16
# proof "FEFO picking test: reservation selects earliest-expiry batch across 100
# batches" (AC-16.4).
#
# Independent of the Go engine (no shared code). The rules are read from the PRD,
# not from the implementation:
#   FR-16.7  FEFO orders candidate batches by expiry_date ASC, receipt_date ASC;
#            batches with status BLOCKED or QUARANTINE, or with
#            expiry_date < required_date + shelf_life_margin_days, are excluded.
#   BR-16.7  equal expiry -> earliest receipt_date -> lowest batch_id (lexical,
#            byte order); batches with no expiry sort last.
#   FR-16.20 an unsegmented demand never takes segment-dedicated stock.
#   Reserved stock is not available (avail_physical = physical - reserved).
#   The receipt date of a stock row is its earliest receipt.
#   The dimension filter (site S1) leaves stock at other sites out.
#
# The fixture is adversarial rather than a clean grid: expiry ties broken by
# receipt date and by lexical batch id ("LOT-100" < "LOT-11" < "LOT-9", upper
# case before lower case), a batch expiring exactly on the cutoff (eligible) and
# one the day before (excluded), never-expiring batches, a batch whose expiry row
# exists but is null, an unbatched row, fully and partly blocked or quarantined
# batches, fully and partly pre-reserved batches, batches with a second, later
# receipt (the earliest receipt date must win), fractional quantities, and decoys
# with the earliest expiries at another site and in a sales-order segment.
#
# Four FEFO reservations run in sequence against the same stock, each seeing what
# the previous ones took. Expected allocations are listed in pick order.
# Seeded, so the output is reproducible:
#   python3 -I gen_fefo_100.py > inv-fefo-100.json
import json
import random
from datetime import date, timedelta
from decimal import ROUND_FLOOR, Decimal as D

rng = random.Random(1607)
REQUIRED = date(2027, 3, 1)


def day(n):
    return (REQUIRED + timedelta(days=n)).isoformat()


def qty():
    # 0.5 to 12.0 in steps of 0.25
    return D(rng.randint(2, 48)) / 4


def quarter(x):
    # largest multiple of 0.25 not above x
    return (x * 4).to_integral_value(rounding=ROUND_FLOOR) / 4


rows = []
# Batch ids are LOT-<n> with n drawn without replacement from 1..140, so lexical
# order differs from numeric order; four are lower case to test byte order.
numbers = rng.sample(range(1, 141), 100)
names = ["LOT-%d" % n for n in numbers]
for i in rng.sample(range(100), 4):
    names[i] = names[i].lower()

# Expiry offsets (days after the required date). Thirty batches share expiry
# with at least one other (ties of 2 and 3), the rest are distinct.
offsets = rng.sample(range(-40, 400), 76)
ties = []
for k in range(12):  # 12 pairs
    ties += [offsets[k]] * 2
for k in range(12, 14):  # 2 triples
    ties += [offsets[k]] * 3
expiries = [day(o) for o in ties + offsets[14:]]  # 24 + 6 + 62 = 92
expiries += [None] * 6  # never expire (no batch_expiry row)
expiries += ["NULL_ROW"] * 2  # batch_expiry row with a null date
assert len(expiries) == 100
rng.shuffle(expiries)

receipt_days = [day(-rng.randint(30, 400)) for _ in range(100)]
# Make some ties share the receipt date too, so the lexical batch id decides.
by_expiry = {}
for i, e in enumerate(expiries):
    if e not in (None, "NULL_ROW"):
        by_expiry.setdefault(e, []).append(i)
groups = [g for g in by_expiry.values() if len(g) > 1]
for g in groups[:6]:
    for i in g[1:]:
        receipt_days[i] = receipt_days[g[0]]

for i in range(100):
    rows.append({
        "batch": names[i],
        "site": "S1",
        "warehouse": rng.choice(["W1", "W2", "W3"]),
        "location": "L%02d" % rng.randint(1, 40),
        "qty": qty(),
        "receiptDate": receipt_days[i],
        "laterReceipt": None,
        "expiry": expiries[i],
        "block": None,
        "reservedBefore": D(0),
        "segment": None,
    })

# Shelf-life cutoff edges for the first reservation (margin 7): one batch expires
# exactly on REQUIRED + 7 (eligible), one on REQUIRED + 6 (excluded).
rows[0]["expiry"], rows[1]["expiry"] = day(7), day(6)

# Ten batches blocked or quarantined in full (the PRD fixture's "10 blocked"),
# chosen among the earliest eligible expiries so the exclusion matters.
dated = sorted((i for i in range(2, 100) if rows[i]["expiry"] not in (None, "NULL_ROW") and rows[i]["expiry"] >= day(7)),
               key=lambda i: rows[i]["expiry"])
for n, i in enumerate(dated[:10]):
    rows[i]["block"] = {"qty": rows[i]["qty"], "target": "BLOCKED" if n % 3 else "QUARANTINE"}
# Five more partly blocked: the rest of the batch stays eligible.
for i in dated[10:15]:
    part = quarter(rows[i]["qty"] / 2)
    if part > 0:
        rows[i]["block"] = {"qty": part, "target": "BLOCKED"}
# Pre-reserved by a MANUAL reservation: three in full, nine in part.
for n, i in enumerate(dated[15:27]):
    rows[i]["reservedBefore"] = rows[i]["qty"] if n < 3 else quarter(rows[i]["qty"] / 4)
# Eight batches received twice; the second receipt is later and must not move
# the receipt date.
for i in rng.sample(range(2, 100), 8):
    rows[i]["laterReceipt"] = {"qty": qty(), "date": day(-rng.randint(1, 29))}

# One unbatched row at S1 (no batch id: no expiry, lowest id among no-expiry).
rows.append({"batch": "", "site": "S1", "warehouse": "W1", "location": "L99", "qty": D("6.5"),
             "receiptDate": day(-200), "laterReceipt": None, "expiry": None, "block": None,
             "reservedBefore": D(0), "segment": None})
# Decoys: earliest expiries, but at site S2 or dedicated to a sales order.
for k in range(5):
    rows.append({"batch": "S2-LOT-%d" % k, "site": "S2", "warehouse": "W1", "location": "L01", "qty": D(20),
                 "receiptDate": day(-500), "laterReceipt": None, "expiry": day(8 + k), "block": None,
                 "reservedBefore": D(0), "segment": None})
for k in range(3):
    rows.append({"batch": "SEG-LOT-%d" % k, "site": "S1", "warehouse": "W2", "location": "L02", "qty": D(20),
                 "receiptDate": day(-500), "laterReceipt": None, "expiry": day(8 + k), "block": None,
                 "reservedBefore": D(0), "segment": "SALES_ORDER:SO-DEDICATED"})

reservations = [
    {"qty": D(50), "marginDays": 7, "allowPartial": False},      # AC-16.4: 50 units
    {"qty": D("37.5"), "marginDays": 60, "allowPartial": False},  # a longer shelf-life margin
    {"qty": D(100000), "marginDays": 0, "allowPartial": True},     # drains every eligible batch
    {"qty": D(1), "marginDays": 0, "allowPartial": False},         # nothing left: INV-RES-002
]


def available(r):
    a = r["qty"] - r["reservedBefore"]
    if r["laterReceipt"]:
        a += r["laterReceipt"]["qty"]
    if r["block"]:
        a -= r["block"]["qty"]
    return a


avail = [available(r) for r in rows]
expected = []
for res in reservations:
    cutoff = (REQUIRED + timedelta(days=res["marginDays"])).isoformat()
    cands = []
    for i, r in enumerate(rows):
        exp = r["expiry"] if r["expiry"] != "NULL_ROW" else None
        if r["site"] != "S1" or r["segment"] or avail[i] <= 0:
            continue
        if exp is not None and exp < cutoff:
            continue
        cands.append((exp is None, exp or "", r["receiptDate"], r["batch"].encode(), i))
    cands.sort()
    left, picks = res["qty"], []
    for *_, i in cands:
        if left <= 0:
            break
        take = min(avail[i], left)
        picks.append({"batch": rows[i]["batch"], "qty": str(take)})
        avail[i] -= take
        left -= take
    if not picks:
        expected.append({"code": "INV-RES-002", "allocations": [], "shortfall": str(res["qty"])})
    elif left > 0 and not res["allowPartial"]:
        raise SystemExit("fixture reservation %s cannot be met without allowPartial" % res)
    else:
        expected.append({"code": "", "allocations": picks, "shortfall": str(left)})


def out_row(r):
    o = dict(r)
    o["qty"] = str(r["qty"])
    o["reservedBefore"] = str(r["reservedBefore"])
    if r["block"]:
        o["block"] = {"qty": str(r["block"]["qty"]), "target": r["block"]["target"]}
    if r["laterReceipt"]:
        o["laterReceipt"] = {"qty": str(r["laterReceipt"]["qty"]), "date": r["laterReceipt"]["date"]}
    if r["expiry"] == "NULL_ROW":
        o["expiry"], o["expiryRowNull"] = None, True
    else:
        o["expiryRowNull"] = False
    return o


print(json.dumps({
    "requiredDate": REQUIRED.isoformat(),
    "filterSite": "S1",
    "rows": [out_row(r) for r in rows],
    "reservations": [{"qty": str(r["qty"]), "marginDays": r["marginDays"], "allowPartial": r["allowPartial"]} for r in reservations],
    "expected": expected,
}, indent=1))
