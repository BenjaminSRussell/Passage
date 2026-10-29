"""Tests for check_soak.py. Run: python3 -m unittest discover -s load/tools"""

import tempfile
import unittest
from pathlib import Path

import check_soak as cs

DAY = 24 * 3600


def series(values: list[float], span_s: float = DAY) -> list[tuple[float, float]]:
    """Evenly spaced samples over span_s seconds."""
    step = span_s / max(1, len(values) - 1)
    return [(i * step, v) for i, v in enumerate(values)]


def flat(n: int, value: float = 1000.0) -> list[float]:
    return [value] * n


def judge(values: list[float], span_s: float = DAY, max_growth: float = 0.10, min_span_h: float = 24):
    return cs.judge("svc", series(values, span_s), max_growth, min_span_h)


class TestJudge(unittest.TestCase):
    def test_flat_series_passes(self) -> None:
        v = judge(flat(100))
        self.assertTrue(v.passed)
        self.assertEqual(v.growth, 0.0)
        self.assertAlmostEqual(v.span_hours, 24.0)

    def test_growth_exactly_at_budget_passes(self) -> None:
        v = judge(flat(50, 1000.0) + flat(50, 1100.0))
        self.assertTrue(v.passed)
        self.assertAlmostEqual(v.growth, 0.10)

    def test_growth_over_budget_fails(self) -> None:
        v = judge(flat(50, 1000.0) + flat(50, 1101.0))
        self.assertFalse(v.passed)
        self.assertIn("exceeds", v.reason)

    def test_shrinking_memory_passes(self) -> None:
        self.assertTrue(judge(flat(50, 1000.0) + flat(50, 800.0)).passed)

    def test_single_end_spike_does_not_decide(self) -> None:
        # One GC spike at the end is absorbed by the median of the last window.
        self.assertTrue(judge(flat(99, 1000.0) + [5000.0]).passed)

    def test_order_is_by_timestamp_not_input(self) -> None:
        # The export lists the leaking end first; sorting by time still finds the growth.
        samples = series(flat(50, 1000.0) + flat(50, 1500.0))
        v = cs.judge("svc", list(reversed(samples)), 0.10, 24)
        self.assertFalse(v.passed)
        self.assertAlmostEqual(v.growth, 0.5)

    def test_too_few_samples_fails(self) -> None:
        v = judge(flat(9))
        self.assertFalse(v.passed)
        self.assertIsNone(v.growth)

    def test_short_run_cannot_pass_as_a_soak(self) -> None:
        v = judge(flat(100), span_s=2 * 3600)
        self.assertFalse(v.passed)
        self.assertIn("spans 2.0 h, need 24 h", v.reason)
        # A smoke soak opts out explicitly.
        self.assertTrue(judge(flat(100), span_s=2 * 3600, min_span_h=0).passed)

    def test_zero_baseline_fails(self) -> None:
        v = judge(flat(20, 0.0))
        self.assertFalse(v.passed)
        self.assertEqual(v.reason, "baseline is zero")


class TestReadSeries(unittest.TestCase):
    def _write(self, tmp: str, text: str) -> Path:
        path = Path(tmp) / "svc.csv"
        path.write_text(text, encoding="utf-8")
        return path

    def test_reads_and_sorts_by_timestamp(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = self._write(tmp, "timestamp,bytes\n2,200\n1,100\n")
            self.assertEqual(cs.read_series(path), [(1.0, 100.0), (2.0, 200.0)])

    def test_iso_timestamps(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = self._write(tmp, "timestamp,bytes\n2026-10-01T00:00:00Z,1\n2026-10-02T00:00:00+00:00,2\n")
            (t0, _), (t1, _) = cs.read_series(path)
            self.assertEqual(t1 - t0, DAY)

    def test_missing_columns_raise(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaisesRegex(ValueError, "'bytes'"):
                cs.read_series(self._write(tmp, "timestamp,rss\n1,100\n"))
            with self.assertRaisesRegex(ValueError, "'timestamp'"):
                cs.read_series(self._write(tmp, "time,bytes\n1,100\n"))

    def test_bad_values_raise_with_line(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaisesRegex(ValueError, ":2: bytes"):
                cs.read_series(self._write(tmp, "timestamp,bytes\n1,abc\n"))
            with self.assertRaisesRegex(ValueError, ":3: timestamp"):
                cs.read_series(self._write(tmp, "timestamp,bytes\n1,1\nyesterday,1\n"))
            with self.assertRaisesRegex(ValueError, "negative"):
                cs.read_series(self._write(tmp, "timestamp,bytes\n1,-1\n"))


class TestMain(unittest.TestCase):
    def _csv(self, tmp: str, name: str, values: list[float], span_s: float = DAY) -> Path:
        path = Path(tmp) / f"{name}.csv"
        rows = "".join(f"{t:.0f},{v}\n" for t, v in series(values, span_s))
        path.write_text("timestamp,bytes\n" + rows, encoding="utf-8")
        return path

    def test_exit_codes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            good = self._csv(tmp, "good", flat(40))
            leak = self._csv(tmp, "leak", [1000 + 20 * i for i in range(40)])
            self.assertEqual(cs.main([str(good)]), 0)
            self.assertEqual(cs.main([str(leak)]), 1)
            self.assertEqual(cs.main([str(good), str(leak)]), 1)
            self.assertEqual(cs.main([str(leak), "--max-growth", "1.0"]), 0)

    def test_defaults_come_from_the_soak_profile(self) -> None:
        self.assertEqual(cs.soak_defaults(cs.DEFAULT_SLO), (0.10, 24.0))
        with tempfile.TemporaryDirectory() as tmp:
            short = self._csv(tmp, "short", flat(40), span_s=3600)
            self.assertEqual(cs.main([str(short)]), 1)
            self.assertEqual(cs.main([str(short), "--min-span-hours", "0"]), 0)

    def test_bad_file_returns_2(self) -> None:
        self.assertEqual(cs.main(["/nonexistent/path.csv"]), 2)


if __name__ == "__main__":
    unittest.main()
