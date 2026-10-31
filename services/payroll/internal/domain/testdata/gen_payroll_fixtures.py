#!/usr/bin/env python3
"""Independent reference for the wave-43 payroll proof fixtures.

Run from the repository root with an isolated interpreter:

    python3 -I services/payroll/internal/domain/testdata/gen_payroll_fixtures.py [outdir]

It writes, next to this script:

  payroll-us-200.csv   200 US gross-to-net scenarios (AC1 / AC-43.3)
  payroll-retro-25.json 25 retro recalculation cases (AC2 / AC-43.4)

Nothing here imports or mirrors the Go engine. Amounts are exact fractions and
are rounded half-up to cents once per line.

Federal withholding is 2025 Pub 15-T Worksheet 1A (annual percentage method
for automated payroll systems):

* The STANDARD schedules are not typed in: they are derived from the 2025
  taxable-income brackets (Rev. Proc. 2024-40) and the 2025 standard
  deductions, shifted by the Worksheet 1A line 1g amount, and the tax is
  computed as a marginal sum over the brackets rather than a row lookup. The
  published standard rows are typed separately and must equal the derivation.
* The STEP 2 CHECKBOX schedules are the published Pub 15-T rows (the IRS rounds
  the halved thresholds to whole dollars and the halved base taxes to cents);
  the script checks each row against half the standard derivation.

FICA follows Pub 15 (Circular E) for 2025: Social Security 6.2% to the
176,100 wage base, Medicare 1.45% plus the employee-only 0.9% over 200,000 of
YTD wages. FUTA 0.6% to 7,000. Colorado is the pack's flat 4.40% on federal
taxable wages (BR-43.1), the only state the engine ships.
"""

import csv
import json
import os
import random
import sys
from fractions import Fraction as F

HERE = os.path.dirname(os.path.abspath(__file__))
# Optional argument: write the fixtures to another directory (the Go test uses
# it to check that the committed files are what this reference produces).
OUT = sys.argv[1] if len(sys.argv) > 1 else HERE

# ---------------------------------------------------------------- 2025 tables

# Rev. Proc. 2024-40 taxable-income brackets: (lower bound, marginal rate).
BRACKETS = {
    "S": [(0, "0.10"), (11925, "0.12"), (48475, "0.22"), (103350, "0.24"),
          (197300, "0.32"), (250525, "0.35"), (626350, "0.37")],
    "MFJ": [(0, "0.10"), (23850, "0.12"), (96950, "0.22"), (206700, "0.24"),
            (394600, "0.32"), (501050, "0.35"), (751600, "0.37")],
    "HOH": [(0, "0.10"), (17000, "0.12"), (64850, "0.22"), (103350, "0.24"),
            (197300, "0.32"), (250500, "0.35"), (626350, "0.37")],
}
STD_DEDUCTION = {"S": 15000, "MFJ": 30000, "HOH": 22500}
LINE_1G = {"S": 8600, "MFJ": 12900, "HOH": 8600}  # Worksheet 1A line 1g
ALLOWANCE = 4300  # Worksheet 1A line 1k, per pre-2020 allowance

# Published Pub 15-T 2025 STANDARD rows (A = at least, C = base tax, D = rate).
PUB_STANDARD = {
    "MFJ": [(0, "0", "0"), (17100, "0", "0.10"), (40950, "2385", "0.12"), (114050, "11157", "0.22"),
            (223800, "35302", "0.24"), (411700, "80398", "0.32"), (518150, "114462", "0.35"),
            (768700, "202154.50", "0.37")],
    "S": [(0, "0", "0"), (6400, "0", "0.10"), (18325, "1192.50", "0.12"), (54875, "5578.50", "0.22"),
          (109750, "17651", "0.24"), (203700, "40199", "0.32"), (256925, "57231", "0.35"),
          (632750, "188769.75", "0.37")],
    "HOH": [(0, "0", "0"), (13900, "0", "0.10"), (30900, "1700", "0.12"), (78750, "7442", "0.22"),
            (117250, "15912", "0.24"), (211200, "38460", "0.32"), (264400, "55484", "0.35"),
            (640250, "187031.50", "0.37")],
}
# Published Pub 15-T 2025 STEP 2 CHECKBOX rows.
PUB_CHECKBOX = {
    "MFJ": [(0, "0", "0"), (15000, "0", "0.10"), (26925, "1192.50", "0.12"), (63475, "5578.50", "0.22"),
            (118350, "17651", "0.24"), (212300, "40199", "0.32"), (265525, "57231", "0.35"),
            (390800, "101077.25", "0.37")],
    "S": [(0, "0", "0"), (7500, "0", "0.10"), (13463, "596.25", "0.12"), (31738, "2789.25", "0.22"),
          (59175, "8825.50", "0.24"), (106150, "20099.50", "0.32"), (132763, "28615.50", "0.35"),
          (320675, "94384.88", "0.37")],
    "HOH": [(0, "0", "0"), (11250, "0", "0.10"), (19750, "850", "0.12"), (43675, "3721", "0.22"),
            (62925, "7956", "0.24"), (109900, "19230", "0.32"), (136500, "27742", "0.35"),
            (324425, "93515.75", "0.37")],
}

SS_RATE, SS_BASE = F("0.062"), F(176100)
MED_RATE, ADDL_MED_RATE, ADDL_MED_THRESHOLD = F("0.0145"), F("0.009"), F(200000)
FUTA_RATE, FUTA_BASE = F("0.006"), F(7000)
CO_RATE = F("0.044")

STATUS_COLUMN = {"SINGLE": "S", "MARRIED_FILING_SEPARATELY": "S",
                 "MARRIED_FILING_JOINTLY": "MFJ", "HEAD_OF_HOUSEHOLD": "HOH"}


def cents(x):
    """Round half away from zero to 0.01."""
    sign = -1 if x < 0 else 1
    a = abs(F(x)) * 100
    whole = a.numerator // a.denominator
    if a - whole >= F(1, 2):
        whole += 1
    return F(sign * whole, 100)


def fmt(x):
    x = F(x)
    assert x == cents(x), x
    sign = "-" if x < 0 else ""
    c = abs(x * 100)
    assert c.denominator == 1
    c = c.numerator
    return "%s%d.%02d" % (sign, c // 100, c % 100)


def marginal_tax(col, taxable):
    """Income tax on taxable income as a marginal sum over the brackets."""
    tax = F(0)
    rows = BRACKETS[col]
    for i, (lo, rate) in enumerate(rows):
        hi = rows[i + 1][0] if i + 1 < len(rows) else None
        if taxable <= lo:
            break
        top = taxable if hi is None else min(taxable, F(hi))
        tax += (top - lo) * F(rate)
    return tax


def standard_annual_tax(col, adjusted):
    # The standard schedule's zero row ends at standard deduction - line 1g.
    start = STD_DEDUCTION[col] - LINE_1G[col]
    return marginal_tax(col, max(F(0), adjusted - start))


def lookup(rows, adjusted):
    best = rows[0]
    for r in rows:
        if adjusted >= r[0]:
            best = r
    over, base, rate = best
    return F(base) + (adjusted - over) * F(rate)


def check_tables():
    for col, rows in PUB_STANDARD.items():
        start = STD_DEDUCTION[col] - LINE_1G[col]
        assert rows[1][0] == start, (col, rows[1])
        for over, base, rate in rows[1:]:
            assert F(base) == standard_annual_tax(col, F(over)), (col, over)
            # One dollar into the row, the marginal rate is the row's rate.
            assert standard_annual_tax(col, F(over + 1)) - F(base) == F(rate), (col, over)
    for col, rows in PUB_CHECKBOX.items():
        std = PUB_STANDARD[col]
        for (over, base, rate), (s_over, s_base, s_rate) in zip(rows, std):
            # Half the bracket bound and half the standard deduction: the
            # standard row start plus line 1g, halved (within the IRS rounding).
            half_over = F(s_over + LINE_1G[col], 2) if s_over else F(0)
            assert abs(F(over) - half_over) <= F(1, 2), (col, over, half_over)
            assert abs(F(base) - F(s_base) / 2) <= F(1, 200), (col, base)
            assert rate == s_rate


def federal_wh(w4, wage, n):
    """Worksheet 1A. Returns (withholding, adjusted annual wage, schedule)."""
    col = STATUS_COLUMN[w4["filing"]]
    if w4["legacy"] and w4["filing"] == "HEAD_OF_HOUSEHOLD":
        raise ValueError("pre-2020 W-4 has no head of household")
    annual = wage * n                                         # 1c
    schedule = "STANDARD"
    if w4["legacy"]:
        annual -= ALLOWANCE * w4["allowances"]                # 1l
    else:
        annual += w4["step4a"]                                # 1e
        if w4["step2"]:
            schedule = "CHECKBOX"
            annual -= w4["step4b"]                            # 1h = 1f + 0
        else:
            annual -= w4["step4b"] + LINE_1G[col]             # 1h = 1f + 1g
    annual = max(F(0), annual)                                # 1i / 1l
    if schedule == "STANDARD":
        tentative = standard_annual_tax(col, annual) / n      # 2h
    else:
        tentative = lookup(PUB_CHECKBOX[col], annual) / n
    if not w4["legacy"]:
        tentative = max(F(0), tentative - w4["step3"] / n)    # 3c
    return cents(tentative + w4["step4c"]), annual, schedule  # 4b


def gross_to_net(s):
    gross = s["regular"] + s["overtime"] + sum((e for e in s.get("extra_earnings", [])), F(0))
    pre = s["pretax_401k"] + s["pretax_s125"]
    assert pre < gross
    fed_wages = gross - pre
    fica_wages = gross - s["pretax_s125"]
    fed, adjusted, schedule = federal_wh(s["w4"], fed_wages, s["periods"])
    ss_base = max(F(0), min(fica_wages, SS_BASE - s["ytd_fica"]))
    ss = cents(ss_base * SS_RATE)
    addl_base = max(F(0), min(fica_wages, s["ytd_fica"] + fica_wages - ADDL_MED_THRESHOLD))
    med = cents(fica_wages * MED_RATE + addl_base * ADDL_MED_RATE)
    state = cents(fed_wages * CO_RATE) if s["state"] == "CO" else F(0)
    net = gross - pre - fed - ss - med - state
    futa = cents(max(F(0), min(fica_wages, FUTA_BASE - s["ytd_futa"])) * FUTA_RATE)
    med_er = cents(fica_wages * MED_RATE)
    return {
        "gross": gross, "fed_wages": fed_wages, "fica_wages": fica_wages, "adjusted": adjusted,
        "schedule": schedule, "fed": fed, "ss": ss, "med": med, "state": state, "net": net,
        "futa": futa, "med_er": med_er, "employer": futa + ss + med_er,
    }


def w4(filing="SINGLE", legacy=False, allowances=0, step2=False, step3=0, step4a=0, step4b=0, step4c=0):
    return {"filing": filing, "legacy": legacy, "allowances": allowances, "step2": step2,
            "step3": F(step3), "step4a": F(step4a), "step4b": F(step4b), "step4c": F(step4c)}


def check_anchors():
    """Hand-worked Worksheet 1A cases (independent of this file's tables)."""
    def fed(w, wage, n):
        return fmt(federal_wh(w, F(wage), n)[0])
    assert fed(w4(), "3760", 26) == "504.66"                     # 89,160 -> 13,121.20 / 26
    assert fed(w4(legacy=True), "3760", 26) == "577.43"          # PRD BR-43.1 arithmetic
    assert fed(w4("MARRIED_FILING_JOINTLY"), "3760", 26) == "294.39"
    assert fed(w4("HEAD_OF_HOUSEHOLD"), "5000", 12) == "346.67"
    assert fed(w4(step2=True), "3760", 26) == "695.61"
    assert fed(w4(step3=2000, step4c=25), "3760", 26) == "452.74"
    assert fed(w4(step4a=5200, step4b=2600), "3760", 26) == "526.66"
    assert fed(w4("MARRIED_FILING_JOINTLY", legacy=True, allowances=2), "1500", 52) == "111.52"
    assert fed(w4(), "60000", 12) == "18155.85"
    # AC-43.2: YTD 174,000 + 4,000 -> SS on 2,100 only.
    r = gross_to_net(scenario(26, w4(), "", "4000", ytd_fica="174000"))
    assert fmt(r["ss"]) == "130.20"
    # BR-43.1 full statement on the pre-2020 W-4 (the PRD figures).
    r = gross_to_net(scenario(26, w4(legacy=True), "CO", "4000", pretax_401k="240"))
    assert (fmt(r["fed"]), fmt(r["ss"]), fmt(r["med"]), fmt(r["state"]), fmt(r["net"])) == \
        ("577.43", "248.00", "58.00", "165.44", "2711.13")


def scenario(periods, w, state, regular, overtime="0", pretax_401k="0", pretax_s125="0",
             ytd_fica="0", ytd_futa=None, group="random"):
    ytd_futa = ytd_fica if ytd_futa is None else ytd_futa
    return {"periods": periods, "w4": w, "state": state, "regular": F(regular), "overtime": F(overtime),
            "pretax_401k": F(pretax_401k), "pretax_s125": F(pretax_s125), "ytd_fica": F(ytd_fica),
            "ytd_futa": F(ytd_futa), "group": group}


def ceil_cents(x):
    c = F(x) * 100
    whole = -((-c.numerator) // c.denominator)
    return F(whole, 100)


def floor_cents(x):
    c = F(x) * 100
    return F(c.numerator // c.denominator, 100)


FREQS = [1, 2, 4, 12, 24, 26, 52, 260]
FREQ_WEIGHTS = [1, 1, 2, 6, 8, 14, 8, 2]


def boundary_scenarios(rng):
    """Adjusted annual wage exactly at / just past a row start, and one cent per
    period below it: the row lookup must switch exactly there."""
    out = []
    picks = []
    for schedule in ("STANDARD", "CHECKBOX"):
        for col, filing in (("S", "SINGLE"), ("MFJ", "MARRIED_FILING_JOINTLY"), ("HOH", "HEAD_OF_HOUSEHOLD")):
            rows = (PUB_STANDARD if schedule == "STANDARD" else PUB_CHECKBOX)[col]
            for idx in rng.sample(range(1, len(rows)), 2):
                picks.append((schedule, filing, col, rows[idx][0]))
    for schedule, filing, col, over in picks:
        n = rng.choice([1, 4, 12, 24, 26, 52])
        step4a = rng.choice([0, 0, 1200])
        step4b = rng.choice([0, 0, 3000])
        w = w4(filing, step2=(schedule == "CHECKBOX"), step4a=step4a, step4b=step4b)
        shift = (0 if schedule == "CHECKBOX" else LINE_1G[col]) - step4a + step4b
        annual_needed = F(over + shift)
        pre = F(rng.choice([0, 0, 150]))
        at = ceil_cents(annual_needed / n)
        below = at - F(1, 100)
        for wage, tag in ((at, "boundary-at"), (below, "boundary-below")):
            out.append(scenario(n, w, rng.choice(["", "CO"]), wage + pre, pretax_401k=pre, group=tag))
    return out


def targeted_scenarios():
    s = []
    # Social Security wage base: crossing, exactly reaching, already past.
    for ytd, gross in (("174000", "4000"), ("175000", "3500.55"), ("176099.99", "800"),
                       ("172100", "4000"), ("176100", "5000"), ("180000", "6923.08"),
                       ("170000", "12000"), ("160000", "16100")):
        s.append(scenario(26, w4(), "CO", gross, ytd_fica=ytd, group="ss-base"))
    # Additional Medicare: crossing, landing exactly on 200,000, already past.
    for ytd, gross, filing in (("199000", "4000", "SINGLE"), ("196000", "4000", "MARRIED_FILING_JOINTLY"),
                               ("195000.01", "4999.99", "HEAD_OF_HOUSEHOLD"), ("210000", "8000", "SINGLE"),
                               ("199999.99", "0.02", "SINGLE"), ("150000", "75000", "MARRIED_FILING_JOINTLY")):
        s.append(scenario(26 if gross != "75000" else 4, w4(filing), "", gross, ytd_fica=ytd, group="addl-medicare"))
    # Step 3 credit larger than, equal to, and part of the tentative amount.
    for gross, credit, n in (("1000", "4000", 26), ("1800", "2000", 26), ("2500", "6000", 24),
                             ("900", "500", 52), ("3000", "2000", 12), ("15000", "8000", 4)):
        s.append(scenario(n, w4("MARRIED_FILING_JOINTLY", step3=credit), "", gross, group="step3-credit"))
    # Step 4(b) deductions driving the adjusted wage below zero (clamped), with 4(c) extra.
    for gross, d4b, extra in (("800", "40000", "0"), ("1200", "50000", "15"), ("300", "20000", "7.50")):
        s.append(scenario(26, w4(step4b=d4b, step4c=extra), "CO", gross, group="step4b-clamp"))
    # Below the zero-rate row; tiny wages; daily payroll.
    for gross, n, filing in (("150", 52, "SINGLE"), ("40", 260, "HEAD_OF_HOUSEHOLD"),
                             ("1300", 12, "MARRIED_FILING_JOINTLY"), ("0.01", 26, "SINGLE")):
        s.append(scenario(n, w4(filing), "", gross, group="below-zero-row"))
    # Top bracket and supplemental-size regular pay.
    for gross, n, filing, step2 in (("60000", 12, "SINGLE", False), ("45000", 24, "MARRIED_FILING_JOINTLY", True),
                                    ("400000", 1, "HEAD_OF_HOUSEHOLD", False), ("38461.54", 26, "MARRIED_FILING_SEPARATELY", True)):
        s.append(scenario(n, w4(filing, step2=step2), "CO", gross, ytd_fica="0", group="top-bracket"))
    # Pre-2020 W-4: allowances, married, married filing separately (Single column).
    for gross, n, filing, allow, extra in (("1500", 52, "MARRIED_FILING_JOINTLY", 2, "0"),
                                           ("2200", 24, "SINGLE", 0, "0"), ("2200", 24, "SINGLE", 3, "20"),
                                           ("5400", 12, "MARRIED_FILING_SEPARATELY", 1, "0"),
                                           ("700", 26, "MARRIED_FILING_JOINTLY", 9, "0"),
                                           ("9100", 26, "MARRIED_FILING_JOINTLY", 4, "50")):
        s.append(scenario(n, w4(filing, legacy=True, allowances=allow, step4c=extra), "", gross, group="pre-2020"))
    # Section 125 reduces FICA/FUTA wages, 401(k) does not; both near the FUTA base.
    for gross, k401, s125, ytd in (("4000", "240", "100", "0"), ("3100", "0", "450.25", "5000"),
                                   ("2750", "550", "0", "6900"), ("6200", "1240", "310", "6990")):
        s.append(scenario(26, w4(), "CO", gross, pretax_401k=k401, pretax_s125=s125, ytd_fica=ytd,
                          group="pretax-fica"))
    return s


def random_scenarios(rng, count):
    out = []
    statuses = ["SINGLE", "MARRIED_FILING_JOINTLY", "MARRIED_FILING_SEPARATELY", "HEAD_OF_HOUSEHOLD"]
    while len(out) < count:
        filing = rng.choice(statuses)
        legacy = filing != "HEAD_OF_HOUSEHOLD" and rng.random() < 0.2
        n = rng.choices(FREQS, FREQ_WEIGHTS)[0]
        salary = F(round(12000 * (75 ** rng.random())))  # 12k .. 900k, log-uniform
        regular = cents(salary / n)
        overtime = cents(regular * F(rng.choice([0, 0, 0, 5, 10, 18]), 100))
        gross = regular + overtime
        k401 = cents(gross * F(rng.choice([0, 0, 3, 6, 10, 15]), 100))
        s125 = F(rng.choice([0, 0, 0, 45, 120, 260])) if gross > 600 else F(0)
        if k401 + s125 >= gross:
            continue
        if legacy:
            w = w4(filing, legacy=True, allowances=rng.choice([0, 0, 1, 2, 3, 5]),
                   step4c=rng.choice([0, 0, 0, 10, 25]))
        else:
            w = w4(filing, step2=rng.random() < 0.25,
                   step3=rng.choice([0, 0, 0, 500, 2000, 4000, 6500]),
                   step4a=rng.choice([0, 0, 0, 0, 2500, 12000]),
                   step4b=rng.choice([0, 0, 0, 0, 4000, 18000]),
                   step4c=rng.choice([0, 0, 0, 0, "12.50", 40]))
        elapsed = rng.randrange(0, n)
        ytd = gross * elapsed
        out.append(scenario(n, w, rng.choice(["", "CO"]), regular, overtime=overtime, pretax_401k=k401,
                            pretax_s125=s125, ytd_fica=ytd, ytd_futa=ytd, group="random"))
    return out


US_HEADER = ["id", "group", "periods", "filing", "legacy", "allowances", "step2", "step3", "step4a",
             "step4b", "step4c", "state", "regular", "overtime", "pretax_401k", "pretax_s125", "ytd_fica",
             "ytd_futa", "exp_fed_wages", "exp_fica_wages", "exp_adjusted_annual", "exp_schedule",
             "exp_fed_wh", "exp_ss", "exp_medicare", "exp_state", "exp_net", "exp_futa", "exp_medicare_er",
             "exp_employer_cost"]


def write_us_200():
    rng = random.Random(4303)
    scenarios = boundary_scenarios(rng) + targeted_scenarios()
    scenarios += random_scenarios(rng, 200 - len(scenarios))
    assert len(scenarios) == 200, len(scenarios)
    path = os.path.join(OUT, "payroll-us-200.csv")
    with open(path, "w", newline="") as f:
        out = csv.writer(f, lineterminator="\n")
        out.writerow(US_HEADER)
        for i, s in enumerate(scenarios, 1):
            r = gross_to_net(s)
            w = s["w4"]
            out.writerow([
                "US-%03d" % i, s["group"], s["periods"], w["filing"], str(w["legacy"]).lower(), w["allowances"],
                str(w["step2"]).lower(), fmt(w["step3"]), fmt(w["step4a"]), fmt(w["step4b"]), fmt(w["step4c"]),
                s["state"], fmt(s["regular"]), fmt(s["overtime"]), fmt(s["pretax_401k"]), fmt(s["pretax_s125"]),
                fmt(s["ytd_fica"]), fmt(s["ytd_futa"]), fmt(r["fed_wages"]), fmt(r["fica_wages"]),
                fmt(r["adjusted"]), r["schedule"], fmt(r["fed"]), fmt(r["ss"]), fmt(r["med"]), fmt(r["state"]),
                fmt(r["net"]), fmt(r["futa"]), fmt(r["med_er"]), fmt(r["employer"]),
            ])
    groups = {}
    for s in scenarios:
        groups[s["group"]] = groups.get(s["group"], 0) + 1
    return groups


# ---------------------------------------------------------------- retro (AC2)

def period_earnings(rate, hours, ot_hours, shift_hours=0, shift_rate=0):
    e = {"REGULAR": cents(F(rate) * F(hours))}
    if ot_hours:
        e["OVERTIME"] = cents(F(rate) * F("1.5") * F(ot_hours))
    if shift_hours:
        e["SHIFT_DIFF"] = cents(F(shift_rate) * F(shift_hours))
    return e


def retro_cases():
    rng = random.Random(4304)
    cases = []

    def add(name, periods, w, state, k401, s125, ytd, prior, recalc, current):
        cases.append({"name": name, "periods": periods, "w4": w, "state": state, "k401": F(k401),
                      "s125": F(s125), "ytd": F(ytd), "prior": prior, "recalc": recalc, "current": current})

    # Rate increases effective 1..4 periods back, odd rates so per-period rounding matters.
    for i in range(10):
        k = 1 + i % 4
        old = F(rng.choice(["18.375", "22.40", "27.125", "31.333", "44.10", "19.995"]))
        new = old + F(rng.choice(["0.625", "1.10", "2.333", "0.015", "3.75"]))
        hours = [rng.choice([72, 76.5, 80, 80, 84.25]) for _ in range(k)]
        ot = [rng.choice([0, 0, 3.5, 6, 10.25]) for _ in range(k)]
        prior = [period_earnings(old, h, o) for h, o in zip(hours, ot)]
        recalc = [period_earnings(new, h, o) for h, o in zip(hours, ot)]
        current = period_earnings(new, 80, rng.choice([0, 4]))
        filing = rng.choice(["SINGLE", "MARRIED_FILING_JOINTLY", "HEAD_OF_HOUSEHOLD"])
        add("rate-increase-%d-periods-%d" % (k, i), 26, w4(filing, step2=i % 3 == 0, step3=(2000 if i % 4 == 1 else 0)),
            rng.choice(["", "CO"]), rng.choice(["0", "120", "250"]), rng.choice(["0", "0", "85"]),
            str(cents(sum(current.values()) * rng.randrange(1, 20))), prior, recalc, current)
    # AC-43.4 shape on salary: 4,000 -> 4,200 two periods back (BR-43.1 worker).
    add("ac-43.4-salary-two-periods", 26, w4(), "CO", "240", "0", "40000",
        [{"REGULAR": F(4000)}, {"REGULAR": F(4000)}], [{"REGULAR": F(4200)}, {"REGULAR": F(4200)}],
        {"REGULAR": F(4200)})
    # Rate decreases (negative deltas reduce current gross).
    add("rate-decrease-3-periods", 52, w4("MARRIED_FILING_JOINTLY"), "", "0", "0", "18000",
        [period_earnings("26.50", 40, 5)] * 3, [period_earnings("25.75", 40, 5)] * 3, period_earnings("25.75", 40, 0))
    add("rate-decrease-weekly-legacy", 52, w4("SINGLE", legacy=True, allowances=1), "CO", "50", "0", "9000",
        [period_earnings("19.10", 38, 0)] * 2, [period_earnings("18.95", 38, 0)] * 2, period_earnings("18.95", 40, 0))
    # Time corrections: hours change under the same rate.
    add("hours-correction-overtime-added", 26, w4(), "CO", "0", "0", "22000",
        [period_earnings("30", 80, 0)], [period_earnings("30", 80, "7.75")], period_earnings("30", 80, 0))
    add("hours-correction-overtime-removed", 24, w4("HEAD_OF_HOUSEHOLD"), "", "100", "0", "15000",
        [period_earnings("28.40", "86.67", 6), period_earnings("28.40", "86.67", 2)],
        [period_earnings("28.40", "86.67", 0), period_earnings("28.40", "86.67", 0)],
        period_earnings("28.40", "86.67", 0))
    add("hours-correction-regular-down", 26, w4("MARRIED_FILING_JOINTLY", step2=True), "", "0", "60", "30000",
        [period_earnings("41.20", 80, 0)], [period_earnings("41.20", "76.5", 0)], period_earnings("41.20", 80, 0))
    # A code only in the recalculation (shift differential granted retroactively).
    add("new-code-shift-diff", 26, w4(), "CO", "0", "0", "12000",
        [period_earnings("24", 80, 0)] * 3,
        [period_earnings("24", 80, 0, 24, "1.85"), period_earnings("24", 80, 0, 32, "1.85"),
         period_earnings("24", 80, 0, "16.5", "1.85")],
        period_earnings("24", 80, 0, 24, "1.85"))
    # Mixed signs: overtime reclassified into regular hours.
    add("overtime-reclassified-to-regular", 26, w4("MARRIED_FILING_SEPARATELY"), "", "0", "0", "8000",
        [period_earnings("33.30", 80, 8)], [period_earnings("33.30", 88, 0)], period_earnings("33.30", 80, 0))
    # Deltas that net to zero across periods produce no line.
    add("deltas-net-to-zero", 26, w4(), "", "0", "0", "6000",
        [period_earnings("20", 80, 4), period_earnings("20", 80, 0)],
        [period_earnings("20", 80, 0), period_earnings("20", 80, 4)], period_earnings("20", 80, 0))
    # Retro pushes the current period across the Social Security wage base.
    add("retro-crosses-ss-base", 26, w4("MARRIED_FILING_JOINTLY"), "CO", "500", "0", "173500",
        [{"REGULAR": F("6500")}] * 4, [{"REGULAR": F("6900")}] * 4, {"REGULAR": F("6900")})
    # Retro pushes the current period across the additional Medicare threshold.
    add("retro-crosses-addl-medicare", 24, w4(), "", "0", "200", "196500",
        [{"REGULAR": F("8333.33")}] * 3, [{"REGULAR": F("8750.00")}] * 3, {"REGULAR": F("8750.00")})
    # Monthly salaried worker, raise backdated 4 months on the Step 2 checkbox schedule.
    add("monthly-raise-4-months", 12, w4("SINGLE", step2=True, step4c=30), "CO", "600", "150", "40000",
        [{"REGULAR": F("9166.67")}] * 4, [{"REGULAR": F("9625.00")}] * 4, {"REGULAR": F("9625.00")})
    # Rate and hours change together in one period, rate only in the other.
    add("rate-and-hours-mixed", 26, w4("HEAD_OF_HOUSEHOLD", step3=4000), "CO", "175", "95", "26000",
        [period_earnings("29.85", 80, 4), period_earnings("29.85", 80, 0)],
        [period_earnings("31.10", "82.5", 6), period_earnings("31.10", 80, 0)],
        period_earnings("31.10", 80, 2))
    # Semimonthly pre-2020 W-4, raise with overtime in every period.
    add("semimonthly-legacy-raise", 24, w4("MARRIED_FILING_JOINTLY", legacy=True, allowances=3), "", "0", "0", "52000",
        [period_earnings("37.777", "86.67", 3)] * 2, [period_earnings("39.004", "86.67", 3)] * 2,
        period_earnings("39.004", "86.67", 3))
    # Tiny delta: a one-cent rate correction over one weekly period.
    add("one-cent-rate-correction", 52, w4(), "", "0", "0", "1500",
        [period_earnings("15.00", 40, 0)], [period_earnings("15.01", 40, 0)], period_earnings("15.01", 40, 0))
    assert len(cases) == 25, len(cases)
    return cases


def write_retro_25():
    out = []
    for c in retro_cases():
        deltas = {}
        for p, r in zip(c["prior"], c["recalc"]):
            for code in sorted(set(p) | set(r)):
                deltas[code] = deltas.get(code, F(0)) + r.get(code, F(0)) - p.get(code, F(0))
        retro = {"RETRO_" + code: d for code, d in deltas.items() if d != 0}
        extra = [retro[k] for k in sorted(retro)]
        current = dict(c["current"])
        s = scenario(c["periods"], c["w4"], c["state"], current.pop("REGULAR"),
                     overtime=current.pop("OVERTIME", F(0)), pretax_401k=c["k401"], pretax_s125=c["s125"],
                     ytd_fica=c["ytd"], group="retro")
        s["extra_earnings"] = list(current.values()) + extra
        r = gross_to_net(s)
        w = c["w4"]
        out.append({
            "name": c["name"], "periods": c["periods"], "state": c["state"],
            "w4": {"filing": w["filing"], "legacy": w["legacy"], "allowances": w["allowances"], "step2": w["step2"],
                   "step3": fmt(w["step3"]), "step4a": fmt(w["step4a"]), "step4b": fmt(w["step4b"]),
                   "step4c": fmt(w["step4c"])},
            "pretax401k": fmt(c["k401"]), "pretaxS125": fmt(c["s125"]), "ytdWages": fmt(c["ytd"]),
            "affected": [{"prior": {k: fmt(v) for k, v in sorted(p.items())},
                          "recalculated": {k: fmt(v) for k, v in sorted(rc.items())}}
                         for p, rc in zip(c["prior"], c["recalc"])],
            "current": {k: fmt(v) for k, v in sorted(c["current"].items())},
            "expected": {
                "retro": {k: fmt(v) for k, v in sorted(retro.items())},
                "gross": fmt(r["gross"]), "fedWages": fmt(r["fed_wages"]), "fedWH": fmt(r["fed"]),
                "ss": fmt(r["ss"]), "medicare": fmt(r["med"]), "state": fmt(r["state"]), "net": fmt(r["net"]),
            },
        })
    with open(os.path.join(OUT, "payroll-retro-25.json"), "w") as f:
        json.dump(out, f, indent=1, sort_keys=True)
        f.write("\n")


if __name__ == "__main__":
    check_tables()
    check_anchors()
    groups = write_us_200()
    write_retro_25()
    print("payroll-us-200.csv groups:", dict(sorted(groups.items())))
    print("payroll-retro-25.json: 25 cases")
