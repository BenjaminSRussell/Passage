#!/usr/bin/env python3
# Regenerates cost_reference.json: 100 receipt/issue sequences, each costed under all
# seven methods with exact rational arithmetic (no shared code with the Go engine).
# Rules: values round half-even to cents; the last unit taken from a layer or pool
# carries its remaining value. Sequences are seeded, so the output is reproducible.
import datetime
import json
import random
from fractions import Fraction

SCALE = 100
METHODS = ["FIFO", "LIFO", "LIFO_DATE", "WEIGHTED_AVG_PERIODIC", "WEIGHTED_AVG_RUNNING", "MOVING_AVG", "STANDARD"]


def terminating(den):
    for p in (2, 5):
        while den % p == 0:
            den //= p
    return den == 1


def rnd(x):
    n = x * SCALE
    f = n.numerator // n.denominator
    r = n - f
    if r > Fraction(1, 2) or (r == Fraction(1, 2) and f % 2 == 1):
        f += 1
    return Fraction(f, SCALE)


class Tie(Exception):
    pass


def rnd_checked(x, unit):
    if (x * SCALE).denominator == 2 and not terminating(unit.denominator):
        raise Tie()
    return rnd(x)


def month(t):
    return t["date"][:7]


def run_layered(txns, method):
    layers = [
        {"t": t, "qty": Fraction(t["qty"]), "value": Fraction(t["value"]), "unit": Fraction(t["value"]) / Fraction(t["qty"])}
        for t in txns if t["kind"] == "R"
    ]
    if method == "LIFO":
        visible = lambda t: [l for l in layers if month(l["t"]) <= month(t)]
    else:
        visible = lambda t: [l for l in layers if l["t"]["date"] <= t["date"]]
    costs = {}
    for t in txns:
        if t["kind"] != "I":
            continue
        pool = [l for l in visible(t) if l["qty"] > 0]
        pool.sort(key=lambda l: l["t"]["date"], reverse=(method != "FIFO"))
        need, total = Fraction(t["qty"]), Fraction(0)
        for l in pool:
            if need == 0:
                break
            take = min(need, l["qty"])
            v = l["value"] if take == l["qty"] else rnd_checked(take * l["unit"], l["unit"])
            l["qty"] -= take
            l["value"] -= v
            total += v
            need -= take
        assert need == 0
        costs[t["id"]] = total
    return costs, sum((l["qty"] for l in layers), Fraction(0)), sum((l["value"] for l in layers), Fraction(0))


def run_pool(txns, method, std):
    costs, qty, val = {}, Fraction(0), Fraction(0)
    months = sorted({month(t) for t in txns})
    if method == "WEIGHTED_AVG_PERIODIC":
        for m in months:
            for t in txns:
                if month(t) == m and t["kind"] == "R":
                    qty += Fraction(t["qty"])
                    val += Fraction(t["value"])
            for t in txns:
                if month(t) == m and t["kind"] == "I":
                    take = Fraction(t["qty"])
                    v = val if take == qty else rnd_checked(take * val / qty, val / qty)
                    qty, val = qty - take, val - v
                    costs[t["id"]] = v
        return costs, qty, val
    for t in txns:
        if t["kind"] == "R":
            qty += Fraction(t["qty"])
            val += rnd(Fraction(t["qty"]) * std) if method == "STANDARD" else Fraction(t["value"])
        else:
            take = Fraction(t["qty"])
            if take == qty:
                v = val
            elif method == "STANDARD":
                v = rnd_checked(take * std, std)
            else:
                v = rnd_checked(take * val / qty, val / qty)
            qty, val = qty - take, val - v
            costs[t["id"]] = v
    return costs, qty, val


def fmt(x):
    assert (x * SCALE).denominator == 1
    n = int(x * SCALE)
    sign = "-" if n < 0 else ""
    return f"{sign}{abs(n) // SCALE}.{abs(n) % SCALE:02d}"


def build(rng, n):
    for _ in range(100):
        txns, onhand, day = [], 0, 0
        std = Fraction(rng.randint(500, 2500), 100)
        for k in range(rng.randint(5, 12)):
            day += rng.randint(1, 20)
            date = (datetime.date(2026, 1, 1) + datetime.timedelta(days=day)).isoformat()
            if not txns or (onhand < 10 and rng.random() < 0.9) or rng.random() < 0.45:
                q = rng.randint(1, 120)
                unit = Fraction(rng.randint(100, 9999), 100)
                txns.append({"id": f"T{k:02d}", "kind": "R", "qty": str(q), "value": fmt(rnd(q * unit + Fraction(rng.randint(-99, 99), 100))), "date": date})
                onhand += q
            else:
                q = rng.randint(1, onhand)
                txns.append({"id": f"T{k:02d}", "kind": "I", "qty": str(q), "date": date})
                onhand -= q
        if not any(t["kind"] == "I" for t in txns):
            continue
        out = {}
        try:
            for m in METHODS:
                if m in ("FIFO", "LIFO", "LIFO_DATE"):
                    c, q, v = run_layered(txns, m)
                else:
                    c, q, v = run_pool(txns, m, std)
                out[m] = {"issues": {k: fmt(x) for k, x in c.items()}, "remainingQty": str(q), "remainingValue": fmt(v)}
        except (Tie, AssertionError):
            continue
        return {"id": f"SEQ-{n:03d}", "standardUnitCost": fmt(std), "transactions": txns, "expected": out}
    raise SystemExit("no sequence")


def main():
    rng = random.Random(20)
    seqs = [
        {
            "id": "SEQ-001",
            "note": "FR-20.1: 100@10.00 (Jan 5) + 100@12.00 (Jan 20), issue 150 on Jan 25: FIFO 1600.00, LIFO 1700.00, average 1650.00",
            "standardUnitCost": "11.00",
            "transactions": [
                {"id": "R1", "kind": "R", "qty": "100", "value": "1000.00", "date": "2026-01-05"},
                {"id": "R2", "kind": "R", "qty": "100", "value": "1200.00", "date": "2026-01-20"},
                {"id": "I1", "kind": "I", "qty": "150", "date": "2026-01-25"},
            ],
        },
        {
            "id": "SEQ-002",
            "note": "3 units for 10.00, issue 1 then 1 then 1: 3.33 + 3.33 + remainder 3.34 under every method but STANDARD (std 3.00: 3.00 each)",
            "standardUnitCost": "3.00",
            "transactions": [
                {"id": "R1", "kind": "R", "qty": "3", "value": "10.00", "date": "2026-02-01"},
                {"id": "I1", "kind": "I", "qty": "1", "date": "2026-02-02"},
                {"id": "I2", "kind": "I", "qty": "1", "date": "2026-02-03"},
                {"id": "I3", "kind": "I", "qty": "1", "date": "2026-02-04"},
            ],
        },
    ]
    for s in seqs:
        std, out = Fraction(s["standardUnitCost"]), {}
        for m in METHODS:
            if m in ("FIFO", "LIFO", "LIFO_DATE"):
                c, q, v = run_layered(s["transactions"], m)
            else:
                c, q, v = run_pool(s["transactions"], m, std)
            out[m] = {"issues": {k: fmt(x) for k, x in c.items()}, "remainingQty": str(q), "remainingValue": fmt(v)}
        s["expected"] = out
    while len(seqs) < 100:
        seqs.append(build(rng, len(seqs) + 1))
    with open("cost_reference.json", "w") as f:
        json.dump({"scale": 2, "sequences": seqs}, f, indent=1)
        f.write("\n")


main()
