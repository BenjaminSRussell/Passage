#!/usr/bin/env python3
"""Docs link and runbook checker for docs/ops (PSG-50, PRD 9.50.9 docs row, FR-50.10).

Four checks, all must pass (exit 1 otherwise):
  1. Markdown links: every relative link under docs/ops resolves to a file, and every #anchor resolves to
     a heading in the target file (GitHub slug rules). External http(s)/mailto links are not fetched.
  2. Alert runbooks: every alert in the multi-region PrometheusRule carries a runbook_url, each URL points
     into https://docs.passage.dev/ops/runbooks/ and the page exists in docs/ops/runbooks/ (FR-50.10).
  3. Runbook directory hygiene: docs/ops/runbooks holds only .md pages (stray files fail).
  4. Repository paths: every `services/...` or `apps/...` path written in backticks in prose names a file or
     directory that exists in the repository, so a guide cannot point readers at a service that is not there.

    python3 docs/ops/tools/check_docs.py [--root docs/ops] [--rules deploy/multi-region/chart/templates/prometheusrule.yaml]

The site build and CI wiring are a handoff to deployment-cicd; this tool is what that gate will run.
Stdlib only, Python 3.11+. Tests: python3 -m unittest discover -s docs/ops/tools
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

RUNBOOK_PREFIX = "https://docs.passage.dev/ops/runbooks/"
LINK_RE = re.compile(r"\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
HEADING_RE = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")
ALERT_RE = re.compile(r"^\s*- alert:\s*(\S+)", re.MULTILINE)
URL_RE = re.compile(r"runbook_url:\s*(\S+)")
EXTERNAL = ("http://", "https://", "mailto:", "<")
REPO_PATH_RE = re.compile(r"`((?:services|apps)/[A-Za-z0-9_.\-/]+)`")
ROOT = Path(__file__).resolve().parents[3]


def _prose_lines(text: str) -> list[str]:
    """Lines outside fenced code blocks, so example links in code are not checked."""
    out: list[str] = []
    fenced = False
    for line in text.splitlines():
        if line.lstrip().startswith("```"):
            fenced = not fenced
            continue
        if not fenced:
            out.append(line)
    return out


def slugify(heading: str) -> str:
    """GitHub-style anchor: lowercase, drop punctuation except hyphen and underscore, spaces to hyphens."""
    text = heading.strip().lower()
    text = re.sub(r"[^\w\- ]", "", text)
    return text.replace(" ", "-")


def headings(text: str) -> set[str]:
    slugs: set[str] = set()
    for line in _prose_lines(text):
        m = HEADING_RE.match(line)
        if m:
            slugs.add(slugify(m.group(2)))
    return slugs


def links(text: str) -> list[str]:
    found: list[str] = []
    for line in _prose_lines(text):
        found.extend(LINK_RE.findall(line))
    return found


def check_markdown(root: Path) -> list[str]:
    errors: list[str] = []
    for md in sorted(root.rglob("*.md")):
        rel = md.relative_to(root)
        for target in links(md.read_text(encoding="utf-8")):
            if target.startswith(EXTERNAL):
                continue
            path_part, _, anchor = target.partition("#")
            dest = md if path_part == "" else (md.parent / path_part)
            if not dest.exists():
                errors.append(f"{rel}: broken link {target}")
                continue
            if anchor and dest.suffix == ".md":
                if anchor.lower() not in headings(dest.read_text(encoding="utf-8")):
                    errors.append(f"{rel}: missing anchor #{anchor} in {target.split('#')[0] or md.name}")
    return errors


def check_alerts(rules: Path, runbooks: Path) -> list[str]:
    text = rules.read_text(encoding="utf-8")
    alerts = ALERT_RE.findall(text)
    urls = URL_RE.findall(text)
    errors: list[str] = []
    if len(alerts) != len(urls):
        errors.append(f"{rules.name}: {len(alerts)} alerts but {len(urls)} runbook_url entries")
    for url in urls:
        if not url.startswith(RUNBOOK_PREFIX):
            errors.append(f"{rules.name}: runbook_url outside the runbook site: {url}")
            continue
        slug = url[len(RUNBOOK_PREFIX):]
        if not (runbooks / f"{slug}.md").is_file():
            errors.append(f"{rules.name}: runbook page missing for {url}")
    return errors


def check_runbook_dir(runbooks: Path) -> list[str]:
    return [f"runbooks/{p.name}: stray file, only .md pages belong here" for p in sorted(runbooks.iterdir())
            if p.is_file() and p.suffix != ".md"]


def check_repo_paths(root: Path, repo: Path) -> list[str]:
    errors: list[str] = []
    for md in sorted(root.rglob("*.md")):
        rel = md.relative_to(root)
        for line in _prose_lines(md.read_text(encoding="utf-8")):
            for path in REPO_PATH_RE.findall(line):
                if not (repo / path.rstrip("/.")).exists():
                    errors.append(f"{rel}: repository path {path} does not exist")
    return errors


def run(root: Path, rules: Path, repo: Path = ROOT) -> list[str]:
    runbooks = root / "runbooks"
    return (check_markdown(root) + check_alerts(rules, runbooks) + check_runbook_dir(runbooks)
            + check_repo_paths(root, repo))


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", type=Path, default=ROOT / "docs" / "ops")
    parser.add_argument(
        "--rules", type=Path, default=ROOT / "deploy" / "multi-region" / "chart" / "templates" / "prometheusrule.yaml"
    )
    args = parser.parse_args(argv)
    errors = run(args.root, args.rules)
    for err in errors:
        print(f"FAIL  {err}")
    print("DOCS CHECK " + ("FAILED" if errors else "PASSED"))
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
