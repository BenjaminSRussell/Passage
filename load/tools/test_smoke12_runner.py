"""Tests for smoke12.py. Run: python3 -m unittest discover -s load/tools"""

import json
import os
import re
import tempfile
import unittest
from pathlib import Path
from typing import Any
from unittest import mock

import smoke12


class FakeService:
    """Records calls and answers with a canned status per (method, path template)."""

    def __init__(self, fail_on: str | None = None, fail_status: int = 500) -> None:
        self.calls: list[tuple[str, str, dict[str, str], Any]] = []
        self.fail_on = fail_on
        self.fail_status = fail_status

    def __call__(self, method: str, url: str, headers: Any, body: Any) -> tuple[int | None, Any]:
        self.calls.append((method, url, dict(headers), body))
        if self.fail_on and self.fail_on in url:
            return self.fail_status, {"error": "boom"}
        return 201, {"id": f"obj-{len(self.calls)}"}


def fixture() -> dict[str, Any]:
    return smoke12.load_fixture()


def one_process(steps: list[dict[str, str]], pid: str = "PX") -> dict[str, Any]:
    return {"id": pid, "name": "test", "steps": steps, "assert": "x"}


class TestFillPath(unittest.TestCase):
    def test_no_placeholder_returns_path(self) -> None:
        self.assertEqual(smoke12.fill_path("/api/v1/a/b", {}), "/api/v1/a/b")

    def test_placeholder_filled_from_context(self) -> None:
        self.assertEqual(smoke12.fill_path("/x/{id}:post", {"id": "42"}), "/x/42:post")

    def test_placeholder_without_id_is_none(self) -> None:
        self.assertIsNone(smoke12.fill_path("/x/{id}", {}))

    def test_every_placeholder_is_filled(self) -> None:
        self.assertEqual(smoke12.fill_path("/w/{id}/lines/{n}:confirm", {"id": "w1", "n": 2}), "/w/w1/lines/2:confirm")
        self.assertIsNone(smoke12.fill_path("/w/{id}/lines/{n}:confirm", {"id": "w1"}))

    def test_values_are_one_escaped_segment(self) -> None:
        self.assertEqual(smoke12.fill_path("/x/{id}", {"id": "a/b?c"}), "/x/a%2Fb%3Fc")


class TestRunProcess(unittest.TestCase):
    def test_latest_id_fills_the_next_step(self) -> None:
        # P01 shape: the receipt step must post the receipt just created, not the earlier order.
        svc = FakeService()
        proc = one_process(
            [
                {"action": "create_order", "method": "POST", "path": "/api/v1/a/orders"},
                {"action": "create_receipt", "method": "POST", "path": "/api/v1/a/receipts"},
                {"action": "post_receipt", "method": "POST", "path": "/api/v1/a/receipts/{id}:post"},
            ]
        )
        smoke12.run_process(proc, send=svc, base_url="https://h", token="t", run_id="r", payloads={}, verifier=None)
        self.assertEqual(svc.calls[2][1], "https://h/api/v1/a/receipts/obj-2:post")

    def test_unverified_step_is_not_sent_and_fails_the_process(self) -> None:
        svc = FakeService()
        proc = one_process(
            [
                {"action": "a", "method": "POST", "path": "/api/v1/a"},
                {"action": "b", "method": "POST", "path": "/api/v1/b", "unverified": "no route on main"},
                {"action": "c", "method": "POST", "path": "/api/v1/c"},
            ]
        )
        res = smoke12.run_process(
            proc, send=svc, base_url="https://h", token="t", run_id="r", payloads={}, verifier=lambda c: True
        )
        self.assertEqual([c[1] for c in svc.calls], ["https://h/api/v1/a"])
        self.assertFalse(res.passed)
        self.assertEqual(res.assertion, "SKIPPED")
        self.assertEqual(res.steps[-1].detail, "no route on main, not sent: no route on main")

    def test_params_fill_other_placeholders(self) -> None:
        svc = FakeService()
        proc = one_process(
            [
                {"action": "create", "method": "POST", "path": "/api/v1/w"},
                {"action": "confirm", "method": "POST", "path": "/api/v1/w/{id}/lines/{n}:confirm"},
            ]
        )
        smoke12.run_process(
            proc, send=svc, base_url="https://h", token="t", run_id="r", payloads={}, verifier=None, params={"n": 1}
        )
        self.assertEqual(svc.calls[1][1], "https://h/api/v1/w/obj-1/lines/1:confirm")

    def test_missing_param_fails_without_sending(self) -> None:
        svc = FakeService()
        proc = one_process(
            [
                {"action": "create", "method": "POST", "path": "/api/v1/w"},
                {"action": "confirm", "method": "POST", "path": "/api/v1/w/{id}/lines/{n}:confirm"},
            ]
        )
        res = smoke12.run_process(proc, send=svc, base_url="https://h", token="t", run_id="r", payloads={}, verifier=None)
        self.assertEqual(len(svc.calls), 1)
        self.assertIn("{n}", res.steps[-1].detail)
        self.assertEqual(res.assertion, "SKIPPED")

    def test_id_from_first_step_fills_second(self) -> None:
        svc = FakeService()
        proc = one_process(
            [
                {"action": "create", "method": "POST", "path": "/api/v1/a/things"},
                {"action": "approve", "method": "POST", "path": "/api/v1/a/things/{id}:approve"},
            ]
        )
        res = smoke12.run_process(
            proc, send=svc, base_url="https://h/", token="t", run_id="r1", payloads={}, verifier=None
        )
        self.assertEqual(svc.calls[1][1], "https://h/api/v1/a/things/obj-1:approve")
        self.assertEqual(res.assertion, "UNVERIFIED")
        self.assertFalse(res.passed)

    def test_failed_step_stops_process_and_skips_assertion(self) -> None:
        svc = FakeService(fail_on="/second", fail_status=500)
        proc = one_process(
            [
                {"action": "one", "method": "POST", "path": "/first"},
                {"action": "two", "method": "POST", "path": "/second"},
                {"action": "three", "method": "POST", "path": "/third"},
            ]
        )
        res = smoke12.run_process(
            proc, send=svc, base_url="https://h", token="t", run_id="r", payloads={}, verifier=lambda c: True
        )
        self.assertEqual(len(svc.calls), 2, "third step must not run after a failure")
        self.assertEqual(res.assertion, "SKIPPED")
        self.assertFalse(res.passed)
        self.assertEqual(res.steps[-1].status, 500)

    def test_get_has_no_idempotency_key_and_post_does(self) -> None:
        svc = FakeService()
        proc = one_process(
            [
                {"action": "read", "method": "GET", "path": "/api/v1/r"},
                {"action": "write", "method": "POST", "path": "/api/v1/w"},
            ]
        )
        smoke12.run_process(proc, send=svc, base_url="https://h", token="t", run_id="RUN", payloads={}, verifier=None)
        self.assertNotIn("Idempotency-Key", svc.calls[0][2])
        self.assertEqual(svc.calls[1][2]["Idempotency-Key"], "smoke:RUN:PX:write")

    def test_payload_used_for_post_and_default_empty(self) -> None:
        svc = FakeService()
        proc = one_process(
            [
                {"action": "a", "method": "POST", "path": "/p1"},
                {"action": "b", "method": "POST", "path": "/p2"},
            ]
        )
        smoke12.run_process(
            proc, send=svc, base_url="https://h", token="t", run_id="r", payloads={"a": {"x": 1}}, verifier=None
        )
        self.assertEqual(svc.calls[0][3], {"x": 1})
        self.assertEqual(svc.calls[1][3], {})

    def test_transport_error_is_failed_step(self) -> None:
        def down(method: str, url: str, headers: Any, body: Any) -> tuple[int | None, Any]:
            return None, None

        proc = one_process([{"action": "a", "method": "GET", "path": "/p"}])
        res = smoke12.run_process(proc, send=down, base_url="https://h", token="t", run_id="r", payloads={}, verifier=None)
        self.assertFalse(res.steps[0].ok)
        self.assertEqual(res.assertion, "SKIPPED")

    def test_verifier_decides_pass_and_fail(self) -> None:
        proc = one_process([{"action": "a", "method": "GET", "path": "/p"}])
        ok = smoke12.run_process(
            proc, send=FakeService(), base_url="https://h", token="t", run_id="r", payloads={}, verifier=lambda c: True
        )
        bad = smoke12.run_process(
            proc, send=FakeService(), base_url="https://h", token="t", run_id="r", payloads={}, verifier=lambda c: False
        )
        self.assertTrue(ok.passed)
        self.assertEqual(bad.assertion, "FAILED")
        self.assertFalse(bad.passed)

    def test_verifier_sees_captured_ids(self) -> None:
        seen: dict[str, Any] = {}

        def verify(ctx: dict[str, Any]) -> bool:
            seen.update(ctx)
            return True

        proc = one_process([{"action": "a", "method": "POST", "path": "/p"}])
        smoke12.run_process(proc, send=FakeService(), base_url="https://h", token="t", run_id="r", payloads={}, verifier=verify)
        self.assertEqual(seen, {"id": "obj-1"})


def routed_fixture() -> dict[str, Any]:
    """The fixture without the steps that have no route on main, as it will be once the owners publish them."""
    fx = fixture()
    for proc in fx["processes"]:
        proc["steps"] = [{k: v for k, v in s.items() if k != "unverified"} for s in proc["steps"]]
    return fx


class TestRunSuite(unittest.TestCase):
    def test_all_twelve_run_in_order_with_fake_service(self) -> None:
        svc = FakeService()
        report = smoke12.run_suite(fixture(), send=svc, base_url="https://h", token="t", run_id="R", params={"n": 1})
        self.assertEqual([p["id"] for p in report["processes"]], [f"P{i:02d}" for i in range(1, 13)])
        self.assertFalse(report["passed"], "no verifiers registered, so the run must not pass")
        unrouted = {p["id"] for p in fixture()["processes"] if any("unverified" in s for s in p["steps"])}
        for p in report["processes"]:
            with self.subTest(process=p["id"]):
                self.assertEqual(p["assertion"], "SKIPPED" if p["id"] in unrouted else "UNVERIFIED")
        sent = {c[1] for c in svc.calls}
        # No step marked unverified in the fixture was sent (placeholders match any one path segment).
        unverified = [s["path"] for p in fixture()["processes"] for s in p["steps"] if "unverified" in s]
        self.assertTrue(unverified, "fixture has no unverified step left; this check needs another example")
        for path in unverified:
            pattern = re.compile("https://h" + re.sub(r"\\\{[^}]*\\\}", "[^/]+", re.escape(path)))
            with self.subTest(path=path):
                self.assertFalse([u for u in sent if pattern.fullmatch(u)], "a step with no route was sent")

    def test_real_fixture_cannot_pass_while_routes_are_missing(self) -> None:
        fx = fixture()
        verifiers = {p["id"]: (lambda c: True) for p in fx["processes"]}
        report = smoke12.run_suite(
            fx, send=FakeService(), base_url="https://h", token="t", verifiers=verifiers, params={"n": 1}
        )
        self.assertFalse(report["passed"])
        failed = sorted(p["id"] for p in report["processes"] if not p["passed"])
        unrouted = sorted(p["id"] for p in fx["processes"] if any("unverified" in s for s in p["steps"]))
        self.assertEqual(failed, unrouted)

    def test_all_verified_passes_once_every_route_exists(self) -> None:
        fx = routed_fixture()
        verifiers = {p["id"]: (lambda c: True) for p in fx["processes"]}
        report = smoke12.run_suite(
            fx, send=FakeService(), base_url="https://h", token="t", verifiers=verifiers, params={"n": 1}
        )
        self.assertTrue(report["passed"])
        self.assertEqual(sum(p["passed"] for p in report["processes"]), 12)

    def test_allow_unverified_passes_when_steps_succeed(self) -> None:
        report = smoke12.run_suite(
            routed_fixture(), send=FakeService(), base_url="https://h", token="t", allow_unverified=True, params={"n": 1}
        )
        self.assertTrue(report["passed"])

    def test_allow_unverified_does_not_excuse_a_missing_route(self) -> None:
        report = smoke12.run_suite(
            fixture(), send=FakeService(), base_url="https://h", token="t", allow_unverified=True, params={"n": 1}
        )
        self.assertFalse(report["passed"])

    def test_allow_unverified_still_fails_on_step_failure(self) -> None:
        report = smoke12.run_suite(
            routed_fixture(),
            send=FakeService(fail_on="/api/v1/ledger/journal-entries", fail_status=422),
            base_url="https://h",
            token="t",
            allow_unverified=True,
            params={"n": 1},
        )
        self.assertFalse(report["passed"])
        p4 = next(p for p in report["processes"] if p["id"] == "P04")
        self.assertEqual(p4["assertion"], "SKIPPED")
        self.assertEqual(p4["steps"][0]["status"], 422)

    def test_one_failed_verifier_fails_run(self) -> None:
        fx = routed_fixture()
        verifiers = {p["id"]: (lambda c: True) for p in fx["processes"]}
        verifiers["P09"] = lambda c: False
        report = smoke12.run_suite(
            fx, send=FakeService(), base_url="https://h", token="t", verifiers=verifiers, params={"n": 1}
        )
        self.assertFalse(report["passed"])
        p9 = next(p for p in report["processes"] if p["id"] == "P09")
        self.assertEqual(p9["assertion"], "FAILED")

    def test_report_is_json_serialisable(self) -> None:
        report = smoke12.run_suite(fixture(), send=FakeService(), base_url="https://h", token="t", run_id="R")
        json.dumps(report)
        self.assertEqual(report["runId"], "R")


class TestMain(unittest.TestCase):
    def test_missing_token_is_a_usage_error(self) -> None:
        with mock.patch.dict(os.environ, {"SMOKE_TOKEN": ""}):
            self.assertEqual(smoke12.main(["--base-url", "https://h"]), 2)

    def test_base_url_must_be_http(self) -> None:
        with mock.patch.dict(os.environ, {"SMOKE_TOKEN": "t"}):
            self.assertEqual(smoke12.main(["--base-url", "file:///etc/passwd"]), 2)

    def test_params_must_be_an_object(self) -> None:
        with tempfile.TemporaryDirectory() as tmp, mock.patch.dict(os.environ, {"SMOKE_TOKEN": "t"}):
            params = Path(tmp) / "params.json"
            params.write_text("[1]", encoding="utf-8")
            self.assertEqual(smoke12.main(["--base-url", "https://h", "--params", str(params)]), 2)

    def test_run_writes_report_and_fails_without_verifiers(self) -> None:
        with tempfile.TemporaryDirectory() as tmp, mock.patch.dict(os.environ, {"SMOKE_TOKEN": "t"}):
            out = Path(tmp) / "report.json"
            with mock.patch.object(smoke12, "http_send", FakeService()):
                code = smoke12.main(["--base-url", "https://h", "--report", str(out)])
            self.assertEqual(code, 1)
            self.assertFalse(json.loads(out.read_text(encoding="utf-8"))["passed"])


class TestHttpSend(unittest.TestCase):
    def test_connection_errors_are_a_failed_step_not_a_crash(self) -> None:
        with mock.patch.object(smoke12.urllib.request, "urlopen", side_effect=ConnectionResetError()):
            self.assertEqual(smoke12.http_send("GET", "https://h/x", {}, None), (None, None))


if __name__ == "__main__":
    unittest.main()
