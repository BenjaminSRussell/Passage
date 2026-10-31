#!/usr/bin/env python3
"""Generate testdata/proj-estimates-12x4.csv: 12 fixed-price projects x 4 period-end estimates
(Wave 41 AC1, PRD 8.41.9 fixture `proj-estimates-12x4`).

This is an independent reference for the cost-to-cost percentage-of-completion estimate of
BR-41.1 and the loss provision of BR-41.2. It shares no code with the Go engine
(internal/domain/poc.go): it works in exact rational arithmetic (fractions.Fraction) and rounds
once at the end. Before writing anything it checks itself against the PRD worked example
(BR-41.1, AC-41.1, AC-41.2) and the hand-derived rounding cases listed in SELF_CHECKS.

Run from anywhere (stdlib only, deterministic, no randomness):

    python3 -I services/projects/testdata/gen_poc_estimates.py

Reference rules, as implemented below (amounts in the project currency):

  R1  EAC = cost to date + estimate to complete (ETC).
  R2  completion = cost to date / EAC, exact; 0 when EAC is 0.
  R3  revenue to date = contract value x completion, rounded once, half-even, to the currency's
      ISO 4217 minor unit. The PRD text says "poc rounded to 4 decimals" but its own worked example
      (260,000 / 450,000 of 500,000 = 288,888.89, not 288,900.00) uses the unrounded ratio; the
      worked example wins and is asserted below.
  R4  poc (display) = completion rounded half-even to 4 decimals.
  R5  revenue this period = revenue to date - prior period's revenue to date (0 before period 1);
      it is negative when a higher estimate pulls cumulative revenue back (cumulative catch-up).
  R6  accrued revenue = max(revenue to date - billed to date, 0) (asset);
      deferred revenue = max(billed to date - revenue to date, 0) (liability).
  R7  gross profit to date = revenue to date - cost to date.
  R8  loss provision = max(EAC - contract value, 0): the full expected loss in the period it is
      found (BR-41.2, AC-41.2: 20,000.00 on the worked example's period 3).
  R9  provision movement = loss provision - prior period's loss provision (0 before period 1);
      a later improvement releases it, never below zero.

Open question for the accountant review (not decided here): R8 follows BR-41.2 literally, so in a
loss period the provision is the whole expected loss although gross profit to date already shows
part of it (the worked example's period 3 shows gross profit -15,000.00 and a 20,000.00
provision). Practice under ASC 605-35 / IAS 11 often provides only the loss not yet recognised.

The 12 projects cover: the PRD worked example run to completion (loss found, then partly
released); straight-line profit with billing behind and ahead; a loss found and fully released;
JPY (0 decimals) and KWD/BHD (3 decimals) amounts; half-even ties at the minor unit and at the
4-decimal poc; a contract raised by a change order mid-way; no cost yet with billing in advance;
an estimate increase that makes period revenue negative; zero margin; nine-digit amounts with
repeating ratios; and a project in loss from its first period.
"""

import csv
import os
from fractions import Fraction

MINOR = {"USD": 2, "EUR": 2, "GBP": 2, "JPY": 0, "KWD": 3, "BHD": 3}

# project id -> (currency, [(contract value, cost to date, ETC, billed to date) per period 1..4])
PROJECTS = {
    "P01-PRD-WORKED": ("USD", [
        ("500000.00", "120000.00", "280000.00", "100000.00"),
        ("500000.00", "260000.00", "190000.00", "300000.00"),
        ("500000.00", "390000.00", "130000.00", "400000.00"),
        ("500000.00", "515000.00", "0.00", "500000.00"),
    ]),
    "P02-STRAIGHT-LINE": ("USD", [
        ("1200000.00", "225000.00", "675000.00", "0.00"),
        ("1200000.00", "450000.00", "450000.00", "600000.00"),
        ("1200000.00", "675000.00", "235000.00", "600000.00"),
        ("1200000.00", "905000.00", "0.00", "1200000.00"),
    ]),
    "P03-LOSS-RELEASED": ("EUR", [
        ("800000.00", "150000.00", "550000.00", "120000.00"),
        ("800000.00", "400000.00", "460000.00", "380000.00"),
        ("800000.00", "560000.00", "220000.00", "560000.00"),
        ("800000.00", "730000.00", "40000.00", "760000.00"),
    ]),
    "P04-JPY-TIES": ("JPY", [
        ("7000001", "1", "1", "0"),
        ("7000001", "3", "1", "5000000"),
        ("7000003", "5", "3", "5000000"),
        ("7000003", "3333333", "6666667", "7000000"),
    ]),
    "P05-KWD-THIRDS": ("KWD", [
        ("250000.000", "10.000", "20.000", "0.000"),
        ("250000.000", "70000.000", "140000.000", "83333.333"),
        ("250000.000", "140000.000", "70000.000", "160000.000"),
        ("250000.000", "200000.000", "10000.000", "250000.000"),
    ]),
    "P06-CENT-TIES-CO": ("USD", [
        ("1000.01", "1.00", "1.00", "0.00"),
        ("1000.03", "2.00", "2.00", "500.00"),
        ("1000.03", "3.00", "1.00", "750.03"),
        ("1000.03", "4.00", "0.00", "1000.03"),
    ]),
    "P07-NO-COST-BILLED-AHEAD": ("USD", [
        ("450000.00", "0.00", "300000.00", "90000.00"),
        ("450000.00", "0.00", "300000.00", "90000.00"),
        ("450000.00", "100000.00", "200000.00", "90000.00"),
        ("450000.00", "210000.00", "105000.00", "300000.00"),
    ]),
    "P08-NEGATIVE-PERIOD": ("USD", [
        ("600000.00", "200000.00", "200000.00", "250000.00"),
        ("600000.00", "250000.00", "450000.00", "250000.00"),
        ("600000.00", "400000.00", "190000.00", "400000.00"),
        ("600000.00", "585000.00", "0.00", "600000.00"),
    ]),
    "P09-POC-4DP-TIES": ("USD", [
        ("2000000.00", "1.00", "19999.00", "0.00"),
        ("2000000.00", "3.00", "19997.00", "0.00"),
        ("2000000.00", "5.00", "19995.00", "1000.00"),
        ("2000000.00", "20000.00", "0.00", "2000000.00"),
    ]),
    "P10-ZERO-MARGIN": ("GBP", [
        ("300000.00", "75000.00", "225000.00", "75000.00"),
        ("300000.00", "150000.00", "150000.00", "150000.00"),
        ("300000.00", "225000.00", "75000.00", "225000.00"),
        ("300000.00", "300000.00", "0.00", "300000.00"),
    ]),
    "P11-LARGE-REPEATING": ("USD", [
        ("987654321.99", "123456789.01", "765432109.87", "100000000.00"),
        ("987654321.99", "333333333.33", "555555555.55", "400000000.00"),
        ("987654321.99", "666666666.67", "222222222.21", "700000000.00"),
        ("987654321.99", "888888888.89", "0.01", "987654321.99"),
    ]),
    "P12-BHD-LOSS-FROM-START": ("BHD", [
        ("50000.000", "12345.678", "41234.567", "15000.000"),
        ("50000.000", "27777.777", "27777.778", "30000.000"),
        ("50000.000", "41000.001", "12999.999", "45000.000"),
        ("50000.000", "53999.999", "0.001", "50000.000"),
    ]),
}


def rhe(x, places):
    """Round a Fraction half-even to `places` decimals, returning a Fraction."""
    scaled = x * 10 ** places
    q = scaled.numerator // scaled.denominator  # floor
    r = scaled - q
    if r > Fraction(1, 2) or (r == Fraction(1, 2) and q % 2 == 1):
        q += 1
    return Fraction(q, 10 ** places)


def fmt(x, places):
    """Format a Fraction that is exact at `places` decimals."""
    scaled = x * 10 ** places
    assert scaled.denominator == 1, (x, places)
    n = scaled.numerator
    sign = "-" if n < 0 else ""
    n = abs(n)
    if places == 0:
        return f"{sign}{n}"
    s = str(n).rjust(places + 1, "0")
    return f"{sign}{s[:-places]}.{s[-places:]}"


def estimate(currency, contract, cost, etc, billed, prior_revenue, prior_provision):
    """R1..R9 for one period. Inputs are Fractions; returns a dict of Fractions."""
    m = MINOR[currency]
    eac = cost + etc
    completion = cost / eac if eac > 0 else Fraction(0)
    revenue = rhe(contract * completion, m)
    provision = max(eac - contract, Fraction(0))
    return {
        "eac": eac,
        "poc": rhe(completion, 4),
        "revenue_to_date": revenue,
        "revenue_this_period": revenue - prior_revenue,
        "accrued_revenue": max(revenue - billed, Fraction(0)),
        "deferred_revenue": max(billed - revenue, Fraction(0)),
        "gross_profit_to_date": revenue - cost,
        "loss_provision": provision,
        "provision_movement": provision - prior_provision,
    }


def run(currency, periods):
    rows, prior_rev, prior_prov = [], Fraction(0), Fraction(0)
    for contract, cost, etc, billed in periods:
        f = [Fraction(v) for v in (contract, cost, etc, billed)]
        out = estimate(currency, *f, prior_rev, prior_prov)
        rows.append((f, prior_rev, prior_prov, out))
        prior_rev, prior_prov = out["revenue_to_date"], out["loss_provision"]
    return rows


# Hand-derived literals: (project, period, field, expected). The PRD values are quoted from
# BR-41.1 / AC-41.1 / AC-41.2; the others were worked by hand from the inputs above.
SELF_CHECKS = [
    ("P01-PRD-WORKED", 1, "revenue_this_period", "150000.00"),
    ("P01-PRD-WORKED", 1, "accrued_revenue", "50000.00"),
    ("P01-PRD-WORKED", 1, "poc", "0.3000"),
    ("P01-PRD-WORKED", 2, "eac", "450000.00"),
    ("P01-PRD-WORKED", 2, "poc", "0.5778"),
    ("P01-PRD-WORKED", 2, "revenue_to_date", "288888.89"),
    ("P01-PRD-WORKED", 2, "revenue_this_period", "138888.89"),
    ("P01-PRD-WORKED", 2, "deferred_revenue", "11111.11"),
    ("P01-PRD-WORKED", 2, "gross_profit_to_date", "28888.89"),
    ("P01-PRD-WORKED", 3, "loss_provision", "20000.00"),       # AC-41.2, EAC 520,000
    ("P01-PRD-WORKED", 4, "provision_movement", "-5000.00"),   # EAC 515,000: release 5,000
    ("P03-LOSS-RELEASED", 2, "loss_provision", "60000.00"),    # EAC 860,000
    ("P03-LOSS-RELEASED", 3, "provision_movement", "-60000.00"),  # EAC 780,000: never below zero
    ("P04-JPY-TIES", 1, "revenue_to_date", "3500000"),         # 3,500,000.5 -> even
    ("P04-JPY-TIES", 2, "revenue_to_date", "5250001"),         # 5,250,000.75 -> up
    ("P04-JPY-TIES", 3, "revenue_to_date", "4375002"),         # 7,000,003 x 5/8 = 4,375,001.875
    ("P06-CENT-TIES-CO", 1, "revenue_to_date", "500.00"),      # 500.005 -> even
    ("P06-CENT-TIES-CO", 2, "revenue_to_date", "500.02"),      # 500.015 -> even
    ("P06-CENT-TIES-CO", 3, "revenue_to_date", "750.02"),      # 750.0225
    ("P06-CENT-TIES-CO", 3, "accrued_revenue", "0.00"),
    ("P06-CENT-TIES-CO", 3, "deferred_revenue", "0.01"),
    ("P07-NO-COST-BILLED-AHEAD", 1, "deferred_revenue", "90000.00"),
    ("P07-NO-COST-BILLED-AHEAD", 2, "revenue_this_period", "0.00"),
    ("P08-NEGATIVE-PERIOD", 2, "revenue_to_date", "214285.71"),  # 600,000 x 250/700
    ("P08-NEGATIVE-PERIOD", 2, "revenue_this_period", "-85714.29"),
    ("P08-NEGATIVE-PERIOD", 2, "loss_provision", "100000.00"),
    ("P09-POC-4DP-TIES", 1, "poc", "0.0000"),                  # 0.00005 -> even
    ("P09-POC-4DP-TIES", 1, "revenue_to_date", "100.00"),
    ("P09-POC-4DP-TIES", 2, "poc", "0.0002"),                  # 0.00015 -> even
    ("P09-POC-4DP-TIES", 3, "poc", "0.0002"),                  # 0.00025 -> even
    ("P05-KWD-THIRDS", 1, "revenue_to_date", "83333.333"),
    ("P05-KWD-THIRDS", 3, "revenue_to_date", "166666.667"),
    ("P10-ZERO-MARGIN", 3, "gross_profit_to_date", "0.00"),
]


def main():
    here = os.path.dirname(os.path.abspath(__file__))
    results = {pid: (cur, run(cur, periods)) for pid, (cur, periods) in PROJECTS.items()}
    assert len(results) == 12 and all(len(r) == 4 for _, r in results.values())
    for pid, period, field, want in SELF_CHECKS:
        cur, rows = results[pid]
        places = 4 if field == "poc" else MINOR[cur]
        got = fmt(rows[period - 1][3][field], places)
        assert got == want, f"self-check {pid} p{period} {field}: {got} != {want}"

    fields = ["eac", "poc", "revenue_to_date", "revenue_this_period", "accrued_revenue",
              "deferred_revenue", "gross_profit_to_date", "loss_provision", "provision_movement"]
    path = os.path.join(here, "proj-estimates-12x4.csv")
    with open(path, "w", newline="") as fh:
        w = csv.writer(fh, lineterminator="\n")
        w.writerow(["project", "period", "currency", "contract_value", "cost_to_date", "etc",
                    "billed_to_date", "prior_revenue_to_date", "prior_loss_provision"]
                   + ["expect_" + f for f in fields])
        for pid, (cur, rows) in results.items():
            m = MINOR[cur]
            for i, (inp, prior_rev, prior_prov, out) in enumerate(rows, start=1):
                w.writerow([pid, i, cur] + [fmt(v, m) for v in inp] + [fmt(prior_rev, m), fmt(prior_prov, m)]
                           + [fmt(out[f], 4 if f == "poc" else m) for f in fields])
    print(f"wrote {path}: {sum(len(r) for _, r in results.values())} estimates, {len(SELF_CHECKS)} self-checks passed")


if __name__ == "__main__":
    main()
