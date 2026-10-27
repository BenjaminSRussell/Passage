import pytest
from fastapi.testclient import TestClient

from forecast import models
from forecast.api import app

client = TestClient(app)


def test_healthz():
    r = client.get("/healthz")
    assert r.status_code == 200


def test_generate_returns_horizon_and_warning_for_short_history():
    body = {
        "series": [
            {"itemId": "I-1", "siteId": "S-1", "history": [1, 2, 3], "horizonPeriods": 4},
            {"itemId": "I-2", "siteId": "S-1", "history": [10] * 30, "horizonPeriods": 3},
        ]
    }
    r = client.post("/v1/forecast/forecasts:generate", json=body)
    assert r.status_code == 200
    res = r.json()["results"]
    assert res[0]["model"] == "naive"
    assert res[0]["shortHistory"] is True
    assert res[0]["warnings"] == ["PLAN-0012"]
    assert res[0]["forecast"] == [3.0] * 4
    assert res[1]["model"] == "ets"
    assert len(res[1]["forecast"]) == 3


def test_readyz():
    assert client.get("/readyz").status_code == 200


def test_ac_22_7_intermittent_history_picks_tsb_and_reports_mape():
    # 24 months with demand in 11 of them: ADI 24/11 = 2.18, the closest a
    # 24-month history gets to the AC's 2.1, and well above the 1.32 cut-off.
    history = [0, 7, 0, 5, 0, 3, 9, 0, 6, 0, 4, 0, 0, 8, 0, 5, 0, 7, 0, 0, 6, 0, 5, 0]
    assert 2.0 < models.adi(history) < 2.2
    body = {"series": [{"itemId": "I-9", "siteId": "S-1", "history": history, "horizonPeriods": 6}]}
    r = client.post("/v1/forecast/forecasts:generate", json=body)
    assert r.status_code == 200
    res = r.json()["results"][0]
    assert res["model"] == "tsb"
    assert res["shortHistory"] is False
    assert res["warnings"] == []
    assert res["mape"] is not None and res["mape"] >= 0
    assert res["wmape"] is not None and res["wmape"] >= 0
    assert len(res["forecast"]) == 6


@pytest.mark.parametrize(
    "series",
    [
        {"itemId": "I-1", "siteId": "S-1", "history": [1, -2, 3]},
        {"itemId": "I-1", "siteId": "S-1", "history": [1, 2], "model": "magic"},
        {"itemId": "I-1", "siteId": "S-1", "history": []},
        {"itemId": "I-1", "siteId": "S-1", "history": [1], "horizonPeriods": 0},
        {"itemId": "", "siteId": "S-1", "history": [1]},
    ],
)
def test_generate_rejects_invalid_series(series):
    r = client.post("/v1/forecast/forecasts:generate", json={"series": [series]})
    assert r.status_code == 422


def test_generate_rejects_empty_batch():
    assert client.post("/v1/forecast/forecasts:generate", json={"series": []}).status_code == 422
