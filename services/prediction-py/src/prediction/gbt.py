"""A small gradient-boosted tree implementation in the standard library (FR-47.15).

Two heads share one tree builder: squared-loss regression (days late) and a logistic
classifier (paid more than 30 days late). Splits are chosen on quantile bins of each
feature, so a fit is O(rows x features x depth) per tree. Everything is deterministic:
there is no sampling, so the same data gives the same model.
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field

MAX_BINS = 24
MIN_LEAF = 5


@dataclass
class Node:
    feature: int = -1
    threshold: float = 0.0
    left: "Node | None" = None
    right: "Node | None" = None
    value: float = 0.0

    def predict(self, x: list[float]) -> float:
        node = self
        while node.left is not None and node.right is not None:
            node = node.left if x[node.feature] <= node.threshold else node.right
        return node.value

    def to_dict(self) -> dict[str, object]:
        if self.left is None or self.right is None:
            return {"v": self.value}
        return {"f": self.feature, "t": self.threshold, "l": self.left.to_dict(), "r": self.right.to_dict()}

    @staticmethod
    def from_dict(d: dict[str, object]) -> "Node":
        if "v" in d:
            return Node(value=float(d["v"]))  # type: ignore[arg-type]
        return Node(
            feature=int(d["f"]),  # type: ignore[arg-type]
            threshold=float(d["t"]),  # type: ignore[arg-type]
            left=Node.from_dict(d["l"]),  # type: ignore[arg-type]
            right=Node.from_dict(d["r"]),  # type: ignore[arg-type]
        )


def thresholds(X: list[list[float]]) -> list[list[float]]:
    """Candidate split points per feature: midpoints between up to MAX_BINS quantile values."""
    out: list[list[float]] = []
    for f in range(len(X[0])):
        vals = sorted({row[f] for row in X})
        if len(vals) > MAX_BINS:
            vals = [vals[int(i * (len(vals) - 1) / (MAX_BINS - 1))] for i in range(MAX_BINS)]
            vals = sorted(set(vals))
        out.append([(a + b) / 2 for a, b in zip(vals, vals[1:])])
    return out


def build_tree(
    X: list[list[float]],
    g: list[float],
    h: list[float],
    cands: list[list[float]],
    max_depth: int,
    reg_lambda: float = 1.0,
) -> Node:
    """Newton tree: leaf value = -sum(g)/(sum(h)+lambda); gain is the usual second-order score."""
    return _grow(X, g, h, list(range(len(X))), cands, max_depth, reg_lambda)


def _grow(X, g, h, idx, cands, depth, lam) -> Node:  # noqa: ANN001
    sg = sum(g[i] for i in idx)
    sh = sum(h[i] for i in idx)
    leaf = Node(value=-sg / (sh + lam))
    if depth == 0 or len(idx) < 2 * MIN_LEAF:
        return leaf
    parent = sg * sg / (sh + lam)
    best_gain, best_f, best_t = 1e-9, -1, 0.0
    for f, ts in enumerate(cands):
        if not ts:
            continue
        # Histogram over the candidate thresholds: bin k holds values in (ts[k-1], ts[k]].
        gs = [0.0] * (len(ts) + 1)
        hs = [0.0] * (len(ts) + 1)
        ns = [0] * (len(ts) + 1)
        for i in idx:
            v = X[i][f]
            lo, hi = 0, len(ts)
            while lo < hi:
                mid = (lo + hi) // 2
                if v <= ts[mid]:
                    hi = mid
                else:
                    lo = mid + 1
            gs[lo] += g[i]
            hs[lo] += h[i]
            ns[lo] += 1
        lg = lh = 0.0
        ln = 0
        for k, t in enumerate(ts):
            lg += gs[k]
            lh += hs[k]
            ln += ns[k]
            rn = len(idx) - ln
            if ln < MIN_LEAF or rn < MIN_LEAF:
                continue
            rg, rh = sg - lg, sh - lh
            gain = lg * lg / (lh + lam) + rg * rg / (rh + lam) - parent
            if gain > best_gain:
                best_gain, best_f, best_t = gain, f, t
    if best_f < 0:
        return leaf
    left = [i for i in idx if X[i][best_f] <= best_t]
    right = [i for i in idx if X[i][best_f] > best_t]
    return Node(
        feature=best_f,
        threshold=best_t,
        left=_grow(X, g, h, left, cands, depth - 1, lam),
        right=_grow(X, g, h, right, cands, depth - 1, lam),
    )


def _sigmoid(z: float) -> float:
    if z >= 0:
        return 1.0 / (1.0 + math.exp(-z))
    e = math.exp(z)
    return e / (1.0 + e)


@dataclass
class Booster:
    """Additive trees on a base score. `logistic` turns the sum into a probability."""

    logistic: bool
    base: float
    learning_rate: float
    trees: list[Node] = field(default_factory=list)

    def to_dict(self) -> dict[str, object]:
        """JSON form for the model artifact; floats round-trip exactly through json."""
        return {"logistic": self.logistic, "base": self.base, "learning_rate": self.learning_rate, "trees": [t.to_dict() for t in self.trees]}

    @staticmethod
    def from_dict(d: dict[str, object]) -> "Booster":
        return Booster(
            logistic=bool(d["logistic"]),
            base=float(d["base"]),  # type: ignore[arg-type]
            learning_rate=float(d["learning_rate"]),  # type: ignore[arg-type]
            trees=[Node.from_dict(t) for t in d["trees"]],  # type: ignore[union-attr]
        )

    def raw(self, x: list[float]) -> float:
        return self.base + self.learning_rate * sum(t.predict(x) for t in self.trees)

    def predict(self, x: list[float]) -> float:
        r = self.raw(x)
        return _sigmoid(r) if self.logistic else r


def fit_regressor(X: list[list[float]], y: list[float], trees: int = 80, depth: int = 3, lr: float = 0.15) -> Booster:
    base = sum(y) / len(y)
    cands = thresholds(X)
    model = Booster(False, base, lr)
    pred = [base] * len(y)
    ones = [1.0] * len(y)
    for _ in range(trees):
        g = [p - t for p, t in zip(pred, y)]
        tree = build_tree(X, g, ones, cands, depth)
        model.trees.append(tree)
        for i, row in enumerate(X):
            pred[i] += lr * tree.predict(row)
    return model


def fit_classifier(X: list[list[float]], y: list[int], trees: int = 40, depth: int = 3, lr: float = 0.2) -> Booster:
    rate = min(max(sum(y) / len(y), 1e-3), 1 - 1e-3)
    base = math.log(rate / (1 - rate))
    cands = thresholds(X)
    model = Booster(True, base, lr)
    raw = [base] * len(y)
    for _ in range(trees):
        p = [_sigmoid(r) for r in raw]
        g = [pi - yi for pi, yi in zip(p, y)]
        h = [max(pi * (1 - pi), 1e-6) for pi in p]
        tree = build_tree(X, g, h, cands, depth)
        model.trees.append(tree)
        for i, row in enumerate(X):
            raw[i] += lr * tree.predict(row)
    return model
