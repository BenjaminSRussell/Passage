#!/usr/bin/env python3
"""Residency evidence report: tenant -> regions for the database, object store, Kafka and backups (#605).

An auditor asks "where does this tenant's data live, and does that stay inside the residency the tenant was sold?".
This tool answers from two inputs and one table, and flags every tenant whose residency allows less than where its data is.

  1. a tenants export, JSON, ``[{"tenantId", "name", "residency", "region"}]`` (the identity service's ``GET /v1/tenants``
     items, ``{"items": [...]}``, are accepted too, with ``displayName`` read as ``name``). To export from the database:
       psql -At -c "select coalesce(json_agg(json_build_object('tenantId', tenant_id, 'name', display_name,
                    'residency', residency, 'region', region)), '[]') from identity_tenants" > tenants.json
  2. the region value files (``deploy/multi-region/values/**/*.yaml``), merged over the chart defaults as Helm does.
  3. platform/go/residency/residency.json, the table that maps a residency code to the regions it allows. It is the file
     the Go provisioning check embeds, so this report and the provisioning check cannot disagree.

A tenant is placed in one region (its ``region``). Its data is in that region and in the pair's other region (the CNPG
replica, the MirrorMaker 2 target, the peer's backups and object store), so each store lists both:

  database     the region's Postgres and the pair peer's replica           region.id, kafka.remoteAlias (derived)
  objectStore  ``objectStore.region`` of each region of the pair            stated, else region.id (derived)
  kafka        ``kafka.localAlias`` and ``kafka.remoteAlias``               stated
  backups      azure-blob: ``backup.azure.storageAccountRegion``            stated
               s3: ``backup.s3.region``, else the bucket named by ``backup.destinationPath`` (``s3://passage-pg-backups-<id>/``),
               else region.id, the chart default                            derived

``derived`` marks a region the values do not state and the tool infers from region.id. Output is one row per tenant per
data store plus a violations list, as JSON and CSV. A violation has a kind:

  outside-residency   a store holds the tenant's data in a region its residency does not allow (an EU tenant whose backup is in a US region)
  placement           the tenant's own region is outside its residency
  unplaced            a tenant with a restricted residency has no region, so nothing can be shown
  no-region-values    the tenant's region has no region value file, so its stores cannot be shown
  unknown-residency   the residency code is not in the table

Usage:
    python3 deploy/multi-region/tools/residency_report.py --tenants tenants.json --json report.json --csv report.csv
    python3 deploy/multi-region/tools/residency_report.py --tenants tenants.json            # JSON to stdout

Exit status: 0 no violations, 1 violations found, 2 usage or input error. Stdlib only, Python 3.11+.
Tests: python3 -m unittest discover -s deploy/multi-region/tools
"""

from __future__ import annotations

import argparse
import csv
import io
import json
import re
import sys
from pathlib import Path
from typing import Any

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import check_region_pair as crp  # noqa: E402  (the values-file parser and Helm merge)

VALUES_DIR = HERE.parent / "values"
CHART_VALUES = HERE.parent / "chart" / "values.yaml"
RESIDENCY_TABLE = HERE.parents[2] / "platform" / "go" / "residency" / "residency.json"
STORES = ("database", "objectStore", "kafka", "backups")
S3_BUCKET = re.compile(r"^s3://passage-pg-backups-([a-z0-9-]+)/?$")
CSV_COLUMNS = ("tenantId", "name", "residency", "region", "store", "regions", "derived", "source", "status", "outside")


def norm_region(region: Any) -> str:
    return str(region or "").strip().lower()


def load_table(path: Path = RESIDENCY_TABLE) -> dict[str, Any]:
    """The residency table (platform/go/residency/residency.json), codes upper-cased and regions lower-cased."""
    doc = json.loads(path.read_text(encoding="utf-8"))
    return {
        "default": doc["default"],
        "residencies": {
            code.upper(): {"description": e.get("description", ""), "regions": [norm_region(r) for r in e["regions"]]}
            for code, e in doc["residencies"].items()
        },
    }


def norm_code(table: dict[str, Any], code: Any) -> str:
    code = str(code or "").strip().upper()
    return code or table["default"]


def allows(table: dict[str, Any], code: str, region: str) -> bool:
    """The Go residency.Allows: an unknown code allows nothing, a blank region is never allowed, an empty list is any."""
    entry = table["residencies"].get(norm_code(table, code))
    region = norm_region(region)
    if entry is None or not region:
        return False
    return not entry["regions"] or region in entry["regions"]


def load_regions(values_dir: Path, chart: dict[str, Any]) -> dict[str, tuple[Path, dict[str, Any]]]:
    """Every region value file under values_dir, merged over the chart defaults, keyed by region.id."""
    out: dict[str, tuple[Path, dict[str, Any]]] = {}
    for path in sorted(values_dir.rglob("*.yaml")):
        try:
            doc = crp.merge(chart, crp.parse(path.read_text(encoding="utf-8")))
        except ValueError as exc:
            raise ValueError(f"{path}: {exc}") from exc
        region_id = norm_region(crp._get(doc, "region.id"))
        if not region_id:
            continue
        if region_id in out:
            raise ValueError(f"{path}: region {region_id} is also defined in {out[region_id][0]}")
        out[region_id] = (path, doc)
    return out


def _s3_region(doc: dict[str, Any], own: str) -> tuple[str, bool]:
    stated = crp._get(doc, "backup.s3.region")
    if stated:
        return norm_region(stated), False
    match = S3_BUCKET.match(str(crp._get(doc, "backup.destinationPath") or ""))
    return (match.group(1), True) if match else (own, True)


def store_regions(store: str, own: str, doc: dict[str, Any]) -> tuple[list[tuple[str, bool]], str]:
    """The (region, derived) pairs one region file puts a store in, and the keys they came from."""
    if store == "database":
        peer = norm_region(crp._get(doc, "kafka.remoteAlias"))
        return [(own, True)] + ([(peer, True)] if peer else []), "region.id, kafka.remoteAlias"
    if store == "objectStore":
        stated = crp._get(doc, "objectStore.region")
        return [(norm_region(stated), False)] if stated else [(own, True)], "objectStore.region" if stated else "region.id"
    if store == "kafka":
        found = [(norm_region(crp._get(doc, k)), False) for k in ("kafka.localAlias", "kafka.remoteAlias")]
        return [(r, d) for r, d in found if r], "kafka.localAlias, kafka.remoteAlias"
    if crp._get(doc, "backup.store") == "azure-blob":
        stated = norm_region(crp._get(doc, "backup.azure.storageAccountRegion"))
        return [(stated, False)] if stated else [(own, True)], "backup.azure.storageAccountRegion"
    region, derived = _s3_region(doc, own)
    return [(region, derived)], "backup.s3.region" if not derived else "backup.destinationPath or region.id"


def tenant_stores(region: str, regions: dict[str, tuple[Path, dict[str, Any]]]) -> dict[str, tuple[list[str], bool, str]]:
    """store -> (sorted regions, derived, source) for a tenant placed in ``region``: its region file and its pair peer's."""
    path, doc = regions[region]
    peer = norm_region(crp._get(doc, "kafka.remoteAlias"))
    out: dict[str, tuple[list[str], bool, str]] = {}
    for store in STORES:
        found, keys = store_regions(store, region, doc)
        sources = [f"{path.name}: {keys}"]
        if peer and peer in regions and store != "kafka":
            more, _ = store_regions(store, peer, regions[peer][1])
            found += more
            sources.append(f"{regions[peer][0].name}: {keys}")
        elif peer and store in ("objectStore", "backups"):
            found.append((peer, True))
            sources.append(f"{peer}: no region file, assumed co-located with its region id")
        out[store] = (sorted({r for r, _ in found}), any(d for _, d in found), "; ".join(sources))
    return out


def build_report(tenants: list[dict[str, Any]], regions: dict[str, tuple[Path, dict[str, Any]]], table: dict[str, Any]) -> dict[str, Any]:
    rows: list[dict[str, Any]] = []
    violations: list[dict[str, Any]] = []

    for t in tenants:
        code = norm_code(table, t.get("residency"))
        region = norm_region(t.get("region"))
        base = {"tenantId": t["tenantId"], "name": t.get("name", ""), "residency": code, "region": region}
        restricted = bool(table["residencies"].get(code, {"regions": ["?"]})["regions"])

        def violate(kind: str, detail: str, store: str = "", offending: list[str] | None = None) -> None:
            violations.append({**base, "store": store, "kind": kind, "regions": offending or [], "detail": detail})

        if code not in table["residencies"]:
            violate("unknown-residency", f"residency {code!r} is not one of {', '.join(sorted(table['residencies']))}")
        elif region and not allows(table, code, region):
            violate("placement", f"the tenant's own region {region} is outside residency {code}", offending=[region])

        stores: dict[str, tuple[list[str], bool, str]] = {}
        unverified = ""
        if not region:
            unverified = "unplaced"
            if restricted:
                violate("unplaced", f"a tenant with residency {code} has no region, so where its data lives cannot be shown")
        elif region not in regions:
            unverified = "no-region-values"
            if restricted:
                violate("no-region-values", f"no region value file defines region {region}, so its stores cannot be shown")
        else:
            stores = tenant_stores(region, regions)

        for store in STORES:
            found, derived, source = stores.get(store, ([], False, "no evidence: " + (unverified or "-")))
            outside = [r for r in found if not allows(table, code, r)]
            status = "unverified" if unverified or code not in table["residencies"] else ("violation" if outside else "ok")
            rows.append({**base, "store": store, "regions": found, "derived": derived, "source": source, "status": status, "outside": outside})
            if outside and code in table["residencies"]:
                violate("outside-residency", f"{store} holds data in {', '.join(outside)}, outside residency {code}", store, outside)

    return {
        "schemaVersion": 1,
        "summary": {
            "tenants": len(tenants),
            "rows": len(rows),
            "violations": len(violations),
            "tenantsWithViolations": len({v["tenantId"] for v in violations}),
        },
        "rows": rows,
        "violations": violations,
    }


def to_csv(report: dict[str, Any]) -> str:
    buf = io.StringIO()
    writer = csv.writer(buf, lineterminator="\n")
    writer.writerow(CSV_COLUMNS)
    for row in report["rows"]:
        writer.writerow([";".join(row[c]) if isinstance(row[c], list) else row[c] for c in CSV_COLUMNS])
    return buf.getvalue()


def read_tenants(path: Path) -> list[dict[str, Any]]:
    doc = json.loads(path.read_text(encoding="utf-8"))
    items = doc.get("items") if isinstance(doc, dict) else doc
    if not isinstance(items, list):
        raise ValueError(f"{path}: expected a JSON list of tenants (or an object with an items list)")
    out: list[dict[str, Any]] = []
    for i, item in enumerate(items):
        if not isinstance(item, dict) or not item.get("tenantId"):
            raise ValueError(f"{path}: tenant {i} has no tenantId")
        out.append({**item, "name": item.get("name") or item.get("displayName") or item.get("realmName") or ""})
    return out


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--tenants", type=Path, required=True, help="tenants export, JSON [{tenantId, name, residency, region}]")
    parser.add_argument("--values-dir", type=Path, default=VALUES_DIR, help="the region value files (default deploy/multi-region/values)")
    parser.add_argument("--chart-values", type=Path, default=CHART_VALUES, help="the chart's default values.yaml")
    parser.add_argument("--residency-table", type=Path, default=RESIDENCY_TABLE, help="platform/go/residency/residency.json")
    parser.add_argument("--json", type=Path, default=None, dest="json_out", help="write the report here (default: stdout)")
    parser.add_argument("--csv", type=Path, default=None, dest="csv_out", help="write the rows as CSV here")
    args = parser.parse_args(argv)

    try:
        table = load_table(args.residency_table)
        chart = crp.parse(args.chart_values.read_text(encoding="utf-8"))
        regions = load_regions(args.values_dir, chart)
        tenants = read_tenants(args.tenants)
    except (OSError, ValueError, KeyError) as exc:  # json.JSONDecodeError is a ValueError
        print(f"error: {exc}", file=sys.stderr)
        return 2

    report = build_report(tenants, regions, table)
    text = json.dumps(report, indent=2) + "\n"
    if args.json_out:
        args.json_out.write_text(text, encoding="utf-8")
    else:
        sys.stdout.write(text)
    if args.csv_out:
        args.csv_out.write_text(to_csv(report), encoding="utf-8")
    s = report["summary"]
    print(
        f"RESIDENCY REPORT: {s['tenants']} tenant(s), {s['violations']} violation(s) across {s['tenantsWithViolations']} tenant(s)",
        file=sys.stderr,
    )
    for v in report["violations"]:
        print(f"VIOLATION  {v['tenantId']} ({v['residency']} in {v['region'] or '-'}): {v['kind']}: {v['detail']}", file=sys.stderr)
    return 1 if report["violations"] else 0


if __name__ == "__main__":
    sys.exit(main())
