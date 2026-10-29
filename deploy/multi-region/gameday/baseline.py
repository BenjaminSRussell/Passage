#!/usr/bin/env python3
"""Game-day baseline manifest and post-failover verifier (PSG-50, FR-50.6 steps 2 and 5, fx-gameday).

Two commands:

    python3 baseline.py capture --input facts.json --out baseline.json
        Build a baseline manifest from facts gathered before failure injection: ledger chain head
        per tenant, row counts per domain, Kafka end offsets per topic partition, and fiscal
        sequence ranges. The manifest carries a sha256 over its canonical body and the command
        prints it; record that digest in the game-day ticket. Anyone can recompute the digest of
        an edited file, so it proves nothing unless verify is given the recorded value.

    python3 baseline.py verify --baseline baseline.json --observed post.json \
            --expect-digest sha256:... [--report report.json]
        Compare the post-failover facts with the baseline. Exit 0 only when every check passes.

Checks (all must pass):
  * ledger: the observed chain sequence is >= the baseline sequence, and the observed hash at the
    baseline sequence equals the baseline head. This proves the chain was extended, not replaced
    (BR-50.3). Observed facts must carry ``checkpoints`` mapping sequence -> hash for the baseline
    sequence.
  * rows: observed count >= baseline count for every domain (committed rows cannot disappear).
  * offsets: observed end offset >= baseline end offset for every topic partition. MirrorMaker 2
    (IdentityReplicationPolicy) keeps topic names and partitions but not offsets: a mirrored
    partition's offsets differ from its source's. Both sides must therefore be read from the same
    cluster, the standby that will be promoted, and facts name it in ``offsetsCluster``; verify
    fails when the two names differ. A missing partition fails.
  * sequences: every fiscal sequence series is contiguous (no gap, no duplicate) in the observed
    facts and covers the whole baseline range first..last, so no number issued before the
    failure was lost or reissued (BR-50.4). A baseline series missing from the facts fails.

Stdlib only, Python 3.11+. Facts are plain JSON; this tool never talks to a cluster. The
collector that produces facts is a runbook step, not part of this file.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

SCHEMA = "passage.gameday.baseline/v1"


class FactsError(ValueError):
    """Facts or a manifest are malformed. Distinct from a failed check."""


@dataclass
class Check:
    name: str
    passed: bool
    detail: str


@dataclass
class Report:
    checks: list[Check] = field(default_factory=list)

    @property
    def passed(self) -> bool:
        return bool(self.checks) and all(c.passed for c in self.checks)

    def add(self, name: str, passed: bool, detail: str) -> None:
        self.checks.append(Check(name, passed, detail))


def canonical(obj: Any) -> bytes:
    return json.dumps(obj, sort_keys=True, separators=(",", ":")).encode("utf-8")


def digest(body: dict[str, Any]) -> str:
    return "sha256:" + hashlib.sha256(canonical(body)).hexdigest()


def _require(obj: Any, key: str, kind: type, where: str) -> Any:
    if not isinstance(obj, dict) or key not in obj:
        raise FactsError(f"{where}: missing '{key}'")
    value = obj[key]
    if not isinstance(value, kind) or isinstance(value, bool):
        raise FactsError(f"{where}: '{key}' must be {kind.__name__}")
    return value


def validate_facts(facts: dict[str, Any]) -> None:
    """Shape check only. Raises FactsError; semantic checks happen in verify()."""
    if not isinstance(facts, dict):
        raise FactsError("facts: top level must be an object")
    cluster = _require(facts, "offsetsCluster", str, "facts")
    if not cluster.strip():
        raise FactsError("facts: 'offsetsCluster' must name the Kafka cluster the offsets were read from")
    ledger = _require(facts, "ledger", dict, "facts")
    for tenant, entry in ledger.items():
        _require(entry, "seq", int, f"ledger[{tenant}]")
        _require(entry, "head", str, f"ledger[{tenant}]")
        checkpoints = entry.get("checkpoints", {})
        if not isinstance(checkpoints, dict) or not all(
            isinstance(k, str) and isinstance(v, str) for k, v in checkpoints.items()
        ):
            raise FactsError(f"ledger[{tenant}]: 'checkpoints' must map sequence strings to hashes")
    rows = _require(facts, "rows", dict, "facts")
    for domain, count in rows.items():
        if not isinstance(count, int) or isinstance(count, bool) or count < 0:
            raise FactsError(f"rows[{domain}]: must be a non-negative integer")
    offsets = _require(facts, "offsets", dict, "facts")
    for tp, off in offsets.items():
        if not isinstance(off, int) or isinstance(off, bool) or off < 0:
            raise FactsError(f"offsets[{tp}]: must be a non-negative integer")
    seqs = _require(facts, "sequences", dict, "facts")
    for series, values in seqs.items():
        if not isinstance(values, list) or not values:
            raise FactsError(f"sequences[{series}]: must be a non-empty list")
        if not all(isinstance(v, int) and not isinstance(v, bool) for v in values):
            raise FactsError(f"sequences[{series}]: values must be integers")


def capture(facts: dict[str, Any]) -> dict[str, Any]:
    validate_facts(facts)
    body = {
        "schema": SCHEMA,
        "offsetsCluster": facts["offsetsCluster"],
        "ledger": {t: {"seq": e["seq"], "head": e["head"]} for t, e in sorted(facts["ledger"].items())},
        "rows": dict(sorted(facts["rows"].items())),
        "offsets": dict(sorted(facts["offsets"].items())),
        "sequences": {s: {"first": v[0], "last": v[-1]} for s, v in sorted(facts["sequences"].items())},
    }
    body["digest"] = digest({k: v for k, v in body.items() if k != "digest"})
    return body


def check_manifest(manifest: dict[str, Any]) -> None:
    if manifest.get("schema") != SCHEMA:
        raise FactsError("baseline: unknown schema")
    expected = digest({k: v for k, v in manifest.items() if k != "digest"})
    if manifest.get("digest") != expected:
        raise FactsError("baseline: digest does not match body (manifest was edited)")


def verify(baseline: dict[str, Any], facts: dict[str, Any], expect_digest: str | None = None) -> Report:
    check_manifest(baseline)
    if expect_digest is not None and baseline["digest"] != expect_digest:
        raise FactsError("baseline: digest differs from the recorded one (manifest was replaced)")
    validate_facts(facts)
    report = Report()

    # Offsets are only comparable within one cluster's offset space.
    same = facts["offsetsCluster"] == baseline.get("offsetsCluster")
    report.add(
        "offsets cluster",
        same,
        f"offsets read from {facts['offsetsCluster']}"
        + ("" if same else f", baseline from {baseline.get('offsetsCluster')}"),
    )

    # Ledger chain (BR-50.3): extended, not replaced.
    checkpoints = facts["ledger"]
    for tenant, base in baseline["ledger"].items():
        obs = checkpoints.get(tenant)
        name = f"ledger {tenant}"
        if obs is None:
            report.add(name, False, "tenant missing from observed facts")
            continue
        if obs["seq"] < base["seq"]:
            report.add(name, False, f"chain shortened: observed {obs['seq']} < baseline {base['seq']}")
            continue
        at_base = obs.get("checkpoints", {}).get(str(base["seq"]))
        if at_base != base["head"]:
            report.add(name, False, f"hash at baseline seq {base['seq']} does not match baseline head")
            continue
        report.add(name, True, f"chain extended from seq {base['seq']} to {obs['seq']}")

    # Row counts: committed rows never disappear.
    for domain, count in baseline["rows"].items():
        obs = facts["rows"].get(domain)
        if obs is None:
            report.add(f"rows {domain}", False, "domain missing from observed facts")
        elif obs < count:
            report.add(f"rows {domain}", False, f"observed {obs} < baseline {count}")
        else:
            report.add(f"rows {domain}", True, f"observed {obs} >= baseline {count}")

    # Kafka end offsets (MM2 translated names).
    for tp, base_off in baseline["offsets"].items():
        obs = facts["offsets"].get(tp)
        if obs is None:
            report.add(f"offset {tp}", False, "partition missing after translation")
        elif obs < base_off:
            report.add(f"offset {tp}", False, f"observed {obs} < baseline {base_off}")
        else:
            report.add(f"offset {tp}", True, f"observed {obs} >= baseline {base_off}")

    # Fiscal sequences (BR-50.4): contiguous, and every number issued before the failure is still there.
    for series, values in facts["sequences"].items():
        gaps = [(a, b) for a, b in zip(values, values[1:]) if b != a + 1]
        base = baseline["sequences"].get(series)
        if gaps:
            report.add(f"sequence {series}", False, f"gap or duplicate between {gaps[0][0]} and {gaps[0][1]}")
        elif base is not None and not (values[0] <= base["first"] and base["last"] <= values[-1]):
            report.add(
                f"sequence {series}",
                False,
                f"observed {values[0]}..{values[-1]} does not cover baseline {base['first']}..{base['last']}",
            )
        else:
            report.add(f"sequence {series}", True, f"contiguous {values[0]}..{values[-1]}")
    for series in baseline["sequences"]:
        if series not in facts["sequences"]:
            report.add(f"sequence {series}", False, "series missing from observed facts")
    return report


def render(report: Report) -> str:
    lines = [f"{'PASS' if c.passed else 'FAIL'}  {c.name}: {c.detail}" for c in report.checks]
    lines.append("GAME-DAY VERIFICATION " + ("PASSED" if report.passed else "FAILED"))
    return "\n".join(lines)


def _load(path: Path) -> dict[str, Any]:
    with path.open(encoding="utf-8") as fh:
        data = json.load(fh)
    if not isinstance(data, dict):
        raise FactsError(f"{path}: top level must be an object")
    return data


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="cmd", required=True)
    cap = sub.add_parser("capture", help="write a baseline manifest from facts")
    cap.add_argument("--input", type=Path, required=True)
    cap.add_argument("--out", type=Path, required=True)
    ver = sub.add_parser("verify", help="compare observed facts with a baseline")
    ver.add_argument("--baseline", type=Path, required=True)
    ver.add_argument("--observed", type=Path, required=True)
    ver.add_argument(
        "--expect-digest",
        required=True,
        help="digest printed by capture and recorded in the game-day ticket",
    )
    ver.add_argument("--report", type=Path, help="write the verification report as JSON")
    args = parser.parse_args(argv)

    try:
        if args.cmd == "capture":
            manifest = capture(_load(args.input))
            args.out.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            print(f"baseline written: {manifest['digest']}")
            return 0
        report = verify(_load(args.baseline), _load(args.observed), args.expect_digest)
    except (OSError, json.JSONDecodeError, FactsError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    print(render(report))
    if args.report:
        payload = {
            "passed": report.passed,
            "checks": [c.__dict__ for c in report.checks],
        }
        args.report.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    return 0 if report.passed else 1


if __name__ == "__main__":
    sys.exit(main())
