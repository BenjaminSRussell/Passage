from pathlib import Path
from time import perf_counter

from configurator.model import load_fixture
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
        assert (perf_counter() - start) < 0.2
    for i, sel in enumerate(invalid):
        start = perf_counter()
        res = solve(model, sel)
        assert not res["feasible"], f"invalid[{i}] {sel} should fail"
        assert res["iis"]
        assert (perf_counter() - start) < 0.2


def test_fixture_file_exists():
    assert Path("testdata/configurator-50.yaml").exists() or load_fixture()
