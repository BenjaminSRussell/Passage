#!/usr/bin/env python3
"""API reference renderer and undocumented-operation gate for OpenAPI documents (PSG-50, PRD 9.50.8 AC-50.11).

Every operation a service exposes must be documented before the reference is published. An operation is
documented when it has:

  operationId    the stable name the reference and generated clients use
  summary or     a non-empty sentence saying what the operation does (summary preferred; a description
  description    alone also counts)
  x-passage-duty the privilege code that guards it (conventions: every operation carries x-passage-duty)
  2xx response   at least one success status, and every response has a description

Probe and internal routes are not part of the published API and are skipped: /healthz, /readyz, /metrics
and paths under /internal/ (the gateway does not route them).

Local $ref pointers (to parameters and responses) are resolved first. An unresolvable $ref is an input
error, not a silent pass.

For each --spec the tool prints every gap as `FAIL  <service>: <METHOD /path>: <problem>`. With --out it
also writes one Markdown page per spec (`<service>.md`) for the docs site build, which is a handoff to
deployment-cicd. The service name is the file stem, or the service directory for
`services/<service>/api/openapi.json`; two specs with the same name are an input error. Exit status: 0 no
gaps, 1 undocumented operations, 2 usage or input error.

    python3 docs/ops/tools/api_reference.py --spec services/org/api/openapi.json --out build/api

The input is JSON; convert YAML first (CI does this, e.g. `yq -o=json`). AsyncAPI documents are checked by
asyncapi_reference.py. Stdlib only, Python 3.11+. Tests: python3 -m unittest discover -s docs/ops/tools
"""

from __future__ import annotations

import argparse
import json
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

HTTP_METHODS = ("get", "put", "post", "delete", "options", "head", "patch", "trace")
MAX_REF_HOPS = 32
UNPUBLISHED_PATHS = ("/healthz", "/readyz", "/metrics")
UNPUBLISHED_PREFIXES = ("/internal/",)


@dataclass(frozen=True)
class Operation:
    method: str
    path: str
    op: dict[str, Any]
    path_params: list[Any]


def _resolve(node: Any, doc: dict[str, Any]) -> dict[str, Any]:
    """Follow local $ref pointers. Non-object nodes resolve to {}; broken references raise ValueError."""
    hops = 0
    while isinstance(node, dict) and "$ref" in node:
        ref = node["$ref"]
        if not isinstance(ref, str) or not ref.startswith("#/"):
            raise ValueError(f"external or invalid $ref not supported: {ref!r}")
        cur: Any = doc
        for part in ref[2:].split("/"):
            key = part.replace("~1", "/").replace("~0", "~")
            if not isinstance(cur, dict) or key not in cur:
                raise ValueError(f"unresolved $ref: {ref}")
            cur = cur[key]
        node = cur
        hops += 1
        if hops > MAX_REF_HOPS:
            raise ValueError("$ref chain too long")
    return node if isinstance(node, dict) else {}


def _success(code: Any) -> bool:
    text = str(code)
    return text.isdigit() and 200 <= int(text) < 300


def published(path: str) -> bool:
    return path not in UNPUBLISHED_PATHS and not path.startswith(UNPUBLISHED_PREFIXES)


def operations(doc: dict[str, Any]) -> list[Operation]:
    """Every published operation, ordered by path then by HTTP method (deterministic output)."""
    out: list[Operation] = []
    paths = doc.get("paths") or {}
    for path in sorted(paths):
        item = paths[path]
        if not isinstance(item, dict) or not published(path):
            continue
        shared = list(item.get("parameters") or [])
        for method in HTTP_METHODS:
            op = item.get(method)
            if isinstance(op, dict):
                out.append(Operation(method, path, op, shared))
    return out


def _duty(o: Operation) -> str:
    return str(o.op.get("x-passage-duty") or "").strip()


def _params(o: Operation, doc: dict[str, Any]) -> list[dict[str, Any]]:
    return [_resolve(p, doc) for p in o.path_params + list(o.op.get("parameters") or [])]


def _idempotent(o: Operation, doc: dict[str, Any]) -> bool:
    return any(
        p.get("in") == "header" and str(p.get("name", "")).lower() == "idempotency-key" for p in _params(o, doc)
    )


def gaps(o: Operation, doc: dict[str, Any]) -> list[str]:
    """Problems that keep an operation out of the published reference. Empty list means documented."""
    label = f"{o.method.upper()} {o.path}"
    out: list[str] = []
    if not str(o.op.get("operationId") or "").strip():
        out.append(f"{label}: missing operationId")
    if not _text(o.op, "summary") and not _text(o.op, "description"):
        out.append(f"{label}: missing summary or description")
    if not _duty(o):
        out.append(f"{label}: missing x-passage-duty")
    responses = o.op.get("responses") or {}
    if not any(_success(code) for code in responses):
        out.append(f"{label}: no 2xx response")
    for code, raw in responses.items():
        if not str(_resolve(raw, doc).get("description") or "").strip():
            out.append(f"{label}: response {code} missing description")
    return out


def _text(op: dict[str, Any], key: str) -> str:
    return str(op.get(key) or "").strip()


def _cell(value: Any) -> str:
    text = str(value if value is not None else "").replace("|", "\\|").replace("\n", " ").strip()
    return text or "-"


def render(name: str, doc: dict[str, Any]) -> str:
    """Markdown reference page: a summary table, then one section per operation."""
    info = doc.get("info") or {}
    ops = operations(doc)
    lines = [
        f"# {name} API reference",
        "",
        f"OpenAPI {doc.get('openapi', '?')}, API version {info.get('version', 'unknown')}. "
        "Generated by docs/ops/tools/api_reference.py from the service's OpenAPI document; edit the spec, not this page.",
        "",
        f"{len(ops)} operations.",
        "",
        "| Method | Path | Operation | Summary | Duty | Idempotency-Key | Success codes |",
        "|---|---|---|---|---|---|---|",
    ]
    for o in ops:
        success = ", ".join(sorted(str(c) for c in (o.op.get("responses") or {}) if _success(c)))
        idem = "yes" if _idempotent(o, doc) else "no"
        summary = _text(o.op, "summary") or _text(o.op, "description").split(". ")[0]
        lines.append(
            f"| {o.method.upper()} | {_cell(o.path)} | {_cell(o.op.get('operationId'))} | {_cell(summary)} "
            f"| {_cell(_duty(o))} | {idem} | {_cell(success)} |"
        )
    for o in ops:
        lines += ["", f"## {o.method.upper()} {o.path}", ""]
        lines += [text for text in (_text(o.op, "summary"), _text(o.op, "description")) if text]
        lines += [
            "",
            f"- Operation: `{_text(o.op, 'operationId') or 'missing'}`",
            f"- Duty: `{_duty(o) or 'missing'}`",
            f"- Idempotency-Key: {'header required by convention' if _idempotent(o, doc) else 'not declared'}",
        ]
        params = _params(o, doc)
        if params:
            lines += ["", "Parameters:", ""]
            for p in params:
                required = ", required" if p.get("required") is True else ""
                lines.append(f"- `{p.get('name', '?')}` ({p.get('in', '?')}{required})")
        responses = o.op.get("responses") or {}
        if responses:
            lines += ["", "Responses:", ""]
            for code in sorted(responses, key=str):
                lines.append(f"- `{code}`: {str(_resolve(responses[code], doc).get('description') or '').strip()}")
    return "\n".join(lines) + "\n"


def load_spec(path: Path) -> dict[str, Any]:
    if path.suffix.lower() in (".yaml", ".yml"):
        raise ValueError(f"{path}: convert YAML to JSON first (e.g. yq -o=json)")
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict) or not isinstance(data.get("paths"), dict):
        raise ValueError(f"{path}: not an OpenAPI document with paths")
    return data


def service_name(spec: Path) -> str:
    """`services/<svc>/api/openapi.json` is named after <svc>; any other file after its stem."""
    resolved = spec.resolve()
    if resolved.stem == "openapi" and resolved.parent.name == "api":
        return resolved.parent.parent.name
    return spec.stem


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--spec", type=Path, action="append", required=True, help="OpenAPI JSON; repeat per service")
    parser.add_argument("--out", type=Path, help="directory for one <service>.md page per spec")
    args = parser.parse_args(argv)

    problems: list[str] = []
    counted = 0
    try:
        names = [service_name(spec) for spec in args.spec]
        duplicates = sorted({n for n in names if names.count(n) > 1})
        if duplicates:
            raise ValueError(f"two specs map to the same service page: {', '.join(duplicates)}")
        for spec, name in zip(args.spec, names):
            doc = load_spec(spec)
            ops = operations(doc)
            counted += len(ops)
            for o in ops:
                problems += [f"{name}: {g}" for g in gaps(o, doc)]
            if args.out is not None:
                args.out.mkdir(parents=True, exist_ok=True)
                (args.out / f"{name}.md").write_text(render(name, doc), encoding="utf-8")
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    for problem in problems:
        print(f"FAIL  {problem}")
    if problems:
        undocumented = len({p.rsplit(": ", 1)[0] for p in problems})
        print(f"API REFERENCE FAILED: {undocumented} of {counted} operations undocumented ({len(problems)} gap(s))")
        return 1
    print(f"API REFERENCE PASSED: {counted} operations, 0 undocumented")
    return 0


if __name__ == "__main__":
    sys.exit(main())
