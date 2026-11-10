#!/usr/bin/env python3
"""Independent reference for the Wave 10 AX proof fixtures (issue #10, AC1, AC2, AC4).

Standard library only. Run from anywhere with:  python3 -I ax_reference.py

It generates a synthetic AX 2012 extract (the generator spec below is mirrored by axGen in ax_proof_test.go),
writes the demo extract as one CSV per AX table under demo/, and computes the expected migration outcome from its
own reading of the mapping rules in mappings/*.json and the pipeline's duplicate-key rule. It never runs or
imports the Go engine, so expected.json is not re-derived from the code under test.

The extract is deliberately dirty at fixed rates (locale-formatted numbers and timestamps, AX enum labels instead
of values, blank required columns, values past scale or magnitude, exact and conflicting duplicate keys, padded
keys, typographic punctuation, combining marks, tabs and non-breaking spaces). The rates are part of the
generator, not tuned to any threshold; the script prints the observed reject and warning rates.
"""
import csv
import hashlib
import json
import os
import re
import unicodedata
from collections import Counter, defaultdict
from datetime import datetime, timedelta
from decimal import ROUND_HALF_EVEN, Decimal

HERE = os.path.dirname(os.path.abspath(__file__))

DEMO_SIZES = {"party": 600, "customer": 400, "vendor": 200, "product": 800, "item": 800, "bom": 150, "route": 100}
SCALE_SIZES = {"customer": 50_000, "vendor": 20_000, "item": 100_000, "bom": 10_000}

# Table name, key columns and column order, as in tools/connectors/ax.go (plus the ModifiedDateTime cursor).
TABLES = {
    "party": ("DirParty", ["PartyNumber"], ["PartyNumber", "Name", "RecId"]),
    "customer": ("CustTable", ["DataAreaId", "AccountNum"], ["DataAreaId", "AccountNum", "Party", "CustGroup", "Currency", "CreditMax", "Blocked"]),
    "vendor": ("VendTable", ["DataAreaId", "AccountNum"], ["DataAreaId", "AccountNum", "Party", "VendGroup", "Currency", "Blocked"]),
    "item": ("InventTable", ["DataAreaId", "ItemId"], ["DataAreaId", "ItemId", "ItemType", "Product", "NetWeight"]),
    "product": ("EcoResProduct", ["DisplayProductNumber"], ["DisplayProductNumber", "ProductType", "RecId"]),
    "bom": ("BOM", ["DataAreaId", "BOMId"], ["DataAreaId", "BOMId", "Name", "Approved"]),
    "route": ("Route", ["DataAreaId", "RouteId"], ["DataAreaId", "RouteId", "Name", "Approved"]),
}
for _e, (_t, _k, _c) in TABLES.items():
    _c.append("ModifiedDateTime")

DUP = "MIG-ROW-DUPLICATE-KEY"
REQUIRED = "MIG-MAP-REQUIRED"
COERCE = "MIG-MAP-COERCE"
ENUM = "MIG-MAP-ENUM"
RANGE = "MIG-MAP-RANGE"
ENUM_DEFAULT = "MIG-MAP-ENUM-DEFAULT"
PRECISION = "MAP-PRECISION-LOSS"

# ---------------------------------------------------------------- generator


def fnv1a32(s):
    h = 0x811C9DC5
    for b in s.encode("utf-8"):
        h ^= b
        h = (h * 0x01000193) & 0xFFFFFFFF
    return h


def u(entity, i, field):
    return fnv1a32(f"{entity}:{i}:{field}")


EPOCH = datetime(2012, 1, 1)
TS_SPAN = 378_691_200  # twelve years of seconds


def pick(r, table):
    for limit, value in table:
        if r < limit:
            return value
    return table[-1][1]


def company(e, i):
    return pick(u(e, i, "co") % 10, [(6, "usmf"), (9, "demf"), (10, "gbsi")])


def modified(e, i):
    r = u(e, i, "mdt") % 1000
    t = EPOCH + timedelta(seconds=u(e, i, "ts") % TS_SPAN)
    if r < 30:
        return "1900-01-01 00:00:00"  # AX null date
    if r < 32:
        return t.strftime("%d.%m.%Y %H:%M:%S")  # locale export, not an AX datetime
    return t.strftime("%Y-%m-%d %H:%M:%S")


def blocked(e, i):
    return pick(u(e, i, "blk") % 1000, [(850, "0"), (900, "1"), (940, "2"), (960, "3"), (975, "4"), (995, "5"), (1000, "All")])


def noyes(e, i):
    return pick(u(e, i, "apr") % 1000, [(550, "1"), (600, "Yes"), (950, "0"), (990, "No"), (1000, "2")])


def named(e, i, names):
    if u(e, i, "nmblank") % 1000 < 4:
        return ""
    return names[u(e, i, "nm") % len(names)] + f" {i}"


PARTY_NAMES = ["Contoso Retail", "Fabrikam \u201cS\u00fcd\u201d GmbH", "  Adatum\u00a0Corp ", "Soci\u00e9t\u00e9 G\u00e9n\u00e9rale \u2013 Paris",
               "Litware\tInc", "O\u2019Brien & Sons", "Northwind", "Zoe\u0308 Industries"]
BOM_NAMES = ["Frame assembly", "Caf\u00e9 table \u2013 oak", "M\u00fcller \u201cPro\u201d kit", "  Pump\u00a0housing  ",
             "Geh\u00e4use  Typ\tB", "Bracket, steel", "Cafe\u0301 chair"]
ROUTE_NAMES = ["Weld \u2013 paint \u2013 pack", "Assemble \u201cA\u201d line", "Machining  cell 2", "Pr\u00fcfung\u00a0QA"]


def gen_party(e, i):
    return {"PartyNumber": f"{i + 1:09d}", "Name": named(e, i, PARTY_NAMES), "RecId": str(5637144576 + i), "ModifiedDateTime": modified(e, i)}


def credit_max(e, i):
    r = u(e, i, "cm") % 1000
    cents = u(e, i, "cmv") % 50_000_000
    if r < 3:
        return f"{cents // 100:,}".replace(",", ".") + f",{cents % 100:02d}"  # German export: 12.500,00
    if r < 5:
        return f"{150_000_000_000_000 + cents // 100}.{cents % 100:02d}"  # at or past the 1e14 magnitude limit
    if r < 105:
        return f"{cents // 100}.{cents % 100:02d}5"  # three places, exactly half a cent
    if r < 205:
        return "0"
    return f"{cents // 100}.{cents % 100:02d}"


def gen_customer(e, i):
    return {
        "DataAreaId": company(e, i), "AccountNum": f"C{i:06d}", "Party": str(5637144576 + i),
        "CustGroup": pick(u(e, i, "grp") % 100, [(35, "10"), (65, "20"), (85, "30"), (95, "40"), (100, "")]),
        "Currency": pick(u(e, i, "cur") % 100, [(60, "USD"), (80, "EUR"), (92, "CAD"), (98, ""), (100, " usd ")]),
        "CreditMax": credit_max(e, i), "Blocked": blocked(e, i), "ModifiedDateTime": modified(e, i),
    }


def gen_vendor(e, i):
    return {
        "DataAreaId": company(e, i), "AccountNum": f"V{i:06d}", "Party": str(5637144976 + i),
        "VendGroup": pick(u(e, i, "grp") % 100, [(40, "10"), (70, "20"), (95, "30"), (98, "INTERCO"), (100, "")]),
        "Currency": pick(u(e, i, "cur") % 1000, [(600, "USD"), (800, "EUR"), (990, "CAD"), (1000, "")]),
        "Blocked": blocked(e, i), "ModifiedDateTime": modified(e, i),
    }


def net_weight(e, i):
    r = u(e, i, "nw") % 1000
    if r < 2:
        return "0.12345678"  # eight places, past the unscaled limit of six
    if r < 100:
        return "0"
    w = u(e, i, "nwv") % 2_500_000
    return f"{w // 10000}.{w % 10000:04d}"


def gen_item(e, i):
    item = f"I{i:06d}"
    if u(e, i, "pad") % 100 < 5:
        item = f" {item}  "
    return {
        "DataAreaId": company(e, i), "ItemId": item,
        "ItemType": pick(u(e, i, "typ") % 1000, [(700, "0"), (900, "1"), (996, "2"), (1000, "Item")]),
        "Product": "" if u(e, i, "prd") % 1000 < 3 else str(5637344576 + i),
        "NetWeight": net_weight(e, i), "ModifiedDateTime": modified(e, i),
    }


def gen_product(e, i):
    return {"DisplayProductNumber": f"P-{i:05d}", "ProductType": pick(u(e, i, "typ") % 1000, [(800, "1"), (995, "2"), (1000, "0")]),
            "RecId": str(5637344576 + i), "ModifiedDateTime": modified(e, i)}


def gen_bom(e, i):
    return {"DataAreaId": company(e, i), "BOMId": f"B{i:06d}", "Name": named(e, i, BOM_NAMES), "Approved": noyes(e, i), "ModifiedDateTime": modified(e, i)}


def gen_route(e, i):
    return {"DataAreaId": company(e, i), "RouteId": f"R{i:06d}", "Name": named(e, i, ROUTE_NAMES), "Approved": noyes(e, i), "ModifiedDateTime": modified(e, i)}


def flip_currency(f):
    f["Currency"] = "USD" if f["Currency"] == "EUR" else "EUR"


def flip_weight(f):
    f["NetWeight"] = "1.5" if f["NetWeight"] == "0" else "0"


def flip_approved(f):
    f["Approved"] = "0" if f["Approved"] in ("1", "Yes") else "1"


# entity: (row generator, duplicate rate per mille, change applied to a conflicting duplicate)
GENERATORS = {
    "party": (gen_party, 0, None),
    "customer": (gen_customer, 6, flip_currency),
    "vendor": (gen_vendor, 4, flip_currency),
    "item": (gen_item, 3, flip_weight),
    "product": (gen_product, 0, None),
    "bom": (gen_bom, 3, flip_approved),
    "route": (gen_route, 0, None),
}


def generate(entity, n):
    make, dup_permille, conflict = GENERATORS[entity]
    rows, prev = [], None
    for i in range(n):
        if prev is not None and u(entity, i, "dup") % 1000 < dup_permille:
            fields = dict(prev)  # an overlapping extract repeats the previous row
            if u(entity, i, "dupkind") % 2 == 1:
                conflict(fields)  # ...after it changed in the source
        else:
            fields = make(entity, i)
        rows.append(fields)
        prev = fields
    return rows


# ---------------------------------------------------------------- mapping semantics (read from the README, not the code)

LEGAL_ENTITY = {"usmf": "USMF", "demf": "DEMF", "gbsi": "GBSI"}
BLOCKED = {"0": "none", "1": "invoice", "2": "all", "3": "payment", "4": "requisition", "5": "never"}
SEGMENT = {"10": "retail", "20": "wholesale", "30": "oem"}
ITEM_TYPE = {"0": "item", "1": "bom", "2": "service"}
PRODUCT_TYPE = {"1": "item", "2": "service"}
PLAIN_DECIMAL = re.compile(r"[+-]?\d+(\.\d+)?")
INTEGER = re.compile(r"[+-]?\d+")
AX_NULL = {"1900-01-01", "1900-01-01 00:00:00", "1900-01-01T00:00:00"}
LIMIT = Decimal(10) ** 14
PUNCT = [("\u2018", "'"), ("\u2019", "'"), ("\u201c", '"'), ("\u201d", '"'), ("\u2013", "-"), ("\u2014", "-"),
         ("\u2026", "..."), ("\u00a0", " "), ("\ufeff", "")]


class Row:
    """Collects one mapped row: values tagged s/i/b, warnings and rejects (target, code)."""

    def __init__(self, src):
        self.src, self.out, self.warns, self.rejects = src, {}, [], []

    def text(self, col):
        return self.src[col].strip()

    def reject(self, target, code):
        self.rejects.append((target, code))

    def string(self, target, col, required=False, default=None, enum=None, enum_default=None, normalize=False):
        t = self.text(col)
        if not t:
            if default is not None:
                self.out[target] = ("s", default)
            elif required:
                self.reject(target, REQUIRED)
            return
        if enum is not None:
            if t in enum:
                t = enum[t]
            elif enum_default is not None:
                self.warns.append((target, ENUM_DEFAULT))
                t = enum_default
            else:
                self.reject(target, ENUM)
                return
        if normalize:
            t = codepage_normalize(t)
        self.out[target] = ("s", t)

    def integer(self, target, col, required=False):
        t = self.text(col)
        if not t:
            if required:
                self.reject(target, REQUIRED)
            return
        if not INTEGER.fullmatch(t) or not -(2 ** 63) <= int(t) < 2 ** 63:
            self.reject(target, COERCE)
            return
        self.out[target] = ("i", int(t))

    def decimal(self, target, col, scale=None, default=None):
        t = self.text(col) or default
        if t is None:
            return
        if not PLAIN_DECIMAL.fullmatch(t):
            self.reject(target, COERCE)
            return
        d = Decimal(t)
        if scale is None:
            places = len(t.split(".")[1].rstrip("0")) if "." in t else 0
            if places > 6 or abs(d) >= LIMIT:
                self.reject(target, RANGE)
                return
            self.out[target] = ("s", format(d.normalize(), "f"))
            return
        q = d.quantize(Decimal(1).scaleb(-scale), rounding=ROUND_HALF_EVEN)
        if abs(q) >= LIMIT:
            self.reject(target, RANGE)
            return
        if q != d:
            self.warns.append((target, PRECISION))
        self.out[target] = ("s", f"{q:.{scale}f}")

    def ax_noyes(self, target, col):
        t = self.text(col)
        if not t:
            self.reject(target, REQUIRED)
            return
        v = {"1": True, "yes": True, "0": False, "no": False}.get(t.lower())
        if v is None:
            self.reject(target, COERCE)
            return
        self.out[target] = ("b", v)

    def ax_datetime(self, target, col):
        t = self.text(col)
        if not t or t in AX_NULL:
            return
        for layout in ("%Y-%m-%d %H:%M:%S", "%Y-%m-%dT%H:%M:%S"):
            try:
                dt = datetime.strptime(t, layout)
                break
            except ValueError:
                dt = None
        if dt is None or not re.fullmatch(r"\d{4}-\d\d-\d\d[ T]\d\d:\d\d:\d\d", t):
            self.reject(target, COERCE)
            return
        self.out[target] = ("s", dt.strftime("%Y-%m-%dT%H:%M:%SZ"))  # sourceTimezone unset: UTC

    def result(self, conditions=()):
        if self.rejects:
            return None, [], [c for _, c in self.rejects]
        failed = [code for target, test, code in conditions if test(self.out.get(target))]
        if failed:
            return None, [], failed
        return self.out, [c for _, c in self.warns], []


def codepage_normalize(s):
    s = unicodedata.normalize("NFC", s)
    for a, b in PUNCT:
        s = s.replace(a, b)
    s = "".join(" " if ch.isspace() else ("" if unicodedata.category(ch) == "Cc" else ch) for ch in s)
    return " ".join(s.split())


def map_party(src):
    r = Row(src)
    r.string("partyNumber", "PartyNumber", required=True)
    r.string("name", "Name", required=True, normalize=True)
    r.integer("recId", "RecId", required=True)
    r.ax_datetime("modifiedAt", "ModifiedDateTime")
    return r.result()


def map_customer(src):
    r = Row(src)
    r.string("accountNumber", "AccountNum", required=True)
    r.string("legalEntity", "DataAreaId", required=True, enum=LEGAL_ENTITY)
    r.integer("partyRecId", "Party", required=True)
    r.string("segment", "CustGroup", enum=SEGMENT, enum_default="other")
    r.string("currency", "Currency", default="USD")
    r.decimal("creditLimit", "CreditMax", scale=2, default="0")
    r.string("blocked", "Blocked", required=True, enum=BLOCKED)
    r.ax_datetime("modifiedAt", "ModifiedDateTime")
    return r.result([("currency", lambda v: v is None or v[1] not in ("USD", "EUR", "CAD"), "CUST-CURRENCY-UNKNOWN")])


def map_vendor(src):
    r = Row(src)
    r.string("accountNumber", "AccountNum", required=True)
    r.string("legalEntity", "DataAreaId", required=True, enum=LEGAL_ENTITY)
    r.integer("partyRecId", "Party", required=True)
    r.string("vendorGroup", "VendGroup")
    r.string("currency", "Currency", required=True)
    r.string("blocked", "Blocked", required=True, enum=BLOCKED)
    r.ax_datetime("modifiedAt", "ModifiedDateTime")
    return r.result([("vendorGroup", lambda v: v is not None and v[1] == "INTERCO", "VEND-INTERCOMPANY")])


def map_item(src):
    r = Row(src)
    r.string("sku", "ItemId", required=True)
    r.string("legalEntity", "DataAreaId", required=True, enum=LEGAL_ENTITY)
    r.string("itemType", "ItemType", required=True, enum=ITEM_TYPE)
    r.integer("productRecId", "Product", required=True)
    r.decimal("netWeight", "NetWeight", default="0")
    r.ax_datetime("modifiedAt", "ModifiedDateTime")
    return r.result()


def map_product(src):
    r = Row(src)
    r.string("productNumber", "DisplayProductNumber", required=True)
    r.string("productType", "ProductType", required=True, enum=PRODUCT_TYPE)
    r.integer("recId", "RecId", required=True)
    r.ax_datetime("modifiedAt", "ModifiedDateTime")
    return r.result()


def map_bom(src):
    r = Row(src)
    r.string("bomNumber", "BOMId", required=True)
    r.string("legalEntity", "DataAreaId", required=True, enum=LEGAL_ENTITY)
    r.string("name", "Name", required=True, normalize=True)
    r.ax_noyes("approved", "Approved")
    r.ax_datetime("modifiedAt", "ModifiedDateTime")
    return r.result()


def map_route(src):
    r = Row(src)
    r.string("routeNumber", "RouteId", required=True)
    r.string("legalEntity", "DataAreaId", required=True, enum=LEGAL_ENTITY)
    r.string("name", "Name", required=True, normalize=True)
    r.ax_noyes("approved", "Approved")
    r.ax_datetime("modifiedAt", "ModifiedDateTime")
    return r.result()


MAPPERS = {"party": map_party, "customer": map_customer, "vendor": map_vendor, "item": map_item,
           "product": map_product, "bom": map_bom, "route": map_route}

# ---------------------------------------------------------------- pipeline outcome


def value_text(v):
    tag, x = v
    if tag == "b":
        return "b:" + ("true" if x else "false")
    return f"{tag}:{x}"


def digest(lines):
    return hashlib.sha256("\n".join(sorted(lines)).encode("utf-8")).hexdigest()


def migrate(entity, rows):
    _, key_cols, _ = TABLES[entity]
    keys = ["|".join(r[c].strip() for c in key_cols) for r in rows]
    by_key = defaultdict(list)
    for k, r in zip(keys, rows):
        by_key[k].append(r)
    conflicting = {k for k, rs in by_key.items() if any(x != rs[0] for x in rs[1:])}
    seen, loaded = set(), {}
    rejects, warns = defaultdict(list), defaultdict(list)
    rejected_rows = 0
    for k, r in zip(keys, rows):
        if k in conflicting or k in seen:
            rejects[k].append(DUP)
            rejected_rows += 1
            continue
        seen.add(k)
        out, w, rej = MAPPERS[entity](r)
        if rej:
            rejects[k] += rej
            rejected_rows += 1
            continue
        if w:
            warns[k] += w
        loaded[k] = out
    target_lines = [k + "\t" + "\t".join(f"{name}={value_text(v)}" for name, v in sorted(out.items())) for k, out in loaded.items()]
    return {
        "extracted": len(rows),
        "loaded": len(loaded),
        "rejectedRows": rejected_rows,
        "warnedRows": len(warns),
        "rejectCodes": dict(sorted(Counter(c for cs in rejects.values() for c in cs).items())),
        "warningCodes": dict(sorted(Counter(c for cs in warns.values() for c in cs).items())),
        "targetDigest": digest(target_lines),
        "rejectDigest": digest(f"{k}\t{','.join(sorted(cs))}" for k, cs in rejects.items()),
        "warningDigest": digest(f"{k}\t{','.join(sorted(cs))}" for k, cs in warns.items()),
    }, {k: sorted(cs) for k, cs in sorted(rejects.items())}


def write_demo(entity, rows):
    table, _, cols = TABLES[entity]
    os.makedirs(os.path.join(HERE, "demo"), exist_ok=True)
    with open(os.path.join(HERE, "demo", table + ".csv"), "w", encoding="utf-8", newline="") as fh:
        w = csv.writer(fh, lineterminator="\n")
        w.writerow(cols)
        for r in rows:
            w.writerow([r[c] for c in cols])


def read_demo(entity):
    table, _, _ = TABLES[entity]
    with open(os.path.join(HERE, "demo", table + ".csv"), encoding="utf-8", newline="") as fh:
        return list(csv.DictReader(fh))


def real_loader_prediction(demo):
    """What the real loader adds to the pipeline outcome: a customer or vendor whose AX party was rejected has no
    Passage party to attach the role to, so it is refused MIG-REF-UNRESOLVED. The real-loader run keys parties by RecId."""
    party_rejects = demo["party"]["rejects"]
    by_recid = {}
    for r in read_demo("party"):
        if r["PartyNumber"].strip() in party_rejects:
            by_recid[r["RecId"].strip()] = party_rejects[r["PartyNumber"].strip()]
    unresolved = {}
    for entity in ("customer", "vendor"):
        _, key_cols, _ = TABLES[entity]
        found = {}
        for r in read_demo(entity):
            k = "|".join(r[c].strip() for c in key_cols)
            if k not in demo[entity]["rejects"] and r["Party"].strip() in by_recid:
                found[k] = ["MIG-REF-UNRESOLVED"]
        unresolved[entity] = dict(sorted(found.items()))
    return {"partyRejectsByRecId": dict(sorted(by_recid.items())), "unresolvedParty": unresolved}


def main():
    expected = {"demo": {}, "scale": {}}
    for entity, n in DEMO_SIZES.items():
        rows = generate(entity, n)
        write_demo(entity, rows)
        reread = read_demo(entity)
        assert reread == rows, f"{entity}: CSV round trip changed the extract"
        stats, rejects = migrate(entity, reread)
        stats["rejects"] = rejects
        expected["demo"][entity] = stats
    expected["demoReal"] = real_loader_prediction(expected["demo"])
    for entity, n in SCALE_SIZES.items():
        expected["scale"][entity], _ = migrate(entity, generate(entity, n))
    with open(os.path.join(HERE, "expected.json"), "w", encoding="utf-8") as fh:
        json.dump(expected, fh, indent=1, ensure_ascii=False, sort_keys=True)
        fh.write("\n")
    for part in ("demo", "scale"):
        for entity, s in expected[part].items():
            print(f"{part:5} {entity:8} extracted={s['extracted']:6} loaded={s['loaded']:6} "
                  f"rejected={s['rejectedRows']:5} ({100 * s['rejectedRows'] / s['extracted']:.2f}%) "
                  f"warned={s['warnedRows']:5} ({100 * s['warnedRows'] / s['extracted']:.2f}%) codes={s['rejectCodes']}")


if __name__ == "__main__":
    main()
