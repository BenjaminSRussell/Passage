"""Tests for gen_fixture.py. Run: python3 -m unittest discover -s load/tools"""

import csv
import hashlib
import io
import unittest
from decimal import Decimal

import gen_fixture as gf


def run(cfg: gf.Config) -> tuple[str, dict]:
    buf = io.StringIO()
    manifest = gf.generate(cfg, buf)
    return buf.getvalue(), manifest


def parse(text: str) -> list[dict[str, str]]:
    return list(csv.DictReader(io.StringIO(text)))


class TestConfig(unittest.TestCase):
    def test_rejects_odd_or_small_journal_size(self) -> None:
        for lines in (0, 1, 3, -2):
            with self.subTest(lines=lines):
                with self.assertRaises(ValueError):
                    gf.Config(100, 1, 1, lines, 1).validate()

    def test_rejects_empty_volume(self) -> None:
        with self.assertRaises(ValueError):
            gf.Config(0, 1, 1, 4, 1).validate()


class TestPeriods(unittest.TestCase):
    def test_period_labels_roll_over_year(self) -> None:
        cfg = gf.Config(1, 24, 1, 2, 1, first_period=(2024, 11))
        self.assertEqual(gf.period_label(cfg, 0), "2024-11")
        self.assertEqual(gf.period_label(cfg, 2), "2025-01")
        self.assertEqual(gf.period_label(cfg, 14), "2026-01")


class TestGenerate(unittest.TestCase):
    def setUp(self) -> None:
        self.cfg = gf.Config(total_lines=400, months=6, tenants=2, lines_per_journal=4, seed=7)
        self.text, self.manifest = run(self.cfg)
        self.rows = parse(self.text)

    def test_header_and_row_count(self) -> None:
        self.assertEqual(self.text.splitlines()[0], ",".join(gf.COLUMNS))
        self.assertEqual(len(self.rows), 400)
        self.assertEqual(self.manifest["lines"], 400)
        self.assertEqual(self.manifest["journals"], 100)

    def test_every_journal_balanced_per_currency(self) -> None:
        totals: dict[str, list[Decimal]] = {}
        for r in self.rows:
            sums = totals.setdefault(r["journal_id"], [Decimal(0), Decimal(0)])
            sums[0] += Decimal(r["debit"])
            sums[1] += Decimal(r["credit"])
        for jid, (dr, cr) in totals.items():
            with self.subTest(journal=jid):
                self.assertEqual(dr, cr)
                self.assertGreater(dr, 0)

    def test_each_line_is_debit_or_credit_not_both(self) -> None:
        for r in self.rows:
            debit_set, credit_set = Decimal(r["debit"]) != 0, Decimal(r["credit"]) != 0
            self.assertNotEqual(debit_set, credit_set)

    def test_amounts_are_two_place_decimal_strings(self) -> None:
        for r in self.rows:
            for field in ("debit", "credit"):
                self.assertRegex(r[field], r"^\d+\.\d{2}$")

    def test_journal_lines_numbered_and_sized(self) -> None:
        by_journal: dict[str, list[dict[str, str]]] = {}
        for r in self.rows:
            by_journal.setdefault(r["journal_id"], []).append(r)
        for jid, lines in by_journal.items():
            with self.subTest(journal=jid):
                self.assertEqual([int(x["line_no"]) for x in lines], [1, 2, 3, 4])
                self.assertEqual(len({x["currency"] for x in lines}), 1)
                self.assertEqual(len({x["period"] for x in lines}), 1)

    def test_tenants_and_months_spread(self) -> None:
        self.assertEqual(sum(self.manifest["linesPerTenant"]), 400)
        self.assertEqual(self.manifest["linesPerTenant"], [200, 200])
        self.assertEqual(sum(self.manifest["linesPerMonth"]), 400)
        self.assertTrue(all(v > 0 for v in self.manifest["linesPerMonth"]))

    def test_tenant_ids_are_uuid_shaped(self) -> None:
        import uuid

        for r in self.rows:
            uuid.UUID(r["tenant_id"])

    def test_deterministic_for_same_seed(self) -> None:
        text2, manifest2 = run(self.cfg)
        self.assertEqual(text2, self.text)
        self.assertEqual(manifest2, self.manifest)

    def test_different_seed_changes_bytes(self) -> None:
        other, _ = run(gf.Config(400, 6, 2, 4, 8))
        self.assertNotEqual(other, self.text)

    def test_manifest_hash_matches_content(self) -> None:
        digest = "sha256:" + hashlib.sha256(self.text.encode("utf-8")).hexdigest()
        self.assertEqual(self.manifest["csvSha256"], digest)
        self.assertEqual(self.manifest["csvBytes"], len(self.text.encode("utf-8")))

    def test_manifest_marks_synthetic(self) -> None:
        self.assertTrue(self.manifest["synthetic"])
        self.assertEqual(self.manifest["schema"], gf.SCHEMA)

    def test_partial_last_journal_rounds_up(self) -> None:
        text, manifest = run(gf.Config(total_lines=10, months=1, tenants=1, lines_per_journal=4, seed=1))
        self.assertEqual(manifest["journals"], 3)
        self.assertEqual(manifest["lines"], 12)
        self.assertEqual(len(parse(text)), 12)


if __name__ == "__main__":
    unittest.main()
