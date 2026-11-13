"""Postgres repository (migrations/0001_prediction.sql). Every statement runs in a transaction that sets
passage.tenant_id, so row-level security answers only the caller's tenant whatever the SQL says."""

from __future__ import annotations

import os
import uuid
from contextlib import contextmanager
from decimal import Decimal
from pathlib import Path
from typing import Iterator

import psycopg
from psycopg.types.json import Jsonb

from prediction.domain import Config, Model, Outcome, PaidInvoice, Prediction, PredictionError, model_artifact, model_from_artifact
from prediction.service import SCORED_EVENT, scored_event

# The image copies the migrations to /app/migrations (Dockerfile); a checkout reads them from the service directory.
MIGRATIONS_DIR = Path(os.environ.get("PREDICTION_MIGRATIONS_DIR") or Path(__file__).resolve().parents[2] / "migrations")


def migrate(url: str, directory: Path = MIGRATIONS_DIR) -> None:
    """Applies the migrations not yet in schema_migrations, each in its own transaction (dbx.Migrate contract).
    Run as the owner role."""
    with psycopg.connect(url, autocommit=True) as conn:
        conn.execute("create table if not exists schema_migrations (name text primary key, applied_at timestamptz not null default now())")
        for path in sorted(directory.glob("*.sql")):
            if conn.execute("select 1 from schema_migrations where name = %s", (path.name,)).fetchone():
                continue
            with conn.transaction():
                conn.execute(path.read_text(encoding="utf-8"))  # type: ignore[arg-type]
                conn.execute("insert into schema_migrations (name) values (%s)", (path.name,))


class PgRepository:
    def __init__(self, url: str) -> None:
        self.url = url

    @contextmanager
    def _tenant(self, tenant_id: str) -> Iterator[psycopg.Connection]:
        str(uuid.UUID(tenant_id))  # never set a malformed tenant
        with psycopg.connect(self.url) as conn:
            with conn.transaction():
                conn.execute("select set_config('passage.tenant_id', %s, true)", (tenant_id,))
                yield conn

    def ping(self) -> None:
        with psycopg.connect(self.url, connect_timeout=2) as conn:
            conn.execute("select 1")

    def models(self, tenant_id: str) -> list[Model]:
        with self._tenant(tenant_id) as c:
            rows = c.execute(
                "select model_version, trained_at, rows, mae_days, status, min_rows, mae_max_days, artifact from prediction_model order by trained_at, model_version"
            ).fetchall()
        return [self._model(r) for r in rows]

    def model_count(self, tenant_id: str) -> int:
        with self._tenant(tenant_id) as c:
            return int(c.execute("select count(*) from prediction_model").fetchone()[0])  # type: ignore[index]

    def active_model(self, tenant_id: str) -> Model | None:
        with self._tenant(tenant_id) as c:
            row = c.execute(
                "select model_version, trained_at, rows, mae_days, status, min_rows, mae_max_days, artifact from prediction_model where status = 'ACTIVE'"
            ).fetchone()
        return None if row is None else self._model(row)

    @staticmethod
    def _model(r: tuple) -> Model:  # type: ignore[type-arg]
        version, trained_at, rows, mae, status, min_rows, mae_max, artifact = r
        return model_from_artifact(
            artifact, model_version=version, trained_at=trained_at, rows=rows, mae_days=float(mae), status=status, min_rows=min_rows, mae_max_days=float(mae_max)
        )

    def save_model(self, tenant_id: str, model: Model) -> None:
        try:
            with self._tenant(tenant_id) as c:
                # One retrain at a time per tenant, so the ACTIVE swap and the version number never race.
                c.execute("select pg_advisory_xact_lock(hashtextextended('prediction_model:' || %s, 0))", (tenant_id,))
                if model.status == "ACTIVE":
                    c.execute("update prediction_model set status = 'RETIRED' where status = 'ACTIVE'")
                c.execute(
                    """insert into prediction_model (tenant_id, model_version, trained_at, rows, mae_days, status, min_rows, mae_max_days, artifact)
                       values (%s, %s, %s, %s, %s, %s, %s, %s, %s)""",
                    (tenant_id, model.model_version, model.trained_at, model.rows, Decimal(str(model.mae_days)), model.status, model.min_rows, Decimal(str(model.mae_max_days)), Jsonb(model_artifact(model))),
                )
        except psycopg.errors.UniqueViolation as exc:
            raise PredictionError("AGT-0409", f"model {model.model_version} was stored by a concurrent retrain; retrain again", 409) from exc

    def save_predictions(self, tenant_id: str, predictions: list[Prediction]) -> None:
        """Upserts the predictions and writes one prediction.payment.scored outbox row each, in one transaction."""
        with self._tenant(tenant_id) as c:
            with c.cursor() as cur:
                cur.executemany(
                    """insert into payment_prediction (tenant_id, invoice_id, model_version, predicted_payment_date, p_late_30d, features_hash, due_date, scored_at)
                       values (%s, %s, %s, %s, %s, %s, %s, %s)
                       on conflict (tenant_id, invoice_id) do update set model_version = excluded.model_version,
                         predicted_payment_date = excluded.predicted_payment_date, p_late_30d = excluded.p_late_30d,
                         features_hash = excluded.features_hash, due_date = excluded.due_date, scored_at = excluded.scored_at,
                         actual_paid_date = null""",
                    [(tenant_id, p.invoice_id, p.model_version, p.predicted_payment_date, Decimal(p.p_late_30d), p.features_hash, p.due_date, p.scored_at) for p in predictions],
                )
                cur.executemany(
                    "insert into outbox (id, tenant_id, aggregate_type, aggregate_id, type, payload) values (%s, %s, 'payment_prediction', %s, %s, %s)",
                    [(str(uuid.uuid4()), tenant_id, p.invoice_id, SCORED_EVENT, Jsonb(scored_event(p))) for p in predictions],
                )

    def prediction(self, tenant_id: str, invoice_id: str) -> Prediction | None:
        with self._tenant(tenant_id) as c:
            r = c.execute(
                "select invoice_id, model_version, predicted_payment_date, p_late_30d, features_hash, scored_at, due_date from payment_prediction where invoice_id = %s",
                (invoice_id,),
            ).fetchone()
        if r is None:
            return None
        return Prediction(r[0], r[1], r[2], f"{r[3]:.4f}", r[4], r[5], r[6])

    def settings(self, tenant_id: str) -> Config | None:
        with self._tenant(tenant_id) as c:
            r = c.execute("select min_rows, mae_max_days from prediction_settings").fetchone()
        return None if r is None else Config(min_rows=r[0], mae_max_days=float(r[1]))

    def save_settings(self, tenant_id: str, config: Config, user_id: str) -> None:
        with self._tenant(tenant_id) as c:
            c.execute(
                """insert into prediction_settings (tenant_id, min_rows, mae_max_days, updated_by) values (%s, %s, %s, %s)
                   on conflict (tenant_id) do update set min_rows = excluded.min_rows, mae_max_days = excluded.mae_max_days,
                     updated_by = excluded.updated_by, updated_at = now()""",
                (tenant_id, config.min_rows, Decimal(str(config.mae_max_days)), user_id),
            )

    def record_actuals(self, tenant_id: str, paid: list[PaidInvoice]) -> int:
        """Stamps the paid date on each stored prediction of a paid invoice, once."""
        if not paid:
            return 0
        with self._tenant(tenant_id) as c:
            res = c.execute(
                """update payment_prediction p set actual_paid_date = v.paid_date
                     from (select * from unnest(%s::text[], %s::date[]) as t(invoice_id, paid_date)) v
                    where p.invoice_id = v.invoice_id and p.actual_paid_date is null""",
                ([i.invoice_id for i in paid], [i.paid_date for i in paid]),
            )
            return res.rowcount

    def outcomes(self, tenant_id: str) -> list[Outcome]:
        with self._tenant(tenant_id) as c:
            rows = c.execute(
                "select model_version, predicted_payment_date, p_late_30d, due_date, actual_paid_date from payment_prediction where actual_paid_date is not null order by invoice_id"
            ).fetchall()
        return [Outcome(r[0], r[1], f"{r[2]:.4f}", r[3], r[4]) for r in rows]

