"""Tests for baseline.py (game-day fx-gameday). Run: python3 -m unittest discover -s deploy/multi-region/gameday"""

import copy
import json
import tempfile
import unittest
from pathlib import Path

import baseline as bl


STANDBY = "passage-kafka-eu-central"


def facts(**overrides):
    base = {
        "offsetsCluster": STANDBY,
        "ledger": {
            "tenant-a": {"seq": 120, "head": "h120", "checkpoints": {"100": "h100", "120": "h120"}},
        },
        "rows": {"journal_lines": 1000, "invoices": 50},
        "offsets": {"passage.ledger.journal-0": 500, "passage.ledger.journal-1": 480},
        "sequences": {"tenant-a/INV/2026": [1, 2, 3, 4, 5]},
    }
    base.update(overrides)
    return base


def pre_facts():
    return {
        "offsetsCluster": STANDBY,
        "ledger": {"tenant-a": {"seq": 120, "head": "h120"}},
        "rows": {"journal_lines": 1000, "invoices": 50},
        "offsets": {"passage.ledger.journal-0": 500, "passage.ledger.journal-1": 480},
        "sequences": {"tenant-a/INV/2026": [1, 2, 3, 4, 5]},
    }


def baseline_manifest():
    return bl.capture(pre_facts())


class TestCapture(unittest.TestCase):
    def test_capture_sets_digest_and_sorted_body(self) -> None:
        m = baseline_manifest()
        self.assertEqual(m["schema"], bl.SCHEMA)
        self.assertTrue(m["digest"].startswith("sha256:"))
        bl.check_manifest(m)

    def test_capture_is_deterministic(self) -> None:
        self.assertEqual(baseline_manifest(), baseline_manifest())

    def test_capture_summarises_sequences_to_first_and_last(self) -> None:
        m = baseline_manifest()
        self.assertEqual(m["sequences"]["tenant-a/INV/2026"], {"first": 1, "last": 5})

    def test_missing_ledger_is_facts_error(self) -> None:
        with self.assertRaises(bl.FactsError):
            bl.capture({"rows": {}, "offsets": {}, "sequences": {}})

    def test_negative_row_count_rejected(self) -> None:
        f = facts(rows={"journal_lines": -1})
        with self.assertRaises(bl.FactsError):
            bl.capture(f)

    def test_empty_sequence_rejected(self) -> None:
        with self.assertRaises(bl.FactsError):
            bl.capture(facts(sequences={"s": []}))

    def test_offsets_cluster_required(self) -> None:
        f = facts()
        del f["offsetsCluster"]
        with self.assertRaisesRegex(bl.FactsError, "offsetsCluster"):
            bl.capture(f)
        with self.assertRaisesRegex(bl.FactsError, "offsetsCluster"):
            bl.capture(facts(offsetsCluster=" "))

    def test_malformed_checkpoints_rejected(self) -> None:
        f = facts()
        f["ledger"]["tenant-a"]["checkpoints"] = ["h100"]
        with self.assertRaisesRegex(bl.FactsError, "checkpoints"):
            bl.verify(baseline_manifest(), f)


class TestManifestIntegrity(unittest.TestCase):
    def test_edited_manifest_is_rejected(self) -> None:
        m = baseline_manifest()
        m["rows"]["journal_lines"] = 1
        with self.assertRaisesRegex(bl.FactsError, "edited"):
            bl.check_manifest(m)

    def test_replaced_manifest_rejected_by_recorded_digest(self) -> None:
        # A re-captured manifest has a valid digest of its own; only the recorded one exposes it.
        forged = pre_facts()
        forged["rows"]["invoices"] = 0
        replaced = bl.capture(forged)
        bl.check_manifest(replaced)
        recorded = baseline_manifest()["digest"]
        with self.assertRaisesRegex(bl.FactsError, "recorded"):
            bl.verify(replaced, facts(), expect_digest=recorded)
        self.assertTrue(bl.verify(baseline_manifest(), facts(), expect_digest=recorded).passed)

    def test_unknown_schema_rejected(self) -> None:
        m = baseline_manifest()
        m["schema"] = "other"
        with self.assertRaises(bl.FactsError):
            bl.check_manifest(m)


class TestVerify(unittest.TestCase):
    def test_extended_chain_and_growth_passes(self) -> None:
        report = bl.verify(baseline_manifest(), facts())
        self.assertTrue(report.passed, [c for c in report.checks if not c.passed])

    def test_replaced_chain_fails(self) -> None:
        f = facts()
        f["ledger"]["tenant-a"]["checkpoints"]["120"] = "forged"
        report = bl.verify(baseline_manifest(), f)
        self.assertFalse(report.passed)
        self.assertTrue(any("does not match baseline head" in c.detail for c in report.checks))

    def test_shortened_chain_fails(self) -> None:
        f = facts()
        f["ledger"]["tenant-a"]["seq"] = 110
        self.assertFalse(bl.verify(baseline_manifest(), f).passed)

    def test_missing_tenant_fails(self) -> None:
        f = facts(ledger={})
        self.assertFalse(bl.verify(baseline_manifest(), f).passed)

    def test_lost_rows_fail(self) -> None:
        f = facts(rows={"journal_lines": 999, "invoices": 50})
        report = bl.verify(baseline_manifest(), f)
        self.assertFalse(report.passed)
        self.assertTrue(any(c.name == "rows journal_lines" and not c.passed for c in report.checks))

    def test_missing_partition_fails(self) -> None:
        f = facts(offsets={"passage.ledger.journal-0": 500})
        self.assertFalse(bl.verify(baseline_manifest(), f).passed)

    def test_sequence_gap_fails(self) -> None:
        f = facts(sequences={"tenant-a/INV/2026": [1, 2, 4, 5]})
        report = bl.verify(baseline_manifest(), f)
        self.assertFalse(report.passed)
        self.assertTrue(any("gap or duplicate" in c.detail for c in report.checks))

    def test_sequence_not_covering_baseline_last_fails(self) -> None:
        f = facts(sequences={"tenant-a/INV/2026": [1, 2, 3]})
        self.assertFalse(bl.verify(baseline_manifest(), f).passed)

    def test_sequence_losing_early_numbers_fails(self) -> None:
        # 1 and 2 were issued before the failure; a contiguous 3..7 after it means they were lost.
        f = facts(sequences={"tenant-a/INV/2026": [3, 4, 5, 6, 7]})
        report = bl.verify(baseline_manifest(), f)
        self.assertFalse(report.passed)
        self.assertTrue(any("does not cover baseline 1..5" in c.detail for c in report.checks))

    def test_sequence_extended_after_failover_passes(self) -> None:
        f = facts(sequences={"tenant-a/INV/2026": [1, 2, 3, 4, 5, 6, 7]})
        self.assertTrue(bl.verify(baseline_manifest(), f).passed)

    def test_duplicate_number_fails(self) -> None:
        f = facts(sequences={"tenant-a/INV/2026": [1, 2, 3, 4, 5, 5, 6]})
        self.assertFalse(bl.verify(baseline_manifest(), f).passed)

    def test_missing_series_fails(self) -> None:
        report = bl.verify(baseline_manifest(), facts(sequences={}))
        self.assertTrue(any(c.name == "sequence tenant-a/INV/2026" and not c.passed for c in report.checks))

    def test_offsets_from_another_cluster_fail(self) -> None:
        # Source offsets are not comparable with the mirror's, even with equal topic names.
        report = bl.verify(baseline_manifest(), facts(offsetsCluster="passage-kafka-eu-west"))
        self.assertFalse(report.passed)
        self.assertTrue(any(c.name == "offsets cluster" and not c.passed for c in report.checks))

    def test_empty_report_is_not_a_pass(self) -> None:
        report = bl.Report()
        self.assertFalse(report.passed)


class TestCli(unittest.TestCase):
    def test_capture_then_verify_round_trip(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "facts.json").write_text(json.dumps(facts()), encoding="utf-8")
            base_in = root / "pre.json"
            base_in.write_text(json.dumps(pre_facts()), encoding="utf-8")
            out = root / "baseline.json"
            self.assertEqual(bl.main(["capture", "--input", str(base_in), "--out", str(out)]), 0)
            digest = json.loads(out.read_text(encoding="utf-8"))["digest"]

            def verify(observed: str, expect: str = digest) -> int:
                return bl.main(
                    ["verify", "--baseline", str(out), "--observed", str(root / observed), "--expect-digest", expect]
                )

            report = root / "report.json"
            self.assertEqual(
                bl.main(
                    ["verify", "--baseline", str(out), "--observed", str(root / "facts.json"),
                     "--expect-digest", digest, "--report", str(report)]
                ),
                0,
            )
            self.assertTrue(json.loads(report.read_text(encoding="utf-8"))["passed"])
            bad = copy.deepcopy(facts())
            bad["rows"]["invoices"] = 0
            (root / "bad.json").write_text(json.dumps(bad), encoding="utf-8")
            self.assertEqual(verify("bad.json"), 1)
            self.assertEqual(verify("facts.json", "sha256:" + "0" * 64), 2)

    def test_malformed_input_returns_2(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / "x.json"
            p.write_text("[]", encoding="utf-8")
            self.assertEqual(bl.main(["capture", "--input", str(p), "--out", str(Path(tmp) / "o.json")]), 2)


if __name__ == "__main__":
    unittest.main()
