"""Route reader for the services' OpenAPI files (PSG-50). Shared by test_smoke12.py and verify_paths.py.

Stdlib only, so it reads the top-level `paths:` block by indentation instead of parsing YAML. Gateway
paths are `/api/v1/<prefix>/...`; the gateway rewrites them to the services' `/v1/<prefix>/...`.
"""

from __future__ import annotations

import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SERVICES = ROOT / "services"
OPERATIONS = {"get", "post", "put", "patch", "delete"}


def openapi_routes(text: str) -> list[tuple[str, str]]:
    """(METHOD, path) pairs from the top-level `paths:` block of an OpenAPI YAML file.

    A path key is the first indentation level under `paths:`, its operations the next one.
    """
    routes: list[tuple[str, str]] = []
    in_paths = False
    path_indent: int | None = None
    current: str | None = None
    for line in text.splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        indent = len(line) - len(line.lstrip(" "))
        if indent == 0:
            in_paths = line.startswith("paths:")
            current = None
            continue
        if not in_paths:
            continue
        key = line.strip().split(":", 1)[0] if not line.strip().startswith(("'", '"')) else None
        if path_indent is None:
            path_indent = indent
        if indent == path_indent:
            raw = line.strip()
            if raw.endswith(":"):
                raw = raw[:-1]
            current = raw.strip("'\"") if raw.strip("'\"").startswith("/") else None
        elif current and indent > path_indent and key in OPERATIONS:
            routes.append((key.upper(), current))
    return routes


def route_regex(path: str) -> re.Pattern[str]:
    """A template segment like {id} or {command} matches one path piece (no '/' and no ':')."""
    parts = re.split(r"(\{[^}]+\})", path)
    return re.compile("^" + "".join("[^/:]+" if p.startswith("{") else re.escape(p) for p in parts) + "$")


def all_routes(services: Path = SERVICES) -> list[tuple[str, re.Pattern[str], str]]:
    """(METHOD, path regex, label) for every operation in services/*/api/openapi.yaml."""
    found = []
    for spec in sorted(services.glob("*/api/openapi.yaml")):
        for method, path in openapi_routes(spec.read_text(encoding="utf-8")):
            found.append((method, route_regex(path), f"{spec.parent.parent.name} {method} {path}"))
    return found


def resolve(routes, method: str, gateway_path: str) -> str | None:
    """The label of the service route that serves a gateway path, or None."""
    if not gateway_path.startswith("/api/v1/"):
        return None
    service_path = gateway_path[len("/api"):]
    for m, rx, label in routes:
        if m == method and rx.match(service_path):
            return label
    return None
