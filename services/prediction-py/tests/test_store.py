"""Repository contract, run against the in-memory repository and, when PASSAGE_TEST_DATABASE_URL (owner) and
PASSAGE_TEST_APP_DATABASE_URL (passage_app) are set, against Postgres in a private schema."""

from __future__ import annotations

import os
import uuid
from datetime import date, datetime, timedelta, timezone

import pytest

from prediction import synth
from prediction.domain import Config, PaidInvoice, PredictionError
from prediction.service import SCORED_EVENT, InMemoryRepository, PredictionService

OWNER_URL = os.environ.get("PASSAGE_TEST_DATABASE_URL", "")
APP_URL = os.environ.get("PASSAGE_TEST_APP_DATABASE_URL", "") or OWNER_URL
T1 = "11111111-1111-1111-1111-111111111111"
T2 = "22222222-2222-2222-2222-222222222222"
USER = "33333333-3333-3333-3333-333333333333"
NOW = datetime(2026, 11, 13, 10, 0, tzinfo=timezone.utc)


@pytest.fixture(scope="module")
def pg_schema():
    if not OWNER_URL:
        pytest.skip("PASSAGE_TEST_DATABASE_URL is not set")
    psycopg = pytest.importorskip("psycopg")
    from psycopg.conninfo import make_conninfo

    from prediction.store import migrate

    schema = f"prediction_{uuid.uuid4().hex[:12]}"
    with psycopg.connect(OWNER_URL, autocommit=True) as admin:
        admin.execute(f"create schema {schema}")
    try:
        migrate(make_conninfo(OWNER_URL, options=f"-c search_path={schema}"))
        yield make_conninfo(APP_URL, options=f"-c search_path={schema}"), make_conninfo(OWNER_URL, options=f"-c search_path={schema}")
    finally:
        with psycopg.connect(OWNER_URL, autocommit=True) as admin:
            admin.execute(f"drop schema {schema} cascade")


@pytest.fixture(params=["memory", "postgres"])
def make_repo(request):
    """A factory: calling it again returns a fresh handle on the same store (a restart for Postgres)."""
    if request.param == "memory":
        repo = InMemoryRepository()
        return lambda: repo
    app_url, _owner = request.getfixturevalue("pg_schema")
    from prediction.store import PgRepository

    return lambda: PgRepository(app_url)


def tenant() -> str:
    return str(uuid.uuid4())


def svc(repo, **cfg) -> PredictionService:
    # mae_max_days 30 (the top of the range) keeps models of these small synthetic sets ACTIVE.
    return PredictionService(repo, Config(**{"min_rows": 100, "mae_max_days": 30.0, **cfg}), clock=lambda: NOW)


def test_models_and_predictions_survive_a_restart(make_repo):
    t = tenant()
    model = svc(make_repo()).retrain(t, synth.paid_invoices(600))
    assert model.status == "ACTIVE"
    open_invoices = synth.open_invoices(5)
    first = svc(make_repo()).score(t, open_invoices)
    restarted = svc(make_repo())
    active = restarted.active_model(t)
    assert active is not None and active.model_version == model.model_version
    # The stored model scores exactly as the trained one did.
    assert restarted.score(t, open_invoices) == first
    found = restarted.prediction(t, first[0].invoice_id)
    assert found is not None and found[0] == first[0] and found[1] is False


def test_a_passing_retrain_retires_the_previous_active_model_and_a_failing_one_stays_candidate(make_repo):
    t = tenant()
    s = svc(make_repo())
    m1 = s.retrain(t, synth.paid_invoices(400))
    m2 = svc(make_repo(), mae_max_days=1.0).retrain(t, synth.paid_invoices(400))
    assert m2.status == "CANDIDATE"
    assert s.active_model(t).model_version == m1.model_version
    m3 = s.retrain(t, synth.paid_invoices(500))
    assert [(m.model_version, m.status) for m in make_repo().models(t)] == [
        (m1.model_version, "RETIRED"),
        (m2.model_version, "CANDIDATE"),
        (m3.model_version, "ACTIVE"),
    ]


def test_settings_are_per_tenant_and_drive_the_next_retrain(make_repo):
    t, other = tenant(), tenant()
    s = svc(make_repo(), min_rows=500)
    assert s.config_for(t) == Config(min_rows=500, mae_max_days=30.0)
    s.save_settings(t, USER, 200, None)
    assert svc(make_repo(), min_rows=500).config_for(t) == Config(min_rows=200, mae_max_days=30.0)
    assert s.config_for(other) == Config(min_rows=500, mae_max_days=30.0)
    assert s.retrain(t, synth.paid_invoices(250)).min_rows == 200
    with pytest.raises(PredictionError) as err:
        s.retrain(other, synth.paid_invoices(250))
    assert err.value.code == "AGT-0213"
    s.save_settings(t, USER, None, 12.5)
    assert s.config_for(t) == Config(min_rows=200, mae_max_days=12.5)


def test_paid_invoices_become_the_outcomes_of_the_accuracy_report(make_repo):
    t = tenant()
    s = svc(make_repo())
    s.retrain(t, synth.paid_invoices(400))
    invoices = synth.open_invoices(4)
    preds = s.score(t, invoices)
    assert s.accuracy(t)["count"] == 0
    # Two of them are paid: one on the predicted day, one 10 days later and more than 30 days past due.
    on_time, late = invoices[0], invoices[1]
    paid = [
        PaidInvoice(on_time.invoice_id, on_time.customer_id, on_time.amount, on_time.invoice_date, on_time.due_date, 0, 0, preds[0].predicted_payment_date),
        PaidInvoice(late.invoice_id, late.customer_id, late.amount, late.invoice_date, late.due_date, 0, 0, max(preds[1].predicted_payment_date + timedelta(days=10), late.due_date + timedelta(days=31))),
    ]
    assert make_repo().record_actuals(t, paid) == 2
    assert make_repo().record_actuals(t, paid) == 0  # once
    report = svc(make_repo()).accuracy(t)
    assert report["count"] == 2 and report["withinDays"] == 7
    errors = [0, abs((paid[1].paid_date - preds[1].predicted_payment_date).days)]
    assert report["maeDays"] == f"{sum(errors) / 2:.2f}"
    assert report["withinShare"] == "0.5000"
    assert sum(b["count"] for b in report["calibration"]) == 2
    assert [m["modelVersion"] for m in report["byModel"]] == [preds[0].model_version]
    assert svc(make_repo()).accuracy(tenant())["count"] == 0


def test_tenants_never_see_each_other(make_repo):
    s = svc(make_repo())
    t1, t2 = tenant(), tenant()
    s.retrain(t1, synth.paid_invoices(300))
    p = s.score(t1, synth.open_invoices(1))[0]
    assert s.active_model(t2) is None
    assert s.prediction(t2, p.invoice_id) is None
    with pytest.raises(PredictionError):
        s.score(t2, synth.open_invoices(1))


def test_scoring_writes_one_scored_event_per_invoice_in_the_same_transaction(pg_schema):
    import psycopg

    from prediction.store import PgRepository

    app_url, owner_url = pg_schema
    t = tenant()
    s = svc(PgRepository(app_url))
    s.retrain(t, synth.paid_invoices(300))
    preds = s.score(t, synth.open_invoices(3))
    with psycopg.connect(owner_url) as c:
        rows = c.execute("select type, aggregate_id, payload from outbox where tenant_id = %s order by aggregate_id", (t,)).fetchall()
    assert [r[0] for r in rows] == [SCORED_EVENT] * 3
    by_invoice = {r[1]: r[2] for r in rows}
    p = preds[0]
    assert by_invoice[p.invoice_id] == {
        "invoice_id": p.invoice_id,
        "model_version": p.model_version,
        "predicted_payment_date": p.predicted_payment_date.isoformat(),
        "p_late_30d": p.p_late_30d,
        "scored_at": p.scored_at.isoformat(),
    }


def test_the_application_role_is_bound_by_row_level_security(pg_schema):
    import psycopg

    from prediction.store import PgRepository

    app_url, _owner = pg_schema
    t = tenant()
    svc(PgRepository(app_url)).retrain(t, synth.paid_invoices(300))
    with psycopg.connect(app_url) as c:
        # No tenant set: nothing is visible and nothing can be written.
        assert c.execute("select count(*) from prediction_model").fetchone()[0] == 0
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            c.execute("insert into prediction_settings (tenant_id, min_rows, mae_max_days, updated_by) values (%s, 100, 6, 'x')", (t,))
    with psycopg.connect(app_url) as c:
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            c.execute("delete from payment_prediction")


def test_events_in_memory_mirror_the_outbox():
    repo = InMemoryRepository()
    s = svc(repo)
    s.retrain(T1, synth.paid_invoices(300))
    preds = s.score(T1, synth.open_invoices(2))
    assert [(t, e["invoice_id"]) for t, e in repo.events] == [(T1, p.invoice_id) for p in preds]
    assert date.fromisoformat(repo.events[0][1]["predicted_payment_date"]) == preds[0].predicted_payment_date
