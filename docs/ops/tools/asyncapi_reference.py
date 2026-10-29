#!/usr/bin/env python3
"""Event reference renderer and undocumented-event gate for AsyncAPI 3 documents (PSG-50, PRD 9.50.8 AC-50.11).

The event counterpart of api_reference.py (FR-50.11: "API reference (OpenAPI/AsyncAPI generated)"). Every event a
service publishes or consumes must be documented before the event reference is published:

  event      every message under components.messages, and every message written inline in a channel, has
             - a CloudEvents type of the form dev.passage.<context>.<entity>.<verb>.v<n>: `x-passage-type` when
               the message declares it, otherwise the message `name` (most services name the message after
               its type, e.g. `name: dev.passage.eam.workorder.created.v1`)
             - a title, summary or description
             - a payload schema (an object, after following a local $ref)
             No two messages of one document declare the same type.
  channel    a non-empty address
  operation  action send or receive, a channel reference, and every message reference points into that
             channel's messages (AsyncAPI 3 rule). The operation is described by its own title, summary or
             description, or else by the description of every message it carries.

Local $ref pointers are resolved with api_reference's resolver. An unresolvable or external $ref is an input
error (exit 2), not a silent pass.

For each --spec the tool prints every gap as `FAIL  <service>: <problem>`. With --out it also writes one Markdown
page per spec (`<service>.md`) for the docs site build (a handoff to deployment-cicd). The service name is the
service directory for `services/<service>/events/asyncapi.json`, otherwise the file stem; two specs with the same
name are an input error. Exit status: 0 no gaps, 1 undocumented events or operations, 2 usage or input error.

    python3 docs/ops/tools/asyncapi_reference.py --spec services/org/events/asyncapi.json --out build/events

The input is JSON; convert YAML first (CI does this, e.g. `yq -o=json`). Stdlib only, Python 3.11+.
Tests: python3 -m unittest discover -s docs/ops/tools
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import api_reference as ar

ACTIONS = ("send", "receive")
EVENT_TYPE = re.compile(r"^dev\.passage\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.v[1-9][0-9]*$")


@dataclass(frozen=True)
class Message:
    label: str  # components.messages key, or "<channel>/<key>" for an inline channel message
    msg: dict[str, Any]


def event_type_valid(value: Any) -> bool:
    return isinstance(value, str) and EVENT_TYPE.match(value) is not None


def event_type(msg: dict[str, Any]) -> str:
    """The message's CloudEvents type: x-passage-type when declared, else its name. Empty when neither is set."""
    if "x-passage-type" in msg:
        return str(msg.get("x-passage-type") or "").strip()
    return str(msg.get("name") or "").strip()


def _text(node: dict[str, Any], *keys: str) -> str:
    for key in keys:
        text = str(node.get(key) or "").strip()
        if text:
            return text
    return ""


def _described(msg: dict[str, Any]) -> str:
    return _text(msg, "title", "summary", "description")


def _dicts(raw: Any) -> dict[str, dict[str, Any]]:
    return {key: raw[key] for key in sorted(raw) if isinstance(raw[key], dict)} if isinstance(raw, dict) else {}


def channels(doc: dict[str, Any]) -> dict[str, dict[str, Any]]:
    return _dicts(doc.get("channels"))


def operations(doc: dict[str, Any]) -> dict[str, dict[str, Any]]:
    return _dicts(doc.get("operations"))


def messages(doc: dict[str, Any]) -> list[Message]:
    """Component messages (references followed) and inline channel messages, in a deterministic order."""
    out = [Message(key, ar._resolve(raw, doc)) for key, raw in _dicts((doc.get("components") or {}).get("messages")).items()]
    for name, channel in channels(doc).items():
        for key, raw in _dicts(channel.get("messages")).items():
            if "$ref" not in raw:
                out.append(Message(f"{name}/{key}", raw))
    return out


def message_gaps(m: Message, doc: dict[str, Any]) -> list[str]:
    out: list[str] = []
    declared = event_type(m.msg)
    if not declared:
        out.append(f"message {m.label}: no event type (set x-passage-type, or name the message after its type)")
    elif not event_type_valid(declared):
        out.append(f"message {m.label}: event type {declared!r} is not dev.passage.<context>.<entity>.<verb>.v<n>")
    if not _described(m.msg):
        out.append(f"message {m.label}: missing title, summary or description")
    if not ar._resolve(m.msg.get("payload"), doc):
        out.append(f"message {m.label}: missing payload schema")
    return out


def channel_gaps(name: str, channel: dict[str, Any], doc: dict[str, Any]) -> list[str]:
    out: list[str] = []
    if not str(channel.get("address") or "").strip():
        out.append(f"channel {name}: missing address")
    for key, raw in sorted((channel.get("messages") or {}).items()):
        if not ar._resolve(raw, doc):
            out.append(f"channel {name}: message {key} is not an object")
    return out


def operation_gaps(name: str, op: dict[str, Any], doc: dict[str, Any]) -> list[str]:
    out: list[str] = []
    if op.get("action") not in ACTIONS:
        out.append(f"operation {name}: action {op.get('action')!r} is not send or receive")
    channel = op.get("channel")
    channel_ref = channel.get("$ref") if isinstance(channel, dict) else None
    if not isinstance(channel_ref, str) or not ar._resolve(channel, doc):
        out.append(f"operation {name}: channel must be a $ref to a channel")
        channel_ref = None
    carried: list[dict[str, Any]] = []
    for raw in op.get("messages") or []:
        ref = raw.get("$ref") if isinstance(raw, dict) else None
        msg = ar._resolve(raw, doc)
        if not msg:
            out.append(f"operation {name}: message reference is not an object")
            continue
        carried.append(msg)
        if channel_ref and not (isinstance(ref, str) and ref.startswith(channel_ref + "/messages/")):
            out.append(f"operation {name}: message {ref or '(inline)'} is not one of channel {channel_ref}'s messages")
    if not _text(op, "title", "summary", "description") and not (carried and all(_described(m) for m in carried)):
        out.append(f"operation {name}: missing title, summary or description (and its messages are not all described)")
    return out


def gaps(doc: dict[str, Any]) -> list[str]:
    """Every problem that keeps an event out of the published reference. Empty list means documented."""
    out: list[str] = []
    for name, channel in channels(doc).items():
        out += channel_gaps(name, channel, doc)
    seen: dict[str, str] = {}
    for m in messages(doc):
        out += message_gaps(m, doc)
        declared = event_type(m.msg)
        if declared in seen:
            out.append(f"message {m.label}: event type {declared} also declared by {seen[declared]}")
        elif declared:
            seen[declared] = m.label
    for name, op in operations(doc).items():
        out += operation_gaps(name, op, doc)
    return out


def _cell(value: Any) -> str:
    text = str(value if value is not None else "").replace("|", "\\|").replace("\n", " ").strip()
    return text or "-"


def _ref_name(node: Any) -> str:
    ref = node.get("$ref") if isinstance(node, dict) else None
    return ref.rsplit("/", 1)[-1].replace("~1", "/").replace("~0", "~") if isinstance(ref, str) else "-"


def _op_summary(op: dict[str, Any], doc: dict[str, Any]) -> str:
    own = _text(op, "title", "summary", "description")
    if own:
        return own
    return "; ".join(_described(ar._resolve(raw, doc)) for raw in op.get("messages") or [] if isinstance(raw, dict))


def _kind(schema: dict[str, Any]) -> str:
    kind = schema.get("type")
    if isinstance(kind, list):
        return "|".join(map(str, kind))
    if kind is None and "$ref" in schema:
        return _ref_name(schema)
    return str(kind or "?")


def render(name: str, doc: dict[str, Any]) -> str:
    """Markdown event reference: operation and event tables, then one section per event type."""
    info = doc.get("info") or {}
    msgs = sorted(messages(doc), key=lambda m: (event_type(m.msg), m.label))
    ops = operations(doc)
    lines = [
        f"# {name} event reference",
        "",
        f"AsyncAPI {doc.get('asyncapi', '?')}, API version {info.get('version', 'unknown')}. "
        "Generated by docs/ops/tools/asyncapi_reference.py from the service's AsyncAPI document; edit the spec, not this page.",
        "",
        f"{len(channels(doc))} channels, {len(ops)} operations, {len(msgs)} events.",
        "",
        "| Operation | Action | Channel | Summary |",
        "|---|---|---|---|",
    ]
    for op_name, op in ops.items():
        lines.append(
            f"| {_cell(op_name)} | {_cell(op.get('action'))} | {_cell(_ref_name(op.get('channel')))} "
            f"| {_cell(_op_summary(op, doc))} |"
        )
    lines += ["", "| Event type | Title | Required payload fields |", "|---|---|---|"]
    for m in msgs:
        payload = ar._resolve(m.msg.get("payload"), doc)
        required = payload.get("required") or []
        fields = ", ".join(f"`{f}`" for f in required) if required else "-"
        lines.append(f"| {_cell(event_type(m.msg))} | {_cell(_described(m.msg))} | {fields} |")
    for m in msgs:
        lines += ["", f"## {event_type(m.msg) or m.label}", ""]
        lines += [text for text in (_text(m.msg, "title", "summary"), _text(m.msg, "description")) if text]
        lines += ["", f"- Message: `{m.label}`"]
        props = ar._resolve(m.msg.get("payload"), doc).get("properties") or {}
        if isinstance(props, dict) and props:
            lines += ["", "Payload fields:", ""]
            for field in sorted(props):
                schema = props[field] if isinstance(props[field], dict) else {}
                lines.append(f"- `{field}` ({_kind(schema)})")
    return "\n".join(lines) + "\n"


def load_spec(path: Path) -> dict[str, Any]:
    if path.suffix.lower() in (".yaml", ".yml"):
        raise ValueError(f"{path}: convert YAML to JSON first (e.g. yq -o=json)")
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict) or not str(data.get("asyncapi", "")).startswith("3."):
        raise ValueError(f"{path}: not an AsyncAPI 3 document")
    return data


def service_name(spec: Path) -> str:
    """`services/<svc>/events/asyncapi.json` is named after <svc>; any other file after its stem."""
    resolved = spec.resolve()
    if resolved.stem == "asyncapi" and resolved.parent.name == "events":
        return resolved.parent.parent.name
    return spec.stem


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--spec", type=Path, action="append", required=True, help="AsyncAPI JSON; repeat per service")
    parser.add_argument("--out", type=Path, help="directory for one <service>.md page per spec")
    args = parser.parse_args(argv)

    problems: list[str] = []
    counted = 0
    try:
        names = [service_name(spec) for spec in args.spec]
        duplicates = sorted({n for n in names if names.count(n) > 1})
        if duplicates:
            raise ValueError(f"two specs map to the same service page: {', '.join(duplicates)}")
        for spec, name in zip(args.spec, names):
            doc = load_spec(spec)
            counted += len(messages(doc))
            problems += [f"{name}: {g}" for g in gaps(doc)]
            if args.out is not None:
                args.out.mkdir(parents=True, exist_ok=True)
                (args.out / f"{name}.md").write_text(render(name, doc), encoding="utf-8")
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    for problem in problems:
        print(f"FAIL  {problem}")
    if problems:
        events = {p.split(": ", 2)[0] + ": " + p.split(": ", 2)[1] for p in problems if ": message " in p}
        print(f"EVENT REFERENCE FAILED: {len(events)} of {counted} events undocumented ({len(problems)} gap(s))")
        return 1
    print(f"EVENT REFERENCE PASSED: {counted} events, 0 undocumented")
    return 0


if __name__ == "__main__":
    sys.exit(main())
