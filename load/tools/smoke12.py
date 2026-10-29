#!/usr/bin/env python3
"""fx-smoke-12 runner (PSG-50, PRD 9.50.9, AC-50.4).

Runs the twelve core processes in ``load/fixtures/smoke-12.json`` against a base URL, in order, and
writes a JSON report. A process passes only when every step returns 2xx AND its final assertion is
verified. Assertions are domain checks (AP subledger = journal, hash chain extended, ...). They are
supplied as named verifier callables; a process with no verifier is reported UNVERIFIED and fails the
run unless ``--allow-unverified-assertions`` is given (development only).

A step the fixture marks ``unverified`` has no route on main. It is never sent: the step fails with the
fixture's reason and the process stops, as the fixture requires, whatever the flags.

Path placeholders: ``{id}`` is the ``id`` of the most recent response in the same process that carried
one (``create receipt`` then ``post receipt {id}`` posts the receipt, not the earlier order). Any other
placeholder (``{n}``, the pick line) comes from ``--params``; a placeholder with no value fails the step
without sending it.

Usage:
    SMOKE_TOKEN=... python3 load/tools/smoke12.py --base-url https://api.perf.passage.dev \\
        --payloads payloads.json --params params.json --report smoke-report.json

``--payloads`` maps an action name to the JSON body sent for that step. The fixture carries no bodies,
so without it each POST sends ``{}``. ``--params`` is a JSON object of placeholder values, for example
``{"n": 1}``. The token is read from ``SMOKE_TOKEN``, never from argv.

Stdlib only, Python 3.11+. Tests: ``python3 -m unittest discover -s load/tools``.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, Mapping

FIXTURE = Path(__file__).resolve().parent.parent / "fixtures" / "smoke-12.json"
PLACEHOLDER_RE = re.compile(r"\{(\w+)\}")

# (status or None on transport error, parsed JSON body or None)
Transport = Callable[[str, str, Mapping[str, str], Any], tuple[int | None, Any]]
# Receives the process context (ids captured from earlier steps) and returns True when the assertion holds.
Verifier = Callable[[dict[str, Any]], bool]


@dataclass
class StepResult:
    action: str
    method: str
    path: str
    status: int | None
    ok: bool
    detail: str


@dataclass
class ProcessResult:
    id: str
    name: str
    steps: list[StepResult] = field(default_factory=list)
    assertion: str = "UNVERIFIED"  # PASSED | FAILED | UNVERIFIED | SKIPPED
    passed: bool = False


def load_fixture(path: Path = FIXTURE) -> dict[str, Any]:
    with path.open(encoding="utf-8") as fh:
        return json.load(fh)


def missing_params(path: str, ctx: Mapping[str, Any]) -> list[str]:
    return [name for name in PLACEHOLDER_RE.findall(path) if name not in ctx]


def fill_path(path: str, ctx: Mapping[str, Any]) -> str | None:
    """Replace every {name} with its value from ctx. Returns None when a placeholder has no value."""
    if missing_params(path, ctx):
        return None
    return PLACEHOLDER_RE.sub(lambda m: urllib.parse.quote(str(ctx[m.group(1)]), safe=""), path)


def run_process(
    proc: Mapping[str, Any],
    *,
    send: Transport,
    base_url: str,
    token: str,
    run_id: str,
    payloads: Mapping[str, Any],
    verifier: Verifier | None,
    params: Mapping[str, Any] | None = None,
) -> ProcessResult:
    result = ProcessResult(id=proc["id"], name=proc["name"])
    ctx: dict[str, Any] = dict(params or {})
    for step in proc["steps"]:
        method, action = step["method"], step["action"]
        if step.get("unverified"):
            detail = f"no route on main, not sent: {step['unverified']}"
            result.steps.append(StepResult(action, method, step["path"], None, False, detail))
            result.assertion = "SKIPPED"
            return result
        path = fill_path(step["path"], ctx)
        if path is None:
            names = ", ".join("{" + n + "}" for n in missing_params(step["path"], ctx))
            detail = f"no value for {names} (an id from an earlier step, or --params)"
            result.steps.append(StepResult(action, method, step["path"], None, False, detail))
            result.assertion = "SKIPPED"
            return result
        headers = {"Authorization": f"Bearer {token}", "Accept": "application/json", "X-Correlation-Id": run_id}
        body: Any = None
        if method != "GET":
            headers["Content-Type"] = "application/json"
            headers["Idempotency-Key"] = f"smoke:{run_id}:{proc['id']}:{action}"
            body = payloads.get(action, {})
        status, parsed = send(method, base_url.rstrip("/") + path, headers, body)
        ok = status is not None and 200 <= status < 300
        result.steps.append(StepResult(action, method, path, status, ok, "ok" if ok else f"status {status}"))
        if not ok:
            result.assertion = "SKIPPED"
            return result
        if isinstance(parsed, dict) and parsed.get("id") not in (None, ""):
            ctx["id"] = parsed["id"]  # the most recent object wins: later steps act on what was just created

    if verifier is None:
        result.assertion = "UNVERIFIED"
        return result
    result.assertion = "PASSED" if verifier(ctx) else "FAILED"
    result.passed = result.assertion == "PASSED"
    return result


def run_suite(
    fixture: Mapping[str, Any],
    *,
    send: Transport,
    base_url: str,
    token: str,
    payloads: Mapping[str, Any] | None = None,
    verifiers: Mapping[str, Verifier] | None = None,
    run_id: str | None = None,
    allow_unverified: bool = False,
    params: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    """Run every process. allow_unverified lets a process without a verifier pass when all its steps did."""
    run_id = run_id or str(uuid.uuid4())
    payloads = payloads or {}
    verifiers = verifiers or {}
    results = [
        run_process(
            proc,
            send=send,
            base_url=base_url,
            token=token,
            run_id=run_id,
            payloads=payloads,
            verifier=verifiers.get(proc["id"]),
            params=params,
        )
        for proc in fixture["processes"]
    ]
    passed = bool(results) and all(r.passed or (allow_unverified and r.assertion == "UNVERIFIED") for r in results)
    return {
        "fixture": fixture.get("fixture"),
        "runId": run_id,
        "baseUrl": base_url,
        "passed": passed,
        "processes": [
            {
                "id": r.id,
                "name": r.name,
                "assertion": r.assertion,
                "passed": r.passed,
                "steps": [s.__dict__ for s in r.steps],
            }
            for r in results
        ],
    }


def http_send(method: str, url: str, headers: Mapping[str, str], body: Any) -> tuple[int | None, Any]:
    data = None if body is None else json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, method=method, headers=dict(headers))
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:  # noqa: S310 - operator-supplied base URL
            return resp.status, _parse(resp.read())
    except urllib.error.HTTPError as exc:
        return exc.code, _parse(exc.read())
    except OSError:  # URLError, timeouts, refused or reset connections
        return None, None


def _parse(raw: bytes) -> Any:
    try:
        return json.loads(raw.decode("utf-8") or "null")
    except (UnicodeDecodeError, json.JSONDecodeError):
        return None


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--payloads", type=Path, help="JSON object: action -> request body")
    parser.add_argument("--params", type=Path, help="JSON object: path placeholder -> value, e.g. {\"n\": 1}")
    parser.add_argument("--report", type=Path, help="write the JSON report here")
    parser.add_argument(
        "--allow-unverified-assertions",
        action="store_true",
        help="development only: a process with no verifier passes when its steps pass",
    )
    args = parser.parse_args(argv)

    token = os.environ.get("SMOKE_TOKEN", "")
    if not token:
        print("error: SMOKE_TOKEN is not set", file=sys.stderr)
        return 2
    if not args.base_url.startswith(("https://", "http://")):
        print("error: --base-url must be an http(s) URL", file=sys.stderr)
        return 2
    try:
        payloads = _object(args.payloads, "--payloads")
        params = _object(args.params, "--params")
    except (OSError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    # No verifiers are registered in this file: the domain checks need the campaign environment.
    report = run_suite(
        load_fixture(),
        send=http_send,
        base_url=args.base_url,
        token=token,
        payloads=payloads,
        allow_unverified=args.allow_unverified_assertions,
        params=params,
    )
    for proc in report["processes"]:
        last = proc["steps"][-1] if proc["steps"] else None
        note = f" ({last['action']}: {last['detail']})" if last and not last["ok"] else ""
        print(f"{'PASS' if proc['passed'] else 'FAIL'}  {proc['id']} {proc['name']}: assertion {proc['assertion']}{note}")
    print("SMOKE-12 " + ("PASSED" if report["passed"] else "FAILED"))
    if args.report:
        args.report.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return 0 if report["passed"] else 1


def _object(path: Path | None, flag: str) -> dict[str, Any]:
    if path is None:
        return {}
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        raise ValueError(f"{flag}: top level must be a JSON object")
    return data


if __name__ == "__main__":
    sys.exit(main())
