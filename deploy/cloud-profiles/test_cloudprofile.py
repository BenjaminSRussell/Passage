"""Tests for the cloud profile table (#599). Run: python3 -m unittest discover -s deploy/cloud-profiles"""

from __future__ import annotations

import copy
import json
import re
import subprocess
import sys
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import cloudprofile as cp  # noqa: E402

REPO = cp.REPO
AZURE_HOST = re.compile(
    r"managedhsm\.|microsoftonline\.|graph\.microsoft\.|microsoftgraph\.|usgovcloudapi\.net|chinacloudapi\.cn|vault\.azure\.|"
    r"azurecr\.|blob\.core\.windows\.net|openai\.azure\.|postgres\.database\.azure\.com|management\.azure\.com"
)


class ParityTests(unittest.TestCase):
    def setUp(self) -> None:
        self.doc = cp.load()

    def test_committed_table_passes(self) -> None:
        self.assertEqual(cp.check_parity(self.doc), [])

    def test_all_profiles_and_overlays_present(self) -> None:
        self.assertEqual(sorted(self.doc["profiles"]), ["china", "commercial", "dod", "gcc", "gcc-high"])
        self.assertEqual(sorted(self.doc["overlays"]), ["eu-data-boundary", "hipaa"])

    def test_a_profile_missing_a_key_another_has_fails(self) -> None:
        doc = copy.deepcopy(self.doc)
        del doc["profiles"]["china"]["endpoints"]["azureOpenAISuffix"]
        problems = cp.check_parity(doc)
        self.assertTrue(any("china: endpoint azureOpenAISuffix is missing" in p for p in problems), problems)

    def test_an_extra_key_in_one_profile_fails(self) -> None:
        doc = copy.deepcopy(self.doc)
        doc["profiles"]["gcc"]["endpoints"]["extraThing"] = {"value": "x", "source": "https://learn.microsoft.com/x"}
        self.assertTrue(any("extraThing" in p for p in cp.check_parity(doc)))

    def test_every_endpoint_names_a_microsoft_learn_source(self) -> None:
        doc = copy.deepcopy(self.doc)
        doc["profiles"]["dod"]["endpoints"]["graphRoot"]["source"] = "https://example.com/blog"
        self.assertTrue(any("Microsoft Learn source" in p for p in cp.check_parity(doc)))

    def test_status_is_verified_or_unverified(self) -> None:
        doc = copy.deepcopy(self.doc)
        doc["profiles"]["dod"]["endpoints"]["graphRoot"]["status"] = "probably"
        self.assertTrue(any("status" in p for p in cp.check_parity(doc)))

    def test_unverified_values_are_only_the_ones_microsoft_pages_do_not_list(self) -> None:
        unverified = {
            (name, key)
            for name, p in self.doc["profiles"].items()
            for key, ep in p["endpoints"].items()
            if ep.get("status") == "unverified"
        }
        # China's registry and OpenAI suffixes, and Managed HSM outside the commercial cloud (#599 #602 #605).
        self.assertEqual(
            unverified,
            {("china", "containerRegistrySuffix"), ("china", "azureOpenAISuffix"), ("china", "managedHsmSuffix"),
             ("gcc-high", "managedHsmSuffix"), ("dod", "managedHsmSuffix")},
        )


class ResolveTests(unittest.TestCase):
    def setUp(self) -> None:
        self.doc = cp.load()

    def test_gcc_uses_worldwide_entra_and_graph_with_us_regions_and_fips(self) -> None:
        r = cp.resolve(self.doc, "gcc")
        self.assertEqual(r["endpoints"]["entraAuthority"], "https://login.microsoftonline.com")
        self.assertEqual(r["endpoints"]["graphRoot"], "https://graph.microsoft.com")
        self.assertTrue(r["fips"])
        self.assertIn("eastus2", r["allowedRegions"])
        self.assertIn("centralus", r["allowedRegions"])
        self.assertTrue(all(not re.search(r"eu|gov|china|asia", x) for x in r["allowedRegions"]), r["allowedRegions"])

    def test_gov_clouds_use_us_authorities(self) -> None:
        self.assertEqual(cp.resolve(self.doc, "gcc-high")["endpoints"]["entraAuthority"], "https://login.microsoftonline.us")
        self.assertEqual(cp.resolve(self.doc, "dod")["endpoints"]["graphRoot"], "https://dod-graph.microsoft.us")
        self.assertEqual(cp.resolve(self.doc, "china")["entraAuthorityAliases"], ["https://login.chinacloudapi.cn"])

    def test_overlays(self) -> None:
        eu = cp.resolve(self.doc, "commercial", ["eu-data-boundary"])
        self.assertIn("westeurope", eu["allowedRegions"])
        self.assertNotIn("eastus2", eu["allowedRegions"])
        hipaa = cp.resolve(self.doc, "commercial", ["hipaa"])
        self.assertFalse(hipaa["pagerDutyAllowed"])
        self.assertNotIn("anthropic", hipaa["llmProviders"])
        self.assertGreaterEqual(hipaa["keycloak"]["eventRetentionDays"], 2190)

    def test_bad_combinations_and_names_are_errors(self) -> None:
        with self.assertRaises(ValueError):
            cp.resolve(self.doc, "gcc", ["eu-data-boundary"])
        with self.assertRaises(ValueError):
            cp.resolve(self.doc, "azure-stack")
        with self.assertRaises(ValueError):
            cp.resolve(self.doc, "commercial", ["nope"])

    def test_selector(self) -> None:
        self.assertEqual(cp.parse_selector("gcc+hipaa"), ("gcc", ["hipaa"]))
        self.assertEqual(cp.parse_selector(""), ("commercial", []))

    def test_gov_allowlists_never_name_commercial_authorities(self) -> None:
        for name in ("gcc-high", "dod"):
            allow = cp.resolve(self.doc, name)["egressAllowlist"]
            self.assertNotIn("login.microsoftonline.com", allow)
            self.assertFalse(any(h.endswith("azure.net") or h.endswith("windows.net") or h.endswith("azurecr.io") for h in allow), allow)

    def test_host_matching(self) -> None:
        self.assertTrue(cp.host_matches("kv.vault.azure.net", "*.vault.azure.net"))
        self.assertFalse(cp.host_matches("vault.azure.net", "*.vault.azure.net"))
        self.assertFalse(cp.host_matches("evil-vault.azure.net", "*.vault.azure.net"))
        self.assertTrue(cp.host_matches("bedrock-runtime.us-east-1.amazonaws.com", "bedrock-runtime.*.amazonaws.com"))
        self.assertFalse(cp.host_matches("a.b.example.com", "b.example.com"))

    def test_gcc_allowlist_has_no_third_party_saas(self) -> None:
        allow = cp.resolve(self.doc, "gcc")["egressAllowlist"]
        for host in ("events.pagerduty.com", "api.anthropic.com", "api.openai.com", "ghcr.io"):
            self.assertFalse(cp.host_allowed(host, allow), host)
        self.assertTrue(cp.host_allowed("res.openai.azure.com", allow))
        self.assertTrue(cp.host_allowed("passage.azurecr.io", allow))


class CloudEnvironmentTests(unittest.TestCase):
    """GCC High, DoD, EU Data Boundary and HIPAA settings (#602, #603, #605, #606)."""

    def setUp(self) -> None:
        self.doc = cp.load()

    def test_government_clouds_deploy_privately_with_customer_managed_keys_in_managed_hsm(self) -> None:
        for name in ("gcc-high", "dod"):
            d = cp.resolve(self.doc, name)["deployment"]
            self.assertEqual(d["azureEnvironment"], "usgovernment", name)
            self.assertTrue(d["privateCluster"] and d["customerManagedKeys"] and d["managedHsm"], name)
            self.assertEqual(d["postgresSslMode"], "verify-full", name)

    def test_only_dod_needs_isolated_nodes_and_only_in_the_us_gov_regions(self) -> None:
        self.assertFalse(cp.resolve(self.doc, "gcc-high")["deployment"]["isolatedNodes"])
        dod = cp.resolve(self.doc, "dod")["deployment"]
        self.assertTrue(dod["isolatedNodes"])
        self.assertEqual(sorted(dod["isolatedNodesRegions"]), ["usgovarizona", "usgovtexas", "usgovvirginia"])
        self.assertTrue({"usdodeast", "usdodcentral"}.isdisjoint(dod["isolatedNodesRegions"]))

    def test_gcc_high_and_dod_only_allow_azure_openai_in_azure_government(self) -> None:
        for name in ("gcc-high", "dod"):
            r = cp.resolve(self.doc, name)
            self.assertEqual(r["llmProviders"], ["azure-openai"])
            self.assertTrue(r["enforceProviders"])
            self.assertTrue(cp.host_allowed("res.openai.azure.us", r["egressAllowlist"]))
            self.assertFalse(cp.host_allowed("res.openai.azure.com", r["egressAllowlist"]))

    def test_eu_overlay_enforces_providers_requires_a_mirror_and_keeps_the_global_authority(self) -> None:
        r = cp.resolve(self.doc, "commercial", ["eu-data-boundary"])
        self.assertTrue(r["enforceProviders"] and r["requireMirror"])
        self.assertEqual(r["endpoints"]["entraAuthority"], "https://login.microsoftonline.com")
        self.assertEqual(r["llmProviders"], ["azure-openai"])
        self.assertNotIn("docker.io", r["extraAllow"])

    def test_hipaa_overlay_sets_idle_timeout_baa_list_and_policy_initiative(self) -> None:
        r = cp.resolve(self.doc, "commercial", ["hipaa"])
        self.assertEqual(r["idleTimeoutMinutes"], 15)
        self.assertTrue(r["enforceProviders"])
        self.assertEqual(r["llmProviders"], ["azure-openai"])
        self.assertIn("Azure OpenAI Service", r["baaCoveredServices"])
        self.assertRegex(r["azurePolicyInitiative"], r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
        self.assertEqual(sorted(r["phiServices"]), ["expense", "fieldservice", "hr", "payroll"])
        self.assertTrue(self.doc["overlays"]["hipaa"]["baaCoveredServices"]["source"].startswith("https://learn.microsoft.com/"))
        self.assertEqual(cp.resolve(self.doc, "commercial")["idleTimeoutMinutes"], 0)

    def test_hipaa_combines_with_the_government_clouds(self) -> None:
        r = cp.resolve(self.doc, "gcc-high", ["hipaa"])
        self.assertEqual(r["endpoints"]["entraAuthority"], "https://login.microsoftonline.us")
        self.assertEqual(r["llmProviders"], ["azure-openai"])
        self.assertGreaterEqual(r["keycloak"]["eventRetentionDays"], 2190)

    def test_tofu_vars_carry_the_deployment_switches_and_overlays(self) -> None:
        out = json.loads(cp.tofu_vars(cp.resolve(self.doc, "dod", ["hipaa"])))
        self.assertEqual(out["azure_environment"], "usgovernment")
        self.assertTrue(out["private_cluster"] and out["managed_hsm"] and out["require_isolated_nodes"] and out["hipaa"])
        self.assertEqual(out["azure_policy_initiative_id"], self.doc["overlays"]["hipaa"]["azurePolicyInitiative"]["policySetDefinitionId"])
        self.assertFalse(json.loads(cp.tofu_vars(cp.resolve(self.doc, "gcc-high")))["hipaa"])

    def test_cli_prints_tofu_vars_for_a_selector(self) -> None:
        proc = subprocess.run([sys.executable, str(HERE / "cloudprofile.py"), "tofu-vars", "gcc-high+hipaa"], capture_output=True, text=True)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertTrue(json.loads(proc.stdout)["hipaa"])

    def test_unverified_china_suffixes_say_what_was_checked(self) -> None:
        for key in ("containerRegistrySuffix", "azureOpenAISuffix"):
            ep = self.doc["profiles"]["china"]["endpoints"][key]
            self.assertEqual(ep["status"], "unverified")
            self.assertIn("Checked on Microsoft Learn", ep["note"])

    def test_parity_rejects_a_bad_deployment_block(self) -> None:
        doc = copy.deepcopy(self.doc)
        doc["profiles"]["dod"]["deployment"]["azureEnvironment"] = "mars"
        doc["profiles"]["gcc"]["deployment"]["managedHsm"] = True
        problems = cp.check_parity(doc)
        self.assertTrue(any("dod: deployment.azureEnvironment" in p for p in problems), problems)
        self.assertTrue(any("gcc: deployment.managedHsm needs" in p for p in problems), problems)


class GeneratedCopiesTests(unittest.TestCase):
    def test_generated_copies_are_current(self) -> None:
        self.assertEqual(
            [str(p.relative_to(REPO)) for p in cp.sync(check_only=True)],
            [],
            "run: python3 deploy/cloud-profiles/cloudprofile.py sync",
        )

    def test_values_files_carry_the_global_contract(self) -> None:
        for name in cp.load()["profiles"]:
            text = cp.values_path(name).read_text(encoding="utf-8")
            for key in ("cloudProfile", "cloudOverlays", "fips", "imageRegistry", "imageRegistryRequired", "allowedRegions", "endpoints", "egressAllowlist", "pagerDutyAllowed", "secretStoreProvider"):
                self.assertIn(f"  {key}:", text, f"{name}: global.{key}")

    def test_cli_values_and_errors(self) -> None:
        run = lambda *a: subprocess.run([sys.executable, str(HERE / "cloudprofile.py"), *a], capture_output=True, text=True)
        ok = run("values", "gcc+hipaa", "--registry", "passage.azurecr.io")
        self.assertEqual(ok.returncode, 0, ok.stderr)
        self.assertIn('cloudOverlays: ["hipaa"]', ok.stdout)
        self.assertIn('imageRegistry: "passage.azurecr.io"', ok.stdout)
        bad = run("values", "gcc+eu-data-boundary")
        self.assertEqual(bad.returncode, 2)
        self.assertEqual(run("check").returncode, 0)


class NoHardCodedAzureHostsTests(unittest.TestCase):
    """No Azure hostname outside the profile table, its generated copies, rendered examples, docs and tests."""

    EXEMPT_DIRS = (
        "deploy/cloud-profiles/", "deploy/keycloak/idp/", "platform/go/cloudprofile/", "services/agents-ts/src/cloudprofile/",
        "docs/", "roadmap/", ".claude/", ".github/ISSUE_TEMPLATE/",
    )

    def test_no_hostname_in_code_charts_or_config(self) -> None:
        files = subprocess.run(["git", "ls-files", "-co", "--exclude-standard"], cwd=REPO, capture_output=True, text=True, check=True).stdout.split("\n")
        offenders = []
        for rel in filter(None, files):
            if rel.startswith(self.EXEMPT_DIRS) or "node_modules" in rel or rel.endswith((".md", ".lock", ".sum", ".png", ".svg", ".ico", ".woff2")):
                continue
            name = Path(rel).name
            if name.startswith("test_") or name.endswith(("_test.go", ".test.ts", ".test.tsx", ".spec.ts", ".tftest.hcl")) or "/testdata/" in rel or "/test/" in rel:
                continue
            path = REPO / rel
            if not path.is_file():
                continue
            try:
                text = path.read_text(encoding="utf-8")
            except (UnicodeDecodeError, OSError):
                continue
            hit = AZURE_HOST.search(text)
            if hit:
                offenders.append(f"{rel}: {hit.group(0)}")
        self.assertEqual(offenders, [], "read Azure hostnames from the cloud profile (deploy/cloud-profiles/profiles.json)")


if __name__ == "__main__":
    unittest.main()
