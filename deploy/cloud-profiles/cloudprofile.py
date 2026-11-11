#!/usr/bin/env python3
"""Passage cloud profile (#599): load, resolve and render the one profile table.

`profiles.json` is the single place an Azure hostname, a FIPS requirement or an allowed region is written down.
Everything else is generated from it or reads it:

  Helm        deploy/cloud-profiles/values/<profile>.yaml  (`global.cloudProfile`, `global.endpoints`, ...)
  Tofu        deploy/cloud-profiles/tofu/<profile>.tfvars.json  (`-var-file` for deploy/tofu/cluster)
  Go          platform/go/cloudprofile/profiles.json       (embedded; same bytes)
  TypeScript  services/agents-ts/src/cloudprofile/profiles.generated.ts
  Python      this module, used by deploy/multi-region/tools/check_region_pair.py and the render tests

    python3 deploy/cloud-profiles/cloudprofile.py sync           # rewrite the generated copies
    python3 deploy/cloud-profiles/cloudprofile.py sync --check   # exit 1 when a copy has drifted
    python3 deploy/cloud-profiles/cloudprofile.py check          # key parity and data rules
    python3 deploy/cloud-profiles/cloudprofile.py values gcc [--overlay hipaa] [--registry passage.azurecr.io]
    python3 deploy/cloud-profiles/cloudprofile.py tofu-vars gcc-high+hipaa > gcc-high-hipaa.tfvars.json

A profile selector is a base profile plus optional overlays written `gcc+hipaa` (env var PASSAGE_CLOUD_PROFILE)
or `cloudProfile: gcc` with `cloudOverlays: [hipaa]` (Helm). One deployment is one profile: tokens from one cloud
are not accepted in another, so there is no runtime fallback list. Stdlib only, Python 3.11+.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
PROFILES_JSON = HERE / "profiles.json"
VALUES_DIR = HERE / "values"
TOFU_DIR = HERE / "tofu"
GO_COPY = REPO / "platform" / "go" / "cloudprofile" / "profiles.json"
TS_COPY = REPO / "services" / "agents-ts" / "src" / "cloudprofile" / "profiles.generated.ts"

ENV_PROFILE = "PASSAGE_CLOUD_PROFILE"
STATUSES = ("verified", "unverified")
DEPLOYMENT_KEYS = (
    "azureEnvironment", "privateCluster", "customerManagedKeys", "managedHsm", "isolatedNodes", "isolatedNodesRegions",
    "postgresSslMode", "source",
)
AZURE_ENVIRONMENTS = ("public", "usgovernment", "china")
LLM_HOSTS = {
    "anthropic": "api.anthropic.com",
    "bedrock": "bedrock-runtime.*.amazonaws.com",
    "openai": "api.openai.com",
}
PAGERDUTY_HOST = "events.pagerduty.com"
GHCR_HOST = "ghcr.io"
GENERATED_NOTE = "Generated from deploy/cloud-profiles/profiles.json by deploy/cloud-profiles/cloudprofile.py; do not edit."


def load(path: Path = PROFILES_JSON) -> dict[str, Any]:
    return json.loads(path.read_text(encoding="utf-8"))


def parse_selector(selector: str) -> tuple[str, list[str]]:
    """`gcc+hipaa` -> ("gcc", ["hipaa"]). An empty selector is the commercial profile."""
    parts = [p.strip() for p in (selector or "commercial").split("+")]
    return parts[0], [p for p in parts[1:] if p]


def host_of(value: str) -> str:
    """Host of a URL or bare host."""
    return (urlparse(value).hostname or value) if "://" in value else value


def host_matches(host: str, pattern: str) -> bool:
    """Exact host, `*.suffix` (one or more labels) or a `*` label inside a host (`bedrock-runtime.*.amazonaws.com`)."""
    host, pattern = host.lower(), pattern.lower()
    if pattern == host:
        return True
    labels, plabels = host.split("."), pattern.split(".")
    if plabels[0] == "*":
        return len(labels) > len(plabels) - 1 and labels[-(len(plabels) - 1):] == plabels[1:]
    return len(labels) == len(plabels) and all(p in ("*", h) for p, h in zip(plabels, labels))


def host_allowed(host: str, allowlist: list[str]) -> bool:
    return any(host_matches(host, p) for p in allowlist)


def check_parity(doc: dict[str, Any]) -> list[str]:
    """Problems with the profile table: a key one profile has and another lacks, a missing source, a bad status."""
    problems: list[str] = []
    keys = doc["endpointKeys"]
    names = sorted(doc["profiles"])
    for name in names:
        profile = doc["profiles"][name]
        have = set(profile.get("endpoints", {}))
        for key in sorted(set(keys) - have):
            problems.append(f"{name}: endpoint {key} is missing")
        for key in sorted(have - set(keys)):
            problems.append(f"{name}: endpoint {key} is not in endpointKeys (another profile lacks it)")
        for key, ep in sorted(profile.get("endpoints", {}).items()):
            if not ep.get("value"):
                problems.append(f"{name}: endpoint {key} has no value")
            if not str(ep.get("source", "")).startswith("https://learn.microsoft.com/"):
                problems.append(f"{name}: endpoint {key} needs a Microsoft Learn source URL, got {ep.get('source')!r}")
            if ep.get("status", "verified") not in STATUSES:
                problems.append(f"{name}: endpoint {key} status {ep.get('status')!r} is not one of {STATUSES}")
        for field in ("cloud", "fips", "allowedRegions", "registry", "egress", "keycloak", "description", "deployment"):
            if field not in profile:
                problems.append(f"{name}: {field} is missing")
        dep = profile.get("deployment", {})
        if sorted(dep) != sorted(DEPLOYMENT_KEYS):
            problems.append(f"{name}: deployment keys are {sorted(dep)}, expected {sorted(DEPLOYMENT_KEYS)}")
        if dep.get("azureEnvironment") not in AZURE_ENVIRONMENTS:
            problems.append(f"{name}: deployment.azureEnvironment {dep.get('azureEnvironment')!r} is not one of {AZURE_ENVIRONMENTS}")
        if dep.get("managedHsm") and not dep.get("customerManagedKeys"):
            problems.append(f"{name}: deployment.managedHsm needs customerManagedKeys")
        if dep.get("isolatedNodesRegions") and not set(dep["isolatedNodesRegions"]) <= set(profile.get("allowedRegions", [])):
            problems.append(f"{name}: deployment.isolatedNodesRegions must be allowedRegions")
    shapes = {n: sorted(doc["profiles"][n]) for n in names}
    if len({json.dumps(s) for s in shapes.values()}) > 1:
        problems.append("profiles do not have the same top-level keys")
    return problems


def resolve(doc: dict[str, Any], base: str, overlays: list[str] | None = None) -> dict[str, Any]:
    """The effective settings of a base profile with overlays applied. Raises ValueError for a bad combination."""
    if base not in doc["profiles"]:
        raise ValueError(f"unknown cloud profile {base!r}; known: {', '.join(sorted(doc['profiles']))}")
    overlays = list(overlays or [])
    p = doc["profiles"][base]
    regions: list[str] = list(p["allowedRegions"])
    fips = bool(p["fips"])
    llm = list(p["egress"]["llmProviders"])
    pager = bool(p["egress"]["pagerDuty"])
    retention = int(p["keycloak"]["eventRetentionDays"])
    enforce = bool(p["egress"]["enforceProviders"])
    require_mirror = bool(p["registry"]["requireMirror"])
    extra = list(p["egress"]["extraAllow"])
    idle = 0
    baa: dict[str, Any] | None = None
    policy: dict[str, Any] | None = None
    phi_services: list[str] = []
    removed: list[str] = []
    for name in overlays:
        if name not in doc["overlays"]:
            raise ValueError(f"unknown overlay {name!r}; known: {', '.join(sorted(doc['overlays']))}")
        o = doc["overlays"][name]
        if o["allowedRegions"]:
            regions = [r for r in regions if r in o["allowedRegions"]] if regions else list(o["allowedRegions"])
            if not regions:
                raise ValueError(f"overlay {name} cannot be combined with {base}: no region is allowed by both")
        fips = fips or bool(o.get("requireFips"))
        retention = max(retention, int(o.get("eventRetentionDays", 0)))
        enforce = enforce or bool(o.get("enforceProviders"))
        require_mirror = require_mirror or bool(o.get("requireMirror"))
        idle = max(idle, int(o.get("idleTimeoutMinutes", 0)))
        baa = o.get("baaCoveredServices", baa)
        policy = o.get("azurePolicyInitiative", policy)
        phi_services = o.get("phiServices", phi_services)
        for item in o.get("egressRemove", []):
            removed.append(item)
            if item == "pagerDuty":
                pager = False
            if item in llm:
                llm.remove(item)
            if item in extra:
                extra.remove(item)
    eps = {k: v["value"] for k, v in p["endpoints"].items()}
    out: dict[str, Any] = {
        "name": base,
        "overlays": overlays,
        "cloud": p["cloud"],
        "fips": fips,
        "allowedRegions": regions,
        "endpoints": eps,
        "entraAuthorityAliases": p["endpoints"]["entraAuthority"].get("aliases", []),
        "requireMirror": require_mirror,
        "registryDefault": p["registry"]["default"],
        "llmProviders": llm,
        "enforceProviders": enforce,
        "pagerDutyAllowed": pager,
        "extraAllow": extra,
        "removed": removed,
        "idleTimeoutMinutes": idle,
        "deployment": {k: v for k, v in p["deployment"].items() if k != "source"},
        "baaCoveredServices": list(baa["services"]) if baa else [],
        "azurePolicyInitiative": policy["policySetDefinitionId"] if policy else "",
        "phiServices": list(phi_services),
        "keycloak": {**p["keycloak"], "eventRetentionDays": retention},
    }
    out["egressAllowlist"] = egress_allowlist(out)
    return out


def egress_allowlist(r: dict[str, Any]) -> list[str]:
    """Hosts a deployment of this profile may call: derived from the endpoint table plus the SaaS the profile keeps."""
    ep = r["endpoints"]
    hosts = [host_of(ep["entraAuthority"]), *map(host_of, r["entraAuthorityAliases"]), host_of(ep["graphRoot"]), host_of(ep["armEndpoint"])]
    hosts += [f"*.{ep['keyVaultSuffix']}", f"*.{ep['blobSuffix']}", f"*.{ep['containerRegistrySuffix']}", f"*.{ep['postgresSuffix']}"]
    for name in r["llmProviders"]:
        hosts.append(f"*.{ep['azureOpenAISuffix']}" if name == "azure-openai" else LLM_HOSTS[name])
    if r["pagerDutyAllowed"]:
        hosts.append(PAGERDUTY_HOST)
    if not r["requireMirror"]:
        hosts.append(GHCR_HOST)
    hosts += r["extraAllow"]
    seen: list[str] = []
    for h in hosts:
        if h not in seen:
            seen.append(h)
    return seen


def _yaml(value: Any, indent: int = 0) -> list[str]:
    pad = "  " * indent
    lines: list[str] = []
    for key, val in value.items():
        if isinstance(val, dict):
            lines.append(f"{pad}{key}:")
            lines += _yaml(val, indent + 1)
        else:
            lines.append(f"{pad}{key}: {json.dumps(val)}")
    return lines


def helm_values(r: dict[str, Any], registry: str = "") -> str:
    """Helm values file for `r`: the `global` block every chart reads. `registry` is the mirror host and path prefix."""
    allow = egress_allowlist(r)
    mirror = registry.split("/")[0]
    if mirror and not host_allowed(mirror, allow):
        allow = allow + [mirror]
    doc = {
        "global": {
            "cloudProfile": r["name"],
            "cloudOverlays": r["overlays"],
            "cloudName": r["cloud"],
            "fips": r["fips"],
            # Every profile is an Azure cloud: ExternalSecret references use Key Vault naming (see passage.secretKey).
            "secretStoreProvider": "azure-key-vault",
            "imageRegistry": registry,
            "imageRegistryRequired": r["requireMirror"],
            "allowedRegions": r["allowedRegions"],
            "pagerDutyAllowed": r["pagerDutyAllowed"],
            "idleTimeoutMinutes": r["idleTimeoutMinutes"],
            "postgresSslMode": r["deployment"]["postgresSslMode"],
            "enforceProviders": r["enforceProviders"],
            "endpoints": r["endpoints"],
            "egressAllowlist": allow,
        }
    }
    return f"# {GENERATED_NOTE}\n" + "\n".join(_yaml(doc)) + "\n"


def values_path(name: str) -> Path:
    return VALUES_DIR / f"{name}.yaml"


def tofu_vars(r: dict[str, Any]) -> str:
    """`-var-file` for deploy/tofu/cluster: the profile name, its endpoints, regions and FIPS flag."""
    doc = {
        "cloud_profile": r["name"],
        "cloud_overlays": r["overlays"],
        "cloud_endpoints": r["endpoints"],
        "allowed_regions": r["allowedRegions"],
        "fips": r["fips"],
        "azure_environment": r["deployment"]["azureEnvironment"],
        "private_cluster": r["deployment"]["privateCluster"],
        "customer_managed_keys": r["deployment"]["customerManagedKeys"],
        "managed_hsm": r["deployment"]["managedHsm"],
        "require_isolated_nodes": r["deployment"]["isolatedNodes"],
        "isolated_nodes_regions": r["deployment"]["isolatedNodesRegions"],
        "hipaa": "hipaa" in r["overlays"],
        "azure_policy_initiative_id": r["azurePolicyInitiative"],
    }
    return json.dumps(doc, indent=2) + "\n"


def expected_copies(doc: dict[str, Any]) -> dict[Path, str]:
    """Every generated file and the text it must hold."""
    raw = PROFILES_JSON.read_text(encoding="utf-8")
    out: dict[Path, str] = {GO_COPY: raw}
    ts = (
        f"// {GENERATED_NOTE}\n"
        "export const CLOUD_PROFILE_DATA = "
        + json.dumps(doc, indent=2)
        + " as const;\n"
    )
    out[TS_COPY] = ts
    for name in sorted(doc["profiles"]):
        out[values_path(name)] = helm_values(resolve(doc, name))
        out[TOFU_DIR / f"{name}.tfvars.json"] = tofu_vars(resolve(doc, name))
    return out


def sync(check_only: bool = False) -> list[Path]:
    """Write (or with check_only, compare) the generated copies. Returns the paths that differ."""
    stale: list[Path] = []
    for path, text in expected_copies(load()).items():
        current = path.read_text(encoding="utf-8") if path.exists() else None
        if current != text:
            stale.append(path)
            if not check_only:
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(text, encoding="utf-8")
    return stale


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("sync", help="rewrite the generated copies")
    s.add_argument("--check", action="store_true")
    sub.add_parser("check", help="key parity and data rules")
    t = sub.add_parser("tofu-vars", help="print the OpenTofu -var-file for a selector such as gcc-high+hipaa")
    t.add_argument("profile")
    v = sub.add_parser("values", help="print the Helm values for one profile")
    v.add_argument("profile")
    v.add_argument("--overlay", action="append", default=[])
    v.add_argument("--registry", default="")
    args = parser.parse_args(argv)

    if args.cmd == "sync":
        stale = sync(args.check)
        for path in stale:
            print(("STALE  " if args.check else "wrote  ") + str(path.relative_to(REPO)))
        if args.check and stale:
            print("run: python3 deploy/cloud-profiles/cloudprofile.py sync", file=sys.stderr)
            return 1
        return 0
    doc = load()
    if args.cmd == "check":
        problems = check_parity(doc)
        for problem in problems:
            print(f"FAIL  {problem}")
        print("CLOUD PROFILES " + ("FAILED" if problems else "PASSED") + f": {', '.join(sorted(doc['profiles']))}")
        return 1 if problems else 0
    if args.cmd == "tofu-vars":
        try:
            base, overlays = parse_selector(args.profile)
            print(tofu_vars(resolve(doc, base, overlays)), end="")
        except ValueError as exc:
            print(f"error: {exc}", file=sys.stderr)
            return 2
        return 0
    try:
        base, overlays = parse_selector(args.profile)
        print(helm_values(resolve(doc, base, overlays + args.overlay), args.registry), end="")
    except ValueError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
