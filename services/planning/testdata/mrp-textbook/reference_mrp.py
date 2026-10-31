"""Independent reference for the mrp-textbook fixtures (issue #22, AC1).

A plain bucketed MRP record as taught for APICS CPIM, written from the
textbook definitions and the PRD rules, not from the Go engine:

  projected = PAB(t-1) + SR(t) - GR(t)
  net requirement NR(t) = SS - projected when projected < SS
  L4L  : order NR(t)
  FOQ  : order the smallest multiple of the FOQ that covers NR(t)
  POQ  : order the sum of the net requirements of periods t .. t+P-1, i.e. the
         largest cumulative shortfall below SS inside the window
  MINMAX: when projected < min, order max - projected
  then the modifiers: at least min, rounded up to mult, split by max.
  receipt = t - safety lead time, release = receipt - lead time.
  Dependent demand = qty per x parent order, at the parent's release period;
  a past-due release (period < 1) is a requirement of period 1.
  Items are planned in low-level-code order so a component sees every parent.

PRD rule BR-22.5 (policy today): a safety-stock planned order is dated today
+ lead time. A period-1 net requirement is a safety-stock order when on-hand
plus period-1 receipts cover the period-1 gross requirement, so only the
safety stock is short; its release is period 1 and its receipt 1 + lead time.
Netting is unchanged (the quantity serves the safety stock from period 1).

Futures and delay (BR-22.1 step 7, FR-22.7, BR-22.2 "Propagation"): a
past-due release cannot be placed before today, so the order is expected in
period 1 + lead time (its futures period); its delay is futures - receipt.
The futures date propagates upward through the pegging: a component order
that is available (futures + safety lead time) after the parent release it
is pegged to lets that parent start no earlier, so the parent is expected at
that period + the parent's lead time when that is later than its receipt.
Pegging is FIFO (FR-22.6): every item's supply in period order (on-hand
first, then a scheduled receipt before a planned receipt of the same period)
serves its demand in period order, ties by priority: independent demand,
then safety stock (a requirement of period 1), then dependent demand.

Usage: python3 -I reference_mrp.py scenarios.json expected.json
Exits 1 when a computed plan differs from the hand-derived literals.
"""

import json
import sys
from fractions import Fraction


def ceil_to(q, step):
    n = q / step
    whole = n.numerator // n.denominator
    if whole * n.denominator != n.numerator:
        whole += 1
    return whole * step


def modifiers(q, it):
    mn = Fraction(it.get("min", 0))
    mult = Fraction(it.get("mult", 0))
    mx = Fraction(it.get("max", 0))
    if mn > 0 and q < mn:
        q = mn
    if mult > 0:
        q = ceil_to(q, mult)
    if mx <= 0 or q <= mx:
        return [q]
    lots = []
    while q > mx:
        lots.append(mx)
        q -= mx
    lots.append(q)
    return lots


def low_level_codes(items):
    llc = {i: 0 for i in items}
    changed = True
    while changed:
        changed = False
        for pid, it in items.items():
            for line in it.get("bom", []):
                if llc[line["item"]] < llc[pid] + 1:
                    llc[line["item"]] = llc[pid] + 1
                    changed = True
    return llc


def plan(scn, br225):
    items = {it["id"]: it for it in scn["items"]}
    gross = {i: {} for i in items}
    # Demand lines for pegging: (period, priority, seq, qty, parent order or None).
    lines = {i: [] for i in items}
    for i, it in items.items():
        for p, q in it.get("gross", {}).items():
            gross[i][int(p)] = gross[i].get(int(p), 0) + Fraction(q)
            lines[i].append((int(p), 1, 0, Fraction(q), None))
        if Fraction(it.get("ss", 0)) > 0:
            lines[i].append((1, 3, 0, Fraction(it["ss"]), None))
    llc = low_level_codes(items)
    orders = []
    for iid in sorted(items, key=lambda i: (llc[i], i)):
        it = items[iid]
        sr = {int(p): Fraction(q) for p, q in it.get("sr", {}).items()}
        g = gross[iid]
        lt, slt = it.get("lt", 0), it.get("slt", 0)
        ss = Fraction(it.get("ss", 0))
        rule = it["lot"]
        last = max([1] + list(g) + list(sr))
        pab = Fraction(it.get("onHand", 0))
        for t in range(1, last + 1):
            projected = pab + sr.get(t, 0) - g.get(t, 0)
            lots = []
            if rule == "MINMAX":
                if projected < Fraction(it["reorderMin"]):
                    lots = modifiers(Fraction(it["reorderMax"]) - projected, it)
            elif projected < ss:
                nr = ss - projected
                if rule == "FOQ":
                    nr = ceil_to(nr, Fraction(it["foq"]))
                elif rule == "POQ":
                    cum = pab
                    for k in range(t, t + it["poq"]):
                        cum += sr.get(k, 0) - g.get(k, 0)
                        nr = max(nr, ss - cum)
                elif rule != "L4L":
                    raise ValueError(rule)
                lots = modifiers(nr, it)
            pab = projected + sum(lots)
            receipt = t - slt
            release = receipt - lt
            if br225 and t == 1 and lots and rule != "MINMAX" and projected >= 0:
                release, receipt = 1, 1 + lt
            for q in lots:
                n = len(orders)
                futures = 1 + lt if release < 1 else None
                orders.append([iid, q, release, receipt, futures])
                for line in it.get("bom", []):
                    c = line["item"]
                    p = max(release, 1)
                    gross[c][p] = gross[c].get(p, 0) + Fraction(line["per"]) * q
                    lines[c].append((p, 4, n, Fraction(line["per"]) * q, n))
    propagate(items, llc, orders, lines)
    return sorted((tuple(o) for o in orders), key=lambda o: (o[0], o[3], o[2], o[1], o[4] or 0))


def propagate(items, llc, orders, lines):
    """Pegs each item's supply FIFO to its demand lines and moves the futures
    period of every parent whose component order is available too late."""
    for iid in sorted(items, key=lambda i: (-llc[i], i)):
        it = items[iid]
        supply = [(0, 0, 0, Fraction(it.get("onHand", 0)), None)]
        supply += [(int(p), 1, 0, Fraction(q), None) for p, q in it.get("sr", {}).items()]
        supply += [(o[3], 2, n, o[1], n) for n, o in enumerate(orders) if o[0] == iid]
        supply = [list(s) for s in sorted(supply, key=lambda s: s[:3])]
        si = 0
        for period, _, _, qty, parent in sorted(lines[iid], key=lambda d: d[:3]):
            while qty > 0 and si < len(supply):
                take = min(qty, supply[si][3])
                qty -= take
                supply[si][3] -= take
                own = supply[si][4]
                if take > 0 and own is not None and parent is not None and orders[own][4] is not None:
                    ready = orders[own][4] + it.get("slt", 0)
                    if ready > period:
                        p = orders[parent]
                        delivery = ready + items[p[0]].get("lt", 0)
                        if delivery > p[3] and (p[4] is None or delivery > p[4]):
                            p[4] = delivery
                if supply[si][3] == 0:
                    si += 1


def as_json(orders):
    out = []
    for iid, q, rel, rec, futures in orders:
        if q.denominator != 1:
            raise ValueError("non-integer quantity in a fixture")
        row = {"item": iid, "qty": str(q.numerator), "releasePeriod": rel, "receiptPeriod": rec}
        if futures is not None:
            row["futuresPeriod"], row["delayPeriods"] = futures, futures - rec
        out.append(row)
    return out


def from_hand(rows, futures_rows):
    """Hand rows [item, qty, release, receipt]; futures_rows give the futures
    period of the orders that have one as [item, qty, release, receipt, futures]."""
    futures = {}
    for r in futures_rows:
        futures.setdefault(tuple(r[:4]), []).append(r[4])
    out = []
    for r in rows:
        f = futures.get(tuple(r[:4]))
        out.append((r[0], Fraction(r[1]), r[2], r[3], f.pop(0) if f else None))
    if any(futures.values()):
        raise ValueError(f"futures rows without a planned order: {futures}")
    return sorted(out, key=lambda o: (o[0], o[3], o[2], o[1], o[4] or 0))


def main(src, dst):
    with open(src) as f:
        doc = json.load(f)
    failures = 0
    expected = {}
    for scn in doc["scenarios"]:
        textbook = plan(scn, br225=False)
        prd = plan(scn, br225=True)
        want = from_hand(scn["hand"], scn.get("handFutures", []))
        if textbook != want:
            print(f"{scn['id']}: textbook {textbook} != hand {want}")
            failures += 1
        want_prd = from_hand(scn.get("prdHand", scn["hand"]), scn.get("prdHandFutures", scn.get("handFutures", [])))
        if prd != want_prd:
            print(f"{scn['id']}: prd {prd} != hand {want_prd}")
            failures += 1
        expected[scn["id"]] = {"textbook": as_json(textbook), "prd": as_json(prd)}
    if failures:
        sys.exit(1)
    with open(dst, "w") as f:
        json.dump({"generatedBy": "reference_mrp.py", "scenarios": expected}, f, indent=1, sort_keys=True)
        f.write("\n")
    print(f"{len(expected)} scenarios match the hand-derived plans")


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
