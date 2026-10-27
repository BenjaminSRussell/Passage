"""Statistical forecast models (FR-22.9). Pure functions, no I/O.

Models:
- ``ses``: simple exponential smoothing (ETS A,N) with alpha chosen on a grid by AICc.
- ``croston``: Croston's method for intermittent demand.
- ``tsb``: Teunter-Syntetos-Babai, intermittent demand with decaying demand probability.
- ``naive``: last observed value, used when history is too short.

Selection (``select_model``): ADI > 1.32 means intermittent, so TSB is used; otherwise
ETS(A,N) is used. Histories shorter than ``MIN_HISTORY`` fall back to naive and the
caller must report PLAN-0012.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from typing import Sequence

INTERMITTENT_ADI = 1.32
MIN_HISTORY = 24
ALPHA_GRID = tuple(round(0.05 * k, 2) for k in range(1, 21))  # 0.05 .. 1.00


@dataclass(frozen=True)
class Forecast:
    model: str
    value: float  # forecast per period, applied to every horizon period
    short_history: bool


def adi(y: Sequence[float]) -> float:
    """Average demand interval: periods per non-zero demand. inf when there is no demand."""
    nonzero = sum(1 for v in y if v > 0)
    if nonzero == 0:
        return math.inf
    return len(y) / nonzero


def croston(y: Sequence[float], alpha: float = 0.1) -> float:
    """Croston's method. Returns the per-period forecast z/p."""
    if not 0 < alpha <= 1:
        raise ValueError("alpha must be in (0, 1]")
    first = next((i for i, v in enumerate(y) if v > 0), None)
    if first is None:
        return 0.0
    z = float(y[first])
    p = float(first + 1)
    q = 1
    for v in y[first + 1 :]:
        if v > 0:
            z += alpha * (v - z)
            p += alpha * (q - p)
            q = 1
        else:
            q += 1
    return z / p


def tsb(y: Sequence[float], alpha: float = 0.1, beta: float = 0.1) -> float:
    """TSB: demand probability and size smoothed separately; forecast = p * z."""
    if not (0 < alpha <= 1 and 0 < beta <= 1):
        raise ValueError("alpha and beta must be in (0, 1]")
    nonzero = [v for v in y if v > 0]
    if not nonzero:
        return 0.0
    z = float(nonzero[0])
    prob = sum(1 for v in y if v > 0) / len(y)
    for v in y:
        hit = 1.0 if v > 0 else 0.0
        prob += beta * (hit - prob)
        if v > 0:
            z += alpha * (v - z)
    return prob * z


def _ses_sse(y: Sequence[float], alpha: float) -> tuple[float, float]:
    level = float(y[0])
    sse = 0.0
    for v in y[1:]:
        err = v - level
        sse += err * err
        level += alpha * err
    return sse, level


def ses(y: Sequence[float]) -> tuple[float, float]:
    """Simple exponential smoothing with alpha chosen by AICc. Returns (forecast, alpha)."""
    n = len(y)
    if n < 2:
        return (float(y[0]) if n else 0.0), 0.0
    best: tuple[float, float, float] | None = None  # (aicc, alpha, level)
    for alpha in ALPHA_GRID:
        sse, level = _ses_sse(y, alpha)
        k = 2  # alpha and initial level
        mse = max(sse / n, 1e-12)
        aicc = n * math.log(mse) + 2 * k + (2 * k * (k + 1)) / max(n - k - 1, 1)
        if best is None or aicc < best[0]:
            best = (aicc, alpha, level)
    assert best is not None
    return best[2], best[1]


def naive(y: Sequence[float]) -> float:
    return float(y[-1]) if y else 0.0


MODELS = ("auto", "ets", "croston", "tsb", "naive")
HOLDOUT_MAX = 12


def fit(y: Sequence[float], model: str) -> float:
    """Per-period forecast of one concrete model (not ``auto``)."""
    if model == "ets":
        return ses(y)[0]
    if model == "croston":
        return croston(y)
    if model == "tsb":
        return tsb(y)
    if model == "naive":
        return naive(y)
    raise ValueError(f"unknown model {model!r}")


def choose(y: Sequence[float], requested: str = "auto") -> str:
    """The concrete model for a history: ``auto`` picks naive for short
    histories, TSB when ADI > 1.32 (intermittent) and ETS otherwise."""
    if requested not in MODELS:
        raise ValueError(f"unknown model {requested!r}")
    if requested != "auto":
        return requested
    if len(y) < MIN_HISTORY:
        return "naive"
    return "tsb" if adi(y) > INTERMITTENT_ADI else "ets"


def select_model(y: Sequence[float], requested: str = "auto") -> Forecast:
    """Pick and run a model for one item/site history (FR-22.9, PLAN-0012)."""
    model = choose(y, requested)
    return Forecast(model, fit(y, model), short_history=len(y) < MIN_HISTORY)


def backtest(y: Sequence[float], model: str) -> tuple[float | None, float | None]:
    """Out-of-sample accuracy of ``model`` (FR-22.9 "MAPE/wMAPE per item").

    The last ``min(12, len(y) // 4)`` periods are held out, the model is refit
    on the periods before them, and its flat forecast is scored against the
    held-out actuals. Returns ``(mape, wmape)``; each is None when undefined
    (history too short to hold anything out, or all held-out actuals zero).
    """
    h = min(HOLDOUT_MAX, len(y) // 4)
    if h < 1 or len(y) - h < 2:
        return None, None
    train, test = y[:-h], y[-h:]
    value = max(fit(train, model), 0.0)
    predicted = [value] * h
    return mape(test, predicted), wmape(test, predicted)


def mape(actual: Sequence[float], forecast: Sequence[float]) -> float | None:
    """Mean absolute percentage error over non-zero actuals; None when undefined."""
    pairs = [(a, f) for a, f in zip(actual, forecast) if a != 0]
    if not pairs:
        return None
    return sum(abs(a - f) / abs(a) for a, f in pairs) / len(pairs)


def wmape(actual: Sequence[float], forecast: Sequence[float]) -> float | None:
    """Weighted MAPE: sum |a-f| / sum |a|; None when all actuals are zero."""
    denom = sum(abs(a) for a in actual)
    if denom == 0:
        return None
    return sum(abs(a - f) for a, f in zip(actual, forecast)) / denom
