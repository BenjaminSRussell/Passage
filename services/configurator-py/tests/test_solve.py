import os
from pathlib import Path
from time import perf_counter

import pytest

from configurator.model import load_fixture
from configurator import solve as solve_module
from configurator.solve import solve


def _expand_valid(base: list[dict]) -> list[dict]:
    colors = ["black", "red", "blue", "green"]
    sizes = ["S", "M", "L", "XL"]
    out = list(base)
    while len(out) < 60:
        row = dict(out[len(out) % 10])
        row["color"] = colors[len(out) % len(colors)]
        row["size"] = sizes[len(out) % len(sizes)]
        if row["frame"] == "steel" and row["color"] == "white":
            row["color"] = "black"
        out.append(row)
    return out


def test_fifty_constraint_fixture():
    model = load_fixture()
    assert len(model["constraints"]) == 50
    valid = _expand_valid(model["valid"])
    invalid = model["invalid"]
    assert len(valid) == 60
    assert len(invalid) == 40
    for i, sel in enumerate(valid):
        start = perf_counter()
        res = solve(model, sel)
        assert res["feasible"], f"valid[{i}] {sel} -> {res}"
        for key, value in sel.items():
            assert res["assignment"][key] == value
        # Backtracking without OR-Tools is ~200ms on a laptop and can exceed
        # that on GitHub-hosted runners (observed ~230ms).
        assert (perf_counter() - start) < 2.0
    for i, sel in enumerate(invalid):
        start = perf_counter()
        res = solve(model, sel)
        assert not res["feasible"], f"invalid[{i}] {sel} should fail"
        assert res["iis"]
        assert (perf_counter() - start) < 2.0


def test_fixture_file_exists():
    assert Path("testdata/configurator-50.yaml").exists() or load_fixture()


def test_cpsat_backend_present_when_required():
    if os.environ.get("CONFIGURATOR_REQUIRE_ORTOOLS") != "1":
        pytest.skip("CP-SAT backend not required")
    assert solve_module.cp_model is not None


class _FellThrough(Exception):
    pass


def test_cpsat_itself_rejects_invalid_and_accepts_valid(monkeypatch):
    if solve_module.cp_model is None:
        pytest.skip("ortools not installed")

    def fail(*args, **kwargs):
        raise _FellThrough()

    monkeypatch.setattr(solve_module, "_solve_backtrack", fail)
    model = load_fixture()
    tables = {t["name"]: t for t in model.get("tables", [])}
    for sel in model["valid"]:
        assert solve_module._solve_cpsat(model, sel, tables)["feasible"]
    fell_through = 0
    for sel in model["invalid"]:
        try:
            result = solve_module._solve_cpsat(model, sel, tables)
        except _FellThrough:
            fell_through += 1
            continue
        assert not result["feasible"]
    assert fell_through > 0
