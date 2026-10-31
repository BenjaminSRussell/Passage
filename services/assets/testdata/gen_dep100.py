#!/usr/bin/env python3
# Regenerates dep-100.json: 100 asset books with the yearly depreciation each
# must produce, for the Wave 37 proof "100 assets x methods x conventions match
# published tables (IRS MACRS, straight-line edge cases) to the cent".
#
# Independent of the Go engine: no code is shared, and every amount is computed
# here with exact rational arithmetic.
#   - MACRS (60 books): the IRS Pub. 946 Appendix A percentage tables A-1 (half-
#     year) and A-2..A-5 (mid-quarter by quarter placed in service), transcribed
#     below from the 2025 publication. Year amount = cost x rate / 100 rounded
#     half-up to the cent; the last recovery year takes the remainder so the
#     amounts sum to the cost. 30 books cost 100,000.00 (each amount is the
#     table percentage x 1,000, so a mistyped rate fails directly) and 30 cost
#     seeded odd amounts (rounding and the final-year remainder).
#   - Straight line (30 books): monthly, cumulative target basis x consumed /
#     life rounded half-up to the currency minor unit each month (BR-37.1);
#     None / FullMonth charge a full first month, MidMonth15 half of the first
#     and of the last month. Edge cases: 1-month lives, Dec 31 and Feb 29
#     in-service, the 1,200-month maximum, salvage = cost, zero cost, a cost
#     smaller than the number of months, JPY (0), KWD (3) and CLF (4) minor units.
#   - Declining balance (10 books): annual, rate = factor / life; each year
#     charges the remaining basis x rate x the year's fraction (or, with the
#     switch, the remaining basis / remaining life when larger), rounded half-up;
#     the final year clears the remaining basis. First-year fraction: None 1,
#     HalfYear 1/2, MidQuarter 10.5, 7.5, 4.5 or 1.5 twelfths by quarter.
# Seeded, so the output is reproducible: python3 gen_dep100.py > dep-100.json
import datetime
import json
import random
from fractions import Fraction as F

MINOR = {"USD": 2, "EUR": 2, "JPY": 0, "KWD": 3, "CLF": 4}

PUB946 = {
    "HY": {  # Table A-1
        3: "33.33 44.45 14.81 7.41",
        5: "20.00 32.00 19.20 11.52 11.52 5.76",
        7: "14.29 24.49 17.49 12.49 8.93 8.92 8.93 4.46",
        10: "10.00 18.00 14.40 11.52 9.22 7.37 6.55 6.55 6.56 6.55 3.28",
        15: "5.00 9.50 8.55 7.70 6.93 6.23 5.90 5.90 5.91 5.90 5.91 5.90 5.91 5.90 5.91 2.95",
        20: "3.750 7.219 6.677 6.177 5.713 5.285 4.888 4.522 4.462 4.461 4.462 4.461 4.462 4.461 4.462 4.461 4.462 4.461 4.462 4.461 2.231",
    },
    "Q1": {  # Table A-2
        3: "58.33 27.78 12.35 1.54",
        5: "35.00 26.00 15.60 11.01 11.01 1.38",
        7: "25.00 21.43 15.31 10.93 8.75 8.74 8.75 1.09",
        10: "17.50 16.50 13.20 10.56 8.45 6.76 6.55 6.55 6.56 6.55 0.82",
        15: "8.75 9.13 8.21 7.39 6.65 5.99 5.90 5.91 5.90 5.91 5.90 5.91 5.90 5.91 5.90 0.74",
        20: "6.563 7.000 6.482 5.996 5.546 5.130 4.746 4.459 4.459 4.459 4.459 4.460 4.459 4.460 4.459 4.460 4.459 4.460 4.459 4.460 0.565",
    },
    "Q2": {  # Table A-3
        3: "41.67 38.89 14.14 5.30",
        5: "25.00 30.00 18.00 11.37 11.37 4.26",
        7: "17.85 23.47 16.76 11.97 8.87 8.87 8.87 3.34",
        10: "12.50 17.50 14.00 11.20 8.96 7.17 6.55 6.55 6.56 6.55 2.46",
        15: "6.25 9.38 8.44 7.59 6.83 6.15 5.91 5.90 5.91 5.90 5.91 5.90 5.91 5.90 5.91 2.21",
        20: "4.688 7.148 6.612 6.116 5.658 5.233 4.841 4.478 4.463 4.463 4.463 4.463 4.463 4.463 4.462 4.463 4.462 4.463 4.462 4.463 1.673",
    },
    "Q3": {  # Table A-4
        3: "25.00 50.00 16.67 8.33",
        5: "15.00 34.00 20.40 12.24 11.30 7.06",
        7: "10.71 25.51 18.22 13.02 9.30 8.85 8.86 5.53",
        10: "7.50 18.50 14.80 11.84 9.47 7.58 6.55 6.55 6.56 6.55 4.10",
        15: "3.75 9.63 8.66 7.80 7.02 6.31 5.90 5.90 5.91 5.90 5.91 5.90 5.91 5.90 5.91 3.69",
        20: "2.813 7.289 6.742 6.237 5.769 5.336 4.936 4.566 4.460 4.460 4.460 4.460 4.461 4.460 4.461 4.460 4.461 4.460 4.461 4.460 2.788",
    },
    "Q4": {  # Table A-5
        3: "8.33 61.11 20.37 10.19",
        5: "5.00 38.00 22.80 13.68 10.94 9.58",
        7: "3.57 27.55 19.68 14.06 10.04 8.73 8.73 7.64",
        10: "2.50 19.50 15.60 12.48 9.98 7.99 6.55 6.55 6.56 6.55 5.74",
        15: "1.25 9.88 8.89 8.00 7.20 6.48 5.90 5.90 5.90 5.91 5.90 5.91 5.90 5.91 5.90 5.17",
        20: "0.938 7.430 6.872 6.357 5.880 5.439 5.031 4.654 4.458 4.458 4.458 4.458 4.458 4.458 4.458 4.458 4.458 4.459 4.458 4.459 3.901",
    },
}
TABLE_NAME = {"HY": "A-1", "Q1": "A-2", "Q2": "A-3", "Q3": "A-4", "Q4": "A-5"}
QUARTER_DAY = {"Q1": "02-14", "Q2": "05-20", "Q3": "08-31", "Q4": "11-03"}


def rnd(x, places):
    """Round a Fraction half-up (amounts are never negative) to places."""
    n = x * 10**places
    f = n.numerator // n.denominator
    if n - f >= F(1, 2):
        f += 1
    return F(f, 10**places)


def fmt(x, places):
    s = rnd(x, places)
    whole = s.numerator * 10**places // s.denominator
    neg = whole < 0
    whole = abs(whole)
    if places == 0:
        out = str(whole)
    else:
        out = "%d.%0*d" % (whole // 10**places, places, whole % 10**places)
    return "-" + out if neg else out


def by_year(lines, cur):
    years = {}
    for period, amount in lines:
        y = period[:4]
        years[y] = years.get(y, F(0)) + amount
    return {y: fmt(v, MINOR[cur]) for y, v in sorted(years.items())}


def macrs(cost, life, row, year):
    rates = [F(r) for r in PUB946[row][life].split()]
    assert sum(rates) == 100, (row, life, sum(rates))
    out, cum = [], F(0)
    for i, r in enumerate(rates):
        a = rnd(cost * r / 100, 2)
        if i == len(rates) - 1 or a > cost - cum:
            a = cost - cum
        cum += a
        out.append(("%04d" % (year + i), a))
    return out


def straight_line(cost, salvage, life, conv, in_service, cur):
    places = MINOR[cur]
    basis = cost - salvage
    first = F(1, 2) if conv == "MidMonth15" else F(1)
    consumed, cum, out, i = F(0), F(0), [], 0
    while consumed < life:
        step = first if i == 0 else F(1)
        step = min(step, life - consumed)
        consumed += step
        target = rnd(basis * consumed / life, places)
        m = in_service.month - 1 + i
        out.append(("%04d-%02d" % (in_service.year + m // 12, m % 12 + 1), target - cum))
        cum = target
        i += 1
    return out


def declining(cost, salvage, life, factor, switch, conv, in_service, cur):
    places = MINOR[cur]
    basis = cost - salvage
    if conv == "HalfYear":
        first = F(1, 2)
    elif conv == "MidQuarter":
        first = [F(21, 24), F(15, 24), F(9, 24), F(3, 24)][(in_service.month - 1) // 3]
    else:
        first = F(1)
    rate = factor / life
    consumed, cum, out, i = F(0), F(0), [], 0
    while consumed < life:
        step = first if i == 0 else F(1)
        rem = life - consumed
        step = min(step, rem)
        left = basis - cum
        if consumed + step >= life:
            a = left
        else:
            a = left * rate * step
            if switch and left / rem * step > a:
                a = left / rem * step
        a = min(rnd(a, places), left)
        cum += a
        consumed += step
        out.append(("%04d" % (in_service.year + i), a))
        i += 1
    return out


def book(name, cur, cost, salvage, life, method, conv, in_service, lines, source, factor="0", switch=False):
    return {
        "name": name, "currency": cur, "cost": fmt(cost, MINOR[cur]), "salvage": fmt(salvage, MINOR[cur]),
        "life": life, "method": method, "factor": factor, "switch": switch, "convention": conv,
        "inService": in_service.isoformat(), "source": source, "periods": len(lines), "firstPeriod": lines[0][0],
        "total": fmt(sum((a for _, a in lines), F(0)), MINOR[cur]), "annual": by_year(lines, cur),
        "amounts": [fmt(a, MINOR[cur]) for _, a in lines],
    }


def main():
    rng = random.Random(37)
    books = []
    # MACRS: every table row twice.
    for variant in ("round", "odd"):
        for row in ("HY", "Q1", "Q2", "Q3", "Q4"):
            for life in (3, 5, 7, 10, 15, 20):
                year = 2025 if variant == "round" else 2026
                cost = F(100000) if variant == "round" else F(rng.randint(100_00, 2_500_000_00), 100)
                day = "07-01" if row == "HY" else QUARTER_DAY[row]
                ins = datetime.date.fromisoformat("%d-%s" % (year, day))
                conv = "HalfYear" if row == "HY" else "MidQuarter"
                books.append(book("macrs-%s-%dy-%s" % (row.lower(), life, variant), "USD", cost, F(0), life, "MACRS", conv, ins,
                                  macrs(cost, life, row, year), "IRS Pub. 946 Table " + TABLE_NAME[row]))
    # Straight line: fixed edge cases, then seeded mixes.
    sl = [
        ("sl-prd-midmonth15", "USD", "12000", "0", 60, "MidMonth15", "2026-03-10"),
        ("sl-one-month-dec31", "USD", "10000", "0", 1, "None", "2026-12-31"),
        ("sl-one-month-midmonth", "USD", "10000", "0", 1, "MidMonth15", "2026-12-15"),
        ("sl-thirds", "USD", "1000", "0", 3, "None", "2026-01-31"),
        ("sl-sevenths-fullmonth", "USD", "100", "0", 7, "FullMonth", "2026-06-01"),
        ("sl-cost-below-months", "USD", "0.05", "0", 12, "None", "2026-01-01"),
        ("sl-max-life", "USD", "999999.99", "0", 1200, "None", "2026-01-01"),
        ("sl-salvage", "USD", "50000", "5000", 120, "None", "2026-04-01"),
        ("sl-salvage-equals-cost", "USD", "5000", "5000", 24, "None", "2026-04-01"),
        ("sl-zero-cost", "USD", "0", "0", 12, "FullMonth", "2026-04-01"),
        ("sl-leap-day", "USD", "36500", "0", 36, "MidMonth15", "2024-02-29"),
        ("sl-jpy", "JPY", "1000000", "0", 7, "None", "2026-05-20"),
        ("sl-kwd", "KWD", "1234.567", "0", 13, "MidMonth15", "2026-09-09"),
        ("sl-clf", "CLF", "100.0001", "0.0001", 9, "FullMonth", "2026-10-10"),
        ("sl-odd-cents-nov30", "USD", "12345.67", "0", 59, "FullMonth", "2026-11-30"),
    ]
    for name, cur, cost, salv, life, conv, ins in sl:
        d = datetime.date.fromisoformat(ins)
        books.append(book(name, cur, F(cost), F(salv), life, "StraightLine", conv, d,
                          straight_line(F(cost), F(salv), life, conv, d, cur), "BR-37.1 straight-line rule (exact rationals)"))
    for i in range(15):
        cur = rng.choice(["USD", "USD", "EUR", "JPY", "KWD"])
        p = MINOR[cur]
        cost = F(rng.randint(1, 50_000_000 * 10**p), 10**p) if p else F(rng.randint(1, 50_000_000))
        salv = rnd(cost * F(rng.choice([0, 0, 5, 10, 15]), 100), p)
        life = rng.choice([2, 11, 13, 36, 47, 60, 84, 119, 240, 360])
        conv = rng.choice(["None", "FullMonth", "MidMonth15"])
        d = datetime.date(rng.randint(2024, 2027), rng.randint(1, 12), rng.randint(1, 28))
        books.append(book("sl-mix-%02d" % (i + 1), cur, cost, salv, life, "StraightLine", conv, d,
                          straight_line(cost, salv, life, conv, d, cur), "BR-37.1 straight-line rule (exact rationals)"))
    # Declining balance (non-table): factors, switch on/off, conventions, salvage.
    db = [
        ("db-150-noswitch-none", "USD", "80000", "0", 8, "1.5", False, "None", "2026-01-01"),
        ("db-150-switch-hy", "USD", "80000", "8000", 8, "1.5", True, "HalfYear", "2026-07-01"),
        ("db-200-switch-none", "USD", "10000", "0", 5, "2", True, "None", "2026-03-01"),
        ("db-200-noswitch-mq1", "USD", "12345.67", "0", 4, "2", False, "MidQuarter", "2026-02-01"),
        ("db-200-switch-mq4", "USD", "250000", "0", 7, "2", True, "MidQuarter", "2026-12-01"),
        ("db-125-switch-mq2", "EUR", "99999.99", "999.99", 10, "1.25", True, "MidQuarter", "2026-04-15"),
        ("db-300-switch-hy", "USD", "6000", "0", 3, "3", True, "HalfYear", "2026-09-30"),
        ("db-100-switch-mq3", "USD", "70000", "0", 7, "1", True, "MidQuarter", "2026-08-15"),
        ("db-one-year-hy", "USD", "1000.01", "0", 1, "2", True, "HalfYear", "2026-05-05"),
        ("db-jpy-200-switch-none", "JPY", "3333333", "0", 6, "2", True, "None", "2026-06-06"),
    ]
    for name, cur, cost, salv, life, factor, switch, conv, ins in db:
        d = datetime.date.fromisoformat(ins)
        books.append(book(name, cur, F(cost), F(salv), life, "DecliningBalance", conv, d,
                          declining(F(cost), F(salv), life, F(factor), switch, conv, d, cur),
                          "declining-balance rule (exact rationals)", factor=factor, switch=switch))
    assert len(books) == 100 and len({b["name"] for b in books}) == 100
    print(json.dumps({"books": books}, indent=1))


if __name__ == "__main__":
    main()
