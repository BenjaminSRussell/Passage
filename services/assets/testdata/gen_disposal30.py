#!/usr/bin/env python3
# Regenerates disposal-30.json: 30 books, each disposed of in one to four steps
# (partial sales and scraps, then usually the rest), with the amounts every step
# must post, for the Wave 37 proof "Disposal gain/loss posting correct across
# partial disposals".
#
# Independent of the Go engine (no shared code, exact rationals). Rules (FR-37.8,
# BR-37.7): a step disposes of `percentage` of the book as it stands before the
# step; the disposed cost, accumulated depreciation and the impairment within it
# are each balance x percentage / 100 rounded half-up to the currency minor
# unit, the residual staying on the book; NBV disposed = disposed cost -
# disposed accumulated; gain (+) or loss (-) = proceeds - NBV disposed; a scrap
# has no proceeds. 100% leaves nothing.
# Seeded, so the output is reproducible: python3 gen_disposal30.py > disposal-30.json
import json
import random
from fractions import Fraction as F

MINOR = {"USD": 2, "EUR": 2, "JPY": 0, "KWD": 3}


def rnd(x, p):
    neg = x < 0
    n = abs(x) * 10**p
    f = n.numerator // n.denominator
    if n - f >= F(1, 2):
        f += 1
    v = F(f, 10**p)
    return -v if neg else v


def fmt(x, p):
    neg = x < 0
    whole = abs(rnd(x, p)) * 10**p
    whole = whole.numerator // whole.denominator
    s = str(whole) if p == 0 else "%d.%0*d" % (whole // 10**p, p, whole % 10**p)
    return "-" + s if neg else s


def money(rng, p, lo, hi):
    return F(rng.randint(lo * 10**p, hi * 10**p), 10**p)


def main():
    rng = random.Random(3730)
    out = []
    pct_pool = ["10", "12.5", "25", "33.33", "40", "50", "60", "66.67", "75", "0.01", "99.99", "1"]
    for n in range(30):
        cur = ["USD", "USD", "USD", "EUR", "JPY", "KWD"][n % 6]
        p = MINOR[cur]
        cost = money(rng, p, 1, 2_000_000)
        acc = rnd(cost * F(rng.randint(0, 100), 100), p)
        imp = rnd(acc * F(rng.choice([0, 0, 10, 25, 50, 100]), 100), p)
        nsteps = rng.randint(1, 4)
        steps = []
        c, a, i = cost, acc, imp
        for k in range(nsteps):
            last = k == nsteps - 1
            pct = "100" if last and rng.random() < 0.7 else rng.choice(pct_pool)
            kind = "scrap" if rng.random() < 0.25 else "sale"
            proceeds = F(0) if kind == "scrap" else money(rng, p, 0, max(1, int(c * 2)))
            pf = F(pct)
            dc, da, di = rnd(c * pf / 100, p), rnd(a * pf / 100, p), rnd(i * pf / 100, p)
            nbv = dc - da
            c, a, i = c - dc, a - da, i - di
            steps.append({
                "kind": kind, "percentage": pct, "proceeds": fmt(proceeds, p),
                "disposedCost": fmt(dc, p), "disposedAccumulated": fmt(da, p), "disposedImpairment": fmt(di, p),
                "nbvDisposed": fmt(nbv, p), "gainLoss": fmt(proceeds - nbv, p),
                "remainingCost": fmt(c, p), "remainingAccumulated": fmt(a, p), "remainingImpairment": fmt(i, p),
            })
            if pct == "100":
                break
        out.append({"name": "disposal-%02d" % (n + 1), "currency": cur, "cost": fmt(cost, p), "accumulated": fmt(acc, p),
                    "impairment": fmt(imp, p), "steps": steps})
    print(json.dumps({"books": out}, indent=1))


if __name__ == "__main__":
    main()
