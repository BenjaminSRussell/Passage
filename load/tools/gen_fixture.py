#!/usr/bin/env python3
"""Synthetic journal-line generator for fx-load-10k growth (PSG-50, PRD 9.50.9, BR-50.5).

Writes a deterministic, balanced journal-line CSV that the load campaign uses as synthetic growth on top
of the first customer's masked copy. The masked copy itself is NOT produced here; this generator only
makes the synthetic part. Output is reproducible: the same arguments and seed give the same bytes, and
the manifest records the sha256 of the CSV so a campaign can prove which fixture it ran against.

    python3 load/tools/gen_fixture.py --total-lines 40000000 --months 36 --tenants 1 \\
        --lines-per-journal 4 --seed 50 --out fx-load-10k.csv --manifest fx-load-10k.manifest.json

Columns: tenant_id, journal_id, line_no, period, account, currency, debit, credit.
Every journal is balanced in its own currency: the debit lines and the credit lines carry the same
amounts in the same total (the credit side mirrors the debit side). Amounts are decimal strings with two
places, never floats. Journal ids are synthetic (`<tenant index>-<journal index>`), not UUIDs.

Stdlib only, Python 3.11+. Tests: python3 -m unittest discover -s load/tools
"""

from __future__ import annotations

import argparse
import hashlib
import json
import random
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any, TextIO

SCHEMA = "passage.load.fixture-synthetic/v1"
COLUMNS = ["tenant_id", "journal_id", "line_no", "period", "account", "currency", "debit", "credit"]
CURRENCIES = ["EUR", "USD", "GBP"]
ACCOUNTS = [f"{n:04d}" for n in range(1000, 10000, 37)]  # 245 synthetic account numbers


@dataclass(frozen=True)
class Config:
    total_lines: int
    months: int
    tenants: int
    lines_per_journal: int
    seed: int
    first_period: tuple[int, int] = (2024, 1)  # (year, month) of the first period

    def validate(self) -> None:
        if self.total_lines < 1:
            raise ValueError("total_lines must be >= 1")
        if self.months < 1 or self.tenants < 1:
            raise ValueError("months and tenants must be >= 1")
        if self.lines_per_journal < 2 or self.lines_per_journal % 2:
            raise ValueError("lines_per_journal must be an even number >= 2")


def period_label(cfg: Config, month_index: int) -> str:
    year, month = cfg.first_period
    total = year * 12 + (month - 1) + month_index
    return f"{total // 12:04d}-{total % 12 + 1:02d}"


def money(cents: int) -> str:
    return f"{cents // 100}.{cents % 100:02d}"


def tenant_id(index: int) -> str:
    """Deterministic, valid UUID-shaped tenant id for fixture tenant `index` (0-based)."""
    return f"00000000-0000-7000-8000-{index + 1:012d}"


def journal_rows(cfg: Config, rng: random.Random, tenant_index: int, journal_index: int, month_index: int) -> list[list[str]]:
    """One balanced journal: lines/2 debit lines, then lines/2 credit lines mirroring the same amounts."""
    half = cfg.lines_per_journal // 2
    currency = CURRENCIES[(tenant_index + journal_index) % len(CURRENCIES)]
    period = period_label(cfg, month_index)
    amounts = [rng.randint(100, 5_000_000) for _ in range(half)]  # cents
    journal_id = f"{tenant_index:04d}-{journal_index:010d}"
    rows: list[list[str]] = []
    line_no = 1
    for cents in amounts:
        account = rng.choice(ACCOUNTS)
        rows.append([tenant_id(tenant_index), journal_id, str(line_no), period, account, currency, money(cents), "0.00"])
        line_no += 1
    for cents in reversed(amounts):
        account = rng.choice(ACCOUNTS)
        rows.append([tenant_id(tenant_index), journal_id, str(line_no), period, account, currency, "0.00", money(cents)])
        line_no += 1
    return rows


class _HashingWriter:
    """Writes text and feeds the same bytes to sha256 so the manifest needs no second pass."""

    def __init__(self, out: TextIO) -> None:
        self.out = out
        self.sha = hashlib.sha256()
        self.bytes = 0

    def write(self, text: str) -> None:
        data = text.encode("utf-8")
        self.sha.update(data)
        self.bytes += len(data)
        self.out.write(text)


def generate(cfg: Config, out: TextIO) -> dict[str, Any]:
    """Stream the fixture to `out` and return its manifest."""
    cfg.validate()
    rng = random.Random(cfg.seed)
    writer = _HashingWriter(out)
    writer.write(",".join(COLUMNS) + "\n")

    journals = -(-cfg.total_lines // cfg.lines_per_journal)  # ceiling division
    lines = 0
    per_tenant = [0] * cfg.tenants
    per_month = [0] * cfg.months
    for j in range(journals):
        tenant_index = j % cfg.tenants
        month_index = j * cfg.months // journals  # spread evenly across the months
        for row in journal_rows(cfg, rng, tenant_index, j, month_index):
            writer.write(",".join(row) + "\n")
        lines += cfg.lines_per_journal
        per_tenant[tenant_index] += cfg.lines_per_journal
        per_month[month_index] += cfg.lines_per_journal

    return {
        "schema": SCHEMA,
        "fixture": "fx-load-10k",
        "synthetic": True,
        "note": "synthetic growth only; the masked copy of the first customer is a separate extract",
        "seed": cfg.seed,
        "tenants": cfg.tenants,
        "months": cfg.months,
        "firstPeriod": period_label(cfg, 0),
        "linesPerJournal": cfg.lines_per_journal,
        "journals": journals,
        "lines": lines,
        "linesPerTenant": per_tenant,
        "linesPerMonth": per_month,
        "csvBytes": writer.bytes,
        "csvSha256": "sha256:" + writer.sha.hexdigest(),
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--total-lines", type=int, required=True)
    parser.add_argument("--months", type=int, default=36, help="3 years by default (PRD 9.50.19)")
    parser.add_argument("--tenants", type=int, default=1)
    parser.add_argument("--lines-per-journal", type=int, default=4)
    parser.add_argument("--seed", type=int, default=50)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, help="write the manifest JSON here")
    args = parser.parse_args(argv)

    cfg = Config(args.total_lines, args.months, args.tenants, args.lines_per_journal, args.seed)
    try:
        cfg.validate()
    except ValueError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    with args.out.open("w", encoding="utf-8", newline="") as fh:
        manifest = generate(cfg, fh)
    if args.manifest:
        args.manifest.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(f"wrote {manifest['lines']} lines in {manifest['journals']} journals; {manifest['csvSha256']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
