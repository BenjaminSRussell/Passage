"""Per-service workload identity wiring (#599): every service that unwraps fieldcrypt keys runs as a service account the
OpenTofu module federates, and its pods carry the AKS workload identity label. Run:

    python3 -m unittest discover -s deploy/cloud-profiles -p 'test_workload_identity.py'
"""

from __future__ import annotations

import re
import shutil
import subprocess
import sys
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import cloudprofile as cp  # noqa: E402

REPO = cp.REPO
HELM = shutil.which("helm")
CLIENT = "00000000-0000-0000-0000-0000000000f1"

# service directory -> chart directory, for every service that unwraps fieldcrypt keys.
CHARTS = {
    "compliance": "services/compliance/charts/compliance",
    "hr": "services/hr/charts/hr",
    "migration": "services/migration/charts/migration",
    "party": "services/party/charts/party",
    "payables": "services/payables/charts/payables",
    "receiving": "services/receiving/charts/receiving",
    "sales": "services/sales/charts/sales",
}


def tofu_service_accounts() -> set[str]:
    text = (REPO / "deploy/tofu/cluster/modules/aks/variables.tf").read_text(encoding="utf-8")
    block = text[text.index('variable "fieldcrypt_service_accounts"'):]
    block = block[: block.index("\nvariable ")]
    return set(re.findall(r'service_account = "([^"]+)"', block))


def fieldcrypt_services() -> set[str]:
    """Go services whose non-test source loads fieldcrypt keys (only Go unwraps Key Vault-wrapped keys; the TypeScript
    services take plain keys and need no Azure identity)."""
    found: set[str] = set()
    for svc in sorted((REPO / "services").iterdir()):
        if not svc.is_dir() or svc.name.endswith(("-ts", "-py")):
            continue
        for go in svc.rglob("*.go"):
            if go.name.endswith("_test.go"):
                continue
            if 'platform/fieldcrypt"' in go.read_text(encoding="utf-8", errors="ignore"):
                found.add(svc.name)
                break
    return found


def render(chart: str, *sets: str) -> str:
    cmd = [HELM, "template", "wi", str(REPO / chart)]
    for s in sets:
        cmd += ["--set", s]
    return subprocess.run(cmd, capture_output=True, text=True, check=True).stdout


def docs(manifest: str) -> list[str]:
    return [d for d in re.split(r"^---\s*$", manifest, flags=re.M) if re.search(r"^kind:", d, re.M)]


@unittest.skipUnless(HELM, "helm not installed")
class WorkloadIdentityTests(unittest.TestCase):
    def test_every_service_that_unwraps_fieldcrypt_keys_has_a_wired_chart(self) -> None:
        self.assertEqual(fieldcrypt_services() - set(CHARTS), set(), "a service reads fieldcrypt keys but its chart is not wired for workload identity")

    def test_the_accounts_the_charts_create_are_the_ones_tofu_federates(self) -> None:
        names = set()
        for chart in CHARTS.values():
            sa = [d for d in docs(render(chart, f"global.workloadIdentity.fieldcryptClientId={CLIENT}")) if re.search(r"^kind: ServiceAccount$", d, re.M)]
            self.assertEqual(len(sa), 1, chart)
            names.add(re.search(r"^  name: (\S+)", sa[0], re.M).group(1))
        self.assertEqual(names, tofu_service_accounts())

    def test_pods_use_the_account_and_carry_the_label_when_a_client_id_is_set(self) -> None:
        for svc, chart in CHARTS.items():
            with self.subTest(service=svc):
                pods = [d for d in docs(render(chart, f"global.workloadIdentity.fieldcryptClientId={CLIENT}")) if re.search(r"^kind: (Deployment|CronJob)$", d, re.M)]
                self.assertTrue(pods, svc)
                for d in pods:
                    self.assertIn('azure.workload.identity/use: "true"', d)
                    self.assertRegex(d, rf"serviceAccountName: passage-{re.escape(svc)}\b")
                sa = [d for d in docs(render(chart, f"global.workloadIdentity.fieldcryptClientId={CLIENT}")) if re.search(r"^kind: ServiceAccount$", d, re.M)][0]
                self.assertIn(f'azure.workload.identity/client-id: "{CLIENT}"', sa)

    def test_nothing_changes_without_a_client_id(self) -> None:
        for svc, chart in CHARTS.items():
            with self.subTest(service=svc):
                out = render(chart)
                self.assertNotIn("azure.workload.identity", out)
                self.assertNotIn("serviceAccountName", out)
                self.assertNotRegex(out, r"^kind: ServiceAccount$")


if __name__ == "__main__":
    unittest.main()
