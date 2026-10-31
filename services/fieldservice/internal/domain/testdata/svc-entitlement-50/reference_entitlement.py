"""Independent reference for svc-entitlement-50 (AC-45.1). Run: python3 -I reference_entitlement.py

It re-derives each case from BR-45.1 and BR-45.2 without the Go engine:
  * candidates: ACTIVE agreements of the case's customer and legal entity whose
    validity (inclusive dates) covers the UTC calendar day of openedAt;
  * a line matches at 3 (same object), 2 (same, non-empty category) or 1 (a line
    naming neither); an agreement matches at its best line;
  * rank: specificity, then priority rank, then latest validity start, then id;
  * no candidate: the fallback profile, else SVC-ENT-002; a profile without a
    target for the priority: SVC-SLA-003;
  * clocks count business time by walking the timeline one minute (one second
    when openedAt has seconds) at a time in UTC and asking whether that instant
    is inside the profile's local opening hours on a Monday-to-Friday that is
    not a holiday (every instant for a 24x7 profile). The due is the instant at
    which the count reaches the target; the warning is at ceil(75 % of target).

It checks every answer against the hand-derived 'hand' block of cases.json and
writes expected.json next to it. Exit status 1 on any disagreement.
"""

from __future__ import annotations

import json
import sys
from datetime import date, datetime, timedelta, timezone
from pathlib import Path
from zoneinfo import ZoneInfo

HERE = Path(__file__).resolve().parent
DEFAULT_FROM = "2026-01-01"


def utc_day(opened: datetime) -> date:
    return opened.astimezone(timezone.utc).date()


def line_spec(line: dict, obj: str, cat: str) -> int:
    if line.get("object"):
        return 3 if line["object"] == obj else 0
    if line.get("category"):
        return 2 if cat and line["category"] == cat else 0
    return 1


def resolve(case: dict) -> tuple[dict | None, int]:
    customer = case.get("customer", "C1")
    le = case.get("legalEntity", "LE1")
    day = utc_day(datetime.fromisoformat(case["openedAt"]))
    best = None
    for a in case["agreements"]:
        if a.get("customer", "C1") != customer or a.get("legalEntity", "LE1") != le:
            continue
        if a.get("status", "ACTIVE") != "ACTIVE":
            continue
        start = date.fromisoformat(a.get("validFrom", DEFAULT_FROM))
        end = date.fromisoformat(a["validTo"]) if a.get("validTo") else None
        if day < start or (end is not None and day > end):
            continue
        spec = max(line_spec(l, case.get("object", ""), case.get("category", "")) for l in a["lines"])
        if spec == 0:
            continue
        # Higher tuple wins; the id is negated by sorting below.
        key = (spec, a["priorityRank"], start)
        if best is None or key > best[0] or (key == best[0] and a["id"] < best[1]["id"]):
            best = (key, a)
    if best is None:
        return None, 0
    return best[1], best[0][0]


def is_business(instant: datetime, prof: dict) -> bool:
    if prof.get("allDay"):
        return True
    local = instant.astimezone(ZoneInfo(prof["timeZone"]))
    if local.weekday() >= 5 or local.date().isoformat() in prof["holidays"]:
        return False
    minute_of_day = local.hour * 60 + local.minute
    return prof["open"] <= minute_of_day < prof["close"]


def due_after(opened: datetime, minutes: int, prof: dict) -> datetime:
    step = timedelta(seconds=1) if opened.second else timedelta(minutes=1)
    need = timedelta(minutes=minutes)
    t = opened.astimezone(timezone.utc)
    counted = timedelta(0)
    while counted < need:
        if is_business(t, prof):
            counted += step
        t += step
    return t


def ceil_pct(minutes: int, pct: int) -> int:
    return -(-minutes * pct // 100)


def fmt(t: datetime) -> str:
    return t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def derive(case: dict, profiles: dict) -> dict:
    agreement, spec = resolve(case)
    if agreement is not None:
        profile_code = agreement["profile"]
    elif case.get("fallback"):
        profile_code = case["fallback"]
    else:
        return {"error": "SVC-ENT-002"}
    resolved = {"agreement": agreement["id"] if agreement else None, "specificity": spec, "profile": profile_code}
    prof = profiles[profile_code]
    target = next((t for t in prof["targets"] if t[0] == case["priority"]), None)
    if target is None:
        return {"error": "SVC-SLA-003", **resolved}
    opened = datetime.fromisoformat(case["openedAt"])
    _, response, resolution = target
    return {
        **resolved,
        "responseDue": fmt(due_after(opened, response, prof)),
        "responseWarnAt": fmt(due_after(opened, ceil_pct(response, 75), prof)),
        "resolutionDue": fmt(due_after(opened, resolution, prof)),
        "resolutionWarnAt": fmt(due_after(opened, ceil_pct(resolution, 75), prof)),
    }


def main() -> int:
    data = json.loads((HERE / "cases.json").read_text())
    out, bad = {}, 0
    for case in data["cases"]:
        got = derive(case, data["profiles"])
        for k, v in case["hand"].items():
            if got.get(k) != v:
                print(f"{case['id']}: {k} reference={got.get(k)!r} hand={v!r}", file=sys.stderr)
                bad += 1
        out[case["id"]] = got
    if len(data["cases"]) != 50 or len(out) != 50:
        print(f"want 50 cases, have {len(out)}", file=sys.stderr)
        bad += 1
    (HERE / "expected.json").write_text(json.dumps(out, indent=1, sort_keys=True) + "\n")
    print(f"{len(out)} cases, {bad} disagreements with the hand-derived answers")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
