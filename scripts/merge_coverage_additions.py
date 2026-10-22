#!/usr/bin/env python3
"""Merge roadmap/coverage-additions.json into roadmap/waves.json (idempotent).

Each addition is appended to its wave's "deliverable" list unless an identical
bullet is already there, so re-running after editing the additions file is safe.

  python3 scripts/merge_coverage_additions.py
"""
import json
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
WAVES = ROOT / "roadmap" / "waves.json"
ADDITIONS = ROOT / "roadmap" / "coverage-additions.json"


def main():
    waves = json.loads(WAVES.read_text())
    by_n = {w["n"]: w for w in waves}
    added = skipped = 0
    for a in json.loads(ADDITIONS.read_text())["additions"]:
        bullets = by_n[a["n"]]["deliverable"]
        if a["deliverable"] in bullets:
            skipped += 1
            continue
        bullets.append(a["deliverable"])
        added += 1
    WAVES.write_text(json.dumps(waves, indent=2, ensure_ascii=False) + "\n")
    total = sum(len(w["deliverable"]) for w in waves)
    print(f"added {added}, already present {skipped}; {total} deliverable bullets across {len(waves)} waves")


if __name__ == "__main__":
    main()
