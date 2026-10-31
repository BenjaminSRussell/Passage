#!/usr/bin/env python3
"""Local failover drill: the game-day toolchain run end to end against real Postgres replication (PSG-50).

    python3 deploy/multi-region/gameday/local_drill.py [--mode sync|async] [--out DIR] [--pg-bin DIR]

What it is: a rehearsal of docs/ops/runbooks/gameday.md steps 2-7 on one machine, so the collector,
the baseline verifier and the scorecard are exercised on a real primary/standby pair instead of
hand-written facts. What it is not: a region failover. There is no second region, no CNPG, no
MirrorMaker 2, no Kafka, no Temporal and no DNS; the AC-50.3 game day still needs the live pair.

Steps:
  1. initdb a primary and a streaming standby (pg_basebackup -R) in a temporary directory, Unix sockets
     only. ``--mode sync`` sets ``synchronous_standby_names='*'`` on the primary (commit waits for the
     standby to flush), ``--mode async`` leaves replication asynchronous.
  2. One writer appends a per-tenant hash chain (``hash = sha256(prev ":" seq)``, genesis ``"genesis"``)
     and a gap-free invoice number per entry, recording every commit the server acknowledged.
  3. Baseline (game-day step 2): ``collect.py`` against the primary, ``baseline.py capture``.
  4. Fence (failover step 1, the region partition): the primary is stopped with ``-m immediate`` while
     the writer is still writing, so in-flight commits are cut off as a partition would cut them off.
  5. Promote the standby; the "DNS verified" time is the first write the new primary accepts.
  6. Verify (game-day step 5): ``collect.py --baseline`` against the new primary, ``baseline.py verify``.
  7. RPO: the old primary is started read-only (fenced) and compared with the new one. RPO is the commit
     time of the latest acknowledged entry missing from the new primary minus the latest commit time
     the new primary holds (0 when no acknowledged entry is missing). Entries the old primary committed
     locally but never acknowledged are counted separately (``unacknowledgedLocalOnly``): no client was
     told they were committed. ``score.py`` then judges RTO, RPO and the verify report.
  8. Independently of ``baseline.py``, the new primary's chains are recomputed with hashlib and every
     acknowledged entry is looked up.

Exit status: 0 when the scorecard passes and no acknowledged commit was lost, 1 otherwise, 2 when the
drill could not run (missing binaries, a cluster that would not start). Artifacts (facts, baseline,
verify report, timeline, scorecard, drill.json) go to ``--out``. Stdlib only, Python 3.11+.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

HERE = Path(__file__).resolve().parent
TENANTS = ("tenant-a", "tenant-b")
USER = "drill"
BINARIES = ("initdb", "pg_ctl", "pg_basebackup", "psql")

SCHEMA = """
create table chain (
  tenant text not null,
  seq bigint not null,
  hash text not null,
  committed_at timestamptz not null default clock_timestamp(),
  primary key (tenant, seq)
);
create table fiscal_number (series text not null, n bigint not null, primary key (series, n));
create table drill_probe (id bigserial primary key, at timestamptz not null default clock_timestamp());
"""

APPEND = """with last as (select seq, hash from chain where tenant = '{t}' order by seq desc limit 1),
nxt as (select coalesce((select seq from last), 0) + 1 as seq, coalesce((select hash from last), 'genesis') as prev),
ins as (insert into chain (tenant, seq, hash)
        select '{t}', seq, encode(sha256(convert_to(prev || ':' || seq, 'UTF8')), 'hex') from nxt
        returning tenant, seq, committed_at),
num as (insert into fiscal_number (series, n) select 'INV-' || tenant, seq from ins)
select tenant, seq, extract(epoch from committed_at) from ins;
"""


class DrillError(RuntimeError):
    """The drill could not run (environment problem), as opposed to a failed check."""


def find_binaries(pg_bin: Path | None) -> dict[str, str]:
    found: dict[str, str] = {}
    for name in BINARIES:
        path = str(pg_bin / name) if pg_bin else shutil.which(name)
        if not path or not os.access(path, os.X_OK):
            raise DrillError(f"{name} not found (install PostgreSQL client and server binaries or pass --pg-bin)")
        found[name] = path
    return found


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def now_utc() -> datetime:
    return datetime.now(timezone.utc)


def iso(t: datetime) -> str:
    return t.isoformat().replace("+00:00", "Z")


class Node:
    """One local cluster reached over a Unix socket in the drill directory."""

    def __init__(self, bins: dict[str, str], data: Path, sockdir: Path, port: int):
        self.bins, self.data, self.sockdir, self.port = bins, data, sockdir, port

    @property
    def env(self) -> dict[str, str]:
        env = {k: v for k, v in os.environ.items() if not k.startswith("PG")}
        env.update(PGHOST=str(self.sockdir), PGPORT=str(self.port), PGUSER=USER, PGDATABASE="postgres")
        return env

    def pg_ctl(self, *args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
        proc = subprocess.run([self.bins["pg_ctl"], "-D", str(self.data), *args],
                              capture_output=True, text=True, timeout=120, check=False)
        if check and proc.returncode != 0:
            raise DrillError(f"pg_ctl {args[-1]} failed: {proc.stderr.strip() or proc.stdout.strip()}")
        return proc

    def start(self, *options: str) -> None:
        opts = ["-o", " ".join(options)] if options else []
        self.pg_ctl("-l", str(self.data.parent / f"{self.data.name}.log"), "-w", *opts, "start")

    def psql(self, sql: str, check: bool = True) -> str:
        try:
            proc = subprocess.run([self.bins["psql"], "-X", "-A", "-t", "-q", "-v", "ON_ERROR_STOP=1", "-c", sql],
                                  capture_output=True, text=True, timeout=60, env=self.env, check=False)
        except subprocess.TimeoutExpired as exc:
            if check:
                raise DrillError("psql timed out") from exc
            return ""
        if check and proc.returncode != 0:
            raise DrillError(f"psql failed: {proc.stderr.strip()}")
        return proc.stdout.strip() if proc.returncode == 0 else ""


def write_conf(data: Path, lines: list[str]) -> None:
    with (data / "postgresql.conf").open("a", encoding="utf-8") as fh:
        fh.write("\n# local_drill.py\n" + "\n".join(lines) + "\n")


class Writer:
    """Feeds hash-chain appends to one psql session and records every acknowledged commit."""

    def __init__(self, node: Node):
        self.proc = subprocess.Popen(
            [node.bins["psql"], "-X", "-A", "-t", "-q", "-v", "ON_ERROR_STOP=1"],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            text=True, bufsize=1, env=node.env,
        )
        self.acked: list[tuple[str, int, float]] = []
        self.stop = threading.Event()
        self._lock = threading.Lock()
        self._feeder = threading.Thread(target=self._feed, daemon=True)
        self._reader = threading.Thread(target=self._read, daemon=True)
        self._feeder.start()
        self._reader.start()

    def _feed(self) -> None:
        assert self.proc.stdin is not None
        i = 0
        try:
            while not self.stop.is_set():
                # Keep a short queue so the session is never idle but the pipe holds little.
                with self._lock:
                    pending = i - len(self.acked)
                if pending > 8:
                    time.sleep(0.0005)
                    continue
                self.proc.stdin.write(APPEND.format(t=TENANTS[i % len(TENANTS)]))
                self.proc.stdin.flush()
                i += 1
        except (BrokenPipeError, OSError, ValueError):
            pass

    def _read(self) -> None:
        assert self.proc.stdout is not None
        for line in self.proc.stdout:
            parts = line.strip().split("|")
            if len(parts) == 3:
                with self._lock:
                    self.acked.append((parts[0], int(parts[1]), float(parts[2])))

    def count(self) -> int:
        with self._lock:
            return len(self.acked)

    def finish(self) -> list[tuple[str, int, float]]:
        self.stop.set()
        try:
            if self.proc.stdin:
                self.proc.stdin.close()
        except OSError:
            pass
        try:
            self.proc.wait(timeout=30)
        except subprocess.TimeoutExpired:
            self.proc.kill()
        self._reader.join(timeout=10)
        with self._lock:
            return list(self.acked)


def collector_spec() -> dict[str, Any]:
    return {
        "ledger": {
            "sql": "select json_agg(t) from (select distinct on (tenant) tenant, seq, hash as head "
                   "from chain order by tenant, seq desc) t",
            "checkpoint_sql": "select to_json(hash) from chain where tenant = :tenant and seq = :seq",
        },
        "rows": {"chain": "select count(*) from chain", "fiscal_number": "select count(*) from fiscal_number"},
        "sequences": {f"INV-{t}": f"select json_agg(n order by n) from fiscal_number where series = 'INV-{t}'"
                      for t in TENANTS},
        # The drill has no Kafka: the offsets section is empty and says so in the cluster name.
        "offsets_cluster": "none (local drill, no Kafka)",
        "offsets_cmd": [sys.executable, "-I", "-c", "print('{}')"],
    }


def run_tool(script: str, args: list[str], env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run([sys.executable, str(HERE / script), *args], capture_output=True, text=True,
                          timeout=300, env=env, check=False)


def recompute_chains(rows: list[tuple[str, int, str]]) -> list[str]:
    """Independent of baseline.py: walk each tenant's chain from genesis with hashlib."""
    problems: list[str] = []
    by_tenant: dict[str, list[tuple[int, str]]] = {}
    for tenant, seq, h in rows:
        by_tenant.setdefault(tenant, []).append((seq, h))
    for tenant, entries in sorted(by_tenant.items()):
        prev = "genesis"
        for expected, (seq, h) in enumerate(sorted(entries), start=1):
            if seq != expected:
                problems.append(f"{tenant}: seq {seq} where {expected} was expected")
                break
            want = hashlib.sha256(f"{prev}:{seq}".encode()).hexdigest()
            if h != want:
                problems.append(f"{tenant}: hash at seq {seq} does not follow from seq {seq - 1}")
                break
            prev = h
    return problems


def chain_rows(node: Node) -> list[tuple[str, int, str]]:
    out = node.psql("select tenant, seq, hash from chain order by tenant, seq")
    return [(t, int(s), h) for t, s, h in (line.split("|") for line in out.splitlines() if line)]


def drill(mode: str, out: Path, bins: dict[str, str], warmup_acks: int = 200, after_baseline_s: float = 1.0,
          cut_replication: bool = False) -> dict[str, Any]:
    work = Path(tempfile.mkdtemp(prefix="pgdrill"))
    sock = work / "s"
    sock.mkdir()
    primary = Node(bins, work / "primary", sock, free_port())
    standby = Node(bins, work / "standby", sock, free_port())
    started: list[Node] = []
    try:
        proc = subprocess.run([bins["initdb"], "-D", str(primary.data), "-U", USER, "-A", "trust", "-N"],
                              capture_output=True, text=True, timeout=120, check=False)
        if proc.returncode != 0:
            raise DrillError(f"initdb failed: {proc.stderr.strip()}")
        conf = ["listen_addresses = ''", f"unix_socket_directories = '{sock}'", f"port = {primary.port}",
                "wal_level = replica", "max_wal_senders = 4", "hot_standby = on"]
        write_conf(primary.data, conf)
        primary.start()
        started.append(primary)
        primary.psql(SCHEMA)

        proc = subprocess.run([bins["pg_basebackup"], "-h", str(sock), "-p", str(primary.port), "-U", USER,
                               "-D", str(standby.data), "-R", "-X", "stream", "-c", "fast"],
                              capture_output=True, text=True, timeout=300, check=False)
        if proc.returncode != 0:
            raise DrillError(f"pg_basebackup failed: {proc.stderr.strip()}")
        write_conf(standby.data, [f"port = {standby.port}", "synchronous_standby_names = ''"])
        standby.start()
        started.append(standby)
        if mode == "sync":
            # Only once the standby exists: before that, every commit would wait for it forever.
            primary.psql("alter system set synchronous_standby_names = '*'")
            primary.psql("select pg_reload_conf()")
        want = "sync" if mode == "sync" else "async"
        deadline = time.monotonic() + 30
        while primary.psql(f"select count(*) from pg_stat_replication where state = 'streaming' "
                           f"and sync_state = '{want}'") != "1":
            if time.monotonic() > deadline:
                raise DrillError(f"standby did not reach {want} streaming")
            time.sleep(0.1)

        writer = Writer(primary)
        deadline = time.monotonic() + 60
        while writer.count() < warmup_acks:
            if time.monotonic() > deadline or writer.proc.poll() is not None:
                writer.finish()
                raise DrillError("writer made no progress")
            time.sleep(0.05)

        spec = out / "spec.json"
        spec.write_text(json.dumps(collector_spec(), indent=2) + "\n", encoding="utf-8")
        res = run_tool("collect.py", ["--spec", str(spec), "--out", str(out / "facts.json")], env=primary.env)
        if res.returncode != 0:
            writer.finish()
            raise DrillError(f"baseline collect failed: {res.stderr.strip()}")
        res = run_tool("baseline.py", ["capture", "--input", str(out / "facts.json"), "--out", str(out / "baseline.json")])
        if res.returncode != 0:
            writer.finish()
            raise DrillError(f"baseline capture failed: {res.stderr.strip()}")
        recorded_digest = json.loads((out / "baseline.json").read_text())["digest"]

        if cut_replication:
            # Everything up to the baseline reaches the standby first, so the baseline itself is not at risk.
            lsn = primary.psql("select pg_current_wal_lsn()")
            deadline = time.monotonic() + 30
            while standby.psql(f"select pg_last_wal_replay_lsn() >= '{lsn}'::pg_lsn") != "t":
                if time.monotonic() > deadline:
                    writer.finish()
                    raise DrillError("standby did not replay up to the baseline")
                time.sleep(0.02)
            standby.psql("alter system set primary_conninfo = ''")
            standby.psql("select pg_reload_conf()")
            deadline = time.monotonic() + 30
            while primary.psql("select count(*) from pg_stat_replication") != "0":
                if time.monotonic() > deadline:
                    writer.finish()
                    raise DrillError("replication link did not drop")
                time.sleep(0.02)

        time.sleep(after_baseline_s)
        fenced_at = now_utc()
        primary.pg_ctl("-m", "immediate", "-w", "stop")
        started.remove(primary)
        acked = writer.finish()

        standby.pg_ctl("-w", "promote")
        deadline = time.monotonic() + 600
        while True:
            if standby.psql("insert into drill_probe default values returning id", check=False):
                verified_at = now_utc()
                break
            if time.monotonic() > deadline:
                raise DrillError("promoted standby never accepted a write")
            time.sleep(0.05)

        res = run_tool("collect.py", ["--spec", str(spec), "--baseline", str(out / "baseline.json"),
                                      "--out", str(out / "post.json")], env=standby.env)
        if res.returncode != 0:
            raise DrillError(f"post-failover collect failed: {res.stderr.strip()}")
        res = run_tool("baseline.py", ["verify", "--baseline", str(out / "baseline.json"), "--observed",
                                       str(out / "post.json"), "--expect-digest", recorded_digest,
                                       "--report", str(out / "verify.json")])
        if res.returncode == 2:
            raise DrillError(f"baseline verify could not run: {res.stderr.strip()}")

        new_rows = chain_rows(standby)
        present = {(t, s) for t, s, _ in new_rows}
        new_latest = float(standby.psql("select coalesce(extract(epoch from max(committed_at)), 0) from chain"))

        # The old primary comes back fenced: read-only, never a write target again.
        primary.start("-c default_transaction_read_only=on")
        started.append(primary)
        old_rows = {(t, s) for t, s, _ in chain_rows(primary)}

        lost_acked = sorted((t, s, at) for t, s, at in acked if (t, s) not in present)
        lag = max(0.0, max(at for _, _, at in lost_acked) - new_latest) if lost_acked else 0.0
        local_only = old_rows - present - {(t, s) for t, s, _ in acked}

        timeline = {
            "fencedAt": iso(fenced_at),
            "dnsVerifiedAt": iso(verified_at),
            "lagAtFenceSeconds": round(lag, 6),
            "notes": "local drill: no DNS; dnsVerifiedAt is the first write the promoted standby accepted",
        }
        (out / "timeline.json").write_text(json.dumps(timeline, indent=2) + "\n", encoding="utf-8")
        res = run_tool("score.py", ["--timeline", str(out / "timeline.json"), "--verify-report",
                                    str(out / "verify.json"), "--report", str(out / "score.json")])
        if res.returncode == 2:
            raise DrillError(f"score could not run: {res.stderr.strip()}")
        score_report = json.loads((out / "score.json").read_text())
        verify_report = json.loads((out / "verify.json").read_text())
        chain_problems = recompute_chains(new_rows)

        summary = {
            "mode": mode,
            "cutReplication": cut_replication,
            "acknowledged": len(acked),
            "oldPrimaryEntries": len(old_rows),
            "newPrimaryEntries": len(present),
            "lostAcknowledged": len(lost_acked),
            "unacknowledgedLocalOnly": len(local_only),
            "rtoSeconds": round((verified_at - fenced_at).total_seconds(), 3),
            "rpoSeconds": timeline["lagAtFenceSeconds"],
            "verifyPassed": verify_report.get("passed") is True,
            "scorePassed": score_report.get("passed") is True,
            "chainProblems": chain_problems,
        }
        summary["passed"] = summary["scorePassed"] and not lost_acked and not chain_problems
        (out / "drill.json").write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
        return summary
    finally:
        for node in started:
            node.pg_ctl("-m", "immediate", "-w", "stop", check=False)
        shutil.rmtree(work, ignore_errors=True)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--mode", choices=["sync", "async"], default="sync")
    parser.add_argument("--out", type=Path, help="artifact directory (default: a new temporary directory)")
    parser.add_argument("--pg-bin", type=Path, help="directory holding initdb, pg_ctl, pg_basebackup and psql")
    parser.add_argument("--after-baseline-seconds", type=float, default=1.0,
                        help="how long the writer keeps writing between the baseline and the fence")
    parser.add_argument("--cut-replication", action="store_true",
                        help="drop the standby's replication link right after the baseline, so the partition "
                             "reaches the link before the primary (a stalled cross-region link)")
    args = parser.parse_args(argv)
    out = args.out or Path(tempfile.mkdtemp(prefix="gameday-drill-"))
    out.mkdir(parents=True, exist_ok=True)
    try:
        summary = drill(args.mode, out, find_binaries(args.pg_bin), after_baseline_s=args.after_baseline_seconds,
                        cut_replication=args.cut_replication)
    except DrillError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(summary, indent=2))
    print(f"artifacts: {out}")
    print("LOCAL DRILL " + ("PASSED" if summary["passed"] else "FAILED"))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
