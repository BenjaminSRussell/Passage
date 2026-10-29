#!/usr/bin/env python3
"""Facts collector for the game day (PSG-50, FR-50.6 steps 2 and 5, fx-gameday).

Reads a live region and writes the facts JSON that ``baseline.py`` consumes. It is the step the game-day
runbook calls "the collector": run it before failure injection (baseline) and again after failover
(observed). It never changes data; every query is a read.

    python3 collect.py --spec spec.json --out facts.json                              # before failure injection
    python3 baseline.py capture --input facts.json --out baseline.json               # record the digest
    python3 collect.py --spec spec.json --baseline baseline.json --out post.json     # after failover
    python3 baseline.py verify --baseline baseline.json --observed post.json --expect-digest sha256:...

The spec is operator-supplied because the ledger, inventory and sequence tables belong to other waves:

    {
      "ledger": {
        "sql": "select json_agg(t) from (select tenant, seq, head from ...) t",
        "checkpoint_sql": "select to_json(hash) from ... where tenant = :tenant and seq = :seq"
      },
      "rows":      {"ledger.journal_line": "select count(*) from ledger.journal_line"},
      "sequences": {"fiscal-invoice-FR": "select json_agg(n order by n) from ..."},
      "offsets_cluster": "passage-kafka-eu-central",
      "offsets_cmd": ["kafka-get-offsets", "--bootstrap-server", "passage-kafka-bootstrap:9093"]
    }

Rules:
  * ``ledger.sql`` returns a JSON array of objects with ``tenant``, ``seq`` and ``head``; an object may
    carry ``checkpoints`` (sequence -> hash). The collector only reshapes the rows.
  * ``ledger.checkpoint_sql`` (optional) returns the chain hash of one tenant at one sequence as a JSON
    string. With ``--baseline`` the collector runs it once per baseline tenant, ``:tenant`` replaced by
    the tenant id as a quoted literal and ``:seq`` by the baseline sequence, and adds the result to that
    tenant's ``checkpoints``. ``baseline.py verify`` needs exactly this hash to prove the chain was
    extended, not replaced (BR-50.3), so the post-failover run must pass ``--baseline``.
  * ``rows`` values are scalar counts; ``sequences`` values are JSON arrays of integers in order.
  * ``offsets_cmd`` prints a JSON object ``{"topic-partition": end_offset}`` on stdout. Offsets must be
    read from the same Kafka cluster both times (the standby that will be promoted); ``offsets_cluster``
    names it and goes into the facts as ``offsetsCluster``, and verify fails when the two runs differ.
  * Every query runs in a read-only transaction (``PGOPTIONS=-c default_transaction_read_only=on``), so a
    spec that writes fails instead of changing data.
  * Connection settings come from the standard libpq ``PG*`` environment variables (for example
    ``PGHOST``, ``PGUSER``, ``PGPASSWORD`` from a file-projected secret). No DSN or password goes on argv.

Stdlib only, Python 3.11+. Tests: ``python3 -m unittest discover -s deploy/multi-region/gameday``.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import Any, Callable

import baseline

Runner = Callable[[list[str]], str]
REQUIRED = ("ledger", "rows", "sequences", "offsets_cluster", "offsets_cmd")
TENANT_RE = re.compile(r"^[A-Za-z0-9_.-]{1,64}$")
READ_ONLY = "-c default_transaction_read_only=on"


class SpecError(ValueError):
    """The collector spec is malformed."""


def validate_spec(spec: dict[str, Any]) -> None:
    for key in REQUIRED:
        if key not in spec:
            raise SpecError(f"spec: missing '{key}'")
    if not isinstance(spec["ledger"], dict) or not isinstance(spec["ledger"].get("sql"), str):
        raise SpecError("spec: ledger.sql must be a string")
    checkpoint_sql = spec["ledger"].get("checkpoint_sql")
    if checkpoint_sql is not None and (
        not isinstance(checkpoint_sql, str)
        or not re.search(r":tenant\b", checkpoint_sql)
        or not re.search(r":seq\b", checkpoint_sql)
    ):
        raise SpecError("spec: ledger.checkpoint_sql must be a string using :tenant and :seq")
    for section in ("rows", "sequences"):
        if not isinstance(spec[section], dict) or not spec[section]:
            raise SpecError(f"spec: {section} must be a non-empty object")
        for name, sql in spec[section].items():
            if not isinstance(sql, str) or not sql.strip():
                raise SpecError(f"spec: {section}[{name}] must be a non-empty SQL string")
    cluster = spec["offsets_cluster"]
    if not isinstance(cluster, str) or not cluster.strip():
        raise SpecError("spec: offsets_cluster must name the Kafka cluster the offsets are read from")
    cmd = spec["offsets_cmd"]
    if not isinstance(cmd, list) or not cmd or not all(isinstance(a, str) for a in cmd):
        raise SpecError("spec: offsets_cmd must be a non-empty list of strings")


def psql_query(psql: str) -> Callable[[str], str]:
    """Return a function that runs one query through psql, read-only, and returns unaligned, tuples-only stdout."""
    env = dict(os.environ)
    env["PGOPTIONS"] = (env.get("PGOPTIONS", "") + " " + READ_ONLY).strip()

    def query(sql: str) -> str:
        return _run(
            [psql, "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-c", sql],
            "query failed",
            env=env,
        )

    return query


def checkpoint_query(template: str, tenant: str, seq: int) -> str:
    """Fill :tenant and :seq. The tenant is checked against a strict pattern before it is quoted."""
    if not TENANT_RE.match(tenant):
        raise SpecError(f"baseline tenant {tenant!r} is not a plain identifier")
    if not isinstance(seq, int) or isinstance(seq, bool) or seq < 0:
        raise SpecError(f"baseline seq for {tenant} must be a non-negative integer")
    filled = re.sub(r":tenant\b", f"'{tenant}'", template)
    return re.sub(r":seq\b", str(seq), filled)


def collect(
    spec: dict[str, Any],
    query: Callable[[str], str],
    cmd_runner: Runner,
    base: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """Build facts from the spec. `query` runs SQL and returns text; `cmd_runner` runs argv and returns stdout.

    `base` is the baseline manifest of an earlier run; with it, the hash at each baseline tenant's sequence
    is read through ``ledger.checkpoint_sql`` so verify can compare it with the baseline head.
    """
    validate_spec(spec)
    ledger_rows = _json(query(spec["ledger"]["sql"]), "ledger.sql")
    if not isinstance(ledger_rows, list):
        raise SpecError("ledger.sql must return a JSON array")
    ledger: dict[str, Any] = {}
    for row in ledger_rows:
        if not isinstance(row, dict) or "tenant" not in row:
            raise SpecError("ledger row must be an object with 'tenant'")
        entry = {"seq": row.get("seq"), "head": row.get("head")}
        if "checkpoints" in row:
            entry["checkpoints"] = row["checkpoints"]
        ledger[str(row["tenant"])] = entry

    if base is not None:
        template = spec["ledger"].get("checkpoint_sql")
        if template is None:
            raise SpecError("spec: ledger.checkpoint_sql is required with --baseline")
        for tenant, head in sorted((base.get("ledger") or {}).items()):
            entry = ledger.get(tenant)
            if entry is None:
                continue  # verify reports the missing tenant
            seq = head.get("seq") if isinstance(head, dict) else None
            value = _json(query(checkpoint_query(template, tenant, seq)), f"ledger.checkpoint_sql[{tenant}]")
            if not isinstance(value, str):
                raise SpecError(f"ledger.checkpoint_sql[{tenant}] must return a JSON string (the hash)")
            entry.setdefault("checkpoints", {})[str(seq)] = value

    rows: dict[str, int] = {}
    for name, sql in spec["rows"].items():
        value = _json(query(sql), f"rows[{name}]")
        rows[name] = value

    sequences: dict[str, list[int]] = {}
    for name, sql in spec["sequences"].items():
        value = _json(query(sql), f"sequences[{name}]")
        sequences[name] = value

    offsets = _json(cmd_runner(spec["offsets_cmd"]), "offsets_cmd")
    facts = {
        "offsetsCluster": spec["offsets_cluster"],
        "ledger": ledger,
        "rows": rows,
        "offsets": offsets,
        "sequences": sequences,
    }
    try:
        baseline.validate_facts(facts)
    except baseline.FactsError as exc:
        raise SpecError(f"collected facts are invalid: {exc}") from exc
    return facts


def _json(text: str, where: str) -> Any:
    try:
        return json.loads(text.strip() or "null")
    except json.JSONDecodeError as exc:
        raise SpecError(f"{where}: output is not JSON") from exc


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--spec", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--baseline", type=Path, help="baseline manifest; required after failover (checkpoints)")
    parser.add_argument("--psql", default="psql", help="psql binary (default: psql on PATH)")
    args = parser.parse_args(argv)
    try:
        spec = json.loads(args.spec.read_text(encoding="utf-8"))
        if not isinstance(spec, dict):
            raise SpecError("spec: top level must be an object")
        base = None
        if args.baseline is not None:
            base = json.loads(args.baseline.read_text(encoding="utf-8"))
            if not isinstance(base, dict):
                raise SpecError("baseline: top level must be an object")
            baseline.check_manifest(base)
        facts = collect(spec, psql_query(args.psql), _run_cmd, base)
        args.out.write_text(json.dumps(facts, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    except (OSError, json.JSONDecodeError, SpecError, baseline.FactsError, RuntimeError,
            subprocess.TimeoutExpired) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    print(f"facts written: {len(facts['ledger'])} tenants, {len(facts['rows'])} domains, "
          f"{len(facts['offsets'])} partitions, {len(facts['sequences'])} sequences")
    return 0


def _run_cmd(argv: list[str]) -> str:
    return _run(argv, "offsets_cmd failed")


def _run(argv: list[str], what: str, env: dict[str, str] | None = None) -> str:
    """Run a read-only command. Errors name the failure, never the arguments (no secrets in messages)."""
    try:
        proc = subprocess.run(  # noqa: S603
            argv, capture_output=True, text=True, check=False, timeout=300, env=env
        )
    except FileNotFoundError as exc:
        raise RuntimeError(f"{what}: {Path(argv[0]).name} not found") from exc
    if proc.returncode != 0:
        raise RuntimeError(f"{what} with exit {proc.returncode}")
    return proc.stdout


if __name__ == "__main__":
    sys.exit(main())
