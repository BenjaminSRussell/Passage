"""Tests for expand_contract_lint.py. Run: python3 -m unittest discover -s deploy/multi-region/tools"""

import unittest

import expand_contract_lint as ecl


def idx(**words: list[str]) -> dict[str, set[str]]:
    return {w: set(files) for w, files in words.items()}


class TestDropDetection(unittest.TestCase):
    def test_drop_column(self) -> None:
        drops, unparsed = ecl.dropped_names("ALTER TABLE org.legal_entity DROP COLUMN legacy_code;")
        self.assertEqual(drops, [("column", "legacy_code")])
        self.assertEqual(unparsed, 0)

    def test_drop_column_if_exists_and_quoted(self) -> None:
        drops, _ = ecl.dropped_names('ALTER TABLE "org"."unit" DROP COLUMN IF EXISTS "old_name";')
        self.assertEqual(drops, [("column", "old_name")])

    def test_multi_action_alter(self) -> None:
        sql = "ALTER TABLE org.unit ADD COLUMN x int, DROP COLUMN legacy_code;"
        drops, _ = ecl.dropped_names(sql)
        self.assertEqual(drops, [("column", "legacy_code")])

    def test_drop_table(self) -> None:
        drops, _ = ecl.dropped_names("DROP TABLE IF EXISTS org.old_units;")
        self.assertEqual(drops, [("table", "old_units")])

    def test_commented_drop_is_ignored(self) -> None:
        sql = "-- ALTER TABLE org.unit DROP COLUMN gone;\n/* DROP TABLE nope; */\nSELECT 1;"
        drops, unparsed = ecl.dropped_names(sql)
        self.assertEqual(drops, [])
        self.assertEqual(unparsed, 0)

    def test_unparseable_drop_is_counted(self) -> None:
        drops, unparsed = ecl.dropped_names("DROP SCHEMA org CASCADE;")
        self.assertEqual(drops, [])
        self.assertEqual(unparsed, 1)

    def test_add_column_is_not_a_drop(self) -> None:
        drops, unparsed = ecl.dropped_names("ALTER TABLE org.unit ADD COLUMN code text;")
        self.assertEqual((drops, unparsed), ([], 0))

    def test_drop_without_column_keyword(self) -> None:
        # Postgres makes COLUMN optional; a linter that needs the keyword misses this drop.
        drops, unparsed = ecl.dropped_names("ALTER TABLE org.unit DROP legacy_code CASCADE;")
        self.assertEqual((drops, unparsed), ([("column", "legacy_code")], 0))

    def test_several_drops_in_one_alter(self) -> None:
        sql = "ALTER TABLE org.unit DROP COLUMN a, DROP COLUMN IF EXISTS b, ADD COLUMN c numeric(18,2);"
        self.assertEqual(ecl.dropped_names(sql), ([("column", "a"), ("column", "b")], 0))

    def test_drop_table_list(self) -> None:
        drops, unparsed = ecl.dropped_names("DROP TABLE IF EXISTS org.a_old, org.b_old CASCADE;")
        self.assertEqual((drops, unparsed), ([("table", "a_old"), ("table", "b_old")], 0))

    def test_renames_count_as_contract_actions(self) -> None:
        sql = "ALTER TABLE org.unit RENAME COLUMN code TO unit_code;\nALTER TABLE org.old_unit RENAME TO unit_v2;"
        self.assertEqual(ecl.dropped_names(sql), ([("column", "code"), ("table", "old_unit")], 0))

    def test_constraint_and_default_drops_pass(self) -> None:
        sql = (
            "ALTER TABLE org.unit DROP CONSTRAINT unit_code_key;\n"
            "ALTER TABLE org.unit ALTER COLUMN code DROP DEFAULT, ALTER COLUMN code DROP NOT NULL;\n"
            "DROP INDEX org.unit_code_idx; DROP FUNCTION org.f();"
        )
        self.assertEqual(ecl.dropped_names(sql), ([], 0))

    def test_unquoted_names_fold_to_lower_case(self) -> None:
        self.assertEqual(ecl.dropped_names("ALTER TABLE Org.Unit DROP COLUMN Legacy_Code;")[0], [("column", "legacy_code")])

    def test_drop_inside_string_literal_is_ignored(self) -> None:
        sql = "COMMENT ON TABLE org.unit IS 'we will DROP TABLE org.unit; later';"
        self.assertEqual(ecl.dropped_names(sql), ([], 0))


class TestLint(unittest.TestCase):
    def test_drop_still_referenced_is_violation(self) -> None:
        migrations = {"0007_drop.sql": "ALTER TABLE org.unit DROP COLUMN legacy_code;"}
        code = idx(legacy_code=["services/org/internal/store/unit.go"])
        violations = ecl.lint(migrations, code)
        self.assertEqual(len(violations), 1)
        self.assertEqual(violations[0].name, "legacy_code")
        self.assertIn("OPS-1707", violations[0].message())

    def test_drop_with_no_references_passes(self) -> None:
        migrations = {"0007_drop.sql": "ALTER TABLE org.unit DROP COLUMN legacy_code;"}
        self.assertEqual(ecl.lint(migrations, idx(other=["x.go"])), [])

    def test_drop_of_name_only_n_minus_1_reads_is_violation(self) -> None:
        # BR-50.7: the release that stops using the column cannot also drop it, because the
        # previous release keeps running against the migrated schema during the rolling deploy.
        migrations = {"0007_drop.sql": "ALTER TABLE org.unit DROP COLUMN legacy_code;"}
        previous = idx(legacy_code=["prev/services/org/internal/store/unit.go"])
        violations = ecl.lint(migrations, idx(), previous)
        self.assertEqual([(v.name, v.release) for v in violations], [("legacy_code", "the previous release (N-1)")])
        self.assertIn("previous release (N-1)", violations[0].message())

    def test_drop_after_both_releases_stopped_passes(self) -> None:
        migrations = {"0008_drop.sql": "ALTER TABLE org.unit DROP COLUMN legacy_code;"}
        self.assertEqual(ecl.lint(migrations, idx(code=["a.go"]), idx(code=["a.go"])), [])

    def test_reference_in_both_releases_reports_once(self) -> None:
        migrations = {"0007_drop.sql": "ALTER TABLE org.unit DROP COLUMN legacy_code;"}
        code = idx(legacy_code=["a.go"])
        violations = ecl.lint(migrations, code, code)
        self.assertEqual([v.release for v in violations], ["this release"])

    def test_unparsed_drop_is_always_violation(self) -> None:
        violations = ecl.lint({"0009.sql": "DROP SCHEMA org;"}, {})
        self.assertEqual([v.kind for v in violations], ["unparsed"])

    def test_violations_sorted_by_file(self) -> None:
        migrations = {
            "0009.sql": "DROP TABLE b_table;",
            "0008.sql": "DROP TABLE a_table;",
        }
        code = idx(a_table=["a.go"], b_table=["b.go"])
        names = [v.file for v in ecl.lint(migrations, code)]
        self.assertEqual(names, ["0008.sql", "0009.sql"])


class TestCodeIndex(unittest.TestCase):
    def test_indexes_words_in_scanned_files(self) -> None:
        import tempfile
        from pathlib import Path

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "a.go").write_text("select legacy_code from unit", encoding="utf-8")
            (root / "b.bin").write_text("legacy_code", encoding="utf-8")
            index = ecl.code_identifiers([root])
            self.assertIn("legacy_code", index)
            self.assertEqual(len(index["legacy_code"]), 1)
            self.assertNotIn("b.bin", " ".join(index["legacy_code"]))

    def test_migrations_are_not_code_and_case_is_ignored(self) -> None:
        import tempfile
        from pathlib import Path

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "migrations").mkdir()
            (root / "migrations" / "0001.sql").write_text("CREATE TABLE u (legacy_code text);", encoding="utf-8")
            (root / "q.go").write_text('const q = "SELECT LEGACY_CODE FROM u"', encoding="utf-8")
            index = ecl.code_identifiers([root])
            self.assertEqual(sorted(Path(f).name for f in index["legacy_code"]), ["q.go"])


class TestMain(unittest.TestCase):
    def run_main(self, sql: str, current: str, previous: str) -> int:
        import tempfile
        from pathlib import Path

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for d in ("mig", "cur", "prev"):
                (root / d).mkdir()
            (root / "mig" / "0007.sql").write_text(sql, encoding="utf-8")
            (root / "cur" / "a.go").write_text(current, encoding="utf-8")
            (root / "prev" / "a.go").write_text(previous, encoding="utf-8")
            return ecl.main(
                ["--migrations", str(root / "mig"), "--code", str(root / "cur"), "--previous-code", str(root / "prev")]
            )

    def test_exit_codes(self) -> None:
        drop = "ALTER TABLE org.unit DROP COLUMN legacy_code;"
        self.assertEqual(self.run_main(drop, "x", "x"), 0)
        self.assertEqual(self.run_main(drop, "x", "legacy_code"), 1)
        self.assertEqual(self.run_main(drop, "legacy_code", "x"), 1)
        self.assertEqual(ecl.main(["--migrations", "/no/such/dir", "--code", ".", "--previous-code", "."]), 2)

    def test_previous_code_is_required(self) -> None:
        with self.assertRaises(SystemExit):
            ecl.main(["--migrations", ".", "--code", "."])


if __name__ == "__main__":
    unittest.main()
