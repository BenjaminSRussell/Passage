#!/usr/bin/env python3
"""Game-day scorecard: measured RTO and RPO against the targets, plus zero committed loss (PSG-50, AC-50.3).

    python3 deploy/multi-region/gameday/score.py --timeline timeline.json --verify-report report.json \
        [--region-values deploy/multi-region/values/eu-central.yaml] [--report score.json]

This is step 7 of docs/ops/runbooks/gameday.md ("Measure RTO and RPO") turned into a gate, with the
measurements defined the way docs/ops/runbooks/failover.md records them:

  * RPO is the target region's replication lag at fence time (failover step 1, ``lagAtFenceSeconds``).
  * RTO is the time from the fence (step 1, ``fencedAt``) to DNS verified on the target (step 5,
    ``dnsVerifiedAt``).
  * Zero committed-transaction loss is the ``baseline.py verify --report`` result of step 5 of the game
    day: chain extended, rows and offsets not reduced, sequences gap-free. A missing, empty or failed
    report fails the game day.

The timeline is JSON::

    {"fencedAt": "2026-11-03T14:05:00Z", "dnsVerifiedAt": "2026-11-03T14:41:30Z", "lagAtFenceSeconds": 42.0}

Timestamps are ISO 8601 with an offset (a time without one is rejected, not guessed). Targets are
``failover.rtoTargetMinutes`` and ``failover.rpoTargetSeconds`` of the chart defaults, merged with the
region file when ``--region-values`` is given (PRD: RTO <= 1 h, RPO <= 5 min; limits are inclusive).

Exit status: 0 every check passes, 1 a check fails, 2 malformed input. Stdlib only, Python 3.11+.
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent / "tools"))
import check_region_pair as values_reader  # noqa: E402  (stdlib-only YAML subset reader of this chart)

CHART_VALUES = HERE.parent / "chart" / "values.yaml"


class InputError(ValueError):
    """Timeline, report or values are malformed. Distinct from a failed check."""


@dataclass(frozen=True)
class Check:
    name: str
    passed: bool
    detail: str


def parse_time(raw: Any, field: str) -> datetime:
    if not isinstance(raw, str):
        raise InputError(f"timeline: '{field}' must be an ISO 8601 string")
    try:
        parsed = datetime.fromisoformat(raw)
    except ValueError as exc:
        raise InputError(f"timeline: '{field}' is not ISO 8601: {raw!r}") from exc
    if parsed.tzinfo is None:
        raise InputError(f"timeline: '{field}' has no UTC offset: {raw!r}")
    return parsed


def targets(chart: dict[str, Any], region: dict[str, Any] | None = None) -> tuple[float, float]:
    """(RTO target in seconds, RPO target in seconds) from the merged values."""
    values = values_reader.merge(chart, region) if region else chart
    rto_min = values_reader._get(values, "failover.rtoTargetMinutes")
    rpo_s = values_reader._get(values, "failover.rpoTargetSeconds")
    for name, value in (("failover.rtoTargetMinutes", rto_min), ("failover.rpoTargetSeconds", rpo_s)):
        if not isinstance(value, int) or isinstance(value, bool) or value < 0:
            raise InputError(f"values: {name} must be a non-negative integer, got {value!r}")
    return float(rto_min) * 60.0, float(rpo_s)


def score(timeline: dict[str, Any], verify_report: dict[str, Any] | None, rto_target_s: float,
          rpo_target_s: float) -> list[Check]:
    if not isinstance(timeline, dict):
        raise InputError("timeline: top level must be an object")
    fenced = parse_time(timeline.get("fencedAt"), "fencedAt")
    verified = parse_time(timeline.get("dnsVerifiedAt"), "dnsVerifiedAt")
    rto = (verified - fenced).total_seconds()
    if rto < 0:
        raise InputError("timeline: dnsVerifiedAt is before fencedAt")
    lag = timeline.get("lagAtFenceSeconds")
    if not isinstance(lag, (int, float)) or isinstance(lag, bool) or not math.isfinite(lag) or lag < 0:
        raise InputError("timeline: 'lagAtFenceSeconds' must be a non-negative number")

    checks = [
        Check("RTO", rto <= rto_target_s, f"fence to DNS verified {rto:g} s, target <= {rto_target_s:g} s"),
        Check("RPO", lag <= rpo_target_s, f"lag at fence {float(lag):g} s, target <= {rpo_target_s:g} s"),
    ]
    if verify_report is None:
        checks.append(Check("zero committed loss", False, "no baseline.py verify report"))
    else:
        inner = verify_report.get("checks") if isinstance(verify_report, dict) else None
        if not isinstance(inner, list) or not inner:
            checks.append(Check("zero committed loss", False, "verify report has no checks"))
        else:
            failed = [c.get("name", "?") for c in inner if not (isinstance(c, dict) and c.get("passed") is True)]
            ok = verify_report.get("passed") is True and not failed
            detail = f"{len(inner)} verify checks passed" if ok else (
                "failed: " + ", ".join(failed) if failed else "report is not marked passed")
            checks.append(Check("zero committed loss", ok, detail))
    return checks


def render(checks: list[Check]) -> str:
    lines = [f"{'PASS' if c.passed else 'FAIL'}  {c.name}: {c.detail}" for c in checks]
    lines.append("GAME DAY " + ("PASSED" if checks and all(c.passed for c in checks) else "FAILED"))
    return "\n".join(lines)


def _load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise InputError(f"{path}: not JSON") from exc


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--timeline", type=Path, required=True)
    parser.add_argument("--verify-report", type=Path, required=True, help="baseline.py verify --report output")
    parser.add_argument("--chart-values", type=Path, default=CHART_VALUES)
    parser.add_argument("--region-values", type=Path, help="region file merged over the chart defaults")
    parser.add_argument("--report", type=Path, help="write the scorecard as JSON")
    args = parser.parse_args(argv)
    try:
        chart = values_reader.parse(args.chart_values.read_text(encoding="utf-8"))
        region = values_reader.parse(args.region_values.read_text(encoding="utf-8")) if args.region_values else None
        rto_target, rpo_target = targets(chart, region)
        checks = score(_load_json(args.timeline), _load_json(args.verify_report), rto_target, rpo_target)
    except (OSError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    print(render(checks))
    passed = bool(checks) and all(c.passed for c in checks)
    if args.report:
        payload = {"passed": passed, "checks": [c.__dict__ for c in checks]}
        args.report.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
