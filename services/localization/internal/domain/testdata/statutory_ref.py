"""Independent reference for the statutory fixtures in countrypacks/ (PSG-48 AC1).

Run with `python3 -I statutory_ref.py <countrypacks dir>` (stdlib only). It shares no code with the
Go validators: it rebuilds the US IRS FIRE 1099-NEC fixture byte for byte from Publication 1220
(Rev. 05-2026, tax year 2025) record positions, and recomputes every derived value of the other
fixtures (totals, check digits, tax arithmetic) from their inputs. It prints one line per fixture and
exits non-zero on the first disagreement. With `--write` it (re)writes the FIRE fixture.

The Go test TestStatutoryReferenceScriptAgrees runs it in CI when python3 is on the PATH.
"""

import json
import re
import sys
import xml.etree.ElementTree as ET
from datetime import date
from decimal import Decimal, ROUND_HALF_UP
from pathlib import Path

C2 = Decimal("0.01")


def fail(msg):
    print("MISMATCH " + msg)
    sys.exit(1)


def luhn_ok(digits):
    total = 0
    for i, ch in enumerate(reversed(digits)):
        d = int(ch)
        if i % 2 == 1:
            d *= 2
            if d > 9:
                d -= 9
        total += d
    return total % 10 == 0


def gstin_check(body):
    alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
    total = 0
    for i, ch in enumerate(body):  # left to right, weights 1, 2, 1, 2 ...
        v = alphabet.index(ch) * (2 if i % 2 else 1)
        total += v // 36 + v % 36
    return alphabet[(36 - total % 36) % 36]


# ---------------------------------------------------------------- US IRS FIRE 1099-NEC (Pub 1220)

def field(rec, start, value):
    """Place value at 1-based Pub 1220 position start."""
    i = start - 1
    rec[i:i + len(value)] = list(value)


def blank_record():
    return [" "] * 750


def money(amount, width):
    cents = int((Decimal(amount) * 100).to_integral_value())
    return str(cents).rjust(width, "0")


PAYEES = [
    # (type of TIN, TIN, name, address, city, state, zip, NEC amount (code 1), federal tax withheld (code 4))
    ("2", "412873560", "RIVERA MARIA", "418 ELM STREET", "AUSTIN", "TX", "78701", "1250.00", "0.00"),
    ("1", "870431926", "NORTHWIND DESIGN LLC", "77 HARBOR WAY STE 200", "PORTLAND", "OR", "972041234", "98000.75", "0.00"),
    ("2", "529184736", "OKAFOR JAMES", "9 BIRCH LANE", "COLUMBUS", "OH", "43215", "600.00", "144.00"),
]


def build_fire():
    recs = []
    seq = 0

    def finish(rec):
        nonlocal seq
        seq += 1
        field(rec, 500, str(seq).rjust(8, "0"))
        # positions 749-750 carry CR/LF
        recs.append("".join(rec[:748]) + "\r\n")

    t = blank_record()
    field(t, 1, "T"); field(t, 2, "2025"); field(t, 7, "943812765"); field(t, 16, "PSG01")
    field(t, 28, "T"); field(t, 30, "PASSAGE DEMO TRANSMITTER"); field(t, 110, "PASSAGE DEMO INC")
    field(t, 190, "100 MAIN STREET"); field(t, 230, "BOSTON"); field(t, 270, "MA"); field(t, 272, "021101234")
    field(t, 296, str(len(PAYEES)).rjust(8, "0")); field(t, 304, "JANE FILER"); field(t, 344, "6175550100")
    field(t, 359, "filer@example.com"); field(t, 518, "I")
    finish(t)

    a = blank_record()
    field(a, 1, "A"); field(a, 2, "2025"); field(a, 12, "943812765"); field(a, 21, "PASS")
    field(a, 26, "NE"); field(a, 28, "14"); field(a, 53, "PASSAGE DEMO INC"); field(a, 133, "0")
    field(a, 134, "100 MAIN STREET"); field(a, 174, "BOSTON"); field(a, 214, "MA"); field(a, 216, "021101234")
    field(a, 225, "6175550100")
    finish(a)

    totals = {1: Decimal("0"), 4: Decimal("0")}
    for i, (tin_type, tin, name, addr, city, state, zipc, nec, fit) in enumerate(PAYEES, start=1):
        b = blank_record()
        field(b, 1, "B"); field(b, 2, "2025"); field(b, 7, name.replace(" ", "")[:4]); field(b, 11, tin_type)
        field(b, 12, tin); field(b, 21, "NEC2025-%04d" % i)
        for k in range(18):  # Payment Amounts 1..9, A..H, J at 55 + 12k
            field(b, 55 + 12 * k, "0" * 12)
        field(b, 55, money(nec, 12))        # amount code 1
        field(b, 55 + 12 * 3, money(fit, 12))  # amount code 4
        totals[1] += Decimal(nec)
        totals[4] += Decimal(fit)
        field(b, 288, name); field(b, 368, addr); field(b, 448, city); field(b, 488, state); field(b, 490, zipc)
        field(b, 723, "0" * 12); field(b, 735, "0" * 12)
        finish(b)

    c = blank_record()
    field(c, 1, "C"); field(c, 2, str(len(PAYEES)).rjust(8, "0"))
    for k in range(18):  # Control Totals at 16 + 18k
        field(c, 16 + 18 * k, "0" * 18)
    field(c, 16, money(totals[1], 18))
    field(c, 16 + 18 * 3, money(totals[4], 18))
    finish(c)

    f = blank_record()
    field(f, 1, "F"); field(f, 2, "00000001"); field(f, 10, "0" * 21); field(f, 50, str(len(PAYEES)).rjust(8, "0"))
    finish(f)
    return "".join(recs), totals


# ---------------------------------------------------------------- other packs

def check_de(path):
    root = ET.parse(path).getroot()
    u = root.find("./Steuerfall/Umsatzsteuervoranmeldung")
    kz = {e.tag: Decimal(e.text) for e in u if e.tag.startswith("Kz")}
    expected = (kz.get("Kz81", 0) * Decimal("0.19") + kz.get("Kz86", 0) * Decimal("0.07")
                + kz.get("Kz36", 0) - kz.get("Kz66", 0)).quantize(C2)
    if kz["Kz83"] != expected:
        fail("DE Kz83 %s, expected %s" % (kz["Kz83"], expected))
    return "Kz83=%s" % expected


def check_fr(path):
    lines = path.read_text(encoding="utf-8").splitlines()
    rows = [ln.split("|") for ln in lines[1:]]
    by_entry = {}
    for r in rows:
        d = Decimal(r[11].replace(",", "."))
        c = Decimal(r[12].replace(",", "."))
        s = by_entry.setdefault(r[2], [Decimal(0), Decimal(0)])
        s[0] += d
        s[1] += c
    for num, (d, c) in by_entry.items():
        if d != c:
            fail("FR entry %s debit %s credit %s" % (num, d, c))
    siren = path.name[:9]
    if not luhn_ok(siren):
        fail("FR SIREN %s fails Luhn" % siren)
    total = sum(v[0] for v in by_entry.values())
    return "%d entries balanced, total %s" % (len(by_entry), total)


def check_in(path):
    doc = json.loads(path.read_text(), parse_float=Decimal)
    for g in [doc["gstin"]] + [p["ctin"] for p in doc["b2b"]]:
        if gstin_check(g[:14]) != g[14]:
            fail("IN GSTIN %s check character" % g)
    home = doc["gstin"][:2]
    sums = {k: Decimal(0) for k in ("txval", "iamt", "camt", "samt", "csamt")}
    for party in doc["b2b"]:
        for inv in party["inv"]:
            inter = inv["pos"] != home
            for it in inv["itms"]:
                d = it["itm_det"]
                tax = (Decimal(d["txval"]) * Decimal(d["rt"]) / 100).quantize(C2, ROUND_HALF_UP)
                if inter and (Decimal(d["iamt"]) != tax or d["camt"] or d["samt"]):
                    fail("IN %s inter-state tax" % inv["inum"])
                if not inter:
                    half = (tax / 2).quantize(C2, ROUND_HALF_UP)
                    if d["iamt"] or Decimal(d["camt"]) != half or Decimal(d["samt"]) != half:
                        fail("IN %s intra-state tax" % inv["inum"])
                for k in sums:
                    sums[k] += Decimal(d[k])
    hsn = {k: sum(Decimal(r[k]) for r in doc["hsn"]["data"]) for k in sums}
    if hsn != sums:
        fail("IN HSN summary %s != invoices %s" % (hsn, sums))
    return "taxable %s, IGST %s, CGST %s, SGST %s" % (sums["txval"], sums["iamt"], sums["camt"], sums["samt"])


def check_mx(path):
    ns = {"cfdi": "http://www.sat.gob.mx/cfd/4"}
    root = ET.parse(path).getroot()
    subtotal = Decimal(0)
    tax = Decimal(0)
    for c in root.findall("./cfdi:Conceptos/cfdi:Concepto", ns):
        imp = (Decimal(c.get("Cantidad")) * Decimal(c.get("ValorUnitario"))).quantize(C2)
        if imp != Decimal(c.get("Importe")):
            fail("MX Concepto Importe %s, expected %s" % (c.get("Importe"), imp))
        subtotal += imp
        for t in c.findall("./cfdi:Impuestos/cfdi:Traslados/cfdi:Traslado", ns):
            ti = (Decimal(t.get("Base")) * Decimal(t.get("TasaOCuota"))).quantize(C2, ROUND_HALF_UP)
            if ti != Decimal(t.get("Importe")):
                fail("MX Traslado Importe %s, expected %s" % (t.get("Importe"), ti))
            tax += ti
    if subtotal != Decimal(root.get("SubTotal")):
        fail("MX SubTotal")
    if tax != Decimal(root.find("./cfdi:Impuestos", ns).get("TotalImpuestosTrasladados")):
        fail("MX TotalImpuestosTrasladados")
    if subtotal + tax != Decimal(root.get("Total")):
        fail("MX Total")
    return "SubTotal %s, IVA %s, Total %s" % (subtotal, tax, subtotal + tax)


def check_ca(path):
    root = ET.parse(path).getroot()
    v = {e.tag: Decimal(e.text) for e in root if e.tag.startswith("line")}
    if v["line105"] != v["line103"] + v["line104"]:
        fail("CA line 105")
    if v["line108"] != v["line106"] + v["line107"]:
        fail("CA line 108")
    if v["line109"] != v["line105"] - v["line108"]:
        fail("CA line 109")
    bn = root.findtext("businessNumber")
    if not luhn_ok(bn[:9]):
        fail("CA BN %s fails Luhn" % bn)
    p = root.find("reportingPeriod")
    if date.fromisoformat(p.get("end")) < date.fromisoformat(p.get("start")):
        fail("CA period")
    return "net tax %s" % v["line109"]


def check_uk(path):
    r = json.loads(path.read_text(), parse_float=Decimal)
    if r["totalVatDue"] != r["vatDueSales"] + r["vatDueAcquisitions"]:
        fail("UK box 3")
    if r["netVatDue"] != abs(r["totalVatDue"] - r["vatReclaimedCurrPeriod"]):
        fail("UK box 5")
    return "box 5 %s" % r["netVatDue"]


def main():
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    packs = Path(args[0] if args else "countrypacks")
    fire, totals = build_fire()
    fire_path = packs / "us" / "fixtures" / "fire-1099nec-2025.txt"
    if "--write" in sys.argv:
        fire_path.write_bytes(fire.encode("ascii"))
    if fire_path.read_bytes() != fire.encode("ascii"):
        fail("US FIRE fixture differs from the Pub 1220 rebuild")
    print("US_1099_FIRE ok: 7 records, NEC %s, withheld %s" % (totals[1], totals[4]))
    print("UK_MTD_VAT ok: " + check_uk(packs / "uk" / "fixtures" / "mtd-vat-26a1.json"))
    print("DE_UStVA ok: " + check_de(packs / "de" / "fixtures" / "ustva-2025-01.xml"))
    print("FR_FEC ok: " + check_fr(packs / "fr" / "fixtures" / "732829320FEC20251231.txt"))
    print("IN_GSTR1 ok: " + check_in(packs / "in" / "fixtures" / "gstr1-082025.json"))
    print("MX_CFDI40 ok: " + check_mx(packs / "mx" / "fixtures" / "cfdi40-ingreso.xml"))
    print("CA_GST34 ok: " + check_ca(packs / "ca" / "fixtures" / "gst34-2025q3.xml"))


if __name__ == "__main__":
    main()
