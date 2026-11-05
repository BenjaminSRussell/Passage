import re
import subprocess
import unittest
from pathlib import Path

import yaml

DEPLOY = Path(__file__).resolve().parents[1]
TENANT_GO = DEPLOY.parent / "platform" / "go" / "tenant" / "tenant.go"
EDI = DEPLOY.parent / "services" / "edi"
DOWNSTREAM_ROUTE_RE = re.compile(r"^/v1/[a-z-]+$")

# The API gateway (every route behind the JWT SecurityPolicy) and the AS2 edge for trading partners (#483), which
# takes no JWT and carries exactly one route to the edi AS2 listener.
API_GATEWAY = "passage-api"
AS2_GATEWAY = "passage-as2"
AS2_ROUTE = "edi-as2"
AS2_SERVICE = "edi-as2"


def render():
    out = subprocess.run(
        ["kubectl", "kustomize", str(DEPLOY / "gateway")], check=True, capture_output=True, text=True
    ).stdout
    return [d for d in yaml.safe_load_all(out) if d]


def of_kind(docs, kind):
    return [d for d in docs if d["kind"] == kind]


def named(docs, kind, name):
    (doc,) = [d for d in of_kind(docs, kind) if d["metadata"]["name"] == name]
    return doc


def target_names(policy):
    return {(t["kind"], t["name"]) for t in policy["spec"]["targetRefs"]}


def parent_gateways(route):
    return {p["name"] for p in route["spec"]["parentRefs"]}


def api_routes(docs):
    return [r for r in of_kind(docs, "HTTPRoute") if API_GATEWAY in parent_gateways(r)]


def go_byte_const(path, name):
    """Value of a Go constant written as `name = A << B` or `name = N`."""
    m = re.search(rf"^const {name} = (\d+)(?: << (\d+))?$", path.read_text(), re.M)
    assert m, f"{path}: no const {name}"
    return int(m.group(1)) << int(m.group(2) or 0)


def quantity_bytes(q):
    m = re.fullmatch(r"(\d+)(Ki|Mi|Gi)?", str(q))
    assert m, q
    return int(m.group(1)) * {None: 1, "Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30}[m.group(2)]


CLIENT_SUPPLIED = {"X-Passage-LegalEntity-Scope"}


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
        ctp = named(self.docs, "ClientTrafficPolicy", "passage-api-strip-identity-headers")
        removed = set(ctp["spec"]["headers"]["earlyRequestHeaders"]["remove"])
        self.assertTrue(header_consts - CLIENT_SUPPLIED <= removed, header_consts - CLIENT_SUPPLIED - removed)
        self.assertFalse(CLIENT_SUPPLIED & removed)
        self.assertLessEqual(CLIENT_SUPPLIED, header_consts)
        explicit = {
            "X-Passage-Tenant",
            "X-Passage-Legal-Entity",
            "X-Passage-LegalEntity",
            "X-Passage-User",
            "X-Passage-Duties",
            "X-Passage-Tier",
            "X-Passage-Service-Token",
        }
        self.assertTrue(explicit <= removed, explicit - removed)
        self.assertEqual(target_names(ctp), {("Gateway", API_GATEWAY)})

    def test_removal_precedes_claim_mapping_and_covers_mapped_headers(self):
        ctp = named(self.docs, "ClientTrafficPolicy", "passage-api-strip-identity-headers")
        (sp,) = of_kind(self.docs, "SecurityPolicy")
        self.assertNotIn("requestHeaderModifier", str(of_kind(self.docs, "HTTPRoute")))
        removed = set(ctp["spec"]["headers"]["earlyRequestHeaders"]["remove"])
        for provider in sp["spec"]["jwt"]["providers"]:
            mapped = {m["header"] for m in provider["claimToHeaders"]}
            self.assertTrue(mapped <= removed, mapped - removed)
            self.assertIn("X-Passage-Tenant", mapped)

    def test_security_policy_targets_gateway_so_every_route_is_covered(self):
        self.assertTrue(api_routes(self.docs))
        (sp,) = of_kind(self.docs, "SecurityPolicy")
        self.assertEqual(target_names(sp), {("Gateway", API_GATEWAY)})
        # The whole Gateway, not one listener: a listener added later is covered too.
        self.assertFalse([t for t in sp["spec"]["targetRefs"] if "sectionName" in t])

    def test_gateway_has_tls_listener_and_http_is_dev_only(self):
        gw = named(self.docs, "Gateway", API_GATEWAY)
        listeners = {l["name"]: l for l in gw["spec"]["listeners"]}
        self.assertEqual(set(listeners), {"http-dev", "https"})
        self.assertEqual(listeners["https"]["protocol"], "HTTPS")
        refs = listeners["https"]["tls"]["certificateRefs"]
        self.assertEqual([r["kind"] for r in refs], ["Secret"])

    def test_envoy_gateway_version_requirement_recorded(self):
        app = yaml.safe_load((DEPLOY / "envs/dev/apps/gateway.yaml").read_text())
        self.assertIn(">=v1.4", app["metadata"]["annotations"]["passage.dev/requires"])
        self.assertIn("v1.4", (DEPLOY / "gateway/README.md").read_text())

    def test_duties_claim_mapped_as_single_string_claim(self):
        (sp,) = of_kind(self.docs, "SecurityPolicy")
        for provider in sp["spec"]["jwt"]["providers"]:
            self.assertIn(("duties", "X-Passage-Duties"), {(m["claim"], m["header"]) for m in provider["claimToHeaders"]})

    def test_nil_tenant_denied(self):
        (sp,) = of_kind(self.docs, "SecurityPolicy")
        denied = [r for r in sp["spec"]["authorization"]["rules"] if r["action"] == "Deny"]
        values = {v for r in denied for c in r["principal"]["jwt"]["claims"] if c["name"] == "tid" for v in c["values"]}
        self.assertIn("00000000-0000-0000-0000-000000000000", values)

    def test_rate_limit_keyed_on_tenant_and_covers_every_route(self):
        routes = {("HTTPRoute", r["metadata"]["name"]) for r in api_routes(self.docs)}
        btp = named(self.docs, "BackendTrafficPolicy", "passage-api-tenant-rate-limit")
        self.assertEqual(target_names(btp), routes)
        rules = btp["spec"]["rateLimit"]["global"]["rules"]
        for rule in rules:
            self.assertIs(rule.get("shared"), True)
            headers = rule["clientSelectors"][0]["headers"]
            self.assertIn(("X-Passage-Tenant", "Distinct"), {(h["name"], h.get("type")) for h in headers})
        limits = {
            next(h["value"] for h in r["clientSelectors"][0]["headers"] if h["name"] == "X-Passage-Tier"): r["limit"]
            for r in rules
            if any(h["name"] == "X-Passage-Tier" and not h.get("invert") for h in r["clientSelectors"][0]["headers"])
        }
        self.assertEqual(
            {k: (v["requests"], v["unit"]) for k, v in limits.items()},
            {"starter": (600, "Minute"), "professional": (3000, "Minute"), "enterprise": (12000, "Minute")},
        )
        (fallback,) = [
            r
            for r in rules
            if any(h.get("invert") for h in r["clientSelectors"][0]["headers"])
        ]
        self.assertEqual((fallback["limit"]["requests"], fallback["limit"]["unit"]), (600, "Minute"))
        inverted = next(h for h in fallback["clientSelectors"][0]["headers"] if h.get("invert"))
        self.assertEqual(inverted["name"], "X-Passage-Tier")
        self.assertEqual(inverted["value"], "^(starter|professional|enterprise)$")

    def test_routes_strip_api_prefix_to_service_convention(self):
        for route in api_routes(self.docs):
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


class AS2Edge(unittest.TestCase):
    """The AS2 edge is outside the JWT policy, and only POST /as2/v1/ to the edi AS2 Service is exposed there."""

    @classmethod
    def setUpClass(cls):
        cls.docs = render()
        cls.gateway = named(cls.docs, "Gateway", AS2_GATEWAY)
        (cls.listener,) = cls.gateway["spec"]["listeners"]
        cls.route = named(cls.docs, "HTTPRoute", AS2_ROUTE)

    def test_only_two_gateways_and_every_route_on_exactly_one(self):
        self.assertEqual({g["metadata"]["name"] for g in of_kind(self.docs, "Gateway")}, {API_GATEWAY, AS2_GATEWAY})
        for route in of_kind(self.docs, "HTTPRoute"):
            name = route["metadata"]["name"]
            gateways = parent_gateways(route)
            self.assertEqual(len(route["spec"]["parentRefs"]), 1, name)
            # Only edi-as2 attaches to the AS2 edge, and it attaches nowhere else.
            self.assertEqual(gateways, {AS2_GATEWAY} if name == AS2_ROUTE else {API_GATEWAY}, name)

    def test_api_routes_still_require_a_jwt_and_the_as2_edge_never_does(self):
        (sp,) = of_kind(self.docs, "SecurityPolicy")
        self.assertTrue(sp["spec"]["jwt"]["providers"])
        # Every API route is covered: the JWT policy targets the whole passage-api Gateway.
        self.assertEqual(target_names(sp), {("Gateway", API_GATEWAY)})
        self.assertTrue(all(parent_gateways(r) == {API_GATEWAY} for r in api_routes(self.docs)))
        # No SecurityPolicy reaches the AS2 edge, by Gateway or by route; partners authenticate by AS2 signature.
        for policy in of_kind(self.docs, "SecurityPolicy"):
            self.assertFalse(target_names(policy) & {("Gateway", AS2_GATEWAY), ("HTTPRoute", AS2_ROUTE)})
        # Nothing on the API gateway reaches the AS2 listener, and nothing on the AS2 edge reaches anything else.
        for route in api_routes(self.docs):
            self.assertNotIn(AS2_SERVICE, {b["name"] for rule in route["spec"]["rules"] for b in rule["backendRefs"]})

    def test_as2_route_exposes_only_post_as2_v1_to_the_edi_as2_service(self):
        values = yaml.safe_load((EDI / "charts/edi/values.yaml").read_text())
        spec = self.route["spec"]
        self.assertEqual(spec["parentRefs"], [{"name": AS2_GATEWAY, "sectionName": self.listener["name"]}])
        self.assertEqual(spec["hostnames"], [self.listener["hostname"]])
        (rule,) = spec["rules"]
        self.assertEqual(rule["matches"], [{"path": {"type": "PathPrefix", "value": "/as2/v1/"}, "method": "POST"}])
        self.assertNotIn("filters", rule)
        self.assertEqual(rule["backendRefs"], [{"name": AS2_SERVICE, "port": values["transports"]["as2"]["port"]}])
        # The listener serves exactly these paths.
        as2in = (EDI / "internal/api/as2in.go").read_text()
        self.assertEqual(set(re.findall(r'\.Post\("(/as2/v1/[^"]+)"', as2in)), {"/as2/v1/{endpoint}", "/as2/v1/{endpoint}/mdn"})

    def test_as2_gateway_is_https_only_on_its_own_hostname_with_a_certificate(self):
        self.assertEqual(self.gateway["spec"]["gatewayClassName"], named(self.docs, "Gateway", API_GATEWAY)["spec"]["gatewayClassName"])
        listener = self.listener
        self.assertEqual((listener["protocol"], listener["port"], listener["tls"]["mode"]), ("HTTPS", 443, "Terminate"))
        # as2.<env>.passage.dev, in the environment of the IdP the API gateway trusts.
        (sp,) = of_kind(self.docs, "SecurityPolicy")
        issuer_env = re.fullmatch(r"https://idp\.([a-z]+)\.passage\.dev/realms/\w+", sp["spec"]["jwt"]["providers"][0]["issuer"]).group(1)
        self.assertEqual(listener["hostname"], f"as2.{issuer_env}.passage.dev")
        self.assertEqual(listener["allowedRoutes"]["kinds"], [{"kind": "HTTPRoute"}])
        (ref,) = listener["tls"]["certificateRefs"]
        cert = named(self.docs, "Certificate", ref["name"])
        self.assertEqual(cert["apiVersion"], "cert-manager.io/v1")
        self.assertEqual(cert["spec"]["secretName"], ref["name"])
        self.assertEqual(cert["spec"]["dnsNames"], [listener["hostname"]])
        self.assertEqual(cert["spec"]["issuerRef"]["kind"], "ClusterIssuer")
        issuers = {d["metadata"]["name"] for d in yaml.safe_load_all((DEPLOY / "envs/dev/apps/cert-issuer.yaml").read_text())
                   if d and d["kind"] == "ClusterIssuer"}
        self.assertIn(cert["spec"]["issuerRef"]["name"], issuers)
        # The service and the OpenAPI document name the same URL.
        dev_values = yaml.safe_load((DEPLOY / "envs/dev/values/edi.yaml").read_text())
        self.assertEqual(dev_values["transports"]["as2"]["publicUrl"], f"https://{listener['hostname']}")
        self.assertTrue(dev_values["transports"]["as2"]["enabled"])
        self.assertIn("https://as2.{env}.passage.dev", (EDI / "api/openapi.yaml").read_text())

    def test_as2_edge_body_limit_matches_the_service_and_is_rate_limited_per_client(self):
        btp = named(self.docs, "BackendTrafficPolicy", "passage-as2-limits")
        self.assertEqual(target_names(btp), {("HTTPRoute", AS2_ROUTE)})
        limit = quantity_bytes(btp["spec"]["requestBuffer"]["limit"])
        self.assertEqual(limit, go_byte_const(EDI / "internal/api/as2in.go", "maxAS2Body"))
        rate = btp["spec"]["rateLimit"]
        self.assertEqual(rate["type"], "Global")
        cidrs = set()
        for rule in rate["global"]["rules"]:
            (selector,) = rule["clientSelectors"]
            self.assertEqual(set(selector), {"sourceCIDR"})
            self.assertEqual(selector["sourceCIDR"]["type"], "Distinct")
            cidrs.add(selector["sourceCIDR"]["value"])
            self.assertEqual(rule["limit"]["unit"], "Minute")
            self.assertGreater(rule["limit"]["requests"], 0)
        self.assertEqual(cidrs, {"0.0.0.0/0", "::/0"})
        # No other traffic policy targets the AS2 edge (the tenant limit keys on JWT headers it never has).
        for other in of_kind(self.docs, "BackendTrafficPolicy"):
            if other is not btp:
                self.assertFalse(target_names(other) & {("Gateway", AS2_GATEWAY), ("HTTPRoute", AS2_ROUTE)})

    def test_as2_edge_strips_identity_headers_and_requires_tls_1_2(self):
        header_consts = set(re.findall(r'Header\w+\s*=\s*"(X-Passage-[A-Za-z-]+)"', TENANT_GO.read_text()))
        ctp = named(self.docs, "ClientTrafficPolicy", "passage-as2-client")
        self.assertEqual(target_names(ctp), {("Gateway", AS2_GATEWAY)})
        removed = set(ctp["spec"]["headers"]["earlyRequestHeaders"]["remove"])
        self.assertTrue(header_consts <= removed, header_consts - removed)
        self.assertIn(ctp["spec"]["tls"]["minVersion"], {"1.2", "1.3"})

    def test_edi_network_policy_admits_only_the_as2_gateway_to_the_as2_port(self):
        values = yaml.safe_load((EDI / "charts/edi/values.yaml").read_text())
        np = values["networkPolicy"]
        self.assertTrue(np["enabled"])
        self.assertEqual(np["as2Gateway"]["podLabels"], {
            "gateway.envoyproxy.io/owning-gateway-name": AS2_GATEWAY,
            "gateway.envoyproxy.io/owning-gateway-namespace": self.gateway["metadata"]["namespace"],
        })
        self.assertEqual(np["as2Gateway"]["namespaces"], ["envoy-gateway-system"])


if __name__ == "__main__":
    unittest.main()
