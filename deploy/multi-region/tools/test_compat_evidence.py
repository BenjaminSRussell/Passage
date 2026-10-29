"""Tests for compat_evidence.py. Run: python3 -m unittest discover -s deploy/multi-region/tools"""

import json
import tempfile
import unittest
from pathlib import Path

import compat_evidence as ce


def doc(*paths: str) -> dict:
    return {"openapi": "3.1.0", "paths": {p: {"get": {"responses": {"200": {"description": "ok"}}}} for p in paths}}


def status_doc(*values: str) -> dict:
    """One GET whose 200 body has a `status` enum, to exercise response-enum warnings."""
    schema = {"type": "object", "required": ["status"], "properties": {"status": {"type": "string", "enum": list(values)}}}
    ok = {"description": "ok", "content": {"application/json": {"schema": schema}}}
    return {"openapi": "3.1.0", "paths": {"/a": {"get": {"responses": {"200": ok}}}}}


def write(directory: Path, name: str, content: dict) -> None:
    (directory / f"{name}.json").write_text(json.dumps(content), encoding="utf-8")


class TestEvaluate(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self.prev = self.root / "n1"
        self.cur = self.root / "n"
        self.prev.mkdir()
        self.cur.mkdir()

    def tearDown(self):
        self._tmp.cleanup()

    def by_service(self):
        return {r["service"]: r for r in ce.evaluate(self.prev, self.cur)}

    def test_added_path_is_compatible(self):
        write(self.prev, "org", doc("/a"))
        write(self.cur, "org", doc("/a", "/b"))
        r = self.by_service()["org"]
        self.assertEqual((r["status"], r["changes"]), ("compatible", []))

    def test_removed_path_is_breaking(self):
        write(self.prev, "org", doc("/a", "/b"))
        write(self.cur, "org", doc("/a"))
        r = self.by_service()["org"]
        self.assertEqual(r["status"], "breaking")
        self.assertTrue(any(c.startswith("path-removed") for c in r["changes"]))

    def test_new_service_is_new_not_breaking(self):
        write(self.cur, "ledger", doc("/a"))
        self.assertEqual(self.by_service()["ledger"]["status"], "new")

    def test_removed_service_is_breaking(self):
        write(self.prev, "treasury", doc("/a"))
        r = self.by_service()["treasury"]
        self.assertEqual(r["status"], "breaking")
        self.assertEqual(r["changes"], ["service-removed: no longer published"])

    def test_new_response_enum_value_is_a_warning_not_a_break(self):
        write(self.prev, "org", status_doc("open", "closed"))
        write(self.cur, "org", status_doc("open", "closed", "archived"))
        r = self.by_service()["org"]
        self.assertEqual((r["status"], r["changes"]), ("compatible", []))
        self.assertEqual(len(r["warnings"]), 1)
        self.assertIn("archived", r["warnings"][0])

    def test_unreadable_new_service_is_an_input_error(self):
        (self.cur / "ledger.json").write_text(json.dumps({"openapi": "3.1.0"}), encoding="utf-8")
        with self.assertRaises(ValueError):
            ce.evaluate(self.prev, self.cur)

    def test_results_sorted_by_service(self):
        for name in ("sales", "org", "ledger"):
            write(self.cur, name, doc("/a"))
        self.assertEqual([r["service"] for r in ce.evaluate(self.prev, self.cur)], ["ledger", "org", "sales"])


class TestMain(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self.prev = self.root / "n1"
        self.cur = self.root / "n"
        self.prev.mkdir()
        self.cur.mkdir()
        self.out = self.root / "evidence.json"

    def tearDown(self):
        self._tmp.cleanup()

    def run_main(self):
        return ce.main(
            [
                "--previous", str(self.prev),
                "--current", str(self.cur),
                "--release", "v1.0.0",
                "--commit", "abc123",
                "--out", str(self.out),
            ]
        )

    def test_pass_writes_evidence(self):
        write(self.prev, "org", doc("/a"))
        write(self.cur, "org", doc("/a"))
        self.assertEqual(self.run_main(), 0)
        evidence = json.loads(self.out.read_text(encoding="utf-8"))
        self.assertEqual(evidence["schema"], ce.SCHEMA)
        self.assertEqual((evidence["release"], evidence["commit"]), ("v1.0.0", "abc123"))
        self.assertEqual((evidence["verdict"], evidence["breakingServices"]), ("PASS", 0))

    def test_breaking_fails_and_records_verdict(self):
        write(self.prev, "org", doc("/a", "/b"))
        write(self.cur, "org", doc("/a"))
        self.assertEqual(self.run_main(), 1)
        evidence = json.loads(self.out.read_text(encoding="utf-8"))
        self.assertEqual((evidence["verdict"], evidence["breakingServices"]), ("FAIL", 1))

    def test_warnings_are_kept_in_the_evidence_and_pass(self):
        write(self.prev, "org", status_doc("open"))
        write(self.cur, "org", status_doc("open", "held"))
        self.assertEqual(self.run_main(), 0)
        evidence = json.loads(self.out.read_text(encoding="utf-8"))
        self.assertEqual(evidence["verdict"], "PASS")
        self.assertEqual(len(evidence["services"][0]["warnings"]), 1)

    def test_unwritable_evidence_is_a_usage_error(self):
        write(self.prev, "org", doc("/a"))
        write(self.cur, "org", doc("/a"))
        self.out = self.root / "missing-dir" / "evidence.json"
        self.assertEqual(self.run_main(), 2)

    def test_nothing_in_common_is_a_usage_error(self):
        write(self.cur, "org", doc("/a"))
        self.assertEqual(self.run_main(), 2)

    def test_missing_directory_is_a_usage_error(self):
        self.prev.rmdir()
        self.assertEqual(self.run_main(), 2)

    def test_invalid_json_is_a_usage_error(self):
        write(self.prev, "org", doc("/a"))
        (self.cur / "org.json").write_text("{not json", encoding="utf-8")
        self.assertEqual(self.run_main(), 2)


if __name__ == "__main__":
    unittest.main()
