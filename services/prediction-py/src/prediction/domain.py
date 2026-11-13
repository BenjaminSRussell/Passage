"""Payment-date prediction (FR-47.15): features, training, promotion gate and scoring.

Pure functions and dataclasses; no I/O. The service layer adds the repository and the clock.
"""

from __future__ import annotations

import hashlib
import math
from dataclasses import dataclass, field
from datetime import date, datetime, timedelta, timezone

from prediction.gbt import Booster, fit_classifier, fit_regressor

# 9.47.20 parameters: prediction.payment.min_rows (100..10000, default 500) and
# prediction.payment.mae_max_days (1..30, default 6).
MIN_ROWS_DEFAULT = 500
MIN_ROWS_RANGE = (100, 10_000)
MAE_MAX_DAYS_DEFAULT = 6.0
MAE_MAX_DAYS_RANGE = (1.0, 30.0)
LATE_DAYS = 30
STALE_AFTER = timedelta(days=7)  # BR-47.12
HOLDOUT_EVERY = 5  # every 5th paid invoice (by due date order) is held out for the MAE

FEATURES = ["terms_days", "log_amount", "day_of_month", "dunning_count", "disputes", "month", "customer_mean_days_late"]

CODE_UNAVAILABLE = "AGT-0213"


class PredictionError(Exception):
    def __init__(self, code: str, message: str, status: int = 422) -> None:
        super().__init__(message)
        self.code = code
        self.status = status


@dataclass(frozen=True)
class Config:
    min_rows: int = MIN_ROWS_DEFAULT
    mae_max_days: float = MAE_MAX_DAYS_DEFAULT

    def __post_init__(self) -> None:
        lo, hi = MIN_ROWS_RANGE
        if not lo <= self.min_rows <= hi:
            raise ValueError(f"prediction.payment.min_rows must be {lo} to {hi}")
        lo_m, hi_m = MAE_MAX_DAYS_RANGE
        if not lo_m <= self.mae_max_days <= hi_m:
            raise ValueError(f"prediction.payment.mae_max_days must be {lo_m} to {hi_m}")


@dataclass(frozen=True)
class OpenInvoice:
    invoice_id: str
    customer_id: str
    amount: float
    invoice_date: date
    due_date: date
    dunning_count: int = 0
    disputes: int = 0

    @property
    def terms_days(self) -> int:
        return (self.due_date - self.invoice_date).days


@dataclass(frozen=True)
class PaidInvoice(OpenInvoice):
    paid_date: date = date(1970, 1, 1)

    @property
    def days_late(self) -> int:
        return (self.paid_date - self.due_date).days


@dataclass
class CustomerStats:
    total: float = 0.0
    n: int = 0


@dataclass
class Model:
    model_version: str
    trained_at: datetime
    rows: int
    mae_days: float
    status: str  # CANDIDATE | ACTIVE | RETIRED
    regressor: Booster
    classifier: Booster
    customers: dict[str, CustomerStats]
    global_mean: float
    min_rows: int
    mae_max_days: float
    features: list[str] = field(default_factory=lambda: list(FEATURES))


def model_artifact(m: Model) -> dict[str, object]:
    """Everything scoring needs, as JSON (prediction_model.artifact)."""
    return {
        "features": list(m.features),
        "regressor": m.regressor.to_dict(),
        "classifier": m.classifier.to_dict(),
        "customers": {k: [s.total, s.n] for k, s in sorted(m.customers.items())},
        "global_mean": m.global_mean,
    }


def model_from_artifact(
    artifact: dict[str, object], *, model_version: str, trained_at: datetime, rows: int, mae_days: float, status: str, min_rows: int, mae_max_days: float
) -> Model:
    features = list(artifact["features"])  # type: ignore[call-overload]
    if features != FEATURES:
        raise ValueError(f"model {model_version} was trained on features {features}, this build scores {FEATURES}")
    customers = {k: CustomerStats(float(v[0]), int(v[1])) for k, v in artifact["customers"].items()}  # type: ignore[union-attr]
    return Model(
        model_version=model_version,
        trained_at=trained_at,
        rows=rows,
        mae_days=mae_days,
        status=status,
        regressor=Booster.from_dict(artifact["regressor"]),  # type: ignore[arg-type]
        classifier=Booster.from_dict(artifact["classifier"]),  # type: ignore[arg-type]
        customers=customers,
        global_mean=float(artifact["global_mean"]),  # type: ignore[arg-type]
        min_rows=min_rows,
        mae_max_days=mae_max_days,
        features=features,
    )


def _vector(inv: OpenInvoice, customer_mean: float) -> list[float]:
    return [
        float(inv.terms_days),
        math.log1p(max(inv.amount, 0.0)),
        float(inv.invoice_date.day),
        float(inv.dunning_count),
        float(inv.disputes),
        float(inv.due_date.month),
        customer_mean,
    ]


def features_hash(x: list[float]) -> str:
    return hashlib.sha256(",".join(f"{v:.6f}" for v in x).encode()).hexdigest()[:32]


def _stats(rows: list[PaidInvoice]) -> tuple[dict[str, CustomerStats], float]:
    stats: dict[str, CustomerStats] = {}
    for r in rows:
        s = stats.setdefault(r.customer_id, CustomerStats())
        s.total += r.days_late
        s.n += 1
    mean = sum(r.days_late for r in rows) / len(rows)
    return stats, mean


def _customer_mean(stats: dict[str, CustomerStats], customer: str, global_mean: float, exclude: float | None = None) -> float:
    s = stats.get(customer)
    if s is None:
        return global_mean
    total, n = s.total, s.n
    if exclude is not None:  # leave-one-out so a training row never sees its own label
        total, n = total - exclude, n - 1
    return global_mean if n <= 0 else total / n


def _fit(rows: list[PaidInvoice]) -> tuple[Booster, Booster, dict[str, CustomerStats], float]:
    stats, gmean = _stats(rows)
    X = [_vector(r, _customer_mean(stats, r.customer_id, gmean, exclude=r.days_late)) for r in rows]
    reg = fit_regressor(X, [float(r.days_late) for r in rows])
    clf = fit_classifier(X, [1 if r.days_late > LATE_DAYS else 0 for r in rows])
    return reg, clf, stats, gmean


def train(paid: list[PaidInvoice], config: Config, now: datetime, version_seq: int) -> Model:
    """Weekly retrain on paid invoices.

    Below `min_rows` there is no model (AGT-0213). The MAE is measured on a held-out fifth of the
    invoices that the fitting never saw; the published model is then refit on all rows. A model
    whose MAE is above `mae_max_days` stays CANDIDATE and is never used to score.
    """
    if len(paid) < config.min_rows:
        raise PredictionError(
            CODE_UNAVAILABLE,
            f"Payment prediction not available: fewer than {config.min_rows} paid invoices ({len(paid)} given).",
        )
    ordered = sorted(paid, key=lambda r: (r.due_date, r.invoice_id))
    holdout = [r for i, r in enumerate(ordered) if i % HOLDOUT_EVERY == HOLDOUT_EVERY - 1]
    fit_rows = [r for i, r in enumerate(ordered) if i % HOLDOUT_EVERY != HOLDOUT_EVERY - 1]
    reg, _clf, stats, gmean = _fit(fit_rows)
    errors = [abs(reg.predict(_vector(r, _customer_mean(stats, r.customer_id, gmean))) - r.days_late) for r in holdout]
    mae = sum(errors) / len(errors)
    reg, clf, stats, gmean = _fit(ordered)
    status = "ACTIVE" if mae <= config.mae_max_days else "CANDIDATE"
    return Model(
        model_version=f"pm-{now.strftime('%Y%m%d')}-{version_seq}",
        trained_at=now,
        rows=len(ordered),
        mae_days=round(mae, 3),
        status=status,
        regressor=reg,
        classifier=clf,
        customers=stats,
        global_mean=gmean,
        min_rows=config.min_rows,
        mae_max_days=config.mae_max_days,
    )


@dataclass(frozen=True)
class Prediction:
    invoice_id: str
    model_version: str
    predicted_payment_date: date
    p_late_30d: str  # numeric(5,4) as text, never a float on the wire
    features_hash: str
    scored_at: datetime
    due_date: date | None = None  # kept so the accuracy report can tell late from on time


def score(model: Model, invoices: list[OpenInvoice], now: datetime) -> list[Prediction]:
    out: list[Prediction] = []
    for inv in invoices:
        x = _vector(inv, _customer_mean(model.customers, inv.customer_id, model.global_mean))
        days_late = round(model.regressor.predict(x))
        p = min(max(model.classifier.predict(x), 0.0), 1.0)
        out.append(
            Prediction(
                invoice_id=inv.invoice_id,
                model_version=model.model_version,
                predicted_payment_date=inv.due_date + timedelta(days=days_late),
                p_late_30d=f"{p:.4f}",
                features_hash=features_hash(x),
                scored_at=now,
                due_date=inv.due_date,
            )
        )
    return out


def is_stale(scored_at: datetime, now: datetime) -> bool:
    """BR-47.12: a prediction older than 7 days is shown with a stale badge."""
    return now - scored_at > STALE_AFTER


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


CALIBRATION_BUCKETS = (0.0, 0.2, 0.4, 0.6, 0.8, 1.0)
WITHIN_DAYS = 7


@dataclass(frozen=True)
class Outcome:
    """A stored prediction whose invoice has since been paid."""

    model_version: str
    predicted_payment_date: date
    p_late_30d: str
    due_date: date
    paid_date: date


def _ratio(n: int, d: int) -> str | None:
    return None if d == 0 else f"{n / d:.4f}"


def accuracy_report(outcomes: list[Outcome]) -> dict[str, object]:
    """The prediction accuracy report (FR-47.15, 9.47.15): MAE in days between predicted and actual payment date,
    the share within 7 days, and p_late_30d calibration (mean predicted probability against the observed late
    rate per probability bucket), overall and per model version. Ratios are decimal text, never floats."""

    def summary(rows: list[Outcome]) -> dict[str, object]:
        errors = [abs((o.paid_date - o.predicted_payment_date).days) for o in rows]
        buckets = []
        for lo, hi in zip(CALIBRATION_BUCKETS, CALIBRATION_BUCKETS[1:]):
            inside = [o for o in rows if lo <= float(o.p_late_30d) < hi or (hi == 1.0 and float(o.p_late_30d) == 1.0)]
            late = sum(1 for o in inside if (o.paid_date - o.due_date).days > LATE_DAYS)
            buckets.append(
                {
                    "from": f"{lo:.1f}",
                    "to": f"{hi:.1f}",
                    "count": len(inside),
                    "meanPLate30d": None if not inside else f"{sum(float(o.p_late_30d) for o in inside) / len(inside):.4f}",
                    "observedLateRate": _ratio(late, len(inside)),
                }
            )
        return {
            "count": len(rows),
            "maeDays": None if not rows else f"{sum(errors) / len(rows):.2f}",
            "withinDays": WITHIN_DAYS,
            "withinShare": _ratio(sum(1 for e in errors if e <= WITHIN_DAYS), len(rows)),
            "calibration": buckets,
        }

    versions = sorted({o.model_version for o in outcomes})
    return {
        **summary(outcomes),
        "byModel": [{"modelVersion": v, **summary([o for o in outcomes if o.model_version == v])} for v in versions],
    }
