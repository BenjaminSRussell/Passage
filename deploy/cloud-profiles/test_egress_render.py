"""Egress allowlist render test (#599): render every Helm chart under each cloud profile and fail when anything the
rendered manifests would call outside the cluster is not on the profile's egress allowlist.

"Anything" is every container image registry and every http(s) URL host: registries, SaaS (PagerDuty), LLM providers
and Azure services. Cluster-internal names (single-label hosts, .svc, .cluster.local, .internal), Passage's own domain
and reserved placeholder TLDs (.invalid, .example, .test, .local) are not external. Run:

    python3 -m unittest discover -s deploy/cloud-profiles -p 'test_egress_render.py'

Needs helm on PATH (skipped otherwise; CI installs it).
"""

from __future__ import annotations

import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import cloudprofile as cp  # noqa: E402

REPO = cp.REPO
HELM = shutil.which("helm")
PROFILES = ("commercial", "gcc", "gcc-high", "dod", "china")

# Values a chart insists on before it renders. Nothing here names an external host except where a test says so.
REQUIRED_VALUES = {
    "deploy/secret-store": ["azureKeyVault.name=kv-passage", "azureKeyVault.tenantId=00000000-0000-0000-0000-000000000002"],
    "deploy/postgres": ["database=outbox", "externalSecret.environment=dev"],
    "deploy/temporal-postgres": ["externalSecret.environment=dev"],
    "services/supplier-portal-ts/charts/supplier-portal": [
        "oidc.issuer=https://keycloak.dev.passage.dev/realms/passage",
        "oidc.redirectUri=https://supplier.dev.passage.dev/bff/callback",
        "appOrigin=https://supplier.dev.passage.dev",
    ],
    "services/customer-portal-ts/charts/customer-portal": [
        "oidc.issuer=https://keycloak.dev.passage.dev/realms/passage",
        "oidc.redirectUri=https://customer.dev.passage.dev/bff/callback",
        "appOrigin=https://customer.dev.passage.dev",
    ],
}
OWN_SUFFIXES = (".passage.dev", ".svc", ".cluster.local", ".internal", ".local", ".invalid", ".example", ".test", ".localhost")
URL = re.compile(r"https?://([A-Za-z0-9_.\-]+)")
IMAGE = re.compile(r"^\s*-?\s*(?:image|imageName):\s*[\"']?([^\s\"']+)", re.M)


def is_internal(host: str) -> bool:
    host = host.lower().rstrip(".")
    if host in ("localhost", "passage.dev") or "." not in host:
        return True
    if re.fullmatch(r"[0-9.]+", host):
        return True
    return host.endswith(OWN_SUFFIXES)


def registry_of(ref: str) -> str:
    """Registry host of an image reference; a reference with no host is Docker Hub."""
    first = ref.split("/")[0]
    return first if "/" in ref and ("." in first or ":" in first) else "docker.io"


def external_hosts(manifest: str) -> set[str]:
    """Every external host a rendered manifest names: URL hosts and image registries."""
    body = "\n".join(line for line in manifest.splitlines() if not line.lstrip().startswith("#"))
    hosts = {m.group(1).lower().rstrip(".") for m in URL.finditer(body)}
    hosts |= {registry_of(m.group(1)).lower() for m in IMAGE.finditer(body)}
    return {h for h in hosts if not is_internal(h)}


def violations(manifest: str, allowlist: list[str]) -> list[str]:
    return sorted(h for h in external_hosts(manifest) if not cp.host_allowed(h, allowlist))


def charts() -> list[Path]:
    out = []
    for chart in sorted(REPO.rglob("Chart.yaml")):
        rel = chart.relative_to(REPO).as_posix()
        if "node_modules" in rel or rel.startswith((".worktrees/", ".claude/")) or "/testdata/" in rel:
            continue
        out.append(chart.parent)
    return out


def render(chart: Path, profile: str, registry: str | None) -> str:
    cmd = [HELM, "template", "egress", str(chart), "-f", str(cp.values_path(profile))]
    if registry:
        cmd += ["--set", f"global.imageRegistry={registry}"]
    for s in REQUIRED_VALUES.get(chart.relative_to(REPO).as_posix(), []):
        cmd += ["--set", s]
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.returncode != 0:
        raise AssertionError(f"helm template {chart.relative_to(REPO)} under {profile} failed:\n{proc.stderr.strip()}")
    return proc.stdout


def render_with_values(chart: Path, values_text: str) -> str:
    """Render a chart with an ad-hoc values file (a profile plus overlays, which the committed files do not cover)."""
    with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as fh:
        fh.write(values_text)
    try:
        cmd = [HELM, "template", "egress", str(chart), "-f", fh.name]
        for s in REQUIRED_VALUES.get(chart.relative_to(REPO).as_posix(), []):
            cmd += ["--set", s]
        proc = subprocess.run(cmd, capture_output=True, text=True)
    finally:
        Path(fh.name).unlink(missing_ok=True)
    if proc.returncode != 0:
        raise AssertionError(f"helm template {chart.relative_to(REPO)} failed:\n{proc.stderr.strip()}")
    return proc.stdout


# What a Government-cloud render must never name (#602): commercial authority, *.windows.net, *.azure.com, ghcr.io.
COMMERCIAL_REFERENCE = re.compile(r"login\.microsoftonline\.com|[A-Za-z0-9_.\-]+\.windows\.net|[A-Za-z0-9_.\-]+\.azure\.com|ghcr\.io")


def commercial_references(manifest: str) -> set[str]:
    body = "\n".join(line for line in manifest.splitlines() if not line.lstrip().startswith("#"))
    return {m.group(0) for m in COMMERCIAL_REFERENCE.finditer(body)}


def mirror_for(profile: str) -> str:
    """A mirror host in the profile's own registry suffix, as an operator would configure it."""
    return "passage." + cp.load()["profiles"][profile]["endpoints"]["containerRegistrySuffix"]["value"]


class ScannerTests(unittest.TestCase):
    """The scanner itself, on synthetic manifests, so a green render test cannot mean the scanner sees nothing."""

    def test_finds_registries_and_urls(self) -> None:
        text = "image: ghcr.io/x/y:1\n- image: postgres:17\nurl: https://events.pagerduty.com/v2/enqueue\nnote: http://ledger.default.svc:8080\n"
        self.assertEqual(external_hosts(text), {"ghcr.io", "docker.io", "events.pagerduty.com"})

    def test_ignores_comments_and_internal_names(self) -> None:
        text = "# https://example.com/doc\nurl: https://docs.passage.dev/x\nurl: http://tempo.observability.svc:3200\nurl: http://warehouse\nurl: https://idp.example.invalid\n"
        self.assertEqual(external_hosts(text), set())

    def test_flags_what_the_allowlist_lacks(self) -> None:
        gcc = cp.resolve(cp.load(), "gcc")["egressAllowlist"]
        text = "image: ghcr.io/x/y\nurl: https://events.pagerduty.com/v2/enqueue\nurl: https://api.anthropic.com\nurl: https://res.openai.azure.com/x\nimage: passage.azurecr.io/a\n"
        self.assertEqual(violations(text, gcc), ["api.anthropic.com", "events.pagerduty.com", "ghcr.io"])

    def test_commercial_allows_ghcr_and_pagerduty_but_not_unknown_saas(self) -> None:
        commercial = cp.resolve(cp.load(), "commercial")["egressAllowlist"]
        self.assertEqual(violations("image: ghcr.io/x\nurl: https://events.pagerduty.com\nurl: https://hooks.slack.com/x\n", commercial), ["hooks.slack.com"])


@unittest.skipUnless(HELM, "helm not installed")
class RenderedChartsStayInsideTheAllowlist(unittest.TestCase):
    maxDiff = None

    def check_profile(self, profile: str, registry: str | None) -> None:
        resolved = cp.resolve(cp.load(), profile)
        allow = list(resolved["egressAllowlist"])
        if registry and not cp.host_allowed(registry, allow):
            allow.append(registry)
        problems: dict[str, list[str]] = {}
        for chart in charts():
            bad = violations(render(chart, profile, registry), allow)
            if bad:
                problems[chart.relative_to(REPO).as_posix()] = bad
        self.assertEqual(problems, {}, f"charts call hosts outside the {profile} egress allowlist {allow}")

    def test_commercial_defaults(self) -> None:
        self.check_profile("commercial", None)

    def test_every_profile_with_its_registry_mirror(self) -> None:
        for profile in PROFILES:
            with self.subTest(profile=profile):
                self.check_profile(profile, mirror_for(profile))

    def test_government_clouds_render_no_commercial_reference(self) -> None:
        """#602: a gcc-high or dod render fails on any commercial authority, *.windows.net, *.azure.com or ghcr.io."""
        for profile in ("gcc-high", "dod"):
            with self.subTest(profile=profile):
                problems = {}
                for chart in charts():
                    found = commercial_references(render(chart, profile, mirror_for(profile)))
                    if found:
                        problems[chart.relative_to(REPO).as_posix()] = sorted(found)
                self.assertEqual(problems, {})

    def test_the_commercial_reference_scanner_sees_what_it_should(self) -> None:
        text = "a: https://login.microsoftonline.com/x\nb: kv.vault.windows.net\nc: x.azure.com\nd: ghcr.io/o/i\n# https://ghcr.io\ne: login.microsoftonline.us\n"
        self.assertEqual(commercial_references(text), {"login.microsoftonline.com", "kv.vault.windows.net", "x.azure.com", "ghcr.io"})

    def test_overlays_stay_inside_their_allowlists(self) -> None:
        """#605 #606: every chart under a profile plus an overlay calls only hosts the combined allowlist keeps."""
        for base, overlay in (("commercial", "eu-data-boundary"), ("commercial", "hipaa"), ("gcc-high", "hipaa"), ("dod", "hipaa")):
            with self.subTest(profile=f"{base}+{overlay}"):
                registry = mirror_for(base)
                resolved = cp.resolve(cp.load(), base, [overlay])
                allow = list(resolved["egressAllowlist"])
                if not cp.host_allowed(registry, allow):
                    allow.append(registry)
                values = cp.helm_values(resolved, registry)
                problems = {}
                for chart in charts():
                    bad = violations(render_with_values(chart, values), allow)
                    if bad:
                        problems[chart.relative_to(REPO).as_posix()] = bad
                self.assertEqual(problems, {}, f"charts call hosts outside the {base}+{overlay} allowlist {allow}")

    def test_eu_overlay_removes_us_saas_and_the_unmirrored_registries(self) -> None:
        allow = cp.resolve(cp.load(), "commercial", ["eu-data-boundary"])["egressAllowlist"]
        for host in ("events.pagerduty.com", "api.anthropic.com", "api.openai.com", "ghcr.io", "docker.io"):
            self.assertFalse(cp.host_allowed(host, allow), host)
        self.assertTrue(cp.host_allowed("res.openai.azure.com", allow))
        obs = REPO / "deploy/observability/charts/observability"
        out = render_with_values(obs, cp.helm_values(cp.resolve(cp.load(), "commercial", ["eu-data-boundary"]), mirror_for("commercial")))
        self.assertNotIn("pagerduty_configs", out)

    def test_mirror_only_profiles_refuse_to_render_without_a_mirror(self) -> None:
        for profile in ("gcc", "gcc-high", "dod", "china"):
            with self.subTest(profile=profile):
                proc = subprocess.run(
                    [HELM, "template", "egress", str(REPO / "services/ledger/charts/ledger"), "-f", str(cp.values_path(profile))],
                    capture_output=True, text=True,
                )
                self.assertNotEqual(proc.returncode, 0)
                self.assertIn("global.imageRegistry is required", proc.stderr)

    def test_default_render_without_a_profile_names_ghcr_so_the_override_matters(self) -> None:
        out = subprocess.run([HELM, "template", "egress", str(REPO / "services/ledger/charts/ledger")], capture_output=True, text=True, check=True).stdout
        self.assertIn("ghcr.io", external_hosts(out))

    def test_gcc_renders_no_paging_receiver(self) -> None:
        obs = REPO / "deploy/observability/charts/observability"
        gcc = subprocess.run([HELM, "template", "o", str(obs), "-f", str(cp.values_path("gcc"))], capture_output=True, text=True, check=True).stdout
        commercial = subprocess.run([HELM, "template", "o", str(obs), "-f", str(cp.values_path("commercial"))], capture_output=True, text=True, check=True).stdout
        self.assertNotIn("pagerduty_configs", gcc)
        self.assertIn("https://events.pagerduty.com/v2/enqueue", commercial)

    def test_alerts_still_reach_an_in_boundary_receiver_without_pagerduty(self) -> None:
        """#602: where the profile has no PagerDuty, alertmanager routes to the in-boundary webhook; commercial is unchanged."""
        obs = REPO / "deploy/observability/charts/observability"
        for profile in ("gcc", "gcc-high", "dod", "china"):
            with self.subTest(profile=profile):
                out = subprocess.run([HELM, "template", "o", str(obs), "-f", str(cp.values_path(profile))], capture_output=True, text=True, check=True).stdout
                self.assertNotIn("pagerduty", out.lower().replace("pagerduty:", ""))
                self.assertIn("receiver: in-boundary", out)
                self.assertIn("webhook_configs:", out)
                self.assertIn("credentials_file: /etc/alertmanager/secrets/token", out)
                self.assertEqual(violations(out, cp.resolve(cp.load(), profile)["egressAllowlist"]), [])
        commercial = subprocess.run([HELM, "template", "o", str(obs), "-f", str(cp.values_path("commercial"))], capture_output=True, text=True, check=True).stdout
        self.assertNotIn("in-boundary", commercial)
        custom = subprocess.run(
            [HELM, "template", "o", str(obs), "-f", str(cp.values_path("gcc-high")), "--set", "alertWebhook.url=https://hooks.example.invalid/alerts"],
            capture_output=True, text=True, check=True,
        ).stdout
        self.assertIn('url: "https://hooks.example.invalid/alerts"', custom)

    def test_services_get_the_profile_in_their_environment(self) -> None:
        out = render(REPO / "services/ledger/charts/ledger", "gcc", mirror_for("gcc"))
        self.assertRegex(out, r'name: PASSAGE_CLOUD_PROFILE\s+value: "gcc"')
        overlay = subprocess.run(
            [HELM, "template", "egress", str(REPO / "services/ledger/charts/ledger"), "-f", str(cp.values_path("gcc")),
             "--set", "global.imageRegistry=r.azurecr.io", "--set", "global.cloudOverlays={hipaa}"],
            capture_output=True, text=True, check=True,
        ).stdout
        self.assertRegex(overlay, r'value: "gcc\+hipaa"')


if __name__ == "__main__":
    unittest.main()
