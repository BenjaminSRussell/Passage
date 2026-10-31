"""Generates invoices-50.json: the AC-35.2 demo set of 50 Peppol invoices and
credit notes from the demo manufacturer (a German GmbH and its Norwegian AS) to
demo distributors, and the amounts each one must carry.

Run with `python3 -I gen_peppol50.py` (stdlib only). The expected amounts are
computed here from the EN 16931 rules (BR-CO-10 to BR-CO-17, BR-S-08 and its
siblings) with decimal half-up rounding to the minor unit; nothing is taken
from the Go engine. The mix is fixed by family below, not tuned: domestic
German invoices (the Peppol DE-R national rules apply), two German rates,
document allowances and charges, intra-community supplies (K) with delivery,
reverse charge (AE), exports (G) in CHF and USD, zero-rated (Z), exempt (E),
not subject to VAT (O), cross-border consumer sales at the destination rate,
Norwegian domestic invoices in NOK with the 0192 endpoint scheme, prepaid
amounts, SEPA credit transfer, prices with up to four decimals, fractional
quantities, half-cent rounding, a 40-line invoice, and names that need XML
escaping. Every sixth document is a credit note.
"""

import json
import pathlib
import random
from decimal import Decimal, ROUND_HALF_UP

HERE = pathlib.Path(__file__).resolve().parent
CENT = Decimal("0.01")


def r2(v):
    return v.quantize(CENT, rounding=ROUND_HALF_UP)


DE_SUPPLIER = {
    "name": "Demo Manufacturer GmbH", "legalName": "Demo Manufacturer GmbH", "endpointScheme": "0088",
    "endpointId": "4006403333338", "countryCode": "DE", "vatId": "DE123456789", "legalId": "HRB 12345",
    "streetName": "Industriestraße 7", "cityName": "Köln", "postalZone": "50667",
    "contactName": "Accounts receivable", "contactPhone": "+49 221 5550100", "contactEmail": "ar@demo-manufacturer.example",
}
NO_SUPPLIER = {
    "name": "Demo Manufacturer Norge AS", "endpointScheme": "0192", "endpointId": "974760673", "countryCode": "NO",
    "vatId": "NO974760673MVA", "legalId": "974760673", "streetName": "Havnegata 3", "cityName": "Bergen", "postalZone": "5003",
}
CUSTOMERS = {
    "DE": {"name": "Demo Distributor Rhein GmbH", "endpointScheme": "0088", "endpointId": "4000001234561", "countryCode": "DE",
           "vatId": "DE987654321", "streetName": "Am Hafen 2", "cityName": "Bonn", "postalZone": "53111"},
    "DE2": {"name": "Müller & Söhne <Großhandel> KG", "endpointScheme": "0088", "endpointId": "0614141000029", "countryCode": "DE",
            "vatId": "DE811907980", "cityName": "Düsseldorf", "postalZone": "40213"},
    "FR": {"name": "Demo Distributor SAS", "endpointScheme": "0088", "endpointId": "0614141000012", "countryCode": "FR",
           "vatId": "FR12345678901", "cityName": "Lyon", "postalZone": "69002"},
    "NL": {"name": "Demo Distributeur B.V.", "endpointScheme": "0088", "endpointId": "3761042500213", "countryCode": "NL",
           "vatId": "NL123456789B01"},
    "AT": {"name": "Demo Vertrieb GmbH", "endpointScheme": "0088", "endpointId": "9012345000004", "countryCode": "AT",
           "vatId": "ATU12345678"},
    "IT": {"name": "Distributore Demo S.r.l.", "endpointScheme": "0088", "endpointId": "8012345000005", "countryCode": "IT",
           "vatId": "IT12345678901"},
    "BE": {"name": "Demo Distributie NV", "endpointScheme": "0088", "endpointId": "5412345000013", "countryCode": "BE",
           "vatId": "BE0123456749"},
    "CH": {"name": "Demo Vertrieb AG", "endpointScheme": "0088", "endpointId": "7612345000008", "countryCode": "CH"},
    "US": {"name": "Demo Distribution Inc.", "endpointScheme": "0088", "endpointId": "0012345000065", "countryCode": "US"},
    "FRC": {"name": "Jean Dupont", "endpointScheme": "0088", "endpointId": "3012345000017", "countryCode": "FR"},
    "NO": {"name": "Demo Distribusjon AS", "endpointScheme": "0192", "endpointId": "923639462", "countryCode": "NO",
           "vatId": "NO923639462MVA"},
    "NO2": {"name": "Nordlys Grossist AS", "endpointScheme": "0192", "endpointId": "984711611", "countryCode": "NO"},
}

ITEMS = [  # name, unit code, price
    ("Hydraulic pump HP-200", "C62", "1249.00"), ("Steel bracket, galvanised", "H87", "3.4750"),
    ("Sealing ring set", "SET", "18.99"), ("Copper wire 2.5 mm²", "MTR", "1.1235"), ("Lubricant ISO VG 46", "LTR", "4.105"),
    ("Polymer granulate", "KGM", "2.3333"), ("Commissioning service", "HUR", "96.50"), ("Fastener M8 (pack)", "EA", "0.0125"),
    ("Sheet metal 1 mm", "MTK", "27.45"), ("Bearing 6204-2RS", "C62", "5.6050"),
]


def gln_ok(v):
    s = sum(int(c) * (3 if i % 2 else 1) for i, c in enumerate(v[:12]))
    return len(v) == 13 and str((10 - s % 10) % 10) == v[12]


def org_ok(v):
    s = sum(w * int(c) for w, c in zip([3, 2, 7, 6, 5, 4, 3, 2], v[:8]))
    c = (11 - s % 11) % 11
    return len(v) == 9 and c < 10 and str(c) == v[8]


for party in [DE_SUPPLIER, NO_SUPPLIER, *CUSTOMERS.values()]:
    ok = gln_ok if party["endpointScheme"] == "0088" else org_ok
    assert ok(party["endpointId"]), party


def lines_for(rnd, n, rates):
    """n lines from the item list; rates is a list of (category, percent) the lines cycle through."""
    out = []
    for i in range(n):
        name, unit, price = ITEMS[rnd.randrange(len(ITEMS))]
        if unit in ("KGM", "MTR", "LTR", "HUR", "MTK"):
            qty = str(Decimal(rnd.randrange(1, 4000)) / Decimal(4))  # quarter units
        elif unit == "EA":
            qty = str(rnd.choice([1000, 2500, 10000, 12345]))
        else:
            qty = str(rnd.randrange(1, 60))
        cat, pct = rates[i % len(rates)]
        out.append({"id": str(i + 1), "name": name, "unitCode": unit, "quantity": qty, "unitPrice": price,
                    "category": cat, "percent": pct})
    return out


def expected(req):
    """EN 16931 amounts, independent of the engine."""
    lines = [r2(Decimal(l["quantity"]) * Decimal(l["unitPrice"])) for l in req["lines"]]
    breakdown = {}
    for l, amount in zip(req["lines"], lines):
        key = (l["category"], str(Decimal(l["percent"]).normalize()))
        breakdown[key] = breakdown.get(key, Decimal(0)) + amount
    allowances = charges = Decimal(0)
    for ac in req.get("allowanceCharges", []):
        amt = r2(Decimal(ac["amount"]))
        key = (ac["category"], str(Decimal(ac["percent"]).normalize()))
        if ac.get("charge"):
            charges += amt
            breakdown[key] = breakdown.get(key, Decimal(0)) + amt
        else:
            allowances += amt
            breakdown[key] = breakdown.get(key, Decimal(0)) - amt
    subtotals = []
    tax = Decimal(0)
    for (cat, pct), taxable in breakdown.items():
        t = r2(taxable * Decimal(pct) / 100)
        tax += t
        subtotals.append({"category": cat, "percent": pct, "taxable": str(r2(taxable)), "tax": str(t)})
    line_ext = sum(lines, Decimal(0))
    exclusive = line_ext - allowances + charges
    inclusive = exclusive + tax
    prepaid = r2(Decimal(req.get("prepaid") or "0"))
    return {
        "lineAmounts": [str(a) for a in lines],
        "lineExtension": str(line_ext), "allowanceTotal": str(allowances), "chargeTotal": str(charges),
        "taxExclusive": str(exclusive), "taxAmount": str(tax), "taxInclusive": str(inclusive),
        "prepaid": str(prepaid), "payable": str(inclusive - prepaid), "subtotals": subtotals,
    }


def main():
    rnd = random.Random(35)
    docs = []

    def add(family, supplier, customer, rates, *, n=None, currency="EUR", reasons=None, acs=None, prepaid=None,
            delivery=None, means=None, refs="buyer"):
        k = len(docs) + 1
        req = {
            "creditNote": k % 6 == 0,
            "id": f"{'CN' if k % 6 == 0 else 'INV'}-2026-{k:05d}",
            "issueDate": f"2026-10-{1 + k % 28:02d}",
            "dueDate": f"2026-11-{1 + k % 28:02d}",
            "currency": currency,
            "supplier": dict(supplier),
            "customer": dict(CUSTOMERS[customer]),
            "iban": "DE89370400440532013000" if supplier is DE_SUPPLIER else "NO9386011117947",
            "lines": lines_for(rnd, n or rnd.randrange(1, 6), rates),
        }
        if refs in ("order", "both"):
            req["orderReference"] = f"PO-{4000 + k}"
        if refs in ("buyer", "both"):
            req["buyerReference"] = f"BR-{1000 + k}"
        if means:
            req["paymentMeansCode"] = means
        if reasons:
            req["exemptionReasons"] = reasons
        if acs:
            req["allowanceCharges"] = acs
        if prepaid:
            req["prepaid"] = prepaid
        if delivery:
            req["deliveryDate"], req["deliveryCountry"] = delivery
        if "O" in {l["category"] for l in req["lines"]}:  # BR-O-02: no VAT ids with O
            req["supplier"].pop("vatId", None)
            req["customer"].pop("vatId", None)
        docs.append({"family": family, "request": req, "expect": expected(req)})

    S19, S7 = ("S", "19"), ("S", "7")
    for _ in range(4):
        add("DE domestic, 19%", DE_SUPPLIER, rnd.choice(["DE", "DE2"]), [S19])
    for _ in range(4):
        add("DE domestic, 19% and 7%", DE_SUPPLIER, "DE", [S19, S7], n=rnd.randrange(2, 7))
    for _ in range(3):
        add("DE domestic, allowance and freight", DE_SUPPLIER, "DE2", [S19, S7], n=4, acs=[
            {"charge": False, "reason": "Volume discount", "amount": "12.35", "category": "S", "percent": "19"},
            {"charge": True, "reason": "Freight", "amount": "45.00", "category": "S", "percent": "19"},
            {"charge": False, "reason": "Early order", "amount": "0.05", "category": "S", "percent": "7"}])
    for _ in range(2):
        add("DE domestic, SEPA and prepaid", DE_SUPPLIER, "DE", [S19], means="58", prepaid="100.00")
    for _ in range(3):
        add("DE domestic, 19% and zero-rated", DE_SUPPLIER, "DE", [S19, ("Z", "0")], n=3)
    add("DE domestic, zero-rated only", DE_SUPPLIER, "DE2", [("Z", "0")])
    for _ in range(2):
        add("DE domestic, exempt training", DE_SUPPLIER, "DE", [("E", "0")], reasons={"E": "Exempt under §4 Nr. 21 UStG"})
    for c in ["FR", "NL", "AT", "IT", "FR"]:
        add("intra-community supply", DE_SUPPLIER, c, [("K", "0")], reasons={"K": "Intra-community supply"},
            delivery=("2026-09-28", CUSTOMERS[c]["countryCode"]), refs="order")
    for c in ["NL", "AT"]:
        add("intra-community supply with allowance", DE_SUPPLIER, c, [("K", "0")], reasons={"K": "Intra-community supply"},
            delivery=("2026-09-29", CUSTOMERS[c]["countryCode"]),
            acs=[{"charge": False, "reason": "Pallet return", "amount": "8.40", "category": "K", "percent": "0"}])
    for c in ["FR", "IT", "BE", "NL"]:
        add("reverse charge services", DE_SUPPLIER, c, [("AE", "0")], reasons={"AE": "Reverse charge"}, refs="order")
    for c, cur in [("CH", "CHF"), ("US", "USD"), ("CH", "EUR"), ("US", "USD")]:
        add("export", DE_SUPPLIER, c, [("G", "0")], currency=cur, reasons={"G": "Export outside the EU"})
    for _ in range(2):
        add("not subject to VAT", dict(DE_SUPPLIER), "FR", [("O", "0")], reasons={"O": "Not subject to VAT"})
    for _ in range(2):
        add("consumer sale at French rate", DE_SUPPLIER, "FRC", [("S", "20"), ("S", "5.5")], n=2)
    for _ in range(3):
        add("NO domestic, 25% and 15%", NO_SUPPLIER, "NO", [("S", "25"), ("S", "15")], currency="NOK", n=3)
    for _ in range(2):
        add("NO domestic with allowance", NO_SUPPLIER, "NO2", [("S", "25")], currency="NOK",
            acs=[{"charge": False, "reason": "Rabatt", "amount": "150.00", "category": "S", "percent": "25"}])
    add("NO export to Germany", NO_SUPPLIER, "DE", [("G", "0")], currency="EUR", reasons={"G": "Export"})
    add("DE domestic, 40 lines", DE_SUPPLIER, "DE", [S19, S7, S19], n=40)
    add("DE domestic, single line", DE_SUPPLIER, "DE2", [S19], n=1)
    for _ in range(2):
        add("DE domestic, buyer and order reference", DE_SUPPLIER, "DE", [S19], refs="both")
    add("intra-community supply with freight", DE_SUPPLIER, "FR", [("K", "0")], reasons={"K": "Intra-community supply"},
        delivery=("2026-09-30", "FR"),
        acs=[{"charge": True, "reason": "Freight", "amount": "35.00", "category": "K", "percent": "0"}])
    add("DE domestic, half-cent rounding", DE_SUPPLIER, "DE", [S7], n=1)
    docs[-1]["request"]["lines"][0].update(quantity="3", unitPrice="0.835")  # 2.505 -> 2.51; 7% of 2.51 = 0.1757 -> 0.18
    docs[-1]["expect"] = expected(docs[-1]["request"])

    assert len(docs) == 50, len(docs)
    families = {}
    for d in docs:
        families[d["family"]] = families.get(d["family"], 0) + 1
    out = {
        "generator": "gen_peppol50.py (python3 -I, stdlib only)",
        "families": families,
        "creditNotes": sum(1 for d in docs if d["request"]["creditNote"]),
        "documents": docs,
    }
    (HERE / "invoices-50.json").write_text(json.dumps(out, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"{len(docs)} documents, {out['creditNotes']} credit notes, {len(families)} families")


if __name__ == "__main__":
    main()
