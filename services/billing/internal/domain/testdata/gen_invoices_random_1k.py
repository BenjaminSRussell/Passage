#!/usr/bin/env python3
# Regenerates invoices-random-1k.json (§5.29.9 fixture, AC-29.1): 1,000 seeded
# random invoices across currencies with 0, 2, 3 and 4 minor-unit digits, each
# with its expected line nets and header totals computed here with exact
# rational arithmetic (no shared code with the Go engine).
#
# Rules (BR-29.3, FR-29.3), stated independently of the engine:
#   line_net = round_half_even(qty * unit_price * (100 - discount%) / 100)
#   tax      = sum of round_half_even(line tax)            (LINE rounding)
#   charges  = round_half_even(header charges)
#   gross    = net + charges + tax
# every rounding to the currency's ISO 4217 minor unit. About one value in
# eight is built to land exactly on a half minor unit so ties are exercised.
#
# Run from this directory: python3 gen_invoices_random_1k.py
import json
import os
import random
from fractions import Fraction

SEED = 2029
COUNT = 1000
# ISO 4217 exponents (not read from the engine).
CURRENCIES = {"USD": 2, "EUR": 2, "GBP": 2, "CHF": 2, "JPY": 0, "KRW": 0, "KWD": 3, "BHD": 3, "CLF": 4}


def round_half_even(x, exp):
    n = x * 10**exp
    f = n.numerator // n.denominator
    r = n - f
    if r > Fraction(1, 2) or (r == Fraction(1, 2) and f % 2 == 1):
        f += 1
    return Fraction(f, 10**exp)


def fmt(x, places):
    # Exact decimal string of a Fraction whose denominator divides 10**places.
    n = x * 10**places
    assert n.denominator == 1, (x, places)
    n = n.numerator
    sign = "-" if n < 0 else ""
    n = abs(n)
    if places == 0:
        return sign + str(n)
    s = str(n).rjust(places + 1, "0")
    return sign + s[:-places] + "." + s[-places:]


def num(x):
    # Shortest exact decimal string of an input value (trailing zeros dropped).
    s = fmt(x, 7)
    return s.rstrip("0").rstrip(".") if "." in s else s


def dec(rng, max_units, places):
    return Fraction(rng.randint(0, max_units), 10**places)


def tie(rng, exp, extra):
    # A value exactly on half a minor unit: (k + 1/2) / 10**exp.
    k = rng.randint(0, 10**extra)
    return Fraction(2 * k + 1, 2 * 10**exp)


def invoice(rng, i):
    cur = rng.choice(sorted(CURRENCIES))
    exp = CURRENCIES[cur]
    lines = []
    nets = []
    net_total = Fraction(0)
    tax_total = Fraction(0)
    for _ in range(rng.randint(1, 6)):
        if rng.random() < 0.125:
            qty, price, disc = Fraction(1), tie(rng, exp, 5), Fraction(0)
        else:
            qty = Fraction(rng.randint(1, 1_000_000), 1000)
            price = dec(rng, 10_000_000, 6)
            disc = Fraction(rng.choice([0, 0, 0, 5, 10, 12.5, 15, 25, 33, 50, 99.5, 100]))
        tax = tie(rng, exp, 4) if rng.random() < 0.125 else dec(rng, 9_999_999, 6)
        net = round_half_even(qty * price * (100 - disc) / 100, exp)
        nets.append(net)
        net_total += net
        tax_total += round_half_even(tax, exp)
        lines.append({"qty": num(qty), "unitPrice": num(price), "discountPct": num(disc), "tax": num(tax)})
    charges = tie(rng, exp, 4) if rng.random() < 0.125 else dec(rng, 100_000, 4)
    charges_r = round_half_even(charges, exp)
    gross = net_total + charges_r + tax_total
    return {
        "id": "INV-%04d" % (i + 1),
        "currency": cur,
        "charges": num(charges),
        "lines": lines,
        "expected": {
            "lineNets": [fmt(n, exp) for n in nets],
            "net": fmt(net_total, exp),
            "charges": fmt(charges_r, exp),
            "tax": fmt(tax_total, exp),
            "gross": fmt(gross, exp),
        },
    }


def main():
    rng = random.Random(SEED)
    out = {"seed": SEED, "invoices": [invoice(rng, i) for i in range(COUNT)]}
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "invoices-random-1k.json")
    with open(path, "w") as f:
        json.dump(out, f, separators=(",", ":"))
        f.write("\n")


if __name__ == "__main__":
    main()
