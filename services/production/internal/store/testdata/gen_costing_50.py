"""Generates costing-50.json, the AC2 fixture run through the database (Wave 21).

Fifty production orders with known consumptions. Each one is created from a
BOM and a route, started, picked, fed back with hours, reported as finished
(fully, partly or not at all) and ended by store_test code; this script states,
independently of the Go code and with exact rational arithmetic, what every
step must post:

  required qty      = ceil6(qty per × order qty ÷ series × (1 + scrap/100)), or ceil to a unit (round up)   BR-21.2
  process qty       = backward over the route: q_n = ceil6(q_n+1 ÷ (1 − scrap_n/100))                     BR-21.8
  planned material  = round4(Σ required × unit cost) per item; labor and overhead = round4(hours × rate),
                      hours = setup + run × process qty; fixed = round4(setup × rate)                      FR-21.3
  issue amount      = round2(qty × unit cost) per picking line (unit cost overridden or the BOM's)         FR-21.5
  hours amounts     = round2(hours × labor rate), round2(hours × overhead rate)                          FR-21.6
  RAF receipt       = round2(planned total × RAF qty ÷ order qty)                                         FR-21.7
  WIP before End    = Σ issues + Σ labor + Σ overhead − receipt
  variances         = BR-21.6 over planned and actual rows, exact (r = RAF qty ÷ order qty):
                      lot-size = fixed × (1 − r); quantity = (actual qty − r × planned qty) × std − lot-size;
                      price = actual amount − actual qty × std; planned not consumed: substitution −r × planned

Rounding is half away from zero (shopspring/decimal Round). The mix is drawn
from a seeded generator and is not tuned to any threshold; the summary printed
at the end (and stored in the JSON) says what it contains.

Run: python3 -I gen_costing_50.py
"""
import json
import random
from fractions import Fraction as F
from pathlib import Path


def half_away(x):
    n = int(abs(x) + F(1, 2))
    return -n if x < 0 else n


def rnd(x, places):
    q = F(10) ** places
    return F(half_away(x * q), q)


def ceil_to(x, places):
    q = F(10) ** places
    n = x * q
    i = n.numerator // n.denominator
    if i * n.denominator != n.numerator:
        i += 1
    return F(i, q)


def fmt(x, places=6):
    neg = x < 0
    q = 10**places
    n = half_away(abs(x) * q)
    s = f"{n // q}.{n % q:0{places}d}" if places else str(n)
    return ("-" if neg and n else "") + s


def dec(x):
    """Exact decimal string of a Fraction whose denominator divides a power of ten."""
    for places in range(0, 13):
        if (x * 10**places).denominator == 1:
            return fmt(x, places)
    raise ValueError(f"{x} is not a finite decimal")


def settle(order_qty, raf_qty, planned, actual):
    r = F(raf_qty) / F(order_qty)
    act = {(a["group"], a["key"]): a for a in actual}
    var = {}

    def add(t, g, v):
        var[f"{t}/{g}"] = var.get(f"{t}/{g}", F(0)) + v

    matched = set()
    for p in planned:
        k = (p["group"], p["key"])
        if k not in act or p["qty"] <= 0:
            add("production-substitution", p["group"], -r * p["amount"])
            continue
        matched.add(k)
        a = act[k]
        std = p["amount"] / p["qty"]
        lot = p["fixed"] * (1 - r)
        add("lot-size", p["group"], lot)
        add("production-quantity", p["group"], (a["qty"] - r * p["qty"]) * std - lot)
        add("production-price", p["group"], a["amount"] - a["qty"] * std)
    for a in actual:
        if (a["group"], a["key"]) not in matched:
            add("production-substitution", a["group"], a["amount"])
    return var


def generate(rng, n, summary):
    order_qty = rng.choice([1, 7, 10, 25, 40, 100, 120, 150, 250, 333, 500])
    mode = rng.choice(["full", "full", "partial", "partial", "zero"])
    if mode == "partial" and order_qty == 1:
        mode = "full"
    raf = {"full": order_qty, "partial": rng.randint(1, max(order_qty - 1, 1)), "zero": 0}[mode]
    summary[f"raf {mode}"] += 1
    r = F(raf, order_qty)
    # Zero-output orders still consumed something before they were abandoned.
    r_used = r if raf else F(rng.randint(10, 90), 100)

    lines, items = [], []
    for i in range(rng.randint(1, 3)):
        if items and rng.random() < 0.15:
            item = rng.choice(items)
            summary["item on two lines"] += 1
        else:
            item = f"RM-{n}-{i}"
            items.append(item)
        qty_per = F(rng.randint(1, 4000), 1000)
        series = rng.choice([1, 1, 1, 3, 10, 12])
        scrap = rng.choice([F(0), F(0), F(2), F(5), F(25, 2), F(20)])
        round_up = rng.random() < 0.15
        unit_cost = F(rng.randint(500, 500000), 10000)
        exact = qty_per * order_qty / series * (1 + scrap / 100)
        req = ceil_to(exact, 0) if round_up else ceil_to(exact, 6)
        summary["series not 1"] += series != 1
        summary["round up"] += round_up
        summary["scrap"] += scrap != 0
        lines.append({"lineNo": str((i + 1) * 10), "item": item, "qtyPer": dec(qty_per), "series": str(series), "scrap": dec(scrap),
                      "roundUp": round_up, "unitCost": dec(unit_cost), "expectedRequired": fmt(req, 6),
                      "_req": req, "_cost": unit_cost})

    ops = []
    for k in range(rng.randint(1, 2)):
        ops.append({"oper": (k + 1) * 10, "resource": f"WC-{n}-{k}", "setup": dec(F(rng.randint(0, 40), 10)),
                    "run": dec(F(rng.randint(1, 500), 10000)), "scrap": dec(F(rng.choice([0, 0, 0, 5, 10]))),
                    "labor": dec(F(rng.randint(2000, 6000), 100)), "overhead": dec(F(rng.randint(500, 2000), 100))})
    q = F(order_qty)
    for op in reversed(ops):
        q = ceil_to(q / (1 - F(op["scrap"]) / 100), 6)
        op["expectedProcessQty"] = fmt(q, 6)
        op["_pq"] = q
        summary["operation scrap"] += F(op["scrap"]) != 0

    # Planned cost (estimate).
    planned, by_item = [], {}
    for ln in lines:
        by_item.setdefault(ln["item"], [F(0), F(0)])
        by_item[ln["item"]][0] += ln["_req"]
        by_item[ln["item"]][1] += ln["_req"] * ln["_cost"]
    for item, (qty, amount) in by_item.items():
        planned.append({"group": "material", "key": item, "qty": qty, "amount": rnd(amount, 4), "fixed": F(0)})
    for op in ops:
        setup, run = F(op["setup"]), F(op["run"])
        hours = setup + run * op["_pq"]
        for group, rate in (("labor", F(op["labor"])), ("overhead", F(op["overhead"]))):
            planned.append({"group": group, "key": f"{op['resource']}@{op['oper']}", "qty": hours,
                            "amount": rnd(hours * rate, 4), "fixed": rnd(setup * rate, 4)})
    planned_total = sum(p["amount"] for p in planned)

    # Actual consumption.
    picks, actual_mat = [], {}
    for ln in lines:
        if rng.random() < 0.1:
            summary["line not picked"] += 1
            continue
        qty = rnd(ln["_req"] * r_used * F(rng.randint(85, 115), 100), 3)
        if qty <= 0:
            summary["line not picked"] += 1
            continue
        cost = ln["_cost"]
        override = None
        if rng.random() < 0.4:
            cost = rnd(cost * F(rng.randint(90, 112), 100), 4)
            override = dec(cost)
            summary["issue at another unit cost"] += 1
        amount = rnd(qty * cost, 2)
        picks.append({"lineNo": ln["lineNo"], "qty": dec(qty), "unitCost": override, "expectedAmount": fmt(amount, 2)})
        a = actual_mat.setdefault(ln["item"], {"group": "material", "key": ln["item"], "qty": F(0), "amount": F(0)})
        a["qty"] += qty
        a["amount"] += amount
    hours_in, actual_ops = [], []
    for op in ops:
        if rng.random() < 0.1:
            summary["operation without hours"] += 1
            continue
        h = rnd((F(op["setup"]) + F(op["run"]) * op["_pq"] * r_used) * F(rng.randint(80, 125), 100), 2)
        h = max(h, F(1, 100))
        labor, overhead = rnd(h * F(op["labor"]), 2), rnd(h * F(op["overhead"]), 2)
        hours_in.append({"oper": op["oper"], "hours": dec(h), "expectedLabor": fmt(labor, 2), "expectedOverhead": fmt(overhead, 2)})
        key = f"{op['resource']}@{op['oper']}"
        actual_ops.append({"group": "labor", "key": key, "qty": h, "amount": labor})
        actual_ops.append({"group": "overhead", "key": key, "qty": h, "amount": overhead})
    if not picks and not hours_in:
        # An order with nothing posted settles nothing; keep at least one issue.
        ln = lines[0]
        qty = max(rnd(ln["_req"] * r_used, 3), F(1, 1000))
        amount = rnd(qty * ln["_cost"], 2)
        picks.append({"lineNo": ln["lineNo"], "qty": dec(qty), "unitCost": None, "expectedAmount": fmt(amount, 2)})
        actual_mat[ln["item"]] = {"group": "material", "key": ln["item"], "qty": qty, "amount": amount}
    actual = list(actual_mat.values()) + actual_ops

    issues = sum(F(p["expectedAmount"]) for p in picks)
    labor = sum(F(h["expectedLabor"]) for h in hours_in)
    overhead = sum(F(h["expectedOverhead"]) for h in hours_in)
    receipt = rnd(planned_total * raf / order_qty, 2)
    wip = issues + labor + overhead - receipt
    var = settle(order_qty, raf, planned, actual)
    by_group = {}
    for p in planned:
        by_group[p["group"]] = by_group.get(p["group"], F(0)) + p["amount"]

    for ln in lines:
        del ln["_req"], ln["_cost"]
    for op in ops:
        del op["_pq"]
    return {
        "name": f"order {n} ({mode}, qty {order_qty})", "orderQty": str(order_qty), "rafQty": str(raf),
        "lines": lines, "operations": ops, "picks": picks, "hours": hours_in,
        "expected": {
            "plannedByGroup": {g: fmt(v, 4) for g, v in sorted(by_group.items())},
            "receipt": fmt(receipt, 2), "wipBefore": fmt(wip, 2),
            "accounts": {"1400": fmt(-issues, 2), "1410": fmt(receipt, 2), "5510": fmt(-labor, 2), "5520": fmt(-overhead, 2)},
            "variancesExact": {k: fmt(v, 6) for k, v in sorted(var.items()) if v != 0},
        },
    }


def main():
    rng = random.Random(2121)
    keys = ["raf full", "raf partial", "raf zero", "item on two lines", "series not 1", "round up", "scrap", "operation scrap",
            "line not picked", "issue at another unit cost", "operation without hours"]
    summary = {k: 0 for k in keys}
    orders = [generate(rng, n, summary) for n in range(1, 51)]
    out = Path(__file__).with_name("costing-50.json")
    out.write_text(json.dumps({"fixture": "costing-50", "summary": summary, "orders": orders}, indent=1) + "\n")
    print(json.dumps(summary))


if __name__ == "__main__":
    main()
