#!/usr/bin/env python3
# Independent oracle for the Wave 09 proof "Multi-level BOM explosion for a
# 7-level, 2,000-component product ... matches a golden result" (AC-9.1).
#
# Writes, next to this script:
#   bom-golden-7x2000.csv  flat list of the existing tree fixture (the catalog
#                          the Go tests build in goldenCatalog), checked against
#                          the summary in bom-golden-7x2000.json
#   bom-dag-7x2000.json    an adversarial catalog: shared sub-assemblies (a DAG,
#                          so flat quantities aggregate over many paths),
#                          skip-level lines, 40 phantoms, by- and co-products,
#                          line effectivity on both sides of the as-of date,
#                          constant and variable scrap, series, round-up,
#                          decimal quantities, dated, site-specific, draft and
#                          quantity-ranged versions, and kits one level below
#                          the explosion depth that only phantom paths reach
#   bom-dag-7x2000.csv     its expected flat list
#
# No code is shared with the Go engine; arithmetic is exact (fractions). The
# rules come from the PRD (FR-9.1 to FR-9.3, FR-9.9, BR-9.4) and the README:
#   - a version serves a date when it is active (or expired, for history) and
#     effective_from <= date <= effective_to; drafts never serve. It serves a
#     quantity when from_qty <= qty <= to_qty. A version for the requested site
#     beats an all-sites version; any other remaining tie is a fixture error.
#   - consumption = parent qty x qty x (1 + scrap_var/100) / series + scrap_const,
#     rounded up to a whole number when round_up = quantity (AC-9.10).
#   - lines outside their own effectivity, co-products and by-products are not
#     consumed. A phantom is never a node: its own BOM's components take its
#     place at the phantom's level. A component is a node one level below its
#     parent; it is expanded while its level is below the requested depth and
#     it has a version; nodes left without children are summed per product
#     into the flat list.
#
# Seeded, so the output is reproducible: python3 -I gen_bom_golden.py
import hashlib
import json
import random
from datetime import date, timedelta
from fractions import Fraction as F
from pathlib import Path

HERE = Path(__file__).resolve().parent
AS_OF = date(2026, 3, 1)
SITE = "S1"
MAX_NODES = 100000


def frac(s):
    return F(s)


def dec_str(x):
    """Exact decimal string of a fraction whose denominator is 2^a 5^b."""
    neg = x < 0
    x = abs(x)
    k = 0
    den = x.denominator
    while den % 2 == 0 or den % 5 == 0:
        if den % 2 == 0:
            den //= 2
        if den % 5 == 0:
            den //= 5
        k += 1
    if den != 1:
        raise ValueError("non-terminating decimal %s" % x)
    scaled = x * 10**k
    assert scaled.denominator == 1
    n = scaled.numerator
    if k == 0:
        s = str(n)
    else:
        s = "%d.%0*d" % (n // 10**k, k, n % 10**k)
        s = s.rstrip("0").rstrip(".")
    return "-" + s if neg else s


def frac_digits(x):
    s = dec_str(x)
    return len(s.split(".")[1]) if "." in s else 0


def ceil(x):
    return -((-x.numerator) // x.denominator)


def day(s):
    return date.fromisoformat(s) if s else None


# ---------------------------------------------------------------- oracle


class Oracle:
    def __init__(self, versions, site, as_of, levels):
        self.by_product = {}
        for v in versions:
            self.by_product.setdefault(v["productId"], []).append(v)
        self.site, self.as_of, self.levels = site, as_of, levels
        self.max_digits = 0
        self.ceil_margin = F(1)
        self.used_versions = set()
        self.lines_seen = set()

    def serves(self, v, qty):
        if v["status"] not in ("active", "expired"):
            return False
        if self.as_of < day(v["effectiveFrom"]):
            return False
        if v.get("effectiveTo") and self.as_of > day(v["effectiveTo"]):
            return False
        if v.get("fromQty") is not None and qty < frac(v["fromQty"]):
            return False
        if v.get("toQty") is not None and qty > frac(v["toQty"]):
            return False
        return not v.get("siteId") or v["siteId"] == self.site

    def pick(self, product, qty):
        found = [v for v in self.by_product.get(product, []) if self.serves(v, qty)]
        if not found:
            return None
        specific = [v for v in found if v.get("siteId")]
        top = specific or found
        if len(top) != 1:
            raise AssertionError("fixture ambiguity: %d versions of %s serve qty %s" % (len(top), product, qty))
        self.used_versions.add(top[0]["id"])
        return top[0]

    def consumed(self, ln):
        if ln["type"] in ("co_product", "by_product"):
            return False
        if ln.get("effectiveFrom") and self.as_of < day(ln["effectiveFrom"]):
            return False
        if ln.get("effectiveTo") and self.as_of > day(ln["effectiveTo"]):
            return False
        return True

    def need(self, ln, parent_qty):
        q = parent_qty * frac(ln["qty"]) * (100 + frac(ln.get("scrapVarPct", "0"))) / 100 / frac(ln.get("series", "1"))
        q += frac(ln.get("scrapConst", "0"))
        self.max_digits = max(self.max_digits, frac_digits(q))
        if ln.get("roundUp") == "quantity":
            rest = q - int(q)
            if rest:
                self.ceil_margin = min(self.ceil_margin, rest, 1 - rest)
            return F(ceil(q))
        return q

    def explode(self, root, qty):
        self.flat, self.nodes = {}, 0
        root_version = self.pick(root, qty)
        assert root_version is not None
        self.place(root_version, qty, 1, frozenset([root]))
        return self.flat, self.nodes

    def place(self, version, qty, level, above):
        """Places the components of one BOM as nodes on `level`; returns how many."""
        placed = 0
        for ln in version["lines"]:
            if not self.consumed(ln):
                continue
            self.lines_seen.add((version["id"], ln["lineNo"]))
            comp = ln["component"]
            assert comp not in above, "cycle at " + comp
            cq = self.need(ln, qty)
            sub = self.pick(comp, cq)
            if ln["type"] == "phantom":
                assert sub is not None, "phantom without BOM " + comp
                placed += self.place(sub, cq, level, above | {comp})
                continue
            self.nodes += 1
            placed += 1
            assert self.nodes <= MAX_NODES
            children = self.place(sub, cq, level + 1, above | {comp}) if sub is not None and level < self.levels else 0
            if children == 0:
                self.flat[comp] = self.flat.get(comp, F(0)) + cq
        return placed


def write_csv(path, flat):
    with open(path, "w") as out:
        out.write("productId,quantity\n")
        for p in sorted(flat):
            out.write("%s,%s\n" % (p, dec_str(flat[p])))


# ------------------------------------------------- existing tree fixture

def tree_fixture():
    sizes = [1, 4, 12, 30, 60, 150, 300, 1444]
    pid = lambda l, i: "L%d-%04d" % (l, i)
    lines = {}
    for l in range(1, len(sizes)):
        for i in range(sizes[l]):
            ln = {"lineNo": i, "component": pid(l, i), "qty": str(i % 5 + 1), "type": "item"}
            if i % 3 == 0:
                ln["scrapVarPct"], ln["roundUp"] = "5", "quantity"
            if l == 4 and i % 10 == 0:
                ln["type"] = "phantom"
            lines.setdefault(pid(l - 1, i % sizes[l - 1]), []).append(ln)
    return [{"id": "v-" + p, "productId": p, "effectiveFrom": "2025-01-01", "status": "active", "lines": ls}
            for p, ls in lines.items()]


def check_tree():
    summary = json.loads((HERE / "bom-golden-7x2000.json").read_text())
    versions = tree_fixture()
    oracle = Oracle(versions, "", date.fromisoformat(summary["asOf"]), 7)
    flat, nodes = oracle.explode("L0-0000", frac(summary["requestQty"]))
    digest = hashlib.sha256("".join("%s,%s\n" % (p, dec_str(flat[p])) for p in sorted(flat)).encode()).hexdigest()
    got = {
        "bomLines": sum(len(v["lines"]) for v in versions), "treeNodes": nodes, "flatCount": len(flat),
        "totalQuantity": dec_str(sum(flat.values())), "sha256": digest,
    }
    for k, v in got.items():
        assert summary[k] == v, "bom-golden-7x2000.json %s = %r, oracle %r" % (k, summary[k], v)
    write_csv(HERE / "bom-golden-7x2000.csv", flat)
    print("tree fixture: summary confirmed", got)


# --------------------------------------------- adversarial DAG fixture

SIZES = [1, 6, 20, 55, 120, 220, 360, 560]  # products on levels 0..7
RAW = 40  # level-8 raw materials, consumed only by kits
KITS = 10  # level-7 products that have their own BOM
PRIMARY_LINES = 2000
PHANTOMS = 40


def build_dag():
    rng = random.Random(20260301)
    pid = lambda l, i: "L%d-%04d" % (l, i)
    levels = [[pid(l, i) for i in range(n)] for l, n in enumerate(SIZES)] + [[pid(8, i) for i in range(RAW)]]
    level_of = {p: l for l, ps in enumerate(levels) for p in ps}
    kits = rng.sample(levels[7], KITS)
    parents = [p for l in range(7) for p in levels[l]] + kits
    edges = []

    def component_for(parent):
        if parent in kits:
            return rng.choice(levels[8])
        l = level_of[parent]
        step = rng.choices([1, 2, 3], [80, 15, 5])[0]
        return rng.choice(levels[min(l + step, 7)])

    for l in range(1, 9):  # every product has at least one parent
        for c in levels[l]:
            if l == 8:
                edges.append((rng.choice(kits), c))
                continue
            up = rng.choices([1, 2, 3], [85, 10, 5])[0]
            edges.append((rng.choice(levels[max(l - up, 0)]), c))
    links = len(edges)
    with_lines = {p for p, _ in edges}
    for p in parents:  # every BOM has at least one line
        if p not in with_lines:
            edges.append((p, component_for(p)))
    assert len(edges) <= PRIMARY_LINES, len(edges)
    while len(edges) < PRIMARY_LINES:  # shared sub-assemblies and parts
        p = rng.choice(parents)
        edges.append((p, component_for(p)))

    has_bom = set(parents)

    def line(no, parent, comp, link):
        sub = comp in has_bom
        ln = {"lineNo": no, "component": comp, "type": "item"}
        if sub or rng.random() < 0.6:
            ln["qty"] = str(rng.choices([1, 2, 3, 4, 6, 12], [40, 25, 12, 10, 8, 5])[0] if sub else rng.randint(1, 12))
        else:
            ln["qty"] = rng.choice(["0.25", "0.5", "1.5", "2.75", "0.125", "3.2", "0.04"])
        if rng.random() < (0.7 if sub else 0.4):
            ln["roundUp"] = "quantity"
        if rng.random() < 0.3:
            ln["scrapVarPct"] = rng.choice(["2", "5", "10"] if sub else ["1.5", "2", "2.5", "5"])
        if rng.random() < 0.08:
            ln["scrapConst"] = rng.choice(["1", "0.5", "2"])
        if rng.random() < 0.08:
            ln["series"] = rng.choice(["2", "10"] if sub else ["2", "4", "10", "100"])
        # The one line linking a product to the structure is always consumed;
        # by- and co-products are outputs, so they name parts without a BOM.
        r = 1 if link else rng.random()
        if r < 0.02 and not sub:
            ln["type"] = "by_product"
        elif r < 0.03 and not sub:
            ln["type"] = "co_product"
        r = 1 if link else rng.random()
        if r < 0.03:
            ln["effectiveTo"] = "2026-02-28"
        elif r < 0.06:
            ln["effectiveFrom"] = "2026-03-02"
        elif r < 0.07:
            ln["effectiveFrom"] = "2026-03-01"
        elif r < 0.08:
            ln["effectiveTo"] = "2026-03-01"
        return ln

    bom = {p: [] for p in parents}
    for i, (p, c) in enumerate(edges):
        bom[p].append(line(10 * (len(bom[p]) + 1), p, c, i < links))

    phantom_pool = [ln for p in parents for ln in bom[p]
                    if ln["type"] == "item" and ln["component"] in has_bom and 2 <= level_of[ln["component"]] <= 6]
    for ln in rng.sample(phantom_pool, PHANTOMS):
        ln["type"] = "phantom"

    def decoy(lines):
        out = []
        for ln in lines:
            d = dict(ln)
            d["qty"] = dec_str(frac(ln["qty"]) + 1)
            out.append(d)
        out.append({"lineNo": 10 * (len(out) + 1), "component": "DECOY-%04d" % rng.randint(0, 9999), "qty": "7", "type": "item"})
        return out

    versions, seq = [], [0]

    def version(product, lines, frm, to=None, status="active", site=None, from_qty=None, to_qty=None):
        seq[0] += 1
        v = {"id": "V%05d" % seq[0], "productId": product, "effectiveFrom": frm, "status": status, "lines": lines}
        if to:
            v["effectiveTo"] = to
        if site:
            v["siteId"] = site
        if from_qty is not None:
            v["fromQty"] = from_qty
        if to_qty is not None:
            v["toQty"] = to_qty
        versions.append(v)

    variants = ["plain", "superseded", "ends-on-as-of", "future-decoy", "site-override", "other-site", "draft-decoy", "qty-range"]
    weights = [55, 10, 8, 7, 8, 5, 5, 2]
    mix = {k: 0 for k in variants}
    for p in parents:
        lines = bom[p]
        kind = "plain" if p == levels[0][0] else rng.choices(variants, weights)[0]
        mix[kind] += 1
        if kind == "plain":
            version(p, lines, "2025-01-01")
        elif kind == "superseded":
            version(p, decoy(lines), "2024-01-01", "2026-02-28", status="expired")
            version(p, lines, "2026-03-01")
        elif kind == "ends-on-as-of":
            version(p, lines, "2025-06-01", "2026-03-01")
            version(p, decoy(lines), "2026-03-02")
        elif kind == "future-decoy":
            version(p, lines, "2025-01-01", "2026-06-30")
            version(p, decoy(lines), "2026-07-01")
        elif kind == "site-override":
            version(p, decoy(lines), "2025-01-01")
            version(p, lines, "2025-01-01", site=SITE)
        elif kind == "other-site":
            version(p, decoy(lines), "2025-01-01", site="S2")
            version(p, lines, "2025-01-01")
        elif kind == "draft-decoy":
            version(p, decoy(lines), "2025-01-01", status="draft", site=SITE)
            version(p, lines, "2025-01-01")
        else:  # quantity-ranged: small runs use the primary lines, large runs an alternative
            version(p, lines, "2025-01-01", to_qty="50")
            alt = [dict(ln, qty=dec_str(frac(ln["qty"]) * 2)) for ln in lines]
            version(p, alt, "2025-01-01", from_qty="50.000001")
    return versions, levels[0][0], mix, kits


def write_dag():
    versions, root, mix, kits = build_dag()
    fixture = {"root": root, "siteId": SITE, "asOf": AS_OF.isoformat(), "quantity": "10", "levels": 7}
    oracle = Oracle(versions, SITE, AS_OF, fixture["levels"])
    flat, nodes = oracle.explode(root, frac(fixture["quantity"]))
    primary = sum(len(v["lines"]) for v in versions if not v.get("fromQty") and v["status"] != "draft"
                  and not any(ln["component"].startswith("DECOY") for ln in v["lines"]))
    assert primary == PRIMARY_LINES, primary
    fixture["expected"] = {
        "treeNodes": nodes, "flatCount": len(flat), "totalQuantity": dec_str(sum(flat.values())),
        "versionsUsed": len(oracle.used_versions), "consumedLinesReached": len(oracle.lines_seen),
    }
    assert not any(p.startswith("DECOY") for p in flat), "a decoy version was picked"
    fixture["versions"] = versions
    (HERE / "bom-dag-7x2000.json").write_text(json.dumps(fixture, separators=(",", ":")) + "\n")
    write_csv(HERE / "bom-dag-7x2000.csv", flat)
    lines = [ln for v in versions for ln in v["lines"]]
    print("dag fixture:", fixture["expected"], "versions", len(versions), "lines incl. decoys", len(lines))
    print("  version mix", mix)
    print("  line mix: phantom %d, by %d, co %d, dated %d, roundUp %d, varScrap %d, constScrap %d, series %d, decimal qty %d" % (
        sum(ln["type"] == "phantom" for ln in lines), sum(ln["type"] == "by_product" for ln in lines),
        sum(ln["type"] == "co_product" for ln in lines), sum("effectiveFrom" in ln or "effectiveTo" in ln for ln in lines),
        sum("roundUp" in ln for ln in lines), sum("scrapVarPct" in ln for ln in lines),
        sum("scrapConst" in ln for ln in lines), sum("series" in ln for ln in lines), sum("." in ln["qty"] for ln in lines)))
    print("  kits reached in flat:", sum(1 for p in flat if p.startswith("L8-")), "raw products;",
          "kits left as leaves:", sum(1 for k in kits if k in flat))
    print("  exactness: max fractional digits %d (Go divides to 16), smallest distance of a rounded-up value to an integer %s" % (
        oracle.max_digits, float(oracle.ceil_margin)))


if __name__ == "__main__":
    check_tree()
    write_dag()
