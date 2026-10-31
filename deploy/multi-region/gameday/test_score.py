"""Tests for score.py. Expected values are hand-derived from the timestamps (no engine output is reused)."""

import contextlib
import io
import json
import tempfile
import unittest
from pathlib import Path

import score

PASSED_VERIFY = {
    "passed": True,
    "checks": [
        {"name": "offsets cluster", "passed": True, "detail": "x"},
        {"name": "ledger tenant-a", "passed": True, "detail": "x"},
    ],
}


def timeline(fenced="2026-11-03T14:05:00Z", verified="2026-11-03T14:41:30Z", lag=42.0):
    return {"fencedAt": fenced, "dnsVerifiedAt": verified, "lagAtFenceSeconds": lag}


def by_name(checks):
    return {c.name: c for c in checks}


class TestTargets(unittest.TestCase):
    def test_chart_defaults_are_the_prd_targets(self):
        chart = score.values_reader.parse(score.CHART_VALUES.read_text(encoding="utf-8"))
        # PRD 9.50: RTO <= 1 h = 3600 s, RPO <= 5 min = 300 s.
        self.assertEqual(score.targets(chart), (3600.0, 300.0))

    def test_region_file_overrides_the_default(self):
        chart = {"failover": {"rtoTargetMinutes": 60, "rpoTargetSeconds": 300}}
        region = {"failover": {"rtoTargetMinutes": 30}}
        self.assertEqual(score.targets(chart, region), (1800.0, 300.0))

    def test_missing_or_bad_target_is_an_input_error(self):
        with self.assertRaises(score.InputError):
            score.targets({"failover": {"rpoTargetSeconds": 300}})
        with self.assertRaises(score.InputError):
            score.targets({"failover": {"rtoTargetMinutes": "60", "rpoTargetSeconds": 300}})


class TestScore(unittest.TestCase):
    def test_typical_game_day_passes(self):
        # 14:05:00 -> 14:41:30 is 36 min 30 s = 2190 s.
        checks = by_name(score.score(timeline(), PASSED_VERIFY, 3600, 300))
        self.assertTrue(all(c.passed for c in checks.values()))
        self.assertIn("2190 s", checks["RTO"].detail)
        self.assertIn("42 s", checks["RPO"].detail)

    def test_rto_limit_is_inclusive(self):
        at = by_name(score.score(timeline(verified="2026-11-03T15:05:00Z"), PASSED_VERIFY, 3600, 300))
        over = by_name(score.score(timeline(verified="2026-11-03T15:05:01Z"), PASSED_VERIFY, 3600, 300))
        self.assertTrue(at["RTO"].passed)  # exactly 3600 s
        self.assertFalse(over["RTO"].passed)  # 3601 s
        self.assertIn("3601 s", over["RTO"].detail)

    def test_rpo_limit_is_inclusive(self):
        self.assertTrue(by_name(score.score(timeline(lag=300), PASSED_VERIFY, 3600, 300))["RPO"].passed)
        self.assertFalse(by_name(score.score(timeline(lag=300.5), PASSED_VERIFY, 3600, 300))["RPO"].passed)

    def test_offsets_are_honoured(self):
        # 15:05+01:00 is 14:05Z; to 14:40Z is 35 min = 2100 s.
        checks = by_name(score.score(
            timeline(fenced="2026-11-03T15:05:00+01:00", verified="2026-11-03T14:40:00Z"), PASSED_VERIFY, 3600, 300))
        self.assertIn("2100 s", checks["RTO"].detail)
        # Across midnight: 23:50Z -> 00:20Z next day is 1800 s.
        checks = by_name(score.score(
            timeline(fenced="2026-11-03T23:50:00Z", verified="2026-11-04T00:20:00Z"), PASSED_VERIFY, 3600, 300))
        self.assertIn("1800 s", checks["RTO"].detail)

    def test_fast_failover_with_data_loss_still_fails(self):
        lost = {
            "passed": False,
            "checks": [
                {"name": "ledger tenant-a", "passed": True, "detail": "x"},
                {"name": "sequence INV-2026", "passed": False, "detail": "gap"},
            ],
        }
        checks = by_name(score.score(timeline(lag=0), lost, 3600, 300))
        self.assertTrue(checks["RTO"].passed and checks["RPO"].passed)
        self.assertFalse(checks["zero committed loss"].passed)
        self.assertIn("sequence INV-2026", checks["zero committed loss"].detail)

    def test_verify_report_must_be_complete_and_consistent(self):
        cases = {
            "missing": None,
            "empty checks": {"passed": True, "checks": []},
            "marked failed": {"passed": False, "checks": PASSED_VERIFY["checks"]},
            "check without passed": {"passed": True, "checks": [{"name": "rows chain"}]},
            "not an object": ["passed"],
        }
        for label, report in cases.items():
            with self.subTest(label):
                self.assertFalse(by_name(score.score(timeline(), report, 3600, 300))["zero committed loss"].passed)

    def test_malformed_timeline_is_an_input_error(self):
        bad = [
            timeline(fenced="2026-11-03T14:05:00"),  # no offset
            timeline(verified="yesterday"),
            timeline(verified="2026-11-03T14:04:59Z"),  # before the fence
            timeline(lag=-1),
            timeline(lag=True),
            timeline(lag=float("nan")),
            {"fencedAt": "2026-11-03T14:05:00Z", "dnsVerifiedAt": "2026-11-03T14:41:30Z"},
        ]
        for t in bad:
            with self.subTest(t=t), self.assertRaises(score.InputError):
                score.score(t, PASSED_VERIFY, 3600, 300)


class TestCli(unittest.TestCase):
    def run_cli(self, tl, report, *extra):
        with tempfile.TemporaryDirectory() as tmp:
            d = Path(tmp)
            (d / "t.json").write_text(json.dumps(tl), encoding="utf-8")
            (d / "v.json").write_text(json.dumps(report), encoding="utf-8")
            out = io.StringIO()
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
                code = score.main(["--timeline", str(d / "t.json"), "--verify-report", str(d / "v.json"),
                                   "--report", str(d / "s.json"), *extra])
            written = json.loads((d / "s.json").read_text()) if (d / "s.json").exists() else None
            return code, out.getvalue(), written

    def test_exit_codes(self):
        code, out, written = self.run_cli(timeline(), PASSED_VERIFY)
        self.assertEqual(code, 0)
        self.assertIn("GAME DAY PASSED", out)
        self.assertTrue(written["passed"])
        code, out, written = self.run_cli(timeline(verified="2026-11-03T16:00:00Z"), PASSED_VERIFY)
        self.assertEqual(code, 1)
        self.assertFalse(written["passed"])
        code, _, written = self.run_cli(timeline(fenced="not a time"), PASSED_VERIFY)
        self.assertEqual(code, 2)
        self.assertIsNone(written)

    def test_region_values_tighten_the_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            region = Path(tmp) / "r.yaml"
            region.write_text("failover:\n  rtoTargetMinutes: 30\n", encoding="utf-8")
            # 2190 s is within 60 min but not within 30 min (1800 s).
            code, out, _ = self.run_cli(timeline(), PASSED_VERIFY, "--region-values", str(region))
        self.assertEqual(code, 1)
        self.assertIn("target <= 1800 s", out)


if __name__ == "__main__":
    unittest.main()
