"""Independent reference for the cogs-matrix-14 fixture (PRD section 5.28.9).

Run with: python3 -I testdata/gen_cogs_matrix14.py testdata/cogs-matrix-14.json

It shares no code with the Go engine. The 14 cells are the seven slip scenarios
below times the two packing-slip posting policies of BR-28.1 (physical posting:
Dr 1405 delivered-not-invoiced / Cr 1400; cogs_at_shipment: Dr 5000 / Cr 1400).

Rules taken from the PRD, not from the engine:
- BR-28.1: a stocked line posts its cost; a drop-ship line and a non-stocked or
  service line post nothing at the packing slip; a standard-cost item posts at
  standard; a catch-weight item posts on its inventory quantity, never on its
  catch weight.
- Each line amount is quantity x unit cost rounded once to the currency's ISO
  4217 minor unit, half away from zero (Python ROUND_HALF_UP on Decimal is half
  away from zero for negative values too). A line that rounds to zero posts
  nothing.
- Cancellation (FR-28.5) posts the mirror: same account, order line and
  currency, amount negated, so every account nets to zero.

The expected journal is written as signed amounts (debit positive) and also as
the ledger PostEntryRequest lines the ledger API accepts (services/ledger
api/openapi.yaml PostLine: side D or C, a non-negative amount with at most the
currency's minor-unit scale), for the post and for the cancellation.
"""

import json
import sys
from decimal import Decimal, ROUND_HALF_UP

MINOR = {"USD": 2, "EUR": 2, "JPY": 0, "KWD": 3}

# Seven slip scenarios. Lines: (order line, kind, inventory qty, unit cost[, catch weight]).
SCENARIOS = [
    ("stocked-running-average", "AC-28.1: 10 units at the running-average snapshot 12.50 USD", "USD", [
        ("L1", "STOCKED", "10", "12.50"),
    ]),
    ("stocked-fifo-estimate", "FIFO estimate snapshots with six decimals; 3 x 0.835 = 2.505 rounds to 2.51 "
     "(half away from zero; half-even would give 2.50)", "USD", [
        ("L1", "STOCKED", "7", "1.642857"),
        ("L2", "STOCKED", "3", "0.835"),
        ("L3", "STOCKED", "0.5", "19.99"),
    ]),
    ("standard-cost", "Standard-cost item at its standard 3.333 EUR; no variance at shipment", "EUR", [
        ("L1", "STOCKED", "4", "3.333"),
        ("L2", "STOCKED", "6", "3.333"),
    ]),
    ("drop-ship", "Drop-ship line: no posting at the packing slip (COGS at vendor confirmation)", "USD", [
        ("L1", "DROP_SHIP", "5", "9.00"),
    ]),
    ("non-stocked-service", "Non-stocked and service lines: no posting", "USD", [
        ("L1", "NON_STOCKED", "1", "100.00"),
        ("L2", "NON_STOCKED", "2.5", "40.00"),
    ]),
    ("catch-weight", "Catch-weight item: 12 EA weighing 13.7 kg costs 12 x 4.12345 KWD on the inventory "
     "quantity (49.481), not on the weight", "KWD", [
        ("L1", "STOCKED", "12", "4.12345", "13.7"),
    ]),
    ("mixed-slip-zero-decimals", "One slip in JPY mixing stocked, drop-ship, service and zero-cost lines; "
     "1 x 0.4 JPY rounds to 0 and posts nothing, 3 x 333.5 = 1000.5 rounds to 1001", "JPY", [
        ("L1", "STOCKED", "3", "333.5"),
        ("L2", "DROP_SHIP", "2", "500"),
        ("L3", "NON_STOCKED", "1", "1200"),
        ("L4", "STOCKED", "4", "0"),
        ("L5", "STOCKED", "1", "0.4"),
        ("L6", "STOCKED", "2", "0.5"),
    ]),
]

POLICIES = [("physical", False, "1405"), ("cogs-at-shipment", True, "5000")]


def amount(qty, unit, currency):
    exp = Decimal(1).scaleb(-MINOR[currency])
    return (Decimal(qty) * Decimal(unit)).quantize(exp, rounding=ROUND_HALF_UP)


def fmt(d, currency):
    return str(d.quantize(Decimal(1).scaleb(-MINOR[currency])))


def ledger_lines(journal, currency):
    out = []
    for j in journal:
        v = Decimal(j["amount"])
        out.append({"account": j["account"], "debitCredit": "D" if v > 0 else "C",
                    "amount": fmt(abs(v), currency), "sourceLineRef": j["orderLineId"]})
    return out


def main():
    cells = []
    for name, note, currency, lines in SCENARIOS:
        for policy, cogs, debit in POLICIES:
            journal = []
            for line in lines:
                ol, kind, qty, unit = line[:4]
                if kind != "STOCKED":
                    continue
                a = amount(qty, unit, currency)
                if a == 0:
                    continue
                journal.append({"account": debit, "amount": fmt(a, currency), "orderLineId": ol})
                journal.append({"account": "1400", "amount": fmt(-a, currency), "orderLineId": ol})
            mirror = [{"account": j["account"], "amount": fmt(-Decimal(j["amount"]), currency),
                       "orderLineId": j["orderLineId"]} for j in journal]
            total = sum((-Decimal(j["amount"]) for j in journal if j["account"] == "1400"), Decimal(0))
            debits = sum((Decimal(j["amount"]) for j in journal if Decimal(j["amount"]) > 0), Decimal(0))
            cells.append({
                "id": "%s/%s" % (name, policy),
                "scenario": name,
                "note": note,
                "policy": policy,
                "cogsAtShipment": cogs,
                "currency": currency,
                "lines": [{"orderLineId": l[0], "kind": l[1], "qty": l[2], "unitCost": l[3],
                           "catchWeightKg": l[4] if len(l) > 4 else ""} for l in lines],
                "journal": journal,
                "mirror": mirror,
                "totalCost": fmt(total, currency),
                "ledgerPost": ledger_lines(journal, currency),
                "ledgerCancel": ledger_lines(mirror, currency),
                "ledgerTotal": fmt(debits, currency),
            })
    assert len(cells) == 14
    # Hand-derived anchors (worked on paper, not by this script's arithmetic).
    by_id = {c["id"]: c for c in cells}
    assert by_id["stocked-running-average/physical"]["journal"] == [
        {"account": "1405", "amount": "125.00", "orderLineId": "L1"},
        {"account": "1400", "amount": "-125.00", "orderLineId": "L1"}]
    assert [j["amount"] for j in by_id["stocked-fifo-estimate/physical"]["journal"]] == [
        "11.50", "-11.50", "2.51", "-2.51", "10.00", "-10.00"]  # 11.499999, 2.505, 9.995
    assert by_id["standard-cost/cogs-at-shipment"]["totalCost"] == "33.33"  # 13.332 -> 13.33, 19.998 -> 20.00
    assert by_id["drop-ship/physical"]["journal"] == [] and by_id["non-stocked-service/cogs-at-shipment"]["journal"] == []
    assert by_id["catch-weight/physical"]["totalCost"] == "49.481"  # 12 x 4.12345 = 49.4814
    assert [j["amount"] for j in by_id["mixed-slip-zero-decimals/cogs-at-shipment"]["journal"]] == [
        "1001", "-1001", "1", "-1"]  # 1000.5 -> 1001; 0.4 -> 0 (none); 2 x 0.5 = 1
    doc = {"fixture": "cogs-matrix-14", "source": "testdata/gen_cogs_matrix14.py (python3 -I), independent of the Go engine",
           "cells": cells}
    with open(sys.argv[1], "w", encoding="ascii") as f:
        json.dump(doc, f, indent=1, sort_keys=True)
        f.write("\n")


if __name__ == "__main__":
    main()
