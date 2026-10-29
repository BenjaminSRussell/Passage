"""Tests for check_release_notes.py. Run: python3 -m unittest discover -s docs/ops/tools"""

import json
import tempfile
import unittest
from pathlib import Path

import check_release_notes as crn

DIGEST = "sha256:" + "a" * 64
TEMPLATE = Path(__file__).resolve().parents[1] / "release-notes-template.md"

GOOD = f"""# Passage v1.0.0

Release date: 2026-11-01.

## Images and charts

- `passage-org` {DIGEST}

## Features by module

### Organisation

- Companies and legal entities (FR-2.1)

## Migrations

None

## Breaking changes

None.

## CVE fixes

None

## Rollback

Roll back to v0.9.0 by digest.
"""


def drop_section(text: str, heading: str) -> str:
    out, skipping = [], False
    for line in text.splitlines():
        if line.startswith("## "):
            skipping = line[3:].strip() == heading
        if not skipping:
            out.append(line)
    return "\n".join(out) + "\n"


class TestCheck(unittest.TestCase):
    def test_good_notes_pass(self):
        self.assertEqual(crn.check(GOOD), [])

    def test_prerelease_title_passes(self):
        self.assertEqual(crn.check(GOOD.replace("v1.0.0", "v1.0.0-rc.1")), [])

    def test_bad_title_fails(self):
        errors = crn.check(GOOD.replace("# Passage v1.0.0", "# Passage one"))
        self.assertIn("missing title line '# Passage vX.Y.Z'", errors)

    def test_each_required_section_is_needed(self):
        for name in crn.REQUIRED:
            with self.subTest(section=name):
                errors = crn.check(drop_section(GOOD, name))
                self.assertIn(f"missing section '## {name}'", errors)

    def test_empty_section_fails(self):
        text = GOOD.replace("## CVE fixes\n\nNone\n", "## CVE fixes\n\n")
        self.assertIn("empty section '## CVE fixes'", crn.check(text))

    def test_a_section_holding_only_a_comment_is_empty(self):
        text = GOOD.replace("## CVE fixes\n\nNone\n", "## CVE fixes\n\n<!-- list the CVEs -->\n")
        self.assertIn("empty section '## CVE fixes'", crn.check(text))

    def test_images_need_a_digest(self):
        text = GOOD.replace(DIGEST, "passage-org:latest")
        self.assertIn(
            "Images and charts has no sha256 digest (images are published by digest)",
            crn.check(text),
        )

    def test_unedited_template_fails_on_digest_title_and_placeholders(self):
        errors = crn.check(TEMPLATE.read_text(encoding="utf-8"))
        self.assertIn("Images and charts has no sha256 digest (images are published by digest)", errors)
        self.assertIn("missing title line '# Passage vX.Y.Z'", errors)
        left = next(e for e in errors if e.startswith("template placeholders left"))
        for placeholder in ("<git sha>", "<module>", "YYYY-MM-DD", "NNNN"):
            self.assertIn(placeholder, left)

    def test_template_guidance_is_in_comments_so_its_breaking_section_says_none(self):
        secs = crn.sections(crn.strip_comments(TEMPLATE.read_text(encoding="utf-8")))
        for name in crn.REQUIRED:
            with self.subTest(section=name):
                self.assertTrue(secs[name].strip(), f"template section {name} is empty once comments are removed")
        self.assertEqual(crn.first_line(secs["Breaking changes"]), "none")

    def test_filled_template_passes(self):
        filled = (
            TEMPLATE.read_text(encoding="utf-8")
            .replace("vX.Y.Z", "v1.0.0", 1)
            .replace("YYYY-MM-DD", "2026-11-02")
            .replace("`<git sha>`", "`1a2b3c4`")
            .replace("`vX.Y.Z`", "`v0.9.0`")
            .replace("passage-<service>", "passage-org")
            .replace("sha256:<64 lowercase hex characters>", DIGEST)
            .replace("version `X.Y.Z`", "version `1.0.0`")
            .replace("`<registry>/<image>@sha256:<digest>`", f"`ghcr.io/passage/org@{DIGEST}`")
            .replace("### <module>", "### Organisation")
            .replace("- <feature, with its PRD requirement id, for example FR-NN.N>", "- Legal entities (FR-2.1)")
            .replace("`NNNN_<name>.sql`", "`0007_party_index.sql`")
            .replace('<contract step to revert, or "no contract step in this release">', "no contract step in this release")
        )
        self.assertEqual(crn.check(filled), [])

    def test_html_tags_and_autolinks_are_not_placeholders(self):
        text = GOOD.replace("Roll back to v0.9.0 by digest.", "Roll back to v0.9.0<br>see <https://docs.passage.dev/ops>.")
        self.assertEqual(crn.check(text), [])

    def test_breaking_changes_must_be_none_unless_allowed(self):
        text = GOOD.replace("## Breaking changes\n\nNone.\n", "## Breaking changes\n\n- org.party removed\n")
        self.assertIn(
            "Breaking changes must be 'None': no breaking change is allowed for N-1 clients (AC-50.5)",
            crn.check(text),
        )
        self.assertEqual(crn.check(text, allow_breaking=True), [])

    def test_evidence_with_breaking_service_contradicts_none(self):
        evidence = {"release": "v1.0.0", "verdict": "FAIL", "breakingServices": 2}
        self.assertIn(
            "Breaking changes says None but the compat evidence reports 2 breaking service(s)",
            crn.check(GOOD, evidence=evidence),
        )
        self.assertEqual(crn.check(GOOD, evidence={"release": "v1.0.0", "verdict": "PASS"}), [])

    def test_evidence_for_another_release_fails(self):
        errors = crn.check(GOOD, evidence={"release": "v0.9.0", "verdict": "PASS"})
        self.assertIn("compat evidence is for 'v0.9.0', not v1.0.0", errors)

    def test_heading_inside_code_fence_is_not_a_section(self):
        text = GOOD.replace("## Rollback\n\nRoll back", "```\n## Rollback\n```\n\n## Rollback\n\nRoll back")
        self.assertEqual(crn.check(text), [])
        secs = crn.sections("```\n## Rollback\n```\n")
        self.assertNotIn("Rollback", secs)


class TestMain(unittest.TestCase):
    def test_exit_codes(self):
        with tempfile.TemporaryDirectory() as tmp:
            good = Path(tmp) / "v1.md"
            good.write_text(GOOD, encoding="utf-8")
            bad = Path(tmp) / "bad.md"
            bad.write_text("# nothing\n", encoding="utf-8")
            self.assertEqual(crn.main([str(good)]), 0)
            self.assertEqual(crn.main([str(bad)]), 1)
            self.assertEqual(crn.main([str(Path(tmp) / "missing.md")]), 2)

    def test_compat_evidence_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            good = Path(tmp) / "v1.md"
            good.write_text(GOOD, encoding="utf-8")
            evidence = Path(tmp) / "evidence.json"
            body = {"schema": "passage.ops.compat-evidence/v1", "release": "v1.0.0", "verdict": "FAIL", "breakingServices": 1}
            evidence.write_text(json.dumps(body), encoding="utf-8")
            self.assertEqual(crn.main([str(good), "--compat-evidence", str(evidence)]), 1)
            evidence.write_text(json.dumps({**body, "verdict": "PASS", "breakingServices": 0}), encoding="utf-8")
            self.assertEqual(crn.main([str(good), "--compat-evidence", str(evidence)]), 0)
            evidence.write_text(json.dumps({"verdict": "PASS"}), encoding="utf-8")
            self.assertEqual(crn.main([str(good), "--compat-evidence", str(evidence)]), 2)
            evidence.write_text("{not json", encoding="utf-8")
            self.assertEqual(crn.main([str(good), "--compat-evidence", str(evidence)]), 2)


if __name__ == "__main__":
    unittest.main()
