"""Solve a discrete configuration model with OR-Tools CP-SAT, or a backtracking fallback."""

from __future__ import annotations

from typing import Any

try:
    from ortools.sat.python import cp_model
except ImportError:  # pragma: no cover - exercised when ortools is absent
    cp_model = None


def _eval(constraint: dict, assign: dict[str, str], tables: dict[str, dict]) -> bool:
    kind = constraint["kind"]
    if kind == "eq":
        left = assign.get(constraint["left"], "")
        right = constraint.get("right", "")
        return left == right or left == assign.get(right, right)
    if kind == "neq":
        return assign.get(constraint["left"], "") != constraint.get("right", "")
    if kind == "in":
        return assign.get(constraint["left"], "") in constraint.get("values", [])
    if kind == "implies":
        if assign.get(constraint["left"], "") != constraint.get("right", ""):
            return True
        return _eval(constraint["then"], assign, tables)
    if kind == "table":
        table = tables.get(constraint.get("table", ""), {})
        cols = table.get("columns", constraint.get("attrs", []))
        for row in table.get("rows", []):
            if all(assign.get(col, "") in ("", row[i]) for i, col in enumerate(cols)):
                return True
        return False
    return True


def _refs(constraint: dict) -> list[str]:
    names = [constraint.get("left", "")]
    names.extend(constraint.get("attrs") or [])
    if constraint.get("then"):
        names.extend(_refs(constraint["then"]))
    return [n for n in names if n]


def _bound(constraint: dict, assign: dict[str, str], domains: dict[str, list[str]]) -> bool:
    if constraint["kind"] == "implies" and assign.get(constraint["left"], "") != constraint.get("right", ""):
        return True
    for name in _refs(constraint):
        if name in domains and not assign.get(name):
            return False
    return True


def _violated(model: dict, assign: dict[str, str], tables: dict[str, dict]) -> list[str]:
    return [c["name"] for c in model["constraints"] if not _eval(c, assign, tables)]


def _bound_violated(model: dict, assign: dict[str, str], tables: dict[str, dict], domains: dict[str, list[str]]) -> list[str]:
    return [c["name"] for c in model["constraints"] if _bound(c, assign, domains) and not _eval(c, assign, tables)]


def _search(model: dict, names: list[str], i: int, assign: dict[str, str], domains: dict[str, list[str]], tables: dict[str, dict]) -> bool:
    if i == len(names):
        return not _violated(model, assign, tables)
    name = names[i]
    if assign.get(name):
        return _search(model, names, i + 1, assign, domains, tables)
    for value in domains[name]:
        assign[name] = value
        if _search(model, names, i + 1, assign, domains, tables):
            return True
    assign.pop(name, None)
    return False


def _solve_backtrack(model: dict, selections: dict[str, str], tables: dict[str, dict]) -> dict[str, Any]:
    domains = {a["name"]: list(a["domain"]) for a in model["attributes"]}
    assign = dict(model.get("defaults") or {})
    assign.update(selections)
    for name, value in selections.items():
        if name not in domains:
            return {"feasible": False, "iis": [name], "assignment": assign}
        if value not in domains[name]:
            return {"feasible": False, "iis": [name], "assignment": assign}
    names = [a["name"] for a in model["attributes"]]
    early = _bound_violated(model, assign, tables, domains)
    if early:
        return {"feasible": False, "assignment": assign, "iis": early[:10]}
    if _search(model, names, 0, assign, domains, tables):
        return {"feasible": True, "assignment": assign, "iis": []}
    iis = _violated(model, {**dict(model.get("defaults") or {}), **selections}, tables)
    return {"feasible": False, "assignment": assign, "iis": iis[:10] or ["infeasible"]}


def _solve_cpsat(model: dict, selections: dict[str, str], tables: dict[str, dict]) -> dict[str, Any]:
    domains = {a["name"]: list(a["domain"]) for a in model["attributes"]}
    m = cp_model.CpModel()
    vars_ = {}
    for name, domain in domains.items():
        vars_[name] = m.NewIntVar(0, len(domain) - 1, name)
    for name, value in selections.items():
        if name not in domains or value not in domains[name]:
            return {"feasible": False, "iis": [name], "assignment": dict(selections)}
        m.Add(vars_[name] == domains[name].index(value))
    for default_name, default_value in (model.get("defaults") or {}).items():
        if default_name in domains and default_name not in selections and default_value in domains[default_name]:
            # defaults are preferences, not hard constraints
            pass

    def lit(name: str, value: str):
        return vars_[name] == domains[name].index(value)

    for c in model["constraints"]:
        kind = c["kind"]
        if kind == "neq":
            if c["right"] in domains.get(c["left"], []):
                m.Add(vars_[c["left"]] != domains[c["left"]].index(c["right"]))
        elif kind == "in":
            m.AddAllowedAssignments([vars_[c["left"]]], [[domains[c["left"]].index(v)] for v in c["values"] if v in domains[c["left"]]])
        elif kind == "implies":
            if c["right"] not in domains.get(c["left"], []):
                continue
            ant = lit(c["left"], c["right"])
            then = c["then"]
            if then["kind"] == "in":
                allowed = [domains[then["left"]].index(v) for v in then["values"] if v in domains[then["left"]]]
                m.AddAllowedAssignments([vars_[then["left"]]], [[i] for i in allowed]).OnlyEnforceIf(ant)
            elif then["kind"] == "eq" and then.get("right") in domains.get(then["left"], []):
                m.Add(vars_[then["left"]] == domains[then["left"]].index(then["right"])).OnlyEnforceIf(ant)
        elif kind == "table":
            table = tables.get(c.get("table", ""), {})
            cols = table.get("columns", [])
            rows = []
            for row in table.get("rows", []):
                rows.append([domains[col].index(row[i]) for i, col in enumerate(cols)])
            if rows:
                m.AddAllowedAssignments([vars_[col] for col in cols], rows)

    solver = cp_model.CpSolver()
    solver.parameters.max_time_in_seconds = 2.0
    solver.parameters.num_search_workers = 1
    solver.parameters.random_seed = 1
    status = solver.Solve(m)
    if status in (cp_model.OPTIMAL, cp_model.FEASIBLE):
        assignment = {name: domains[name][solver.Value(var)] for name, var in vars_.items()}
        return {"feasible": True, "assignment": assignment, "iis": []}
    return _solve_backtrack(model, selections, tables)


def solve(model: dict, selections: dict[str, str], tables: dict[str, dict] | None = None) -> dict[str, Any]:
    tables = tables or {t["name"]: t for t in model.get("tables", [])}
    if cp_model is not None:
        return _solve_cpsat(model, selections, tables)
    return _solve_backtrack(model, selections, tables)
