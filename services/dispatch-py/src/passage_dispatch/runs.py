from __future__ import annotations

import logging
import threading
import uuid
from collections import Counter
from collections.abc import Callable
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any

from .auth import Caller
from .cpsat import SolveResult, optimize
from .model import Assignment, Problem

log = logging.getLogger("passage_dispatch.runs")

QUEUED, RUNNING, SUCCEEDED, FAILED = "QUEUED", "RUNNING", "SUCCEEDED", "FAILED"
DEFAULT_CONCURRENCY = 2
MAX_RUNS = 1000
MAX_QUEUED = 50


class QueueFull(Exception):
    pass


class IdempotencyReuse(Exception):
    pass


def _now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


@dataclass
class Run:
    id: str
    tenant_id: str
    legal_entity_id: str
    correlation_id: str
    request_hash: str
    key: str | None
    status: str = QUEUED
    created_at: str = field(default_factory=_now)
    started_at: str | None = None
    finished_at: str | None = None
    result: dict[str, Any] | None = None
    error: dict[str, str] | None = None

    def view(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "status": self.status,
            "createdAt": self.created_at,
            "startedAt": self.started_at,
            "finishedAt": self.finished_at,
            "result": self.result,
            "error": self.error,
        }


def result_view(result: SolveResult) -> dict[str, Any]:
    c = result.cost
    return {
        "assignments": [
            {"workOrderId": a.work_order_id, "technicianId": a.technician_id, "start": a.start, "end": a.end, "sequence": a.sequence}
            for a in result.assignments
        ],
        "unassigned": result.unassigned,
        "solver": result.solver,
        "cpsatStatus": result.cpsat_status,
        "fallbackReason": result.fallback_reason,
        "elapsedSeconds": round(result.elapsed_seconds, 3),
        "cost": {
            "unassignedPriority": c.unassigned_priority,
            "latenessMinutes": c.lateness_minutes,
            "travelMinutes": c.travel_minutes,
            "overtimeMinutes": c.overtime_minutes,
            "technicians": c.technicians,
            "total": c.total,
        },
    }


class RunStore:
    def __init__(
        self,
        concurrency: int = DEFAULT_CONCURRENCY,
        max_runs: int = MAX_RUNS,
        max_queued: int = MAX_QUEUED,
        solve: Callable[..., SolveResult] = optimize,
    ) -> None:
        self._pool = ThreadPoolExecutor(max_workers=concurrency, thread_name_prefix="dispatch-run")
        self._runs: dict[str, Run] = {}
        self._by_key: dict[tuple[str, str], str] = {}
        self._lock = threading.Lock()
        self._max_runs, self._max_queued, self._solve = max_runs, max_queued, solve
        self.outcomes: Counter[str] = Counter()
        self.fallbacks: Counter[str] = Counter()

    def submit(
        self,
        caller: Caller,
        problem: Problem,
        pinned: list[Assignment],
        time_limit_s: float,
        workers: int,
        key: str | None,
        request_hash: str,
        correlation_id: str,
    ) -> tuple[Run, bool]:
        with self._lock:
            if key is not None and (caller.tenant_id, key) in self._by_key:
                existing = self._runs.get(self._by_key[(caller.tenant_id, key)])
                if existing is not None:
                    if existing.request_hash != request_hash:
                        raise IdempotencyReuse
                    return existing, True
            if sum(r.status == QUEUED for r in self._runs.values()) >= self._max_queued:
                raise QueueFull
            run = Run(str(uuid.uuid4()), caller.tenant_id, caller.legal_entity_id, correlation_id, request_hash, key)
            self._runs[run.id] = run
            if key is not None:
                self._by_key[(caller.tenant_id, key)] = run.id
            self._evict()
        self._pool.submit(self._execute, run, problem, pinned, time_limit_s, workers)
        return run, False

    def get(self, caller: Caller, run_id: str) -> Run | None:
        with self._lock:
            run = self._runs.get(run_id)
        if run is None or (run.tenant_id, run.legal_entity_id) != (caller.tenant_id, caller.legal_entity_id):
            return None
        return run

    def _evict(self) -> None:
        finished = [r for r in self._runs.values() if r.status in (SUCCEEDED, FAILED)]
        for old in finished[: max(0, len(self._runs) - self._max_runs)]:
            del self._runs[old.id]
            if old.key is not None:
                self._by_key.pop((old.tenant_id, old.key), None)

    def _execute(self, run: Run, problem: Problem, pinned: list[Assignment], time_limit_s: float, workers: int) -> None:
        context = {"runId": run.id, "correlationId": run.correlation_id}
        with self._lock:
            run.status, run.started_at = RUNNING, _now()
        try:
            result = self._solve(problem, pinned, time_limit_s=time_limit_s, workers=workers)
        except Exception as exc:
            log.exception("dispatch run failed", extra={**context, "code": "solver_error"})
            with self._lock:
                run.status, run.finished_at = FAILED, _now()
                run.error = {"code": "solver_error", "detail": type(exc).__name__}
                self.outcomes[FAILED] += 1
            return
        with self._lock:
            run.result, run.status, run.finished_at = result_view(result), SUCCEEDED, _now()
            self.outcomes[SUCCEEDED] += 1
            if result.fallback_reason:
                self.fallbacks[result.fallback_reason] += 1
        log.info(
            "dispatch run finished",
            extra={**context, "solver": result.solver, "fallbackReason": result.fallback_reason, "cost": result.cost.total,
                   "unassigned": len(result.unassigned), "elapsedSeconds": round(result.elapsed_seconds, 3)},
        )
