"""FR-22.9 slice 26: damped-trend ETS candidates and automatic season-length detection."""

import pytest
from fastapi.testclient import TestClient

from forecast import models
from forecast.api import app

client = TestClient(app)


def wave(length: int, periods: int, trend: float = 0.0) -> list[float]:
    """Demand with a cycle of `length`: the first half of each cycle high, the second low."""
    return [200.0 + trend * t + (40.0 if t % length < length / 2 else -40.0) for t in range(periods)]


def damped_growth(periods: int = 40, phi: float = 0.85) -> list[float]:
    """A level that rises with a trend that dies out: it flattens near 200."""
    level, trend, out = 100.0, 10.0, []
    for _ in range(periods):
        out.append(level)
        level += trend
        trend *= phi
    return out


@pytest.mark.parametrize("length", [3, 4, 6, 12])
def test_detects_the_cycle_length(length):
    assert models.detect_season(wave(length, 4 * length + 12)) == length


def test_detects_a_cycle_inside_a_growing_series():
    assert models.detect_season(wave(12, 60, trend=2.0)) == 12


def test_flat_noise_free_history_is_not_seasonal():
    assert models.detect_season([50.0] * 48) == 1
    assert models.detect_season([2.0 * t + 5.0 for t in range(48)]) == 1


def test_too_short_a_history_for_two_cycles_is_not_seasonal():
    assert models.detect_season(wave(12, 20)) == 1
    assert models.detect_season([1.0, 2.0]) == 1


def test_a_cycle_needs_two_full_cycles_plus_one_period():
    # 24 months hold two yearly cycles but not the extra period the seasonal ETS
    # needs, so 12 is not offered (and no shorter cycle stands out); 25 do.
    assert models.detect_season(wave(12, 24)) == 1
    assert models.detect_season(wave(12, 25)) == 12


def test_constant_or_tiny_histories_are_not_seasonal():
    assert models.detect_season([]) == 1
    assert models.detect_season([0.0] * 30) == 1


def test_damped_candidate_pays_one_more_parameter():
    y = damped_growth()
    plain = models._holt_candidate(y, 1, 2)
    damped = models._holt_candidate(y, 1, 2, models.PHI_GRID)
    n = len(y) - 2
    # Same residuals, k = 4 (alpha, beta, level, trend) against 5 (and phi).
    sse_plain = models._holt_run(y, 1, 0.1, 0.1, 0.0, 2)[0]
    assert models._aicc(sse_plain, n, 4) >= plain.aicc
    assert damped.variant == "A,Ad,N" and damped.phi in models.PHI_GRID
    sse_damped = models._holt_run(y, 1, 0.1, 0.1, 0.0, 2, damped.phi)[0]
    assert models._aicc(sse_damped, n, 5) >= damped.aicc


def test_damped_one_step_recursion():
    # l1 = a*y1 + (1-a)(l0 + phi*b0); b1 = b*(l1 - l0) + (1-b)*phi*b0.
    y = [10.0, 12.0, 15.0]
    sse, level, trend, _ = models._holt_run(y, 1, 0.3, 0.1, 0.0, 2, 0.8)
    l0, b0 = 12.0, 2.0  # initial state after two observations
    err = 15.0 - (l0 + 0.8 * b0)
    l1 = 0.3 * 15.0 + 0.7 * (l0 + 0.8 * b0)
    b1 = 0.1 * (l1 - l0) + 0.9 * 0.8 * b0
    assert sse == pytest.approx(err * err)
    assert (level, trend) == pytest.approx((l1, b1))


def test_damped_trend_is_chosen_for_a_flattening_series_and_does_not_overshoot():
    y = damped_growth()
    fit = models.ets_fit(y, season=1)
    assert fit.variant == "A,Ad,N"
    assert 0.0 < fit.phi < 1.0
    path = fit.path(len(y), 24)
    # The undamped trend would keep climbing by the last step; the damped path flattens.
    steps = [b - a for a, b in zip(path, path[1:])]
    assert all(later <= earlier + 1e-9 for earlier, later in zip(steps, steps[1:]))
    assert path[-1] < y[-1] + 24 * (y[-1] - y[-2]) / 2


def test_undamped_trend_keeps_the_plain_model():
    y = [2.0 * t + 5.0 for t in range(40)]
    assert models.ets_fit(y).variant == "A,A,N"


def test_damped_path_sums_the_geometric_trend():
    fit = models._EtsFit("A,Ad,N", 0.0, 100.0, 10.0, (0.0,), 1, phi=0.5)
    assert fit.path(10, 3) == pytest.approx((105.0, 107.5, 108.75))


def test_api_detects_the_season_and_reports_it():
    body = {"series": [{"itemId": "i", "siteId": "s", "history": wave(12, 48), "horizonPeriods": 12, "autoSeason": True,
                        "seasonLength": 4}]}
    r = client.post("/v1/forecast/forecasts:generate", json=body)
    assert r.status_code == 200
    res = r.json()["results"][0]
    assert res["seasonLength"] == 12
    assert res["forecast"][0] == pytest.approx(240.0)
    assert res["forecast"][6] == pytest.approx(160.0)


def test_api_without_autoseason_uses_the_sent_length():
    body = {"series": [{"itemId": "i", "siteId": "s", "history": wave(12, 48), "seasonLength": 3}]}
    res = client.post("/v1/forecast/forecasts:generate", json=body).json()["results"][0]
    assert res["seasonLength"] == 3
