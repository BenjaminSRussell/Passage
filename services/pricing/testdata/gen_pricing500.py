#!/usr/bin/env python3
"""Generate testdata/pricing-500.json: the pricing-500 resolver fixture (Wave 26 AC1, PRD 5.26.9).

The expected prices and explanations are computed by an independent reference resolver written
here, in Python, from the PRD text (BR-26.1, BR-26.3, BR-26.4, BR-26.10, FR-26.3, FR-26.5,
FR-26.6) and the service README (campaign ranking, surcharge threshold). It shares no code with
the Go engine. Before it writes anything it checks itself against:

  * the PRD acceptance examples AC-26.1, AC-26.2, AC-26.3 and AC-26.5, as hand-derived literals;
  * the 19 hand-worked cases in testdata/pricing-scenarios.json (slice 1).

Run from anywhere (stdlib only, deterministic, seed 26):

    python3 -I services/pricing/testdata/gen_pricing500.py

Reference rules, as implemented below:

  R1  Accesses run in the BR-26.1 order 1..9 (customer x variant ... all x all).
  R2  A record is a candidate at an access when it is ACTIVE, of the step's condition type, in the
      document currency, valid_from <= pricing date <= valid_to (inclusive days), its key matches
      the access (kind "all" ignores the key), from_quantity <= the quantity the step compares and,
      for line-level steps, its unit is empty or equals the line unit.
  R3  Candidates rank by greatest from_quantity, then campaign record before plain record (the
      campaign dimension is one more dimension matched), then lowest resulting value, then id.
  R4  The first-ranked candidate wins the access. The search stops there unless the winner has
      search_next; across accesses the lowest value is kept, and an equal value keeps the earlier
      access.
  R5  PRICE value = amount x (1 - p1/100) x (1 - p2/100), half-even to 6 decimals. A discount value
      = max(current - amount, 0) x (1 - p1/100) x (1 - p2/100), half-even to 6 decimals (amount
      before percentage, floored at zero). SURCHARGE value = current + amount + current x p1/100.
  R6  Steps run PRICE, LINE_DISC, MULTILINE_DISC per line; an exclusive winner suppresses every
      later discount step of that line, including its share of the total discount. No PRICE winner
      is PRC-0001 on that line only; the line is left out of every total.
  R7  MULTILINE_DISC compares the summed quantity of all lines with the same product group and unit;
      a line without a product group compares its own quantity.
  R8  Line net = half-even(quantity x net unit, ISO 4217 minor units). Subtotal = sum of line nets.
  R9  TOTAL_DISC runs once on base = sum of the line nets not protected by an exclusive winner,
      comparing the summed quantity of those lines, without a unit filter; the discounted base is
      rounded once to minor units and total discount = base - rounded value. Not run when base = 0.
  R10 SURCHARGE runs when the threshold (250 USD, 200 EUR, none otherwise) is positive and
      0 < subtotal < threshold, on the order net after total discount, comparing the summed quantity
      of all priced lines; the surcharged net is rounded once and surcharge = rounded - order net.
"""
import json
import os
import random
import sys
from decimal import ROUND_HALF_EVEN, Decimal as D, getcontext

getcontext().prec = 60

# ISO 4217 minor units for the currencies the fixture uses.
MINOR = {"USD": 2, "EUR": 2, "JPY": 0, "KWD": 3, "CLF": 4}
THRESHOLD = {"USD": D("250"), "EUR": D("200")}
ACCESSES = [
    (1, "customer", "variant"), (2, "customer", "product"), (3, "customer", "product_group"),
    (4, "customer_group", "product"), (5, "customer_group", "product_group"), (6, "customer", "all"),
    (7, "all", "product"), (8, "all", "product_group"), (9, "all", "all"),
]
LINE_STEPS = ["LINE_DISC", "MULTILINE_DISC"]
ORDER_LEVEL = {"TOTAL_DISC", "SURCHARGE"}


def dec(raw):
    return D(raw) if raw not in (None, "") else D(0)


def half_even(x, places):
    return x.quantize(D(1).scaleb(-places), rounding=ROUND_HALF_EVEN)


def fmt(x):
    """Plain decimal string without exponent; trailing zeros stripped."""
    s = format(x.normalize(), "f")
    return "0" if s in ("-0", "") else s


def factor(r):
    return (D(100) - dec(r.get("discPct1"))) * (D(100) - dec(r.get("discPct2"))) / D(10000)


def value_of(ctype, current, r):
    if ctype == "PRICE":
        return half_even(dec(r.get("amount")) * factor(r), 6)
    if ctype == "SURCHARGE":
        return half_even(current + dec(r.get("amount")) + current * dec(r.get("discPct1")) / D(100), 6)
    base = current - dec(r.get("amount"))
    if base < 0:
        base = D(0)
    return half_even(base * factor(r), 6)


def key_matches(r, acc, hdr, ctx):
    _, ckind, pkind = acc
    if r.get("customerKind") != ckind or r.get("productKind") != pkind:
        return False
    if ckind == "customer" and r.get("customerKey", "") != hdr.get("customerId", ""):
        return False
    if ckind == "customer_group" and r.get("customerKey", "") != hdr.get("customerGroupId", ""):
        return False
    if pkind == "product":
        return r.get("productKey", "") == ctx["productId"]
    if pkind == "product_group":
        return r.get("productKey", "") == ctx["productGroupId"]
    if pkind == "variant":
        return r.get("productKey", "") == ctx["productId"] and r.get("variant", "") == ctx["variant"]
    return True


def eligible(ctype, hdr, ctx, r):
    if r.get("type") != ctype or r.get("status", "ACTIVE") != "ACTIVE":
        return False
    if r.get("currency") != hdr["currency"]:
        return False
    if not (r["validFrom"] <= hdr["pricingDate"] <= r["validTo"]):
        return False
    if ctype not in ORDER_LEVEL and r.get("unit", "") not in ("", ctx["unit"]):
        return False
    return dec(r.get("fromQuantity")) <= ctx["qty"]


def evaluate(ctype, hdr, ctx, records, current):
    """One condition type through the access sequence. Returns (winner, value, access, trace, matched)."""
    best, best_value, best_access = None, None, 0
    accesses, stopped, matched = [], False, set()
    for acc in ACCESSES:
        seq = acc[0]
        if stopped:
            accesses.append("%d:x" % seq)
            continue
        cands = [r for r in records if eligible(ctype, hdr, ctx, r) and key_matches(r, acc, hdr, ctx)]
        cands.sort(key=lambda r: (-dec(r.get("fromQuantity")), 0 if r.get("campaignId") else 1,
                                  value_of(ctype, current, r), r["id"].encode()))
        matched.update(r["id"] for r in cands)
        if not cands:
            accesses.append("%d:-" % seq)
            continue
        accesses.append("%d:%s" % (seq, ",".join(r["id"] for r in cands)))
        w = cands[0]
        v = value_of(ctype, current, w)
        if best is None or v < best_value:
            best, best_value, best_access = w, v, seq
        if not w.get("searchNext"):
            stopped = True
    trace = {"condition": ctype, "selected": best["id"] if best else "", "accesses": accesses}
    return best, best_value, best_access, trace, len(matched)


def resolve(case):
    hdr, lines, records = case["header"], case["lines"], case["records"]
    minor = MINOR.get(hdr["currency"], 2)
    threshold = dec(case.get("smallOrderThreshold"))
    group_qty = {}
    for l in lines:
        if l.get("productGroupId"):
            k = (l["productGroupId"], l["unit"])
            group_qty[k] = group_qty.get(k, D(0)) + D(l["quantity"])
    out = {"lines": [], "applied": [], "trace": []}
    overlap = 0
    subtotal, base, base_qty, all_qty = D(0), D(0), D(0), D(0)
    for i, l in enumerate(lines, start=1):
        qty = D(l["quantity"])
        ctx = {"productId": l["productId"], "productGroupId": l.get("productGroupId", ""),
               "variant": l.get("variant", ""), "unit": l["unit"], "qty": qty}
        lr = {"lineNo": i, "applied": [], "trace": [], "errorCode": "", "exclusive": False,
              "unitPrice": D(0), "netUnitPrice": D(0), "lineNet": D(0)}
        w, v, acc, tr, m = evaluate("PRICE", hdr, ctx, records, D(0))
        overlap = max(overlap, m)
        lr["trace"].append(tr)
        if w is None:
            lr["errorCode"] = "PRC-0001"
            out["lines"].append(lr)
            continue
        lr["applied"].append("%s@%d" % (w["id"], acc))
        lr["unitPrice"], net, suppressed = v, v, bool(w.get("exclusive"))
        for ctype in LINE_STEPS:
            if suppressed:
                break
            sctx = dict(ctx)
            if ctype == "MULTILINE_DISC" and ctx["productGroupId"]:
                sctx["qty"] = group_qty[(ctx["productGroupId"], ctx["unit"])]
            w, v, acc, tr, m = evaluate(ctype, hdr, sctx, records, net)
            overlap = max(overlap, m)
            lr["trace"].append(tr)
            if w is None:
                continue
            net, suppressed = v, bool(w.get("exclusive"))
            lr["applied"].append("%s@%d" % (w["id"], acc))
        lr["netUnitPrice"] = net
        lr["lineNet"] = half_even(qty * net, minor)
        lr["exclusive"] = suppressed
        out["lines"].append(lr)
        subtotal += lr["lineNet"]
        all_qty += qty
        if not suppressed:
            base += lr["lineNet"]
            base_qty += qty
    order_ctx = {"productId": "", "productGroupId": "", "variant": "", "unit": ""}
    total_discount, surcharge, order_net = D(0), D(0), subtotal
    if base > 0:
        w, v, acc, tr, m = evaluate("TOTAL_DISC", hdr, dict(order_ctx, qty=base_qty), records, base)
        overlap = max(overlap, m)
        out["trace"].append(tr)
        if w is not None:
            total_discount = base - half_even(v, minor)
            order_net = subtotal - total_discount
            out["applied"].append("%s@%d" % (w["id"], acc))
    if threshold > 0 and 0 < subtotal < threshold:
        w, v, acc, tr, m = evaluate("SURCHARGE", hdr, dict(order_ctx, qty=all_qty), records, order_net)
        overlap = max(overlap, m)
        out["trace"].append(tr)
        if w is not None:
            surcharged = half_even(v, minor)
            surcharge = surcharged - order_net
            order_net = surcharged
            out["applied"].append("%s@%d" % (w["id"], acc))
    out.update(subtotal=subtotal, totalDiscount=total_discount, surcharge=surcharge, orderNet=order_net,
               maxCandidates=overlap)
    return out


# ---------------------------------------------------------------- self checks (hand-derived)

def _rec(id_, ctype, ck, ckey, pk, pkey, amount="0", **kw):
    r = {"id": id_, "type": ctype, "customerKind": ck, "customerKey": ckey, "productKind": pk,
         "productKey": pkey, "currency": kw.pop("currency", "USD"), "unit": kw.pop("unit", "EA"),
         "fromQuantity": kw.pop("fromQuantity", "0"), "amount": amount,
         "validFrom": "2027-01-01", "validTo": "2027-12-31"}
    r.update(kw)
    return r


def self_check():
    hdr = {"customerId": "C1", "customerGroupId": "PG-A", "currency": "USD", "pricingDate": "2027-03-10"}
    line = [{"productId": "P1", "unit": "EA", "quantity": "1"}]
    a2 = _rec("A2", "PRICE", "customer", "C1", "product", "P1", "95.00")
    a4 = _rec("A4", "PRICE", "customer_group", "PG-A", "product", "P1", "90.00")
    # AC-26.1: 95.00, access 4 not evaluated.
    r = resolve({"header": hdr, "lines": line, "records": [a2, a4]})
    assert r["lines"][0]["netUnitPrice"] == D("95.00") and r["lines"][0]["trace"][0]["accesses"][3] == "4:x", r
    # AC-26.2: search_next on A2 gives 90.00 and both accesses in the trace.
    r = resolve({"header": hdr, "lines": line, "records": [dict(a2, searchNext=True), a4]})
    acc = r["lines"][0]["trace"][0]["accesses"]
    assert r["lines"][0]["netUnitPrice"] == D("90.00") and acc[1] == "2:A2" and acc[3] == "4:A4", r
    # AC-26.3: breaks 0 -> 10.00, 100 -> 9.50, 500 -> 9.00 EUR; 500 -> 9.00, 499 -> 9.50.
    eur = dict(hdr, currency="EUR")
    br = [_rec("B%d" % f, "PRICE", "customer", "C1", "product", "P1", p, currency="EUR", fromQuantity=str(f))
          for f, p in ((0, "10.00"), (100, "9.50"), (500, "9.00"))]
    for qty, want in (("500", "9.00"), ("499", "9.50")):
        r = resolve({"header": eur, "lines": [{"productId": "P1", "unit": "EA", "quantity": qty}], "records": br})
        assert r["lines"][0]["netUnitPrice"] == D(want), (qty, r)
    # AC-26.5: 10% / 5% on 100.00 USD x 3 = 256.50.
    r = resolve({"header": hdr, "lines": [{"productId": "P1", "unit": "EA", "quantity": "3"}],
                 "records": [_rec("P", "PRICE", "customer", "C1", "product", "P1", "100.00", discPct1="10", discPct2="5")]})
    assert r["lines"][0]["lineNet"] == D("256.50"), r
    # Hand-worked: half-even at the cent (0.125 -> 0.12, 0.135 -> 0.14) and at the yen (2.5 -> 2).
    assert half_even(D("0.125"), 2) == D("0.12") and half_even(D("0.135"), 2) == D("0.14") and half_even(D("2.5"), 0) == D("2")
    # The 19 hand-worked slice-1 cases.
    here = os.path.dirname(os.path.abspath(__file__))
    with open(os.path.join(here, "pricing-scenarios.json")) as f:
        hand = json.load(f)["cases"]
    for c in hand:
        r = resolve(c)
        e = c["expect"]
        assert r["orderNet"] == D(e["orderNet"]), (c["name"], r["orderNet"], e["orderNet"])
        if e.get("totalDiscount"):
            assert r["totalDiscount"] == D(e["totalDiscount"]), c["name"]
        assert r["applied"] == (e.get("applied") or []), (c["name"], r["applied"])
        for got, want in zip(r["lines"], e["lines"]):
            assert got["errorCode"] == want.get("errorCode", ""), c["name"]
            if want.get("netUnitPrice"):
                assert got["netUnitPrice"] == D(want["netUnitPrice"]), (c["name"], got["netUnitPrice"])
            assert got["lineNet"] == D(want["lineNet"]), (c["name"], got["lineNet"], want["lineNet"])
            assert got["exclusive"] == want.get("exclusive", False), c["name"]
            assert got["applied"] == (want.get("applied") or []), (c["name"], got["applied"], want.get("applied"))
    return len(hand)


# ---------------------------------------------------------------- scenario families

DATE = "2027-03-10"
CUSTOMERS = ["C1", "C2"]
CGROUPS = ["CG-A", "CG-B"]
PRODUCTS = {"P1": "PG1", "P2": "PG1", "P3": "PG2", "P4": ""}
VARIANTS = ["RED", "BLUE"]


class Gen:
    def __init__(self, rng, currency="USD"):
        self.rng, self.currency, self.records, self.n = rng, currency, [], 0
        ids = ["R%03d" % i for i in range(1, 200)]
        rng.shuffle(ids)  # id order is not creation order, so the id tie-break is exercised
        self.ids = ids

    def add(self, ctype, ckind, pkind, hdr=None, line=None, amount="0", **kw):
        """A record at (ckind, pkind) keyed to match hdr/line when given, else to a random key."""
        rng = self.rng
        ckey = ""
        if ckind == "customer":
            ckey = hdr["customerId"] if hdr and rng.random() < 0.85 else rng.choice(CUSTOMERS)
        elif ckind == "customer_group":
            ckey = hdr["customerGroupId"] if hdr and rng.random() < 0.85 else rng.choice(CGROUPS)
        pkey, variant = "", ""
        if pkind in ("product", "variant"):
            pkey = line["productId"] if line and rng.random() < 0.85 else rng.choice(list(PRODUCTS))
        if pkind == "variant":
            variant = (line.get("variant") or rng.choice(VARIANTS)) if line and rng.random() < 0.85 else rng.choice(VARIANTS)
        if pkind == "product_group":
            pkey = (line.get("productGroupId") or "PG1") if line and rng.random() < 0.85 else rng.choice(["PG1", "PG2"])
        r = {"id": self.ids[self.n], "type": ctype, "customerKind": ckind, "customerKey": ckey,
             "productKind": pkind, "productKey": pkey, "variant": variant, "currency": kw.pop("currency", self.currency),
             "unit": kw.pop("unit", "EA"), "fromQuantity": kw.pop("fromQuantity", "0"), "amount": amount,
             "validFrom": kw.pop("validFrom", "2027-01-01"), "validTo": kw.pop("validTo", "2027-12-31")}
        for k, v in kw.items():
            if v not in (None, "", False, "0"):
                r[k] = v
        self.n += 1
        self.records.append(r)
        return r


def money(rng, lo=1, hi=500, places=2):
    return fmt(D(rng.randint(lo * 10 ** places, hi * 10 ** places)).scaleb(-places))


def pct(rng, choices=(0, 0, 2, 5, 7.5, 10, 12.5, 15, 25, 33.3333, 50, 100)):
    return fmt(D(str(rng.choice(choices))))


def header(rng, currency="USD", group=True):
    return {"customerId": rng.choice(CUSTOMERS), "customerGroupId": rng.choice(CGROUPS) if group else "",
            "currency": currency, "pricingDate": DATE}


def mkline(rng, product=None, qty=None, unit="EA", variant=None):
    p = product or rng.choice(list(PRODUCTS))
    l = {"productId": p, "productGroupId": PRODUCTS[p], "unit": unit,
         "quantity": qty or str(rng.choice([1, 2, 3, 5, 7, 10, 12, 25, 99, 100, 101, 250, 499, 500, 1000]))}
    if variant:
        l["variant"] = variant
    return l


def line_kinds(line):
    kinds = [a for a in ACCESSES if a[2] != "variant" or line.get("variant")]
    if not line.get("productGroupId"):
        kinds = [a for a in kinds if a[2] != "product_group"]
    return kinds


def fam_overlap_access(rng):
    hdr = header(rng)
    lines = [mkline(rng, variant=rng.choice([None, "RED"])) for _ in range(rng.randint(1, 2))]
    g = Gen(rng)
    for l in lines:
        for _ in range(rng.randint(4, 10)):
            _, ck, pk = rng.choice(line_kinds(l))
            g.add("PRICE", ck, pk, hdr, l, money(rng, 5, 200), searchNext=rng.random() < 0.3,
                  discPct1=pct(rng) if rng.random() < 0.2 else "0")
    return hdr, lines, g.records


def fam_break_ladder(rng):
    hdr = header(rng, rng.choice(["USD", "EUR"]))
    l = mkline(rng)
    breaks = [0, 10, 50, 100, 500]
    b = rng.choice(breaks[1:])
    l["quantity"] = rng.choice([str(b - 1), str(b), str(b + 1), fmt(D(b) - D("0.001")), fmt(D(b) + D("0.5"))])
    g = Gen(rng, hdr["currency"])
    for _ in range(rng.randint(1, 3)):
        _, ck, pk = rng.choice(line_kinds(l))
        sn = rng.random() < 0.35
        for f in sorted(rng.sample(breaks, rng.randint(2, 4))):
            # prices are not monotonic in the break: a higher break can be dearer (break wins anyway)
            g.add("PRICE", ck, pk, hdr, l, money(rng, 5, 50), fromQuantity=str(f), searchNext=sn)
    return hdr, [l], g.records


def fam_validity(rng):
    hdr = header(rng)
    l = mkline(rng)
    g = Gen(rng)
    windows = [("2027-03-10", "2027-03-10"), ("2027-01-01", "2027-03-09"), ("2027-03-11", "2027-12-31"),
               ("2027-03-10", "2027-12-31"), ("2026-01-01", "2027-03-10"), ("2027-01-01", "2027-12-31")]
    for _ in range(rng.randint(3, 7)):
        _, ck, pk = rng.choice(line_kinds(l))
        vf, vt = rng.choice(windows)
        status = rng.choice(["ACTIVE", "ACTIVE", "ACTIVE", "DRAFT", "EXPIRED", "WITHDRAWN"])
        g.add("PRICE", ck, pk, hdr, l, money(rng, 5, 99), validFrom=vf, validTo=vt, searchNext=rng.random() < 0.3,
              status=status if status != "ACTIVE" else None)
    if rng.random() < 0.6:
        g.add("LINE_DISC", "all", "all", hdr, l, "0", discPct1=pct(rng), validFrom=rng.choice(windows)[0], validTo="2027-03-10")
    return hdr, [l], g.records


def fam_currency_unit(rng):
    cur = rng.choice(["USD", "EUR", "JPY"])
    hdr = header(rng, cur)
    lines = [mkline(rng, unit=rng.choice(["EA", "BOX"])) for _ in range(rng.randint(1, 3))]
    g = Gen(rng, cur)
    for l in lines:
        for _ in range(rng.randint(2, 5)):
            _, ck, pk = rng.choice(line_kinds(l))
            g.add("PRICE", ck, pk, hdr, l, money(rng, 1, 900, 0 if cur == "JPY" else 2),
                  currency=rng.choice([cur, cur, "USD", "EUR", "JPY"]), unit=rng.choice(["EA", "BOX", ""]),
                  searchNext=rng.random() < 0.25)
        if rng.random() < 0.5:
            g.add("LINE_DISC", "all", "all", hdr, l, "0", discPct1=pct(rng), unit=rng.choice(["EA", "BOX", ""]))
    return hdr, lines, g.records


def fam_campaign(rng):
    hdr = header(rng)
    l = mkline(rng)
    g = Gen(rng)
    for _ in range(rng.randint(1, 3)):
        _, ck, pk = rng.choice(line_kinds(l))
        f = rng.choice(["0", "0", "10"])
        g.add("PRICE", ck, pk, hdr, l, money(rng, 10, 60), fromQuantity=f, searchNext=rng.random() < 0.3)
        g.add("PRICE", ck, pk, hdr, l, money(rng, 10, 60), fromQuantity=rng.choice(["0", f]), campaignId="CMP-1",
              searchNext=rng.random() < 0.3)
    if rng.random() < 0.5:
        _, ck, pk = rng.choice(line_kinds(l))
        g.add("LINE_DISC", ck, pk, hdr, l, "0", discPct1=pct(rng))
        g.add("LINE_DISC", ck, pk, hdr, l, "0", discPct1=pct(rng), campaignId="CMP-2")
    return hdr, [l], g.records


def fam_discount_stack(rng):
    cur = rng.choice(["USD", "EUR"])
    hdr = header(rng, cur)
    lines = [mkline(rng) for _ in range(rng.randint(1, 6))]
    g = Gen(rng, cur)
    for l in lines:
        _, ck, pk = rng.choice(line_kinds(l))
        g.add("PRICE", ck, pk, hdr, l, money(rng, 1, 300), exclusive=rng.random() < 0.1,
              discPct1=pct(rng) if rng.random() < 0.3 else "0", discPct2=pct(rng) if rng.random() < 0.2 else "0")
        for _ in range(rng.randint(0, 3)):
            _, ck, pk = rng.choice(line_kinds(l))
            g.add("LINE_DISC", ck, pk, hdr, l, money(rng, 0, 20) if rng.random() < 0.4 else "0",
                  discPct1=pct(rng), discPct2=pct(rng) if rng.random() < 0.3 else "0",
                  fromQuantity=rng.choice(["0", "0", "10", "100"]), exclusive=rng.random() < 0.15,
                  searchNext=rng.random() < 0.3)
    for _ in range(rng.randint(0, 2)):
        pk = rng.choice(["product_group", "all"])
        ck = rng.choice(["customer", "customer_group", "all"]) if pk == "product_group" else rng.choice(["customer", "all"])
        g.add("MULTILINE_DISC", ck, pk, hdr, rng.choice(lines), "0", discPct1=pct(rng),
              fromQuantity=rng.choice(["0", "20", "100", "500"]), exclusive=rng.random() < 0.1)
    for _ in range(rng.randint(0, 3)):
        g.add("TOTAL_DISC", rng.choice(["customer", "all", "all", "customer_group"]), "all", hdr, None,
              money(rng, 0, 50) if rng.random() < 0.3 else "0", discPct1=pct(rng),
              fromQuantity=rng.choice(["0", "0", "10", "200"]), searchNext=rng.random() < 0.3)
    return hdr, lines, g.records


def fam_multiline(rng):
    hdr = header(rng)
    lines = [mkline(rng, product=rng.choice(["P1", "P2", "P3", "P4"]), unit=rng.choice(["EA", "EA", "BOX"]),
                    qty=str(rng.choice([5, 10, 15, 30, 45, 60, 99]))) for _ in range(rng.randint(2, 6))]
    g = Gen(rng)
    for l in lines:
        g.add("PRICE", "all", "product", hdr, l, money(rng, 2, 80))
    for _ in range(rng.randint(1, 4)):
        pk = rng.choice(["product_group", "product_group", "all"])
        ck = rng.choice(["customer", "customer_group", "all"]) if pk == "product_group" else rng.choice(["customer", "all"])
        g.add("MULTILINE_DISC", ck, pk, hdr, rng.choice(lines), money(rng, 0, 3) if rng.random() < 0.2 else "0",
              discPct1=pct(rng), fromQuantity=rng.choice(["0", "50", "60", "100", "150"]),
              unit=rng.choice(["EA", "EA", "BOX", ""]), searchNext=rng.random() < 0.3)
    return hdr, lines, g.records


def fam_rounding(rng):
    cur = rng.choice(["USD", "EUR", "JPY", "KWD", "CLF"])
    hdr = header(rng, cur)
    g = Gen(rng, cur)
    lines = []
    for _ in range(rng.randint(1, 4)):
        l = mkline(rng, qty=rng.choice(["1", "3", "7", "0.5", "2.25", "1.125", "13.333", "1000"]))
        lines.append(l)
        _, ck, pk = rng.choice(line_kinds(l))
        if rng.random() < 0.4:
            # a constructed half-even tie at the currency's minor unit (x.xx5 with one unit)
            amount = fmt(D(rng.randint(1, 9999)).scaleb(-MINOR[cur]) + D(5).scaleb(-MINOR[cur] - 1))
            l["quantity"] = "1"
        else:
            amount = fmt(D(rng.randint(1, 99999999)).scaleb(-6))
        g.add("PRICE", ck, pk, hdr, l, amount, discPct1=rng.choice(["0", "0", "3", "33.3333", "12.5"]),
              discPct2=rng.choice(["0", "0", "7", "0.5"]))
    if rng.random() < 0.5:
        g.add("TOTAL_DISC", "all", "all", hdr, None, "0", discPct1=rng.choice(["2.5", "3", "33.3333", "10"]))
    return hdr, lines, g.records


def fam_surcharge(rng):
    cur = rng.choice(["USD", "EUR"])
    thr = THRESHOLD[cur]
    hdr = header(rng, cur)
    l = mkline(rng, qty="1")
    target = rng.choice([thr - D("0.01"), thr, thr + D("0.01"), thr / 2, D("0.01"), thr * 2])
    g = Gen(rng, cur)
    g.add("PRICE", "all", "product", hdr, l, fmt(target))
    lines = [l]
    if rng.random() < 0.3:
        extra = mkline(rng, product="P4", qty="2")
        lines.append(extra)
        g.add("PRICE", "all", "product", hdr, extra, money(rng, 1, 40))
    for _ in range(rng.randint(1, 3)):
        g.add("SURCHARGE", rng.choice(["customer", "all"]), "all", hdr, None, money(rng, 0, 25),
              discPct1=rng.choice(["0", "0", "2", "5", "10"]), fromQuantity=rng.choice(["0", "0", "2", "3"]),
              searchNext=rng.random() < 0.3)
    if rng.random() < 0.4:
        # a total discount can take the order net under the threshold; the subtotal decides
        g.add("TOTAL_DISC", "all", "all", hdr, None, "0", discPct1=rng.choice(["5", "10", "50"]))
    return hdr, lines, g.records


def fam_ties(rng):
    hdr = header(rng)
    l = mkline(rng)
    g = Gen(rng)
    same = money(rng, 10, 50)
    kinds = line_kinds(l)
    for _ in range(rng.randint(2, 4)):
        _, ck, pk = rng.choice(kinds)
        # equal values at one access (id decides) and across accesses (the earlier access is kept)
        g.add("PRICE", ck, pk, hdr, l, same if rng.random() < 0.7 else money(rng, 10, 50), searchNext=rng.random() < 0.6,
              discPct1=rng.choice(["0", "0", "10"]) if rng.random() < 0.3 else "0")
    if rng.random() < 0.5:
        for _ in range(2):
            g.add("LINE_DISC", "all", "all", hdr, l, "0", discPct1="10", searchNext=True)
    return hdr, [l], g.records


def fam_price_not_found(rng):
    hdr = header(rng)
    lines = [mkline(rng) for _ in range(rng.randint(2, 5))]
    g = Gen(rng)
    priced = rng.sample(range(len(lines)), rng.randint(0, len(lines) - 1))
    for i, l in enumerate(lines):
        if i in priced:
            _, ck, pk = rng.choice(line_kinds(l))
            g.add("PRICE", ck, pk, hdr, l, money(rng, 1, 90))
        else:
            # a near miss: right product, wrong currency, unit, customer, window or status
            miss = rng.choice(["currency", "unit", "customer", "window", "status", "break"])
            kw = {"currency": "EUR"} if miss == "currency" else {"unit": "BOX"} if miss == "unit" else \
                {"validFrom": "2027-04-01"} if miss == "window" else {"status": "WITHDRAWN"} if miss == "status" else \
                {"fromQuantity": "100000"} if miss == "break" else {}
            r = g.add("PRICE", "customer" if miss == "customer" else "all", "product", hdr, l, money(rng, 1, 90), **kw)
            if miss == "customer":
                r["customerKey"] = "C-OTHER"
            else:
                r["productKey"] = l["productId"]
    g.add("TOTAL_DISC", "all", "all", hdr, None, "0", discPct1=pct(rng))
    return hdr, lines, g.records


def fam_variant(rng):
    hdr = header(rng)
    lines = [mkline(rng, product=p, variant=v) for p, v in
             rng.sample([("P1", "RED"), ("P1", "BLUE"), ("P2", "RED"), ("P1", None), ("P3", "BLUE")], rng.randint(1, 3))]
    g = Gen(rng)
    for l in lines:
        for _ in range(rng.randint(2, 4)):
            pk = rng.choice(["variant", "variant", "product"])
            r = g.add("PRICE", rng.choice(["customer", "customer"]) if pk == "variant" else "customer", pk, hdr, l,
                      money(rng, 5, 60), searchNext=rng.random() < 0.3)
            if pk == "variant" and not r["variant"]:
                r["variant"] = rng.choice(VARIANTS)
    return hdr, lines, g.records


def fam_same_key(rng):
    # Two ACTIVE records with one key and overlapping validity: activation refuses this
    # (PRC-0002), but resolve must still be deterministic if such data reaches it.
    hdr = header(rng)
    l = mkline(rng)
    g = Gen(rng)
    _, ck, pk = rng.choice(line_kinds(l))
    a = g.add("PRICE", ck, pk, hdr, l, money(rng, 10, 40))
    b = g.add("PRICE", ck, pk, hdr, l, money(rng, 10, 40) if rng.random() < 0.6 else a["amount"],
              validFrom="2027-02-01", validTo="2027-06-30")
    for k in ("customerKey", "productKey", "variant"):
        b[k] = a[k]
    if rng.random() < 0.5:
        _, ck, pk = rng.choice(line_kinds(l))
        g.add("PRICE", ck, pk, hdr, l, money(rng, 10, 40), searchNext=True)
    return hdr, [l], g.records


def fam_full_order(rng):
    cur = rng.choice(["USD", "EUR", "JPY"])
    hdr = header(rng, cur)
    lines = [mkline(rng, variant=rng.choice([None, None, "RED"]), unit=rng.choice(["EA", "EA", "EA", "BOX"]))
             for _ in range(10)]
    g = Gen(rng, cur)
    places = 0 if cur == "JPY" else 2
    for _ in range(rng.randint(20, 60)):
        l = rng.choice(lines)
        ctype = rng.choice(["PRICE", "PRICE", "PRICE", "LINE_DISC", "LINE_DISC", "MULTILINE_DISC", "TOTAL_DISC"])
        if ctype == "TOTAL_DISC":
            ck, pk = rng.choice(["customer", "all"]), "all"
        elif ctype == "MULTILINE_DISC":
            pk = rng.choice(["product_group", "all"])
            ck = rng.choice(["customer_group", "all"]) if pk == "product_group" else "all"
        else:
            _, ck, pk = rng.choice(line_kinds(l))
        price = ctype == "PRICE"
        g.add(ctype, ck, pk, hdr, l, money(rng, 1, 400, places) if price else (money(rng, 0, 5, places) if rng.random() < 0.3 else "0"),
              discPct1=pct(rng) if not price or rng.random() < 0.2 else "0",
              fromQuantity=rng.choice(["0", "0", "0", "10", "100", "500"]), searchNext=rng.random() < 0.25,
              exclusive=rng.random() < 0.06, unit=rng.choice(["EA", "EA", "", "BOX"]),
              campaignId="CMP-9" if rng.random() < 0.08 else None,
              currency=cur if rng.random() < 0.95 else "USD" if cur != "USD" else "EUR")
    return hdr, lines, g.records


FAMILIES = [
    ("overlap-access", fam_overlap_access, 60), ("break-ladder", fam_break_ladder, 50),
    ("validity-status", fam_validity, 40), ("currency-unit", fam_currency_unit, 30),
    ("campaign", fam_campaign, 35), ("discount-stack", fam_discount_stack, 70),
    ("multiline", fam_multiline, 35), ("rounding", fam_rounding, 40), ("surcharge", fam_surcharge, 35),
    ("ties", fam_ties, 25), ("price-not-found", fam_price_not_found, 20), ("variant", fam_variant, 20),
    ("same-key-overlap", fam_same_key, 15), ("full-10-line-order", fam_full_order, 25),
]


def expect_of(r):
    return {
        "subtotal": fmt(r["subtotal"]), "totalDiscount": fmt(r["totalDiscount"]), "surcharge": fmt(r["surcharge"]),
        "orderNet": fmt(r["orderNet"]), "applied": r["applied"], "trace": r["trace"],
        "lines": [{"lineNo": l["lineNo"], "errorCode": l["errorCode"], "unitPrice": fmt(l["unitPrice"]),
                   "netUnitPrice": fmt(l["netUnitPrice"]), "lineNet": fmt(l["lineNet"]), "exclusive": l["exclusive"],
                   "applied": l["applied"], "trace": l["trace"]} for l in r["lines"]],
    }


def main():
    hand = self_check()
    rng = random.Random(26)
    cases, stats = [], {"overlapping": 0, "searchNextDecided": 0, "exclusive": 0, "priceNotFound": 0,
                        "totalDiscount": 0, "surcharge": 0, "multiline": 0, "campaignWon": 0}
    for fam, fn, count in FAMILIES:
        for i in range(count):
            hdr, lines, records = fn(rng)
            case = {"name": "%s-%03d" % (fam, i + 1), "family": fam, "header": hdr, "lines": lines, "records": records,
                    "smallOrderThreshold": fmt(THRESHOLD.get(hdr["currency"], D(0)))}
            r = resolve(case)
            case["expect"] = expect_of(r)
            cases.append(case)
            # Honest coverage counts, not targets.
            stats["overlapping"] += r["maxCandidates"] >= 2
            stats["exclusive"] += any(l["exclusive"] for l in r["lines"])
            stats["priceNotFound"] += any(l["errorCode"] for l in r["lines"])
            stats["totalDiscount"] += r["totalDiscount"] != 0
            stats["surcharge"] += r["surcharge"] != 0
            applied = [a for l in r["lines"] for a in l["applied"]] + r["applied"]
            ids = {x["id"]: x for x in records}
            stats["multiline"] += any(ids[a.split("@")[0]]["type"] == "MULTILINE_DISC" for a in applied)
            stats["campaignWon"] += any(ids[a.split("@")[0]].get("campaignId") for a in applied)
            stats["searchNextDecided"] += any(
                len([x for x in t["accesses"] if x[-1] not in "x-"]) >= 2
                for l in r["lines"] for t in l["trace"]) or any(
                len([x for x in t["accesses"] if x[-1] not in "x-"]) >= 2 for t in r["trace"])
    out = {
        "description": "pricing-500 (PRD 5.26.9, Wave 26 AC1): generated resolver scenarios. Expected values come from "
                       "the independent Python reference in gen_pricing500.py (rules R1-R10 in its docstring), "
                       "which first reproduces AC-26.1/26.2/26.3/26.5 and the %d hand-worked slice-1 cases. "
                       "Regenerate with: python3 -I services/pricing/testdata/gen_pricing500.py" % hand,
        "seed": 26,
        "coverage": stats,
        "cases": cases,
    }
    here = os.path.dirname(os.path.abspath(__file__))
    path = sys.argv[1] if len(sys.argv) > 1 else os.path.join(here, "pricing-500.json")
    with open(path, "w") as f:
        f.write("{\n")
        for k in ("description", "seed", "coverage"):
            f.write(" %s: %s,\n" % (json.dumps(k), json.dumps(out[k], sort_keys=True)))
        f.write(' "cases": [\n')
        f.write(",\n".join("  " + json.dumps(c, separators=(",", ":")) for c in cases))
        f.write("\n ]\n}\n")
    print("wrote %d cases to %s; self-check passed (%d hand cases); coverage %s" % (len(cases), path, hand, stats))


if __name__ == "__main__":
    main()
