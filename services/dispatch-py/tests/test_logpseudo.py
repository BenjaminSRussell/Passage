"""Log pseudonymization: no raw email or name reaches the exported line, pseudonyms are tenant-keyed, unrelated fields pass."""

from __future__ import annotations

import base64
import io
import json
import logging
import unittest

from passage_dispatch import logpseudo as lp
from passage_dispatch.__main__ import JsonFormatter

TENANT_A = "11111111-1111-4111-8111-111111111111"
TENANT_B = "22222222-2222-4222-8222-222222222222"
# Master key bytes 0..31, the key platform/go/logpseudo's TestKnownAnswers uses.
KEY_B64 = base64.b64encode(bytes(range(32))).decode()
ENV_KEYS = {"FIELDCRYPT_KEYS": f"k1={KEY_B64}", "FIELDCRYPT_CURRENT_KEY_ID": "k1"}
KEYS = lp.MacKeys.from_env(ENV_KEYS)


def logger_with(filter_: logging.Filter | None) -> tuple[logging.Logger, io.StringIO]:
    """A logger that writes the service's JSON lines, the shape Loki receives."""
    buf = io.StringIO()
    handler = logging.StreamHandler(buf)
    handler.setFormatter(JsonFormatter())
    if filter_ is not None:
        handler.addFilter(filter_)
    log = logging.getLogger(f"test.logpseudo.{id(buf)}")
    log.handlers, log.propagate = [handler], False
    log.setLevel(logging.INFO)
    return log, buf


class TestPseudonyms(unittest.TestCase):
    def test_match_the_go_implementation_byte_for_byte(self):
        self.assertEqual(lp.pseudonym(KEYS, TENANT_A, "Jane.Doe@Example.com"), "pn_81c1af9dc7db4ac7")
        self.assertEqual(lp.pseudonym(KEYS, TENANT_B, "Jane.Doe@Example.com"), "pn_cc6b2b4cc9040118")

    def test_stable_per_tenant_and_different_across_tenants(self):
        self.assertEqual(lp.pseudonym(KEYS, TENANT_A, " JANE@example.com"), lp.pseudonym(KEYS, TENANT_A, "jane@example.com"))
        self.assertNotEqual(lp.pseudonym(KEYS, TENANT_A, "jane@example.com"), lp.pseudonym(KEYS, TENANT_B, "jane@example.com"))

    def test_none_without_a_key_or_tenant(self):
        self.assertIsNone(lp.pseudonym(None, TENANT_A, "x"))
        self.assertIsNone(lp.pseudonym(KEYS, "", "x"))


class TestRecords(unittest.TestCase):
    def test_no_raw_email_or_name_survives(self):
        out = json.dumps(
            lp.pseudonymize_record(
                {
                    "message": "sign-in of jane.doe@example.com failed", "tenant_id": TENANT_A, "user": "Jane Doe",
                    "email": "jane.doe@example.com", "display_name": "Jane Doe", "remoteIp": "203.0.113.9",
                    "detail": "contact jane.doe@example.com or JANE.DOE@EXAMPLE.COM", "person": {"first": "Jane", "last": "Doe"},
                    "req": {"subject": "auth0|jane", "path": "/users/jane.doe@example.com", "tags": ["jane.doe@example.com"]},
                    "customer_name": "Acme Jane",
                },
                KEYS,
            )
        )
        for raw in ("jane.doe", "Jane", "Doe", "JANE", "example.com", "203.0.113.9", "auth0", "Acme"):
            self.assertNotIn(raw, out)
        self.assertIn(lp.PREFIX, out)

    def test_same_pseudonym_per_tenant_different_across_tenants(self):
        a1 = lp.pseudonymize_record({"tenant_id": TENANT_A, "email": "jane@example.com"}, KEYS)
        a2 = lp.pseudonymize_record({"tenantId": TENANT_A, "user": "Jane@Example.com"}, KEYS)
        b = lp.pseudonymize_record({"tenant_id": TENANT_B, "email": "jane@example.com"}, KEYS)
        self.assertEqual(a1["email"], a2["user"])
        self.assertNotEqual(a1["email"], b["email"])
        self.assertEqual(a1["tenant_id"], TENANT_A)

    def test_unrelated_fields_pass_through(self):
        rec = {"level": "INFO", "message": "solve done", "service": "dispatch", "tenant_id": TENANT_A, "run_id": "r-1",
               "elapsed_ms": 12, "ok": True, "hostname": "pod-1", "nothing": None}
        self.assertEqual(lp.pseudonymize_record(rec, KEYS), rec)

    def test_no_key_or_no_tenant_gives_markers_never_the_value(self):
        no_key = json.dumps(lp.pseudonymize_record({"tenant_id": TENANT_A, "email": "a@example.com", "user": "Jane", "note": "cc b@example.com"}, None))
        no_tenant = json.dumps(lp.pseudonymize_record({"email": "a@example.com", "name": "Jane", "note": "cc b@example.com"}, KEYS))
        for out in (no_key, no_tenant):
            self.assertNotIn("example.com", out)
            self.assertNotIn("Jane", out)
            self.assertIn(lp.REDACTED, out)
            self.assertIn(lp.EMAIL_REDACTED, out)
            self.assertNotIn(lp.PREFIX, out)

    def test_which_keys_name_a_person(self):
        for k in ("user", "userId", "user_id", "user-name", "email", "contactEmail", "name", "displayName", "customer_name", "remoteIp", "client.ip", "subject", "actor"):
            self.assertTrue(lp.is_personal_key(k), k)
        for k in ("method", "path", "status", "request_id", "realm_name", "hostname", "service", "tenant_id", "run_id", "description", "ipsum"):
            self.assertFalse(lp.is_personal_key(k), k)


class TestFilter(unittest.TestCase):
    def test_what_the_handler_writes_has_no_raw_email_or_name(self):
        log, buf = logger_with(lp.PseudonymizingFilter(KEYS))
        log.info("sign-in of %s failed", "jane.doe@example.com", extra={"tenant_id": TENANT_A, "user": "Jane Doe", "email": "jane.doe@example.com", "run_id": "r-1"})
        try:
            raise RuntimeError("no mailbox jane.doe@example.com")
        except RuntimeError:
            log.exception("solver crashed", extra={"tenant_id": TENANT_A})
        out = buf.getvalue()
        for raw in ("jane.doe", "Jane", "example.com"):
            self.assertNotIn(raw, out)
        first, second = (json.loads(line) for line in out.splitlines())
        self.assertEqual(first["email"], lp.pseudonym(KEYS, TENANT_A, "jane.doe@example.com"))
        self.assertEqual((first["run_id"], first["tenant_id"]), ("r-1", TENANT_A))
        self.assertIn("RuntimeError", second["exception"])

    def test_tenant_from_the_context_variable(self):
        log, buf = logger_with(lp.PseudonymizingFilter(KEYS))
        token = lp.tenant.set(TENANT_B)
        try:
            log.info("x", extra={"email": "jane@example.com"})
        finally:
            lp.tenant.reset(token)
        self.assertEqual(json.loads(buf.getvalue())["email"], lp.pseudonym(KEYS, TENANT_B, "jane@example.com"))

    def test_without_the_filter_the_line_is_raw(self):
        log, buf = logger_with(None)
        log.info("x", extra={"email": "jane@example.com"})
        self.assertIn("jane@example.com", buf.getvalue())


class TestInstall(unittest.TestCase):
    def test_on_under_the_eu_overlay_and_with_the_flag_only(self):
        for env, want in (
            ({}, False), ({"LOG_PSEUDONYMIZE": "false"}, False), ({"LOG_PSEUDONYMIZE": "true"}, True),
            ({"PASSAGE_CLOUD_PROFILE": "gcc"}, False), ({"PASSAGE_CLOUD_PROFILE": "commercial+eu-data-boundary"}, True),
            ({"PASSAGE_CLOUD_PROFILE": "gcc+hipaa"}, False),
            ({"PASSAGE_CLOUD_PROFILE": "commercial+eu-data-boundary", "LOG_PSEUDONYMIZE": "false"}, True),
        ):
            handler = logging.StreamHandler(io.StringIO())
            self.assertEqual(lp.install(handler, env), want, env)
            self.assertEqual(len(handler.filters), int(want))

    def test_with_the_flag_on_and_no_key_values_are_redacted_and_a_bad_key_raises(self):
        handler = logging.StreamHandler(io.StringIO())
        self.assertTrue(lp.install(handler, {"LOG_PSEUDONYMIZE": "true"}))
        (flt,) = handler.filters
        self.assertIsNone(flt.keys)
        with self.assertRaises(ValueError):
            lp.install(logging.StreamHandler(io.StringIO()), {"LOG_PSEUDONYMIZE": "true", "FIELDCRYPT_KEYS": "k1=bad", "FIELDCRYPT_CURRENT_KEY_ID": "k1"})


if __name__ == "__main__":
    unittest.main()
