"""Independent reference for svc-billing-30 (ticket AC3). Run: python3 -I reference_billing.py

It posts each work order from BR-45.4 and BR-45.8 without the Go engine:
  * a warranty is active on the completion day when start <= day < start +
    months, the month addition clamped to the end of a short month (as
    Postgres date + interval does: 31 Aug + 6 months = 28 Feb);
  * per line: WARRANTY when the active warranty covers its kind (PART, LABOR,
    TRAVEL; a FEE never), else AGREEMENT when the agreement covers its kind,
    else BILLABLE;
  * a WARRANTY line is zero-priced and records qty x unit cost as warranty cost;
  * agreement-covered LABOR draws the agreement's included hours first-in
    first-out across lines and across the orders sharing the agreement; the
    hours beyond them are BILLABLE at the line price; other agreement-covered
    lines are free and draw no hours;
  * every amount and cost is rounded half-even per posted line to the
    currency's ISO 4217 minor unit.

It checks the totals against the hand-derived 'hand' block of cases.json and
writes expected.json (posted lines sorted, totals). Exit status 1 on any
disagreement.
"""

from __future__ import annotations

import calendar
import json
import sys
from datetime import date
from decimal import ROUND_HALF_EVEN, Decimal
from pathlib import Path

HERE = Path(__file__).resolve().parent
DEFAULT_COMPLETED = "2026-10-21"
MINOR = {"USD": 2, "EUR": 2, "JPY": 0, "KWD": 3}
FLAG = {"PART": "parts", "LABOR": "labor", "TRAVEL": "travel"}


def round_minor(x: Decimal, currency: str) -> Decimal:
    return x.quantize(Decimal(1).scaleb(-MINOR[currency]), rounding=ROUND_HALF_EVEN)


def add_months(d: date, months: int) -> date:
    y, m = divmod(d.month - 1 + months, 12)
    y, m = d.year + y, m + 1
    return date(y, m, min(d.day, calendar.monthrange(y, m)[1]))


def warranty_active(w: dict | None, on: date) -> bool:
    if not w or w["months"] <= 0:
        return False
    start = date.fromisoformat(w["startsOn"])
    return start <= on < add_months(start, w["months"])


def post(wo: dict, agreement: dict | None, remaining: Decimal) -> tuple[dict, Decimal]:
    cur = wo["currency"]
    on = date.fromisoformat(wo.get("completedOn", DEFAULT_COMPLETED))
    w = wo.get("warranty")
    active = warranty_active(w, on)
    lines, billable, warranty_cost, consumed = [], Decimal(0), Decimal(0), Decimal(0)
    for l in wo["lines"]:
        kind, qty = l["kind"], Decimal(l["qty"])
        price, cost = Decimal(l.get("unitPrice", "0")), Decimal(l.get("unitCost", "0"))
        flag = FLAG.get(kind)
        if flag and active and w.get(flag, False):
            wc = round_minor(qty * cost, cur)
            lines.append([l["id"], "WARRANTY", qty, Decimal(0), wc, Decimal(0)])
            warranty_cost += wc
        elif flag and agreement and agreement[flag]:
            if kind != "LABOR":
                lines.append([l["id"], "AGREEMENT", qty, Decimal(0), Decimal(0), Decimal(0)])
                continue
            use = min(qty, max(remaining, Decimal(0)))
            remaining -= use
            consumed += use
            if use > 0:
                lines.append([l["id"], "AGREEMENT", use, Decimal(0), Decimal(0), use])
            if qty - use > 0:
                amt = round_minor((qty - use) * price, cur)
                lines.append([l["id"], "BILLABLE", qty - use, amt, Decimal(0), Decimal(0)])
                billable += amt
        else:
            amt = round_minor(qty * price, cur)
            lines.append([l["id"], "BILLABLE", qty, amt, Decimal(0), Decimal(0)])
            billable += amt
    out = {
        "billableTotal": str(billable),
        "warrantyCost": str(warranty_cost),
        "entitlementConsumed": str(consumed),
        "lines": sorted([[a, b, str(c), str(d), str(e), str(f)] for a, b, c, d, e, f in lines]),
    }
    return out, remaining


def main() -> int:
    data = json.loads((HERE / "cases.json").read_text())
    remaining = {k: Decimal(v["includedHours"]) for k, v in data["agreements"].items()}
    out, bad = {}, 0
    for wo in data["workOrders"]:
        label = wo.get("agreement")
        agreement = data["agreements"][label] if label else None
        got, left = post(wo, agreement, remaining[label] if label else Decimal(0))
        if label:
            remaining[label] = left
        for k, v in wo["hand"].items():
            if Decimal(got[k]) != Decimal(v):
                print(f"{wo['id']}: {k} reference={got[k]} hand={v}", file=sys.stderr)
                bad += 1
        out[wo["id"]] = got
    if len(out) != 30:
        print(f"want 30 work orders, have {len(out)}", file=sys.stderr)
        bad += 1
    (HERE / "expected.json").write_text(json.dumps(out, indent=1, sort_keys=True) + "\n")
    print(f"{len(out)} work orders, {bad} disagreements with the hand-derived totals")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
