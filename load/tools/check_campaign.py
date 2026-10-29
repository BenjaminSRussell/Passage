#!/usr/bin/env python3
"""Check a k6 campaign summary against the SLO table in load/slo/slo.json (PSG-50, AC1).

Usage:
    python3 load/tools/check_campaign.py results/summary.json [--slo load/slo/slo.json]

Exit status is 0 only when every SLO has data and meets its limit. A missing metric
counts as a failure: an operation that was not measured cannot pass a campaign.
Latency and error limits are strict upper bounds (k6 "<"); throughput targets are
lower bounds. --scale must match the run's LOAD_SCALE and lowers only throughput
targets; a published campaign (AC1) runs at scale 1.
Stdlib only, Python 3.11+.
"""

from __future__ import annotations

import argparse
import json
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

DEFAULT_SLO = Path(__file__).resolve().parent.parent / "slo" / "slo.json"


@dataclass(frozen=True)
class Check:
    label: str
    metric: str
    stat: str
    limit: float
    # "<" for latency and error budgets, ">=" for throughput and counts.
    comparison: str = "<"
    # Multiplies the stat before comparing, e.g. 3600 turns a per-second rate into per hour.
    factor: float = 1.0

    def passes(self, value: float) -> bool:
        if self.comparison == "<":
            return value < self.limit
        if self.comparison == ">=":
            return value >= self.limit
        raise ValueError(f"unknown comparison {self.comparison!r}")


@dataclass(frozen=True)
class Row:
    label: str
    value: float | None
    limit: float
    passed: bool
    comparison: str = "<"


def build_checks(slo: dict[str, Any], scale: float = 1.0) -> list[Check]:
    """Translate the SLO table into concrete metric checks."""
    if scale <= 0:
        raise ValueError("scale must be positive")
    checks: list[Check] = []
    for s in slo["slos"]:
        sid = s["id"]
        if "op" in s:
            duration = f"http_req_duration{{op:{s['op']}}}"
            failed = f"http_req_failed{{op:{s['op']}}}"
            if "p95ms" in s:
                checks.append(Check(f"{sid} p95 (ms)", duration, "p(95)", float(s["p95ms"])))
            if "p99ms" in s:
                checks.append(Check(f"{sid} p99 (ms)", duration, "p(99)", float(s["p99ms"])))
            if "errorBudget" in s:
                checks.append(Check(f"{sid} error rate", failed, "rate", float(s["errorBudget"])))
        elif "metric" in s:
            if "p95Seconds" in s:
                checks.append(Check(f"{sid} p95 (s)", s["metric"], "p(95)", float(s["p95Seconds"])))
            if "maxSeconds" in s:
                checks.append(Check(f"{sid} max (s)", s["metric"], "max", float(s["maxSeconds"])))
            if "minPerHour" in s:
                # k6 reports a counter's rate per second over the whole run.
                limit = float(s["minPerHour"]) * scale
                checks.append(Check(f"{sid} per hour", s["metric"], "rate", limit, ">=", 3600.0))
            if "minCount" in s:
                checks.append(Check(f"{sid} count", s["metric"], "count", float(s["minCount"]), ">="))
    return checks


def read_stat(metrics: dict[str, Any], metric: str, stat: str) -> float | None:
    """Read one statistic from a k6 summary export.

    k6 writes flat stats per metric. handleSummary output nests them under "values".
    Rate metrics expose the ratio as "rate" in the export and as "value" in handleSummary.
    """
    entry = metrics.get(metric)
    if not isinstance(entry, dict):
        return None
    values = entry.get("values", entry)
    if not isinstance(values, dict):
        return None
    candidates = [stat, "value"] if stat == "rate" else [stat]
    for key in candidates:
        raw = values.get(key)
        if isinstance(raw, (int, float)) and not isinstance(raw, bool):
            return float(raw)
    return None


def evaluate(summary: dict[str, Any], slo: dict[str, Any], scale: float = 1.0) -> list[Row]:
    metrics = summary.get("metrics", {})
    if not isinstance(metrics, dict):
        raise ValueError("summary has no metrics object")
    rows: list[Row] = []
    for check in build_checks(slo, scale):
        value = read_stat(metrics, check.metric, check.stat)
        if value is not None:
            value *= check.factor
        passed = value is not None and check.passes(value)
        rows.append(Row(check.label, value, check.limit, passed, check.comparison))
    return rows


def render(rows: list[Row]) -> str:
    width = max(len(r.label) for r in rows) if rows else 10
    lines = [f"{'SLO'.ljust(width)}  {'measured':>12}  {'limit':>10}  result"]
    for r in rows:
        measured = "no data" if r.value is None else f"{r.value:.4f}"
        status = "PASS" if r.passed else "FAIL"
        limit = f"{r.comparison} {format(r.limit, 'g')}"
        lines.append(f"{r.label.ljust(width)}  {measured:>12}  {limit:>10}  {status}")
    failed = sum(1 for r in rows if not r.passed)
    lines.append(f"{len(rows) - failed}/{len(rows)} SLO checks passed")
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("summary", type=Path, help="k6 --summary-export JSON file")
    parser.add_argument("--slo", type=Path, default=DEFAULT_SLO, help="SLO table JSON")
    parser.add_argument("--scale", type=float, default=1.0, help="LOAD_SCALE of the run (default 1)")
    args = parser.parse_args(argv)
    if args.scale <= 0:
        parser.error("--scale must be positive")

    with args.summary.open(encoding="utf-8") as fh:
        summary = json.load(fh)
    with args.slo.open(encoding="utf-8") as fh:
        slo = json.load(fh)

    rows = evaluate(summary, slo, args.scale)
    print(render(rows))
    return 0 if rows and all(r.passed for r in rows) else 1


if __name__ == "__main__":
    sys.exit(main())
