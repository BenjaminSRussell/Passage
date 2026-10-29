#!/usr/bin/env python3
"""N / N-1 API compatibility check for OpenAPI documents (PSG-50, FR-50.7, AC-50.5).

A rolling deploy runs release N+1 next to release N, and clients still on N-1 keep calling the
API. This check compares the previous release's OpenAPI document (N-1) with the new one (N) and
reports every change that an N-1 client could notice as breaking:

  path-removed             the whole path is gone
  operation-removed        a path/method that N-1 clients call is gone
  parameter-removed        a parameter N-1 clients send is gone
  parameter-required       a parameter became required, or a new required one was added
  request-body-required    the request body became required, or a required one was added
  request-media-removed    a request content type N-1 clients send is gone (415)
  request-field-removed    a request property N-1 clients send is gone
  request-field-required   a request property became required, or a new required one was added
  request-closed           additionalProperties became false, so unknown fields are now rejected
  response-removed         a 2xx status an N-1 client may rely on is gone
  response-media-removed   the JSON body of a 2xx response is gone
  response-field-removed   a response property N-1 clients may read is gone
  response-field-optional  a response property stopped being required, so clients may not get it
  type-changed             a request accepts fewer JSON types, or a response may return a new one
  enum-value-removed       a request no longer accepts an enum value N-1 clients send

Direction matters: a client sends requests and reads responses, so a request may widen (accept
more types or enum values) and a response may narrow. New response enum values are printed as
warnings, not failures: clients must tolerate unknown values, and listing them lets the release
manager check that the N-1 web client does.

Additive changes (new paths, methods, optional fields, optional parameters, new status codes) and
renamed path parameters (``{id}`` -> ``{orderId}``: clients only see the position) are allowed.

Input is JSON. Convert YAML first (e.g. ``yq -o=json services/x/api/openapi.yaml``); this keeps the
tool free of third-party dependencies. Only local ``#/`` references are resolved; external
references are an error, not a silent pass. ``allOf`` members are merged before comparing; schemas
under ``oneOf`` and ``anyOf`` are not compared.

Usage:
    python3 deploy/multi-region/tools/openapi_compat.py --old n1.json --new n.json

Exit status: 0 compatible, 1 breaking change(s), 2 usage or input error.
Stdlib only, Python 3.11+.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

HTTP_METHODS = ("get", "put", "post", "delete", "options", "head", "patch", "trace")
MAX_DEPTH = 64  # safety net; cycles through $ref are cut by the seen set
MAX_REF_HOPS = 32
PARAM_RE = re.compile(r"\{[^}]+\}")
JSON_MEDIA = "application/json"


@dataclass(frozen=True)
class Change:
    kind: str
    location: str
    detail: str

    def __str__(self) -> str:
        return f"{self.kind} {self.location}" + (f": {self.detail}" if self.detail else "")


@dataclass
class Report:
    breaking: list[Change]
    warnings: list[Change]


def resolve(node: Any, doc: dict[str, Any]) -> dict[str, Any]:
    """Follow local $ref pointers until a concrete object is reached. Non-objects resolve to {}."""
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


def flatten(node: Any, doc: dict[str, Any], hops: int = 0) -> dict[str, Any]:
    """Resolve $ref and merge allOf members: properties and required are unioned, the rest kept."""
    s = resolve(node, doc)
    members = s.get("allOf")
    if not isinstance(members, list):
        return s
    if hops > MAX_REF_HOPS:
        raise ValueError("allOf nesting too deep")
    out = {k: v for k, v in s.items() if k != "allOf"}
    props: dict[str, Any] = {}
    required: list[str] = []
    for m in members:
        part = flatten(m, doc, hops + 1)
        for k, v in part.items():
            if k not in ("properties", "required"):
                out.setdefault(k, v)
        props.update(part.get("properties") or {})
        required += [r for r in part.get("required") or [] if r not in required]
    props.update(s.get("properties") or {})
    required += [r for r in s.get("required") or [] if r not in required]
    if props:
        out["properties"] = props
    if required:
        out["required"] = required
    return out


def types_of(s: dict[str, Any]) -> set[str] | None:
    """The JSON types a schema allows (3.1 type lists and 3.0 nullable), or None when unconstrained."""
    t = s.get("type")
    if t is None:
        return None
    found = set(t) if isinstance(t, list) else {t}
    if s.get("nullable") is True:
        found.add("null")
    return found


def covers(wide: set[str], narrow: set[str]) -> bool:
    """True when every type in narrow is allowed by wide (a number accepts an integer)."""
    return all(t in wide or (t == "integer" and "number" in wide) for t in narrow)


def media(owner: dict[str, Any]) -> dict[str, Any]:
    content = owner.get("content")
    return content if isinstance(content, dict) else {}


def json_schema(owner: dict[str, Any]) -> dict[str, Any] | None:
    """The application/json schema of a request body or response, or None when absent."""
    schema = (media(owner).get(JSON_MEDIA) or {}).get("schema")
    return schema if isinstance(schema, dict) else None


def schema_changes(
    old: Any,
    new: Any,
    old_doc: dict[str, Any],
    new_doc: dict[str, Any],
    loc: str,
    mode: str,
    report: Report,
    seen: frozenset = frozenset(),
    depth: int = 0,
) -> None:
    """Changes in one schema. mode is "request" (client sends it) or "response" (client reads it)."""
    refs = (old.get("$ref") if isinstance(old, dict) else None, new.get("$ref") if isinstance(new, dict) else None)
    if refs != (None, None):
        if refs in seen:
            return
        seen = seen | {refs}
    if depth > MAX_DEPTH:
        return
    o = flatten(old, old_doc)
    n = flatten(new, new_doc)
    if any(k in s for s in (o, n) for k in ("oneOf", "anyOf")):
        return  # not compared (module docstring)
    out = report.breaking

    ot, nt = types_of(o), types_of(n)
    if mode == "request":
        # A constraint where there was none narrows the request; dropping one widens it.
        if nt is not None and (ot is None or not covers(nt, ot)):
            out.append(Change("type-changed", loc, f"accepts {fmt_types(nt)}, N-1 clients send {fmt_types(ot)}"))
            return
    elif ot is not None and (nt is None or not covers(ot, nt)):
        out.append(Change("type-changed", loc, f"may return {fmt_types(nt)}, N-1 clients expect {fmt_types(ot)}"))
        return

    oe, ne = o.get("enum"), n.get("enum")
    if mode == "request":
        if isinstance(ne, list) and (not isinstance(oe, list) or any(v not in ne for v in oe)):
            removed = [v for v in oe if v not in ne] if isinstance(oe, list) else []
            detail = ", ".join(json.dumps(v) for v in removed) if removed else "enum added to a free field"
            out.append(Change("enum-value-removed", loc, detail))
    elif isinstance(oe, list) and isinstance(ne, list):
        added = [v for v in ne if v not in oe]
        if added:
            report.warnings.append(Change("enum-value-added", loc, ", ".join(json.dumps(v) for v in added)))

    op_props = o.get("properties") or {}
    np_props = n.get("properties") or {}
    old_req = set(o.get("required") or [])
    new_req = set(n.get("required") or [])
    if mode == "response":
        for name, sub in op_props.items():
            child = f"{loc}.{name}"
            if name not in np_props:
                out.append(Change("response-field-removed", child, "clients may read it"))
                continue
            if name in old_req and name not in new_req:
                out.append(Change("response-field-optional", child, "no longer required, clients may not get it"))
            schema_changes(sub, np_props[name], old_doc, new_doc, child, mode, report, seen, depth + 1)
    else:
        if o.get("additionalProperties") is not False and n.get("additionalProperties") is False:
            out.append(Change("request-closed", loc, "additionalProperties became false"))
        for name in np_props:
            if name not in op_props and name in new_req:
                out.append(Change("request-field-required", f"{loc}.{name}", "new required field"))
        for name, sub in op_props.items():
            child = f"{loc}.{name}"
            if name not in np_props:
                out.append(Change("request-field-removed", child, "N-1 clients still send it"))
                continue
            if name in new_req and name not in old_req:
                out.append(Change("request-field-required", child, "field became required"))
            schema_changes(sub, np_props[name], old_doc, new_doc, child, mode, report, seen, depth + 1)

    if isinstance(o.get("items"), dict) and isinstance(n.get("items"), dict):
        schema_changes(o["items"], n["items"], old_doc, new_doc, f"{loc}[]", mode, report, seen, depth + 1)
    oa, na = o.get("additionalProperties"), n.get("additionalProperties")
    if isinstance(oa, dict) and isinstance(na, dict):
        schema_changes(oa, na, old_doc, new_doc, f"{loc}{{*}}", mode, report, seen, depth + 1)


def fmt_types(t: set[str] | None) -> str:
    return "any type" if t is None else "/".join(sorted(t))


def path_key(path: str) -> str:
    """Path with parameter names erased: clients see /v1/x/{id} and /v1/x/{orderId} as one path."""
    return PARAM_RE.sub("{}", path)


def effective_params(
    item: dict[str, Any], op: dict[str, Any], doc: dict[str, Any], rename: dict[str, str]
) -> dict[tuple[str, str], dict[str, Any]]:
    """Path-level parameters overridden by operation-level ones, keyed by (name, location).

    rename maps this document's path parameter names to the other document's, so a renamed path
    parameter is compared with its counterpart at the same position.
    """
    out: dict[tuple[str, str], dict[str, Any]] = {}
    for raw in list(item.get("parameters") or []) + list(op.get("parameters") or []):
        p = resolve(raw, doc)
        name, where = str(p.get("name")), str(p.get("in"))
        if where == "path":
            name = rename.get(name, name)
        out[(name, where)] = p
    return out


def operation_changes(
    old_path: str,
    new_path: str,
    method: str,
    old_item: dict[str, Any],
    new_item: dict[str, Any],
    old_doc: dict[str, Any],
    new_doc: dict[str, Any],
    report: Report,
) -> None:
    old_op = old_item[method]
    new_op = new_item[method]
    loc = f"{method.upper()} {old_path}"
    out = report.breaking

    rename = {n[1:-1]: o[1:-1] for o, n in zip(PARAM_RE.findall(old_path), PARAM_RE.findall(new_path))}
    oldp = effective_params(old_item, old_op, old_doc, {})
    newp = effective_params(new_item, new_op, new_doc, rename)
    for key, p in oldp.items():
        label = f"{key[1]} {key[0]}"
        if key not in newp:
            out.append(Change("parameter-removed", loc, label))
            continue
        if p.get("required") is not True and newp[key].get("required") is True:
            out.append(Change("parameter-required", loc, f"{label} became required"))
        if isinstance(p.get("schema"), dict) and isinstance(newp[key].get("schema"), dict):
            schema_changes(p["schema"], newp[key]["schema"], old_doc, new_doc, f"{loc} {label}", "request", report)
    for key, p in newp.items():
        if key not in oldp and p.get("required") is True:
            out.append(Change("parameter-required", loc, f"new required {key[1]} {key[0]}"))

    old_body = resolve(old_op["requestBody"], old_doc) if old_op.get("requestBody") is not None else None
    new_body = resolve(new_op["requestBody"], new_doc) if new_op.get("requestBody") is not None else None
    if new_body is not None:
        if old_body is None and new_body.get("required") is True:
            out.append(Change("request-body-required", loc, "new required body"))
        elif old_body is not None and new_body.get("required") is True and old_body.get("required") is not True:
            out.append(Change("request-body-required", loc, "body became required"))
        if old_body is not None:
            for mt in media(old_body):
                if mt not in media(new_body):
                    out.append(Change("request-media-removed", loc, mt))
            osch, nsch = json_schema(old_body), json_schema(new_body)
            if osch is not None and nsch is not None:
                schema_changes(osch, nsch, old_doc, new_doc, f"{loc} body", "request", report)

    old_rsp = old_op.get("responses") or {}
    new_rsp = new_op.get("responses") or {}
    for code, raw in old_rsp.items():
        if not is_success(str(code)):
            continue
        if code not in new_rsp:
            out.append(Change("response-removed", loc, f"status {code}"))
            continue
        osch = json_schema(resolve(raw, old_doc))
        nsch = json_schema(resolve(new_rsp[code], new_doc))
        if osch is not None and nsch is None:
            out.append(Change("response-media-removed", loc, f"status {code} no longer returns {JSON_MEDIA}"))
        elif osch is not None and nsch is not None:
            schema_changes(osch, nsch, old_doc, new_doc, f"{loc} {code}", "response", report)


def is_success(code: str) -> bool:
    return code.isdigit() and 200 <= int(code) < 300


def check(old_doc: dict[str, Any], new_doc: dict[str, Any]) -> Report:
    """Breaking changes and warnings for N-1 clients after the API moves from old_doc to new_doc."""
    report = Report([], [])
    old_paths = old_doc.get("paths") or {}
    new_by_key = {path_key(p): p for p in (new_doc.get("paths") or {})}
    for path, old_item in old_paths.items():
        new_path = new_by_key.get(path_key(path))
        if new_path is None:
            report.breaking.append(Change("path-removed", path, ""))
            continue
        new_item = new_doc["paths"][new_path]
        for method in HTTP_METHODS:
            if method not in old_item:
                continue
            if method not in new_item:
                report.breaking.append(Change("operation-removed", f"{method.upper()} {path}", ""))
                continue
            operation_changes(path, new_path, method, old_item, new_item, old_doc, new_doc, report)
    return report


def compare(old_doc: dict[str, Any], new_doc: dict[str, Any]) -> list[Change]:
    """The breaking changes only."""
    return check(old_doc, new_doc).breaking


def load_doc(path: Path) -> dict[str, Any]:
    if path.suffix.lower() in (".yaml", ".yml"):
        raise ValueError(f"{path}: convert YAML to JSON first (e.g. yq -o=json)")
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict) or not isinstance(data.get("paths"), dict):
        raise ValueError(f"{path}: not an OpenAPI document with paths")
    return data


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--old", type=Path, required=True, help="OpenAPI JSON of release N-1")
    parser.add_argument("--new", type=Path, required=True, help="OpenAPI JSON of release N")
    args = parser.parse_args(argv)
    try:
        report = check(load_doc(args.old), load_doc(args.new))
    except (OSError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    for c in report.warnings:
        print(f"WARN {c} (N-1 clients must tolerate the new value)")
    for c in report.breaking:
        print(f"BREAKING {c}")
    if report.breaking:
        print(f"{len(report.breaking)} breaking change(s): N-1 clients would fail (AC-50.5)")
        return 1
    print("compatible: no N-1 client would see a breaking change")
    return 0


if __name__ == "__main__":
    sys.exit(main())
