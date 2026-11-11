#!/usr/bin/env python3
"""Region pair and parameter-range checker for the multi-region values (PSG-50, BR-50.1, PRD 9.50.13).

`values.schema.json` checks one region's values at a time when Helm renders it. It cannot see the other region
of the pair, so a failover edit that leaves two PRIMARY files, or swaps MirrorMaker 2 in one file only, would
still lint. This tool reads both region files of a pair, merges each over the chart defaults the way Helm does
(mappings merge, everything else is replaced) and checks:

  pair (BR-50.1, FR-50.5)
    - exactly two region files; each file's ``region.id`` equals its file name and ``region.role`` is
      PRIMARY, PASSIVE or FENCED
    - exactly one region is PRIMARY (never two PRIMARY)
    - both files agree on the MirrorMaker 2 direction, ``mm2.sourceAlias`` is the PRIMARY and
      ``mm2.targetAlias`` is the other region
    - ``kafka.localAlias`` is the file's own region and ``kafka.remoteAlias`` is the other region
    - a PASSIVE region's own file names the primary read-write endpoint (``postgres.primary.host``); the
      chart default is not accepted, because it names one fixed region

  parameter ranges (PRD 9.50.13), on every region's merged values and on the chart defaults
    - ``failover.maxLagSeconds`` 30-3600, ``failover.dnsTtlSeconds`` 30-300,
      ``failover.twoPersonWindowMinutes`` 1-30
    - ``mm2.lagAlertMessages`` >= 1000
    - ``slo.burnFast`` > ``slo.burnSlow`` > 0 (BR-50.6)
    - ``license.warnDays`` a non-empty list of distinct integers 1-365
    - ``fence.enabled`` false in the chart defaults (a region is fenced by its FENCED role or a game day)

  cloud profile (#599), when a profile is named with --cloud-profile or by ``region.cloudProfile`` in the files
    - ``region.id`` is in the profile's ``allowedRegions`` (a region outside the list fails; a profile with no list
      allows any), so a gcc pair can only be US regions and an EU-data-boundary pair only EU ones
    - on ``provider: aks`` the backup store is ``azure-blob``, its storage account sits in the region itself, that
      region is allowed, and an explicit ``backup.destinationPath`` is an https URL on the profile's Blob suffix

Run it after every edit to the region files, including step 3 of docs/ops/runbooks/failover.md:

    python3 deploy/multi-region/tools/check_region_pair.py
    python3 deploy/multi-region/tools/check_region_pair.py --values-dir deploy/multi-region/values/us --cloud-profile gcc

Without --values-dir it checks the pair in values/ and every pair in a subdirectory of values/ (values/us).

The values files use a small YAML subset (nested mappings, scalars, flow lists, comments). The parser here
handles exactly that subset and rejects anything else (block lists, anchors, multi-line scalars), so no
third-party library is needed. Exit status: 0 no problems, 1 problems found, 2 usage or input error.
Stdlib only, Python 3.11+. Tests: python3 -m unittest discover -s deploy/multi-region/tools
"""

from __future__ import annotations

import argparse
import copy
import sys
from pathlib import Path
from typing import Any

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent.parent / "cloud-profiles"))
import cloudprofile  # noqa: E402  (the one profile table, deploy/cloud-profiles/profiles.json)

VALUES_DIR = HERE.parent / "values"
CHART_VALUES = HERE.parent / "chart" / "values.yaml"
ROLES = ("PRIMARY", "PASSIVE", "FENCED")


def _strip_comment(line: str) -> str:
    """Drop a `#` comment that starts the line or follows whitespace, outside quotes."""
    quote = ""
    for i, ch in enumerate(line):
        if quote:
            if ch == quote:
                quote = ""
        elif ch in "\"'":
            quote = ch
        elif ch == "#" and (i == 0 or line[i - 1] in " \t"):
            return line[:i]
    return line


def _scalar(text: str, number: int) -> Any:
    if text.startswith("[") or text.endswith("]"):
        if not (text.startswith("[") and text.endswith("]")):
            raise ValueError(f"line {number}: unterminated flow list")
        return [_scalar(part.strip(), number) for part in text[1:-1].split(",") if part.strip()]
    if text[0] in "&*|>{!" or text.startswith("- "):
        raise ValueError(f"line {number}: unsupported YAML {text[:1]!r}")
    if len(text) >= 2 and text[0] == text[-1] and text[0] in "\"'":
        return text[1:-1]
    if text in ("true", "false"):
        return text == "true"
    try:
        return int(text)
    except ValueError:
        pass
    try:
        return float(text)
    except ValueError:
        return text


def parse(text: str) -> dict[str, Any]:
    """Parse the indentation-based YAML subset used by the region and chart values files."""
    root: dict[str, Any] = {}
    # (indent of the key that opened the mapping, the mapping, indent its keys use once the first is seen)
    stack: list[list[Any]] = [[-1, root, None]]
    for number, raw in enumerate(text.splitlines(), start=1):
        line = _strip_comment(raw).rstrip()
        if not line.strip():
            continue
        lead = line[: len(line) - len(line.lstrip())]
        if "\t" in lead:
            raise ValueError(f"line {number}: tabs are not allowed for indentation")
        indent = len(lead)
        body = line.strip()
        if body.startswith("-"):
            raise ValueError(f"line {number}: block lists are not supported; use a flow list [a, b]")
        key, sep, rest = body.partition(":")
        if not sep or not key or (rest and not rest.startswith(" ")):
            raise ValueError(f"line {number}: expected 'key: value'")
        while stack[-1][0] >= indent:
            stack.pop()
        if stack[-1][2] is None:
            stack[-1][2] = indent
        elif stack[-1][2] != indent:
            raise ValueError(f"line {number}: indentation does not match the other keys of this mapping")
        parent = stack[-1][1]
        if key in parent:
            raise ValueError(f"line {number}: duplicate key {key!r}")
        rest = rest.strip()
        if rest == "":
            child: dict[str, Any] = {}
            parent[key] = child
            stack.append([indent, child, None])
        else:
            parent[key] = _scalar(rest, number)
    return root


def merge(base: dict[str, Any], override: dict[str, Any]) -> dict[str, Any]:
    """Helm's values merge: mappings merge key by key, any other value replaces the default."""
    out = copy.deepcopy(base)
    for key, value in override.items():
        if isinstance(value, dict) and isinstance(out.get(key), dict):
            out[key] = merge(out[key], value)
        else:
            out[key] = copy.deepcopy(value)
    return out


def _get(doc: dict[str, Any], dotted: str) -> Any:
    cur: Any = doc
    for part in dotted.split("."):
        if not isinstance(cur, dict):
            return None
        cur = cur.get(part)
    return cur


def _int_in(value: Any, low: int, high: int | None = None) -> bool:
    return isinstance(value, int) and not isinstance(value, bool) and low <= value and (high is None or value <= high)


def check_pair(files: dict[str, dict[str, Any]], chart: dict[str, Any] | None = None) -> list[str]:
    """Problems with the region pair. ``files`` maps region id (file stem) to the region file as written."""
    if len(files) != 2:
        return [f"a region pair needs exactly two region files, found {len(files)}: {', '.join(sorted(files)) or '-'}"]
    merged = {name: merge(chart or {}, doc) for name, doc in files.items()}
    problems: list[str] = []
    roles: dict[str, Any] = {}
    for name, doc in sorted(merged.items()):
        if _get(doc, "region.id") != name:
            problems.append(f"{name}: region.id is {_get(doc, 'region.id')!r}, expected the file name {name!r}")
        role = _get(doc, "region.role")
        if role not in ROLES:
            problems.append(f"{name}: region.role {role!r} is not one of {', '.join(ROLES)}")
        roles[name] = role

    primaries = sorted(n for n, r in roles.items() if r == "PRIMARY")
    if len(primaries) != 1:
        problems.append(f"exactly one PRIMARY is required across the pair, found {len(primaries)}")

    for name, doc in sorted(merged.items()):
        (other,) = set(files) - {name}
        if _get(doc, "kafka.localAlias") != name:
            problems.append(f"{name}: kafka.localAlias is {_get(doc, 'kafka.localAlias')!r}, expected {name!r}")
        if _get(doc, "kafka.remoteAlias") != other:
            problems.append(f"{name}: kafka.remoteAlias is {_get(doc, 'kafka.remoteAlias')!r}, expected {other!r}")
        if roles[name] == "PASSIVE" and not _get(files[name], "postgres.primary.host"):
            problems.append(f"{name}: a PASSIVE region file must name postgres.primary.host (the PRIMARY's endpoint)")

    directions = {name: (_get(d, "mm2.sourceAlias"), _get(d, "mm2.targetAlias")) for name, d in merged.items()}
    if len(set(directions.values())) > 1:
        shown = "; ".join(f"{n}: {s} -> {t}" for n, (s, t) in sorted(directions.items()))
        problems.append(f"region files disagree on the MirrorMaker 2 direction ({shown})")
    elif len(primaries) == 1:
        source, target = next(iter(directions.values()))
        (passive,) = set(files) - {primaries[0]}
        if (source, target) != (primaries[0], passive):
            problems.append(
                f"MirrorMaker 2 must replicate from the PRIMARY {primaries[0]} to {passive}, got {source} -> {target}"
            )
    return problems


def check_ranges(values: dict[str, Any], label: str = "chart defaults") -> list[str]:
    """Problems with the PRD 9.50.13 parameter ranges in one set of values."""
    problems: list[str] = []
    for key, low, high in (
        ("failover.maxLagSeconds", 30, 3600),
        ("failover.dnsTtlSeconds", 30, 300),
        ("failover.twoPersonWindowMinutes", 1, 30),
    ):
        if not _int_in(_get(values, key), low, high):
            problems.append(f"{label}: {key} must be an integer in {low}-{high}, got {_get(values, key)!r}")

    lag = _get(values, "mm2.lagAlertMessages")
    if not _int_in(lag, 1000):
        problems.append(f"{label}: mm2.lagAlertMessages must be an integer >= 1000, got {lag!r}")

    fast, slow = _get(values, "slo.burnFast"), _get(values, "slo.burnSlow")
    numeric = all(isinstance(v, (int, float)) and not isinstance(v, bool) for v in (fast, slow))
    if not numeric or not 0 < slow < fast:
        problems.append(f"{label}: slo.burnFast must exceed slo.burnSlow > 0, got {fast!r} and {slow!r}")

    days = _get(values, "license.warnDays")
    if not isinstance(days, list) or not days or not all(_int_in(d, 1, 365) for d in days):
        problems.append(f"{label}: license.warnDays must be a non-empty list of integers 1-365, got {days!r}")
    elif len(set(days)) != len(days):
        problems.append(f"{label}: license.warnDays has a repeated day, got {days!r}")
    return problems


def check(files: dict[str, dict[str, Any]], chart: dict[str, Any]) -> list[str]:
    """Every problem: the pair, the defaults' ranges and fence, and each region's merged ranges."""
    problems = check_pair(files, chart) + check_ranges(chart)
    if _get(chart, "fence.enabled") is not False:
        problems.append("chart defaults: fence.enabled must be false; a region is fenced by its FENCED role or a game day")
    for name, doc in sorted(files.items()):
        problems += check_ranges(merge(chart, doc), name)
    return problems


def check_cloud_profile(files: dict[str, dict[str, Any]], chart: dict[str, Any], selector: str | None = None) -> list[str]:
    """Problems with the pair against a cloud profile; the profile is `selector` or the files' region.cloudProfile."""
    declared = {name: _get(doc, "region.cloudProfile") for name, doc in files.items()}
    named = {v for v in declared.values() if v}
    if len(named) > 1:
        return [f"region files name different cloud profiles: {declared}"]
    selector = selector or (named.pop() if named else None)
    if not selector:
        return []
    base, overlays = cloudprofile.parse_selector(selector)
    try:
        profile = cloudprofile.resolve(cloudprofile.load(), base, overlays)
    except ValueError as exc:
        return [f"cloud profile {selector!r}: {exc}"]
    problems: list[str] = []
    allowed = profile["allowedRegions"]
    blob = profile["endpoints"]["blobSuffix"]
    for name, doc in sorted({n: merge(chart, d) for n, d in files.items()}.items()):
        if allowed and name not in allowed:
            problems.append(f"{name}: region is not in the {selector} allowedRegions ({', '.join(allowed)})")
        if _get(doc, "region.provider") != "aks":
            continue
        if _get(doc, "backup.store") != "azure-blob":
            problems.append(f"{name}: provider aks needs backup.store azure-blob, got {_get(doc, 'backup.store')!r}")
            continue
        account_region = _get(doc, "backup.azure.storageAccountRegion")
        if account_region != name:
            problems.append(f"{name}: backup.azure.storageAccountRegion is {account_region!r}, expected the region itself ({name!r})")
        if allowed and account_region not in allowed:
            problems.append(f"{name}: backup storage region {account_region!r} is not in the {selector} allowedRegions")
        if not _get(doc, "backup.azure.storageAccount"):
            problems.append(f"{name}: backup.azure.storageAccount is not set")
        destination = _get(doc, "backup.destinationPath")
        if destination:
            host = cloudprofile.host_of(destination)
            if not destination.startswith("https://") or not host.endswith("." + blob):
                problems.append(f"{name}: backup.destinationPath {destination!r} must be an https URL on *.{blob}")
    return problems


def load_pair(values_dir: Path) -> dict[str, dict[str, Any]]:
    out: dict[str, dict[str, Any]] = {}
    for path in sorted(values_dir.glob("*.yaml")):
        try:
            out[path.stem] = parse(path.read_text(encoding="utf-8"))
        except ValueError as exc:
            raise ValueError(f"{path.name}: {exc}") from exc
    return out


def discover_pairs(values_dir: Path) -> dict[str, Path]:
    """The pair in `values_dir` itself plus one pair per subdirectory that holds region files."""
    pairs: dict[str, Path] = {}
    if any(values_dir.glob("*.yaml")):
        pairs[values_dir.name] = values_dir
    for sub in sorted(p for p in values_dir.iterdir() if p.is_dir() and any(p.glob("*.yaml"))):
        pairs[sub.name] = sub
    return pairs


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--values-dir", type=Path, default=None, help="directory holding one pair's two files (default: values/ and each subdirectory of it)")
    parser.add_argument("--cloud-profile", default=None, help="check the pair against a cloud profile selector, for example gcc or commercial+eu-data-boundary")
    parser.add_argument("--chart-values", type=Path, default=CHART_VALUES, help="the chart's default values.yaml")
    args = parser.parse_args(argv)

    try:
        chart = parse(args.chart_values.read_text(encoding="utf-8"))
        if args.values_dir is None:
            dirs = discover_pairs(VALUES_DIR)
        else:
            dirs = {args.values_dir.name: args.values_dir}
        pairs = {name: load_pair(path) for name, path in dirs.items()}
    except (OSError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    total = 0
    for pair_name, files in pairs.items():
        problems = check(files, chart) + check_cloud_profile(files, chart, args.cloud_profile)
        for problem in problems:
            print(f"FAIL  {pair_name + ': ' if len(pairs) > 1 else ''}{problem}")
        total += len(problems)
    if total:
        print(f"REGION PAIR FAILED: {total} problem(s)")
        return 1
    shown = "; ".join(
        (f"{name}: " if len(pairs) > 1 else "") + ", ".join(sorted(files)) for name, files in pairs.items()
    )
    suffix = f"; cloud profile {args.cloud_profile}" if args.cloud_profile else ""
    print(f"REGION PAIR PASSED: {shown}; parameters within PRD 9.50.13 ranges{suffix}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
