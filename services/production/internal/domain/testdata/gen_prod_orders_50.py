"""Generates prod-orders-50.json, the AC2 settlement fixture (PRD 4.21.9).

The expectations are computed here, independently of the Go code, with exact
rational arithmetic straight from the BR-21.6 definitions:

  r = RAF quantity / order quantity
  matched row (planned and consumed), std = planned amount / planned qty:
    lot-size              = fixed × (1 − r)
    production-quantity   = (actual qty − r × planned qty) × std − lot-size
    production-price      = actual amount − actual qty × std
  planned row not consumed: production-substitution −= r × planned amount
  consumed row not planned: production-substitution += actual amount
  WIP before End          = Σ actual − r × Σ planned

Order 1 is the BR-21.7 worked example. Run: python3 gen_prod_orders_50.py
"""
import json
import random
from fractions import Fraction as F
from pathlib import Path

GROUPS = ["material", "labor", "overhead"]


def half_away(x):
    """Rounds a Fraction to an integer, halves away from zero (as shopspring/decimal Round)."""
    n = int(abs(x) + F(1, 2))
    return -n if x < 0 else n


def money(x, places):
    q = F(10) ** places
    return F(half_away(x * q), q)


def fmt(x, places):
    neg = x < 0
    x = abs(x)
    q = 10**places
    n = half_away(x * q)
    s = f"{n // q}.{n % q:0{places}d}" if places else str(n)
    return ("-" if neg and n else "") + s


def expected(order_qty, raf_qty, planned, actual):
    r = F(raf_qty) / F(order_qty)
    act = {(a["group"], a["key"]): a for a in actual}
    var = {}

    def add(t, g, v):
        var[f"{t}/{g}"] = var.get(f"{t}/{g}", F(0)) + v

    matched = set()
    for p in planned:
        k = (p["group"], p["key"])
        pq, pa, pf = F(p["qty"]), F(p["amount"]), F(p["fixed"])
        if k not in act or pq <= 0:
            add("production-substitution", p["group"], -r * pa)
            continue
        matched.add(k)
        a = act[k]
        aq, aa = F(a["qty"]), F(a["amount"])
        std = pa / pq
        lot = pf * (1 - r)
        add("lot-size", p["group"], lot)
        add("production-quantity", p["group"], (aq - r * pq) * std - lot)
        add("production-price", p["group"], aa - aq * std)
    for a in actual:
        if (a["group"], a["key"]) not in matched:
            add("production-substitution", a["group"], F(a["amount"]))
    wip = sum(F(a["amount"]) for a in actual) - r * sum(F(p["amount"]) for p in planned)
    return wip, var


def row(group, key, qty, amount, fixed=F(0)):
    return {"group": group, "key": key, "qty": fmt(qty, 6), "amount": fmt(amount, 4), "fixed": fmt(fixed, 4)}


def br217():
    planned = [row("material", "RM-1", F(300), F(3000)), row("labor", "WC-1@10", F(50), F(1500)), row("overhead", "WC-1@10", F(50), F(500))]
    actual = [row("material", "RM-1", F(305), F(3111)), row("labor", "WC-1@10", F(46), F(1380)), row("overhead", "WC-1@10", F(46), F(460))]
    return {"name": "BR-21.7 FG-100", "orderQty": "100", "rafQty": "100", "planned": planned, "actual": actual}


def generated(rng, n):
    order_qty = rng.choice([10, 25, 40, 100, 120, 250, 500])
    raf_qty = rng.choice([0, order_qty // 2, (order_qty * 3) // 5, order_qty, order_qty])
    r = F(raf_qty, order_qty)
    planned, actual = [], []
    for i in range(rng.randint(1, 3)):
        key = f"RM-{n}-{i}"
        per = F(rng.randint(5, 400), 100)
        cost = F(rng.randint(50, 5000), 100)
        pq = per * order_qty
        planned.append(row("material", key, pq, money(pq * cost, 4)))
        if rng.random() < 0.85:
            aq = money(pq * r * F(rng.randint(90, 115), 100), 2)
            actual.append(row("material", key, aq, money(aq * cost * F(rng.randint(95, 108), 100), 2)))
    for op in range(rng.randint(1, 2)):
        key = f"WC-{n}@{(op + 1) * 10}"
        setup = F(rng.randint(0, 40), 10)
        run = F(rng.randint(1, 50), 100)
        hours = setup + run * order_qty
        for group, rate in (("labor", F(rng.randint(2000, 6000), 100)), ("overhead", F(rng.randint(500, 2000), 100))):
            planned.append(row(group, key, hours, money(hours * rate, 4), money(setup * rate, 4)))
            if raf_qty or rng.random() < 0.5:
                ah = money((setup + run * order_qty * r) * F(rng.randint(85, 120), 100), 2)
                actual.append(row(group, key, ah, money(ah * rate * F(rng.randint(97, 104), 100), 2)))
    if rng.random() < 0.2:
        aq = F(rng.randint(1, 50))
        actual.append(row("material", f"SUB-{n}", aq, money(aq * F(rng.randint(100, 900), 100), 2)))
    return {"name": f"generated order {n}", "orderQty": str(order_qty), "rafQty": str(raf_qty), "planned": planned, "actual": actual}


def main():
    rng = random.Random(21)
    orders = [br217()] + [generated(rng, n) for n in range(2, 51)]
    for o in orders:
        wip, var = expected(int(o["orderQty"]), int(o["rafQty"]), o["planned"], o["actual"])
        o["expected"] = {
            "wipBefore": fmt(money(wip, 2), 2),
            "wipExact": fmt(wip, 6),
            "variancesExact": {k: fmt(v, 6) for k, v in sorted(var.items()) if v != 0},
        }
    out = Path(__file__).with_name("prod-orders-50.json")
    out.write_text(json.dumps({"fixture": "prod-orders-50", "orders": orders}, indent=1) + "\n")


if __name__ == "__main__":
    main()
