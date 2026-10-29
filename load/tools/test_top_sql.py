"""Tests for top_sql.py (PSG-50). Run: python3 -m unittest discover -s load/tools"""

import contextlib
import io
import tempfile
import unittest
from pathlib import Path

import top_sql as ts

CSV = (
    "query,calls,total_exec_time,mean_exec_time,max_exec_time,rows\n"
    '"select * from ledger.journal_line where journal_id = $1",100,5000.0,50.0,80.0,100\n'
    '"insert into ledger.journal_line (a,b)\n  values ($1, $2)",1000,300.0,0.3,4.0,1000\n'
    '"select pg_sleep($1)",2,4000.0,2000.0,2100.0,0\n'
    '"update inventory.onhand set qty = qty + $1",10,20.0,2.0,2.5,10\n'
)


class TestRead(unittest.TestCase):
    def test_multiline_quoted_query_is_one_row(self) -> None:
        stmts = ts.read_statements(CSV)
        self.assertEqual(len(stmts), 4)
        self.assertEqual(stmts[1].query, "insert into ledger.journal_line (a,b) values ($1, $2)")

    def test_numbers_parsed(self) -> None:
        s = ts.read_statements(CSV)[0]
        self.assertEqual((s.calls, s.total_ms, s.mean_ms, s.max_ms, s.rows), (100, 5000.0, 50.0, 80.0, 100))

    def test_pg12_column_aliases_accepted(self) -> None:
        text = "query,calls,total_time,mean_time,max_time,rows\nselect 1,1,2.0,2.0,2.0,1\n"
        self.assertEqual(ts.read_statements(text)[0].total_ms, 2.0)

    def test_null_cells_read_as_zero(self) -> None:
        text = "query,calls,total_exec_time,mean_exec_time,max_exec_time,rows\nselect 1,,NULL,,,\n"
        s = ts.read_statements(text)[0]
        self.assertEqual((s.calls, s.total_ms, s.mean_ms, s.rows), (0, 0.0, 0.0, 0))

    def test_missing_column_is_an_error(self) -> None:
        with self.assertRaises(ts.InputError):
            ts.read_statements("query,calls\nselect 1,1\n")

    def test_empty_input_is_an_error(self) -> None:
        with self.assertRaises(ts.InputError):
            ts.read_statements("")


class TestRanking(unittest.TestCase):
    def test_top_by_total_orders_descending(self) -> None:
        top = ts.top_by_total(ts.read_statements(CSV), 2)
        self.assertEqual([s.total_ms for s in top], [5000.0, 4000.0])

    def test_top_limit_larger_than_input(self) -> None:
        self.assertEqual(len(ts.top_by_total(ts.read_statements(CSV), 50)), 4)

    def test_over_limit_uses_mean_not_max(self) -> None:
        stmts = ts.read_statements(CSV)
        breaches = ts.over_limit(stmts, 1000.0)
        self.assertEqual([s.query for s in breaches], ["select pg_sleep($1)"])

    def test_max_only_outlier_is_not_a_breach(self) -> None:
        text = "query,calls,total_exec_time,mean_exec_time,max_exec_time,rows\nselect 1,10,10.0,1.0,1500.0,1\n"
        stmts = ts.read_statements(text)
        self.assertEqual(ts.over_limit(stmts, 1000.0), [])


class TestRender(unittest.TestCase):
    def test_table_marks_breaches_and_escapes_pipes(self) -> None:
        text = "query,calls,total_exec_time,mean_exec_time,max_exec_time,rows\nselect a|b,1,1.0,1500.0,1500.0,1\n"
        out = ts.render(ts.read_statements(text), 50, 1000.0)
        self.assertIn("1500.00 ⚠", out)
        self.assertIn("select a\\|b", out)

    def test_long_query_truncated(self) -> None:
        long_q = "select " + "x" * 300
        self.assertEqual(len(ts._preview(long_q)), ts.QUERY_PREVIEW)
        self.assertTrue(ts._preview(long_q).endswith("…"))

    def test_truncation_never_splits_a_pipe_escape(self) -> None:
        q = "select " + "x" * (ts.QUERY_PREVIEW - 9) + "|y"
        self.assertFalse(ts._preview(q).endswith("\\…"))

    def test_backtick_in_query_keeps_the_code_span_closed(self) -> None:
        text = 'query,calls,total_exec_time,mean_exec_time,max_exec_time,rows\n"select `a` from t",1,1.0,1.0,1.0,1\n'
        row = ts.render(ts.read_statements(text), 50, 1000.0).splitlines()[2]
        self.assertTrue(row.endswith("| ``select `a` from t`` |"), row)

    def test_share_is_of_all_statements_not_only_the_top(self) -> None:
        out = ts.render(ts.read_statements(CSV), 1, 1000.0).splitlines()
        self.assertEqual(len(out), 3)
        self.assertIn("| 53.6% |", out[2])


class TestMain(unittest.TestCase):
    def _run(self, text: str, *extra: str) -> int:
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / "stats.csv"
            p.write_text(text, encoding="utf-8")
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                return ts.main([str(p), *extra])

    def test_exit_zero_when_all_means_within_limit(self) -> None:
        text = "query,calls,total_exec_time,mean_exec_time,max_exec_time,rows\nselect 1,1,1.0,1.0,1.0,1\n"
        self.assertEqual(self._run(text), 0)

    def test_exit_one_on_mean_breach(self) -> None:
        self.assertEqual(self._run(CSV), 1)

    def test_exit_two_on_bad_input(self) -> None:
        self.assertEqual(self._run("nope\n"), 2)

    def test_exit_two_on_bad_top(self) -> None:
        self.assertEqual(self._run(CSV, "--top", "0"), 2)


if __name__ == "__main__":
    unittest.main()
