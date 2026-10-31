#!/usr/bin/env python3
# Regenerates pmf-potency-adv-40.csv for the Wave 24 proof "Potency fixtures:
# consumption scales correctly with active-ingredient percentage".
#
# Independent of the Go engine (no shared code, exact rationals). Rule BR-24.2
# (docs/prd/phase-4-manufacturing.md): for an active-ingredient line with
# standard potency Ps and picked batch potency Pa, qty = planned x Ps / Pa,
# rounded half away from zero to 6 decimals (numeric(18,6), the quantity
# precision of the platform); the compensating line is planned_comp - (qty -
# planned); a negative compensating line is refused (PMF-0002). A potency that
# is not positive, or a negative planned quantity, is invalid (COM-0001).
#
# The rows are adversarial on purpose: repeating fractions, values whose 7th
# decimal is exactly 5 (the half-up boundary), the compensating line landing on
# exactly zero and one micro-unit below it, potencies with decimals and above
# 100, quantities near numeric(18,6) limits, zero planned and zero filler.
# Run: python3 -I gen_pmf_potency_adv.py > pmf-potency-adv-40.csv
import csv
import sys
from fractions import Fraction as F

P = 6


def rnd(x):
    neg = x < 0
    n = abs(x) * 10**P
    f = n.numerator // n.denominator
    if n - f >= F(1, 2):
        f += 1
    v = F(f, 10**P)
    return -v if neg else v


def fmt(x):
    neg = x < 0
    n = abs(x) * 10**P
    assert n.denominator == 1, x
    n = n.numerator
    s = "%d.%06d" % (n // 10**P, n % 10**P)
    return "-" + s if neg else s


# planned, standard potency, batch potency, compensating planned, note
CASES = [
    ("100", "50", "40", "100", "BR-24.2 worked example"),
    ("100", "50", "30", "100", "repeating 166.6666..."),
    ("100", "50", "70", "100", "repeating 71.428571428..."),
    ("100", "33.3333", "66.6667", "100", "potencies with decimals"),
    ("12.345678", "47.5", "52.25", "87.654322", "planned at full precision"),
    ("0.000001", "50", "100", "1", "7th decimal exactly 5 rounds up"),
    ("0.000003", "1", "2", "1", "1.5 micro-units rounds up to 2"),
    ("0.000005", "1", "10", "1", "half a micro-unit rounds up to 1"),
    ("0.000001", "1", "3", "1", "a third of a micro-unit rounds to 0"),
    ("1", "2", "3", "0", "0.666666666... rounds up, filler grows from zero"),
    ("2", "1", "3", "0", "0.666666666... from a weaker standard"),
    ("1", "1", "7", "5", "1/7 rounds down"),
    ("100", "60", "37.5", "60", "filler lands on exactly zero"),
    ("100", "60", "37.5", "59.999999", "filler one micro-unit below zero"),
    ("100", "50", "25", "100", "filler exactly consumed"),
    ("100", "50", "24.999999", "100", "batch a hair weaker than the zero point"),
    ("100", "50", "80", "0", "stronger batch with no filler planned"),
    ("100", "50", "40", "0", "weaker batch with no filler planned"),
    ("0", "50", "40", "100", "nothing planned"),
    ("0", "50", "40", "0", "nothing planned, no filler"),
    ("250", "98.5", "99.7", "10", "high-purity API slightly stronger"),
    ("250", "98.5", "97.3", "10", "high-purity API slightly weaker"),
    ("250", "100", "102.4", "0", "assay above 100 %"),
    ("1000", "0.5", "0.4975", "50", "trace active at half a percent"),
    ("1000", "0.0001", "0.0003", "1", "smallest potencies"),
    ("100", "50", "0.0001", "1000000", "tiny batch potency, huge consumption refused"),
    ("999999.999999", "33.3333", "66.6667", "1", "planned at numeric(18,6) scale"),
    ("999999.999999", "66.6667", "33.3333", "1", "near-doubling of a huge line refused"),
    ("500000", "40", "39.99", "200", "0.025 % potency drift"),
    ("75.5", "12.25", "12.25", "24.5", "equal potencies are the identity"),
    ("0.000007", "45", "90", "0.000001", "half of 7 micro-units rounds up to 4"),
    ("0.000009", "45", "90", "0", "half of 9 micro-units, filler from zero"),
    ("3", "10", "9", "0.333333", "filler exactly consumed by a repeating qty"),
    ("3", "10", "9", "0.333332", "filler one micro-unit short of a repeating qty"),
    ("100", "0", "40", "100", "zero standard potency is invalid"),
    ("100", "50", "0", "100", "zero batch potency is invalid"),
    ("100", "50", "-40", "100", "negative batch potency is invalid"),
    ("-1", "50", "40", "100", "negative planned is invalid"),
    ("100", "50", "40", "-1", "negative filler is invalid"),
    ("1", "3", "1", "1.999999", "tripling one micro-unit short of the filler"),
]


def main():
    w = csv.writer(sys.stdout, lineterminator="\n")
    w.writerow(["planned", "standard_potency", "batch_potency", "compensating_planned", "expected_qty", "expected_compensating", "expected_code", "note"])
    for planned, ps, pa, comp, note in CASES:
        p, s, a, c = F(planned), F(ps), F(pa), F(comp)
        if s <= 0 or a <= 0 or p < 0 or c < 0:
            w.writerow([planned, ps, pa, comp, "", "", "COM-0001", note])
            continue
        qty = rnd(p * s / a)
        rest = c - (qty - p)
        if rest < 0:
            w.writerow([planned, ps, pa, comp, "", "", "PMF-0002", note])
            continue
        w.writerow([planned, ps, pa, comp, fmt(qty), fmt(rest), "", note])


if __name__ == "__main__":
    assert len(CASES) == 40
    main()
