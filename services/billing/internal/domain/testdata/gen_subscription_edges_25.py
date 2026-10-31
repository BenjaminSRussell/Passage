#!/usr/bin/env python3
# Regenerates subscription-edges-25.json (§5.29.9 fixture): 25 subscription
# proration edge cases (BR-29.8) with the expected amount computed here from
# calendar day counts and exact rational arithmetic (no shared code with the
# Go engine). Every case also carries the amount derived by hand in the
# comment beside it; the script refuses to write if the two disagree.
#
# Rule: amount * service_days / period_days, where service days are the
# inclusive overlap of the service range with the inclusive billing period,
# rounded half-even to the currency's minor unit; no overlap bills zero.
#
# Run from this directory: python3 gen_subscription_edges_25.py
import datetime
import json
import os
from fractions import Fraction

EXP = {"USD": 2, "EUR": 2, "JPY": 0, "KWD": 3}

# (id, why, amount, currency, period from, period to, service from, service to, hand-derived amount)
CASES = [
    ("SE-01", "AC-29.7 mid-period start, leap February", "300.00", "EUR", "2028-02-01", "2028-02-29", "2028-02-10", "2028-02-29", "206.90"),  # 300*20/29 = 206.896..
    ("SE-02", "start on the leap day", "300.00", "EUR", "2028-02-01", "2028-02-29", "2028-02-29", "2028-02-29", "10.34"),  # 300/29 = 10.344..
    ("SE-03", "cancel the day before the leap day", "300.00", "EUR", "2028-02-01", "2028-02-29", "2028-02-01", "2028-02-28", "289.66"),  # 300*28/29 = 289.655..
    ("SE-04", "cancel effective on the leap day bills the whole month", "300.00", "EUR", "2028-02-01", "2028-02-29", "2028-02-01", "2028-02-29", "300.00"),
    ("SE-05", "non-leap February, start on the 15th", "300.00", "EUR", "2027-02-01", "2027-02-28", "2027-02-15", "2027-02-28", "150.00"),  # 300*14/28
    ("SE-06", "non-leap February, cancel on the 14th", "300.00", "EUR", "2027-02-01", "2027-02-28", "2027-02-01", "2027-02-14", "150.00"),  # 300*14/28
    ("SE-07", "start and cancel inside one 31-day month", "100.00", "USD", "2027-03-01", "2027-03-31", "2027-03-10", "2027-03-20", "35.48"),  # 100*11/31 = 35.483..
    ("SE-08", "start and cancel the same day, 31-day month", "100.00", "USD", "2027-03-01", "2027-03-31", "2027-03-31", "2027-03-31", "3.23"),  # 100/31 = 3.225..
    ("SE-09", "start and cancel the same day, 30-day month", "100.00", "USD", "2027-04-01", "2027-04-30", "2027-04-01", "2027-04-01", "3.33"),  # 100/30 = 3.333..
    ("SE-10", "full period", "100.00", "USD", "2027-04-01", "2027-04-30", "2027-04-01", "2027-04-30", "100.00"),
    ("SE-11", "service starts after the period", "100.00", "USD", "2027-04-01", "2027-04-30", "2027-05-01", "2027-05-31", "0.00"),
    ("SE-12", "service cancelled before the period", "100.00", "USD", "2027-04-01", "2027-04-30", "2027-01-01", "2027-03-31", "0.00"),
    ("SE-13", "service began before the period, cancelled mid-period", "100.00", "USD", "2027-03-01", "2027-03-31", "2027-01-15", "2027-03-10", "32.26"),  # 100*10/31 = 32.258..
    ("SE-14", "service spans the period on both sides", "100.00", "USD", "2027-03-01", "2027-03-31", "2027-02-15", "2027-04-15", "100.00"),
    ("SE-15", "annual leap-year period, start 1 March", "1200.00", "USD", "2028-01-01", "2028-12-31", "2028-03-01", "2028-12-31", "1003.28"),  # 1200*306/366 = 1003.278..
    ("SE-16", "annual non-leap period, start 1 March", "1200.00", "USD", "2027-01-01", "2027-12-31", "2027-03-01", "2027-12-31", "1006.03"),  # 1200*306/365 = 1006.027..
    ("SE-17", "annual period across the leap day, cancel on the leap day", "1200.00", "USD", "2027-07-01", "2028-06-30", "2027-07-01", "2028-02-29", "800.00"),  # 1200*244/366 = 800
    ("SE-18", "last day of an annual leap-year period", "1200.00", "USD", "2028-01-01", "2028-12-31", "2028-12-31", "2028-12-31", "3.28"),  # 1200/366 = 3.278..
    ("SE-19", "quarter with the leap day, start on the leap day", "300.00", "USD", "2028-01-01", "2028-03-31", "2028-02-29", "2028-03-31", "105.49"),  # 300*32/91 = 105.494..
    ("SE-20", "non-leap quarter, start 1 March", "300.00", "USD", "2027-01-01", "2027-03-31", "2027-03-01", "2027-03-31", "103.33"),  # 300*31/90 = 103.333..
    ("SE-21", "start and cancel across the leap day inside February", "300.00", "EUR", "2028-02-01", "2028-02-29", "2028-02-28", "2028-03-01", "20.69"),  # 300*2/29 = 20.689..
    ("SE-22", "JPY mid-period start, leap February", "10000", "JPY", "2028-02-01", "2028-02-29", "2028-02-10", "2028-02-29", "6897"),  # 10000*20/29 = 6896.55..
    ("SE-23", "JPY exact half yen rounds to even (down)", "15", "JPY", "2027-04-01", "2027-04-30", "2027-04-30", "2027-04-30", "0"),  # 15/30 = 0.5
    ("SE-24", "USD exact half cent rounds to even (up)", "1.55", "USD", "2027-04-01", "2027-04-30", "2027-04-01", "2027-04-03", "0.16"),  # 1.55*3/30 = 0.155
    ("SE-25", "KWD three decimals, one day of 31", "100.000", "KWD", "2027-03-01", "2027-03-31", "2027-03-05", "2027-03-05", "3.226"),  # 100/31 = 3.2258..
]


def round_half_even(x, exp):
    n = x * 10**exp
    f = n.numerator // n.denominator
    r = n - f
    if r > Fraction(1, 2) or (r == Fraction(1, 2) and f % 2 == 1):
        f += 1
    return Fraction(f, 10**exp)


def fmt(x, places):
    n = x * 10**places
    assert n.denominator == 1
    n = n.numerator
    if places == 0:
        return str(n)
    s = str(n).rjust(places + 1, "0")
    return s[:-places] + "." + s[-places:]


def day(s):
    return datetime.date.fromisoformat(s)


def expected(amount, cur, pf, pt, sf, st):
    pf, pt, sf, st = day(pf), day(pt), day(sf), day(st)
    lo, hi = max(pf, sf), min(pt, st)
    service = (hi - lo).days + 1 if hi >= lo else 0
    period = (pt - pf).days + 1
    return fmt(round_half_even(Fraction(amount) * service / period, EXP[cur]), EXP[cur]), service, period


def main():
    assert len(CASES) == 25
    out = []
    for cid, why, amount, cur, pf, pt, sf, st, hand in CASES:
        got, service, period = expected(amount, cur, pf, pt, sf, st)
        if got != hand:
            raise SystemExit("%s: reference %s != hand-derived %s" % (cid, got, hand))
        out.append({"id": cid, "why": why, "amount": amount, "currency": cur, "periodFrom": pf, "periodTo": pt,
                    "serviceFrom": sf, "serviceTo": st, "serviceDays": service, "periodDays": period, "expected": got})
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "subscription-edges-25.json")
    with open(path, "w") as f:
        json.dump({"cases": out}, f, indent=1)
        f.write("\n")


if __name__ == "__main__":
    main()
