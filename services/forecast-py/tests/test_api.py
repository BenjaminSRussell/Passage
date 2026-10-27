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


def test_history_months_models_only_the_window():
    # Two histories that differ only before the 24-month window forecast alike;
    # without the window the old level shows through.
    tail = [float(10 + (i % 3)) for i in range(24)]
    high = {"itemId": "I-1", "siteId": "S-1", "history": [500.0] * 24 + tail, "historyMonths": 24, "horizonPeriods": 2}
    low = {**high, "itemId": "I-2", "history": [1.0] * 24 + tail}
    unwindowed = {k: v for k, v in high.items() if k != "historyMonths"} | {"itemId": "I-3"}
    r = client.post("/v1/forecast/forecasts:generate", json={"series": [high, low, unwindowed]})
    assert r.status_code == 200
    res = r.json()["results"]
    assert res[0]["forecast"] == res[1]["forecast"]
    assert res[0]["shortHistory"] is False and res[0]["warnings"] == []
    assert res[2]["forecast"] != res[0]["forecast"]


def test_history_window_below_the_model_minimum_falls_back_to_naive():
    # A 12-month window leaves less than the 24 months ETS needs (PLAN-0012).
    body = {"series": [{"itemId": "I-1", "siteId": "S-1", "history": [10.0] * 36, "historyMonths": 12, "horizonPeriods": 2}]}
    res = client.post("/v1/forecast/forecasts:generate", json=body).json()["results"][0]
    assert res["model"] == "naive"
    assert res["shortHistory"] is True
    assert res["warnings"] == ["PLAN-0012"]


@pytest.mark.parametrize("months", [6, 11, 121])
def test_history_months_out_of_range_is_rejected(months):
    body = {"series": [{"itemId": "I-1", "siteId": "S-1", "history": [10.0] * 30, "historyMonths": months}]}
    assert client.post("/v1/forecast/forecasts:generate", json=body).status_code == 422


def _series(i: int) -> dict:
    return {"itemId": f"I-{i}", "siteId": "S-1", "history": [10.0 + (i % 3)] * 24, "horizonPeriods": 2}


def test_generate_batches_cuts_by_batch_size_and_reports_complete():
    body = {"series": [_series(i) for i in range(5)], "batchSize": 2}
    r = client.post("/v1/forecast/forecasts:generate-batches", json=body)
    assert r.status_code == 200
    out = r.json()
    assert out["totalBatches"] == 3
    assert out["complete"] is True
    assert [b["resultCount"] for b in out["batches"]] == [2, 2, 1]
    assert all(b["status"] == "completed" for b in out["batches"])
    assert [res["itemId"] for b in out["batches"] for res in b["results"]] == [f"I-{i}" for i in range(5)]


def test_generate_batches_results_equal_the_single_call():
    series = [_series(i) for i in range(3)]
    single = client.post("/v1/forecast/forecasts:generate", json={"series": series}).json()["results"]
    out = client.post("/v1/forecast/forecasts:generate-batches", json={"series": series, "batchSize": 2}).json()
    assert [res for b in out["batches"] for res in b["results"]] == single


def test_generate_batches_skips_completed_batches():
    body = {"series": [_series(i) for i in range(4)], "batchSize": 2, "completedBatches": [0]}
    out = client.post("/v1/forecast/forecasts:generate-batches", json=body).json()
    assert [b["status"] for b in out["batches"]] == ["skipped", "completed"]
    assert out["batches"][0]["results"] == []
    assert [res["itemId"] for res in out["batches"][1]["results"]] == ["I-2", "I-3"]
    assert out["complete"] is True


def test_generate_batches_failed_batch_marks_run_incomplete(monkeypatch):
    real = models.select_model

    def flaky(history, model, **kw):
        if history[0] == 11.0:  # the history of I-1
            raise ValueError("model did not converge")
        return real(history, model, **kw)

    monkeypatch.setattr(models, "select_model", flaky)
    body = {"series": [_series(0), _series(1), _series(2)], "batchSize": 1}
    out = client.post("/v1/forecast/forecasts:generate-batches", json=body).json()
    assert [b["status"] for b in out["batches"]] == ["completed", "failed", "completed"]
    assert out["batches"][1]["detail"] == "ValueError: model did not converge"
    assert out["batches"][1]["results"] == []
    assert out["complete"] is False


@pytest.mark.parametrize(
    "body",
    [
        {"series": [_series(0)], "batchSize": 1001},
        {"series": [_series(0)], "batchSize": 0},
        {"series": []},
        {"series": [_series(0)], "completedBatches": [-1]},
        {"series": [_series(0), _series(1)], "batchSize": 1, "completedBatches": [2]},
        {"series": [{"itemId": "I-1", "siteId": "S-1", "history": [1, -2, 3]}]},
    ],
)
def test_generate_batches_rejects_invalid_requests(body):
    assert client.post("/v1/forecast/forecasts:generate-batches", json=body).status_code == 422
