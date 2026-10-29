"""Structural checks for the chaos experiments. Run: python3 -m unittest discover -s deploy/multi-region/chaos

The experiments run against a live cluster (a later slice). These tests keep the manifests honest: each
one is a single-pod, budgeted, acceptance-tagged PodChaos aimed at the right role, and each links to a
runbook that exists in docs/ops.
"""

import re
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
RUNBOOKS = HERE.parents[2] / "docs" / "ops" / "runbooks"
EXPERIMENTS = sorted(HERE.glob("*.yaml"))
CHART_VALUES = HERE.parent / "chart" / "values.yaml"
KAFKA = HERE.parents[1] / "kafka" / "kafka.yaml"


def read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


class TestExperiments(unittest.TestCase):
    def test_there_are_two_experiments(self) -> None:
        self.assertEqual([p.name for p in EXPERIMENTS], ["kafka-broker-kill.yaml", "postgres-primary-kill.yaml"])

    def test_each_is_single_pod_pod_kill(self) -> None:
        for path in EXPERIMENTS:
            text = read(path)
            with self.subTest(file=path.name):
                self.assertRegex(text, r"(?m)^apiVersion: chaos-mesh\.org/v1alpha1$")
                self.assertRegex(text, r"(?m)^kind: PodChaos$")
                self.assertRegex(text, r"(?m)^  action: pod-kill$")
                self.assertRegex(text, r"(?m)^  mode: one$")
                self.assertNotRegex(text, r"mode: (all|fixed|fixed-percent)")

    def test_recovery_budget_and_acceptance_present(self) -> None:
        for path in EXPERIMENTS:
            text = read(path)
            with self.subTest(file=path.name):
                self.assertIn('passage.dev/recovery-budget: "5m"', text)
                self.assertIn("no failed postings", text)

    def test_runbook_link_resolves_to_docs_page(self) -> None:
        for path in EXPERIMENTS:
            text = read(path)
            match = re.search(r"passage\.dev/runbook: \"https://docs\.passage\.dev/ops/runbooks/([a-z0-9-]+)\"", text)
            with self.subTest(file=path.name):
                self.assertIsNotNone(match)
                assert match is not None
                self.assertTrue((RUNBOOKS / f"{match.group(1)}.md").is_file(), match.group(1))

    def test_postgres_targets_the_primary_of_the_charts_cluster(self) -> None:
        # The chart's defaults name the CNPG cluster and its namespace; a rename there must fail here.
        values = read(CHART_VALUES)
        cluster = re.search(r"(?m)^postgres:\n(?:  .*\n)*?  clusterName: (\S+)", values)
        namespace = re.search(r"(?m)^region:\n(?:  .*\n)*?  namespace: (\S+)", values)
        assert cluster is not None and namespace is not None
        text = read(HERE / "postgres-primary-kill.yaml")
        self.assertIn(f"cnpg.io/cluster: {cluster.group(1)}", text)
        self.assertIn("cnpg.io/instanceRole: primary", text)
        self.assertIn(f"  namespace: {namespace.group(1)}", text)
        self.assertRegex(text, rf"(?m)^      - {re.escape(namespace.group(1))}$")

    def test_kafka_targets_brokers_of_the_deployed_cluster(self) -> None:
        kafka = read(KAFKA)
        name = re.search(r"(?m)^kind: Kafka\nmetadata:\n  name: (\S+)\n  namespace: (\S+)", kafka)
        assert name is not None
        text = read(HERE / "kafka-broker-kill.yaml")
        self.assertIn(f"strimzi.io/cluster: {name.group(1)}", text)
        self.assertIn('strimzi.io/broker-role: "true"', text)
        self.assertIn(f"  namespace: {name.group(2)}", text)
        self.assertRegex(text, rf"(?m)^      - {re.escape(name.group(2))}$")

    def test_one_broker_loss_keeps_partitions_writable(self) -> None:
        # Killing one broker is only survivable when replicas - 1 >= min.insync.replicas.
        kafka = read(KAFKA)
        rf = int(re.search(r"default\.replication\.factor: (\d+)", kafka).group(1))  # type: ignore[union-attr]
        isr = int(re.search(r"(?m)^      min\.insync\.replicas: (\d+)", kafka).group(1))  # type: ignore[union-attr]
        self.assertGreaterEqual(rf - 1, isr)

    def test_parses_as_yaml_when_pyyaml_available(self) -> None:
        try:
            import yaml  # type: ignore[import-untyped]
        except ImportError:
            self.skipTest("PyYAML not installed; structural checks above still ran")
        for path in EXPERIMENTS:
            with self.subTest(file=path.name):
                doc = yaml.safe_load(read(path))
                self.assertEqual(doc["kind"], "PodChaos")
                self.assertEqual(doc["spec"]["mode"], "one")
                self.assertIn("labelSelectors", doc["spec"]["selector"])


if __name__ == "__main__":
    unittest.main()
