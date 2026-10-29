"""Tests for cost_meter.py. Run: python3 -m unittest discover -s docs/ops/tools"""

import unittest
from decimal import Decimal

import cost_meter as cm

RATES = {
    "cpu_core_hours": "0.04",
    "memory_gb_hours": "0.005",
    "storage_gb": "0.10",
    "kafka_gb": "0.02",
    "lakehouse_gb": "0.023",
    "ai_tokens": "0.000002",
}
USAGE = {
    "cpu_core_hours": "1000",
    "memory_gb_hours": "4000",
    "storage_gb": "500",
    "kafka_gb": "200",
    "lakehouse_gb": "300",
    "ai_tokens": "1000000",
}


class TestDecimalBoundary(unittest.TestCase):
    def test_float_rejected(self):
        with self.assertRaises(cm.CostError):
            cm.dec(1.5, "x")

    def test_bool_rejected(self):
        with self.assertRaises(cm.CostError):
            cm.dec(True, "x")

    def test_negative_and_nan_rejected(self):
        for bad in ("-1", "NaN", "Infinity", "abc"):
            with self.subTest(value=bad), self.assertRaises(cm.CostError):
                cm.dec(bad, "x")

    def test_missing_dimension_is_an_error_not_zero(self):
        raw = dict(USAGE)
        del raw["ai_tokens"]
        with self.assertRaises(cm.CostError):
            cm.Usage.from_dict(raw)

    def test_unknown_dimension_rejected(self):
        with self.assertRaises(cm.CostError):
            cm.Usage.from_dict({**USAGE, "seats": "3"})


class TestCompute(unittest.TestCase):
    def setUp(self):
        self.usage = cm.Usage.from_dict(USAGE)
        self.rates = cm.Rates.from_dict(RATES)

    def test_subtotal_and_overhead(self):
        # 40 + 20 + 50 + 4 + 6.9 + 2 = 122.9 before overhead; 12% overhead = 137.648 -> 137.65
        st = cm.compute("2026-10", self.usage, self.rates, "1.0850", "v1")
        self.assertEqual(st.subtotal_usd, Decimal("122.900000"))
        self.assertEqual(st.cost_usd, Decimal("137.65"))

    def test_eur_uses_raw_total_not_rounded_usd(self):
        st = cm.compute("2026-10", self.usage, self.rates, "1.0850", "v1")
        # 137.648 / 1.0850 = 126.8649... -> 126.86
        self.assertEqual(st.cost_eur, Decimal("126.86"))

    def test_output_is_cents(self):
        st = cm.compute("2026-10", self.usage, self.rates, "1.0850", "v1")
        for value in (st.cost_usd, st.cost_eur, *st.lines.values()):
            self.assertEqual(value, value.quantize(cm.CENT))

    def test_zero_usage_costs_zero(self):
        zero = cm.Usage.from_dict({d: "0" for d in cm.DIMENSIONS})
        st = cm.compute("2026-01", zero, self.rates, "1.1", "v1")
        self.assertEqual(st.cost_usd, Decimal("0.00"))
        self.assertEqual(st.cost_eur, Decimal("0.00"))

    def test_fx_zero_rejected(self):
        with self.assertRaises(cm.CostError):
            cm.compute("2026-10", self.usage, self.rates, "0", "v1")

    def test_bad_month_rejected(self):
        for bad in ("2026-13", "26-10", "2026-1", ""):
            with self.subTest(month=bad), self.assertRaises(cm.CostError):
                cm.compute(bad, self.usage, self.rates, "1.1", "v1")

    def test_overhead_out_of_range_rejected(self):
        with self.assertRaises(cm.CostError):
            cm.Rates.from_dict(RATES, Decimal("1.5"))

    def test_statement_serialises_strings_only(self):
        d = cm.compute("2026-10", self.usage, self.rates, "1.0850", "v1").to_dict()
        self.assertEqual(d["costUsd"], "137.65")
        self.assertEqual(d["fx"]["source"], "ECB_MONTH_AVG")
        self.assertEqual(d["modelVersion"], "v1")


class TestVariance(unittest.TestCase):
    def test_within_limit_ok(self):
        v = cm.variance("2026-10", Decimal("104.00"), "100.00")
        self.assertTrue(v.ok)
        self.assertEqual(v.pct, Decimal("4.00"))

    def test_above_limit_raises_cost_1901_with_catalog_text(self):
        v = cm.variance("2026-10", Decimal("106.00"), "100.00")
        self.assertEqual(v.code, "COST-1901")
        self.assertEqual(
            cm.variance_message(v), "Metered cost for 2026-10 differs from provider billing by 6.00%."
        )

    def test_under_billing_counts_as_variance_too(self):
        self.assertEqual(cm.variance("2026-10", Decimal("94.00"), "100.00").code, "COST-1901")

    def test_exactly_at_limit_is_ok(self):
        self.assertTrue(cm.variance("2026-10", Decimal("105.00"), "100.00").ok)

    def test_zero_billing_rejected(self):
        with self.assertRaises(cm.CostError):
            cm.variance("2026-10", Decimal("1"), "0")


class TestClusterReconciliation(unittest.TestCase):
    """cost-model.md: the sum of tenant costs is reconciled with the cluster invoice, not one tenant's cost."""

    def setUp(self):
        self.rates = cm.Rates.from_dict(RATES)

    def stmt(self, tenant, month="2026-10"):
        return cm.compute(month, cm.Usage.from_dict({**USAGE, "tenantId": tenant}), self.rates, "1.0850", "v1")

    def test_total_is_sum_of_rounded_tenant_costs(self):
        self.assertEqual(cm.cluster_total([self.stmt("t1"), self.stmt("t2")]), Decimal("275.30"))

    def test_duplicate_or_missing_tenant_rejected(self):
        with self.assertRaises(cm.CostError):
            cm.cluster_total([self.stmt("t1"), self.stmt("t1")])
        anonymous = cm.compute("2026-10", cm.Usage.from_dict(USAGE), self.rates, "1.0850", "v1")
        with self.assertRaises(cm.CostError):
            cm.cluster_total([self.stmt("t1"), anonymous])

    def test_mixed_months_rejected(self):
        with self.assertRaises(cm.CostError):
            cm.cluster_total([self.stmt("t1"), self.stmt("t2", month="2026-09")])

    def test_statement_carries_tenant_and_quantities_for_tenant_cost(self):
        d = self.stmt("t1").to_dict()
        self.assertEqual(d["tenantId"], "t1")
        self.assertEqual(d["quantities"]["ai_tokens"], "1000000")

    def test_blank_tenant_id_rejected(self):
        with self.assertRaises(cm.CostError):
            cm.Usage.from_dict({**USAGE, "tenantId": " "})


class TestCli(unittest.TestCase):
    def test_main_reports_variance_exit_code(self):
        import json
        import tempfile
        from pathlib import Path

        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            (base / "u.json").write_text(json.dumps(USAGE), encoding="utf-8")
            (base / "r.json").write_text(json.dumps(RATES), encoding="utf-8")
            args = ["--usage", str(base / "u.json"), "--rates", str(base / "r.json"),
                    "--usd-per-eur", "1.0850", "--month", "2026-10"]
            self.assertEqual(cm.main(args + ["--billed-usd", "137.00"]), 0)
            self.assertEqual(cm.main(args + ["--billed-usd", "100.00"]), 1)
            self.assertEqual(cm.main(args[:-2] + ["--month", "bad"]), 2)

    def test_main_reconciles_all_tenants_against_cluster_bill(self):
        import contextlib
        import io
        import json
        import tempfile
        from pathlib import Path

        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            for t in ("t1", "t2"):
                (base / f"{t}.json").write_text(json.dumps({**USAGE, "tenantId": t}), encoding="utf-8")
            (base / "r.json").write_text(json.dumps(RATES), encoding="utf-8")
            args = ["--usage", str(base / "t1.json"), "--usage", str(base / "t2.json"),
                    "--rates", str(base / "r.json"), "--usd-per-eur", "1.0850", "--month", "2026-10"]
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                # 275.30 metered vs 275.00 billed is within 5%; one tenant's 137.65 alone would not be.
                self.assertEqual(cm.main(args + ["--billed-usd", "275.00"]), 0)
            out = json.loads(buf.getvalue())
            self.assertEqual(out["clusterCostUsd"], "275.30")
            self.assertEqual(out["variance"]["meteredUsd"], "275.30")
            self.assertEqual([s["tenantId"] for s in out["statements"]], ["t1", "t2"])


if __name__ == "__main__":
    unittest.main()
