#!/usr/bin/env python3
"""Generate testdata/atp-100.json: the atp-100 promising fixture (Wave 27 proof AC1, PRD 5.27.9).

The expected promises are computed by an independent reference written here, in Python, from
BR-27.1 / FR-27.4 as the service README states them. It shares no code with the Go engine and
does not use the engine's backward suffix-minimum: feasibility is decided by simulating the
running balance of every bucket after a placement. Before it writes anything it checks itself
against the nine worked cases below, whose expectations are hand-derived literals, and it
re-verifies every generated expectation by brute force.

Run from anywhere (stdlib only, deterministic, seed 27):

    python3 -I services/sales/testdata/gen_atp100.py [out.json]

Reference rules:

  R1  Bucket k holds supply_k and demand_k dated date_k (dates strictly increasing; bucket 0's
      supply is on-hand stock). The running balance of bucket k is the sum over m <= k of
      supply_m - demand_m.
  R2  A bucket covers [date_k, date_k+1). The search starts at the last bucket dated on or before
      the requested date, or at bucket 0 when the request precedes every bucket.
  R3  Placing x on bucket j adds x to the demand of j. A placement is feasible when, after it,
      the running balance of every bucket m >= j is >= 0. (Earlier buckets are untouched; an
      already over-committed earlier bucket does not block a later one.)
  R4  completeOnly: the whole quantity goes on the first bucket j >= start where its placement is
      feasible, dated max(date_j, requested). No such bucket: the whole quantity is backordered,
      no date, no schedule line.
  R5  partial: for j = start, start+1, ... place on j the largest x <= remaining whose placement
      is feasible given the placements already made (skip j when that is 0). The schedule lines
      are dated max(date_j, requested); the promised date is the last line's date; what is left
      is the backorder; complete iff nothing is left.
"""
import datetime as dt
import json
import random
import sys
from decimal import Decimal as D

SEED = 27
COUNT = 100
STEP = D("0.001")  # finest quantity the fixture uses; brute-force probes add one step


def day(s):
    return dt.date.fromisoformat(s)


def fmt(x):
    s = format(x, "f")
    if "." in s:
        s = s.rstrip("0").rstrip(".")
    return "0" if s in ("", "-0") else s


def balances(periods, extra):
    """Running balance of every bucket after adding extra[k] to the demand of bucket k."""
    out, run = [], D(0)
    for k, p in enumerate(periods):
        run += p["supply"] - p["demand"] - extra[k]
        out.append(run)
    return out


def feasible(periods, extra, j, x):
    """R3: placing x on bucket j keeps every running balance from j on at or above zero."""
    trial = list(extra)
    trial[j] += x
    bal = balances(periods, trial)
    return all(bal[m] >= 0 for m in range(j, len(periods)))


def largest(periods, extra, j, cap):
    """R5: the largest x in [0, cap] that can be placed on j. The constraint set of R3 is
    'x <= balance_m for every m >= j', so it is the smallest of those balances (clipped)."""
    bal = balances(periods, extra)
    room = cap
    for m in range(j, len(periods)):
        room = min(room, bal[m])
    return max(room, D(0))


def start_bucket(periods, requested):
    start = 0
    for k, p in enumerate(periods):
        if p["date"] <= requested:
            start = k
    return start


def promise(periods, requested, qty, complete_only):
    start = start_bucket(periods, requested)
    zero = [D(0)] * len(periods)
    if complete_only:
        for j in range(start, len(periods)):
            if feasible(periods, zero, j, qty):
                date = max(periods[j]["date"], requested)
                return {"complete": True, "promisedDate": date.isoformat(), "backorder": "0",
                        "schedule": [{"date": date.isoformat(), "quantity": fmt(qty), "period": j}]}
        return {"complete": False, "promisedDate": None, "backorder": fmt(qty), "schedule": []}
    extra, remaining, sched = list(zero), qty, []
    for j in range(start, len(periods)):
        if remaining <= 0:
            break
        x = largest(periods, extra, j, remaining)
        if x <= 0:
            continue
        assert feasible(periods, extra, j, x)
        extra[j] += x
        remaining -= x
        sched.append({"date": max(periods[j]["date"], requested).isoformat(), "quantity": fmt(x), "period": j})
    return {"complete": remaining == 0, "promisedDate": sched[-1]["date"] if sched else None,
            "backorder": fmt(remaining), "schedule": sched}


def brute_force_check(periods, requested, qty, complete_only, exp):
    """Independent re-check of an expectation by exhaustive probing."""
    start = start_bucket(periods, requested)
    zero = [D(0)] * len(periods)
    placed = sum((D(s["quantity"]) for s in exp["schedule"]), D(0))
    assert placed + D(exp["backorder"]) == qty, "quantity not conserved"
    for s in exp["schedule"]:
        assert s["period"] >= start and day(s["date"]) >= requested, "line before the request"
    if complete_only:
        if exp["complete"]:
            j = exp["schedule"][0]["period"]
            assert feasible(periods, zero, j, qty)
            assert not any(feasible(periods, zero, k, qty) for k in range(start, j)), "an earlier bucket could take it"
        else:
            assert not any(feasible(periods, zero, k, qty) for k in range(start, len(periods)))
        return
    extra = list(zero)
    for s in exp["schedule"]:
        extra[s["period"]] += D(s["quantity"])
    bal = balances(periods, extra)
    first = exp["schedule"][0]["period"] if exp["schedule"] else len(periods)
    assert all(bal[m] >= 0 for m in range(first, len(periods))), "over-promised"
    if D(exp["backorder"]) > 0:
        # Not one more step fits anywhere from the start bucket on.
        assert not any(feasible(periods, extra, k, STEP) for k in range(start, len(periods))), "under-promised"


# --- Worked cases (inputs from BR-27.1 / AC-27.1 / AC-27.8; expectations derived by hand) ---
# BR-27.1 projection: on-hand 100 on 03-01 with SO-1001 60; PO 50 on 03-04 with SO-1002 40;
# PRD 80 on 03-08 with SO-1003 70. Running balances 40, 50, 60.
BR = [("2027-03-01", "100", "60"), ("2027-03-04", "50", "40"), ("2027-03-08", "80", "70")]
WORKED = [
    # 45 complete: 45 on bucket 0 leaves balances -5,5,15 -> bucket 1: 40,5,15 ok -> 03-04.
    ("BR-27.1 / AC-27.1: 45 requested 03-01, complete delivery -> 03-04 bucket 1", "2027-03-01", "45", True, BR,
     (True, "2027-03-04", "0", [("2027-03-04", "45", 1)])),
    # 45 partial: bucket 0 room min(40,50,60)=40; bucket 1 room min(50-40, 60-40)=10 -> 5.
    ("BR-27.1 / AC-27.8: 45 requested 03-01, partial -> 40 on 03-01 and 5 on 03-04", "2027-03-01", "45", False, BR,
     (True, "2027-03-04", "0", [("2027-03-01", "40", 0), ("2027-03-04", "5", 1)])),
    # 03-02 lies in bucket 0; 30 <= 40 fits there, dated on the request.
    ("request between buckets uses the bucket that contains it", "2027-03-02", "30", True, BR,
     (True, "2027-03-02", "0", [("2027-03-02", "30", 0)])),
    # 45 from 03-02: bucket 0 fails (40), bucket 1 (50, 60) fits.
    ("request between buckets, quantity needs the next bucket", "2027-03-02", "45", True, BR,
     (True, "2027-03-04", "0", [("2027-03-04", "45", 1)])),
    # 03-21 is in bucket 2 (balance 60): 60 fits, dated on the request.
    ("request after the last bucket keeps the requested date", "2027-03-21", "60", True, BR,
     (True, "2027-03-21", "0", [("2027-03-21", "60", 2)])),
    # Balances 10, 20; 15 fails on bucket 0, fits on bucket 1 (03-06).
    ("request before the first bucket starts at bucket 0 on its date", "2027-03-01", "15", True,
     [("2027-03-03", "10", "0"), ("2027-03-06", "10", "0")],
     (True, "2027-03-06", "0", [("2027-03-06", "15", 1)])),
    # SO-1002 at 60: balances 40, 30, 40; 35 fails on 0 (30 at bucket 1) and 1, fits on 2.
    ("BR-27.1 variant: SO-1002 at 60 makes bucket 1 negative and cuts bucket 0 to 30", "2027-03-01", "35", True,
     [("2027-03-01", "100", "60"), ("2027-03-04", "50", "60"), ("2027-03-08", "80", "70")],
     (True, "2027-03-08", "0", [("2027-03-08", "35", 2)])),
    # Total ATP is 60 (last balance), so 61 complete fits nowhere.
    ("nothing covers 61 complete: backordered in full", "2027-03-01", "61", True, BR,
     (False, None, "61", [])),
    # 70 partial: 40 on 0, then min(10, 20) = 10 on 1, then 60-50 = 10 on 2; 10 left.
    ("partial 70 over total ATP 60 backorders 10", "2027-03-01", "70", False, BR,
     (False, "2027-03-08", "10", [("2027-03-01", "40", 0), ("2027-03-04", "10", 1), ("2027-03-08", "10", 2)])),
]


def periods_of(rows):
    return [{"date": day(d), "supply": D(s), "demand": D(m)} for d, s, m in rows]


def case(cid, note, requested, qty, complete_only, periods, exp):
    return {"id": cid, "note": note, "requested": requested.isoformat(), "quantity": fmt(qty),
            "completeOnly": complete_only,
            "periods": [{"date": p["date"].isoformat(), "supply": fmt(p["supply"]), "demand": fmt(p["demand"])} for p in periods],
            "expect": exp}


def generate(rng, n_cases):
    """Representative projections: every one has a receipt after on-hand; 1 in 5 is
    over-committed (a confirmed order whose supply slipped); requests fall before, on, between
    and after buckets; a fifth of the quantities are fractional and some sit exactly on an
    available quantity. Quantities are drawn over the whole range of the projection's balances,
    not chosen to produce a given outcome."""
    out = []
    for _ in range(n_cases):
        n = rng.randint(3, 7)
        dates = [day("2027-03-01")]
        for _ in range(n - 1):
            dates.append(dates[-1] + dt.timedelta(days=rng.randint(1, 5)))
        supply = [D(rng.randint(0, 120))] + [D(rng.randint(10, 150)) if rng.random() < 0.5 else D(0) for _ in range(n - 1)]
        if all(s == 0 for s in supply[1:]):
            supply[rng.randint(1, n - 1)] = D(rng.randint(10, 150))
        demand, run = [], D(0)
        for k in range(n):
            want = D(rng.randint(0, 80))
            avail = run + supply[k]
            if want > avail:
                # A projection before promising is feasible: demand that would not fit is
                # redrawn within what is available instead of pinning the balance at zero.
                want = D(rng.randint(0, int(avail)))
            demand.append(want)
            run += supply[k] - want
        tags = []
        if rng.random() < 0.2:
            k = rng.randint(1, n - 1)
            bal = sum(supply[: k + 1]) - sum(demand[: k + 1])
            demand[k] += bal + D(rng.randint(1, 30))
            tags.append(f"over-committed at bucket {k}")
        periods = [{"date": dates[k], "supply": supply[k], "demand": demand[k]} for k in range(n)]
        mode = rng.choices(["before", "on", "between", "after"], weights=[1, 4, 4, 1])[0]
        if mode == "before":
            requested = dates[0] - dt.timedelta(days=rng.randint(1, 3))
        elif mode == "after":
            requested = dates[-1] + dt.timedelta(days=rng.randint(1, 5))
        else:
            k = rng.randint(0, n - 1)
            requested = dates[k]
            if mode == "between" and k < n - 1 and (dates[k + 1] - dates[k]).days > 1:
                requested = dates[k] + dt.timedelta(days=rng.randint(1, (dates[k + 1] - dates[k]).days - 1))
            else:
                mode = "on"
        complete_only = rng.random() < 0.5
        top = max(max(balances(periods, [D(0)] * n)), D(0))
        qty = D(rng.randint(1, int(top * D("1.25")) + 10))
        if rng.random() < 0.2:
            qty = qty - 1 + D(rng.randint(1, 999)) / 1000
            tags.append("fractional")
        if rng.random() < 0.15:
            start = start_bucket(periods, requested)
            j = rng.randint(start, n - 1)
            room = largest(periods, [D(0)] * n, j, D(10) ** 9)
            if room > 0:
                qty = room
                tags.append(f"quantity = available on bucket {j}")
        note = f"generated: request {mode}, {'complete only' if complete_only else 'partial'}" + "".join(f", {t}" for t in tags)
        out.append((note, requested, qty, complete_only, periods))
    return out


def main():
    cases = []
    for i, (note, req, qty, co, rows, (c, pdate, bo, sched)) in enumerate(WORKED, start=1):
        periods = periods_of(rows)
        got = promise(periods, day(req), D(qty), co)
        want = {"complete": c, "promisedDate": pdate, "backorder": bo,
                "schedule": [{"date": d, "quantity": q, "period": k} for d, q, k in sched]}
        if got != want:
            sys.exit(f"reference disagrees with worked case {i} ({note}):\n got  {got}\n want {want}")
        brute_force_check(periods, day(req), D(qty), co, got)
        cases.append(case(f"atp-{i:03d}", note, day(req), D(qty), co, periods, got))
    rng = random.Random(SEED)
    for note, req, qty, co, periods in generate(rng, COUNT - len(WORKED)):
        exp = promise(periods, req, qty, co)
        brute_force_check(periods, req, qty, co, exp)
        cases.append(case(f"atp-{len(cases) + 1:03d}", note, req, qty, co, periods, exp))

    stats = {"complete only": 0, "partial": 0, "promised on the requested date": 0,
             "promised later (waits for incoming supply)": 0, "split over buckets": 0,
             "partly backordered": 0, "backordered in full": 0, "over-committed projection": 0}
    for c in cases:
        e = c["expect"]
        stats["complete only" if c["completeOnly"] else "partial"] += 1
        stats["over-committed projection"] += "over-committed" in c["note"] or "negative" in c["note"]
        if not e["schedule"]:
            stats["backordered in full"] += 1
            continue
        if e["promisedDate"] == c["requested"] or day(e["promisedDate"]) <= day(c["requested"]):
            stats["promised on the requested date"] += 1
        else:
            stats["promised later (waits for incoming supply)"] += 1
        stats["split over buckets"] += len(e["schedule"]) > 1
        stats["partly backordered"] += D(e["backorder"]) > 0

    out = sys.argv[1] if len(sys.argv) > 1 else __file__.rsplit("/", 1)[0] + "/atp-100.json"
    with open(out, "w") as f:
        json.dump(cases, f, indent=1)
        f.write("\n")
    print(f"wrote {len(cases)} cases to {out}", file=sys.stderr)
    for k, v in stats.items():
        print(f"  {k}: {v}", file=sys.stderr)


if __name__ == "__main__":
    main()
