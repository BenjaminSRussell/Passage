"""Render tests for the Azure Key Vault ClusterSecretStore (#600). Run: python3 -m unittest discover -s deploy/secret-store

Needs helm on PATH (skipped otherwise). Also checks that the store name is the one every chart's externalSecret.storeName
defaults to, and that the ExternalSecret templates produce names Key Vault accepts.
"""

from __future__ import annotations

import re
import shutil
import subprocess
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
PROFILES = REPO / "deploy" / "cloud-profiles" / "values"
HELM = shutil.which("helm")
KV_NAME = re.compile(r"^[0-9A-Za-z-]{1,127}$")


def render(chart: str, profile: str, *sets: str, registry: str = "r.azurecr.io") -> str:
    cmd = [HELM, "template", "x", str(REPO / chart), "-f", str(PROFILES / f"{profile}.yaml"), "--set", f"global.imageRegistry={registry}"]
    for s in sets:
        cmd += ["--set", s]
    return subprocess.run(cmd, check=True, capture_output=True, text=True).stdout


STORE = ("azureKeyVault.name=kv-passage", "azureKeyVault.tenantId=00000000-0000-0000-0000-000000000002")


@unittest.skipUnless(HELM, "helm not installed")
class StoreTests(unittest.TestCase):
    def test_commercial_store_uses_workload_identity_and_the_profile_suffix(self) -> None:
        out = render("deploy/secret-store", "commercial", *STORE)
        self.assertIn("kind: ClusterSecretStore", out)
        self.assertIn("name: passage\n", out)
        self.assertIn("authType: WorkloadIdentity", out)
        self.assertIn('vaultUrl: "https://kv-passage.vault.azure.net"', out)
        self.assertIn("environmentType: PublicCloud", out)

    def test_gov_and_china_use_their_own_suffix_and_environment(self) -> None:
        gov = render("deploy/secret-store", "gcc-high", *STORE)
        self.assertIn('vaultUrl: "https://kv-passage.vault.usgovcloudapi.net"', gov)
        self.assertIn("environmentType: USGovernmentCloud", gov)
        china = render("deploy/secret-store", "china", *STORE)
        self.assertIn("vault.azure.cn", china)
        self.assertIn("environmentType: ChinaCloud", china)

    def test_gcc_is_the_public_cloud(self) -> None:
        self.assertIn("environmentType: PublicCloud", render("deploy/secret-store", "gcc", *STORE))

    def test_required_values_and_profile(self) -> None:
        proc = subprocess.run([HELM, "template", "x", str(REPO / "deploy/secret-store")], capture_output=True, text=True)
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("keyVaultSuffix is required", proc.stderr)
        proc = subprocess.run([HELM, "template", "x", str(REPO / "deploy/secret-store"), "-f", str(PROFILES / "gcc.yaml")], capture_output=True, text=True)
        self.assertIn("azureKeyVault.name is required", proc.stderr)

    def test_optional_service_account_carries_the_client_id(self) -> None:
        out = render("deploy/secret-store", "commercial", *STORE, "azureKeyVault.createServiceAccount=true", "azureKeyVault.workloadIdentityClientId=abc")
        self.assertIn("kind: ServiceAccount", out)
        self.assertIn('azure.workload.identity/client-id: "abc"', out)


def external_secret_keys(manifest: str) -> list[str]:
    docs = [d for d in re.split(r"^---\s*$", manifest, flags=re.M) if re.search(r"^kind: ExternalSecret$", d, re.M)]
    return [k for d in docs for k in re.findall(r"^\s+key: (\S+)$", d, re.M)]


class EveryChartUsesThisStoreName(unittest.TestCase):
    def test_charts_default_to_the_store_this_chart_installs(self) -> None:
        names = {}
        for values in list(REPO.glob("services/*/charts/*/values.yaml")) + [REPO / "deploy/postgres/values.yaml", REPO / "deploy/temporal-postgres/values.yaml"]:
            for m in re.finditer(r"^\s+storeName:\s*(\S+)", values.read_text(encoding="utf-8"), re.M):
                names.setdefault(m.group(1), []).append(values.relative_to(REPO).as_posix())
        self.assertEqual(sorted(names), ["passage"], names)
        self.assertGreaterEqual(len(names["passage"]), 4)


@unittest.skipUnless(HELM, "helm not installed")
class ExternalSecretsWorkWithKeyVault(unittest.TestCase):
    """Under a cloud profile every ExternalSecret reference is a legal Key Vault secret name with no JSON property."""

    CASES = [
        ("deploy/postgres", ["database=outbox", "externalSecret.environment=dev"]),
        ("deploy/temporal-postgres", ["externalSecret.environment=dev"]),
        ("services/identity/charts/identity", ["operatorTenantId=00000000-0000-0000-0000-000000000001", "keycloakDeprovision.enabled=true", "keycloakDeprovision.externalSecret.environment=dev", "keycloakDeprovision.externalSecret.realms={acme}", "keycloakDeprovision.adminUrl=https://keycloak.dev.passage.dev"]),
        ("services/batch/charts/batch", ["notify.enabled=true", "notify.smtp.enabled=true", "notify.smtp.from=noreply@dev.passage.dev", "notify.webhook.enabled=true", "notify.webhook.url=https://hooks.dev.passage.dev/x", "notify.externalSecret.environment=dev"]),
        ("services/edi/charts/edi", ["transports.externalSecret.enabled=true", "transports.secretName=edi-transports", "transports.externalSecret.key=passage/dev/edi/transports"]),
    ]

    def test_keys_are_key_vault_names_under_a_profile_and_unchanged_without_one(self) -> None:
        for chart, sets in self.CASES:
            with self.subTest(chart=chart):
                out = render(chart, "gcc", *sets)
                keys = external_secret_keys(out)
                self.assertTrue(keys, f"{chart} renders no ExternalSecret under the profile")
                for key in keys:
                    self.assertRegex(key, KV_NAME)
                self.assertNotIn("property: value", out)
                plain = subprocess.run([HELM, "template", "x", str(REPO / chart), *[a for s in sets for a in ("--set", s)]], capture_output=True, text=True)
                self.assertEqual(plain.returncode, 0, plain.stderr)
                self.assertTrue(any("/" in k for k in external_secret_keys(plain.stdout)), "default render keeps the Vault path")


if __name__ == "__main__":
    unittest.main()
