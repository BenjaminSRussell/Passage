import math

import pytest
from hypothesis import given, settings
from hypothesis import strategies as st

from forecast import models


@pytest.mark.parametrize(
    "y, expected",
    [
        ([0, 0, 5, 0, 5], 2.5),
        ([1, 1, 1, 1], 1.0),
        ([0, 0, 0], math.inf),
    ],
)
def test_adi(y, expected):
    assert models.adi(y) == pytest.approx(expected)


def test_croston_constant_intermittent_converges_to_size_over_interval():
    y = [0, 0, 4] * 10
    # size 4 every 3 periods -> 4/3 per period
    assert models.croston(y) == pytest.approx(4 / 3)


def test_croston_no_demand_is_zero():
    assert models.croston([0, 0, 0]) == 0.0


def test_tsb_no_demand_is_zero():
    assert models.tsb([0.0] * 10) == 0.0


def test_ses_constant_series_forecasts_constant():
    fc, alpha = models.ses([10.0] * 30)
    assert fc == pytest.approx(10.0)
    assert 0 < alpha <= 1


@pytest.mark.parametrize(
    "y, requested, model, short",
    [
        ([0, 0, 0, 9] * 10, "auto", "tsb", False),  # ADI 4 > 1.32 -> intermittent
        ([10] * 30, "auto", "ets", False),  # smooth
        ([1, 2, 3], "auto", "naive", True),  # too short
        ([10] * 30, "croston", "croston", False),
        ([10] * 30, "naive", "naive", False),
    ],
)
def test_select_model(y, requested, model, short):
    fc = models.select_model(y, requested)
    assert fc.model == model
    assert fc.short_history is short


def test_select_model_rejects_unknown_model():
    with pytest.raises(ValueError):
        models.select_model([1.0] * 30, "magic")


def test_mape_and_wmape():
    assert models.mape([10, 20], [9, 22]) == pytest.approx(0.1)
    assert models.wmape([10, 20], [9, 22]) == pytest.approx(3 / 30)


def test_error_metrics_undefined_for_zero_actuals():
    assert models.mape([0, 0], [1, 1]) is None
    assert models.wmape([0, 0], [1, 1]) is None


@settings(max_examples=200, deadline=None)
@given(st.lists(st.floats(min_value=0, max_value=1000, allow_nan=False), min_size=1, max_size=80))
def test_forecast_is_finite_and_non_negative(y):
    fc = models.select_model(y, "auto")
    assert math.isfinite(fc.value)
    assert fc.value >= 0


def test_backtest_scores_the_held_out_periods():
    # 24 smooth periods of 10 then a level of 10: perfect fit, zero error.
    assert models.backtest([10.0] * 24, "ets") == (pytest.approx(0.0), pytest.approx(0.0))
    # Holdout is the last 6 periods (24 // 4); naive on the training part forecasts 10.
    y = [10.0] * 18 + [20.0] * 6
    mape, wmape = models.backtest(y, "naive")
    assert mape == pytest.approx(0.5)
    assert wmape == pytest.approx(0.5)


def test_backtest_is_undefined_without_enough_history_or_nonzero_actuals():
    assert models.backtest([5.0, 6.0, 7.0], "naive") == (None, None)
    assert models.backtest([3.0] * 18 + [0.0] * 6, "naive") == (None, None)


def test_choose_rejects_unknown_model():
    with pytest.raises(ValueError):
        models.choose([1.0] * 30, "magic")
