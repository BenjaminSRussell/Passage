"""Tenant-scoped use cases over a repository port. Production uses the Postgres repository (store.py); the
in-memory one below serves tests and a process started without DATABASE_URL."""

from __future__ import annotations

from dataclasses import replace
from datetime import datetime
from typing import Callable, Protocol

from prediction.domain import (
    CODE_UNAVAILABLE,
    Config,
    Model,
    OpenInvoice,
    Outcome,
    PaidInvoice,
    Prediction,
    PredictionError,
    accuracy_report,
    is_stale,
    score,
    train,
    utcnow,
)


class Repository(Protocol):
    """Every call carries the tenant; an implementation must never answer across tenants."""

    def models(self, tenant_id: str) -> list[Model]: ...
    def model_count(self, tenant_id: str) -> int: ...
    def active_model(self, tenant_id: str) -> Model | None: ...
    def save_model(self, tenant_id: str, model: Model) -> None: ...
    def save_predictions(self, tenant_id: str, predictions: list[Prediction]) -> None: ...
    def prediction(self, tenant_id: str, invoice_id: str) -> Prediction | None: ...
    def settings(self, tenant_id: str) -> Config | None: ...
    def save_settings(self, tenant_id: str, config: Config, user_id: str) -> None: ...
    def record_actuals(self, tenant_id: str, paid: list[PaidInvoice]) -> int: ...
    def outcomes(self, tenant_id: str) -> list[Outcome]: ...


class InMemoryRepository:
    def __init__(self) -> None:
        self._models: dict[str, list[Model]] = {}
        self._predictions: dict[str, dict[str, Prediction]] = {}
        self._settings: dict[str, Config] = {}
        self._paid: dict[str, dict[str, PaidInvoice]] = {}
        # prediction.payment.scored events, as the Postgres repository writes them to the outbox.
        self.events: list[tuple[str, dict[str, object]]] = []

    def models(self, tenant_id: str) -> list[Model]:
        return list(self._models.get(tenant_id, []))

    def model_count(self, tenant_id: str) -> int:
        return len(self._models.get(tenant_id, []))

    def active_model(self, tenant_id: str) -> Model | None:
        return next((m for m in self._models.get(tenant_id, []) if m.status == "ACTIVE"), None)

    def save_model(self, tenant_id: str, model: Model) -> None:
        stored = self._models.setdefault(tenant_id, [])
        if model.status == "ACTIVE":
            for m in stored:
                if m.status == "ACTIVE":
                    m.status = "RETIRED"
        stored.append(model)

    def save_predictions(self, tenant_id: str, predictions: list[Prediction]) -> None:
        bucket = self._predictions.setdefault(tenant_id, {})
        for p in predictions:
            bucket[p.invoice_id] = p
            self._paid.get(tenant_id, {}).pop(p.invoice_id, None)  # rescored: the old outcome no longer applies
            self.events.append((tenant_id, scored_event(p)))

    def prediction(self, tenant_id: str, invoice_id: str) -> Prediction | None:
        return self._predictions.get(tenant_id, {}).get(invoice_id)

    def settings(self, tenant_id: str) -> Config | None:
        return self._settings.get(tenant_id)

    def save_settings(self, tenant_id: str, config: Config, user_id: str) -> None:
        self._settings[tenant_id] = config

    def record_actuals(self, tenant_id: str, paid: list[PaidInvoice]) -> int:
        stored = self._predictions.get(tenant_id, {})
        seen = self._paid.setdefault(tenant_id, {})
        n = 0
        for inv in paid:
            if inv.invoice_id in stored and inv.invoice_id not in seen:
                seen[inv.invoice_id] = inv
                n += 1
        return n

    def outcomes(self, tenant_id: str) -> list[Outcome]:
        stored = self._predictions.get(tenant_id, {})
        out = []
        for invoice_id, inv in sorted(self._paid.get(tenant_id, {}).items()):
            p = stored[invoice_id]
            out.append(Outcome(p.model_version, p.predicted_payment_date, p.p_late_30d, p.due_date or inv.due_date, inv.paid_date))
        return out


SCORED_EVENT = "dev.passage.prediction.payment.scored.v1"


def scored_event(p: Prediction) -> dict[str, object]:
    """prediction.payment.scored payload: the prediction only, no customer or amount (events/asyncapi.yaml)."""
    return {
        "invoice_id": p.invoice_id,
        "model_version": p.model_version,
        "predicted_payment_date": p.predicted_payment_date.isoformat(),
        "p_late_30d": p.p_late_30d,
        "scored_at": p.scored_at.isoformat(),
    }


class PredictionService:
    def __init__(self, repo: Repository, config: Config | None = None, clock: Callable[[], datetime] = utcnow) -> None:
        self.repo = repo
        self.config = config or Config()
        self.clock = clock

    def active_model(self, tenant_id: str) -> Model | None:
        return self.repo.active_model(tenant_id)

    def config_for(self, tenant_id: str) -> Config:
        """The tenant's prediction.payment.* settings, else the service defaults (9.47.20)."""
        return self.repo.settings(tenant_id) or self.config

    def save_settings(self, tenant_id: str, user_id: str, min_rows: int | None, mae_max_days: float | None) -> Config:
        current = self.config_for(tenant_id)
        updated = replace(
            current,
            **({"min_rows": min_rows} if min_rows is not None else {}),
            **({"mae_max_days": mae_max_days} if mae_max_days is not None else {}),
        )
        self.repo.save_settings(tenant_id, updated, user_id)
        return updated

    def retrain(self, tenant_id: str, paid: list[PaidInvoice]) -> Model:
        """A model that passes the MAE gate becomes ACTIVE and retires the previous one; one that
        does not is stored as CANDIDATE and the previous ACTIVE model keeps serving. Paid invoices
        that had a stored prediction are recorded as its outcome first (the accuracy report)."""
        self.repo.record_actuals(tenant_id, paid)
        model = train(paid, self.config_for(tenant_id), self.clock(), self.repo.model_count(tenant_id) + 1)
        self.repo.save_model(tenant_id, model)
        return model

    def accuracy(self, tenant_id: str) -> dict[str, object]:
        return accuracy_report(self.repo.outcomes(tenant_id))

    def score(self, tenant_id: str, invoices: list[OpenInvoice]) -> list[Prediction]:
        model = self.active_model(tenant_id)
        if model is None:
            cfg = self.config_for(tenant_id)
            raise PredictionError(
                CODE_UNAVAILABLE,
                f"Payment prediction not available: no active model; it needs at least {cfg.min_rows} paid invoices and a model within {cfg.mae_max_days} days MAE.",
            )
        predictions = score(model, invoices, self.clock())
        self.repo.save_predictions(tenant_id, predictions)
        return predictions

    def prediction(self, tenant_id: str, invoice_id: str) -> tuple[Prediction, bool] | None:
        """The stored prediction and whether it is stale (BR-47.12)."""
        found = self.repo.prediction(tenant_id, invoice_id)
        if found is None:
            return None
        return found, is_stale(found.scored_at, self.clock())
