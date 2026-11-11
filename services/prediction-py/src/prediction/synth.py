"""Deterministic synthetic receivables history for tests and demos (fictional Cedar Ridge Pumps
customers). Lateness has a customer habit, terms, amount, dunning, dispute and December effects
plus noise and a tail of very late payers (2%, far more likely with a dispute or heavy dunning), so the model has real signal to find."""

from __future__ import annotations

import random
from datetime import date, timedelta

from prediction.domain import OpenInvoice, PaidInvoice

TERMS = (15, 30, 45, 60)


def customers(rng: random.Random, n: int) -> dict[str, float]:
    return {f"cust-{i:03d}": rng.uniform(-3, 12) for i in range(n)}


def _draw(rng: random.Random, habits: dict[str, float], i: int, start: date) -> tuple[OpenInvoice, float]:
    cust = rng.choice(sorted(habits))
    terms = rng.choice(TERMS)
    invoice_date = start + timedelta(days=rng.randint(0, 540))
    amount = round(rng.lognormvariate(8.0, 1.0), 2)
    dunning = rng.choices([0, 1, 2, 3], weights=[70, 18, 8, 4])[0]
    disputes = rng.choices([0, 1], weights=[92, 8])[0]
    due = invoice_date + timedelta(days=terms)
    late = habits[cust] + 0.4 * (terms - 30) / 15 + 1.2 * (amount > 8000) + 4.0 * dunning + 9.0 * disputes
    late += 3.0 if due.month == 12 else 0.0
    late += rng.gauss(0, 2.0) + (rng.uniform(25, 40) if rng.random() < 0.02 + 0.15 * disputes + 0.05 * (dunning >= 2) else 0.0)
    inv = OpenInvoice(f"INV-{i:06d}", cust, amount, invoice_date, due, dunning, disputes)
    return inv, late


def paid_invoices(n: int, seed: int = 47, n_customers: int = 60) -> list[PaidInvoice]:
    rng = random.Random(seed)
    habits = customers(rng, n_customers)
    out: list[PaidInvoice] = []
    for i in range(n):
        inv, late = _draw(rng, habits, i, date(2025, 1, 1))
        out.append(
            PaidInvoice(inv.invoice_id, inv.customer_id, inv.amount, inv.invoice_date, inv.due_date, inv.dunning_count, inv.disputes, inv.due_date + timedelta(days=round(late)))
        )
    return out


def open_invoices(n: int, seed: int = 47, n_customers: int = 60) -> list[OpenInvoice]:
    """Open invoices of the same customers (same seed for the habits), issued after the history."""
    habits = customers(random.Random(seed), n_customers)
    rng = random.Random(seed + 1)
    return [_draw(rng, habits, 900_000 + i, date(2026, 6, 1))[0] for i in range(n)]
