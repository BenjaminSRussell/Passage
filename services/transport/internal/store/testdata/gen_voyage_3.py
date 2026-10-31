"""Independent reference for the Wave 34 landed-cost fixture voyage-3.json (issue #34).

Run with `python3 -I gen_voyage_3.py [out.json]` (default: voyage-3.json next to this
file). Three voyages (containers, folios, cost lines that stand for the forwarder,
customs and insurer invoices, receipt lines) and, per cost line, the share of every
receipt line. Nothing here calls the Go engine; the rules come from the PRD:

  * BR-34.6: share_i = round(cost * basis_i / sum(basis), 2), half up; residual =
    cost - sum(shares), added to the line with the highest value (ties: lowest line
    number). Bases: WEIGHT, VOLUME, VALUE, QUANTITY, or 1 per line for EQUAL;
  * a receipt line's landed cost is the sum of its shares over the cost lines;
  * BR-34.12: container numbers carry the ISO 6346 check digit (letters A = 10,
    B = 12 ... skipping multiples of 11, weights 2^position, sum mod 11 mod 10).

Exact rational arithmetic throughout. The voyages are adversarial on purpose: a
three-way 1/3 split (AC-34.6), a negative residual (0.02 over three equal lines),
lines with a zero basis, ties for the highest value, a zero-amount cost line, odd
amounts such as 9,999.99 over 7 lines, and a 300-line voyage whose residuals
accumulate on one line. The generator refuses a voyage whose shares do not sum to
its cost lines (the property under test), and refuses negative shares (BR-34.6 would
give one when the residual line's own share is 0.00 and the residual is negative;
the PRD does not say what should happen then, so the fixture does not assert it).
"""

from __future__ import annotations

import json
import random
import string
import sys
from fractions import Fraction as F
from pathlib import Path


def r2(x: F) -> F:
    assert x >= 0
    n = x * 100
    w = n.numerator // n.denominator
    if n - w >= F(1, 2):
        w += 1
    return F(w, 100)


def money(x: F) -> str:
    cents = x * 100
    assert cents.denominator == 1, x
    c = int(cents)
    sign = "-" if c < 0 else ""
    c = abs(c)
    return f"{sign}{c // 100}.{c % 100:02d}"


LETTERS = {}
_v = 10
for _ch in string.ascii_uppercase:
    if _v % 11 == 0:
        _v += 1
    LETTERS[_ch] = _v
    _v += 1
assert LETTERS["A"] == 10 and LETTERS["B"] == 12 and LETTERS["K"] == 21 and LETTERS["L"] == 23 and LETTERS["U"] == 32 and LETTERS["V"] == 34


def container(owner: str, serial: int) -> str:
    body = f"{owner}U{serial:06d}"
    total = sum((LETTERS[c] if c.isalpha() else int(c)) * 2**i for i, c in enumerate(body))
    return body + str(total % 11 % 10)


assert container("CSQ", 305438) == "CSQU3054383"  # BR-34.12 example


def basis(method: str, line) -> F:
    return F(1) if method == "EQUAL" else F(line[method.lower()])


def allocate(cost: F, method: str, lines):
    total = sum(basis(method, l) for l in lines)
    assert total > 0
    shares = [r2(cost * basis(method, l) / total) for l in lines]
    target = min(range(len(lines)), key=lambda i: (-F(lines[i]["value"]), lines[i]["line"]))
    residual = cost - sum(shares)
    shares[target] += residual
    if any(s < 0 for s in shares):
        raise SystemExit(f"negative share allocating {cost} by {method}")
    assert sum(shares) == cost
    return shares, residual, lines[target]["line"]


def line(n, value, weight, volume, quantity):
    return {"line": n, "value": value, "weight": weight, "volume": volume, "quantity": quantity}


def voyages():
    rng = random.Random(3406)
    v1_lines = [line(1, "100", "1", "0.5", "10"), line(2, "100", "1", "0.5", "10"), line(3, "250", "1", "0.5", "10")]
    v2_lines = [line(1, "18250.00", "2140.5", "11.25", "240"), line(2, "18250.00", "0", "0", "12"),
                line(3, "730.40", "96.25", "0.84", "6"), line(4, "4410.10", "511", "3.2", "60"),
                line(5, "12.99", "0.4", "0.002", "1"), line(6, "9800.00", "1320.75", "7.7", "100"),
                line(7, "18249.99", "2140.5", "11.25", "240")]
    v3_lines = []
    for n in range(1, 301):
        v3_lines.append(line(n, f"{rng.randint(100, 2_500_000) / 100:.2f}", f"{rng.randint(0, 900_000) / 1000:.3f}",
                             f"{rng.randint(1, 40_000) / 10_000:.4f}", str(rng.randint(1, 500))))
    v3_lines[41]["value"] = v3_lines[207]["value"] = "25000.00"  # tie for the residual line: 42 beats 208
    return [
        {"reference": "MAEU-2611E", "currency": "USD",
         "legs": [{"from": "CNSHA", "to": "USLAX", "mode": "OCEAN"}, {"from": "USLAX", "to": "USCHI", "mode": "RAIL"}],
         "containers": [{"number": container("CSQ", 305438), "type": "40HC", "seal": "778812"}],
         "folios": [{"folioNo": "F-1", "forwarder": "Kuehne", "containers": [container("CSQ", 305438)]}],
         "costLines": [{"costType": "FREIGHT", "amount": "1000.00", "allocationMethod": "WEIGHT"},
                       {"costType": "DUTY", "amount": "45.55", "allocationMethod": "VALUE"},
                       {"costType": "BROKERAGE", "amount": "0.02", "allocationMethod": "EQUAL"},
                       {"costType": "INSURANCE", "amount": "0.01", "allocationMethod": "QUANTITY"}],
         "receiptLines": v1_lines},
        {"reference": "HLCU-0457W", "currency": "EUR",
         "legs": [{"from": "CNNGB", "to": "NLRTM", "mode": "OCEAN"}, {"from": "NLRTM", "to": "DEDUS", "mode": "ROAD"}],
         "containers": [{"number": container("HLX", 102030), "type": "40GP"}, {"number": container("HLX", 102031), "type": "20GP"},
                        {"number": container("TGH", 998877), "type": "REEFER", "seal": "S-0091"}],
         "folios": [{"folioNo": "F-NGB-1", "forwarder": "DSV", "containers": [container("HLX", 102030), container("HLX", 102031)]},
                    {"folioNo": "F-NGB-2", "forwarder": "Hapag", "containers": [container("TGH", 998877)]}],
         "costLines": [{"costType": "FREIGHT", "amount": "9999.99", "allocationMethod": "VOLUME"},
                       {"costType": "DUTY", "amount": "4321.09", "allocationMethod": "VALUE"},
                       {"costType": "HANDLING", "amount": "777.77", "allocationMethod": "WEIGHT"},
                       {"costType": "DRAYAGE", "amount": "333.33", "allocationMethod": "QUANTITY"},
                       {"costType": "INSPECTION", "amount": "0.00", "allocationMethod": "EQUAL"},
                       {"costType": "DOCS", "amount": "100.00", "allocationMethod": "EQUAL"}],
         "receiptLines": v2_lines},
        {"reference": "ONEY-1204E", "currency": "USD",
         "legs": [{"from": "JPTYO", "to": "USSEA", "mode": "OCEAN"}],
         "containers": [{"number": container("ONE", 400000 + k), "type": "45HC"} for k in range(12)],
         "folios": [{"folioNo": f"F-{k // 4 + 1}", "forwarder": "Nippon Express",
                     "containers": [container("ONE", 400000 + j) for j in range(k, k + 4)]} for k in (0, 4, 8)],
         "costLines": [{"costType": "FREIGHT", "amount": "48250.17", "allocationMethod": "WEIGHT"},
                       {"costType": "DUTY", "amount": "31877.03", "allocationMethod": "VALUE"},
                       {"costType": "INSURANCE", "amount": "1234.56", "allocationMethod": "VALUE"},
                       {"costType": "HANDLING", "amount": "2999.99", "allocationMethod": "VOLUME"},
                       {"costType": "DRAYAGE", "amount": "1500.01", "allocationMethod": "QUANTITY"},
                       {"costType": "DOCS", "amount": "299.99", "allocationMethod": "EQUAL"}],
         "receiptLines": v3_lines},
    ]


def main() -> None:
    target = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).with_name("voyage-3.json")
    # AC-34.6 worked by hand: 1,000.00 by weight over 1, 1, 1 kg.
    shares, residual, at = allocate(F(1000), "WEIGHT", voyages()[0]["receiptLines"])
    assert [money(s) for s in shares] == ["333.33", "333.33", "333.34"] and money(residual) == "0.01" and at == 3
    out, negatives = [], 0
    for v in voyages():
        totals = [F(0)] * len(v["receiptLines"])
        allocs = []
        for c in v["costLines"]:
            shares, residual, at = allocate(F(c["amount"]), c["allocationMethod"], v["receiptLines"])
            negatives += residual < 0
            totals = [t + s for t, s in zip(totals, shares)]
            allocs.append({"costType": c["costType"], "shares": [money(s) for s in shares], "residual": money(residual), "residualLine": at})
        cost_total = sum(F(c["amount"]) for c in v["costLines"])
        assert sum(totals) == cost_total
        out.append({"voyage": v, "expected": {"costTotal": money(cost_total), "costAllocations": allocs,
                                              "lineTotals": [money(t) for t in totals]}})
    assert negatives > 0, "the fixture lost its negative-residual case"
    target.write_text(json.dumps({"generator": "gen_voyage_3.py", "voyages": out}, separators=(",", ":")) + "\n")


if __name__ == "__main__":
    main()
