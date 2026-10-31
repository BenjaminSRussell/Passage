"""Independent reference for the Wave 34 freight reconciliation fixture (issue #34).

Run with `python3 -I gen_carrier_invoices_50.py [out.json]` (default:
carrier-invoices-50.json next to this file). It writes 50 carrier invoices, the
freight bills they name, and what reconciliation must do with each. Nothing here
calls the Go engine; the rules are coded from the PRD (BR-34.4, FR-34.7 notes:
"tolerance default $5 / 2% per charge") and README "Business rules" in exact
decimal arithmetic:

  * an invoice that names no bill is stored as UNEXPECTED_INVOICE (never matched);
  * an invoice from another carrier, or in another currency, than its bill is
    refused with TMS-0112; a second invoice on an invoiced bill with TMS-0111; a
    carrier's invoice number used twice with TMS-0108. A refused invoice leaves the
    bill as it was;
  * otherwise, per charge code (amounts of one code summed on each side, a code on
    one side only compared with zero): variance = invoiced - expected; the charge is
    within tolerance when |variance| <= min($5.00, 2 % of expected), both limits
    exact (no rounding of the 2 %); the bill is MATCHED when every charge is within,
    else VARIANCE; the variance total is the sum of the per-code variances. Codes
    are listed in the bill's order, then new invoice codes in invoice order.

The first 33 cases are hand-designed edge cases (each limit exactly, one cent and
one millionth past it, the 2.469 allowance that cent rounding would lift to 2.47,
offsetting variances, split lines, zero-amount codes, refusals, unexpected
invoices); the other 17 are seeded random bills and invoices with realistic noise.
The variance count is whatever these produce; it is reported, not targeted.
"""

from __future__ import annotations

import json
import random
import sys
from decimal import Decimal as D
from pathlib import Path

ABS = D("5.00")
PCT = D("2")


def norm(d: D) -> str:
    s = format(d.normalize(), "f")
    return "0" if s in ("-0", "0") else s


def sum_code(lines, code) -> D:
    return sum((D(a) for c, a in lines if c == code), D(0))


def match(expected, invoiced):
    codes = []
    for c, _ in expected + invoiced:
        if c not in codes:
            codes.append(c)
    status, total, out = "MATCHED", D(0), []
    for c in codes:
        e, i = sum_code(expected, c), sum_code(invoiced, c)
        v = i - e
        allowed = min(ABS, e * PCT / 100)
        within = abs(v) <= allowed
        if not within:
            status = "VARIANCE"
        total += v
        out.append({"chargeCode": c, "expected": norm(e), "invoiced": norm(i), "variance": norm(v), "within": within})
    return status, norm(total), out


def L(*pairs):
    return [[c, a] for c, a in pairs]


def hand_cases():
    """(id, bill or None, invoice, billOf) with bill = {carrier, currency, lines}."""
    def bill(lines, carrier="A", currency="USD"):
        return {"carrier": carrier, "currency": currency, "lines": lines}

    def inv(no, lines, carrier="A", currency="USD", source="EDI210"):
        return {"carrier": carrier, "invoiceNo": no, "currency": currency, "source": source, "lines": lines}

    one = L(("BASE", "412.50"))
    three = L(("BASE", "820.00"), ("FUEL", "151.70"), ("ACC:LIFTGATE", "25.00"))
    return [
        ("H01 exact single line", bill(one), inv("A-1001", one), None),
        ("H02 exact three lines", bill(three), inv("A-1002", three), None),
        ("H03 +5.00 on 412.50: $5 limit, inclusive", bill(one), inv("A-1003", L(("BASE", "417.50"))), None),
        ("H04 +5.01 on 412.50", bill(one), inv("A-1004", L(("BASE", "417.51"))), None),
        ("H05 AC-34.4 +5.50 on 412.50", bill(one), inv("A-1005", L(("BASE", "418.00"))), None),
        ("H06 -5.00 on 300.00", bill(L(("BASE", "300.00"))), inv("A-1006", L(("BASE", "295.00"))), None),
        ("H07 -5.01 on 300.00", bill(L(("BASE", "300.00"))), inv("A-1007", L(("BASE", "294.99"))), None),
        ("H08 +0.20 on 10.00: 2 % limit, inclusive", bill(L(("FUEL", "10.00"))), inv("A-1008", L(("FUEL", "10.20"))), None),
        ("H09 +0.21 on 10.00", bill(L(("FUEL", "10.00"))), inv("A-1009", L(("FUEL", "10.21"))), None),
        ("H10 +2.47 on 123.45: 2 % is 2.469", bill(L(("BASE", "123.45"))), inv("A-1010", L(("BASE", "125.92"))), None),
        ("H11 +2.46 on 123.45", bill(L(("BASE", "123.45"))), inv("A-1011", L(("BASE", "125.91"))), None),
        ("H12 +5.00 on 250.00: both limits are 5.00", bill(L(("BASE", "250.00"))), inv("A-1012", L(("BASE", "255.00"))), None),
        ("H13 +1.999999 on 100.00 (sub-cent invoice)", bill(L(("BASE", "100.00"))), inv("A-1013", L(("BASE", "101.999999"))), None),
        ("H14 +2.000001 on 100.00", bill(L(("BASE", "100.00"))), inv("A-1014", L(("BASE", "102.000001"))), None),
        ("H15 extra DETENTION 0.00 is within", bill(L(("BASE", "100.00"))), inv("A-1015", L(("BASE", "100.00"), ("DETENTION", "0.00"))), None),
        ("H16 extra DETENTION 40.00 against zero", bill(L(("BASE", "100.00"))), inv("A-1016", L(("BASE", "100.00"), ("DETENTION", "40.00"))), None),
        ("H17 FUEL missing on the invoice", bill(L(("BASE", "100.00"), ("FUEL", "8.00"))), inv("A-1017", L(("BASE", "100.00"))), None),
        ("H18 BASE split over two invoice lines", bill(L(("BASE", "640.00"), ("FUEL", "96.00"))),
         inv("A-1018", L(("BASE", "400.00"), ("FUEL", "96.00"), ("BASE", "240.00"))), None),
        ("H19 two charges each within, total +8.00", bill(L(("BASE", "500.00"), ("ACC:INSIDE", "300.00"))),
         inv("A-1019", L(("BASE", "504.00"), ("ACC:INSIDE", "304.00"))), None),
        ("H20 offsetting +6 / -6 nets to zero", bill(L(("BASE", "900.00"), ("FUEL", "400.00"))),
         inv("A-1020", L(("BASE", "906.00"), ("FUEL", "394.00"))), None),
        ("H21 0.01 on a zero expected charge", bill(L(("BASE", "75.00"), ("ACC:APPT", "0.00"))),
         inv("A-1021", L(("BASE", "75.00"), ("ACC:APPT", "0.01"))), None),
        ("H22 undercharge within 2 % on a small charge", bill(L(("ACC:RESI", "4.65"))), inv("A-1022", L(("ACC:RESI", "4.56"))), None),
        ("H23 undercharge 0.10 on 4.65 (2 % is 0.093)", bill(L(("ACC:RESI", "4.65"))), inv("A-1023", L(("ACC:RESI", "4.55"))), None),
        ("H24 EUR bill and EUR invoice", bill(L(("BASE", "1180.00"), ("FUEL", "212.40")), carrier="B", currency="EUR"),
         inv("B-77", L(("BASE", "1184.99"), ("FUEL", "212.40")), carrier="B", currency="EUR"), None),
        ("H25 no bill: UNEXPECTED_INVOICE", None, inv("A-1025", L(("BASE", "310.00"))), None),
        ("H26 no bill, manual entry", None, inv("C-9", L(("BASE", "88.10"), ("FUEL", "12.33")), carrier="C", source="MANUAL"), None),
        ("H27 another carrier's bill: TMS-0112", bill(L(("BASE", "200.00"))), inv("C-10", L(("BASE", "200.00")), carrier="C"), None),
        ("H28 right carrier then matches the same bill", None, inv("A-1028", L(("BASE", "200.00"))), "H27 another carrier's bill: TMS-0112"),
        ("H29 EUR invoice on a USD bill: TMS-0112", bill(L(("BASE", "640.00"))), inv("A-1029", L(("BASE", "640.00")), currency="EUR"), None),
        ("H30 second invoice on an invoiced bill: TMS-0111", None, inv("A-1030", L(("BASE", "412.50"))), "H01 exact single line"),
        ("H31 invoice number reused by its carrier: TMS-0108", bill(L(("BASE", "55.00"))), inv("A-1001", L(("BASE", "55.00"))), None),
        ("H32 same number from another carrier is fine", bill(L(("BASE", "55.00")), carrier="B"), inv("A-1001", L(("BASE", "55.00")), carrier="B", source="API"), None),
        ("H33 the H31 bill takes a fresh number", None, inv("A-1033", L(("BASE", "55.00"))), "H31 invoice number reused by its carrier: TMS-0108"),
    ]


def random_cases(n):
    rng = random.Random(3403)
    out = []
    for k in range(n):
        carrier = rng.choice("ABC")
        base = D(rng.randint(15000, 300000)) / 100
        lines = [("BASE", base), ("FUEL", (base * D(rng.choice(["18.5", "21.25", "9.75", "22"])) / 100).quantize(D("0.01")))]
        for code in rng.sample(["ACC:LIFTGATE", "ACC:RESI", "ACC:INSIDE", "ACC:TARP", "ACC:APPT"], rng.choice([0, 0, 1, 2])):
            lines.append((code, D(rng.choice(["25.00", "4.65", "40.50", "100.00", "15.00"]))))
        billed = [[c, str(a)] for c, a in lines]
        invoiced = []
        for c, a in lines:
            roll = rng.random()
            if roll < 0.7:
                delta = D(0)
            elif roll < 0.85:
                delta = D(rng.randint(-300, 300)) / 100  # small noise, may or may not be within
            else:
                delta = D(rng.randint(-2500, 2500)) / 100  # a real dispute
            invoiced.append([c, str(max(D(0), a + delta))])
        if rng.random() < 0.15:
            invoiced.append(["ACC:DETENTION", str(D(rng.choice([75, 150, 225])))])
        out.append((f"R{k + 1:02d} random", {"carrier": carrier, "currency": "USD", "lines": billed},
                    {"carrier": carrier, "invoiceNo": f"{carrier}-R{k + 1:03d}", "currency": "USD",
                     "source": rng.choice(["EDI210", "API"]), "lines": invoiced}, None))
    return out


def main() -> None:
    target = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).with_name("carrier-invoices-50.json")
    cases = hand_cases() + random_cases(17)
    assert len(cases) == 50
    bills: dict[str, dict] = {}  # case id -> bill with its invoiced state
    numbers: set[tuple[str, str]] = set()
    out, counts = [], {}
    for cid, bill, inv, bill_of in cases:
        target_bill = bill if bill is not None else (bills[bill_of] if bill_of else None)
        if bill is not None:
            bills[cid] = bill = dict(bill, invoiced=False)
            target_bill = bill
        exp = {}
        if (inv["carrier"], inv["invoiceNo"]) in numbers:
            exp = {"error": "TMS-0108"}
        elif target_bill is None:
            exp = {"status": "UNEXPECTED_INVOICE"}
        elif target_bill["invoiced"]:
            exp = {"error": "TMS-0111"}
        elif target_bill["carrier"] != inv["carrier"] or target_bill["currency"] != inv["currency"]:
            exp = {"error": "TMS-0112"}
        else:
            status, total, lines = match(target_bill["lines"], inv["lines"])
            exp = {"status": status, "varianceTotal": total, "lines": lines}
            target_bill["invoiced"] = True
        if "error" not in exp:
            numbers.add((inv["carrier"], inv["invoiceNo"]))
        key = exp.get("status") or exp["error"]
        counts[key] = counts.get(key, 0) + 1
        out.append({"id": cid, "bill": None if bill is None else {k: bill[k] for k in ("carrier", "currency", "lines")},
                    "billOf": bill_of, "invoice": inv, "expected": exp})
    # Outcomes worked by hand before the reference was trusted with the rest.
    hand = {"H01": ("MATCHED", "0"), "H03": ("MATCHED", "5"), "H04": ("VARIANCE", "5.01"), "H05": ("VARIANCE", "5.5"),
            "H06": ("MATCHED", "-5"), "H07": ("VARIANCE", "-5.01"), "H08": ("MATCHED", "0.2"), "H09": ("VARIANCE", "0.21"),
            "H10": ("VARIANCE", "2.47"), "H11": ("MATCHED", "2.46"), "H12": ("MATCHED", "5"), "H13": ("MATCHED", "1.999999"),
            "H14": ("VARIANCE", "2.000001"), "H15": ("MATCHED", "0"), "H16": ("VARIANCE", "40"), "H17": ("VARIANCE", "-8"),
            "H18": ("MATCHED", "0"), "H19": ("MATCHED", "8"), "H20": ("VARIANCE", "0"), "H21": ("VARIANCE", "0.01"),
            "H22": ("MATCHED", "-0.09"), "H23": ("VARIANCE", "-0.1"), "H24": ("MATCHED", "4.99"), "H28": ("MATCHED", "0")}
    for c in out:
        want = hand.get(c["id"][:3])
        if want and (c["expected"].get("status"), c["expected"].get("varianceTotal")) != want:
            raise SystemExit(f"{c['id']}: reference gives {c['expected']}, hand-worked {want}")
    errors = {"H27": "TMS-0112", "H29": "TMS-0112", "H30": "TMS-0111", "H31": "TMS-0108"}
    for c in out:
        if c["id"][:3] in errors and c["expected"].get("error") != errors[c["id"][:3]]:
            raise SystemExit(f"{c['id']}: reference gives {c['expected']}")
    lines = ['{"generator":"gen_carrier_invoices_50.py",', '"counts":' + json.dumps(counts, sort_keys=True, separators=(",", ":")) + ",", '"cases":[']
    lines += [json.dumps(c, separators=(",", ":")) + ("," if i < len(out) - 1 else "") for i, c in enumerate(out)]
    lines += ["]}"]
    target.write_text("\n".join(lines) + "\n")


if __name__ == "__main__":
    main()
