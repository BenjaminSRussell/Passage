#!/usr/bin/env python3
"""Release N / N-1 compatibility evidence for every service API (PSG-50, FR-50.7, AC-50.5).

The release pipeline exports each service's OpenAPI document as JSON into two directories: one for the
previous release (N-1) and one for the release being cut (N). This runner pairs the files by name
(`<service>.json`), runs openapi_compat.compare on each pair, and writes one evidence file for the release.

  compatible   every N-1 client call still works against N
  breaking     at least one breaking change from openapi_compat, or a service that N-1 published is no
               longer published (reported as service-removed); the release gate fails
  new          the service is new in N; nothing to compare

Warnings from openapi_compat (new response enum values N-1 clients must tolerate) are kept per service in
``warnings`` and printed, so the release manager can check the N-1 web client; they do not fail the gate.

The evidence file (schema passage.ops.compat-evidence/v1) is kept with the release notes as the N/N-1
record. Exit status: 0 every service compatible, 1 at least one breaking service, 2 usage or input error.

    python3 deploy/multi-region/tools/compat_evidence.py --previous n1/ --current n/ \\
        --release v1.0.0 --commit <sha> --out compat-evidence-v1.0.0.json

Stdlib only, Python 3.11+. Tests: python3 -m unittest discover -s deploy/multi-region/tools
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

from openapi_compat import check, load_doc

SCHEMA = "passage.ops.compat-evidence/v1"


def evaluate(previous: Path, current: Path) -> list[dict[str, Any]]:
    """One result per service seen in either directory, sorted by service name."""
    prev = {p.name: p for p in previous.glob("*.json")}
    cur = {p.name: p for p in current.glob("*.json")}
    results: list[dict[str, Any]] = []
    for name in sorted(set(prev) | set(cur)):
        service = name[: -len(".json")]
        if name not in cur:
            results.append(
                {"service": service, "status": "breaking", "changes": ["service-removed: no longer published"], "warnings": []}
            )
        elif name not in prev:
            load_doc(cur[name])  # a new service's document must still be a readable OpenAPI file
            results.append({"service": service, "status": "new", "changes": [], "warnings": []})
        else:
            report = check(load_doc(prev[name]), load_doc(cur[name]))
            changes = [str(c) for c in report.breaking]
            results.append(
                {
                    "service": service,
                    "status": "breaking" if changes else "compatible",
                    "changes": changes,
                    "warnings": [str(c) for c in report.warnings],
                }
            )
    return results


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--previous", type=Path, required=True, help="directory of N-1 OpenAPI JSON files")
    parser.add_argument("--current", type=Path, required=True, help="directory of N OpenAPI JSON files")
    parser.add_argument("--release", required=True, help="release being cut, for example v1.0.0")
    parser.add_argument("--commit", required=True, help="git commit of the release")
    parser.add_argument("--out", type=Path, required=True, help="evidence file to write")
    args = parser.parse_args(argv)

    for directory in (args.previous, args.current):
        if not directory.is_dir():
            print(f"error: {directory} is not a directory", file=sys.stderr)
            return 2
    try:
        results = evaluate(args.previous, args.current)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    if not any(r["status"] in ("compatible", "breaking") for r in results):
        print("error: no service present in both directories; nothing to compare", file=sys.stderr)
        return 2

    breaking = [r for r in results if r["status"] == "breaking"]
    evidence = {
        "schema": SCHEMA,
        "release": args.release,
        "commit": args.commit,
        "services": results,
        "breakingServices": len(breaking),
        "verdict": "FAIL" if breaking else "PASS",
    }
    try:
        args.out.write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
    except OSError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    for r in results:
        for warning in r["warnings"]:
            print(f"WARN {r['service']}: {warning} (N-1 clients must tolerate the new value)")
    for r in breaking:
        for change in r["changes"]:
            print(f"BREAKING {r['service']}: {change}")
    print(f"compat evidence {args.release}: {evidence['verdict']} ({len(results)} services, {len(breaking)} breaking)")
    return 1 if breaking else 0


if __name__ == "__main__":
    sys.exit(main())
