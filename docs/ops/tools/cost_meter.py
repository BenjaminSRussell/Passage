#!/usr/bin/env python3
"""Monthly tenant cost meter (PSG-50, FR-50.9, BR-50.10, AC-50.7, COST-1901).

Turns one tenant's metered usage for one month into a cost statement in USD and EUR:

    cost_usd = sum(unit_rate[dim] * quantity[dim]) * (1 + overhead)

Rules implemented here:
- Money is `Decimal` only. Floats are rejected at the boundary, so no binary rounding error reaches a statement.
- Rounding happens once per document: the raw USD total is kept unrounded, USD is rounded to 0.01 and EUR is
  derived from the raw total (not from the rounded USD) and rounded to 0.01 (BR-50.10).
- EUR uses the ECB month-average rate, given as USD per 1 EUR (`cost.fx_source=ECB_MONTH_AVG`).
- The meter is reconciled with provider billing for the cluster: the sum of the tenants' rounded USD costs for
  the month is compared with the cluster invoice (cost-model.md, "Accuracy"); a variance above the limit
  (default 5%) raises COST-1901 (AC-50.7). A single tenant is compared only when it is the cluster's only tenant.

This is the calculator only. The monthly job that reads Prometheus and writes `tenant_cost` belongs to the ops
service (see docs/ops/NOT-IMPLEMENTED.md).

    python3 docs/ops/tools/cost_meter.py --usage t1.json [--usage t2.json ...] --rates rates.json \\
        --usd-per-eur 1.0850 --month 2026-10 [--billed-usd 1234.56]

Each usage file is one tenant's month: the six dimensions as decimal strings plus an optional `tenantId`.
`--billed-usd` is the provider invoice for the cluster, so every tenant on the cluster must be passed.
Exit status: 0 statements printed (and variance within the limit), 1 COST-1901, 2 invalid input.

Stdlib only, Python 3.11+. Tests: python3 -m unittest discover -s docs/ops/tools
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass
from decimal import ROUND_HALF_UP, Decimal, InvalidOperation
from pathlib import Path
from typing import Any

DIMENSIONS = ("cpu_core_hours", "memory_gb_hours", "storage_gb", "kafka_gb", "lakehouse_gb", "ai_tokens")
CENT = Decimal("0.01")
DEFAULT_OVERHEAD = Decimal("0.12")  # [ASSUMED 12%] shared control-plane cost (cost-model.md)
DEFAULT_VARIANCE_LIMIT_PCT = Decimal("5")  # AC-50.7
MONTH_RE = re.compile(r"^(\d{4})-(0[1-9]|1[0-2])$")
CODE_VARIANCE = "COST-1901"


class CostError(ValueError):
    """Invalid input to the meter. The message is safe to show an operator."""


def dec(value: Any, field: str) -> Decimal:
    """Parse a non-negative decimal. Floats are rejected: money and quantities are never binary floats."""
    if isinstance(value, bool) or isinstance(value, float):
        raise CostError(f"{field}: floats are not allowed; send a decimal string")
    if not isinstance(value, (str, int, Decimal)):
        raise CostError(f"{field}: expected a decimal string")
    try:
        d = Decimal(str(value))
    except InvalidOperation as exc:
        raise CostError(f"{field}: not a decimal: {value!r}") from exc
    if not d.is_finite() or d < 0:
        raise CostError(f"{field}: must be a finite non-negative number")
    return d


def check_month(month: str) -> str:
    if not MONTH_RE.match(month or ""):
        raise CostError("month must be YYYY-MM")
    return month


@dataclass(frozen=True)
class Usage:
    """Metered quantities for one tenant and one month."""

    quantities: dict[str, Decimal]
    tenant_id: str | None = None

    @staticmethod
    def from_dict(raw: dict[str, Any]) -> "Usage":
        raw = dict(raw)
        tenant_id = raw.pop("tenantId", None)
        if tenant_id is not None and (not isinstance(tenant_id, str) or not tenant_id.strip()):
            raise CostError("tenantId must be a non-empty string")
        unknown = set(raw) - set(DIMENSIONS)
        if unknown:
            raise CostError(f"unknown usage dimension(s): {', '.join(sorted(unknown))}")
        missing = [d for d in DIMENSIONS if d not in raw]
        if missing:
            raise CostError(f"missing usage dimension(s): {', '.join(missing)}; send 0 explicitly")
        return Usage({d: dec(raw[d], d) for d in DIMENSIONS}, tenant_id)


@dataclass(frozen=True)
class Rates:
    """USD unit rates per dimension (per core-hour, GiB-hour, GB-month, GB, token) for one provider."""

    unit_usd: dict[str, Decimal]
    overhead: Decimal = DEFAULT_OVERHEAD

    @staticmethod
    def from_dict(raw: dict[str, Any], overhead: Decimal | None = None) -> "Rates":
        missing = [d for d in DIMENSIONS if d not in raw]
        if missing:
            raise CostError(f"missing rate(s): {', '.join(missing)}")
        ov = DEFAULT_OVERHEAD if overhead is None else overhead
        if ov < 0 or ov > 1:
            raise CostError("overhead must be between 0 and 1")
        return Rates({d: dec(raw[d], f"rate {d}") for d in DIMENSIONS}, ov)


@dataclass(frozen=True)
class Statement:
    """One `tenant_cost` row (PK tenant_id, month) with its resource lines."""

    month: str
    tenant_id: str | None
    quantities: dict[str, Decimal]
    lines: dict[str, Decimal]  # per dimension, USD before overhead, rounded to cents for display
    subtotal_usd: Decimal  # before overhead, unrounded internally
    cost_usd: Decimal  # rounded to 0.01
    cost_eur: Decimal  # rounded to 0.01
    usd_per_eur: Decimal
    model_version: str

    def to_dict(self) -> dict[str, Any]:
        return {
            "tenantId": self.tenant_id,
            "month": self.month,
            "quantities": {k: str(v) for k, v in self.quantities.items()},
            "lines": {k: str(v) for k, v in self.lines.items()},
            "costUsd": str(self.cost_usd),
            "costEur": str(self.cost_eur),
            "fx": {"source": "ECB_MONTH_AVG", "usdPerEur": str(self.usd_per_eur)},
            "modelVersion": self.model_version,
        }


def compute(month: str, usage: Usage, rates: Rates, usd_per_eur: Any, model_version: str) -> Statement:
    """Compute one statement. Pure: same inputs give the same statement."""
    check_month(month)
    fx = dec(usd_per_eur, "usd_per_eur")
    if fx == 0:
        raise CostError("usd_per_eur must be greater than zero")
    raw_lines = {d: usage.quantities[d] * rates.unit_usd[d] for d in DIMENSIONS}
    subtotal = sum(raw_lines.values(), Decimal(0))
    raw_total = subtotal * (Decimal(1) + rates.overhead)  # rounding happens once, below
    cost_usd = raw_total.quantize(CENT, rounding=ROUND_HALF_UP)
    cost_eur = (raw_total / fx).quantize(CENT, rounding=ROUND_HALF_UP)
    return Statement(
        month=month,
        tenant_id=usage.tenant_id,
        quantities=dict(usage.quantities),
        lines={d: v.quantize(CENT, rounding=ROUND_HALF_UP) for d, v in raw_lines.items()},
        subtotal_usd=subtotal,
        cost_usd=cost_usd,
        cost_eur=cost_eur,
        usd_per_eur=fx,
        model_version=model_version,
    )


@dataclass(frozen=True)
class Variance:
    month: str
    metered_usd: Decimal
    billed_usd: Decimal
    pct: Decimal
    code: str | None  # COST-1901 when above the limit, else None

    @property
    def ok(self) -> bool:
        return self.code is None


def cluster_total(statements: list[Statement]) -> Decimal:
    """Sum of the tenants' rounded USD costs for one month: the figure reconciled with the cluster invoice."""
    months = {s.month for s in statements}
    if len(months) != 1:
        raise CostError("reconciliation needs statements for exactly one month")
    tenants = [s.tenant_id for s in statements]
    if len(statements) > 1 and (None in tenants or len(set(tenants)) != len(tenants)):
        raise CostError("each statement in a cluster reconciliation needs a distinct tenantId")
    return sum((s.cost_usd for s in statements), Decimal(0))


def variance(
    month: str, metered_usd: Decimal, billed_usd: Any, limit_pct: Decimal = DEFAULT_VARIANCE_LIMIT_PCT
) -> Variance:
    """Compare the cluster's metered total with provider billing for the same cluster and month (AC-50.7)."""
    check_month(month)
    billed = dec(billed_usd, "billed_usd")
    if billed == 0:
        raise CostError("billed_usd must be greater than zero to compare")
    pct = (abs(metered_usd - billed) / billed * Decimal(100)).quantize(Decimal("0.01"), rounding=ROUND_HALF_UP)
    code = CODE_VARIANCE if pct > limit_pct else None
    return Variance(month=month, metered_usd=metered_usd, billed_usd=billed, pct=pct, code=code)


def variance_message(v: Variance) -> str:
    """COST-1901 text from the message catalog (PRD 9.50.18)."""
    return f"Metered cost for {v.month} differs from provider billing by {v.pct}%."


def load_json(path: Path) -> dict[str, Any]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise CostError(f"cannot read {path}: {exc}") from exc
    if not isinstance(data, dict):
        raise CostError(f"{path}: expected a JSON object")
    return data


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--usage", type=Path, action="append", required=True,
                        help="one tenant's JSON object with the six dimensions (repeat once per tenant)")
    parser.add_argument("--rates", type=Path, required=True, help="JSON object with USD unit rates")
    parser.add_argument("--usd-per-eur", required=True, help="ECB month-average, USD per 1 EUR")
    parser.add_argument("--month", required=True, help="YYYY-MM")
    parser.add_argument("--model-version", default="unversioned")
    parser.add_argument("--overhead", default=None, help="override overhead fraction, default 0.12")
    parser.add_argument("--billed-usd", default=None,
                        help="provider billing for the cluster, same month; every tenant on it must be passed")
    parser.add_argument("--variance-limit-pct", default="5")
    args = parser.parse_args(argv)
    try:
        overhead = None if args.overhead is None else dec(args.overhead, "overhead")
        rates = Rates.from_dict(load_json(args.rates), overhead)
        statements = [
            compute(args.month, Usage.from_dict(load_json(path)), rates, args.usd_per_eur, args.model_version)
            for path in args.usage
        ]
        total = cluster_total(statements)
        out: dict[str, Any] = {"statements": [s.to_dict() for s in statements], "clusterCostUsd": str(total)}
        code = 0
        if args.billed_usd is not None:
            v = variance(args.month, total, args.billed_usd, dec(args.variance_limit_pct, "variance-limit-pct"))
            out["variance"] = {
                "meteredUsd": str(v.metered_usd),
                "billedUsd": str(v.billed_usd),
                "pct": str(v.pct),
                "code": v.code,
                "message": None if v.ok else variance_message(v),
                "modelVersion": args.model_version,
            }
            code = 0 if v.ok else 1
    except CostError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(out, indent=2))
    return code


if __name__ == "__main__":
    sys.exit(main())
