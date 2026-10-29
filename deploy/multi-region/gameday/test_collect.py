"""Tests for collect.py. Run: python3 -m unittest discover -s deploy/multi-region/gameday"""

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock
from typing import Any

import baseline
import collect

SPEC: dict[str, Any] = {
    "ledger": {"sql": "LEDGER", "checkpoint_sql": "CHECKPOINT :tenant :seq"},
    "rows": {"ledger.journal_line": "ROWS_JL", "inventory.txn": "ROWS_INV"},
    "sequences": {"fiscal-invoice-FR": "SEQ_FR"},
    "offsets_cluster": "passage-kafka-eu-central",
    "offsets_cmd": ["offsets"],
}

ANSWERS = {
    "LEDGER": json.dumps(
        [
            {"tenant": "t-1", "seq": 10, "head": "h10", "checkpoints": {"10": "h10"}},
            {"tenant": "t-2", "seq": 4, "head": "h4", "checkpoints": {"4": "h4"}},
        ]
    ),
    "ROWS_JL": "1200",
    "ROWS_INV": "300",
    "SEQ_FR": "[1,2,3,4]",
}


def fake_query(sql: str) -> str:
    return ANSWERS[sql]


# After failover: both chains moved on; t-1's hash at the baseline sequence 10 is still h10.
POST_ANSWERS = {
    **ANSWERS,
    "LEDGER": json.dumps([{"tenant": "t-1", "seq": 12, "head": "h12"}, {"tenant": "t-2", "seq": 6, "head": "h6"}]),
    "CHECKPOINT 't-1' 10": '"h10"',
    "CHECKPOINT 't-2' 4": '"h4"',
    "ROWS_JL": "1250",
    "SEQ_FR": "[1,2,3,4,5]",
}


def fake_cmd(argv: list[str]) -> str:
    assert argv == ["offsets"]
    return json.dumps({"passage.ledger.journal.v1-0": 55, "passage.ledger.journal.v1-1": 60})


class TestValidateSpec(unittest.TestCase):
    def test_accepts_reference_spec(self) -> None:
        collect.validate_spec(SPEC)

    def test_missing_section(self) -> None:
        for key in collect.REQUIRED:
            with self.subTest(key=key):
                bad = {k: v for k, v in SPEC.items() if k != key}
                with self.assertRaises(collect.SpecError):
                    collect.validate_spec(bad)

    def test_empty_rows_rejected(self) -> None:
        with self.assertRaises(collect.SpecError):
            collect.validate_spec({**SPEC, "rows": {}})

    def test_blank_sql_rejected(self) -> None:
        with self.assertRaises(collect.SpecError):
            collect.validate_spec({**SPEC, "sequences": {"s": "   "}})

    def test_offsets_cluster_must_be_named(self) -> None:
        for cluster in ("", "  ", None, 3):
            with self.subTest(cluster=cluster):
                with self.assertRaises(collect.SpecError):
                    collect.validate_spec({**SPEC, "offsets_cluster": cluster})

    def test_checkpoint_sql_needs_both_placeholders(self) -> None:
        for sql in ("select 1", "select :tenant", "select :seq", "select :tenant_id, :seq", 5):
            with self.subTest(sql=sql):
                with self.assertRaises(collect.SpecError):
                    collect.validate_spec({**SPEC, "ledger": {"sql": "LEDGER", "checkpoint_sql": sql}})

    def test_offsets_cmd_must_be_argv_list(self) -> None:
        for cmd in ("kafka-get-offsets", [], [1]):
            with self.subTest(cmd=cmd):
                with self.assertRaises(collect.SpecError):
                    collect.validate_spec({**SPEC, "offsets_cmd": cmd})


class TestCollect(unittest.TestCase):
    def test_builds_facts_the_baseline_tool_accepts(self) -> None:
        facts = collect.collect(SPEC, fake_query, fake_cmd)
        self.assertEqual(facts["ledger"]["t-1"], {"seq": 10, "head": "h10", "checkpoints": {"10": "h10"}})
        self.assertEqual(facts["ledger"]["t-2"], {"seq": 4, "head": "h4", "checkpoints": {"4": "h4"}})
        self.assertEqual(facts["rows"], {"ledger.journal_line": 1200, "inventory.txn": 300})
        self.assertEqual(facts["sequences"], {"fiscal-invoice-FR": [1, 2, 3, 4]})
        self.assertEqual(facts["offsets"]["passage.ledger.journal.v1-1"], 60)
        self.assertEqual(facts["offsetsCluster"], "passage-kafka-eu-central")
        manifest = baseline.capture(facts)
        self.assertEqual(manifest["ledger"]["t-1"], {"seq": 10, "head": "h10"})
        self.assertEqual(manifest["offsetsCluster"], "passage-kafka-eu-central")

    def test_post_failover_run_reads_the_checkpoint_at_the_baseline_sequence(self) -> None:
        manifest = baseline.capture(collect.collect(SPEC, fake_query, fake_cmd))
        post = collect.collect(SPEC, POST_ANSWERS.__getitem__, fake_cmd, manifest)
        self.assertEqual(post["ledger"]["t-1"], {"seq": 12, "head": "h12", "checkpoints": {"10": "h10"}})
        report = baseline.verify(manifest, post, manifest["digest"])
        self.assertTrue(report.passed, [c.detail for c in report.checks if not c.passed])

    def test_replaced_chain_fails_verification(self) -> None:
        manifest = baseline.capture(collect.collect(SPEC, fake_query, fake_cmd))
        forked = {**POST_ANSWERS, "CHECKPOINT 't-1' 10": '"other"'}
        post = collect.collect(SPEC, forked.__getitem__, fake_cmd, manifest)
        report = baseline.verify(manifest, post, manifest["digest"])
        self.assertFalse(report.passed)
        failed = [c.name for c in report.checks if not c.passed]
        self.assertEqual(failed, ["ledger t-1"])

    def test_post_failover_run_without_checkpoint_sql_is_rejected(self) -> None:
        manifest = baseline.capture(collect.collect(SPEC, fake_query, fake_cmd))
        spec = {**SPEC, "ledger": {"sql": "LEDGER"}}
        with self.assertRaises(collect.SpecError):
            collect.collect(spec, POST_ANSWERS.__getitem__, fake_cmd, manifest)

    def test_checkpoint_must_be_a_json_string(self) -> None:
        manifest = baseline.capture(collect.collect(SPEC, fake_query, fake_cmd))
        answers = {**POST_ANSWERS, "CHECKPOINT 't-1' 10": "null"}
        with self.assertRaises(collect.SpecError):
            collect.collect(SPEC, answers.__getitem__, fake_cmd, manifest)

    def test_checkpoint_query_quotes_only_plain_tenant_ids(self) -> None:
        self.assertEqual(
            collect.checkpoint_query("where t = :tenant and s = :seq", "t-1", 7), "where t = 't-1' and s = 7"
        )
        for tenant in ("x' or '1'='1", "a b", ""):
            with self.subTest(tenant=tenant):
                with self.assertRaises(collect.SpecError):
                    collect.checkpoint_query(":tenant :seq", tenant, 1)
        with self.assertRaises(collect.SpecError):
            collect.checkpoint_query(":tenant :seq", "t-1", -1)

    def test_round_trip_baseline_then_identical_observed_passes(self) -> None:
        facts = collect.collect(SPEC, fake_query, fake_cmd)
        manifest = baseline.capture(facts)
        report = baseline.verify(manifest, facts)
        self.assertTrue(report.passed, [c.detail for c in report.checks if not c.passed])

    def test_non_array_ledger_rejected(self) -> None:
        answers = {**ANSWERS, "LEDGER": "null"}
        with self.assertRaises(collect.SpecError):
            collect.collect(SPEC, answers.__getitem__, fake_cmd)

    def test_ledger_row_without_tenant_rejected(self) -> None:
        answers = {**ANSWERS, "LEDGER": json.dumps([{"seq": 1, "head": "h"}])}
        with self.assertRaises(collect.SpecError):
            collect.collect(SPEC, answers.__getitem__, fake_cmd)

    def test_non_json_output_rejected(self) -> None:
        answers = {**ANSWERS, "ROWS_JL": "not-a-number"}
        with self.assertRaises(collect.SpecError) as ctx:
            collect.collect(SPEC, answers.__getitem__, fake_cmd)
        self.assertIn("rows[ledger.journal_line]", str(ctx.exception))

    def test_invalid_facts_surface_as_spec_error(self) -> None:
        answers = {**ANSWERS, "ROWS_JL": "-5"}
        with self.assertRaises(collect.SpecError):
            collect.collect(SPEC, answers.__getitem__, fake_cmd)

    def test_empty_psql_output_is_null_and_rejected(self) -> None:
        answers = {**ANSWERS, "SEQ_FR": ""}
        with self.assertRaises(collect.SpecError):
            collect.collect(SPEC, answers.__getitem__, fake_cmd)


class TestMain(unittest.TestCase):
    def test_spec_error_is_exit_2_and_writes_nothing(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            spec = Path(tmp) / "spec.json"
            spec.write_text(json.dumps({k: v for k, v in SPEC.items() if k != "offsets_cluster"}), encoding="utf-8")
            out = Path(tmp) / "facts.json"
            self.assertEqual(collect.main(["--spec", str(spec), "--out", str(out)]), 2)
            self.assertFalse(out.exists())

    def test_edited_baseline_is_rejected(self) -> None:
        manifest = baseline.capture(collect.collect(SPEC, fake_query, fake_cmd))
        manifest["rows"]["ledger.journal_line"] = 1
        with tempfile.TemporaryDirectory() as tmp:
            spec = Path(tmp) / "spec.json"
            spec.write_text(json.dumps(SPEC), encoding="utf-8")
            base = Path(tmp) / "baseline.json"
            base.write_text(json.dumps(manifest), encoding="utf-8")
            out = Path(tmp) / "post.json"
            code = collect.main(["--spec", str(spec), "--baseline", str(base), "--out", str(out), "--psql", "false"])
            self.assertEqual(code, 2)
            self.assertFalse(out.exists())


class TestRun(unittest.TestCase):
    def test_queries_run_read_only_and_keep_existing_pgoptions(self) -> None:
        seen: dict[str, Any] = {}

        def fake_run(argv: list[str], what: str, env: dict[str, str] | None = None) -> str:
            seen.update(argv=argv, env=env)
            return "1\n"

        with mock.patch.dict(os.environ, {"PGOPTIONS": "-c statement_timeout=30s"}):
            query = collect.psql_query("psql")
        with mock.patch.object(collect, "_run", fake_run):
            self.assertEqual(query("select 1"), "1\n")
        self.assertEqual(seen["argv"][-2:], ["-c", "select 1"])
        self.assertIn("ON_ERROR_STOP=1", seen["argv"])
        self.assertEqual(seen["env"]["PGOPTIONS"], "-c statement_timeout=30s -c default_transaction_read_only=on")

    def test_missing_binary_is_a_runtime_error(self) -> None:
        with self.assertRaises(RuntimeError) as ctx:
            collect._run(["/no/such/psql-binary", "SECRET"], "query failed")
        self.assertNotIn("SECRET", str(ctx.exception))

    def test_run_returns_stdout(self) -> None:
        out = collect._run([sys.executable, "-c", "print('hello')"], "failed")
        self.assertEqual(out.strip(), "hello")

    def test_nonzero_exit_names_failure_without_arguments(self) -> None:
        with self.assertRaises(RuntimeError) as ctx:
            collect._run([sys.executable, "-c", "import sys; sys.exit(3)", "SECRET-ARG"], "query failed")
        self.assertIn("exit 3", str(ctx.exception))
        self.assertNotIn("SECRET-ARG", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()
