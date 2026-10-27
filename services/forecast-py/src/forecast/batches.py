"""Resumable batch generation (FR-22.9, 4.22.16).

A weekly generation covers up to 50,000 series. The series are cut into batches of
at most 1,000; each batch is computed on its own so a failure costs one batch, and a
batch the caller already persisted is skipped on the next call. A run is complete only
when no batch failed: the caller must not publish a partial forecast version.

This module is pure. The compute function is passed in, so it can be tested without
HTTP and the models.
"""

from __future__ import annotations

from collections.abc import Callable, Sequence
from dataclasses import dataclass, field
from typing import Generic, TypeVar

MAX_BATCH_SIZE = 1000
MAX_SERIES = 50_000

S = TypeVar("S")
R = TypeVar("R")

COMPLETED = "completed"
SKIPPED = "skipped"
FAILED = "failed"


@dataclass(frozen=True)
class BatchOutcome(Generic[R]):
    index: int
    status: str
    results: list[R] = field(default_factory=list)
    detail: str = ""


def count(n: int, batch_size: int) -> int:
    """Number of batches n series make at batch_size."""
    if not 1 <= batch_size <= MAX_BATCH_SIZE:
        raise ValueError(f"batch size must be 1 to {MAX_BATCH_SIZE}")
    return -(-n // batch_size)


def split(series: Sequence[S], batch_size: int) -> list[Sequence[S]]:
    """Cut series into consecutive batches of batch_size; the last one may be shorter."""
    if not 1 <= batch_size <= MAX_BATCH_SIZE:
        raise ValueError(f"batch size must be 1 to {MAX_BATCH_SIZE}")
    if len(series) > MAX_SERIES:
        raise ValueError(f"at most {MAX_SERIES} series per run")
    return [series[i : i + batch_size] for i in range(0, len(series), batch_size)]


def run(
    series: Sequence[S],
    batch_size: int,
    completed: Sequence[int],
    compute: Callable[[S], R],
) -> list[BatchOutcome[R]]:
    """Compute every batch not listed in completed.

    Any exception compute raises fails its whole batch (a batch is published or
    retried as a unit); the other batches still run, and the outcome carries the
    reason.
    """
    done = set(completed)
    outcomes: list[BatchOutcome[R]] = []
    for index, chunk in enumerate(split(series, batch_size)):
        if index in done:
            outcomes.append(BatchOutcome(index=index, status=SKIPPED))
            continue
        try:
            results = [compute(s) for s in chunk]
        except Exception as exc:  # noqa: BLE001 - one bad series costs its batch, not the run
            outcomes.append(BatchOutcome(index=index, status=FAILED, detail=f"{type(exc).__name__}: {exc}"))
            continue
        outcomes.append(BatchOutcome(index=index, status=COMPLETED, results=results))
    return outcomes


def is_complete(outcomes: Sequence[BatchOutcome[R]]) -> bool:
    """True when no batch failed. Skipped batches count as done: the caller holds them."""
    return all(o.status != FAILED for o in outcomes)
