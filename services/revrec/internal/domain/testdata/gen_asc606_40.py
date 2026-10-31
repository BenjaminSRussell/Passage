#!/usr/bin/env python3
# Regenerates asc606-40.json (§5.29.9 fixture `asc606-40`, AC-29.3): 40 ASC 606 /
# IFRS 15 bundle scenarios with the expected allocation, recognition schedule,
# revenue recognized to an as-of date and deferred revenue (2400) balance,
# computed here with exact rational arithmetic and calendar arithmetic from the
# standard library (no shared code with the Go engine).
#
# This is an engineering reference, NOT the accountant-reviewed workbook
# (`fixtures/revrec/asc606-40.xlsx`) AC-29.3 names: the workbook sign-off is
# still open. The rules it encodes (BR-29.1, BR-29.3, FR-29.8, FR-29.9):
#   allocation   share_i = truncate(price * ssp_i / sum(ssp)) to the minor unit;
#                the residual goes to the FIRST, LAST or LARGEST-SSP (first on
#                ties) obligation, so the shares sum to the price.
#   POINT_IN_TIME one line on the transfer date (the contract date here).
#   MILESTONE    one undated line, recognized on its satisfaction date.
#   RATABLE      one line per calendar month the term touches, dated at the
#                segment end; weight = covered days / days in month (DAILY) or 1
#                (MONTHLY); line = round_half_even(amount * w / sum(w)), the last
#                line takes the residual.
#   recognized   lines dated on or before the as-of month end (+ milestones
#                satisfied by then).
#   deferred     RATABLE and MILESTONE allocations less what of them is
#                recognized (POINT_IN_TIME revenue never enters 2400).
#
# Run from this directory: python3 gen_asc606_40.py
import calendar
import datetime
import json
import os
import random
from fractions import Fraction

SEED = 606
EXP = {"USD": 2, "EUR": 2, "GBP": 2, "JPY": 0, "KWD": 3}
SOURCES = ["EXPLICIT", "LIST", "PCT_OF_LIST"]


def round_half_even(x, exp):
    n = x * 10**exp
    f = n.numerator // n.denominator
    r = n - f
    if r > Fraction(1, 2) or (r == Fraction(1, 2) and f % 2 == 1):
        f += 1
    return Fraction(f, 10**exp)


def truncate(x, exp):
    n = x * 10**exp
    return Fraction(n.numerator // n.denominator, 10**exp)


def fmt(x, places):
    n = x * 10**places
    assert n.denominator == 1, (x, places)
    n = n.numerator
    if places == 0:
        return str(n)
    s = str(n).rjust(places + 1, "0")
    return s[:-places] + "." + s[-places:]


def iso(d):
    return d.isoformat() if d else None


def month_end(d):
    return d.replace(day=calendar.monthrange(d.year, d.month)[1])


def add_months(d, n):
    y, m = divmod(d.month - 1 + n, 12)
    y, m = d.year + y, m + 1
    return d.replace(year=y, month=m, day=min(d.day, calendar.monthrange(y, m)[1]))


def allocate(price, ssps, rule, exp):
    total = sum(ssps)
    shares = [truncate(price * s / total, exp) for s in ssps]
    if rule == "FIRST":
        idx = 0
    elif rule == "LAST":
        idx = len(ssps) - 1
    else:
        idx = max(range(len(ssps)), key=lambda i: (ssps[i], -i))
    shares[idx] += price - sum(shares)
    return shares


def ratable(amount, start, end, basis, exp):
    segs = []
    cur = start
    while cur <= end:
        seg_end = min(month_end(cur), end)
        days = (seg_end - cur).days + 1
        w = Fraction(days, calendar.monthrange(cur.year, cur.month)[1]) if basis == "DAILY" else Fraction(1)
        segs.append((seg_end, w))
        cur = seg_end + datetime.timedelta(days=1)
    total = sum(w for _, w in segs)
    lines, acc = [], Fraction(0)
    for i, (seg_end, w) in enumerate(segs):
        amt = amount - acc if i == len(segs) - 1 else round_half_even(amount * w / total, exp)
        acc += amt
        lines.append((seg_end, amt))
    return lines


def expect(sc):
    exp = EXP[sc["currency"]]
    obs = sc["obligations"]
    as_of = datetime.date.fromisoformat(sc["asOf"])
    shares = allocate(Fraction(sc["transactionPrice"]), [Fraction(o["ssp"]) for o in obs], sc["residualRule"], exp)
    schedules, recognized, deferred_add, deferred_rec = [], Fraction(0), Fraction(0), Fraction(0)
    for o, share in zip(obs, shares):
        m = o["method"]
        if m == "RATABLE":
            ls = ratable(share, datetime.date.fromisoformat(o["termStart"]), datetime.date.fromisoformat(o["termEnd"]), sc["ratableBasis"], exp)
            lines = [(d, a) for d, a in ls]
            done = sum((a for d, a in ls if d <= as_of), Fraction(0))
        elif m == "MILESTONE":
            lines = [(None, share)]
            sat = o.get("satisfiedOn")
            done = share if sat and datetime.date.fromisoformat(sat) <= as_of else Fraction(0)
        else:
            lines = [(datetime.date.fromisoformat(o["satisfiedOn"]), share)]
            done = share
        recognized += done
        if m in ("RATABLE", "MILESTONE"):
            deferred_add += share
            deferred_rec += done
        schedules.append([{"date": iso(d), "amount": fmt(a, exp)} for d, a in lines])
    return {
        "allocated": [fmt(s, exp) for s in shares],
        "schedules": schedules,
        "recognizedAsOf": fmt(recognized, exp),
        "deferredAdditions": fmt(deferred_add, exp),
        "deferredAsOf": fmt(deferred_add - deferred_rec, exp),
    }


def ob(oid, ssp, method, source="EXPLICIT", term=None, satisfied=None):
    o = {"id": oid, "ssp": ssp, "sspSource": source, "method": method}
    if term:
        o["termStart"], o["termEnd"] = term
    if satisfied:
        o["satisfiedOn"] = satisfied
    return o


def br29_1(sid, desc, price, basis="DAILY", as_of="2027-03-31", rule="LARGEST"):
    return {
        "id": sid, "description": desc, "currency": "USD", "transactionPrice": price, "residualRule": rule,
        "ratableBasis": basis, "contractDate": "2027-03-10", "asOf": as_of,
        "obligations": [
            ob("hardware", "8000", "POINT_IN_TIME", satisfied="2027-03-10"),
            ob("installation", "1500", "MILESTONE", satisfied="2027-03-25"),
            ob("support", "2500", "RATABLE", term=("2027-03-10", "2028-03-09")),
        ],
    }


def curated():
    return [
        br29_1("A606-01", "BR-29.1 bundle at its SSP total less 10%, AC-29.4 March", "10800.00"),
        br29_1("A606-02", "BR-29.1 bundle with a one-cent residual to the largest SSP", "10799.99"),
        br29_1("A606-03", "BR-29.1 bundle on the MONTHLY ratable basis, through August", "10800.00", basis="MONTHLY", as_of="2027-08-31"),
        br29_1("A606-04", "BR-29.1 bundle with the residual to the LAST obligation, fully recognized", "10799.99", as_of="2028-03-31", rule="LAST"),
        {
            "id": "A606-05", "description": "JPY software, implementation and 24-month support at 15% discount", "currency": "JPY",
            "transactionPrice": "1275000", "residualRule": "LARGEST", "ratableBasis": "DAILY", "contractDate": "2028-02-29", "asOf": "2028-12-31",
            "obligations": [
                ob("license", "900000", "POINT_IN_TIME", "LIST", satisfied="2028-02-29"),
                ob("implementation", "300000", "MILESTONE", "EXPLICIT", satisfied="2028-06-15"),
                ob("support", "300000", "RATABLE", "PCT_OF_LIST", term=("2028-02-29", "2030-02-27")),
            ],
        },
        {
            "id": "A606-06", "description": "KWD equipment with a residual-priced service plan", "currency": "KWD",
            "transactionPrice": "1000.000", "residualRule": "FIRST", "ratableBasis": "DAILY", "contractDate": "2027-01-17", "asOf": "2027-06-30",
            "obligations": [
                ob("equipment", "700.000", "POINT_IN_TIME", "LIST", satisfied="2027-01-17"),
                ob("service", "333.333", "RATABLE", "RESIDUAL", term=("2027-01-17", "2027-12-31")),
            ],
        },
        {
            "id": "A606-07", "description": "free promotional bundle (zero transaction price)", "currency": "EUR",
            "transactionPrice": "0.00", "residualRule": "LARGEST", "ratableBasis": "DAILY", "contractDate": "2027-05-01", "asOf": "2027-07-31",
            "obligations": [
                ob("device", "199.00", "POINT_IN_TIME", satisfied="2027-05-01"),
                ob("subscription", "120.00", "RATABLE", term=("2027-05-01", "2028-04-30")),
            ],
        },
        {
            "id": "A606-08", "description": "premium price above the SSP total with an unsatisfied milestone", "currency": "GBP",
            "transactionPrice": "5500.00", "residualRule": "LARGEST", "ratableBasis": "MONTHLY", "contractDate": "2027-11-20", "asOf": "2028-02-29",
            "obligations": [
                ob("design", "2000.00", "MILESTONE", satisfied="2028-04-02"),
                ob("build", "2000.00", "MILESTONE", satisfied="2028-01-10"),
                ob("hosting", "1000.00", "RATABLE", term=("2027-12-01", "2028-11-30")),
            ],
        },
        {
            "id": "A606-09", "description": "one-cent price over three equal obligations", "currency": "USD",
            "transactionPrice": "0.01", "residualRule": "LARGEST", "ratableBasis": "DAILY", "contractDate": "2027-06-30", "asOf": "2027-06-30",
            "obligations": [
                ob("a", "100", "POINT_IN_TIME", satisfied="2027-06-30"),
                ob("b", "100", "MILESTONE"),
                ob("c", "100", "RATABLE", term=("2027-06-30", "2027-09-29")),
            ],
        },
        {
            "id": "A606-10", "description": "single-day ratable term on the leap day", "currency": "USD",
            "transactionPrice": "29.00", "residualRule": "LARGEST", "ratableBasis": "DAILY", "contractDate": "2028-02-29", "asOf": "2028-02-29",
            "obligations": [ob("day-pass", "29", "RATABLE", term=("2028-02-29", "2028-02-29"))],
        },
    ]


def generated(rng, start_no, count):
    out = []
    for k in range(count):
        cur = rng.choice(sorted(EXP))
        exp = EXP[cur]
        contract = datetime.date(2026, 1, 1) + datetime.timedelta(days=rng.randint(0, 1095))
        n = rng.randint(2, 5)
        residual_at = rng.randrange(n) if rng.random() < 0.3 else -1
        obs, ssp_total = [], Fraction(0)
        methods = ["POINT_IN_TIME", "MILESTONE", "RATABLE"]
        for i in range(n):
            method = methods[i] if i < 3 and rng.random() < 0.6 else rng.choice(methods)
            ssp = Fraction(rng.randint(10**exp, 50_000 * 10**exp), 10**exp)
            ssp_total += ssp
            source = "RESIDUAL" if i == residual_at else rng.choice(SOURCES)
            o = ob("po-%d" % (i + 1), fmt(ssp, exp), method, source)
            if method == "POINT_IN_TIME":
                o["satisfiedOn"] = contract.isoformat()
            elif method == "MILESTONE":
                if rng.random() < 0.8:
                    o["satisfiedOn"] = (contract + datetime.timedelta(days=rng.randint(0, 240))).isoformat()
            else:
                ts = contract + datetime.timedelta(days=rng.randint(0, 20))
                te = add_months(ts, rng.choice([1, 3, 6, 12, 12, 24, 36])) - datetime.timedelta(days=rng.choice([1, 1, 1, 0, 15]))
                o["termStart"], o["termEnd"] = ts.isoformat(), te.isoformat()
            obs.append(o)
        factor = Fraction(rng.randint(60, 110), 100)
        price = round_half_even(ssp_total * factor - Fraction(rng.randint(0, 99), 10**(exp + 2)), exp)
        as_of = month_end(add_months(contract.replace(day=1), rng.randint(0, 14)))
        out.append({
            "id": "A606-%02d" % (start_no + k),
            "description": "seeded bundle: %d obligations, %s, price %s%% of SSP total" % (n, cur, factor * 100),
            "currency": cur, "transactionPrice": fmt(price, exp),
            "residualRule": rng.choice(["LARGEST", "FIRST", "LAST"]), "ratableBasis": rng.choice(["DAILY", "MONTHLY"]),
            "contractDate": contract.isoformat(), "asOf": as_of.isoformat(), "obligations": obs,
        })
    return out


# Hand-derived checks (see the BR-29.1, AC-29.4 and monthly-basis unit tests).
HAND = {
    "A606-01": {"allocated": ["7200.00", "1350.00", "2250.00"], "recognizedAsOf": "8683.06", "deferredAsOf": "2116.94", "deferredAdditions": "3600.00"},
    "A606-02": {"allocated": ["7200.01", "1349.99", "2249.99"]},
    "A606-07": {"allocated": ["0.00", "0.00"], "recognizedAsOf": "0.00", "deferredAsOf": "0.00"},
    "A606-09": {"allocated": ["0.01", "0.00", "0.00"], "recognizedAsOf": "0.01", "deferredAsOf": "0.00"},  # equal SSPs: LARGEST = first
    "A606-10": {"allocated": ["29.00"], "recognizedAsOf": "29.00", "deferredAsOf": "0.00"},
}


def main():
    rng = random.Random(SEED)
    scenarios = curated()
    scenarios += generated(rng, len(scenarios) + 1, 40 - len(scenarios))
    assert len(scenarios) == 40
    for sc in scenarios:
        sc["expected"] = expect(sc)
    by_id = {sc["id"]: sc for sc in scenarios}
    s1 = by_id["A606-01"]["expected"]["schedules"][2]
    assert [l["amount"] for l in s1] == ["133.06"] + ["187.50"] * 11 + ["54.44"], s1
    s3 = by_id["A606-03"]["expected"]["schedules"][2]
    assert [l["amount"] for l in s3] == ["173.08"] * 12 + ["173.04"], s3
    for sid, want in HAND.items():
        for k, v in want.items():
            got = by_id[sid]["expected"][k]
            if got != v:
                raise SystemExit("%s %s: reference %s != hand-derived %s" % (sid, k, got, v))
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "asc606-40.json")
    with open(path, "w") as f:
        json.dump({"seed": SEED, "scenarios": scenarios}, f, indent=1)
        f.write("\n")


if __name__ == "__main__":
    main()
