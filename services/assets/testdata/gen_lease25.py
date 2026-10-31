#!/usr/bin/env python3
# Regenerates lease-25.json: 25 IFRS 16 lessee schedules for the Wave 37 proof
# "Lease fixtures: schedules match accountant-reviewed workbook under IFRS 16".
# This is the candidate workbook; the accountant review itself is a human step
# and is not claimed by the tests.
#
# Independent of the Go engine (no shared code). Rules (IFRS 16.26 and 16.36,
# PRD 7.37 / BR-37.1):
#   - periodic rate r = IBR / payments per year (Nominal) or
#     (1 + IBR)^(1 / payments per year) - 1 (Effective);
#   - liability at commencement = present value of the n payments at r
#     (arrears: annuity-immediate, advance: annuity-due), rounded half-up to the
#     currency minor unit; ROU asset = liability (no initial direct costs,
#     incentives or prepayments);
#   - arrears: interest = round(opening x r), closing = opening + interest -
#     payment; advance: interest = round((opening - payment) x r), closing =
#     opening - payment + interest; the last period's interest is the plug that
#     closes the liability at exactly 0;
#   - ROU depreciation straight line: round(ROU / n) per period, the last period
#     takes the remainder.
# Nominal rates are exact rationals; Effective rates use 60-digit decimals.
# Seeded, so the output is reproducible: python3 gen_lease25.py > lease-25.json
import json
import random
from decimal import Decimal, getcontext
from fractions import Fraction as F

getcontext().prec = 60
MINOR = {"USD": 2, "EUR": 2, "GBP": 2, "JPY": 0, "KWD": 3}


def rnd(x, p):
    neg = x < 0
    n = abs(x) * 10**p
    f = n.numerator // n.denominator
    if n - f >= F(1, 2):
        f += 1
    v = F(f, 10**p)
    return -v if neg else v


def fmt(x, p):
    v = rnd(x, p)
    neg = v < 0
    whole = abs(v) * 10**p
    whole = whole.numerator // whole.denominator
    s = str(whole) if p == 0 else "%d.%0*d" % (whole // 10**p, p, whole % 10**p)
    return "-" + s if neg else s


def periodic_rate(ibr, ppy, method):
    if method == "Effective" and ppy > 1:
        d = (Decimal(1) + Decimal(str(ibr.numerator)) / Decimal(str(ibr.denominator))) ** (Decimal(1) / Decimal(ppy)) - Decimal(1)
        return F(d)
    return ibr / ppy


def schedule(payment, ppy, term, advance, ibr, cur, method):
    p = MINOR[cur]
    n = term * ppy // 12
    r = periodic_rate(ibr, ppy, method)
    pv = sum(payment / (1 + r) ** (k if not advance else k - 1) for k in range(1, n + 1))
    liability = rnd(pv, p)
    dep = rnd(liability / n, p)
    lines, bal, dep_cum = [], liability, F(0)
    for k in range(1, n + 1):
        last = k == n
        if advance:
            after = bal - payment
            interest = -after if last else rnd(after * r, p)
            closing = after + interest
        else:
            interest = payment - bal if last else rnd(bal * r, p)
            closing = bal + interest - payment
        d = liability - dep_cum if last else dep
        dep_cum += d
        lines.append({"period": k, "opening": fmt(bal, p), "interest": fmt(interest, p), "payment": fmt(payment, p),
                      "closing": fmt(closing, p), "rouDepreciation": fmt(d, p)})
        bal = closing
    assert bal == 0
    return liability, lines


def main():
    rng = random.Random(16)
    fixed = [
        # name, currency, payment, payments/year, term months, advance, IBR, rate method
        ("prd-ac-37-6", "USD", "10000", 1, 60, False, "0.05", "Nominal"),
        ("monthly-arrears-36", "USD", "2500", 12, 36, False, "0.06", "Nominal"),
        ("monthly-advance-36", "USD", "2500", 12, 36, True, "0.06", "Nominal"),
        ("quarterly-arrears-effective", "EUR", "15000", 4, 48, False, "0.045", "Effective"),
        ("monthly-advance-effective", "GBP", "1234.56", 12, 24, True, "0.0799", "Effective"),
        ("one-payment-arrears", "USD", "50000", 1, 12, False, "0.1", "Nominal"),
        ("one-payment-advance", "USD", "50000", 1, 12, True, "0.1", "Nominal"),
        ("long-50-years", "USD", "1000", 12, 600, False, "0.035", "Nominal"),
        ("max-ibr", "USD", "999.99", 2, 60, True, "0.5", "Nominal"),
        ("tiny-ibr", "USD", "800", 12, 120, False, "0.0001", "Nominal"),
        ("jpy-quarterly", "JPY", "1250000", 4, 36, False, "0.015", "Nominal"),
        ("kwd-semiannual-advance", "KWD", "4321.987", 2, 84, True, "0.0425", "Effective"),
    ]
    out = []
    for name, cur, pay, ppy, term, adv, ibr, method in fixed:
        liab, lines = schedule(F(pay), ppy, term, adv, F(ibr), cur, method)
        out.append(lease(name, cur, pay, ppy, term, adv, ibr, method, liab, lines))
    while len(out) < 25:
        cur = rng.choice(["USD", "USD", "EUR", "JPY"])
        p = MINOR[cur]
        ppy = rng.choice([1, 2, 4, 12])
        term = rng.choice([12, 24, 36, 48, 60, 84, 120])
        adv = rng.random() < 0.5
        ibr = "%.4f" % (rng.randint(50, 1500) / 10000)
        method = rng.choice(["Nominal", "Nominal", "Effective"])
        pay = F(rng.randint(100 * 10**p, 250000 * 10**p), 10**p)
        pay_s = fmt(pay, p)
        liab, lines = schedule(pay, ppy, term, adv, F(ibr), cur, method)
        out.append(lease("mix-%02d" % (len(out) - len(fixed) + 1), cur, pay_s, ppy, term, adv, ibr, method, liab, lines))
    print(json.dumps({"leases": out}, indent=1))


def lease(name, cur, pay, ppy, term, adv, ibr, method, liab, lines):
    p = MINOR[cur]
    total_interest = sum((F(l["interest"]) for l in lines), F(0))
    return {"name": name, "currency": cur, "payment": pay, "paymentsPerYear": ppy, "termMonths": term,
            "timing": "advance" if adv else "arrears", "ibrRate": ibr, "rateMethod": method,
            "liability": fmt(liab, p), "rouAsset": fmt(liab, p), "totalInterest": fmt(total_interest, p), "lines": lines}


if __name__ == "__main__":
    main()
