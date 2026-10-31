#!/usr/bin/env python3
# Regenerates pmf-coby-adv-20.json for the Wave 24 proof "Co/by-product cost
# allocation sums to batch cost exactly across methods".
#
# Independent of the Go engine (no shared code, exact rationals). Rules, from
# BR-24.3 (docs/prd/phase-4-manufacturing.md) and services/processmfg
# NOT-IMPLEMENTED.md (slice 9):
#   1. By-product burdens come off the batch cost first: percent = cost x rate /
#      100; per-series, per-quantity and recycled = qty x rate; each rounded half
#      away from zero to 4 places. If the burdens exceed the cost both exactly
#      and rounded, the allocation is refused (PMF-0021). If only the rounding
#      makes them exceed it, the overshoot comes off the largest burden (first
#      on a tie); burdens that round below the cost are taken as rounded.
#   2. The rest is split by weight: none = main only; manual = percent; tca =
#      standard cost x raf qty; quantity = raf qty. Weights summing to zero are
#      refused (PMF-0021).
#   3. Each co-product gets round(rest x w / sum w, 4) and round(100 x w / sum
#      w, 4) percent; the residual goes to the main product. If rounding the
#      co-products up overshoots the rest (or 100 %), the overshoot comes off the
#      largest co-product (first on a tie), so no share is negative.
# Every expected answer adds up to the batch cost by construction; the script
# asserts it too. Costs respect the currency's minor unit (USD 2, JPY 0, KWD 3)
# so the same cases can be ended on an order in the store.
# Run: python3 -I gen_pmf_coby_adv.py > pmf-coby-adv-20.json
import json
from fractions import Fraction as F

P = 4


def rnd(x):
    neg = x < 0
    n = abs(x) * 10**P
    f = n.numerator // n.denominator
    if n - f >= F(1, 2):
        f += 1
    v = F(f, 10**P)
    return -v if neg else v


def fmt(x):
    n = x * 10**P
    assert n.denominator == 1 and n >= 0, x
    n = n.numerator
    return "%d.%04d" % (n // 10**P, n % 10**P)


def first_max(xs):
    best = 0
    for i, x in enumerate(xs):
        if x > xs[best]:
            best = i
    return best


def prod(std="0", raf="1", pct="0"):
    return {"standardCost": std, "rafQty": raf, "percent": pct}


def by(kind, rate, qty="1"):
    return {"costType": kind, "qty": qty, "rate": rate}


CASES = [
    ("br243-tca", "USD", "10000.00", "tca", prod("40", "200", "70"), [prod("20", "100", "30")], [by("per-quantity", "1.5", "50")]),
    ("thirds-quantity", "USD", "100.00", "quantity", prod(raf="1"), [prod(raf="1"), prod(raf="1")], []),
    ("sevenths-manual", "USD", "1000.00", "manual", prod(pct="42.8571"), [prod(pct="28.5714"), prod(pct="28.5715")], []),
    ("manual-two-decimal-thirds", "USD", "500.00", "manual", prod(pct="33.33"), [prod(pct="33.33"), prod(pct="33.34")], [by("recycled", "0.07", "3")]),
    ("tca-zero-main-overshoot", "USD", "0.05", "tca", prod("0", "10"), [prod("1", "1"), prod("1", "1"), prod("1", "1")], []),
    ("manual-zero-main-overshoot", "USD", "0.02", "manual", prod(pct="0"), [prod(pct="1"), prod(pct="1"), prod(pct="1")], []),
    ("tca-tiny-cost", "USD", "0.01", "tca", prod("1", "1"), [prod("1", "1"), prod("1", "1")], []),
    ("burden-takes-all-per-series", "USD", "250.00", "quantity", prod(raf="3"), [prod(raf="7")], [by("per-series", "250")]),
    ("percent-burden-100", "USD", "77.77", "quantity", prod(raf="1"), [prod(raf="2")], [by("percent", "100")]),
    ("all-four-burden-types", "USD", "12345.67", "tca", prod("3.17", "413", "60"), [prod("0.83", "97", "25"), prod("11.9", "5", "15")],
     [by("percent", "2.75"), by("per-series", "100"), by("per-quantity", "0.4375", "37"), by("recycled", "1.1", "12.5")]),
    ("none-with-co-and-percent-burden", "USD", "999.99", "none", prod("5", "10", "50"), [prod("5", "10", "50")], [by("percent", "33.33")]),
    ("jpy-one-yen-thirds", "JPY", "1", "quantity", prod(raf="1"), [prod(raf="1"), prod(raf="1")], []),
    ("kwd-fractional-raf", "KWD", "1234.567", "quantity", prod(raf="0.333"), [prod(raf="0.667"), prod(raf="0.001")], [by("recycled", "0.001", "1")]),
    ("large-cost-five-co-products", "USD", "9999999999.99", "tca", prod("17.31", "1103"),
     [prod("0.07", "999999"), prod("3.33", "3"), prod("123.45", "77"), prod("0.01", "1"), prod("9.99", "4321")], [by("per-quantity", "0.9999", "1000")]),
    ("dominant-co-product", "USD", "1000.00", "quantity", prod(raf="1"), [prod(raf="999999")], []),
    ("percent-burdens-total-100-round-up", "USD", "0.01", "quantity", prod(raf="1"), [prod(raf="1")], [by("percent", "50.5"), by("percent", "49.5")]),
    ("percent-burdens-overshoot-tie", "USD", "0.01", "quantity", prod(raf="1"), [], [by("percent", "49.5"), by("percent", "49.5"), by("percent", "1")]),
    ("refused-burden-above-cost", "USD", "100.00", "quantity", prod(raf="1"), [], [by("per-quantity", "20", "10")]),
    ("refused-zero-weights", "USD", "100.00", "tca", prod("0", "5"), [prod("0", "5")], []),
    ("refused-percent-burdens-above-100", "USD", "100.00", "quantity", prod(raf="1"), [], [by("percent", "60"), by("percent", "40.0001")]),
]


def allocate(cost, method, main, cos, bys):
    burdens_exact = []
    for b in bys:
        if b["costType"] == "percent":
            burdens_exact.append(cost * F(b["rate"]) / 100)
        else:
            burdens_exact.append(F(b["qty"]) * F(b["rate"]))
    burdens = [rnd(x) for x in burdens_exact]
    if sum(burdens_exact, F(0)) > cost and sum(burdens, F(0)) > cost:
        return None
    over = sum(burdens, F(0)) - cost
    if over > 0:
        burdens[first_max(burdens)] -= over
    net = cost - sum(burdens, F(0))

    def weight(i, p):
        if method == "none":
            return F(1 if i == 0 else 0)
        if method == "manual":
            return F(p["percent"])
        if method == "tca":
            return F(p["standardCost"]) * F(p["rafQty"])
        return F(p["rafQty"])

    ws = [weight(i, p) for i, p in enumerate([main] + cos)]
    total = sum(ws, F(0))
    if total <= 0:
        return None
    amts = [rnd(net * w / total) for w in ws[1:]]
    pcts = [rnd(100 * w / total) for w in ws[1:]]
    if amts and sum(amts) > net:
        amts[first_max(amts)] -= sum(amts) - net
    if pcts and sum(pcts) > 100:
        pcts[first_max(pcts)] -= sum(pcts) - 100
    main_amt = net - sum(amts, F(0))
    main_pct = 100 - sum(pcts, F(0))
    assert main_amt + sum(amts, F(0)) + sum(burdens, F(0)) == cost
    assert min([main_amt] + amts + burdens) >= 0
    return {
        "main": {"amount": fmt(main_amt), "percent": fmt(main_pct)},
        "coProducts": [{"amount": fmt(a), "percent": fmt(p)} for a, p in zip(amts, pcts)],
        "burdens": [fmt(b) for b in burdens],
    }


def main():
    out = []
    for name, cur, cost, method, m, cos, bys in CASES:
        res = allocate(F(cost), method, m, cos, bys)
        case = {"name": name, "currency": cur, "batchCost": cost, "method": method, "main": m, "coProducts": cos, "byProducts": bys}
        if res is None:
            case["expectedCode"] = "PMF-0021"
        else:
            case["expected"] = res
        out.append(case)
    print(json.dumps(out, indent=1))


if __name__ == "__main__":
    assert len(CASES) == 20
    main()
