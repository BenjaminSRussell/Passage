#!/usr/bin/env python3
"""Top-SQL review from a pg_stat_statements export (PSG-50, FR-50.2, AC-50.2).

Input is CSV from ``\\copy (select ...) to 'stats.csv' csv header`` over pg_stat_statements.
Columns used: query, calls, total_exec_time, mean_exec_time, max_exec_time, rows. The PostgreSQL 12
names total_time, mean_time and max_time are accepted too.

Output: the top N statements by total execution time as a Markdown table, the input the review
needs. Exit status is 1 when any statement in the export has a mean execution time above the limit
(default 1000 ms, AC-50.2 "no query exceeds 1 s"). Maxima are reported as warnings, not failures:
pg_stat_statements keeps one maximum per statement since the last reset, so one outlier should be
reviewed, not treated as a sustained breach.

Usage:
    python3 load/tools/top_sql.py stats.csv [--top 50] [--mean-limit-ms 1000]

Stdlib only, Python 3.11+.
"""

from __future__ import annotations

import argparse
import csv
import io
import sys
from dataclasses import dataclass
from pathlib import Path

REQUIRED = ("query", "calls", "total_exec_time", "mean_exec_time", "max_exec_time", "rows")
ALIASES = {"total_time": "total_exec_time", "mean_time": "mean_exec_time", "max_time": "max_exec_time"}
QUERY_PREVIEW = 120


@dataclass(frozen=True)
class Stmt:
    query: str
    calls: int
    total_ms: float
    mean_ms: float
    max_ms: float
    rows: int


class InputError(ValueError):
    pass


def _int(value: str | None) -> int:
    if value is None or value.strip() in ("", "NULL"):
        return 0
    return int(float(value))


def _float(value: str | None) -> float:
    if value is None or value.strip() in ("", "NULL"):
        return 0.0
    return float(value)


def read_statements(text: str) -> list[Stmt]:
    # StringIO, not splitlines(): quoted query fields routinely contain newlines.
    reader = csv.DictReader(io.StringIO(text, newline=""))
    if reader.fieldnames is None:
        raise InputError("empty input: no CSV header")
    names = {ALIASES.get(f, f): f for f in reader.fieldnames}
    missing = [c for c in REQUIRED if c not in names]
    if missing:
        raise InputError(f"missing column(s): {', '.join(missing)}")
    out: list[Stmt] = []
    for row in reader:
        out.append(
            Stmt(
                query=" ".join((row[names["query"]] or "").split()),
                calls=_int(row[names["calls"]]),
                total_ms=_float(row[names["total_exec_time"]]),
                mean_ms=_float(row[names["mean_exec_time"]]),
                max_ms=_float(row[names["max_exec_time"]]),
                rows=_int(row[names["rows"]]),
            )
        )
    return out


def top_by_total(stmts: list[Stmt], top: int) -> list[Stmt]:
    return sorted(stmts, key=lambda s: (-s.total_ms, s.query))[:top]


def over_limit(stmts: list[Stmt], mean_limit_ms: float) -> list[Stmt]:
    """Statements whose mean execution time breaks the limit, slowest first."""
    return sorted((s for s in stmts if s.mean_ms > mean_limit_ms), key=lambda s: (-s.mean_ms, s.query))


def _preview(query: str) -> str:
    q = query if len(query) <= QUERY_PREVIEW else query[: QUERY_PREVIEW - 1] + "…"
    return q.replace("|", "\\|")


def _code(text: str) -> str:
    """A Markdown code span that survives backticks in the text (quoted identifiers, JSON operators)."""
    fence = "`"
    while fence in text:
        fence += "`"
    pad = " " if text.startswith("`") or text.endswith("`") else ""
    return f"{fence}{pad}{text}{pad}{fence}"


def render(stmts: list[Stmt], top: int, mean_limit_ms: float) -> str:
    total_all = sum(s.total_ms for s in stmts) or 1.0
    lines = [
        "| # | calls | total ms | share | mean ms | max ms | rows | query |",
        "|---|---|---|---|---|---|---|---|",
    ]
    for i, s in enumerate(top_by_total(stmts, top), start=1):
        flag = " ⚠" if s.mean_ms > mean_limit_ms else ""
        lines.append(
            f"| {i} | {s.calls} | {s.total_ms:.1f} | {100 * s.total_ms / total_all:.1f}% | "
            f"{s.mean_ms:.2f}{flag} | {s.max_ms:.2f} | {s.rows} | {_code(_preview(s.query))} |"
        )
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("csv", type=Path)
    parser.add_argument("--top", type=int, default=50)
    parser.add_argument("--mean-limit-ms", type=float, default=1000.0)
    args = parser.parse_args(argv)
    if args.top < 1:
        print("error: --top must be at least 1", file=sys.stderr)
        return 2
    try:
        stmts = read_statements(args.csv.read_text(encoding="utf-8"))
    except (OSError, InputError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    print(render(stmts, args.top, args.mean_limit_ms))
    breaches = over_limit(stmts, args.mean_limit_ms)
    for s in breaches:
        print(f"FAIL mean {s.mean_ms:.1f} ms > {args.mean_limit_ms:.0f} ms: {_preview(s.query)}", file=sys.stderr)
    for s in (x for x in stmts if x.max_ms > args.mean_limit_ms and x.mean_ms <= args.mean_limit_ms):
        print(f"WARN max {s.max_ms:.1f} ms (mean ok): {_preview(s.query)}", file=sys.stderr)
    if breaches:
        print(f"{len(breaches)} statement(s) over the mean limit; add indexes or rewrite before exit (FR-50.2)", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
