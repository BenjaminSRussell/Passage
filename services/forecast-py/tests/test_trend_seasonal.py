"""FR-22.9 slice 2: trend and seasonal ETS selected by AICc, path forecasts, backtest on the path."""

import math

import pytest
from fastapi.testclient import TestClient
from hypothesis import given, settings
from hypothesis import strategies as st

from forecast import models
from forecast.api import app

client = TestClient(app)


def square_wave(periods: int = 48) -> list[float]:
    """Seasonal demand with season 12: six periods at 130, six at 70."""
    return [100.0 + (30.0 if t % 12 < 6 else -30.0) for t in range(periods)]


def test_linear_trend_selects_the_trend_model_and_extrapolates():
    y = [2.0 * t + 5.0 for t in range(40)]
    assert models.ets_fit(y).variant == "A,A,N"
    fc = models.select_model(y, "auto", horizon=5)
    assert fc.model == "ets"
    assert fc.path == pytest.approx((85.0, 87.0, 89.0, 91.0, 93.0))  # t = 40 .. 44
    assert fc.value == fc.path[0]


def test_seasonal_pattern_is_captured_by_the_seasonal_model():
    assert models.ets_fit(square_wave(), season=12).variant == "A,A,A"
    fc = models.select_model(square_wave(), "auto", horizon=12, season=12)
    assert fc.path[0] == pytest.approx(130.0)  # period 48 is phase 0, a high period
    assert fc.path[6] == pytest.approx(70.0)  # phase 6 is a low period


def test_season_length_one_turns_the_seasonal_candidate_off():
    assert models.ets_fit(square_wave(), season=1).variant != "A,A,A"


def test_history_shorter_than_two_seasons_and_one_period_is_not_seasonal():
    assert models.ets_fit(square_wave(24), season=12).variant != "A,A,A"  # 24 < 2 * 12 + 1
    # Eligible from 25 periods, but AICc only prefers its 17 parameters once
    # there are enough residuals to pay for them (three seasons here).
    assert models.ets_fit(square_wave(36), season=12).variant == "A,A,A"


def test_flat_series_keeps_the_simplest_model():
    fit = models.ets_fit([10.0] * 30)
    assert fit.variant == "A,N"
    assert fit.path(30, 3) == pytest.approx((10.0, 10.0, 10.0))


def test_falling_trend_is_clipped_at_zero():
    y = [100.0 - 4.0 * t for t in range(25)]  # reaches 4 at t = 24, negative from t = 26
    path = models.fit(y, "ets", horizon=6)
    assert path[0] == pytest.approx(0.0)
    assert min(path) == 0.0


def test_path_length_matches_horizon_for_every_model():
    y = [10.0] * 30
    for name in ("auto", "ets", "croston", "tsb", "naive"):
        assert len(models.select_model(y, name, horizon=7).path) == 7


def test_horizon_and_season_must_be_positive():
    with pytest.raises(ValueError):
        models.select_model([1.0] * 30, "auto", horizon=0)
    with pytest.raises(ValueError):
        models.select_model([1.0] * 30, "auto", season=0)


def test_backtest_scores_the_trend_path_not_a_flat_value():
    y = [float(t + 1) for t in range(40)]  # exact ramp: the trend model forecasts the holdout exactly
    mape, wmape = models.backtest(y, "ets")
    assert mape == pytest.approx(0.0, abs=1e-6)
    assert wmape == pytest.approx(0.0, abs=1e-6)
    flat_mape, _ = models.backtest(y, "naive")
    assert flat_mape > 0.1  # a flat forecast of the same holdout is far off


@settings(max_examples=100, deadline=None)
@given(
    st.lists(st.floats(min_value=0, max_value=1000, allow_nan=False), min_size=1, max_size=80),
    st.integers(min_value=1, max_value=24),
)
def test_ets_path_is_finite_and_non_negative(y, season):
    path = models.fit(y, "ets", horizon=6, season=season)
    assert len(path) == 6
    assert all(math.isfinite(v) and v >= 0 for v in path)


def test_api_returns_the_seasonal_path_and_error_metrics():
    body = {
        "series": [
            {"itemId": "I-3", "siteId": "S-1", "history": square_wave(), "horizonPeriods": 12, "seasonLength": 12},
            {"itemId": "I-4", "siteId": "S-1", "history": square_wave(), "horizonPeriods": 12, "seasonLength": 1},
        ]
    }
    r = client.post("/v1/forecast/forecasts:generate", json=body)
    assert r.status_code == 200
    seasonal, flat = r.json()["results"]
    assert seasonal["forecast"][0] == pytest.approx(130.0)
    assert seasonal["forecast"][6] == pytest.approx(70.0)
    assert seasonal["mape"] == pytest.approx(0.0, abs=1e-9)
    # Without seasonality the same history cannot be forecast exactly.
    assert flat["mape"] > 0.05


def test_api_rejects_season_length_out_of_range():
    body = {"series": [{"itemId": "I-1", "siteId": "S-1", "history": [1.0] * 30, "seasonLength": 25}]}
    assert client.post("/v1/forecast/forecasts:generate", json=body).status_code == 422
