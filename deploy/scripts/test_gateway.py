import re
import subprocess
import unittest
from pathlib import Path

import yaml

DEPLOY = Path(__file__).resolve().parents[1]
TENANT_GO = DEPLOY.parent / "platform" / "go" / "tenant" / "tenant.go"
DOWNSTREAM_ROUTE_RE = re.compile(r"^/v1/[a-z-]+$")


def render():
    out = subprocess.run(
        ["kubectl", "kustomize", str(DEPLOY / "gateway")], check=True, capture_output=True, text=True
    ).stdout
    return [d for d in yaml.safe_load_all(out) if d]


def of_kind(docs, kind):
    return [d for d in docs if d["kind"] == kind]


def target_names(policy):
    return {(t["kind"], t["name"]) for t in policy["spec"]["targetRefs"]}


class GatewayManifests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.docs = render()

    def test_every_namespaced_resource_has_namespace(self):
        for d in self.docs:
            if d["kind"] != "GatewayClass":
                self.assertEqual(d["metadata"].get("namespace"), "hello-ledger", d["metadata"]["name"])

    def test_identity_headers_removed_early_for_every_tenant_header(self):
        header_consts = set(re.findall(r'Header\w+\s*=\s*"(X-Passage-[A-Za-z-]+)"', TENANT_GO.read_text()))
        self.assertTrue(header_consts)
        (ctp,) = of_kind(self.docs, "ClientTrafficPolicy")
        removed = set(ctp["spec"]["headers"]["earlyRequestHeaders"]["remove"])
        self.assertTrue(header_consts <= removed, header_consts - removed)
        gateway = of_kind(self.docs, "Gateway")[0]["metadata"]["name"]
        self.assertEqual(target_names(ctp), {("Gateway", gateway)})

    def test_removal_precedes_claim_mapping_and_covers_mapped_headers(self):
        (ctp,) = of_kind(self.docs, "ClientTrafficPolicy")
        (sp,) = of_kind(self.docs, "SecurityPolicy")
        self.assertNotIn("requestHeaderModifier", str(of_kind(self.docs, "HTTPRoute")))
        removed = set(ctp["spec"]["headers"]["earlyRequestHeaders"]["remove"])
        for provider in sp["spec"]["jwt"]["providers"]:
            mapped = {m["header"] for m in provider["claimToHeaders"]}
            self.assertTrue(mapped <= removed, mapped - removed)
            self.assertIn("X-Passage-Tenant", mapped)

    def test_every_route_has_security_policy(self):
        routes = {("HTTPRoute", r["metadata"]["name"]) for r in of_kind(self.docs, "HTTPRoute")}
        self.assertTrue(routes)
        (sp,) = of_kind(self.docs, "SecurityPolicy")
        self.assertEqual(target_names(sp), routes)

    def test_nil_tenant_denied(self):
        (sp,) = of_kind(self.docs, "SecurityPolicy")
        denied = [r for r in sp["spec"]["authorization"]["rules"] if r["action"] == "Deny"]
        values = {v for r in denied for c in r["principal"]["jwt"]["claims"] if c["name"] == "tid" for v in c["values"]}
        self.assertIn("00000000-0000-0000-0000-000000000000", values)

    def test_rate_limit_keyed_on_tenant_and_covers_every_route(self):
        routes = {("HTTPRoute", r["metadata"]["name"]) for r in of_kind(self.docs, "HTTPRoute")}
        (btp,) = of_kind(self.docs, "BackendTrafficPolicy")
        self.assertEqual(target_names(btp), routes)
        rules = btp["spec"]["rateLimit"]["global"]["rules"]
        for rule in rules:
            headers = rule["clientSelectors"][0]["headers"]
            self.assertIn(("X-Passage-Tenant", "Distinct"), {(h["name"], h.get("type")) for h in headers})
        limits = {
            next(h["value"] for h in r["clientSelectors"][0]["headers"] if h["name"] == "X-Passage-Tier"): r["limit"]
            for r in rules
            if any(h["name"] == "X-Passage-Tier" for h in r["clientSelectors"][0]["headers"])
        }
        self.assertEqual(
            {k: (v["requests"], v["unit"]) for k, v in limits.items()},
            {"starter": (600, "Minute"), "professional": (3000, "Minute"), "enterprise": (12000, "Minute")},
        )
        self.assertTrue(any(len(r["clientSelectors"][0]["headers"]) == 1 for r in rules))

    def test_routes_strip_api_prefix_to_service_convention(self):
        for route in of_kind(self.docs, "HTTPRoute"):
            name = route["metadata"]["name"]
            by_prefix = {r["matches"][0]["path"]["value"]: r for r in route["spec"]["rules"]}
            self.assertEqual(set(by_prefix), {f"/api/v1/{name}", f"/v1/{name}"})
            rewrite = by_prefix[f"/api/v1/{name}"]["filters"][0]["urlRewrite"]["path"]
            self.assertEqual(rewrite["replacePrefixMatch"], f"/v1/{name}")
            self.assertRegex(rewrite["replacePrefixMatch"], DOWNSTREAM_ROUTE_RE)
            self.assertNotIn("filters", by_prefix[f"/v1/{name}"])
            for rule in route["spec"]["rules"]:
                self.assertEqual(rule["backendRefs"][0]["name"], name)

    def test_jwks_configmap_referenced_by_provider_exists(self):
        (sp,) = of_kind(self.docs, "SecurityPolicy")
        cm_names = {c["metadata"]["name"] for c in of_kind(self.docs, "ConfigMap")}
        for provider in sp["spec"]["jwt"]["providers"]:
            self.assertIn(provider["localJWKS"]["valueRef"]["name"], cm_names)

    def test_argocd_app_points_at_gateway_dir_and_flags_crd_dependency(self):
        app = yaml.safe_load((DEPLOY / "envs/dev/apps/gateway.yaml").read_text())
        self.assertEqual(app["spec"]["source"]["path"], "deploy/gateway")
        self.assertIn("#213", app["metadata"]["annotations"]["passage.dev/requires"])
        self.assertIn("SkipDryRunOnMissingResource=true", app["spec"]["syncPolicy"]["syncOptions"])


if __name__ == "__main__":
    unittest.main()
