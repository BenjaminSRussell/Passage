#!/usr/bin/env python3
"""Release-notes gate for a Passage release (PSG-50, PRD 9.50.15, FR-50.12, AC-50.11).

The notes file must follow docs/ops/release-notes-template.md:

  * a title line `# Passage vX.Y.Z` (semver, pre-release suffix allowed);
  * the sections Images and charts, Features by module, Migrations, Breaking changes, CVE fixes and Rollback,
    each non-empty;
  * Images and charts names at least one `sha256:<64 hex>` digest (images are published by digest only);
  * Breaking changes is `None`: PRD 9.50.15 allows no breaking change for N-1 clients (AC-50.5). Only a release
    that deliberately breaks N-1 (a new major version) is checked with --allow-breaking;
  * no template placeholder is left (`<...>`, `vX.Y.Z`, `X.Y.Z`, `YYYY-MM-DD`, `NNNN`);
  * with --compat-evidence (the file written by deploy/multi-region/tools/compat_evidence.py), the evidence
    is for the same version, and notes that say `None` fail when the evidence reports a breaking service.

HTML comments (the template's guidance) are ignored, and lines inside fenced code blocks are not treated as
headings.

    python3 docs/ops/tools/check_release_notes.py RELEASE.md [--compat-evidence evidence.json] [--allow-breaking]

Exit status: 0 passed, 1 gate failed, 2 usage error. Stdlib only, Python 3.11+.
Tests: python3 -m unittest discover -s docs/ops/tools
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from typing import Any

TITLE_RE = re.compile(r"^# Passage v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)\s*$", re.MULTILINE)
DIGEST_RE = re.compile(r"sha256:[0-9a-f]{64}")
COMMENT_RE = re.compile(r"<!--.*?-->", re.DOTALL)
HTML_TAGS = {"br", "b", "i", "em", "strong", "code", "kbd", "sub", "sup", "details", "summary", "p"}
ANGLE_RE = re.compile(r"</?([A-Za-z][^<>\n]*)>")
PLACEHOLDER_RE = re.compile(r"(?<![A-Za-z0-9])(?:v?X\.Y\.Z|YYYY-MM-DD|NNNN)(?![A-Za-z0-9])")
REQUIRED = (
    "Images and charts",
    "Features by module",
    "Migrations",
    "Breaking changes",
    "CVE fixes",
    "Rollback",
)


def strip_comments(text: str) -> str:
    return COMMENT_RE.sub("", text)


def sections(text: str) -> dict[str, str]:
    """Map each `## ` heading to the text under it, ignoring headings inside fenced code."""
    out: dict[str, str] = {}
    current: str | None = None
    fenced = False
    for line in text.splitlines():
        if line.lstrip().startswith("```"):
            fenced = not fenced
        if not fenced and line.startswith("## "):
            current = line[3:].strip()
            out.setdefault(current, "")
            continue
        if current is not None:
            out[current] += line + "\n"
    return out


def first_line(text: str) -> str:
    for line in text.splitlines():
        if line.strip():
            return line.strip().rstrip(".").lower()
    return ""


def placeholders(text: str) -> list[str]:
    """Template placeholders still in the text, in order of appearance, without repeats."""
    found: list[str] = []
    for m in ANGLE_RE.finditer(text):
        tag = m.group(1).split()[0].lower()
        if tag in HTML_TAGS or m.group(1).startswith(("http://", "https://", "mailto:")):
            continue
        found.append(m.group(0))
    found += PLACEHOLDER_RE.findall(text)
    return list(dict.fromkeys(found))


def check(text: str, allow_breaking: bool = False, evidence: dict[str, Any] | None = None) -> list[str]:
    errors: list[str] = []
    text = strip_comments(text)
    title = TITLE_RE.search(text)
    if not title:
        errors.append("missing title line '# Passage vX.Y.Z'")
    secs = sections(text)
    for name in REQUIRED:
        if name not in secs:
            errors.append(f"missing section '## {name}'")
        elif not secs[name].strip():
            errors.append(f"empty section '## {name}'")
    if "Images and charts" in secs and not DIGEST_RE.search(secs["Images and charts"]):
        errors.append("Images and charts has no sha256 digest (images are published by digest)")
    says_none = "Breaking changes" in secs and first_line(secs["Breaking changes"]) == "none"
    if not allow_breaking and "Breaking changes" in secs and secs["Breaking changes"].strip() and not says_none:
        errors.append("Breaking changes must be 'None': no breaking change is allowed for N-1 clients (AC-50.5)")
    left = placeholders(text)
    if left:
        errors.append("template placeholders left: " + ", ".join(left))
    if evidence is not None:
        release = str(evidence.get("release", "")).lstrip("v")
        if title and release != title.group(1):
            errors.append(f"compat evidence is for {evidence.get('release')!r}, not v{title.group(1)}")
        if evidence.get("verdict") != "PASS" and says_none:
            errors.append(
                f"Breaking changes says None but the compat evidence reports "
                f"{evidence.get('breakingServices', '?')} breaking service(s)"
            )
    return errors


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("notes", type=Path, help="release notes Markdown file")
    parser.add_argument("--compat-evidence", type=Path, help="evidence JSON from compat_evidence.py for this release")
    parser.add_argument(
        "--allow-breaking", action="store_true", help="a deliberate N-1 break (new major version) lists its changes"
    )
    args = parser.parse_args(argv)
    try:
        text = args.notes.read_text(encoding="utf-8")
        evidence = None
        if args.compat_evidence is not None:
            evidence = json.loads(args.compat_evidence.read_text(encoding="utf-8"))
            if not isinstance(evidence, dict) or evidence.get("schema") != "passage.ops.compat-evidence/v1":
                raise ValueError(f"{args.compat_evidence}: not a compat evidence file")
    except (OSError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    errors = check(text, args.allow_breaking, evidence)
    for err in errors:
        print(f"FAIL  {args.notes.name}: {err}")
    print("RELEASE NOTES " + ("FAILED" if errors else "PASSED"))
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
