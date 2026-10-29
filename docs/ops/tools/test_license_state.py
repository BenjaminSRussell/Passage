"""Tests for license_state.py. Run: python3 -m unittest discover -s docs/ops/tools"""

import unittest
from datetime import date, timedelta

import license_state as ls

EXPIRES = date(2026, 12, 31)
LIC = ls.License(
    issued_at=date(2026, 1, 1),
    expires_at=EXPIRES,
    full_users=100,
    operational_users=50,
    selfservice_users=500,
)


class TestEvaluate(unittest.TestCase):
    def test_valid_well_before_expiry(self):
        st = ls.evaluate(LIC, ls.Usage(active_full=90), date(2026, 6, 1))
        self.assertEqual(st.state, ls.State.VALID)
        self.assertTrue(st.login_allowed)
        self.assertFalse(st.banner)
        self.assertEqual(st.codes, ())

    def test_expiry_day_itself_is_still_valid(self):
        st = ls.evaluate(LIC, ls.Usage(active_full=1), EXPIRES)
        self.assertEqual(st.state, ls.State.VALID)
        self.assertEqual(st.days_to_expiry, 0)
        self.assertEqual(st.codes, ("LIC-1801",))

    def test_grace_after_expiry_within_30_days(self):
        st = ls.evaluate(LIC, ls.Usage(active_full=1), EXPIRES + timedelta(days=30))
        self.assertEqual(st.state, ls.State.GRACE)
        self.assertTrue(st.login_allowed)
        self.assertTrue(st.banner)
        self.assertEqual(st.codes, ("LIC-1802",))

    def test_expired_31_days_ago_blocks_login_but_not_admin_or_export(self):
        # AC-50.8: expired 31 days ago, default grace 30 days.
        st = ls.evaluate(LIC, ls.Usage(active_full=1), EXPIRES + timedelta(days=31))
        self.assertEqual(st.state, ls.State.EXPIRED)
        self.assertFalse(st.login_allowed)
        self.assertTrue(st.admin_login_allowed)
        self.assertTrue(st.export_allowed)
        self.assertFalse(st.banner)
        self.assertEqual(st.codes, ("LIC-1803",))

    def test_over_use_is_grace_even_before_expiry(self):
        st = ls.evaluate(LIC, ls.Usage(active_full=101), date(2026, 6, 1))
        self.assertEqual(st.state, ls.State.GRACE)
        self.assertTrue(st.login_allowed)
        self.assertEqual(st.codes, ("LIC-1802",))

    def test_over_use_near_expiry_keeps_the_expiry_warning(self):
        st = ls.evaluate(LIC, ls.Usage(active_full=101), EXPIRES - timedelta(days=10))
        self.assertEqual(st.state, ls.State.GRACE)
        self.assertEqual(st.codes, ("LIC-1801", "LIC-1802"))

    def test_over_use_after_grace_end_is_still_expired(self):
        st = ls.evaluate(LIC, ls.Usage(active_full=500), EXPIRES + timedelta(days=31))
        self.assertEqual(st.state, ls.State.EXPIRED)
        self.assertEqual(st.codes, ("LIC-1803",))

    def test_custom_grace_days_moves_the_boundary(self):
        lic_day = EXPIRES + timedelta(days=45)
        self.assertEqual(ls.evaluate(LIC, ls.Usage(active_full=1), lic_day, grace_days=45).state, ls.State.GRACE)
        self.assertEqual(ls.evaluate(LIC, ls.Usage(active_full=1), lic_day, grace_days=44).state, ls.State.EXPIRED)

    def test_exactly_licensed_users_is_valid(self):
        st = ls.evaluate(LIC, ls.Usage(active_full=100), date(2026, 6, 1))
        self.assertEqual(st.state, ls.State.VALID)

    def test_operational_and_selfservice_do_not_move_state(self):
        st = ls.evaluate(LIC, ls.Usage(active_full=1, active_operational=10_000, active_selfservice=10_000),
                         date(2026, 6, 1))
        self.assertEqual(st.state, ls.State.VALID)

    def test_grace_days_zero_expires_the_day_after(self):
        st = ls.evaluate(LIC, ls.Usage(active_full=1), EXPIRES + timedelta(days=1), grace_days=0)
        self.assertEqual(st.state, ls.State.EXPIRED)

    def test_grace_days_out_of_range_rejected(self):
        for bad in (-1, 61):
            with self.subTest(grace=bad), self.assertRaises(ValueError):
                ls.evaluate(LIC, ls.Usage(active_full=1), date(2026, 6, 1), grace_days=bad)

    def test_to_dict_shape(self):
        d = ls.evaluate(LIC, ls.Usage(active_full=1), EXPIRES + timedelta(days=5)).to_dict()
        self.assertEqual(d["state"], "GRACE")
        self.assertEqual(d["codes"], ["LIC-1802"])
        self.assertEqual(d["daysToExpiry"], -5)


class TestLicenseValidation(unittest.TestCase):
    def test_expiry_before_issue_rejected(self):
        with self.assertRaises(ValueError):
            ls.License(date(2026, 2, 1), date(2026, 1, 1), 1, 0, 0)

    def test_negative_or_bool_counts_rejected(self):
        with self.assertRaises(ValueError):
            ls.License(date(2026, 1, 1), date(2026, 2, 1), -1, 0, 0)
        with self.assertRaises(ValueError):
            ls.License(date(2026, 1, 1), date(2026, 2, 1), True, 0, 0)


class TestUsageValidation(unittest.TestCase):
    def test_negative_or_bool_usage_rejected(self):
        for bad in (-1, True):
            with self.subTest(value=bad), self.assertRaises(ValueError):
                ls.Usage(active_full=bad)


class TestMessages(unittest.TestCase):
    def test_catalog_text_with_placeholders(self):
        usage = ls.Usage(active_full=101)
        st = ls.evaluate(LIC, usage, EXPIRES - timedelta(days=10))
        self.assertEqual(ls.message("LIC-1801", LIC, usage, st), "License expires in 10 days.")
        self.assertEqual(
            ls.message("LIC-1802", LIC, usage, st), "License in grace period: 101 full users active, 100 licensed."
        )

    def test_unknown_code_rejected(self):
        usage = ls.Usage(active_full=1)
        with self.assertRaises(ValueError):
            ls.message("LIC-9999", LIC, usage, ls.evaluate(LIC, usage, EXPIRES))


class TestWarningDue(unittest.TestCase):
    def test_thresholds(self):
        cases = {
            31: None,
            30: 30,
            8: 30,
            7: 7,
            2: 7,
            1: 1,
            0: 1,
        }
        for days_out, expected in cases.items():
            today = EXPIRES - timedelta(days=days_out)
            with self.subTest(days_out=days_out):
                self.assertEqual(ls.warning_due(LIC, today), expected)

    def test_no_warning_after_expiry(self):
        self.assertIsNone(ls.warning_due(LIC, EXPIRES + timedelta(days=1)))


if __name__ == "__main__":
    unittest.main()
