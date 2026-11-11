"""Tenant-scoped use cases over a repository port. Persistence is a handoff: the repository is an
interface and the in-memory one below is what runs today."""

from __future__ import annotations

from datetime import datetime
from typing import Callable, Protocol

from prediction.domain import (
    CODE_UNAVAILABLE,
    Config,
    Model,
    OpenInvoice,
    PaidInvoice,
    Prediction,
    PredictionError,
    is_stale,
    score,
    train,
    utcnow,
)


class Repository(Protocol):
    """Every call carries the tenant; an implementation must never answer across tenants."""

    def models(self, tenant_id: str) -> list[Model]: ...
    def save_model(self, tenant_id: str, model: Model) -> None: ...
    def save_predictions(self, tenant_id: str, predictions: list[Prediction]) -> None: ...
    def prediction(self, tenant_id: str, invoice_id: str) -> Prediction | None: ...


class InMemoryRepository:
    def __init__(self) -> None:
        self._models: dict[str, list[Model]] = {}
        self._predictions: dict[str, dict[str, Prediction]] = {}

    def models(self, tenant_id: str) -> list[Model]:
        return list(self._models.get(tenant_id, []))

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

    def prediction(self, tenant_id: str, invoice_id: str) -> Prediction | None:
        return self._predictions.get(tenant_id, {}).get(invoice_id)


class PredictionService:
    def __init__(self, repo: Repository, config: Config | None = None, clock: Callable[[], datetime] = utcnow) -> None:
        self.repo = repo
        self.config = config or Config()
        self.clock = clock

    def active_model(self, tenant_id: str) -> Model | None:
        return next((m for m in self.repo.models(tenant_id) if m.status == "ACTIVE"), None)

    def retrain(self, tenant_id: str, paid: list[PaidInvoice]) -> Model:
        """A model that passes the MAE gate becomes ACTIVE and retires the previous one; one that
        does not is stored as CANDIDATE and the previous ACTIVE model keeps serving."""
        model = train(paid, self.config, self.clock(), len(self.repo.models(tenant_id)) + 1)
        self.repo.save_model(tenant_id, model)
        return model

    def score(self, tenant_id: str, invoices: list[OpenInvoice]) -> list[Prediction]:
        model = self.active_model(tenant_id)
        if model is None:
            raise PredictionError(
                CODE_UNAVAILABLE,
                f"Payment prediction not available: no active model; it needs at least {self.config.min_rows} paid invoices and a model within {self.config.mae_max_days} days MAE.",
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
