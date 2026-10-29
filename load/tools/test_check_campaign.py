"""Tests for check_campaign.py. Run: python3 -m unittest discover -s load/tools"""

import copy
import json
import unittest
from pathlib import Path

import check_campaign as cc

SLO_PATH = Path(__file__).resolve().parent.parent / "slo" / "slo.json"


def load_slo() -> dict:
    with SLO_PATH.open(encoding="utf-8") as fh:
        return json.load(fh)


def passing_summary() -> dict:
    """A summary where every SLO is comfortably met."""
    metrics = {}
    for s in load_slo()["slos"]:
        if "op" in s:
            key = f"http_req_duration{{op:{s['op']}}}"
            metrics[key] = {"p(95)": 1.0, "p(99)": 1.0}
            metrics[f"http_req_failed{{op:{s['op']}}}"] = {"rate": 0.0, "passes": 10, "fails": 0}
        elif "minPerHour" in s:
            # Counter: k6 reports count and a per-second rate; 10% above target.
            metrics[s["metric"]] = {"count": 1, "rate": s["minPerHour"] * 1.1 / 3600}
        elif "minCount" in s:
            metrics[s["metric"]] = {"count": s["minCount"], "rate": 0.0001}
        elif "metric" in s:
            metrics[s["metric"]] = {"p(95)": 1.0, "max": 1.0}
    return {"metrics": metrics}


class TestBuildChecks(unittest.TestCase):
    def test_every_slo_row_produces_checks(self) -> None:
        checks = cc.build_checks(load_slo())
        labels = [c.label for c in checks]
        self.assertIn("journal_post p99 (ms)", labels)
        self.assertIn("entity_read p95 (ms)", labels)
        self.assertIn("entity_read p99 (ms)", labels)
        self.assertIn("mrp_50k max (s)", labels)
        self.assertIn("lakehouse_lag p95 (s)", labels)

    def test_journal_post_limits_match_prd(self) -> None:
        checks = {c.label: c for c in cc.build_checks(load_slo())}
        self.assertEqual(checks["journal_post p99 (ms)"].limit, 2000.0)
        self.assertEqual(checks["journal_post error rate"].limit, 0.0005)
        self.assertEqual(checks["pricing p95 (ms)"].limit, 20.0)


class TestEvaluate(unittest.TestCase):
    def test_passing_summary_passes_all(self) -> None:
        slo = load_slo()
        rows = cc.evaluate(passing_summary(), slo)
        self.assertTrue(rows)
        self.assertTrue(all(r.passed for r in rows))

    def test_p99_breach_fails_only_that_row(self) -> None:
        summary = passing_summary()
        summary["metrics"]["http_req_duration{op:journal_post}"]["p(99)"] = 2500.0
        rows = {r.label: r for r in cc.evaluate(summary, load_slo())}
        self.assertFalse(rows["journal_post p99 (ms)"].passed)
        self.assertTrue(rows["entity_read p99 (ms)"].passed)

    def test_limit_is_strict(self) -> None:
        summary = passing_summary()
        summary["metrics"]["http_req_duration{op:pricing}"]["p(95)"] = 20.0
        rows = {r.label: r for r in cc.evaluate(summary, load_slo())}
        self.assertFalse(rows["pricing p95 (ms)"].passed)

    def test_missing_metric_fails(self) -> None:
        summary = passing_summary()
        del summary["metrics"]["http_req_duration{op:mobile_scan}"]
        rows = {r.label: r for r in cc.evaluate(summary, load_slo())}
        self.assertIsNone(rows["mobile_scan p99 (ms)"].value)
        self.assertFalse(rows["mobile_scan p99 (ms)"].passed)

    def test_error_rate_reads_handlesummary_value_key(self) -> None:
        summary = passing_summary()
        summary["metrics"]["http_req_failed{op:journal_post}"] = {"value": 0.01}
        rows = {r.label: r for r in cc.evaluate(summary, load_slo())}
        self.assertFalse(rows["journal_post error rate"].passed)

    def test_nested_values_layout_is_accepted(self) -> None:
        summary = passing_summary()
        summary["metrics"]["http_req_duration{op:pricing}"] = {"values": {"p(95)": 5.0, "p(99)": 9.0}}
        rows = {r.label: r for r in cc.evaluate(summary, load_slo())}
        self.assertEqual(rows["pricing p95 (ms)"].value, 5.0)
        self.assertTrue(rows["pricing p95 (ms)"].passed)

    def test_mrp_max_over_limit_fails(self) -> None:
        summary = passing_summary()
        summary["metrics"]["mrp_run_seconds"] = {"max": 601.0}
        rows = {r.label: r for r in cc.evaluate(summary, load_slo())}
        self.assertFalse(rows["mrp_50k max (s)"].passed)

    def test_non_numeric_stat_counts_as_missing(self) -> None:
        summary = passing_summary()
        summary["metrics"]["http_req_duration{op:pricing}"] = {"p(95)": "fast"}
        rows = {r.label: r for r in cc.evaluate(summary, load_slo())}
        self.assertIsNone(rows["pricing p95 (ms)"].value)

    def test_metrics_not_an_object_raises(self) -> None:
        with self.assertRaises(ValueError):
            cc.evaluate({"metrics": []}, load_slo())


class TestThroughput(unittest.TestCase):
    def test_throughput_targets_match_prd(self) -> None:
        checks = {c.label: c for c in cc.build_checks(load_slo())}
        self.assertEqual(checks["journal_throughput per hour"].limit, 1_000_000.0)
        self.assertEqual(checks["order_throughput per hour"].limit, 100_000.0)
        self.assertEqual(checks["journal_throughput per hour"].comparison, ">=")

    def test_rate_below_target_fails(self) -> None:
        summary = passing_summary()
        # 34 entries/min x 500 lines arriving for only 95% of the run: 969k lines/h.
        summary["metrics"]["journal_lines_posted"]["rate"] = 0.95 * 34 * 500 / 60
        rows = {r.label: r for r in cc.evaluate(summary, load_slo())}
        self.assertFalse(rows["journal_throughput per hour"].passed)
        self.assertAlmostEqual(rows["journal_throughput per hour"].value, 969_000.0)

    def test_target_met_exactly_passes(self) -> None:
        summary = passing_summary()
        summary["metrics"]["order_lines_accepted"]["rate"] = 100_000 / 3600
        rows = {r.label: r for r in cc.evaluate(summary, load_slo())}
        self.assertTrue(rows["order_throughput per hour"].passed)

    def test_scale_lowers_only_throughput_targets(self) -> None:
        checks = {c.label: c for c in cc.build_checks(load_slo(), scale=0.1)}
        self.assertEqual(checks["journal_throughput per hour"].limit, 100_000.0)
        self.assertEqual(checks["journal_post p99 (ms)"].limit, 2000.0)

    def test_scale_must_be_positive(self) -> None:
        with self.assertRaises(ValueError):
            cc.build_checks(load_slo(), scale=0)

    def test_mrp_without_a_completed_run_fails(self) -> None:
        summary = passing_summary()
        summary["metrics"]["mrp_runs_completed"] = {"count": 0, "rate": 0.0}
        rows = {r.label: r for r in cc.evaluate(summary, load_slo())}
        self.assertFalse(rows["mrp_completed count"].passed)
        # A failed run that returned quickly still passes the duration row; the count row catches it.
        self.assertTrue(rows["mrp_50k max (s)"].passed)

    def test_missing_counter_fails(self) -> None:
        summary = passing_summary()
        del summary["metrics"]["order_lines_accepted"]
        rows = {r.label: r for r in cc.evaluate(summary, load_slo())}
        self.assertIsNone(rows["order_throughput per hour"].value)
        self.assertFalse(rows["order_throughput per hour"].passed)

    def test_not_checked_rows_are_not_checks(self) -> None:
        labels = {c.label for c in cc.build_checks(load_slo())}
        for row in load_slo()["notChecked"]:
            self.assertFalse(any(label.startswith(row["id"]) for label in labels))


class TestMain(unittest.TestCase):
    def test_exit_codes(self) -> None:
        import tempfile

        with tempfile.TemporaryDirectory() as tmp:
            good = Path(tmp) / "good.json"
            good.write_text(json.dumps(passing_summary()), encoding="utf-8")
            self.assertEqual(cc.main([str(good)]), 0)

            bad_summary = copy.deepcopy(passing_summary())
            bad_summary["metrics"]["http_req_failed{op:journal_post}"] = {"rate": 0.5}
            bad = Path(tmp) / "bad.json"
            bad.write_text(json.dumps(bad_summary), encoding="utf-8")
            self.assertEqual(cc.main([str(bad)]), 1)

            # A smoke run at 10% load passes the throughput rows only with the matching --scale.
            smoke = copy.deepcopy(passing_summary())
            smoke["metrics"]["journal_lines_posted"]["rate"] = 100_000 * 1.05 / 3600
            smoke["metrics"]["order_lines_accepted"]["rate"] = 10_000 * 1.05 / 3600
            small = Path(tmp) / "smoke.json"
            small.write_text(json.dumps(smoke), encoding="utf-8")
            self.assertEqual(cc.main([str(small)]), 1)
            self.assertEqual(cc.main([str(small), "--scale", "0.1"]), 0)


if __name__ == "__main__":
    unittest.main()
