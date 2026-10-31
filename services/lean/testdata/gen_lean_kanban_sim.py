#!/usr/bin/env python3
# Regenerates lean-kanban-sim.json (PRD 4.24.9 fixture `lean-kanban-sim`: sales
# lines -> event kanbans -> board completion) for the Wave 24 proof "Kanban
# simulation: event kanbans are generated from a sales order and complete
# through the board".
#
# Independent of the Go engine (no shared code, exact rationals). Rule BR-24.8
# and FR-24.11 (docs/prd/phase-4-manufacturing.md): a sales line of demand D on
# an event rule with fixed quantity F creates ceil(D / F) cards. Each card holds
# F (the last card is full size, overproduction allowed) unless the rule is
# exact, in which case the last card holds D - F x (n - 1). Completing every
# card through the board receives the sum of the card quantities.
#
# One sales order, SO-24, with eleven lines over three event rules. The lines
# are adversarial: the AC-24.6 example, exact multiples (no extra card), one
# micro-unit over and under a multiple, a micro-unit demand, an exact rule
# whose last card is one micro-unit, and a repeating fixed quantity whose
# multiple lands exactly on the demand.
# Run: python3 -I gen_lean_kanban_sim.py > lean-kanban-sim.json
import json
from fractions import Fraction as F

P = 6

RULES = [
    {"code": "SIM-FILL20", "fixedQty": "20", "exactQty": False},
    {"code": "SIM-EXACT7_5", "fixedQty": "7.5", "exactQty": True},
    {"code": "SIM-THIRD", "fixedQty": "3.333333", "exactQty": False},
]

LINES = [
    ("SO-24/10", "SIM-FILL20", "50", "AC-24.6: 50 on fixed 20 makes 3 cards and receives 60"),
    ("SO-24/20", "SIM-FILL20", "40", "exact multiple makes no extra card"),
    ("SO-24/30", "SIM-FILL20", "40.000001", "one micro-unit over a multiple adds a full card"),
    ("SO-24/40", "SIM-FILL20", "19.999999", "one micro-unit under the fixed quantity"),
    ("SO-24/50", "SIM-FILL20", "0.000001", "a micro-unit demand still makes a full card"),
    ("SO-24/60", "SIM-EXACT7_5", "50", "exact rule: six of 7.5 and a last card of 5"),
    ("SO-24/70", "SIM-EXACT7_5", "7.5", "exact rule: demand equal to the fixed quantity"),
    ("SO-24/80", "SIM-EXACT7_5", "7.500001", "exact rule: last card of one micro-unit"),
    ("SO-24/90", "SIM-EXACT7_5", "0.000001", "exact rule: a single micro-unit card"),
    ("SO-24/100", "SIM-THIRD", "100", "repeating fixed quantity: 31 cards overproduce"),
    ("SO-24/110", "SIM-THIRD", "99.99999", "30 x 3.333333 is exactly the demand"),
]


def ceil(x):
    return -((-x.numerator) // x.denominator)


def fmt(x):
    n = x * 10**P
    assert n.denominator == 1 and n > 0, x
    n = n.numerator
    s = "%d.%06d" % (n // 10**P, n % 10**P)
    return s.rstrip("0").rstrip(".")


def main():
    rules = {r["code"]: r for r in RULES}
    out_lines = []
    totals = {r["code"]: F(0) for r in RULES}
    cards_total = 0
    for ref, code, demand, note in LINES:
        r = rules[code]
        d, f = F(demand), F(r["fixedQty"])
        n = ceil(d / f)
        qtys = [f] * n
        if r["exactQty"]:
            qtys[-1] = d - f * (n - 1)
        assert all(q > 0 for q in qtys) and sum(qtys) >= d
        total = sum(qtys, F(0))
        totals[code] += total
        cards_total += n
        out_lines.append({"salesLine": ref, "rule": code, "demandQty": demand, "note": note,
                          "expectedCards": [fmt(q) for q in qtys], "expectedReceived": fmt(total)})
    print(json.dumps({"salesOrder": "SO-24", "rules": RULES, "lines": out_lines,
                      "expectedCardCount": cards_total,
                      "expectedReceivedByRule": {k: fmt(v) for k, v in totals.items()}}, indent=1))


if __name__ == "__main__":
    main()
