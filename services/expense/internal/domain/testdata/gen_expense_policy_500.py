#!/usr/bin/env python3
"""Independent reference for the expense-policy-500 fixture (wave 43, AC3 / AC-43.6).

Run from the repository root with an isolated interpreter:

    python3 -I services/expense/internal/domain/testdata/gen_expense_policy_500.py [outdir]

It writes expense-policy-500.json (next to this script, or into outdir): 500
reports, the rule set each one is evaluated under, and the exact expected
policy results line by line. It does not import or mirror the Go engine; the
rule semantics come from BR-43.5 and FR-43.12/FR-43.16:

* A rule applies to a line when it is the version of its ID in force on the
  line's transaction date (effective by then, no higher version also in
  force), its categories (compared ignoring case) include the line's, its
  receipt clause (only lines without a receipt) and attendee clause (only
  lines without attendees) hold, and its predicate holds.
* Predicates: NONE; AMOUNT_AT_LEAST (>=); AMOUNT_OVER (>); SUBMITTED_LATE
  (calendar days from transaction to submit date > Days); DUPLICATE (same
  merchant ignoring case and surrounding space, same amount, same date as an
  earlier line of the report; the first occurrence and lines without a
  merchant are never duplicates).
* PER_DAY tests the day's sum of the lines the rule selects; when it holds,
  each of those lines carries the result.
* After a line's rule results: EXP-TR-002 BLOCK when the line's category is
  one of the "travel requisition required" categories (ignoring case) and the
  report has no approved requisition.
* Codes: WARN EXP-POL-002, REQUIRE_RECEIPT EXP-REC-001, BLOCK and
  REQUIRE_JUSTIFICATION EXP-POL-001. Satisfied: WARN always, BLOCK never,
  REQUIRE_RECEIPT with a receipt, REQUIRE_JUSTIFICATION with a non-blank
  justification. A report is submittable when every result is satisfied.

The report mix is drawn once from a fixed seed; it is not tuned to any rate.
"""

import datetime as dt
import json
import os
import random
import sys
from fractions import Fraction as F

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = sys.argv[1] if len(sys.argv) > 1 else HERE

EPOCH = "2000-01-01"


def rule(id, version, outcome, message, predicate="NONE", categories=(), basis="PER_LINE", amount="0",
         days=0, no_receipt=False, no_attendees=False, effective_from=EPOCH):
    return {"id": id, "version": version, "effectiveFrom": effective_from, "categories": list(categories),
            "basis": basis, "predicate": predicate, "amount": amount, "days": days, "noReceipt": no_receipt,
            "noAttendees": no_attendees, "outcome": outcome, "message": message}


# BR-43.5 example set as shipped (lodging and mileage need rate tables and are
# previews outside report evaluation, see NOT-IMPLEMENTED.md).
DEFAULT = [
    rule("EXP-RCPT-025", 1, "REQUIRE_RECEIPT", "Receipt required for lines of 25.00 or more",
         predicate="AMOUNT_AT_LEAST", amount="25", no_receipt=True),
    rule("EXP-ALC-001", 1, "BLOCK", "Alcohol is not reimbursable", categories=["ALCOHOL"]),
    rule("EXP-SUB-060", 1, "BLOCK", "Submitted more than 60 days after the transaction date",
         predicate="SUBMITTED_LATE", days=60),
    rule("EXP-DUP-001", 1, "BLOCK", "Duplicate of another line with the same merchant, amount and date",
         predicate="DUPLICATE"),
    rule("EXP-MEAL-075", 3, "REQUIRE_JUSTIFICATION", "Meals over 75.00 a day without attendees need a justification",
         predicate="AMOUNT_OVER", categories=["MEALS"], basis="PER_DAY", amount="75", no_attendees=True),
]

# A tenant set with versioning: the meal cap moves to 100.00 from 2026-06-01
# (v4 supersedes v3 by transaction date), a WARN on taxis over 150.00, a PER_DAY
# lodging justification at 400.00 a night without a receipt, and a gifts rule
# whose v2 BLOCK only takes effect in 2027 (v1 WARN stays in force for 2026).
VERSIONED = DEFAULT + [
    rule("EXP-MEAL-075", 4, "REQUIRE_JUSTIFICATION", "Meals over 100.00 a day without attendees need a justification",
         predicate="AMOUNT_OVER", categories=["MEALS"], basis="PER_DAY", amount="100", no_attendees=True,
         effective_from="2026-06-01"),
    rule("EXP-TAXI-150", 1, "WARN", "Taxi over 150.00", predicate="AMOUNT_OVER", categories=["TAXI"], amount="150"),
    rule("EXP-LODG-400", 1, "REQUIRE_JUSTIFICATION", "Lodging of 400.00 or more a day without a receipt",
         predicate="AMOUNT_AT_LEAST", categories=["LODGING"], basis="PER_DAY", amount="400", no_receipt=True),
    rule("EXP-GIFT-001", 1, "WARN", "Gifts are reviewed", categories=["GIFTS"]),
    rule("EXP-GIFT-001", 2, "BLOCK", "Gifts are not reimbursable", categories=["GIFTS"], effective_from="2027-01-01"),
]

RULE_SETS = {"DEFAULT": DEFAULT, "VERSIONED": VERSIONED}

CODE = {"WARN": "EXP-POL-002", "REQUIRE_RECEIPT": "EXP-REC-001"}


def day(s):
    return dt.date.fromisoformat(s)


def in_force(r, rules, line):
    t = day(line["txnDate"])
    if t < day(r["effectiveFrom"]):
        return False
    return not any(o["id"] == r["id"] and o["version"] > r["version"] and t >= day(o["effectiveFrom"])
                   for o in rules)


def picks(r, line):
    if r["categories"] and line["category"].lower() not in [c.lower() for c in r["categories"]]:
        return False
    if r["noReceipt"] and line["hasReceipt"]:
        return False
    if r["noAttendees"] and line["attendees"] != 0:
        return False
    return True


def satisfied(outcome, line):
    if outcome == "BLOCK":
        return False
    if outcome == "REQUIRE_RECEIPT":
        return line["hasReceipt"]
    if outcome == "REQUIRE_JUSTIFICATION":
        return line["justification"].strip() != ""
    return True


def evaluate(rules, lines, ctx):
    day_sum = {}
    for r in rules:
        if r["basis"] != "PER_DAY":
            continue
        for l in lines:
            if in_force(r, rules, l) and picks(r, l):
                k = (r["id"], l["txnDate"])
                day_sum[k] = day_sum.get(k, F(0)) + F(l["amount"])
    seen, dup = set(), []
    for l in lines:
        m = l["merchant"].strip().lower()
        if not m:
            dup.append(False)
            continue
        k = (m, F(l["amount"]), l["txnDate"])
        dup.append(k in seen)
        seen.add(k)
    submit = day(ctx["submitDate"])
    out = []
    for i, l in enumerate(lines):
        for r in rules:
            if not in_force(r, rules, l) or not picks(r, l):
                continue
            amount = day_sum[(r["id"], l["txnDate"])] if r["basis"] == "PER_DAY" else F(l["amount"])
            p = r["predicate"]
            holds = {
                "NONE": True,
                "AMOUNT_AT_LEAST": amount >= F(r["amount"]),
                "AMOUNT_OVER": amount > F(r["amount"]),
                "SUBMITTED_LATE": (submit - day(l["txnDate"])).days > r["days"],
                "DUPLICATE": dup[i],
            }[p]
            if holds:
                out.append({"lineId": l["id"], "ruleId": r["id"], "ruleVersion": r["version"],
                            "outcome": r["outcome"], "code": CODE.get(r["outcome"], "EXP-POL-001"),
                            "message": r["message"], "satisfied": satisfied(r["outcome"], l)})
        if not ctx["hasRequisition"] and l["category"].lower() in [c.lower() for c in ctx["requisitionCategories"]]:
            out.append({"lineId": l["id"], "ruleId": "EXP-TR-002", "ruleVersion": 1, "outcome": "BLOCK",
                        "code": "EXP-TR-002", "message": "Requisition required for category " + l["category"],
                        "satisfied": False})
    return out


def check_anchors():
    """BR-43.5 / AC-43.6 cases worked by hand."""
    ctx = {"submitDate": "2026-03-15", "requisitionCategories": [], "hasRequisition": False}

    def line(**kw):
        base = {"id": "L1", "category": "TRAVEL", "txnDate": "2026-03-10", "amount": "5", "merchant": "",
                "attendees": 0, "hasReceipt": True, "justification": "", "paidBy": "EMPLOYEE"}
        base.update(kw)
        return base

    def ids(res):
        return [(r["lineId"], r["ruleId"], r["satisfied"]) for r in res]
    assert ids(evaluate(DEFAULT, [line(amount="25.00", hasReceipt=False)], ctx)) == [("L1", "EXP-RCPT-025", False)]
    assert ids(evaluate(DEFAULT, [line(amount="24.99", hasReceipt=False)], ctx)) == []
    assert ids(evaluate(DEFAULT, [line(category="alcohol")], ctx)) == [("L1", "EXP-ALC-001", False)]
    assert ids(evaluate(DEFAULT, [line(txnDate="2026-01-14")], ctx)) == []             # 60 days
    assert ids(evaluate(DEFAULT, [line(txnDate="2026-01-13")], ctx)) == [("L1", "EXP-SUB-060", False)]
    two = [line(id="L1", category="MEALS", amount="40"), line(id="L2", category="MEALS", amount="35.01")]
    assert ids(evaluate(DEFAULT, two, ctx)) == [("L1", "EXP-MEAL-075", False), ("L2", "EXP-MEAL-075", False)]
    two[1]["amount"] = "35"
    assert ids(evaluate(DEFAULT, two, ctx)) == []                                       # 75.00 is not over
    dups = [line(id="L1", merchant="Cafe Uno", amount="12.5"), line(id="L2", merchant=" cafe uno ", amount="12.50")]
    assert ids(evaluate(DEFAULT, dups, ctx)) == [("L2", "EXP-DUP-001", False)]
    june = [line(category="MEALS", amount="90", txnDate="2026-06-01")]
    assert ids(evaluate(VERSIONED, june, {**ctx, "submitDate": "2026-06-05"})) == []   # v4 cap 100
    may = [line(category="MEALS", amount="90", txnDate="2026-05-31")]
    assert ids(evaluate(VERSIONED, may, {**ctx, "submitDate": "2026-06-05"})) == [("L1", "EXP-MEAL-075", False)]
    req = {**ctx, "requisitionCategories": ["AIRFARE"]}
    assert ids(evaluate(DEFAULT, [line(category="Airfare")], req)) == [("L1", "EXP-TR-002", False)]


CATEGORIES = ["MEALS"] * 9 + ["meals", "Meals", "ALCOHOL", "alcohol", "LODGING", "LODGING", "AIRFARE", "airfare",
                                "TAXI", "TAXI", "GIFTS", "OFFICE", "OFFICE", "MILEAGE", "CONFERENCE"]
MERCHANTS = ["Cafe Uno", "CAFE UNO", " cafe uno ", "Delta", "Hilton Midtown", "Yellow Cab", "Staples", "Bar 21",
             "Uber", "Marriott", "Le Bistro", "Office Depot", ""]


def amount_for(rng, category):
    c = category.upper()
    if rng.random() < 0.15:
        return rng.choice(["24.99", "25", "25.00", "25.01", "75", "75.00", "75.01", "100", "100.01", "150",
                           "150.01", "400", "399.99"])
    lo, hi = {"MEALS": (6, 95), "ALCOHOL": (8, 60), "LODGING": (110, 520), "AIRFARE": (120, 900),
              "TAXI": (9, 220), "GIFTS": (15, 80), "OFFICE": (3, 140), "MILEAGE": (12, 180),
              "CONFERENCE": (50, 800)}[c]
    cents = rng.randrange(lo * 100, hi * 100)
    s = "%d.%02d" % (cents // 100, cents % 100)
    # Same value, other spelling: duplicate detection compares amounts, not text.
    if s.endswith("0") and rng.random() < 0.3:
        s = s.rstrip("0").rstrip(".")
    return s


def make_reports():
    rng = random.Random(4306)
    reports = []
    start = dt.date(2026, 1, 20)
    for n in range(1, 501):
        rule_set = "VERSIONED" if rng.random() < 0.3 else "DEFAULT"
        submit = start + dt.timedelta(days=rng.randrange(0, 330))
        if rng.random() < 0.25:
            cats = rng.choice([["AIRFARE", "LODGING"], ["airfare"], ["LODGING"], ["Taxi", "AIRFARE"]])
            ctx = {"submitDate": submit.isoformat(), "requisitionCategories": cats,
                   "hasRequisition": rng.random() < 0.5}
        else:
            ctx = {"submitDate": submit.isoformat(), "requisitionCategories": [], "hasRequisition": False}
        # Most reports are filed within a month; about a third sit on or past the 60-day line.
        if rng.random() < 0.65:
            trip_day = submit - dt.timedelta(days=rng.choice([1, 2, 3, 5, 7, 10, 14, 21, 30, 45]))
        else:
            trip_day = submit - dt.timedelta(days=rng.choice([58, 59, 60, 61, 62, 75, 90]))
        lines = []
        for i in range(rng.randint(1, 8)):
            category = rng.choice(CATEGORIES)
            # Lines cluster on a few trip days so PER_DAY sums and duplicates occur naturally.
            txn = trip_day + dt.timedelta(days=rng.choice([0, 0, 0, 1, 1, 2, -1]))
            attendees = rng.choice([0, 0, 0, 0, 1, 3]) if category.upper() == "MEALS" else 0
            justification = rng.choice([""] * 6 + ["Client dinner", "Late flight", "   "])
            lines.append({"id": "R%03d-L%d" % (n, i + 1), "category": category, "txnDate": txn.isoformat(),
                          "amount": amount_for(rng, category), "merchant": rng.choice(MERCHANTS),
                          "attendees": attendees, "hasReceipt": rng.random() < 0.7,
                          "justification": justification,
                          "paidBy": "COMPANY_CARD" if rng.random() < 0.3 else "EMPLOYEE"})
        # Some reports repeat a line, exactly or with one fact changed.
        if lines and rng.random() < 0.15:
            src = dict(rng.choice(lines))
            twist = rng.choice(["exact", "exact", "case", "date", "cent"])
            if twist == "case":
                src["merchant"] = "  " + src["merchant"].upper() + " "
            elif twist == "date":
                src["txnDate"] = (day(src["txnDate"]) + dt.timedelta(days=1)).isoformat()
            elif twist == "cent":
                c = (F(src["amount"]) + F(1, 100)) * 100
                assert c.denominator == 1
                src["amount"] = "%d.%02d" % (c.numerator // 100, c.numerator % 100)
            src["id"] = "R%03d-L%d" % (n, len(lines) + 1)
            lines.append(src)
        reports.append({"id": "R%03d" % n, "ruleSet": rule_set, "context": ctx, "lines": lines})
    return reports


def main():
    check_anchors()
    reports = make_reports()
    flagged = blocked = results = 0
    by_rule = {}
    for r in reports:
        res = evaluate(RULE_SETS[r["ruleSet"]], r["lines"], r["context"])
        r["expected"] = {"results": res, "submittable": all(x["satisfied"] for x in res)}
        results += len(res)
        flagged += bool(res)
        blocked += not r["expected"]["submittable"]
        for x in res:
            by_rule[x["ruleId"]] = by_rule.get(x["ruleId"], 0) + 1
    doc = {"ruleSets": RULE_SETS, "reports": reports}
    # One report per line keeps the file small and its diffs readable.
    with open(os.path.join(OUT, "expense-policy-500.json"), "w") as f:
        f.write('{"ruleSets": %s,\n"reports": [\n' % json.dumps(doc["ruleSets"], sort_keys=True))
        f.write(",\n".join(json.dumps(r, sort_keys=True, separators=(",", ":")) for r in reports))
        f.write("\n]}\n")
    print("reports 500, lines %d, results %d, reports with results %d, not submittable %d" %
          (sum(len(r["lines"]) for r in reports), results, flagged, blocked))
    print("results by rule:", dict(sorted(by_rule.items())))


if __name__ == "__main__":
    main()
