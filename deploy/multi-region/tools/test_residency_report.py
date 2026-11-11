"""Tests for residency_report.py. Run: python3 -m unittest discover -s deploy/multi-region/tools"""

import csv
import io
import json
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

import check_region_pair as crp
import residency_report as rr

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "cloud-profiles"))
import cloudprofile  # noqa: E402

CHART = crp.parse(rr.CHART_VALUES.read_text(encoding="utf-8"))

# The same vectors as platform/go/residency TestCheck, so the two implementations cannot drift apart.
VECTORS = [
    ("EU", "westeurope", True), ("EU", "swedencentral", True), ("eu", " WestEurope ", True), ("EU", "eu-west", True),
    ("EU", "eu-central", True), ("EU", "eastus2", False), ("EU", "us-east", False), ("EU", "uksouth", False),
    ("EU", "", False), ("EU", "mars", False), ("US", "eastus2", True), ("US", "us-east", True), ("US", "westeurope", False),
    ("UK", "uksouth", True), ("UK", "northeurope", False), ("GLOBAL", "westeurope", True), ("GLOBAL", "us-east", True),
    ("GLOBAL", "anything", True), ("GLOBAL", "", False), ("", "eastus", True), ("ATLANTIS", "westeurope", False),
]

EU_WEST = """
region:
  id: westeurope
  role: PRIMARY
  provider: aks
backup:
  store: azure-blob
  azure:
    storageAccount: acct1
    storageAccountRegion: westeurope
objectStore:
  region: westeurope
kafka:
  localAlias: westeurope
  remoteAlias: northeurope
"""

EU_NORTH = """
region:
  id: northeurope
  role: PASSIVE
  provider: aks
backup:
  store: azure-blob
  azure:
    storageAccount: acct2
    storageAccountRegion: northeurope
objectStore:
  region: northeurope
kafka:
  localAlias: northeurope
  remoteAlias: westeurope
"""


def tenant(tenant_id, residency, region, name=None):
    t = {"tenantId": tenant_id, "name": name or tenant_id, "residency": residency}
    if region is not None:
        t["region"] = region
    return t


def regions_of(**files):
    """{region id: (path, merged doc)} from region file texts keyed by file stem."""
    return {
        crp._get(crp.parse(text), "region.id"): (Path(f"{stem}.yaml"), crp.merge(CHART, crp.parse(text)))
        for stem, text in files.items()
    }


def violations(report, tenant_id=None):
    return [v for v in report["violations"] if tenant_id in (None, v["tenantId"])]


class TestTable(unittest.TestCase):
    def setUp(self):
        self.table = rr.load_table()

    def test_vectors_match_the_go_implementation(self):
        for code, region, want in VECTORS:
            self.assertEqual(rr.allows(self.table, code, region), want, (code, region))

    def test_eu_is_the_eu_data_boundary_overlay_plus_legacy_ids(self):
        eu = set(self.table["residencies"]["EU"]["regions"])
        overlay = set(cloudprofile.resolve(cloudprofile.load(), "commercial", ["eu-data-boundary"])["allowedRegions"])
        self.assertTrue(overlay)
        self.assertEqual(eu - overlay, {"eu-west", "eu-central", "eu-north"})
        self.assertLessEqual(overlay, eu)

    def test_us_holds_every_gcc_region_and_no_region_is_in_two_residencies(self):
        us = set(self.table["residencies"]["US"]["regions"])
        self.assertLessEqual(set(cloudprofile.resolve(cloudprofile.load(), "gcc", [])["allowedRegions"]), us)
        seen: dict[str, str] = {}
        for code, entry in self.table["residencies"].items():
            for region in entry["regions"]:
                self.assertNotIn(region, seen, f"{region} is in {seen.get(region)} and {code}")
                seen[region] = code

    def test_the_table_is_the_file_go_embeds(self):
        # Go embeds this very file (go:embed residency.json in platform/go/residency), so there is no copy to drift.
        self.assertEqual(rr.RESIDENCY_TABLE.name, "residency.json")
        self.assertEqual(rr.RESIDENCY_TABLE.parent.name, "residency")
        self.assertTrue((rr.RESIDENCY_TABLE.parent / "residency.go").read_text(encoding="utf-8").count("go:embed residency.json") == 1)


class TestReport(unittest.TestCase):
    def setUp(self):
        self.table = rr.load_table()

    def test_an_eu_tenant_whose_backup_is_in_a_us_region_is_a_violation(self):
        west = EU_WEST.replace("storageAccountRegion: westeurope", "storageAccountRegion: eastus2")
        report = rr.build_report([tenant("t1", "EU", "westeurope")], regions_of(westeurope=west, northeurope=EU_NORTH), self.table)
        (violation,) = report["violations"]
        self.assertEqual((violation["kind"], violation["store"], violation["regions"]), ("outside-residency", "backups", ["eastus2"]))
        self.assertEqual(violation["tenantId"], "t1")
        status = {r["store"]: (r["status"], r["regions"]) for r in report["rows"]}
        self.assertEqual(status["backups"], ("violation", ["eastus2", "northeurope"]))
        self.assertEqual(status["database"][0], "ok")
        self.assertEqual(status["objectStore"][0], "ok")
        self.assertEqual(status["kafka"][0], "ok")

    def test_an_s3_backup_in_a_us_bucket_is_found_through_the_destination_path(self):
        east = EU_WEST.replace("backup:\n  store: azure-blob\n  azure:\n    storageAccount: acct1\n    storageAccountRegion: westeurope\n",
                               "backup:\n  store: s3\n  destinationPath: s3://passage-pg-backups-us-east/\n")
        report = rr.build_report([tenant("t1", "EU", "westeurope")], regions_of(westeurope=east, northeurope=EU_NORTH), self.table)
        (violation,) = report["violations"]
        self.assertEqual((violation["store"], violation["regions"]), ("backups", ["us-east"]))
        row = next(r for r in report["rows"] if r["store"] == "backups")
        self.assertTrue(row["derived"])

    def test_the_s3_default_bucket_is_derived_from_the_region_id(self):
        legacy = "region:\n  id: eu-west\n  role: PRIMARY\n  provider: eks\nkafka:\n  localAlias: eu-west\n  remoteAlias: eu-central\n"
        peer = legacy.replace("eu-west", "X").replace("eu-central", "eu-west").replace("id: X", "id: eu-central")
        report = rr.build_report([tenant("t1", "EU", "eu-west")], regions_of(**{"eu-west": legacy, "eu-central": peer}), self.table)
        self.assertEqual(report["violations"], [])
        row = next(r for r in report["rows"] if r["store"] == "backups")
        self.assertEqual((row["regions"], row["derived"]), (["eu-central", "eu-west"], True))

    def test_clean_pair_and_one_row_per_tenant_per_store(self):
        tenants = [tenant("t1", "EU", "westeurope"), tenant("t2", "GLOBAL", "northeurope")]
        report = rr.build_report(tenants, regions_of(westeurope=EU_WEST, northeurope=EU_NORTH), self.table)
        self.assertEqual(report["violations"], [])
        self.assertEqual(len(report["rows"]), 2 * len(rr.STORES))
        self.assertEqual({(r["tenantId"], r["store"]) for r in report["rows"]}, {(t, s) for t in ("t1", "t2") for s in rr.STORES})
        self.assertEqual(report["summary"], {"tenants": 2, "rows": 8, "violations": 0, "tenantsWithViolations": 0})

    def test_an_eu_tenant_placed_in_the_us_is_flagged_as_placement_and_in_every_store(self):
        regions = rr.load_regions(rr.VALUES_DIR, CHART)
        report = rr.build_report([tenant("t1", "EU", "eastus2")], regions, self.table)
        kinds = {(v["kind"], v["store"]) for v in report["violations"]}
        self.assertIn(("placement", ""), kinds)
        for store in rr.STORES:
            self.assertIn(("outside-residency", store), kinds)

    def test_repository_values_hold_both_eu_pairs_clean(self):
        regions = rr.load_regions(rr.VALUES_DIR, CHART)
        tenants = [tenant("a", "EU", "westeurope"), tenant("b", "EU", "northeurope"), tenant("c", "EU", "eu-west"), tenant("d", "US", "eastus2")]
        self.assertEqual(rr.build_report(tenants, regions, self.table)["violations"], [])

    def test_unplaced_unverifiable_and_unknown_residency(self):
        regions = regions_of(westeurope=EU_WEST, northeurope=EU_NORTH)
        report = rr.build_report(
            [tenant("t1", "EU", None), tenant("t2", "EU", "francecentral"), tenant("t3", "ATLANTIS", "westeurope"), tenant("t4", "GLOBAL", None)],
            regions,
            self.table,
        )
        self.assertEqual({(v["tenantId"], v["kind"]) for v in report["violations"]},
                         {("t1", "unplaced"), ("t2", "no-region-values"), ("t3", "unknown-residency")})
        by = {(r["tenantId"], r["store"]): r["status"] for r in report["rows"]}
        self.assertEqual(by[("t1", "backups")], "unverified")
        self.assertEqual(by[("t3", "backups")], "unverified")
        self.assertEqual(by[("t4", "backups")], "unverified")  # GLOBAL and unplaced: no violation, but no evidence either

    def test_a_pair_whose_peer_has_no_file_assumes_the_peer_region_id(self):
        report = rr.build_report([tenant("t1", "US", "westeurope")], regions_of(westeurope=EU_WEST), self.table)
        row = next(r for r in report["rows"] if r["store"] == "backups")
        self.assertEqual(row["regions"], ["northeurope", "westeurope"])
        self.assertIn("no region file", row["source"])

    def test_csv_has_one_line_per_row(self):
        report = rr.build_report([tenant("t1", "EU", "westeurope")], regions_of(westeurope=EU_WEST, northeurope=EU_NORTH), self.table)
        lines = list(csv.reader(io.StringIO(rr.to_csv(report))))
        self.assertEqual(tuple(lines[0]), rr.CSV_COLUMNS)
        self.assertEqual(len(lines) - 1, len(rr.STORES))
        self.assertEqual(lines[1][:6], ["t1", "t1", "EU", "westeurope", "database", "northeurope;westeurope"])


class TestMain(unittest.TestCase):
    def _run(self, *argv):
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = rr.main(list(argv))
        return code, out.getvalue(), err.getvalue()

    def _values(self, root: Path, west: str) -> Path:
        values = root / "values"
        values.mkdir()
        (values / "westeurope.yaml").write_text(west, encoding="utf-8")
        (values / "northeurope.yaml").write_text(EU_NORTH, encoding="utf-8")
        return values

    def test_backup_in_a_us_region_exits_one_and_writes_json_and_csv(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            values = self._values(root, EU_WEST.replace("storageAccountRegion: westeurope", "storageAccountRegion: eastus2"))
            tenants = root / "tenants.json"
            tenants.write_text(json.dumps([tenant("t1", "EU", "westeurope", "Acme"), tenant("t2", "US", "westeurope")]), encoding="utf-8")
            code, _, err = self._run("--tenants", str(tenants), "--values-dir", str(values), "--json", str(root / "r.json"), "--csv", str(root / "r.csv"))
            self.assertEqual(code, 1, err)
            self.assertIn("VIOLATION  t1 (EU in westeurope): outside-residency: backups holds data in eastus2", err)
            report = json.loads((root / "r.json").read_text(encoding="utf-8"))
            self.assertEqual([(v["tenantId"], v["store"], v["regions"]) for v in report["violations"] if v["tenantId"] == "t1"], [("t1", "backups", ["eastus2"])])
            self.assertEqual(len((root / "r.csv").read_text(encoding="utf-8").splitlines()), 1 + 2 * len(rr.STORES))

    def test_clean_report_exits_zero_and_prints_json(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            values = self._values(root, EU_WEST)
            tenants = root / "tenants.json"
            tenants.write_text(json.dumps({"items": [{"tenantId": "t1", "displayName": "Acme", "residency": "EU", "region": "westeurope"}]}), encoding="utf-8")
            code, out, err = self._run("--tenants", str(tenants), "--values-dir", str(values))
            self.assertEqual(code, 0, err)
            report = json.loads(out)
            self.assertEqual(report["violations"], [])
            self.assertEqual(report["rows"][0]["name"], "Acme")

    def test_bad_input_is_a_usage_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            bad = root / "t.json"
            bad.write_text('[{"name": "no id"}]', encoding="utf-8")
            self.assertEqual(self._run("--tenants", str(bad), "--values-dir", str(root))[0], 2)
            bad.write_text("not json", encoding="utf-8")
            self.assertEqual(self._run("--tenants", str(bad), "--values-dir", str(root))[0], 2)
            self.assertEqual(self._run("--tenants", str(root / "missing.json"))[0], 2)


if __name__ == "__main__":
    unittest.main()
