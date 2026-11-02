"""Statistical forecast models (FR-22.9). Pure functions, no I/O.

Models:
- ``ets``: the exponential smoothing family selected by AICc (``ets_fit``):
  A,N (simple smoothing), A,A,N (additive trend) and A,A,A (additive trend and
  additive season of length ``season``, needs two full seasons plus one period),
  and the damped-trend forms A,Ad,N and A,Ad,A, which flatten the trend by a
  factor phi per period so a long horizon does not extrapolate it without end.
  All candidates are scored on the same residuals, so their AICc compare.
- ``croston``: Croston's method for intermittent demand.
- ``tsb``: Teunter-Syntetos-Babai, intermittent demand with decaying demand probability.
- ``naive``: last observed value, used when history is too short.

Selection (``select_model``): ADI > 1.32 means intermittent, so TSB is used; otherwise
ETS is used. Histories shorter than ``MIN_HISTORY`` fall back to naive and the
caller must report PLAN-0012. Level models forecast the same value for every
horizon period; trend and seasonal ETS forecast one value per period (``path``).
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from typing import Sequence

INTERMITTENT_ADI = 1.32
MIN_HISTORY = 24
DEFAULT_SEASON = 12  # monthly buckets
ALPHA_GRID = tuple(round(0.05 * k, 2) for k in range(1, 21))  # 0.05 .. 1.00
SMOOTHING_GRID = (0.1, 0.3, 0.6)  # coarse grid for the trend and season models
PHI_GRID = (0.8, 0.9, 0.98)  # damping factors of the damped-trend candidates
SEASON_CANDIDATES = (3, 4, 6, 12)  # quarterly, four-monthly, half-yearly and yearly cycles of monthly buckets
SEASON_MIN_ACF = 0.4  # autocorrelation a cycle length needs to count as seasonal


@dataclass(frozen=True)
class Forecast:
    model: str
    value: float  # first horizon period; equals path[0]
    short_history: bool
    path: tuple[float, ...] = ()  # one non-negative value per horizon period


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


def _aicc(sse: float, n: int, k: int) -> float:
    """Corrected AIC of a Gaussian model with k parameters over n residuals."""
    mse = max(sse / n, 1e-12)
    return n * math.log(mse) + 2 * k + (2 * k * (k + 1)) / max(n - k - 1, 1)


@dataclass(frozen=True)
class _EtsFit:
    variant: str  # "A,N", "A,A,N", "A,Ad,N", "A,A,A" or "A,Ad,A"
    aicc: float
    level: float
    trend: float
    season: tuple[float, ...]  # seasonal states by period index mod m; (0.0,) without season
    m: int
    phi: float = 1.0  # damping factor of the trend; 1.0 is undamped

    def path(self, n: int, horizon: int) -> tuple[float, ...]:
        """Forecast of periods n .. n+horizon-1 after n observations."""
        out = []
        for h in range(1, horizon + 1):
            # The trend of h steps ahead is phi + phi^2 + ... + phi^h times the trend state.
            steps = float(h) if self.phi == 1.0 else self.phi * (1 - self.phi**h) / (1 - self.phi)
            out.append(self.level + steps * self.trend + self.season[(n + h - 1) % self.m])
        return tuple(out)


def _ses_candidate(y: Sequence[float], score_from: int) -> _EtsFit:
    """A,N: best alpha on the grid, scored on residuals from ``score_from``."""
    best: _EtsFit | None = None
    for alpha in ALPHA_GRID:
        level = float(y[0])
        sse = 0.0
        for t in range(1, len(y)):
            err = y[t] - level
            if t >= score_from:
                sse += err * err
            level += alpha * err
        aicc = _aicc(sse, len(y) - score_from, 2)  # alpha and initial level
        if best is None or aicc < best.aicc:
            best = _EtsFit("A,N", aicc, level, 0.0, (0.0,), 1)
    assert best is not None
    return best


def _holt_run(y: Sequence[float], m: int, alpha: float, beta: float, gamma: float, score_from: int, phi: float = 1.0):
    """Additive trend (m == 1) or trend and season (m > 1), damped by phi. Returns (sse, level, trend, season)."""
    if m > 1:
        level = sum(y[:m]) / m
        trend = (sum(y[m : 2 * m]) - sum(y[:m])) / (m * m)
        season = [y[i] - level for i in range(m)]
        start = m
    else:
        # State after the first two observations: level at y[1], trend y[1] - y[0];
        # the recursion then forecasts y[2] as level + phi * trend.
        level = float(y[1])
        trend = float(y[1] - y[0])
        season = [0.0]
        start = 2
    sse = 0.0
    for t in range(start, len(y)):
        s_prev = season[t % m]
        err = y[t] - (level + phi * trend + s_prev)
        if t >= score_from:
            sse += err * err
        new_level = alpha * (y[t] - s_prev) + (1 - alpha) * (level + phi * trend)
        trend = beta * (new_level - level) + (1 - beta) * phi * trend
        if m > 1:
            season[t % m] = gamma * (y[t] - new_level) + (1 - gamma) * s_prev
        level = new_level
    return sse, level, trend, tuple(season)


def _holt_candidate(y: Sequence[float], m: int, score_from: int, phis: Sequence[float] = (1.0,)) -> _EtsFit:
    """A,A,N / A,A,A (phis == (1.0,)) or the damped A,Ad,N / A,Ad,A (phis == PHI_GRID):
    best smoothing parameters on the grid. A damped candidate pays one more
    parameter (phi) in its AICc."""
    damped = tuple(phis) != (1.0,)
    gammas = SMOOTHING_GRID if m > 1 else (0.0,)
    k = (4 if m == 1 else 5 + m) + (1 if damped else 0)  # smoothing parameters plus initial states (and phi)
    name = ("A,Ad," if damped else "A,A,") + ("A" if m > 1 else "N")
    best: _EtsFit | None = None
    for alpha in SMOOTHING_GRID:
        for beta in SMOOTHING_GRID:
            for gamma in gammas:
                for phi in phis:
                    sse, level, trend, season = _holt_run(y, m, alpha, beta, gamma, score_from, phi)
                    aicc = _aicc(sse, len(y) - score_from, k)
                    if best is None or aicc < best.aicc:
                        best = _EtsFit(name, aicc, level, trend, season, m, phi)
    assert best is not None
    return best


def detect_season(y: Sequence[float], candidates: Sequence[int] = SEASON_CANDIDATES) -> int:
    """Season length of a history by autocorrelation (FR-22.9), 1 when none.

    The linear trend is removed first, so a growing series is not read as
    seasonal. A cycle length m counts when the history holds two full cycles
    plus one period (the minimum the seasonal ETS needs) and its lag-m
    autocorrelation reaches ``SEASON_MIN_ACF``; among the lengths within 10%
    of the strongest the shortest wins, since a cycle of 4 also repeats at 12.
    """
    n = len(y)
    if n < 3:
        return 1
    t_mean = (n - 1) / 2
    y_mean = sum(y) / n
    denom_t = sum((t - t_mean) ** 2 for t in range(n))
    slope = sum((t - t_mean) * (v - y_mean) for t, v in enumerate(y)) / denom_t
    resid = [v - (y_mean + slope * (t - t_mean)) for t, v in enumerate(y)]
    var = sum(r * r for r in resid)
    if var <= 1e-12:
        return 1
    acf = {}
    for m in candidates:
        if m > 1 and n >= 2 * m + 1:
            acf[m] = sum(resid[t] * resid[t - m] for t in range(m, n)) / var
    if not acf:
        return 1
    top = max(acf.values())
    if top < SEASON_MIN_ACF:
        return 1
    return min(m for m, a in acf.items() if a >= 0.9 * top)


def ets_fit(y: Sequence[float], season: int = DEFAULT_SEASON) -> _EtsFit:
    """ETS selection by AICc among A,N, A,A,N, A,Ad,N, A,A,A and A,Ad,A (FR-22.9).

    A,A,N needs 4 observations and A,A,A ``2 * season + 1``; each has a damped
    form (A,Ad,N, A,Ad,A) that wins only when its AICc is lower despite the
    extra parameter, so the undamped form keeps ties. Every eligible
    candidate is scored on the residuals after the longest warm-up among them,
    so the AICc values compare. Ties go to the simpler model, considered first.
    """
    n = len(y)
    if n < 2:
        return _EtsFit("A,N", 0.0, float(y[0]) if n else 0.0, 0.0, (0.0,), 1)
    seasonal = season > 1 and n >= 2 * season + 1
    score_from = season if seasonal else (2 if n >= 4 else 1)
    best = _ses_candidate(y, score_from)
    if n >= 4:
        candidates = [_holt_candidate(y, 1, score_from), _holt_candidate(y, 1, score_from, PHI_GRID)]
        if seasonal:
            candidates += [_holt_candidate(y, season, score_from), _holt_candidate(y, season, score_from, PHI_GRID)]
        for fit_ in candidates:
            if fit_.aicc < best.aicc:
                best = fit_
    return best


def naive(y: Sequence[float]) -> float:
    return float(y[-1]) if y else 0.0


MODELS = ("auto", "ets", "croston", "tsb", "naive")
HOLDOUT_MAX = 12


def fit(y: Sequence[float], model: str, horizon: int = 1, season: int = DEFAULT_SEASON) -> tuple[float, ...]:
    """Forecast path of one concrete model (not ``auto``), clipped at zero:
    demand cannot be negative even when a trend extrapolates below it."""
    if horizon < 1:
        raise ValueError("horizon must be at least 1")
    if season < 1:
        raise ValueError("season must be at least 1")
    if model == "ets":
        path = ets_fit(y, season).path(len(y), horizon)
    elif model == "croston":
        path = (croston(y),) * horizon
    elif model == "tsb":
        path = (tsb(y),) * horizon
    elif model == "naive":
        path = (naive(y),) * horizon
    else:
        raise ValueError(f"unknown model {model!r}")
    return tuple(max(v, 0.0) for v in path)


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


def select_model(
    y: Sequence[float], requested: str = "auto", horizon: int = 1, season: int = DEFAULT_SEASON
) -> Forecast:
    """Pick and run a model for one item/site history (FR-22.9, PLAN-0012)."""
    model = choose(y, requested)
    path = fit(y, model, horizon, season)
    return Forecast(model, path[0], short_history=len(y) < MIN_HISTORY, path=path)


def backtest(y: Sequence[float], model: str, season: int = DEFAULT_SEASON) -> tuple[float | None, float | None]:
    """Out-of-sample accuracy of ``model`` (FR-22.9 "MAPE/wMAPE per item").

    The last ``min(12, len(y) // 4)`` periods are held out, the model is refit
    on the periods before them, and its forecast path is scored against the
    held-out actuals. Returns ``(mape, wmape)``; each is None when undefined
    (history too short to hold anything out, or all held-out actuals zero).
    """
    h = min(HOLDOUT_MAX, len(y) // 4)
    if h < 1 or len(y) - h < 2:
        return None, None
    train, test = y[:-h], y[-h:]
    predicted = fit(train, model, h, season)
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
