"""Structural checks for the Argo CD Applications under deploy/argocd and deploy/envs/<env>/apps.

ADR-0001 and architecture section 2 fix GitOps by Argo CD app-of-apps syncing digest-pinned Helm releases. This
test checks, without a cluster:
- every document is an argoproj.io/v1alpha1 Application or a list-generator ApplicationSet (expanded here);
- every Application lives in the argocd namespace, uses project default, and syncs automatically with prune and
  selfHeal; names are unique per environment;
- git sources point at this repo on main and at a path that exists; chart-repo sources pin an exact version;
  `$values/` value files exist unless the source sets ignoreMissingValueFiles;
- every Helm chart in the repo has a dev Application (EXEMPT lists the exceptions and why);
- the event backbone and Temporal Applications exist in dev and sync in order (BACKBONE_ORDER); every manifest in a
  plain-directory source is applied by some dev Application and parses; Strimzi custom resources sync after the
  operator and name an existing Kafka or KafkaConnect; their Secret references (KafkaConnector
  `${secrets:ns/name:key}`, workload secretKeyRef) are written by an ExternalSecret that a dev Application syncs;
- every consumer KafkaUser in deploy/kafka is the username of a chart's consumer, reads its group and writes only
  dead-letter topics a synced KafkaTopic creates; the inventory postings consumer's ACLs are exactly the topics,
  group and dead-letter topic its code uses (#480); an outbox connector that routes by event type (outbox-sales,
  outbox-fulfillment) sends every message of its service's AsyncAPI to that message's channel, and the debezium
  user's literal ACLs under its prefix are exactly those channels;
- if helm is on PATH, every dev and local Application whose source is a chart in this repo renders with its
  values, and outside local every container image is pinned by digest; Temporal's persistence values name the
  Service and Secret its database chart renders.
"""

import fnmatch
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
# Dev sync-wave order (#106): each group syncs strictly after the one before. The operator, then the Kafka cluster
# and the databases, then what the cluster's operators reconcile and Temporal, then Connect and its connectors.
BACKBONE_ORDER = [
    {"strimzi-operator"},
    {"kafka-cluster", "apicurio-postgres", "debezium-cdc-secrets", "temporal-postgres"},
    {"apicurio-registry", "kafka-consumer-users", "temporal"},
    {"debezium-connect"},
]
WAVE = "argocd.argoproj.io/sync-wave"
# Custom-resource API groups and the dev Application that installs their CRDs and controller.
CRD_PROVIDERS = {"kafka.strimzi.io": "strimzi-operator"}
CONNECT_SECRET_REF = re.compile(r"\$\{secrets:([^/}]+)/([^:}]+):([^}]+)\}")
MANIFEST_SUFFIXES = (".yaml", ".yml", ".json")
# Secrets that dev workloads read but no ExternalSecret writes yet: (namespace, name) -> where that is documented.
UNMANAGED_SECRETS = {
    ("hello-ledger", "keycloak-db"): "deploy/keycloak/README.md (created by hand, #80)",
    ("hello-ledger", "keycloak-admin"): "deploy/keycloak/README.md (created by hand, #80)",
}
# Service databases in the shared dev Postgres (deploy/envs/dev/apps/postgres.yaml) whose DATABASE_URL Secret an
# ExternalSecret in that file writes: dev Application -> (database, login role, Secret, key the chart reads).
DEV_POSTGRES_DATABASES = {
    "eam": ("passage_eam", "passage_eam_app", "eam-database", "url"),
    "transport": ("passage_transport", "passage_transport_app", "transport-db", "url"),
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
        # Skip dependencies, apps/ and hidden directories (.git, .claude/worktrees and other local checkouts).
        if "node_modules" in rel or rel.startswith("apps/") or any(part.startswith(".") for part in rel.split("/")):
            continue
        if chart.parent.parent.name == "charts" and (chart.parent.parent.parent / "Chart.yaml").exists():
            continue  # subchart, rendered by its parent
        found.add(rel)
    return found


def wave(app):
    return int((app["metadata"].get("annotations") or {}).get(WAVE, "0"))


def brace_expand(pattern):
    m = re.search(r"\{([^{}]*)\}", pattern)
    if not m:
        return [pattern]
    return [p for alt in m.group(1).split(",") for p in brace_expand(pattern[:m.start()] + alt + pattern[m.end():])]


def manifests(directory):
    return sorted(p for p in directory.iterdir() if p.is_file() and p.suffix in MANIFEST_SUFFIXES)


def directory_files(src):
    """(manifest files a non-Helm git source applies, every file it uses).

    A directory with kustomization.yaml is a Kustomize source: its local file resources, plus the kustomization and
    generator inputs as used files. Otherwise Argo CD's directory source: include/exclude globs, no recurse.
    """
    root = REPO / src["path"]
    kustomization = root / "kustomization.yaml"
    if kustomization.is_file():
        k = yaml.safe_load(kustomization.read_text())
        files = [root / r for r in k.get("resources") or []]
        for f in files:
            assert f.is_file(), f"{kustomization.relative_to(REPO)}: resource {f.name} is not a local file"
        inputs = [root / spec.partition("=")[2] if "=" in spec else root / spec
                  for gen in (k.get("configMapGenerator") or []) + (k.get("secretGenerator") or [])
                  for spec in gen.get("files") or []]
        for f in inputs:
            assert f.is_file(), f"{kustomization.relative_to(REPO)}: generator file {f.name} missing"
        return files, set(files) | set(inputs) | {kustomization}
    directory = src.get("directory") or {}
    assert not directory.get("recurse"), f"{src['path']}: directory.recurse is not modelled here"

    def matches(path, pattern):
        return any(fnmatch.fnmatch(path.name, alt) for alt in brace_expand(pattern))
    files = manifests(root)
    if directory.get("include"):
        files = [p for p in files if matches(p, directory["include"])]
    if directory.get("exclude"):
        files = [p for p in files if not matches(p, directory["exclude"])]
    return files, set(files)


def directory_sources(apps, env):
    """(app, source, manifest files, used files) for every git source in env that is not a Helm chart."""
    out = []
    for e, _, app in apps:
        for src in sources(app):
            if e == env and "path" in src and not (REPO / src["path"] / "Chart.yaml").is_file():
                out.append((app, src, *directory_files(src)))
    return out


def es_output(doc):
    """(namespace, Secret name, keys) an ExternalSecret writes."""
    target = doc["spec"].get("target") or {}
    template = (target.get("template") or {}).get("data") or {}
    keys = set(template) if template else {d["secretKey"] for d in doc["spec"].get("data") or []}
    return doc["metadata"].get("namespace"), target.get("name") or doc["metadata"]["name"], keys


def pod_containers(doc):
    spec = doc.get("spec") or {}
    if not isinstance(spec, dict):
        return []
    pod = ((spec.get("jobTemplate") or {}).get("spec") or {}).get("template", {}).get("spec") \
        or (spec.get("template") or {}).get("spec") or {}
    return [c for key in ("containers", "initContainers") for c in pod.get(key) or []]


def pod_images(doc):
    return [c["image"] for c in pod_containers(doc)]


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

    def dev_apps(self):
        return {app["metadata"]["name"]: app for env, _, app in self.apps if env == "dev"}

    def test_event_backbone_and_temporal_sync_in_order(self):
        dev = self.dev_apps()
        for name in set().union(*BACKBONE_ORDER):
            self.assertIn(name, dev, f"no dev Application {name} (#106)")
        for before, after in zip(BACKBONE_ORDER, BACKBONE_ORDER[1:]):
            for b in sorted(before):
                for a in sorted(after):
                    self.assertLess(wave(dev[b]), wave(dev[a]), f"{b} must sync before {a}")
        self.assertEqual([s.get("chart") for s in sources(dev["strimzi-operator"])], ["strimzi-kafka-operator"])
        temporal = sources(dev["temporal"])
        self.assertIn("temporal", [s.get("chart") for s in temporal])
        self.assertIn("$values/deploy/temporal/values.yaml",
                      [vf for s in temporal for vf in s.get("helm", {}).get("valueFiles", [])])
        services = load_docs(DEPLOY / "envs/dev/apps/services.yaml")[0]
        self.assertGreater(wave(services), max(wave(dev[n]) for n in set().union(*BACKBONE_ORDER)),
                           "the services ApplicationSet must sync after the event backbone")

    def test_directory_sources_apply_every_manifest(self):
        applied = {}
        for env in ("dev", "local"):
            for app, src, files, used in directory_sources(self.apps, env):
                with self.subTest(env=env, app=app["metadata"]["name"]):
                    self.assertTrue(files, f"{app['metadata']['name']}: nothing in {src['path']} matches")
                if env == "dev":
                    applied.setdefault(REPO / src["path"], set()).update(used)
        for directory, files in applied.items():
            with self.subTest(directory=directory.relative_to(REPO).as_posix()):
                orphans = [p.name for p in manifests(directory) if p not in files]
                self.assertEqual(orphans, [], f"{directory.relative_to(REPO)}: manifests no dev Application applies")

    def test_directory_manifests_resolve(self):
        dev = self.dev_apps()
        docs = []
        for app, src, files, _ in directory_sources(self.apps, "dev"):
            name = app["metadata"]["name"]
            for f in files:
                for doc in load_docs(f):
                    docs.append((app, doc))
                    with self.subTest(app=name, file=f.name):
                        self.assertTrue(doc.get("apiVersion") and doc.get("kind"), f"{f}: document without kind")
                        self.assertTrue((doc.get("metadata") or {}).get("name"), f"{f}: {doc['kind']} without name")
                        ns = doc["metadata"].get("namespace")
                        if ns and doc["kind"] != "Namespace":
                            self.assertEqual(ns, app["spec"]["destination"]["namespace"], f"{f}: {doc['kind']}")
                        provider = CRD_PROVIDERS.get(doc["apiVersion"].split("/")[0])
                        if provider:
                            self.assertGreater(wave(app), wave(dev[provider]), f"{name}: {doc['kind']} before its CRD")
                            self.assertIn("SkipDryRunOnMissingResource=true",
                                          app["spec"]["syncPolicy"].get("syncOptions", []), name)

        def named(kind):
            return {d["metadata"]["name"] for _, d in docs if d["kind"] == kind}
        written = {}
        for _, d in docs:
            if d["kind"] == "ExternalSecret":
                ns, secret, keys = es_output(d)
                written[(ns, secret)] = keys
        readable = {(r["metadata"].get("namespace"), n) for _, r in docs if r["kind"] == "Role"
                    for rule in r["rules"] if "get" in rule["verbs"] for n in rule.get("resourceNames", [])}
        for app, d in docs:
            kind, name = d["kind"], d["metadata"]["name"]
            ns = d["metadata"].get("namespace") or app["spec"]["destination"]["namespace"]
            cluster = (d["metadata"].get("labels") or {}).get("strimzi.io/cluster")
            with self.subTest(kind=kind, name=name):
                if kind in ("KafkaNodePool", "KafkaUser", "KafkaTopic"):
                    self.assertIn(cluster, named("Kafka"), f"{kind} {name}: strimzi.io/cluster")
                if kind == "KafkaConnect":
                    user = d["spec"]["authentication"]["passwordSecret"]["secretName"]
                    self.assertIn(user, named("KafkaUser"), f"KafkaConnect {name}: no KafkaUser {user}")
                if kind == "KafkaConnector":
                    self.assertIn(cluster, named("KafkaConnect"), f"KafkaConnector {name}: strimzi.io/cluster")
                    for sns, secret, key in CONNECT_SECRET_REF.findall(json.dumps(d["spec"]["config"])):
                        self.assertIn(key, written.get((sns, secret), set()), f"{name}: nothing writes {secret}/{key}")
                        self.assertIn((sns, secret), readable, f"{name}: no Role lets Connect read {secret}")
                for c in pod_containers(d):
                    for e in c.get("env") or []:
                        ref = (e.get("valueFrom") or {}).get("secretKeyRef")
                        if ref and (ns, ref["name"]) not in UNMANAGED_SECRETS:
                            self.assertIn(ref["key"], written.get((ns, ref["name"]), set()),
                                          f"{kind} {name}: nothing writes {ref['name']}/{ref['key']}")

    def dev_postgres(self):
        docs = load_docs(DEPLOY / "envs/dev/apps/postgres.yaml")
        return {(d["kind"], d["metadata"]["name"]): d for d in docs}

    # The dev Postgres creates each service's login role from the password its DATABASE_URL Secret carries, an
    # ExternalSecret writes that Secret, and the NetworkPolicy admits the service's pods (#426).
    def test_dev_postgres_writes_and_admits_service_databases(self):
        dev, pg = self.dev_apps(), self.dev_postgres()
        secrets = {es_output(d)[:2]: d for (kind, _), d in pg.items() if kind == "ExternalSecret"}
        scripts = pg[("ConfigMap", "postgres-init")]["data"].values()
        (container,) = pod_containers(pg[("Deployment", "postgres")])
        env = {e["name"]: e["valueFrom"]["secretKeyRef"] for e in container["env"] if "valueFrom" in e}
        policy = pg[("NetworkPolicy", "postgres")]
        admitted = {f["podSelector"]["matchLabels"].get("app.kubernetes.io/name")
                    for rule in policy["spec"]["ingress"] for f in rule["from"] if "namespaceSelector" not in f}
        for app, (database, role, secret, key) in DEV_POSTGRES_DATABASES.items():
            with self.subTest(app=app):
                ns = dev[app]["spec"]["destination"]["namespace"]
                self.assertEqual(ns, policy["metadata"]["namespace"], f"{app} runs outside the dev Postgres namespace")
                self.assertIn(app, admitted, f"the dev Postgres NetworkPolicy does not admit {app}")
                es = secrets.get((ns, secret))
                self.assertIsNotNone(es, f"no ExternalSecret in postgres.yaml writes {ns}/{secret}")
                template = es["spec"]["target"]["template"]["data"]
                self.assertEqual(template[key],
                                 f"postgres://{role}:{{{{ .password }}}}@postgres.{ns}.svc:5432/{database}?sslmode=disable")
                self.assertEqual(template["APP_PASSWORD"], "{{ .password }}")
                self.assertEqual(len(es["spec"]["data"]), 1, f"{secret}: one Vault key for the URL and the role")
                (password_env,) = [n for n, ref in env.items() if ref == {"name": secret, "key": "APP_PASSWORD"}]
                (script,) = [s for s in scripts if f"CREATE ROLE {role} " in s]
                self.assertIn(f'-v app_password="${password_env}"', script)
                self.assertIn(f"CREATE ROLE {role} LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD :'app_password';", script)
                self.assertIn(f"CREATE DATABASE {database}'", script)
                self.assertRegex(script, rf"GRANT CONNECT, CREATE ON DATABASE {database} TO [^;]*\b{role}\b")
                self.assertRegex(script, rf"\\c {database}\n[^\n]*GRANT ALL ON SCHEMA public TO [^;]*\b{role}\b")

    @unittest.skipUnless(shutil.which("helm"), "helm not on PATH")
    def test_service_charts_read_their_dev_postgres_secret(self):
        dev = self.dev_apps()
        for app, (_, _, secret, key) in DEV_POSTGRES_DATABASES.items():
            with self.subTest(app=app):
                src = next(s for s in sources(dev[app]) if "path" in s)
                (workload,) = [d for d in render(dev[app], src) if d["kind"] == "Deployment"]
                pod = workload["spec"]["template"]
                self.assertEqual(pod["metadata"]["labels"].get("app.kubernetes.io/name"), app, "NetworkPolicy label")
                refs = [e["valueFrom"]["secretKeyRef"] for c in pod_containers(workload) for e in c.get("env") or []
                        if e["name"] == "DATABASE_URL"]
                self.assertEqual(refs, [{"name": secret, "key": key}])

    def synced_kafka_resources(self):
        """KafkaUser name -> {(resource type, name): operations} and the KafkaTopic names dev Applications sync."""
        users, topics = {}, set()
        for _, _, files, _ in directory_sources(self.apps, "dev"):
            for f in files:
                for doc in load_docs(f):
                    if doc.get("kind") == "KafkaUser":
                        acls = doc["spec"].get("authorization", {}).get("acls") or []
                        users[doc["metadata"]["name"]] = {(a["resource"]["type"], a["resource"].get("name", "")): set(a["operations"])
                                                          for a in acls}
                    elif doc.get("kind") == "KafkaTopic":
                        topics.add(doc["spec"]["topicName"])
        return users, topics

    def test_consumer_kafka_users_match_their_charts(self):
        # Every consumer KafkaUser in deploy/kafka is named by a chart's consumer username, reads that chart's group,
        # and writes only dead-letter topics that a synced KafkaTopic creates.
        users, topics = self.synced_kafka_resources()
        charts_by_user = {}
        for values_file in sorted(REPO.glob("services/*/charts/*/values.yaml")):
            values = yaml.safe_load(values_file.read_text()) or {}
            kafka = ((values.get("consumer") or {}).get("kafka")) or values.get("kafka")
            if isinstance(kafka, dict) and kafka.get("username"):
                charts_by_user.setdefault(kafka["username"], []).append((values_file, kafka))
        for path in manifests(DEPLOY / "kafka"):
            if not path.name.endswith("-consumer.yaml"):
                continue
            for doc in load_docs(path):
                if doc.get("kind") != "KafkaUser":
                    continue
                name = doc["metadata"]["name"]
                with self.subTest(user=name):
                    self.assertIn(name, charts_by_user, f"{path.name}: no chart names consumer username {name}")
                    granted = users[name]
                    for values_file, kafka in charts_by_user[name]:
                        self.assertIn("Read", granted.get(("group", kafka.get("group")), set()),
                                      f"{name}: no Read on group {kafka.get('group')} ({values_file.relative_to(REPO)})")
                        if kafka.get("dlqTopic"):
                            self.assertIn("Write", granted.get(("topic", kafka["dlqTopic"]), set()), f"{name}: dlqTopic")
                    for (kind, resource), ops in granted.items():
                        if kind == "topic" and "Write" in ops:
                            self.assertTrue(resource.endswith(".dlq"), f"{name} writes {resource}, not a dead-letter topic")
                            self.assertIn(resource, topics, f"{name}: no synced KafkaTopic creates {resource}")

    def test_inventory_postings_consumer_acls(self):
        # The inventory postings consumer (#480): its KafkaUser reads exactly the topics and group the code subscribes
        # to and writes the dead-letter topic the code parks on.
        api = (REPO / "services/inventory/internal/api/postings_consumer.go").read_text()
        cmd = (REPO / "services/inventory/cmd/inventory/consumer.go").read_text()
        source_topics = set(re.findall(r'^\s+\w+Topic\s+=\s+"([^"]+)"', api, re.M))
        (group,) = re.findall(r'PostingsConsumerName\s+=\s+"([^"]+)"', api)
        (dlq,) = re.findall(r'postingsDeadLetterTopic\s+=\s+"([^"]+)"', cmd)
        values = yaml.safe_load((REPO / "services/inventory/charts/inventory/values.yaml").read_text())
        kafka = values["consumer"]["kafka"]
        users, topics = self.synced_kafka_resources()
        self.assertEqual(kafka["group"], group)
        self.assertIn(kafka["username"], users, "deploy/kafka/inventory-consumer.yaml")
        granted = users[kafka["username"]]
        self.assertEqual(source_topics, {"passage.receiving.receipt", "passage.sales.orderprogress", "passage.fulfillment.packingslip"})
        want = {("topic", t): {"Read", "Describe"} for t in source_topics}
        want[("topic", dlq)] = {"Write", "Describe"}
        want[("group", group)] = {"Read", "Describe"}
        self.assertEqual(granted, want)
        self.assertIn(dlq, topics)

    def test_revrec_credit_note_consumer_acls(self):
        # The revrec credit-note consumer (#29): its KafkaUser reads exactly billing's invoice topic, the channel billing's
        # AsyncAPI declares for creditnote.booked, and the group the code uses, and writes the dead-letter topic it parks on.
        api = (REPO / "services/revrec/internal/api/creditnotes_consumer.go").read_text()
        cmd = (REPO / "services/revrec/cmd/revrec/consumer.go").read_text()
        (topic,) = re.findall(r'BillingInvoiceTopic\s+=\s+"([^"]+)"', api)
        (event_type,) = re.findall(r'CreditNoteBookedType\s+=\s+"([^"]+)"', api)
        (group,) = re.findall(r'CreditNoteConsumerName\s+=\s+"([^"]+)"', api)
        (dlq,) = re.findall(r'creditNoteDeadLetterTopic\s+=\s+"([^"]+)"', cmd)
        spec = yaml.safe_load((REPO / "services/billing/events/asyncapi.yaml").read_text())
        channels = {ch["address"] for ch in spec["channels"].values()
                    for ref in ch["messages"].values()
                    if spec["components"]["messages"][ref["$ref"].rsplit("/", 1)[1]]["name"] == event_type}
        self.assertEqual(channels, {topic})
        values = yaml.safe_load((REPO / "services/revrec/charts/revrec/values.yaml").read_text())
        kafka = values["consumer"]["kafka"]
        self.assertFalse(values["consumer"]["enabled"], "the revrec consumer is off by default, like the other consumers")
        users, topics = self.synced_kafka_resources()
        self.assertEqual((kafka["group"], kafka["dlqTopic"]), (group, dlq))
        self.assertIn(kafka["username"], users, "deploy/kafka/revrec-consumer.yaml")
        self.assertEqual(users[kafka["username"]], {("topic", topic): {"Read", "Describe"}, ("topic", dlq): {"Write", "Describe"},
                                                    ("group", group): {"Read", "Describe"}})
        self.assertIn(dlq, topics)

    def test_type_routed_outbox_connectors_match_their_asyncapi_channels(self):
        # outbox-sales and outbox-fulfillment route by the event type's entity, not aggregate_type (#480): every
        # message the service's AsyncAPI declares under its own topic prefix routes to that message's channel, the
        # debezium user writes exactly those channels under the prefix by literal ACL, and the inventory postings
        # consumer's sales and fulfillment topics are among them.
        docs = load_docs(DEPLOY / "kafka/connect.yaml")
        (debezium,) = [d for d in docs if d["kind"] == "KafkaUser" and d["metadata"]["name"] == "debezium"]
        writable = {a["resource"]["name"] for a in debezium["spec"]["authorization"]["acls"]
                    if a["resource"]["type"] == "topic" and a["resource"].get("patternType") == "literal"
                    and "Write" in a["operations"]}
        connectors = {d["metadata"]["name"]: d["spec"]["config"] for d in docs if d["kind"] == "KafkaConnector"
                      and d["spec"]["config"].get("transforms.outbox.route.by.field") == "type"}
        self.assertLessEqual({"outbox-sales", "outbox-fulfillment"}, set(connectors))
        routed = set()
        for name, cfg in connectors.items():
            with self.subTest(connector=name):
                service = name.removeprefix("outbox-")
                regex = re.compile(cfg["transforms.outbox.route.topic.regex"].replace("(?<", "(?P<"))
                replacement = cfg["transforms.outbox.route.topic.replacement"]
                prefix = replacement.split("${", 1)[0]
                self.assertEqual(cfg["topic.prefix"] + ".", prefix)
                spec = yaml.safe_load((REPO / f"services/{service}/events/asyncapi.yaml").read_text())
                messages = spec["components"]["messages"]
                channels = set()
                for channel in spec["channels"].values():
                    if not channel["address"].startswith(prefix):
                        continue
                    channels.add(channel["address"])
                    for ref in channel["messages"].values():
                        typ = messages[ref["$ref"].rsplit("/", 1)[1]]["name"]
                        m = regex.fullmatch(typ)
                        self.assertIsNotNone(m, f"{name}: {typ} does not match the route")
                        self.assertEqual(replacement.replace("${routedByValue}", m["routedByValue"]), channel["address"], typ)
                self.assertEqual({t for t in writable if t.startswith(prefix)}, channels,
                                 f"{name}: debezium literal ACLs under {prefix} differ from the AsyncAPI channels")
                routed |= channels
        self.assertLessEqual({"passage.sales.orderprogress", "passage.fulfillment.packingslip"}, routed)

    def test_outbox_budgeting_publishes_only_topics_the_debezium_user_may_write(self):
        docs = load_docs(DEPLOY / "kafka/connect.yaml")
        (debezium,) = [d for d in docs if d["kind"] == "KafkaUser" and d["metadata"]["name"] == "debezium"]
        writable = {a["resource"]["name"] for a in debezium["spec"]["authorization"]["acls"]
                    if a["resource"]["type"] == "topic" and a["resource"].get("patternType") == "literal"
                    and "Write" in a["operations"]}
        (cfg,) = [d["spec"]["config"] for d in docs if d["kind"] == "KafkaConnector" and d["metadata"]["name"] == "outbox-budgeting"]
        chain = cfg["transforms"].split(",")
        self.assertLess(chain.index("outbox"), chain.index("onlyPlan"), "the filter must see the routed topic")
        self.assertLess(chain.index("onlyPlan"), chain.index("enrich"))
        self.assertEqual("org.apache.kafka.connect.transforms.Filter", cfg["transforms.onlyPlan.type"])
        self.assertIs(True, cfg["transforms.onlyPlan.negate"])
        predicate = cfg["transforms.onlyPlan.predicate"]
        self.assertIn(predicate, cfg["predicates"].split(","))
        self.assertEqual("org.apache.kafka.connect.transforms.predicates.TopicNameMatches", cfg[f"predicates.{predicate}.type"])
        pattern = re.compile(cfg[f"predicates.{predicate}.pattern"])
        spec = yaml.safe_load((REPO / "services/budgeting/events/asyncapi.yaml").read_text())
        channels = {c["address"] for c in spec["channels"].values()}
        self.assertEqual({"passage.budget.plan"}, {t for t in channels | writable if pattern.fullmatch(t)})
        self.assertLessEqual({t for t in channels if pattern.fullmatch(t)}, writable)

    def test_outbox_billing_publishes_only_the_invoice_channel_the_debezium_user_may_write(self):
        # Billing enqueues one aggregate type, so no Filter SMT is needed; the day a second one appears it would route to a
        # topic without ACL or channel, which this test refuses until the connector filters it like outbox-budgeting.
        docs = load_docs(DEPLOY / "kafka/connect.yaml")
        (debezium,) = [d for d in docs if d["kind"] == "KafkaUser" and d["metadata"]["name"] == "debezium"]
        writable = {a["resource"]["name"] for a in debezium["spec"]["authorization"]["acls"]
                    if a["resource"]["type"] == "topic" and a["resource"].get("patternType") == "literal"
                    and "Write" in a["operations"]}
        (cfg,) = [d["spec"]["config"] for d in docs if d["kind"] == "KafkaConnector" and d["metadata"]["name"] == "outbox-billing"]
        self.assertEqual("aggregate_type", cfg["transforms.outbox.route.by.field"])
        self.assertEqual("passage.billing", cfg["topic.prefix"])
        self.assertEqual("passage.billing.${routedByValue}", cfg["transforms.outbox.route.topic.replacement"])
        chain = cfg["transforms"].split(",")
        self.assertEqual("outbox", chain[0])
        self.assertEqual("enrich", chain[-1])
        aggregates = set()
        for f in (REPO / "services/billing/internal/store").glob("*.go"):
            if not f.name.endswith("_test.go"):
                aggregates |= set(re.findall(r'outbox\.Enqueue\(ctx, tx, "([^"]+)"', f.read_text()))
        self.assertEqual({"invoice"}, aggregates, "a new billing aggregate type needs a channel, an ACL and a Filter in outbox-billing")
        spec = yaml.safe_load((REPO / "services/billing/events/asyncapi.yaml").read_text())
        channels = {c["address"] for c in spec["channels"].values()}
        routed = {cfg["transforms.outbox.route.topic.replacement"].replace("${routedByValue}", a) for a in aggregates}
        self.assertEqual(channels, routed)
        self.assertLessEqual(routed, writable)
        self.assertEqual({"passage.billing.invoice"}, {t for t in writable if t.startswith("passage.billing.")})
        secrets = {d["metadata"]["name"] for f in manifests(DEPLOY / "envs/dev/kafka-secrets") for d in load_docs(f)
                   if d["kind"] == "ExternalSecret"}
        self.assertIn("debezium-cdc-billing", secrets)
        (role,) = [d for f in manifests(DEPLOY / "envs/dev/kafka-secrets") for d in load_docs(f)
                   if d["kind"] == "Role" and d["metadata"]["name"] == "passage-connect-read-debezium-cdc"]
        self.assertIn("debezium-cdc-billing", role["rules"][0]["resourceNames"])

    @unittest.skipUnless(shutil.which("helm"), "helm not on PATH")
    def test_temporal_values_match_its_database(self):
        dev = self.dev_apps()
        db = dev["temporal-postgres"]
        ns = db["spec"]["destination"]["namespace"]
        self.assertEqual(ns, dev["temporal"]["spec"]["destination"]["namespace"])
        docs = render(db, sources(db)[0])
        hosts = {f"{d['metadata']['name']}.{ns}.svc" for d in docs if d["kind"] == "Service"}
        written = {es_output(d)[1]: es_output(d)[2] for d in docs if d["kind"] == "ExternalSecret"}
        values = yaml.safe_load((DEPLOY / "temporal/values.yaml").read_text())
        for store, cfg in values["server"]["config"]["persistence"].items():
            with self.subTest(store=store):
                self.assertIn(cfg["sql"]["host"], hosts, f"temporal {store} host")
                self.assertIn("password", written.get(cfg["sql"]["existingSecret"], set()), f"temporal {store} secret")

    @unittest.skipUnless(shutil.which("helm"), "helm not on PATH")
    def test_apicurio_database_secret_matches_its_database(self):
        dev = self.dev_apps()
        db = dev["apicurio-postgres"]
        values = sources(db)[0]["helm"]["valuesObject"]
        docs = render(db, sources(db)[0])
        ns = db["spec"]["destination"]["namespace"]
        (service,) = [d["metadata"]["name"] for d in docs if d["kind"] == "Service"]
        (db_secret,) = [d for d in docs if d["kind"] == "ExternalSecret"]
        app_password = next(x["remoteRef"] for x in db_secret["spec"]["data"] if x["secretKey"] == "APP_PASSWORD")
        (registry_secret,) = [d for f in manifests(DEPLOY / "envs/dev/kafka-secrets") for d in load_docs(f)
                              if d["kind"] == "ExternalSecret" and d["metadata"]["name"] == "apicurio-database"]
        template = registry_secret["spec"]["target"]["template"]["data"]
        self.assertEqual(template["JDBC_URL"], f"jdbc:postgresql://{service}.{ns}.svc:5432/{values['database']}")
        self.assertEqual(template["USERNAME"], "passage_app")
        self.assertEqual([x["remoteRef"] for x in registry_secret["spec"]["data"]], [app_password])
        policy = next(d for d in docs if d["kind"] == "NetworkPolicy")
        sources_allowed = [f for rule in policy["spec"]["ingress"] for f in rule["from"]]
        self.assertEqual(sources_allowed, [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "apicurio-registry"}}}])

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
