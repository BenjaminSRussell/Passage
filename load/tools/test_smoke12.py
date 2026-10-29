"""Validate the fx-smoke-12 fixture (PSG-50, PRD 9.50.9). Run: python3 -m unittest discover -s load/tools

Besides the shape, every step is resolved against the OpenAPI files of the services on this
branch: a step either names a route that exists (method and path, through the gateway's
/api/v1/<prefix> -> /v1/<prefix> rewrite) or carries an 'unverified' reason and really has no
route. When an owning wave publishes a missing route, the second check fails until the marker is
removed, so the fixture cannot silently drift from the services.
"""

import json
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = ROOT / "load" / "fixtures" / "smoke-12.json"
SERVICES = ROOT / "services"
ALLOWED_METHODS = {"GET", "POST", "PUT", "PATCH", "DELETE"}
PATH_RE = re.compile(r"^/api/v1/[a-z0-9-]+(/[A-Za-z0-9{}\-:.]+)*$")

# The twelve processes named in PRD 9.50.9, in the PRD's order.
REQUIRED_NAMES = [
    "Purchase to pay",
    "Order to cash",
    "Production order to RAF",
    "Journal post",
    "Bank reconciliation",
    "Asset depreciation",
    "Project invoice",
    "Payroll run",
    "Statutory report",
    "Dashboard",
    "Copilot query",
    "Mobile pick",
]


def load() -> dict:
    with FIXTURE.open(encoding="utf-8") as fh:
        return json.load(fh)


def openapi_routes(text: str) -> list[tuple[str, str]]:
    """(METHOD, path) pairs from the top-level `paths:` block of an OpenAPI YAML file.

    Stdlib only, so this reads the block by indentation instead of parsing YAML: a path key is
    the first indentation level under `paths:`, its operations the next one.
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
        elif current and indent > path_indent and key in {"get", "post", "put", "patch", "delete"}:
            routes.append((key.upper(), current))
    return routes


def route_regex(path: str) -> re.Pattern[str]:
    """A template segment like {id} or {command} matches one path piece (no '/' and no ':')."""
    parts = re.split(r"(\{[^}]+\})", path)
    return re.compile("^" + "".join("[^/:]+" if p.startswith("{") else re.escape(p) for p in parts) + "$")


def all_routes() -> list[tuple[str, re.Pattern[str], str]]:
    found = []
    for spec in sorted(SERVICES.glob("*/api/openapi.yaml")):
        for method, path in openapi_routes(spec.read_text(encoding="utf-8")):
            found.append((method, route_regex(path), f"{spec.parent.parent.name} {method} {path}"))
    return found


def resolve(routes, method: str, gateway_path: str) -> str | None:
    """The matching service route for a gateway path, or None."""
    if not gateway_path.startswith("/api/v1/"):
        return None
    service_path = gateway_path[len("/api"):]
    for m, rx, label in routes:
        if m == method and rx.match(service_path):
            return label
    return None


class TestRouteResolver(unittest.TestCase):
    def test_reads_paths_and_methods(self) -> None:
        spec = (
            "openapi: 3.1.0\npaths:\n  /v1/x/orders:\n    get:\n      operationId: a\n"
            "    post:\n      operationId: b\n  '/v1/x/orders/{id}:{command}':\n    post:\n"
            "      operationId: c\ncomponents:\n  schemas:\n    get:\n      type: object\n"
        )
        self.assertEqual(
            openapi_routes(spec),
            [("GET", "/v1/x/orders"), ("POST", "/v1/x/orders"), ("POST", "/v1/x/orders/{id}:{command}")],
        )

    def test_template_matches_one_segment(self) -> None:
        rx = route_regex("/v1/x/orders/{id}:{command}")
        self.assertTrue(rx.match("/v1/x/orders/{id}:release"))
        self.assertFalse(rx.match("/v1/x/orders/{id}"))
        self.assertFalse(route_regex("/v1/x/orders/{id}").match("/v1/x/orders/{id}:release"))
        self.assertFalse(route_regex("/v1/x/{id}").match("/v1/x/a/b"))


class TestSmoke12Fixture(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.fixture = load()
        cls.processes = cls.fixture["processes"]
        cls.routes = all_routes()

    def test_services_were_found(self) -> None:
        # Guards the resolver: an empty route list would make every step "unverified".
        self.assertGreater(len(self.routes), 100)

    def test_exactly_twelve_processes_in_prd_order(self) -> None:
        self.assertEqual([p["name"] for p in self.processes], REQUIRED_NAMES)
        self.assertEqual([p["id"] for p in self.processes], [f"P{i:02d}" for i in range(1, 13)])

    def test_every_process_has_steps_and_assertion(self) -> None:
        for p in self.processes:
            with self.subTest(process=p["id"]):
                self.assertTrue(p["steps"], "process has no steps")
                self.assertTrue(p.get("assert"), "process has no final assertion")

    def test_steps_well_formed(self) -> None:
        for p in self.processes:
            actions = [s["action"] for s in p["steps"]]
            self.assertEqual(len(actions), len(set(actions)), f"{p['id']} repeats an action")
            for step in p["steps"]:
                with self.subTest(process=p["id"], action=step.get("action")):
                    self.assertIn(step["method"], ALLOWED_METHODS)
                    self.assertRegex(step["path"], PATH_RE)
                    if "unverified" in step:
                        self.assertTrue(step["unverified"].strip(), "unverified needs a reason")

    def test_verified_steps_exist_in_the_services_openapi(self) -> None:
        for p in self.processes:
            for step in p["steps"]:
                if "unverified" in step:
                    continue
                with self.subTest(process=p["id"], action=step["action"]):
                    self.assertIsNotNone(
                        resolve(self.routes, step["method"], step["path"]),
                        f"{step['method']} {step['path']} has no route; fix it or mark it unverified",
                    )

    def test_unverified_steps_really_have_no_route(self) -> None:
        for p in self.processes:
            for step in p["steps"]:
                if "unverified" not in step:
                    continue
                with self.subTest(process=p["id"], action=step["action"]):
                    self.assertIsNone(
                        resolve(self.routes, step["method"], step["path"]),
                        "route now exists; drop the 'unverified' marker",
                    )

    def test_read_only_processes_do_not_write(self) -> None:
        # The dashboard is a read; the copilot runs in suggest mode, so its only call is the query.
        dashboard = next(p for p in self.processes if p["id"] == "P10")
        self.assertTrue(all(s["method"] == "GET" for s in dashboard["steps"]))
        copilot = next(p for p in self.processes if p["id"] == "P11")
        self.assertEqual(len(copilot["steps"]), 1)


if __name__ == "__main__":
    unittest.main()
