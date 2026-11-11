"""Core tests: pure standard library, no fastapi needed."""

import time
from datetime import date, datetime, timedelta, timezone

import pytest

from prediction import synth
from prediction.domain import Config, PredictionError, is_stale
from prediction.service import InMemoryRepository, PredictionService

NOW = datetime(2026, 11, 10, 9, 0, tzinfo=timezone.utc)
A, B = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"


def svc(**cfg) -> PredictionService:
    return PredictionService(InMemoryRepository(), Config(**cfg), clock=lambda: NOW)


def test_ac_47_12_two_thousand_paid_invoices_give_an_active_model_within_6_days():
    s = svc()
    m = s.retrain(A, synth.paid_invoices(2000))
    assert m.rows == 2000
    assert m.mae_days <= 6, m.mae_days
    assert m.status == "ACTIVE"
    assert s.active_model(A) is m
    # Beats the naive "everyone pays on the due date" baseline by a clear margin.
    held = synth.paid_invoices(2000, seed=48)[:300]
    naive = sum(abs(p.days_late) for p in held) / len(held)
    preds = {p.invoice_id: p for p in s.score(A, list(held))}
    model_mae = sum(abs((preds[p.invoice_id].predicted_payment_date - p.paid_date).days) for p in held) / len(held)
    assert model_mae < naive


def test_below_min_rows_there_is_no_model():
    s = svc()
    with pytest.raises(PredictionError) as e:
        s.retrain(A, synth.paid_invoices(499))
    assert e.value.code == "AGT-0213"
    assert "fewer than 500" in str(e.value)
    assert s.active_model(A) is None
    with pytest.raises(PredictionError) as e2:
        s.score(A, synth.open_invoices(3))
    assert e2.value.code == "AGT-0213"


def test_model_above_the_mae_threshold_stays_candidate_and_does_not_replace_the_active_one():
    s = svc()
    good = s.retrain(A, synth.paid_invoices(800))
    assert good.status == "ACTIVE"
    strict = PredictionService(s.repo, Config(mae_max_days=1.0), clock=lambda: NOW)
    cand = strict.retrain(A, synth.paid_invoices(800))
    assert cand.status == "CANDIDATE"
    assert strict.active_model(A) is good and good.status == "ACTIVE"


def test_a_passing_retrain_retires_the_previous_active_model():
    s = svc()
    first = s.retrain(A, synth.paid_invoices(600))
    second = s.retrain(A, synth.paid_invoices(700, seed=5))
    assert (first.status, second.status) == ("RETIRED", "ACTIVE")
    assert second.model_version != first.model_version


def test_config_ranges_follow_the_prd():
    for bad in ({"min_rows": 99}, {"min_rows": 10_001}, {"mae_max_days": 0.5}, {"mae_max_days": 31}):
        with pytest.raises(ValueError):
            Config(**bad)
    Config(min_rows=100, mae_max_days=30)


def test_scoring_shape_and_determinism():
    s = svc()
    s.retrain(A, synth.paid_invoices(600))
    inv = synth.open_invoices(5)
    first = s.score(A, inv)
    again = s.score(A, inv)
    assert [(p.predicted_payment_date, p.p_late_30d, p.features_hash) for p in first] == [(p.predicted_payment_date, p.p_late_30d, p.features_hash) for p in again]
    for p, i in zip(first, inv):
        assert p.invoice_id == i.invoice_id and len(p.features_hash) == 32
        assert len(p.p_late_30d.split(".")[1]) == 4 and 0 <= float(p.p_late_30d) <= 1
        assert p.scored_at == NOW and p.model_version.startswith("pm-20261110-")


def test_p_late_30d_separates_chronic_late_payers():
    s = svc()
    s.retrain(A, synth.paid_invoices(2000))
    inv = synth.open_invoices(600)
    preds = {p.invoice_id: float(p.p_late_30d) for p in s.score(A, inv)}
    disputed = [preds[i.invoice_id] for i in inv if i.disputes]
    clean = [preds[i.invoice_id] for i in inv if not i.disputes and i.dunning_count == 0]
    assert disputed and clean
    assert sum(disputed) / len(disputed) > sum(clean) / len(clean)


def test_tenants_are_isolated():
    s = svc()
    s.retrain(A, synth.paid_invoices(600))
    assert s.active_model(B) is None
    with pytest.raises(PredictionError):
        s.score(B, synth.open_invoices(2))
    inv = synth.open_invoices(2)
    s.score(A, inv)
    assert s.prediction(A, inv[0].invoice_id) is not None
    assert s.prediction(B, inv[0].invoice_id) is None


def test_stale_after_seven_days():
    assert not is_stale(NOW, NOW + timedelta(days=7))
    assert is_stale(NOW, NOW + timedelta(days=7, seconds=1))
    s = svc()
    s.retrain(A, synth.paid_invoices(600))
    inv = synth.open_invoices(1)
    s.score(A, inv)
    s.clock = lambda: NOW + timedelta(days=8)
    _, stale = s.prediction(A, inv[0].invoice_id)
    assert stale is True


def test_scoring_a_thousand_invoices_is_fast():
    s = svc()
    s.retrain(A, synth.paid_invoices(600))
    inv = synth.open_invoices(1000)
    t0 = time.perf_counter()
    out = s.score(A, inv)
    assert len(out) == 1000
    assert time.perf_counter() - t0 < 5  # the PRD asks for 2k invoices/s; this is a loose CI bound


def test_unseen_customer_falls_back_to_the_global_mean():
    s = svc()
    s.retrain(A, synth.paid_invoices(600))
    from prediction.domain import OpenInvoice

    inv = OpenInvoice("X-1", "never-seen", 1200.0, date(2026, 7, 1), date(2026, 7, 31))
    [p] = s.score(A, [inv])
    assert p.predicted_payment_date >= date(2026, 7, 1)
