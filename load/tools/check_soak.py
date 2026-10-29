#!/usr/bin/env python3
"""Check memory growth over a soak run (PSG-50, PRD 9.50.9 "Soak": 24 h at 50% load, growth <= 10%).

Usage:
    python3 load/tools/check_soak.py results/soak-memory-*.csv [--max-growth 0.10] [--min-span-hours 24]

One CSV per service, with a header row and at least the columns ``timestamp`` (epoch seconds or
ISO 8601) and ``bytes`` (working-set bytes of that service, e.g. a range query of
``container_memory_working_set_bytes`` summed per service). Rows are sorted by timestamp before
they are judged, so the export order does not matter.

A series passes when all of these hold:
  * it has at least 10 samples;
  * it spans at least ``--min-span-hours`` (default from the soak profile in load/slo/slo.json,
    24 h), so a short run cannot stand in for the soak. Smoke runs pass ``--min-span-hours 0``;
  * the median of its last 10% of samples exceeds the median of its first 10% by at most
    ``--max-growth`` (default from the soak profile, 0.10). Medians keep one GC spike at either
    end from deciding the result.

Exit status: 0 when every series passes, 1 when any fails, 2 on a malformed or unreadable file.
Stdlib only, Python 3.11+.
"""

from __future__ import annotations

import argparse
import csv
import json
import statistics
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

MIN_SAMPLES = 10
DEFAULT_SLO = Path(__file__).resolve().parent.parent / "slo" / "slo.json"
FALLBACK_MAX_GROWTH = 0.10
FALLBACK_MIN_SPAN_HOURS = 24.0


@dataclass(frozen=True)
class Verdict:
    name: str
    samples: int
    span_hours: float | None
    baseline: float | None
    end: float | None
    growth: float | None
    passed: bool
    reason: str


def parse_timestamp(raw: str) -> float:
    """Epoch seconds, or ISO 8601 (a naive value is taken as UTC). Raises ValueError."""
    raw = raw.strip()
    try:
        return float(raw)
    except ValueError:
        pass
    parsed = datetime.fromisoformat(raw)
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed.timestamp()


def read_series(path: Path) -> list[tuple[float, float]]:
    """Return (timestamp, bytes) pairs sorted by timestamp. Raises ValueError on a malformed file."""
    with path.open(newline="", encoding="utf-8") as fh:
        reader = csv.DictReader(fh)
        for column in ("timestamp", "bytes"):
            if not reader.fieldnames or column not in reader.fieldnames:
                raise ValueError(f"{path}: missing '{column}' column")
        samples: list[tuple[float, float]] = []
        for lineno, row in enumerate(reader, start=2):
            raw_ts = row.get("timestamp") or ""
            raw = (row.get("bytes") or "").strip()
            try:
                ts = parse_timestamp(raw_ts)
            except ValueError as exc:
                raise ValueError(f"{path}:{lineno}: timestamp is not epoch seconds or ISO 8601: {raw_ts!r}") from exc
            try:
                value = float(raw)
            except ValueError as exc:
                raise ValueError(f"{path}:{lineno}: bytes is not a number: {raw!r}") from exc
            if value < 0:
                raise ValueError(f"{path}:{lineno}: negative bytes")
            samples.append((ts, value))
    samples.sort(key=lambda s: s[0])
    return samples


def judge(name: str, samples: list[tuple[float, float]], max_growth: float, min_span_hours: float) -> Verdict:
    """Compare the median of the first 10% of samples (by time) with the median of the last 10%."""
    n = len(samples)
    if n < MIN_SAMPLES:
        return Verdict(name, n, None, None, None, None, False, f"only {n} samples, need {MIN_SAMPLES}")
    ordered = sorted(samples, key=lambda s: s[0])
    span_hours = (ordered[-1][0] - ordered[0][0]) / 3600
    if span_hours < min_span_hours:
        return Verdict(
            name, n, span_hours, None, None, None, False,
            f"series spans {span_hours:.1f} h, need {min_span_hours:g} h",
        )
    values = [v for _, v in ordered]
    window = max(1, n // 10)
    baseline = statistics.median(values[:window])
    end = statistics.median(values[-window:])
    if baseline <= 0:
        return Verdict(name, n, span_hours, baseline, end, None, False, "baseline is zero")
    growth = (end - baseline) / baseline
    passed = growth <= max_growth
    reason = "within budget" if passed else f"growth {growth:.1%} exceeds {max_growth:.0%}"
    return Verdict(name, n, span_hours, baseline, end, growth, passed, reason)


def render(verdicts: list[Verdict]) -> str:
    width = max((len(v.name) for v in verdicts), default=10)
    lines = [f"{'series'.ljust(width)}  {'samples':>7}  {'span h':>6}  {'growth':>8}  result  note"]
    for v in verdicts:
        growth = "n/a" if v.growth is None else f"{v.growth:+.1%}"
        span = "n/a" if v.span_hours is None else f"{v.span_hours:.1f}"
        status = "PASS" if v.passed else "FAIL"
        lines.append(f"{v.name.ljust(width)}  {v.samples:>7}  {span:>6}  {growth:>8}  {status:<6}  {v.reason}")
    failed = sum(1 for v in verdicts if not v.passed)
    lines.append(f"{len(verdicts) - failed}/{len(verdicts)} memory series within budget")
    return "\n".join(lines)


def soak_defaults(slo_path: Path) -> tuple[float, float]:
    """(max growth, min span hours) from the soak profile, or the PRD values if it is absent."""
    try:
        with slo_path.open(encoding="utf-8") as fh:
            soak = json.load(fh).get("soak", {})
    except (OSError, ValueError):
        soak = {}
    return (
        float(soak.get("maxMemoryGrowth", FALLBACK_MAX_GROWTH)),
        float(soak.get("minSpanHours", FALLBACK_MIN_SPAN_HOURS)),
    )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("files", nargs="+", type=Path, help="one CSV per service")
    parser.add_argument("--slo", type=Path, default=DEFAULT_SLO, help="SLO table with the soak profile")
    parser.add_argument("--max-growth", type=float, help="allowed growth ratio (default: soak profile)")
    parser.add_argument("--min-span-hours", type=float, help="minimum series span (default: soak profile)")
    args = parser.parse_args(argv)
    max_growth, min_span = soak_defaults(args.slo)
    if args.max_growth is not None:
        max_growth = args.max_growth
    if args.min_span_hours is not None:
        min_span = args.min_span_hours
    if not 0 <= max_growth < 10:
        parser.error("--max-growth must be a ratio in [0, 10)")
    if min_span < 0:
        parser.error("--min-span-hours must not be negative")

    verdicts: list[Verdict] = []
    for path in args.files:
        try:
            series = read_series(path)
        except (OSError, ValueError) as exc:
            print(f"error: {exc}", file=sys.stderr)
            return 2
        verdicts.append(judge(path.stem, series, max_growth, min_span))
    print(render(verdicts))
    return 0 if verdicts and all(v.passed for v in verdicts) else 1


if __name__ == "__main__":
    sys.exit(main())
