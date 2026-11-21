"""Tests for check_docs.py. Run: python3 -m unittest discover -s docs/ops/tools"""

import tempfile
import unittest
from pathlib import Path

import check_docs as cd

OPS = Path(__file__).resolve().parents[1]
RULES = Path(__file__).resolve().parents[3] / "deploy" / "multi-region" / "chart" / "templates" / "prometheusrule.yaml"


def write(root: Path, rel: str, text: str) -> Path:
    path = root / rel
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    return path


class TestSlugs(unittest.TestCase):
    def test_github_style_slugs(self) -> None:
        self.assertEqual(cd.slugify("Failover: step 1"), "failover-step-1")
        self.assertEqual(cd.slugify("  Fail-back (FR-50.5) "), "fail-back-fr-505")
        self.assertEqual(cd.slugify("snake_case Heading"), "snake_case-heading")

    def test_headings_skip_code_fences(self) -> None:
        text = "# Top\n```\n# not a heading\n```\n## Second part\n"
        self.assertEqual(cd.headings(text), {"top", "second-part"})


class TestLinks(unittest.TestCase):
    def test_extracts_links_outside_code(self) -> None:
        text = "See [a](a.md) and [b](b.md#x \"title\").\n```\n[ignored](zzz.md)\n```\n"
        self.assertEqual(cd.links(text), ["a.md", "b.md#x"])


class TestCheckMarkdown(unittest.TestCase):
    def test_clean_tree_has_no_errors(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "ops"
            write(root, "README.md", "# Hub\n[runbook](runbooks/a.md#step-one)\n[ext](https://example.com/x)\n")
            write(root, "runbooks/a.md", "# A\n## Step one\n[back](../README.md#hub)\n")
            self.assertEqual(cd.check_markdown(root), [])

    def test_broken_file_link_reported(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "ops"
            write(root, "README.md", "[gone](missing.md)\n")
            errors = cd.check_markdown(root)
            self.assertEqual(len(errors), 1)
            self.assertIn("broken link missing.md", errors[0])

    def test_missing_anchor_reported(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "ops"
            write(root, "README.md", "[x](other.md#nope)\n")
            write(root, "other.md", "# Real heading\n")
            errors = cd.check_markdown(root)
            self.assertEqual(len(errors), 1)
            self.assertIn("missing anchor #nope", errors[0])

    def test_same_page_anchor(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "ops"
            write(root, "README.md", "# Top\n[self](#top)\n[bad](#absent)\n")
            errors = cd.check_markdown(root)
            self.assertEqual(len(errors), 1)
            self.assertIn("#absent", errors[0])


class TestCheckAlerts(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.base = Path(self.tmp.name)
        self.runbooks = self.base / "runbooks"
        self.runbooks.mkdir()
        (self.runbooks / "failover.md").write_text("# Failover\n", encoding="utf-8")

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def rules(self, body: str) -> Path:
        return write(self.base, "rules.yaml", body)

    def test_alert_with_existing_runbook_passes(self) -> None:
        rules = self.rules(
            "- alert: A\n  annotations:\n    runbook_url: https://docs.depot.dev/ops/runbooks/failover\n"
        )
        self.assertEqual(cd.check_alerts(rules, self.runbooks), [])

    def test_missing_runbook_page(self) -> None:
        rules = self.rules(
            "- alert: A\n  runbook_url: https://docs.depot.dev/ops/runbooks/nowhere\n"
        )
        errors = cd.check_alerts(rules, self.runbooks)
        self.assertEqual(len(errors), 1)
        self.assertIn("runbook page missing", errors[0])

    def test_alert_without_runbook_url(self) -> None:
        rules = self.rules("- alert: A\n  expr: up == 0\n- alert: B\n  runbook_url: https://docs.depot.dev/ops/runbooks/failover\n")
        errors = cd.check_alerts(rules, self.runbooks)
        self.assertIn("2 alerts but 1 runbook_url", errors[0])

    def test_url_outside_runbook_site(self) -> None:
        rules = self.rules("- alert: A\n  runbook_url: https://example.com/failover\n")
        errors = cd.check_alerts(rules, self.runbooks)
        self.assertIn("outside the runbook site", errors[0])


class TestRunbookHygiene(unittest.TestCase):
    def test_stray_non_markdown_file_flagged(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            runbooks = Path(tmp)
            (runbooks / "ok.md").write_text("# ok\n", encoding="utf-8")
            (runbooks / "leftover.md.tmp").write_text("draft", encoding="utf-8")
            errors = cd.check_runbook_dir(runbooks)
            self.assertEqual(len(errors), 1)
            self.assertIn("leftover.md.tmp", errors[0])


class TestRepoPaths(unittest.TestCase):
    def test_missing_service_path_reported_and_existing_one_passes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp)
            (repo / "services" / "ledger").mkdir(parents=True)
            root = repo / "docs" / "ops"
            write(root, "guide.md", "Owning area: `services/ledger`. Also `services/procurement-ts`.\n")
            errors = cd.check_repo_paths(root, repo)
            self.assertEqual(errors, ["guide.md: repository path services/procurement-ts does not exist"])

    def test_code_fences_and_other_prefixes_ignored(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp)
            root = repo / "docs" / "ops"
            write(root, "guide.md", "```\n`services/nowhere`\n```\nRun `tools/cost_meter.py`.\n")
            self.assertEqual(cd.check_repo_paths(root, repo), [])

    def test_trailing_slash_and_sentence_dot_accepted(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp)
            (repo / "apps" / "web" / "src" / "modules" / "org").mkdir(parents=True)
            root = repo / "docs" / "ops"
            write(root, "guide.md", "Screens: `apps/web/src/modules/org/` and `apps/web/src/modules/org.`\n")
            self.assertEqual(cd.check_repo_paths(root, repo), [])


class TestCheckFrTitles(unittest.TestCase):
    PRD = "- **FR-30.14 Customer portal** — Must. Text.\n- **FR-30.26 Returnable packaging charges** — Could. Text.\n- **FR-30.13 Statements** — Must. Text.\n"

    def repo(self, tmp: str, not_implemented: str, prd: str = PRD) -> Path:
        repo = Path(tmp)
        write(repo, "docs/prd/phase-5.md", prd)
        write(repo, "services/receivables/NOT-IMPLEMENTED.md", not_implemented)
        return repo

    def test_bold_id_with_another_requirements_title_is_flagged(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo = self.repo(tmp, "- **FR-30.26 Invoice and statement PDF download.** Built.\n")
            errors = cd.check_fr_titles(repo)
            self.assertEqual(len(errors), 1)
            self.assertIn("FR-30.26", errors[0])
            self.assertIn("Returnable packaging charges", errors[0])

    def test_matching_title_range_and_code_fence_are_not_flagged(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            text = (
                "- **FR-30.26 Returnable packaging.** Not built.\n"
                "- **FR-30.14 Customer portal.** Built.\n"
                "- **FR-30.14 to FR-30.26.** Range.\n"
                "```\n- **FR-30.26 Wrong title.**\n```\n"
            )
            self.assertEqual(cd.check_fr_titles(self.repo(tmp, text)), [])

    def test_id_missing_from_the_prd_is_flagged_in_bold_and_in_a_table_row(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            text = "- **FR-99.1 Ghost.** Built.\n| Ghost feature (FR-99.2) | built | x |\n"
            errors = cd.check_fr_titles(self.repo(tmp, text))
            self.assertEqual(len(errors), 2)
            self.assertTrue(all("not in the PRD" in e for e in errors))

    def test_table_row_with_another_requirements_title_is_flagged(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo = self.repo(tmp, "| Invoice and statement PDF download (FR-30.26) | built | x |\n")
            errors = cd.check_fr_titles(repo)
            self.assertEqual(len(errors), 1)
            self.assertIn("FR-30.26", errors[0])
            self.assertIn("Returnable packaging charges", errors[0])

    def test_table_row_with_matching_title_is_not_flagged(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            repo = self.repo(tmp, "| Customer portal order requests (FR-30.14) | built | x |\n")
            self.assertEqual(cd.check_fr_titles(repo), [])

    def test_empty_prd_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            errors = cd.check_fr_titles(self.repo(tmp, "- **FR-30.14 Customer portal.**\n", prd="no titles here\n"))
            self.assertEqual(len(errors), 1)
            self.assertIn("no FR titles found", errors[0])

    def test_missing_prd_directory_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual(len(cd.check_fr_titles(Path(tmp))), 1)


class TestRepository(unittest.TestCase):
    """Runs the checks against the real docs/ops tree and the real alert rules."""

    def test_repository_markdown_links_resolve(self) -> None:
        self.assertEqual(cd.check_markdown(OPS), [])

    def test_every_alert_links_to_an_existing_runbook(self) -> None:
        self.assertEqual(cd.check_alerts(RULES, OPS / "runbooks"), [])

    def test_repository_paths_in_docs_exist(self) -> None:
        self.assertEqual(cd.check_repo_paths(OPS, OPS.parents[1]), [])

    def test_not_implemented_fr_ids_match_the_prd(self) -> None:
        self.assertEqual(cd.check_fr_titles(OPS.parents[1]), [])


if __name__ == "__main__":
    unittest.main()
