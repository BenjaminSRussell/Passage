#!/usr/bin/env python3
"""Verify the k6 campaign's requests against the services' OpenAPI files (PSG-50, FR-50.1).

Every ``http.<method>(`...`)`` call in ``load/k6/scenarios.js`` is resolved to a method and a gateway
path (``/api/v1/...``) and looked up in ``services/*/api/openapi.yaml`` through the gateway's
``/api/v1`` -> ``/v1`` rewrite, with the same route reader as ``test_smoke12.py``:

  * a call built from ``PATHS`` must hit a route that exists (same method, same path template);
  * a call built from ``UNVERIFIED_PATHS`` must still have no route; once the owning wave publishes
    it, the entry moves to ``PATHS`` (its body must then follow the published schema);
  * every entry of either object must be used by a call, so neither list can go stale.

With ``--gateway DIR`` it also checks the edge: every ``PATHS`` call must fall under an ``/api/v1/...``
``PathPrefix`` match of an HTTPRoute in ``DIR/*.yaml`` (``deploy/gateway``). A route the service declares
but the gateway does not forward is a 404 at the edge, so the campaign would fail on it.

A request URL that starts from neither object is an error, so a new call cannot skip the check.
Interpolations after the base path (``${POOL.mrp.planCode}``) match one template segment; the query
string is ignored. A variable URL (``${BASE}${path}``) is resolved through the ``path = ...``
assignments in the script, and every object entry they name is checked with the call's method.

Usage:
    python3 load/tools/verify_paths.py [--scenarios load/k6/scenarios.js] [--services services]
        [--gateway deploy/gateway]

Exit status: 0 every check passes, 1 a check fails, 2 the script cannot be read or parsed.
Stdlib only, Python 3.11+.
"""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass
from pathlib import Path

from openapi_routes import ROOT, SERVICES, all_routes, resolve

DEFAULT_SCENARIOS = ROOT / "load" / "k6" / "scenarios.js"
OBJECTS = ("PATHS", "UNVERIFIED_PATHS")

OBJECT_RE = re.compile(r"\bconst\s+(PATHS|UNVERIFIED_PATHS)\s*=\s*\{(.*?)\n\};", re.DOTALL)
ENTRY_RE = re.compile(r"^\s*(\w+)\s*:\s*'([^']+)'\s*,?\s*(?://.*)?$")
CALL_RE = re.compile(r"\bhttp\.(get|post|put|patch|del)\(\s*`([^`]*)`")
REF_RE = re.compile(r"\b(PATHS|UNVERIFIED_PATHS)\.(\w+)\b")
LEAD_RE = re.compile(r"^\$\{\s*(?:(PATHS|UNVERIFIED_PATHS)\.(\w+)|(\w+))\s*\}")
INTERP_RE = re.compile(r"\$\{[^}]*\}")
GATEWAY_PREFIX_RE = re.compile(r"type:\s*PathPrefix\s*\n\s*value:\s*['\"]?(/api/v1/[^\s'\"]+)")
METHODS = {"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH", "del": "DELETE"}


class ScriptError(ValueError):
    pass


@dataclass(frozen=True)
class Call:
    obj: str  # PATHS or UNVERIFIED_PATHS
    key: str
    method: str
    path: str  # gateway path with {param} for interpolations, e.g. /api/v1/planning/plans/{param}:run
    line: int


def extract_objects(text: str) -> dict[str, dict[str, str]]:
    """{"PATHS": {key: path}, "UNVERIFIED_PATHS": {...}}. Entries may carry a trailing // comment."""
    found: dict[str, dict[str, str]] = {}
    for name, body in OBJECT_RE.findall(text):
        entries: dict[str, str] = {}
        for raw in body.splitlines():
            if not raw.strip() or raw.strip().startswith("//"):
                continue
            m = ENTRY_RE.match(raw)
            if not m:
                raise ScriptError(f"{name}: cannot read entry {raw.strip()!r}")
            entries[m[1]] = m[2]
        found[name] = entries
    if "PATHS" not in found:
        raise ScriptError("no `const PATHS = {...};` object in the script")
    found.setdefault("UNVERIFIED_PATHS", {})
    return found


def _line(text: str, offset: int) -> int:
    return text.count("\n", 0, offset) + 1


def _assigned_refs(text: str, var: str) -> list[tuple[str, str]]:
    """Object entries named on the right-hand side of every `var = ...;` assignment."""
    refs: list[tuple[str, str]] = []
    for rhs in re.findall(rf"\b{re.escape(var)}\s*=(?!=)\s*([^;]+);", text):
        refs += REF_RE.findall(rhs)
    return refs


def extract_calls(text: str) -> list[Call]:
    objects = extract_objects(text)
    calls: list[Call] = []
    for m in CALL_RE.finditer(text):
        method, template, line = METHODS[m[1]], m[2], _line(text, m.start())
        rest = re.sub(r"^\$\{\s*BASE\s*\}", "", template)
        lead = LEAD_RE.match(rest)
        if not lead:
            raise ScriptError(f"line {line}: request URL `{template}` does not start from PATHS or UNVERIFIED_PATHS")
        if lead[1]:
            refs = [(lead[1], lead[2])]
        else:
            refs = _assigned_refs(text, lead[3])
            if not refs:
                raise ScriptError(f"line {line}: `{lead[3]}` is never assigned from PATHS or UNVERIFIED_PATHS")
        suffix = INTERP_RE.sub("{param}", rest[lead.end():]).split("?", 1)[0]
        for obj, key in refs:
            if key not in objects[obj]:
                raise ScriptError(f"line {line}: {obj}.{key} is not defined")
            calls.append(Call(obj, key, method, objects[obj][key] + suffix, line))
    return calls


def check(text: str, routes) -> list[str]:
    """Failures, one line each; empty when every call and entry passes."""
    objects = extract_objects(text)
    calls = extract_calls(text)
    failures: list[str] = []
    for c in calls:
        hit = resolve(routes, c.method, c.path)
        if c.obj == "PATHS" and hit is None:
            failures.append(f"line {c.line}: {c.method} {c.path} (PATHS.{c.key}) has no route in any service OpenAPI file")
        elif c.obj == "UNVERIFIED_PATHS" and hit is not None:
            failures.append(
                f"line {c.line}: {c.method} {c.path} (UNVERIFIED_PATHS.{c.key}) now exists as {hit}; "
                "move it to PATHS and match its request body to the schema"
            )
    used = {(c.obj, c.key) for c in calls}
    for obj in OBJECTS:
        for key in objects[obj]:
            if (obj, key) not in used:
                failures.append(f"{obj}.{key} is declared but no request uses it")
    return failures


def gateway_prefixes(gateway: Path) -> list[str]:
    """The public ``/api/v1/...`` PathPrefix values of the HTTPRoutes in ``gateway/*.yaml``.

    Stdlib only, so it reads ``type: PathPrefix`` followed by ``value:`` instead of parsing YAML (the
    layout of ``deploy/gateway/httproutes.yaml``).
    """
    found: set[str] = set()
    for f in sorted(gateway.glob("*.yaml")):
        found.update(p.rstrip("/") for p in GATEWAY_PREFIX_RE.findall(f.read_text(encoding="utf-8")))
    return sorted(found)


def check_gateway(text: str, prefixes: list[str]) -> list[str]:
    """Failures for ``PATHS`` calls no gateway prefix forwards (Gateway API prefixes match whole segments)."""
    failures: list[str] = []
    for c in extract_calls(text):
        if c.obj != "PATHS":
            continue
        if not any(c.path == p or c.path.startswith(p + "/") for p in prefixes):
            failures.append(f"line {c.line}: {c.method} {c.path} (PATHS.{c.key}) has no gateway route")
    return failures


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--scenarios", type=Path, default=DEFAULT_SCENARIOS)
    parser.add_argument("--services", type=Path, default=SERVICES, help="directory holding <svc>/api/openapi.yaml")
    parser.add_argument("--gateway", type=Path, help="also check the edge: HTTPRoute YAML directory (deploy/gateway)")
    args = parser.parse_args(argv)
    try:
        text = args.scenarios.read_text(encoding="utf-8")
        calls = extract_calls(text)
    except (OSError, ScriptError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    routes = all_routes(args.services)
    if not routes:
        print(f"error: no operations found under {args.services}/*/api/openapi.yaml", file=sys.stderr)
        return 2
    failures = check(text, routes)
    if args.gateway is not None:
        prefixes = gateway_prefixes(args.gateway)
        if not prefixes:
            print(f"error: no /api/v1 PathPrefix routes found under {args.gateway}/*.yaml", file=sys.stderr)
            return 2
        failures += check_gateway(text, prefixes)
    for c in calls:
        print(f"{c.obj:<16} {c.method:<6} {c.path}")
    for f in failures:
        print(f"FAIL {f}", file=sys.stderr)
    if failures:
        print(f"{len(failures)} campaign route check(s) failed (FR-50.1)", file=sys.stderr)
        return 1
    print(f"all {len(calls)} campaign request(s) checked against {len(routes)} service operation(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
