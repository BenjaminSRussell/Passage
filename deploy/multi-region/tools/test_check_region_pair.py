"""Tests for check_region_pair.py. Run: python3 -m unittest discover -s deploy/multi-region/tools"""

import io
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

import check_region_pair as crp

PRIMARY_TEXT = """
# eu-west is PRIMARY
region:
  id: eu-west
  role: PRIMARY
postgres:
  primary:
    host: passage-pg-rw.eu-central.passage.internal
kafka:
  localAlias: eu-west
  remoteAlias: eu-central
mm2:
  sourceAlias: eu-west
  targetAlias: eu-central
"""

PASSIVE_TEXT = """
region:
  id: eu-central
  role: PASSIVE
postgres:
  primary:
    host: passage-pg-rw.eu-west.passage.internal
kafka:
  localAlias: eu-central
  remoteAlias: eu-west
mm2:
  sourceAlias: eu-west
  targetAlias: eu-central
"""

CHART = """
region:
  id: eu-west
  role: PRIMARY
postgres:
  primary:
    host: passage-pg-rw.eu-central.passage.internal
    port: 5432
kafka:
  localAlias: eu-west
  remoteAlias: eu-central
mm2:
  sourceAlias: eu-west
  targetAlias: eu-central
  topicsPattern: "passage\\\\.(?!internal\\\\.).*"
  lagAlertMessages: 100000    # mm2.lag_alert_messages, >= 1000
failover:
  maxLagSeconds: 300
  dnsTtlSeconds: 60
  twoPersonWindowMinutes: 5
slo:
  burnFast: 14.4
  burnSlow: 6
license:
  warnDays: [30, 7, 1]
fence:
  enabled: false
"""


def pair() -> dict:
    return {"eu-west": crp.parse(PRIMARY_TEXT), "eu-central": crp.parse(PASSIVE_TEXT)}


def chart() -> dict:
    return crp.parse(CHART)


def failed_over() -> dict:
    """The pair after `passage-ops failover --to eu-central` (docs/ops/runbooks/failover.md step 3)."""
    files = pair()
    files["eu-west"]["region"]["role"] = "FENCED"
    files["eu-central"]["region"]["role"] = "PRIMARY"
    for doc in files.values():
        doc["mm2"]["sourceAlias"], doc["mm2"]["targetAlias"] = "eu-central", "eu-west"
    return files


class TestParse(unittest.TestCase):
    def test_nested_mapping_scalars_and_flow_list(self):
        doc = crp.parse("a:\n  b: 3\n  c: 1.5\n  d: true\n  e: [30, 7, 1]\n  f: \"ANY 1\"\n  g: host:9093\n")
        self.assertEqual(doc, {"a": {"b": 3, "c": 1.5, "d": True, "e": [30, 7, 1], "f": "ANY 1", "g": "host:9093"}})

    def test_comments_stripped_but_hash_inside_word_or_quotes_kept(self):
        doc = crp.parse("x: 5 # trailing\ny: v#1\nz: \"a #b\"  # c\n")
        self.assertEqual(doc, {"x": 5, "y": "v#1", "z": "a #b"})

    def test_sibling_after_nested_block(self):
        self.assertEqual(crp.parse("a:\n  b:\n    c: 1\n  d: 2\ne: 3\n"), {"a": {"b": {"c": 1}, "d": 2}, "e": 3})

    def test_rejected_input(self):
        for text in (
            "a: 1\na: 2\n",  # duplicate key
            "just words\n",  # no colon
            "a:\n\tb: 1\n",  # tab indentation
            "a:\n  - 1\n",  # block list
            "a: &anchor 1\n",  # anchor
            "a: [1, 2\n",  # unterminated flow list
            "a:b\n",  # no space after the colon
            "a: 1\n  b: 2\n",  # key indented under a scalar
            "a:\n    b: 1\n  c: 2\n",  # keys of one mapping at two indents
        ):
            with self.assertRaises(ValueError, msg=repr(text)):
                crp.parse(text)

    def test_merge_follows_helm(self):
        base = {"a": {"b": 1, "c": [1, 2]}, "d": 1}
        got = crp.merge(base, {"a": {"c": [3]}, "e": 2})
        self.assertEqual(got, {"a": {"b": 1, "c": [3]}, "d": 1, "e": 2})
        self.assertEqual(base, {"a": {"b": 1, "c": [1, 2]}, "d": 1})


class TestCheckPair(unittest.TestCase):
    def test_reference_pair_is_clean(self):
        self.assertEqual(crp.check_pair(pair(), chart()), [])

    def test_pair_after_failover_is_clean(self):
        self.assertEqual(crp.check_pair(failed_over(), chart()), [])

    def test_failover_that_forgot_to_swap_mm2_in_one_file(self):
        files = failed_over()
        files["eu-west"]["mm2"]["sourceAlias"], files["eu-west"]["mm2"]["targetAlias"] = "eu-west", "eu-central"
        self.assertEqual(
            crp.check_pair(files, chart()),
            [
                "region files disagree on the MirrorMaker 2 direction "
                "(eu-central: eu-central -> eu-west; eu-west: eu-west -> eu-central)"
            ],
        )

    def test_failover_that_promoted_without_fencing(self):
        files = failed_over()
        files["eu-west"]["region"]["role"] = "PRIMARY"
        self.assertIn("exactly one PRIMARY is required across the pair, found 2", crp.check_pair(files, chart()))

    def test_no_primary(self):
        files = pair()
        files["eu-west"]["region"]["role"] = "FENCED"
        self.assertIn("exactly one PRIMARY is required across the pair, found 0", crp.check_pair(files, chart()))

    def test_unknown_role(self):
        files = pair()
        files["eu-central"]["region"]["role"] = "ACTIVE"
        self.assertIn(
            "eu-central: region.role 'ACTIVE' is not one of PRIMARY, PASSIVE, FENCED", crp.check_pair(files, chart())
        )

    def test_region_id_must_match_file_name(self):
        files = {"eu-north": crp.parse(PASSIVE_TEXT), "eu-west": crp.parse(PRIMARY_TEXT)}
        self.assertIn(
            "eu-north: region.id is 'eu-central', expected the file name 'eu-north'", crp.check_pair(files, chart())
        )

    def test_missing_region_id_inherits_the_default_and_is_caught(self):
        files = pair()
        del files["eu-central"]["region"]["id"]
        self.assertIn(
            "eu-central: region.id is 'eu-west', expected the file name 'eu-central'", crp.check_pair(files, chart())
        )

    def test_mm2_must_run_from_the_primary(self):
        files = pair()
        for doc in files.values():
            doc["mm2"]["sourceAlias"], doc["mm2"]["targetAlias"] = "eu-central", "eu-west"
        self.assertEqual(
            crp.check_pair(files, chart()),
            ["MirrorMaker 2 must replicate from the PRIMARY eu-west to eu-central, got eu-central -> eu-west"],
        )

    def test_passive_region_file_must_name_primary_endpoint(self):
        files = pair()
        del files["eu-central"]["postgres"]
        self.assertEqual(
            crp.check_pair(files, chart()),
            ["eu-central: a PASSIVE region file must name postgres.primary.host (the PRIMARY's endpoint)"],
        )

    def test_kafka_aliases(self):
        files = pair()
        files["eu-west"]["kafka"]["remoteAlias"] = "eu-west"
        files["eu-central"]["kafka"]["localAlias"] = "eu-west"
        problems = crp.check_pair(files, chart())
        self.assertIn("eu-west: kafka.remoteAlias is 'eu-west', expected 'eu-central'", problems)
        self.assertIn("eu-central: kafka.localAlias is 'eu-west', expected 'eu-central'", problems)

    def test_exactly_two_files(self):
        self.assertEqual(crp.check_pair({}, chart()), ["a region pair needs exactly two region files, found 0: -"])
        files = pair()
        files["eu-north"] = crp.parse(PASSIVE_TEXT.replace("eu-central", "eu-north"))
        self.assertEqual(
            crp.check_pair(files, chart()),
            ["a region pair needs exactly two region files, found 3: eu-central, eu-north, eu-west"],
        )


class TestCheckRanges(unittest.TestCase):
    def test_reference_chart_is_clean(self):
        self.assertEqual(crp.check_ranges(chart()), [])

    def test_integer_bounds(self):
        for key, bad in (
            ("maxLagSeconds", 29),
            ("maxLagSeconds", 3601),
            ("dnsTtlSeconds", 301),
            ("dnsTtlSeconds", 29),
            ("twoPersonWindowMinutes", 0),
            ("twoPersonWindowMinutes", True),
            ("twoPersonWindowMinutes", "5"),
        ):
            doc = chart()
            doc["failover"][key] = bad
            self.assertEqual(len(crp.check_ranges(doc)), 1, (key, bad))
            self.assertIn(f"failover.{key} must be an integer", crp.check_ranges(doc)[0])

    def test_bounds_are_inclusive(self):
        doc = chart()
        doc["failover"].update(maxLagSeconds=3600, dnsTtlSeconds=30, twoPersonWindowMinutes=30)
        doc["mm2"]["lagAlertMessages"] = 1000
        self.assertEqual(crp.check_ranges(doc), [])

    def test_alert_threshold_floor(self):
        doc = chart()
        doc["mm2"]["lagAlertMessages"] = 999
        self.assertEqual(
            crp.check_ranges(doc), ["chart defaults: mm2.lagAlertMessages must be an integer >= 1000, got 999"]
        )

    def test_fast_burn_must_exceed_slow(self):
        doc = chart()
        doc["slo"]["burnFast"] = 6
        self.assertEqual(
            crp.check_ranges(doc), ["chart defaults: slo.burnFast must exceed slo.burnSlow > 0, got 6 and 6"]
        )

    def test_warn_days(self):
        for days in ([], [0], [400], [7, 7], None):
            doc = chart()
            doc["license"]["warnDays"] = days
            self.assertEqual(len(crp.check_ranges(doc)), 1, days)
        doc = chart()
        doc["license"]["warnDays"] = [1, 7, 30]  # the template bands do not depend on order
        self.assertEqual(crp.check_ranges(doc), [])


class TestCheck(unittest.TestCase):
    def test_region_override_out_of_range_is_caught(self):
        files = pair()
        files["eu-central"]["failover"] = {"dnsTtlSeconds": 600}
        self.assertEqual(
            crp.check(files, chart()),
            ["eu-central: failover.dnsTtlSeconds must be an integer in 30-300, got 600"],
        )

    def test_fence_default_must_be_false(self):
        doc = chart()
        doc["fence"]["enabled"] = True
        self.assertEqual(
            crp.check(pair(), doc),
            ["chart defaults: fence.enabled must be false; a region is fenced by its FENCED role or a game day"],
        )


US_EAST = """
region:
  id: eastus2
  role: PRIMARY
  provider: aks
postgres:
  primary:
    host: passage-pg-rw.centralus.passage.internal
backup:
  store: azure-blob
  azure:
    storageAccount: acct1
    storageAccountRegion: eastus2
    container: pg-backups
kafka:
  localAlias: eastus2
  remoteAlias: centralus
mm2:
  sourceAlias: eastus2
  targetAlias: centralus
"""

US_CENTRAL = """
region:
  id: centralus
  role: PASSIVE
  provider: aks
postgres:
  primary:
    host: passage-pg-rw.eastus2.passage.internal
backup:
  store: azure-blob
  azure:
    storageAccount: acct2
    storageAccountRegion: centralus
    container: pg-backups
kafka:
  localAlias: centralus
  remoteAlias: eastus2
mm2:
  sourceAlias: eastus2
  targetAlias: centralus
"""


class TestMain(unittest.TestCase):
    def _run(self, *argv: str) -> tuple[int, str]:
        buf = io.StringIO()
        with redirect_stdout(buf):
            code = crp.main(list(argv))
        return code, buf.getvalue()

    def _layout(self, root: Path, primary: str, passive: str) -> list[str]:
        values = root / "values"
        values.mkdir()
        (values / "eu-west.yaml").write_text(primary, encoding="utf-8")
        (values / "eu-central.yaml").write_text(passive, encoding="utf-8")
        chart_path = root / "chart-values.yaml"
        chart_path.write_text(CHART, encoding="utf-8")
        return ["--values-dir", str(values), "--chart-values", str(chart_path)]

    def test_repository_values_pass(self):
        code, text = self._run()
        self.assertEqual(code, 0, text)
        self.assertIn("values: eu-central, eu-west", text)
        self.assertIn("us: centralus, eastus2", text)

    def _profile_layout(self, root: Path, east: str, central: str, subdir: str = "us") -> list[str]:
        values = root / "values" / subdir
        values.mkdir(parents=True)
        (values / "eastus2.yaml").write_text(east, encoding="utf-8")
        (values / "centralus.yaml").write_text(central, encoding="utf-8")
        chart_path = root / "chart-values.yaml"
        chart_path.write_text(CHART, encoding="utf-8")
        return ["--values-dir", str(values), "--chart-values", str(chart_path)]

    def test_us_pair_passes_gcc_and_commercial(self):
        for selector in ("gcc", "commercial", "commercial+hipaa"):
            code, text = self._run("--values-dir", str(crp.VALUES_DIR / "us"), "--cloud-profile", selector)
            self.assertEqual(code, 0, text)

    def test_a_region_outside_allowed_regions_fails(self):
        code, text = self._run("--values-dir", str(crp.VALUES_DIR), "--cloud-profile", "gcc")
        self.assertEqual(code, 1)
        self.assertIn("eu-west: region is not in the gcc allowedRegions", text)
        code, text = self._run("--values-dir", str(crp.VALUES_DIR / "us"), "--cloud-profile", "gcc-high")
        self.assertEqual(code, 1)
        self.assertIn("eastus2: region is not in the gcc-high allowedRegions", text)

    def test_region_cloud_profile_in_the_file_is_enforced(self):
        with tempfile.TemporaryDirectory() as tmp:
            east = US_EAST.replace("  provider: aks", "  provider: aks\n  cloudProfile: gcc")
            central = US_CENTRAL.replace("  provider: aks", "  provider: aks\n  cloudProfile: gcc")
            self.assertEqual(self._run(*self._profile_layout(Path(tmp), east, central))[0], 0)
        with tempfile.TemporaryDirectory() as tmp:
            east = US_EAST.replace("eastus2", "eastus2").replace("  provider: aks", "  provider: aks\n  cloudProfile: gcc-high")
            central = US_CENTRAL.replace("  provider: aks", "  provider: aks\n  cloudProfile: gcc-high")
            code, text = self._run(*self._profile_layout(Path(tmp), east, central))
            self.assertEqual(code, 1, text)

    def test_files_naming_different_profiles_fail(self):
        with tempfile.TemporaryDirectory() as tmp:
            east = US_EAST.replace("  provider: aks", "  provider: aks\n  cloudProfile: gcc")
            central = US_CENTRAL.replace("  provider: aks", "  provider: aks\n  cloudProfile: commercial")
            code, text = self._run(*self._profile_layout(Path(tmp), east, central))
            self.assertEqual(code, 1)
            self.assertIn("different cloud profiles", text)

    def test_aks_backups_must_be_azure_blob_in_the_same_allowed_region(self):
        with tempfile.TemporaryDirectory() as tmp:
            argv = self._profile_layout(Path(tmp), US_EAST.replace("store: azure-blob", "store: s3"), US_CENTRAL)
            code, text = self._run(*argv, "--cloud-profile", "gcc")
            self.assertEqual(code, 1)
            self.assertIn("needs backup.store azure-blob", text)
        with tempfile.TemporaryDirectory() as tmp:
            argv = self._profile_layout(Path(tmp), US_EAST.replace("storageAccountRegion: eastus2", "storageAccountRegion: westeurope"), US_CENTRAL)
            code, text = self._run(*argv, "--cloud-profile", "gcc")
            self.assertEqual(code, 1)
            self.assertIn("expected the region itself", text)
            self.assertIn("westeurope' is not in the gcc allowedRegions", text)

    def test_backup_destination_must_be_on_the_profile_blob_suffix(self):
        with tempfile.TemporaryDirectory() as tmp:
            east = US_EAST.replace("    container: pg-backups", "    container: pg-backups\n  destinationPath: https://acct.blob.core.usgovcloudapi.net/pg/")
            code, text = self._run(*self._profile_layout(Path(tmp), east, US_CENTRAL), "--cloud-profile", "gcc")
            self.assertEqual(code, 1)
            self.assertIn("must be an https URL on *.blob.core.windows.net", text)

    EU_OVERLAY = "commercial+eu-data-boundary"

    def _eu_layout(self, root: Path, west: str, north: str) -> list[str]:
        values = root / "values" / "eu-azure"
        values.mkdir(parents=True)
        (values / "westeurope.yaml").write_text(west, encoding="utf-8")
        (values / "northeurope.yaml").write_text(north, encoding="utf-8")
        chart_path = root / "chart-values.yaml"
        chart_path.write_text(CHART, encoding="utf-8")
        return ["--values-dir", str(values), "--chart-values", str(chart_path)]

    @staticmethod
    def _eu(text: str) -> str:
        return text.replace("eastus2", "westeurope").replace("centralus", "northeurope")

    def test_eu_azure_pair_passes_the_eu_data_boundary(self):
        for selector in (self.EU_OVERLAY, "commercial"):
            code, text = self._run("--values-dir", str(crp.VALUES_DIR / "eu-azure"), "--cloud-profile", selector)
            self.assertEqual(code, 0, text)
            self.assertIn("northeurope, westeurope", text)
        with tempfile.TemporaryDirectory() as tmp:
            code, text = self._run(*self._eu_layout(Path(tmp), self._eu(US_EAST), self._eu(US_CENTRAL)), "--cloud-profile", self.EU_OVERLAY)
            self.assertEqual(code, 0, text)

    def test_eu_and_us_pairs_do_not_pass_each_others_profile(self):
        code, text = self._run("--values-dir", str(crp.VALUES_DIR / "eu-azure"), "--cloud-profile", "gcc")
        self.assertEqual(code, 1)
        self.assertIn("westeurope: region is not in the gcc allowedRegions", text)
        code, text = self._run("--values-dir", str(crp.VALUES_DIR / "us"), "--cloud-profile", self.EU_OVERLAY)
        self.assertEqual(code, 1)
        self.assertIn("eastus2: region is not in the commercial+eu-data-boundary allowedRegions", text)

    def test_eu_overlay_refuses_a_backup_outside_the_eu(self):
        west = self._eu(US_EAST).replace("storageAccountRegion: westeurope", "storageAccountRegion: eastus2")
        with tempfile.TemporaryDirectory() as tmp:
            code, text = self._run(*self._eu_layout(Path(tmp), west, self._eu(US_CENTRAL)), "--cloud-profile", self.EU_OVERLAY)
            self.assertEqual(code, 1)
            self.assertIn("expected the region itself ('westeurope')", text)
            self.assertIn("backup storage region 'eastus2' is not in the commercial+eu-data-boundary allowedRegions", text)

    def test_a_restricting_profile_refuses_an_s3_backup_on_any_provider(self):
        for provider in ("aks", "eks"):
            store_s3 = self._eu(US_EAST).replace("provider: aks", f"provider: {provider}").replace("store: azure-blob", "store: s3")
            with tempfile.TemporaryDirectory() as tmp:
                code, text = self._run(*self._eu_layout(Path(tmp), store_s3, self._eu(US_CENTRAL)), "--cloud-profile", self.EU_OVERLAY)
                self.assertEqual(code, 1, provider)
                self.assertTrue("s3 backup destination is not allowed" in text or "needs backup.store azure-blob" in text, text)
        eks_path = self._eu(US_EAST).replace("provider: aks", "provider: eks").replace(
            "    container: pg-backups", "    container: pg-backups\n  destinationPath: s3://passage-pg-backups-eu/"
        )
        with tempfile.TemporaryDirectory() as tmp:
            code, text = self._run(*self._eu_layout(Path(tmp), eks_path, self._eu(US_CENTRAL)), "--cloud-profile", self.EU_OVERLAY)
            self.assertEqual(code, 1)
            self.assertIn("westeurope: an s3 backup destination is not allowed under commercial+eu-data-boundary", text)

    def test_an_unrestricted_profile_still_allows_s3_on_a_non_aks_provider(self):
        eks = self._eu(US_EAST).replace("provider: aks", "provider: eks").replace("store: azure-blob", "store: s3")
        with tempfile.TemporaryDirectory() as tmp:
            code, text = self._run(*self._eu_layout(Path(tmp), eks, self._eu(US_CENTRAL)), "--cloud-profile", "commercial")
            self.assertEqual(code, 0, text)

    def test_unknown_profile_is_reported(self):
        code, text = self._run("--values-dir", str(crp.VALUES_DIR / "us"), "--cloud-profile", "azure-stack")
        self.assertEqual(code, 1)
        self.assertIn("unknown cloud profile", text)

    def test_two_primaries_exit_one(self):
        with tempfile.TemporaryDirectory() as tmp:
            argv = self._layout(Path(tmp), PRIMARY_TEXT, PASSIVE_TEXT.replace("role: PASSIVE", "role: PRIMARY"))
            code, text = self._run(*argv)
            self.assertEqual(code, 1)
            self.assertIn("FAIL  exactly one PRIMARY is required across the pair, found 2", text)

    def test_clean_layout_exits_zero(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual(self._run(*self._layout(Path(tmp), PRIMARY_TEXT, PASSIVE_TEXT))[0], 0)

    def test_unparseable_input_is_usage_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            argv = self._layout(Path(tmp), PRIMARY_TEXT, "not a mapping\n")
            self.assertEqual(self._run(*argv)[0], 2)


if __name__ == "__main__":
    unittest.main()
