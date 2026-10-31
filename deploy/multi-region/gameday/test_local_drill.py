"""Tests for local_drill.py.

The chain literals were computed with ``python3 -I`` (hashlib) and cross-checked with Postgres
``encode(sha256(convert_to('genesis:1', 'UTF8')), 'hex')``. The drill runs need initdb, pg_ctl,
pg_basebackup and psql on PATH (about 3 s each) and are skipped without them.
"""

import shutil
import tempfile
import unittest
from pathlib import Path

import local_drill

H1 = "02b398315758a3ddaa25f28222db771678e854a900aa18eeead94ec28c748ea8"  # sha256("genesis:1")
H2 = "53584744086bf41da4115b407e3f26405b8e6cc9937c5c7f5ac50a0c625a6324"  # sha256(H1 + ":2")
HAVE_PG = all(shutil.which(b) for b in local_drill.BINARIES)


class TestRecomputeChains(unittest.TestCase):
    def test_valid_chain(self):
        self.assertEqual(local_drill.recompute_chains([("t", 2, H2), ("t", 1, H1)]), [])

    def test_replaced_entry_is_found(self):
        problems = local_drill.recompute_chains([("t", 1, H1), ("t", 2, H1)])
        self.assertEqual(problems, ["t: hash at seq 2 does not follow from seq 1"])

    def test_missing_entry_is_found(self):
        self.assertEqual(local_drill.recompute_chains([("t", 2, H2)]), ["t: seq 2 where 1 was expected"])


@unittest.skipUnless(HAVE_PG, "PostgreSQL server binaries not on PATH")
class TestDrill(unittest.TestCase):
    def run_drill(self, mode, cut):
        out = Path(tempfile.mkdtemp(prefix="drill-test-"))
        self.addCleanup(shutil.rmtree, out, True)
        bins = local_drill.find_binaries(None)
        return local_drill.drill(mode, out, bins, after_baseline_s=1.0, cut_replication=cut)

    def assert_consistent(self, s):
        # Every entry the old primary holds is on the new primary, lost after an ack, or never acknowledged.
        self.assertEqual(s["oldPrimaryEntries"], s["newPrimaryEntries"] + s["lostAcknowledged"] + s["unacknowledgedLocalOnly"])
        self.assertEqual(s["chainProblems"], [])
        self.assertTrue(s["verifyPassed"], "baseline rows were replicated before the fence")
        self.assertLess(s["rtoSeconds"], 3600)

    def test_sync_failover_loses_nothing(self):
        s = self.run_drill("sync", cut=False)
        self.assert_consistent(s)
        self.assertGreater(s["acknowledged"], 0)
        self.assertEqual(s["lostAcknowledged"], 0)
        self.assertEqual(s["rpoSeconds"], 0)
        self.assertTrue(s["scorePassed"])
        self.assertTrue(s["passed"])

    def test_sync_with_cut_link_never_acknowledges_what_it_loses(self):
        # The first commit after the cut is written locally and then waits for the standby forever:
        # exactly one entry is on the old primary only, and no client was told it was committed.
        s = self.run_drill("sync", cut=True)
        self.assert_consistent(s)
        self.assertEqual(s["lostAcknowledged"], 0)
        self.assertEqual(s["unacknowledgedLocalOnly"], 1)
        self.assertEqual(s["rpoSeconds"], 0)
        self.assertTrue(s["passed"])

    def test_async_with_cut_link_loses_acknowledged_commits(self):
        # One second of writes after the cut are acknowledged by the old primary and never reach the
        # standby. The RPO is about that second, inside the 300 s target, and the baseline verify passes
        # because the baseline predates the cut: the scorecard alone does not see this loss. The drill does.
        s = self.run_drill("async", cut=True)
        self.assert_consistent(s)
        self.assertGreater(s["lostAcknowledged"], 0)
        self.assertGreater(s["rpoSeconds"], 0.5)
        self.assertLess(s["rpoSeconds"], 30)
        self.assertTrue(s["scorePassed"])
        self.assertFalse(s["passed"])


if __name__ == "__main__":
    unittest.main()
