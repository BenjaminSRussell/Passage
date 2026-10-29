"""Tests for capacity_estimate.py. Run: python3 -m unittest discover -s docs/ops/tools"""

import unittest
from decimal import Decimal

import capacity_estimate as ce


class TestReferenceTenant(unittest.TestCase):
    """The 10k-user reference tenant from capacity-model.md and PRD 9.50.19."""

    def setUp(self):
        self.out = ce.estimate(ce.Inputs())

    def test_derived_load_matches_document(self):
        d = self.out["derived"]
        self.assertEqual(d["interactiveRequestsPerS"], 1000.0)
        self.assertEqual(d["scansPerS"], 400.0)
        self.assertEqual(d["journalLinesPerSSizing"], 5000)

    def test_pods_match_document(self):
        # capacity-model.md: API 13 -> 20 pods, ledger 9 -> 14 pods.
        pods = self.out["pods"]
        self.assertEqual(pods["api"], 20)
        self.assertEqual(pods["ledger"], 14)
        self.assertEqual(pods["order"], 3)
        self.assertEqual(pods["primary"], 37)
        self.assertEqual(pods["passive"], 19)

    def test_nodes_follow_pod_requests(self):
        # 37 pods x 1 vCPU / 6 = 7 nodes; 37 x 2 GiB / 24 = 4 nodes -> 7.
        self.assertEqual(self.out["nodes"]["primary"], 7)
        self.assertEqual(self.out["nodes"]["passive"], 4)

    def test_postgres_and_kafka_floors(self):
        self.assertEqual(self.out["postgres"]["storageGiB"], 4500)  # (1200+200+100) x 3 years
        self.assertEqual(self.out["postgres"]["instancesPerCluster"], 3)
        self.assertEqual(self.out["kafka"]["brokers"], 6)
        self.assertEqual(self.out["kafka"]["minInsyncReplicas"], 2)
        self.assertEqual(self.out["kafka"]["partitionsMinPerTopic"], 16)

    def test_no_cost_without_rate(self):
        self.assertNotIn("costUsdMonth", self.out)

    def test_cost_with_rate_rounds_once(self):
        out = ce.estimate(ce.Inputs(), Decimal("610.00"))
        # (7 + 4) nodes x 610.00 x 1.12 = 7515.20
        self.assertEqual(out["costUsdMonth"], "7515.20")
        self.assertEqual(out["costPriced"], ["nodes"])

    def test_postgres_and_kafka_priced_in_both_regions(self):
        out = ce.estimate(ce.Inputs(), Decimal("610.00"), Decimal("0.10"), Decimal("400.00"))
        # nodes 11 x 610 = 6710; postgres 4500 GiB x 3 instances x 2 regions x 0.10 = 2700;
        # kafka 6 brokers x 2 regions x 400 = 4800; (6710 + 2700 + 4800) x 1.12 = 15915.20
        self.assertEqual(out["costUsdMonth"], "15915.20")
        self.assertEqual(out["costPriced"], ["kafka", "nodes", "postgres"])


class TestScaling(unittest.TestCase):
    def test_pods_never_decrease_when_load_grows(self):
        last = 0
        for users in (1_000, 5_000, 10_000, 30_000):
            with self.subTest(users=users):
                pods = ce.estimate(ce.Inputs(users=users))["pods"]["api"]
                self.assertGreaterEqual(pods, last)
                last = pods

    def test_order_pods_have_availability_floor(self):
        out = ce.estimate(ce.Inputs(order_lines_per_hour=1))
        self.assertEqual(out["pods"]["order"], ce.MIN_ORDER_PODS)

    def test_kafka_brokers_scale_past_floor(self):
        out = ce.estimate(ce.Inputs(peak_kafka_msgs_per_s=200_000))
        self.assertEqual(out["kafka"]["brokers"], 20)

    def test_storage_floor_is_initial_volume(self):
        out = ce.estimate(ce.Inputs(ledger_gb_per_year=1, orders_gb_per_year=1, scans_gb_per_year=1, years=1))
        self.assertEqual(out["postgres"]["storageGiB"], ce.PG_INITIAL_GIB)

    def test_ceil_helpers(self):
        self.assertEqual(ce._ceil(12.5), 13)
        self.assertEqual(ce._ceil(13.0), 13)
        self.assertEqual(ce._pods(1000, 80), 20)


class TestValidation(unittest.TestCase):
    def test_non_positive_rejected(self):
        for field in ("users", "think_time_s", "journal_lines_per_hour", "scanners"):
            with self.subTest(field=field), self.assertRaises(ce.CapacityError):
                ce.estimate(ce.Inputs(**{field: 0}))

    def test_bool_rejected(self):
        with self.assertRaises(ce.CapacityError):
            ce.estimate(ce.Inputs(users=True))  # type: ignore[arg-type]

    def test_negative_node_rate_rejected(self):
        with self.assertRaises(ce.CapacityError):
            ce.estimate(ce.Inputs(), Decimal("-1"))

    def test_non_finite_or_float_rates_rejected(self):
        for bad in (Decimal("NaN"), Decimal("Infinity"), 1.5):
            with self.subTest(rate=bad), self.assertRaises(ce.CapacityError):
                ce.estimate(ce.Inputs(), None, bad)  # type: ignore[arg-type]


class TestCli(unittest.TestCase):
    def test_defaults_run(self):
        self.assertEqual(ce.main([]), 0)

    def test_bad_rate_exits_2(self):
        self.assertEqual(ce.main(["--node-usd-month", "abc"]), 2)
        self.assertEqual(ce.main(["--broker-usd-month", "NaN"]), 2)

    def test_storage_and_kafka_inputs_reach_the_model(self):
        import contextlib
        import io
        import json

        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            self.assertEqual(ce.main(["--ledger-gb-per-year", "3000", "--peak-kafka-msgs-per-s", "90000"]), 0)
        out = json.loads(buf.getvalue())
        self.assertEqual(out["postgres"]["storageGiB"], (3000 + 200 + 100) * 3)
        self.assertEqual(out["kafka"]["brokers"], 9)


if __name__ == "__main__":
    unittest.main()
