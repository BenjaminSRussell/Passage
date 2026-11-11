"""Tests for verify_paths.py (PSG-50). Run: python3 -m unittest discover -s load/tools"""

import contextlib
import io
import tempfile
import unittest
from pathlib import Path

import verify_paths as vp
from openapi_routes import openapi_routes, route_regex

SPEC = """openapi: 3.1.0
paths:
  /v1/ledger/journal-entries:
    post:
      operationId: postEntry
  /v1/planning/plans/{planCode}:run:
    post:
      operationId: runPlan
  /v1/data/{entity}:
    get:
      operationId: listRecords
components:
  schemas:
    post:
      type: object
"""

SCRIPT = """
const PATHS = {
  journalEntries: '/api/v1/ledger/journal-entries', // ledger postEntry
  planRun: '/api/v1/planning/plans', // runPlan: /{planCode}:run
  data: '/api/v1/data',
};

// not served yet
const UNVERIFIED_PATHS = {
  scans: '/api/v1/warehouse/scans',
  close: '/api/v1/inventory/close',
};

export function s1() {
  http.post(`${BASE}${PATHS.journalEntries}`, body, { headers });
  http.post(
    `${BASE}${PATHS.planRun}/${POOL.mrp.planCode}:run`,
    JSON.stringify({ note: `x ${y}` }),
  );
  http.get(`${BASE}${PATHS.data}/${POOL.entity}?limit=50`);
  http.post(`${BASE}${UNVERIFIED_PATHS.scans}`, body);
  let path = PATHS.journalEntries;
  if (action !== 'post') {
    path = action === 'close' ? UNVERIFIED_PATHS.close : UNVERIFIED_PATHS.scans;
  }
  http.post(`${BASE}${path}`, body);
}
"""


def routes(spec: str = SPEC):
    return [(m, route_regex(p), f"svc {m} {p}") for m, p in openapi_routes(spec)]


class TestExtract(unittest.TestCase):
    def test_objects_read_with_trailing_comments(self) -> None:
        objects = vp.extract_objects(SCRIPT)
        self.assertEqual(objects["PATHS"]["journalEntries"], "/api/v1/ledger/journal-entries")
        self.assertEqual(sorted(objects["UNVERIFIED_PATHS"]), ["close", "scans"])

    def test_calls_carry_method_suffix_and_drop_the_query(self) -> None:
        calls = [(c.obj, c.key, c.method, c.path) for c in vp.extract_calls(SCRIPT)]
        self.assertEqual(
            calls[:4],
            [
                ("PATHS", "journalEntries", "POST", "/api/v1/ledger/journal-entries"),
                ("PATHS", "planRun", "POST", "/api/v1/planning/plans/{param}:run"),
                ("PATHS", "data", "GET", "/api/v1/data/{param}"),
                ("UNVERIFIED_PATHS", "scans", "POST", "/api/v1/warehouse/scans"),
            ],
        )

    def test_variable_url_expands_to_every_assigned_entry(self) -> None:
        last = [(c.obj, c.key) for c in vp.extract_calls(SCRIPT) if c.line == vp.extract_calls(SCRIPT)[-1].line]
        self.assertEqual(
            last,
            [("PATHS", "journalEntries"), ("UNVERIFIED_PATHS", "close"), ("UNVERIFIED_PATHS", "scans")],
        )

    def test_missing_paths_object_is_an_error(self) -> None:
        with self.assertRaises(vp.ScriptError):
            vp.extract_objects("const OTHER = {\n};")

    def test_unreadable_entry_is_an_error(self) -> None:
        with self.assertRaises(vp.ScriptError):
            vp.extract_objects("const PATHS = {\n  a: `/api/v1/${x}`,\n};")

    def test_url_outside_the_objects_is_an_error(self) -> None:
        with self.assertRaises(vp.ScriptError):
            vp.extract_calls("const PATHS = {\n};\nhttp.get(`${BASE}/api/v1/raw`);")

    def test_undefined_entry_is_an_error(self) -> None:
        with self.assertRaises(vp.ScriptError):
            vp.extract_calls("const PATHS = {\n};\nhttp.get(`${BASE}${PATHS.nope}`);")

    def test_unassigned_variable_is_an_error(self) -> None:
        with self.assertRaises(vp.ScriptError):
            vp.extract_calls("const PATHS = {\n};\nhttp.get(`${BASE}${url}`);")


class TestCheck(unittest.TestCase):
    def test_script_matching_the_spec_passes(self) -> None:
        self.assertEqual(vp.check(SCRIPT, routes()), [])

    def test_wrong_method_fails_even_when_the_path_exists(self) -> None:
        script = SCRIPT.replace("http.get(`${BASE}${PATHS.data}", "http.post(`${BASE}${PATHS.data}")
        failures = vp.check(script, routes())
        self.assertEqual(len(failures), 1)
        self.assertIn("POST /api/v1/data/{param} (PATHS.data) has no route", failures[0])

    def test_renamed_route_fails(self) -> None:
        failures = vp.check(SCRIPT, routes(SPEC.replace("journal-entries", "journals")))
        # Both the direct call and the variable call that can resolve to PATHS.journalEntries fail.
        self.assertEqual(len(failures), 2)
        self.assertTrue(all("PATHS.journalEntries" in f for f in failures))

    def test_published_unverified_route_fails_until_moved(self) -> None:
        spec = SPEC.replace("components:", "  /v1/inventory/close:\n    post:\n      operationId: close\ncomponents:")
        failures = vp.check(SCRIPT, routes(spec))
        self.assertEqual(len(failures), 1)
        self.assertIn("UNVERIFIED_PATHS.close) now exists as svc POST /v1/inventory/close", failures[0])

    def test_unused_entry_fails(self) -> None:
        script = SCRIPT.replace("  data: '/api/v1/data',\n", "  data: '/api/v1/data',\n  stale: '/api/v1/x',\n")
        self.assertEqual(vp.check(script, routes()), ["PATHS.stale is declared but no request uses it"])


GATEWAY = """apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: ledger
spec:
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /api/v1/ledger
      filters:
        - type: URLRewrite
          urlRewrite:
            path:
              type: ReplacePrefixMatch
              replacePrefixMatch: /v1/ledger
    - matches:
        - path:
            type: PathPrefix
            value: /v1/ledger
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: planning
spec:
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: "/api/v1/planning/"
"""


class TestGateway(unittest.TestCase):
    def _prefixes(self, text: str = GATEWAY) -> list[str]:
        with tempfile.TemporaryDirectory() as tmp:
            (Path(tmp) / "httproutes.yaml").write_text(text, encoding="utf-8")
            (Path(tmp) / "notes.txt").write_text("type: PathPrefix\nvalue: /api/v1/data\n", encoding="utf-8")
            return vp.gateway_prefixes(Path(tmp))

    def test_reads_public_prefixes_only(self) -> None:
        # The internal /v1/ledger match and non-YAML files are ignored; quotes and a trailing slash are dropped.
        self.assertEqual(self._prefixes(), ["/api/v1/ledger", "/api/v1/planning"])

    def test_unrouted_paths_call_fails_and_unverified_calls_are_skipped(self) -> None:
        failures = vp.check_gateway(SCRIPT, self._prefixes())
        self.assertEqual(len(failures), 1)
        self.assertIn("GET /api/v1/data/{param} (PATHS.data) has no gateway route", failures[0])

    def test_prefix_matches_whole_segments(self) -> None:
        script = SCRIPT.replace("'/api/v1/ledger/journal-entries'", "'/api/v1/ledgerx/journal-entries'")
        failures = vp.check_gateway(script, ["/api/v1/ledger", "/api/v1/planning", "/api/v1/data"])
        self.assertEqual(len(failures), 2)  # the direct call and the variable call
        self.assertTrue(all("PATHS.journalEntries" in f for f in failures))

    def test_exact_prefix_is_routed(self) -> None:
        script = "const PATHS = {\n  a: '/api/v1/ledger',\n};\nhttp.get(`${BASE}${PATHS.a}`);"
        self.assertEqual(vp.check_gateway(script, ["/api/v1/ledger"]), [])

    def test_this_branch_gateway_layout_is_readable(self) -> None:
        prefixes = vp.gateway_prefixes(vp.ROOT / "deploy" / "gateway")
        self.assertIn("/api/v1/org", prefixes)
        self.assertTrue(all(p.startswith("/api/v1/") for p in prefixes))

    def test_this_branch_gateway_forwards_every_campaign_call(self) -> None:
        prefixes = vp.gateway_prefixes(vp.ROOT / "deploy" / "gateway")
        self.assertEqual(vp.check_gateway(vp.DEFAULT_SCENARIOS.read_text(encoding="utf-8"), prefixes), [])


class TestMain(unittest.TestCase):
    def _run(self, script: str, spec: str = SPEC, gateway: str | None = None) -> int:
        with tempfile.TemporaryDirectory() as tmp:
            api = Path(tmp) / "services" / "svc" / "api"
            api.mkdir(parents=True)
            (api / "openapi.yaml").write_text(spec, encoding="utf-8")
            js = Path(tmp) / "scenarios.js"
            js.write_text(script, encoding="utf-8")
            argv = ["--scenarios", str(js), "--services", str(Path(tmp) / "services")]
            if gateway is not None:
                gw = Path(tmp) / "gateway"
                gw.mkdir()
                (gw / "httproutes.yaml").write_text(gateway, encoding="utf-8")
                argv += ["--gateway", str(gw)]
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                return vp.main(argv)

    def test_exit_codes(self) -> None:
        self.assertEqual(self._run(SCRIPT), 0)
        self.assertEqual(self._run(SCRIPT, SPEC.replace("journal-entries", "journals")), 1)
        self.assertEqual(self._run("no objects here"), 2)
        self.assertEqual(self._run(SCRIPT, "openapi: 3.1.0\n"), 2)

    def test_gateway_exit_codes(self) -> None:
        routed = GATEWAY.replace("value: /api/v1/ledger", "value: /api/v1/ledger\n        - path:\n"
                                 "            type: PathPrefix\n            value: /api/v1/data")
        self.assertEqual(self._run(SCRIPT, gateway=routed), 0)
        self.assertEqual(self._run(SCRIPT, gateway=GATEWAY), 1)  # /api/v1/data is not forwarded
        self.assertEqual(self._run(SCRIPT, gateway="kind: HTTPRoute\n"), 2)  # no public prefix at all


class TestCampaignOnThisBranch(unittest.TestCase):
    """The real scenarios.js against the real service specs: the campaign cannot drift from the APIs."""

    def test_campaign_routes_match_the_services(self) -> None:
        text = vp.DEFAULT_SCENARIOS.read_text(encoding="utf-8")
        service_routes = vp.all_routes()
        self.assertGreater(len(service_routes), 100, "route reader found no services")
        calls = vp.extract_calls(text)
        # S1-S7 issue at least these many distinct requests; a parse that drops calls must not pass.
        self.assertGreaterEqual(len({(c.method, c.path) for c in calls}), 12)
        self.assertEqual(vp.check(text, service_routes), [])


if __name__ == "__main__":
    unittest.main()
