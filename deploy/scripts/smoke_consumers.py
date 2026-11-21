#!/usr/bin/env python3
"""Live smoke test of the consumers dev turns on (D34, B-05b): one event consumed per consumer, on a dev cluster (#213).

    python3 deploy/scripts/smoke_consumers.py --dry-run          # print the plan and the temporary KafkaUser, no cluster
    python3 deploy/scripts/smoke_consumers.py [--context dev]    # run it with kubectl against the dev cluster
    make -C deploy smoke-consumers

The consumers, their groups and topics come from deploy/scripts/test_kafka_consumers.py (derived from the code and the
dev values), so the smoke test follows whatever dev turns on. For each consumer that is on it picks one topic per group,
then:
1. applies a temporary KafkaUser `consumer-smoke` in namespace kafka: Write and Describe on exactly those topics,
   Describe on those groups (to read their committed offsets), nothing else;
2. starts a pod with the broker's own image, the user's Secret and the cluster CA, signed in over the SCRAM-SHA-512
   TLS listener;
3. per (group, topic): reads the group's committed offsets, produces one CloudEvent of type dev.depot.smoke.ping.v1
   (no consumer handles it, so it is skipped or dead-lettered by design: the check is that it was consumed, not that a
   handler ran) and waits until the group's committed offsets on that topic have moved past it;
4. deletes the pod and the user (--keep leaves them).
It exits 1 when a group did not consume its event within --timeout seconds. Not wired into CI: it needs a dev cluster
with the event backbone, the consumers' databases and their credentials (#213), and it has not been run yet.
"""

import argparse
import json
import subprocess
import sys
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parent))
import test_kafka_consumers as kc  # noqa: E402

NAMESPACE = "kafka"
USER = "consumer-smoke"
CLUSTER = "depot"
BOOTSTRAP = "depot-kafka-bootstrap:9093"
CLIENT_PROPERTIES = """security.protocol=SASL_SSL
sasl.mechanism=SCRAM-SHA-512
ssl.truststore.type=PEM
ssl.truststore.location=/var/run/smoke/ca/ca.crt
"""


def plan():
    """[(KafkaUser, group, topic)] for every consumer group dev turns on, one topic each."""
    kc.KafkaConsumersTest.setUpClass()
    t = kc.KafkaConsumersTest
    out = []
    for b, _, _, values in t.dev_blocks(t):
        if not t.enabled(b, values):
            continue
        for group, topics in sorted(t.expected[b].pairs):
            if isinstance(group, kc.Prefix):
                continue  # a per-pod group (pricing): no fixed name to read offsets for
            out.append((b.data["username"], str(group), topics[0]))
    return out


def smoke_user(rows):
    acls = [{"resource": {"type": "topic", "name": t, "patternType": "literal"}, "operations": ["Write", "Describe"]}
            for t in sorted({t for _, _, t in rows})]
    acls += [{"resource": {"type": "group", "name": g, "patternType": "literal"}, "operations": ["Describe"]}
             for g in sorted({g for _, g, _ in rows})]
    return {"apiVersion": "kafka.strimzi.io/v1beta2", "kind": "KafkaUser",
            "metadata": {"name": USER, "namespace": NAMESPACE, "labels": {"strimzi.io/cluster": CLUSTER}},
            "spec": {"authentication": {"type": "scram-sha-512"}, "authorization": {"type": "simple", "acls": acls}}}


def smoke_pod(image):
    return {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": USER, "namespace": NAMESPACE},
            "spec": {"restartPolicy": "Never",
                     "securityContext": {"runAsNonRoot": True, "seccompProfile": {"type": "RuntimeDefault"}},
                     "containers": [{"name": "client", "image": image, "command": ["sleep", "1800"],
                                     "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}},
                                     "volumeMounts": [{"name": "user", "mountPath": "/var/run/smoke/user", "readOnly": True},
                                                      {"name": "ca", "mountPath": "/var/run/smoke/ca", "readOnly": True}]}],
                     "volumes": [{"name": "user", "secret": {"secretName": USER}},
                                 {"name": "ca", "secret": {"secretName": kc.CLUSTER_CA_SECRET}}]}}


class Kube:
    def __init__(self, context):
        self.base = ["kubectl"] + (["--context", context] if context else []) + ["-n", NAMESPACE]

    def run(self, *args, stdin=None, check=True):
        res = subprocess.run(self.base + list(args), input=stdin, capture_output=True, text=True)
        if check and res.returncode:
            raise RuntimeError(f"kubectl {' '.join(args)}: {res.stderr.strip()}")
        return res.stdout

    def apply(self, doc):
        self.run("apply", "-f", "-", stdin=yaml.safe_dump(doc))

    def sh(self, script, stdin=None):
        return self.run("exec", "-i", USER, "--", "sh", "-c", script, stdin=stdin)


def committed(kube, group, topic):
    """Sum of the group's committed offsets on topic (partitions with no commit count as 0)."""
    out = kube.sh(f"bin/kafka-consumer-groups.sh --bootstrap-server {BOOTSTRAP} --command-config /tmp/client.properties "
                  f"--describe --group {group} 2>/dev/null || true")
    total = 0
    for line in out.splitlines():
        cols = line.split()
        if len(cols) >= 4 and cols[1] == topic and cols[3].isdigit():
            total += int(cols[3])
    return total


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--context", help="kubectl context of the dev cluster")
    p.add_argument("--timeout", type=int, default=300, help="seconds to wait for each group")
    p.add_argument("--dry-run", action="store_true", help="print the plan and the temporary KafkaUser, touch no cluster")
    p.add_argument("--keep", action="store_true", help="leave the smoke pod and user in place")
    args = p.parse_args(argv)
    rows = plan()
    if not rows:
        print("no consumer is on in dev", file=sys.stderr)
        return 1
    for user, group, topic in rows:
        print(f"{user}: group {group} <- {topic}")
    if args.dry_run:
        print("---\n" + yaml.safe_dump(smoke_user(rows), sort_keys=False))
        return 0
    if not args.context:
        print("refusing to run: pass --context <dev cluster context>; the current kubectl context may be another cluster",
              file=sys.stderr)
        return 2
    kube = Kube(args.context)
    failed = []
    try:
        kube.apply(smoke_user(rows))
        for _ in range(60):
            if kube.run("get", "secret", USER, "--ignore-not-found", "-o", "name"):
                break
            time.sleep(2)
        else:
            raise RuntimeError(f"the User Operator wrote no Secret {USER}")
        image = kube.run("get", "pods", "-l", f"strimzi.io/cluster={CLUSTER},strimzi.io/kind=Kafka",
                         "-o", "jsonpath={.items[0].spec.containers[0].image}")
        kube.apply(smoke_pod(image))
        kube.run("wait", "--for=condition=Ready", f"pod/{USER}", "--timeout=180s")
        kube.sh("cat > /tmp/client.properties && printf 'sasl.jaas.config=%s\\n' \"$(cat /var/run/smoke/user/sasl.jaas.config)\" "
                ">> /tmp/client.properties", stdin=CLIENT_PROPERTIES)
        for user, group, topic in rows:
            before = committed(kube, group, topic)
            event = {"specversion": "1.0", "id": str(uuid.uuid4()), "source": "/deploy/scripts/smoke_consumers",
                     "type": "dev.depot.smoke.ping.v1", "time": datetime.now(timezone.utc).isoformat(),
                     "datacontenttype": "application/json", "data": {"consumer": user}}
            try:
                kube.sh(f"bin/kafka-console-producer.sh --bootstrap-server {BOOTSTRAP} --producer.config /tmp/client.properties "
                        f"--topic {topic}", stdin=json.dumps(event) + "\n")
            except RuntimeError as err:
                # A topic that does not exist yet or a refused write fails this row only; the others still run.
                print(f"FAIL {user}: group {group} on {topic}: could not produce: {err}")
                failed.append((user, group, topic))
                continue
            deadline = time.time() + args.timeout
            while time.time() < deadline and committed(kube, group, topic) <= before:
                time.sleep(5)
            after = committed(kube, group, topic)
            ok = after > before
            print(f"{'ok  ' if ok else 'FAIL'} {user}: group {group} on {topic}: committed {before} -> {after}")
            if not ok:
                failed.append((user, group, topic))
    finally:
        if not args.keep:
            kube.run("delete", "pod", USER, "--ignore-not-found", "--wait=false", check=False)
            kube.run("delete", "kafkauser", USER, "--ignore-not-found", check=False)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
