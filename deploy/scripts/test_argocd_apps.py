"""Structural checks for the Argo CD Applications under deploy/argocd and deploy/envs/<env>/apps.

ADR-0001 and architecture section 2 fix GitOps by Argo CD app-of-apps syncing digest-pinned Helm releases. This
test checks, without a cluster:
- every document is an argoproj.io/v1alpha1 Application or a list-generator ApplicationSet (expanded here);
- every Application lives in the argocd namespace, uses project default, and syncs automatically with prune and
  selfHeal; names are unique per environment;
- git sources point at this repo on main and at a path that exists; chart-repo sources pin an exact version;
  `$values/` value files exist unless the source sets ignoreMissingValueFiles;
- every Helm chart in the repo has a dev Application (EXEMPT lists the exceptions and why);
- if helm is on PATH, every dev and local Application whose source is a chart in this repo renders with its
  values, and outside local every container image is pinned by digest.
"""

import json
import os
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml

DEPLOY = Path(__file__).resolve().parents[1]
REPO = DEPLOY.parent
REPO_URL = "https://github.com/BenjaminSRussell/Passage.git"
SEMVER = re.compile(r"^v?\d+\.\d+\.\d+$")
TEMPLATE_VAR = re.compile(r"\{\{\s*\.(\w+)\s*\}\}")

# Charts deliberately without a dev Application.
EXEMPT = {
    # Per-region CNPG cluster, fence and alert rules; one release per region cluster with
    # deploy/multi-region/values/<region>.yaml. There are no region clusters yet (docs/ops/NOT-IMPLEMENTED.md).
    "deploy/multi-region/chart",
}
# Applications whose images are built and imported locally by `make deploy-local` (tag, not digest).
LOCAL_IMAGE_APPS = {("local", "hello-ledger")}


def load_docs(path):
    return [d for d in yaml.safe_load_all(path.read_text()) if d]


def expand(appset):
    gens = appset["spec"]["generators"]
    if len(gens) != 1 or set(gens[0]) != {"list"}:
        raise AssertionError(f"ApplicationSet {appset['metadata']['name']}: only one list generator is supported")
    raw = json.dumps(appset["spec"]["template"])
    apps = []
    for element in gens[0]["list"]["elements"]:
        def sub(m, element=element):
            if m.group(1) not in element:
                raise AssertionError(f"ApplicationSet element {element} has no {m.group(1)} (missingkey=error)")
            return str(element[m.group(1)])
        app = json.loads(TEMPLATE_VAR.sub(sub, raw))
        app.update(apiVersion="argoproj.io/v1alpha1", kind="Application")
        apps.append(app)
    return apps


def applications():
    """(env, file, Application) for every Application, ApplicationSets expanded."""
    files = [("local" if "local" in p.name else "argocd", p) for p in sorted((DEPLOY / "argocd").glob("*.yaml"))]
    for env_dir in sorted((DEPLOY / "envs").iterdir()):
        files += [(env_dir.name, p) for p in sorted((env_dir / "apps").glob("*.yaml"))]
    out = []
    for env, path in files:
        for doc in load_docs(path):
            if doc.get("apiVersion") != "argoproj.io/v1alpha1":
                continue  # plain resources the root app applies directly (e.g. cert-issuer.yaml)
            if doc["kind"] == "ApplicationSet":
                assert doc["metadata"]["namespace"] == "argocd", f"{path}: ApplicationSet outside argocd"
                assert doc["spec"].get("goTemplate") is True, f"{path}: goTemplate must be true"
                out += [(env, path, app) for app in expand(doc)]
            else:
                assert doc["kind"] == "Application", f"{path}: unexpected kind {doc['kind']}"
                out.append((env, path, doc))
    return out


def sources(app):
    spec = app["spec"]
    return spec["sources"] if "sources" in spec else [spec["source"]]


def charts():
    found = set()
    for chart in REPO.glob("**/Chart.yaml"):
        rel = chart.parent.relative_to(REPO).as_posix()
        if "node_modules" in rel or rel.startswith("apps/"):
            continue
        if chart.parent.parent.name == "charts" and (chart.parent.parent.parent / "Chart.yaml").exists():
            continue  # subchart, rendered by its parent
        found.add(rel)
    return found


def pod_images(doc):
    spec = doc.get("spec") or {}
    if not isinstance(spec, dict):
        return []
    pod = ((spec.get("jobTemplate") or {}).get("spec") or {}).get("template", {}).get("spec") \
        or (spec.get("template") or {}).get("spec") or {}
    return [c["image"] for key in ("containers", "initContainers") for c in pod.get(key) or []]


class ArgoApplicationsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.apps = applications()

    def test_names_unique_per_environment(self):
        seen = {}
        for env, path, app in self.apps:
            key = (env, app["metadata"]["name"])
            self.assertNotIn(key, seen, f"{path}: duplicate Application {key} (also in {seen.get(key)})")
            seen[key] = path

    def test_sync_policy(self):
        for env, path, app in self.apps:
            name = app["metadata"]["name"]
            with self.subTest(app=name, env=env):
                self.assertEqual(app["metadata"]["namespace"], "argocd", path)
                self.assertEqual(app["spec"]["project"], "default", path)
                automated = app["spec"].get("syncPolicy", {}).get("automated")
                self.assertEqual(automated, {"prune": True, "selfHeal": True}, f"{path}: {name} sync policy")
                dest = app["spec"]["destination"]
                self.assertTrue(dest.get("namespace"), f"{path}: {name} has no destination namespace")
                self.assertTrue(dest.get("server") or dest.get("name"), f"{path}: {name} has no destination")

    def test_sources(self):
        for env, path, app in self.apps:
            name = app["metadata"]["name"]
            refs = {s["ref"] for s in sources(app) if "ref" in s}
            for src in sources(app):
                with self.subTest(app=name, env=env, source=src.get("path") or src.get("chart") or src.get("ref")):
                    if "chart" in src:
                        self.assertRegex(src["targetRevision"], SEMVER, f"{path}: {name} chart version must be exact")
                        continue
                    self.assertEqual(src["repoURL"], REPO_URL, path)
                    self.assertEqual(src["targetRevision"], "main", path)
                    if "path" in src:
                        self.assertTrue((REPO / src["path"]).is_dir(), f"{path}: {name} path {src['path']} missing")
                        for inc in [src.get("directory", {}).get("include")] if src.get("directory") else []:
                            if inc and "*" not in inc and "{" not in inc:
                                self.assertTrue((REPO / src["path"] / inc).is_file(), f"{path}: {inc} missing")
                    helm = src.get("helm", {})
                    for vf in helm.get("valueFiles", []):
                        ref, _, rel = vf.partition("/")
                        self.assertTrue(ref.startswith("$") and ref[1:] in refs, f"{path}: {name} value file {vf}")
                        if not helm.get("ignoreMissingValueFiles"):
                            self.assertTrue((REPO / rel).is_file(), f"{path}: {name} value file {vf} missing")
                    if env != "local":
                        tag = (helm.get("valuesObject") or {}).get("image", {}).get("tag")
                        self.assertFalse(tag, f"{path}: {name} sets image.tag outside local; pin a digest")

    def test_every_chart_has_a_dev_application(self):
        dev_paths = {s["path"] for env, _, app in self.apps if env == "dev" for s in sources(app) if "path" in s}
        for chart in sorted(charts() - EXEMPT):
            with self.subTest(chart=chart):
                self.assertIn(chart, dev_paths, f"{chart} has no Application in deploy/envs/dev/apps")
        for chart in EXEMPT:
            self.assertTrue((REPO / chart / "Chart.yaml").is_file(), f"stale EXEMPT entry {chart}")

    @unittest.skipUnless(shutil.which("helm"), "helm not on PATH")
    def test_dev_charts_render_digest_pinned(self):
        for env, path, app in self.apps:
            if env not in ("dev", "local"):
                continue
            name = app["metadata"]["name"]
            for src in sources(app):
                if "path" not in src or not (REPO / src["path"] / "Chart.yaml").is_file():
                    continue
                with self.subTest(app=name, env=env):
                    docs = render(app, src)
                    images = [i for d in docs for i in pod_images(d)]
                    self.assertTrue(images or name == "observability", f"{name}: no workloads rendered")
                    if (env, name) not in LOCAL_IMAGE_APPS:
                        unpinned = [i for i in images if "@sha256:" not in i]
                        self.assertEqual(unpinned, [], f"{path}: {name} renders images without a digest")


def render(app, src):
    helm = src.get("helm", {})
    cmd = ["helm", "template", helm.get("releaseName", app["metadata"]["name"]), str(REPO / src["path"]),
           "--namespace", app["spec"]["destination"]["namespace"]]
    for vf in helm.get("valueFiles", []):
        file = REPO / vf.partition("/")[2]
        if file.is_file():
            cmd += ["-f", str(file)]
    with tempfile.TemporaryDirectory() as tmp:
        if helm.get("valuesObject"):
            obj = Path(tmp) / "values.yaml"
            obj.write_text(yaml.safe_dump(helm["valuesObject"]))
            cmd += ["-f", str(obj)]
        for p in helm.get("parameters", []):
            cmd += ["--set-string" if p.get("forceString") else "--set", f"{p['name']}={p['value']}"]
        res = subprocess.run(cmd, capture_output=True, text=True, env={**os.environ, "HELM_NAMESPACE": ""})
    if res.returncode:
        raise AssertionError(f"helm template {src['path']} failed: {res.stderr.strip()}")
    return [d for d in yaml.safe_load_all(res.stdout) if d]


if __name__ == "__main__":
    unittest.main()
