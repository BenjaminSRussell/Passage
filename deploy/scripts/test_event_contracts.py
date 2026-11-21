"""Every Kafka consumer reads only event fields its producer's AsyncAPI schema declares and its producer's code writes (D6,
B-05c), and the search index reads every type it indexes from a relayed topic (ADR-0013).

Consumers are the ones test_kafka_consumers.py finds in the code (Go eventing, kafkajs, kafka-python); the fields each
reads are found in the code too:
- Go: deploy/ci/eventfields type-checks the consumer's module. Every map[string]eventing.Handler a main package reaches
  (through functions, variables and interface methods) is followed from each entry's handler to the json.Unmarshal of the
  event data: of evt.Data itself, of a local copy, or of a []byte/json.RawMessage parameter it is passed in (a func
  value is resolved to its closure or function first; only one that cannot be resolved matches by signature). A further
  Unmarshal of a json.RawMessage field the first decode filled (directly, through a local copy or through a helper that
  takes it) adds that field's nested paths. The decoded types' JSON paths are the fields (Go matches keys
  case-insensitively, so the comparison does). Fail closed: any other use of the data (json.NewDecoder, len, a call the
  check does not follow, json.Marshal of the whole event) is a problem, and so are a decode into a map or `any` and a
  decoded field with no fixed shape (json.RawMessage, []byte, map, interface) that no followed decode reads, unless
  OPEN_DECODES names them.
- TypeScript: each `event as <Stem>Event` cast in the consumer's file, paired with the `<stem>Type`/`<stem>Types`
  constant of the module that declares the interface; the fields are the interface's `data` object type, with member
  types that are interfaces of the same modules expanded. A consumer with no such cast fails unless TS_NO_DATA says why.
- Python: the module-level set the consumer tests the event type against, every key read from the event's data
  (`data.get("k")`, `data["k"]`; `a or b` reads are alternatives, one of which must be declared) and the keys a
  recursive helper looks for at any depth (a `*_KEYS` tuple: at least one must occur in its events).
Each Go consumer group must read, through its own handlers, a type relayed to each topic it subscribes to, so a group
whose handler map the analyser misses fails (GROUP_TOPICS_EXEMPT lists the audit CDC group). A group's handlers are its
service's when the service runs one group; otherwise those of the handler maps of the eventing.Config named after the
group, tied by the analyser through parameters and struct literals.

For every event type a consumer reads, the producer is the service whose AsyncAPI has a `send` operation with that
message; each field must be a property path of its payload schema ($ref, allOf/anyOf/oneOf followed; an object with no
declared properties accepts any key), and each events.New call of the producer's code that writes the type (followed
through helpers to the call that names the type and payload) must write it. The producer side is also checked on its own:
every field those calls write is declared by the schema, and the message's channel address is the topic its outbox
connector routes the type to (by the EventRouter regex, or as depot.<context>.<aggregate> of the aggregate the producer's
outbox.Enqueue calls write it under, traced from the Enqueue back to the events.New). An events.New or outbox.Enqueue call
the analyser cannot resolve in a producer module of a consumed type fails unless UNRESOLVED_EMITS / UNRESOLVED_ROUTES
name that call (by its source text) with the reason; stale entries fail too. NOT_OUTBOX, PRODUCER_CODE_UNSEEN and CHANNEL_MISMATCH list the other
exceptions, each with its reason.

Search: services/search/events/asyncapi.yaml declares a `receive` operation for exactly the topics the consumer
subscribes to and the types its handlers index (one message per type, under the channel of the topic it arrives on, its
payload a $ref to the producer's message). Each producer has an outbox-<service> connector in deploy/kafka/connect.yaml
that relays the type to that topic (for an aggregate-routed producer: the aggregate its code enqueues that type under),
and the debezium user may write the topic. The index side of the count reconciliation (`search reconcile`) is tested in
services/search; nothing produces the source counts yet (#731).

Negative tests mutate schemas, payloads, routes, channels, the unresolved lists and the search AsyncAPI in memory, and
type-check a synthetic module with each decode form, so each rule is shown to fail. Not covered: whether events really
flow on a cluster (#213), and the meaning of a field (a consumer that reads `qty` as a count where the producer writes a
decimal string passes).
"""

import ast
import copy
import functools
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from collections import defaultdict
from dataclasses import dataclass, field
from pathlib import Path

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parent))
import test_argocd_apps as argo  # noqa: E402
import test_kafka_consumers as kc  # noqa: E402

DEPLOY = Path(__file__).resolve().parents[1]
REPO = DEPLOY.parent
EVENTFIELDS = DEPLOY / "ci/eventfields"
SEARCH_ASYNCAPI = REPO / "services/search/events/asyncapi.yaml"

# Handler-map event types that are not outbox CloudEvents, so no AsyncAPI `send` declares them: (service, type) -> why.
NOT_OUTBOX = {
    ("audit", "cdc:context"): "the audit CDC consumer's own decoder (cdc.Decoder) turns Debezium change records of the "
                              "tables in cdc.Tables into these synthetic types; test_argocd_apps checks the tables against "
                              "the audit-cdc-<service> connectors and services/audit/events/asyncapi.yaml documents them",
    ("audit", "cdc:row-change"): "as cdc:context",
}

# Producers whose events.New calls the analyser cannot tie to an event type or payload: (service, type) -> why. The schema
# check still applies to the consumer's fields; only the code comparison is skipped.
PRODUCER_CODE_UNSEEN = {}

# Go decodes of event data into a map or `any`, whose keys the check cannot list: (service, type) -> why. Any other is a
# problem.
OPEN_DECODES = {}

# kafkajs consumers that cast the event to no `<Stem>Event` interface, so the check finds no field they read: service -> why.
TS_NO_DATA = {
    "integration-hub-ts": "forwards each whole event to the subscriptions whose type filter matches it and reads no data "
                          "field (src/consumer.ts copies e.data as is)",
}

# Consumer groups whose subscribed topics no outbox CloudEvent handler covers: group -> why.
GROUP_TOPICS_EXEMPT = {
    "audit-cdc": "reads Debezium change records of tables (depot.cdc.*), turned into the synthetic types NOT_OUTBOX lists",
}

# events.New calls in a producer module of a consumed type whose event type or payload the analyser cannot work out, by
# (file of the call, file of the call that fails to bind its parameters or None, function that call sits in, source text
# of that call) -> why. A new call fails, even in a function already listed, and so does a stale entry: such a call might
# write a consumed type the comparison never sees.
STORE = "services/{}/internal/store/{}.go"
RMA_CALL = 'enqueue(ctx, tx, "returnorder", id, eventType, p.TenantID, rmaEvent(out, actor))'
ACK_CALL = 'enqueue(ctx, tx, "purchase_order_ack", id, ackEventType(in.Outcome), ackEvent(out))'
RMA_WHY = ("the return-order transition picks its event type at run time (rmaEventType(action)); it writes only returnorder "
           "events, which no consumer reads")
ACK_WHY = ("the acknowledgement picks its type at run time (ackEventType(outcome)); it writes only poack events, which no "
           "consumer reads")
ECR_CALL = 'emit(ctx, tx, p, e.ID, typ, aggregateEcr, e.ID, e.EventData())'
ECR_WHY = ("the ECR transition picks its event type from the new state at run time (a map lookup in SaveEcr); it writes only "
           "ecr events, which no consumer reads (workflow-ts reads the eco events)")
UNRESOLVED_EMITS = {
    (STORE.format("engineering", "store"), STORE.format("engineering", "ecr"), "SaveEcr", ECR_CALL): ECR_WHY,
    (STORE.format("sales", "store"), STORE.format("sales", "rma"), "moveReturn", RMA_CALL): RMA_WHY,
    (STORE.format("purchasing", "store"), STORE.format("purchasing", "po_ack"), "AcknowledgePurchaseOrder", ACK_CALL): ACK_WHY,
}

# outbox.Enqueue calls of an aggregate-routed producer of a consumed type whose (event type, aggregate) the analyser
# cannot work out, keyed as UNRESOLVED_EMITS -> (why, {event type: aggregate} the call writes, read from the code). A new
# call or a stale entry fails; the routes stand in for the analyser's in the channel, group and search checks.
SELF_BILL = ("the self-bill event is the second result of the SelfBillBuild callback the caller stores in a struct field "
             "(outcome.SelfBill); putSelfBill enqueues it under SelfBillAggregate", {"dev.depot.receiving.ersselfbill.issued.v1": "ersselfbill"})
UNRESOLVED_ROUTES = {
    (STORE.format("receiving", "servicesheet"), None, "AcceptSheet", "outbox.Enqueue(ctx, tx, aggregate, aggregateID, evt)"): (
        "the accepted sheet's events come from a slice and the aggregate from each event's type at run time",
        {"dev.depot.receiving.receipt.posted.v1": "receipt", "dev.depot.receiving.serviceentrysheet.decided.v1": "serviceentrysheet"}),
    (STORE.format("receiving", "servicesheet"), STORE.format("receiving", "audit"), "ReleaseInvoice",
     "putSelfBill(ctx, tx, tenantID, inv, outcome.SelfBill)"): SELF_BILL,
    (STORE.format("receiving", "servicesheet"), STORE.format("receiving", "invoices"), "PostInvoice",
     "putSelfBill(ctx, tx, tenantID, in, outcome.SelfBill)"): SELF_BILL,
}

# Producer channels whose AsyncAPI address is not the topic outbox-<service> routes the message to: (service, type) -> why.
CHANNEL_MISMATCH = {}


# ---------------------------------------------------------------------------------------------------------------------
# AsyncAPI

class SchemaLoader(yaml.SafeLoader):
    """YAML 1.2 booleans: a property named no, on, yes or off (installments[].no) stays a string."""


SchemaLoader.yaml_implicit_resolvers = {
    k: [(tag, rx) for tag, rx in v if tag != "tag:yaml.org,2002:bool"] for k, v in yaml.SafeLoader.yaml_implicit_resolvers.items()}
SchemaLoader.add_implicit_resolver("tag:yaml.org,2002:bool", re.compile(r"^(?:true|True|TRUE|false|False|FALSE)$"), list("tTfF"))


@functools.lru_cache(maxsize=None)
def load_yaml(path):
    return yaml.load(Path(path).read_text(), Loader=SchemaLoader)  # noqa: S506 - a SafeLoader subclass


def pointer(doc, frag):
    node = doc
    for part in [p for p in frag.lstrip("/").split("/") if p]:
        node = node[part.replace("~1", "/").replace("~0", "~")]
    return node


def deref(node, file):
    """(node, file) with every $ref followed: local (#/...) or a relative file with an optional #fragment."""
    for _ in range(20):
        if isinstance(node, dict) and "x-ctx" in node:  # a branch built by envelope_data, already resolved
            node, file = node["x-ctx"]
            continue
        if not isinstance(node, dict) or "$ref" not in node:
            return node, file
        target, _, frag = node["$ref"].partition("#")
        if target:
            file = (Path(file).parent / target).resolve()
        node = pointer(load_yaml(str(file)), frag)
    raise ValueError(f"{file}: $ref cycle")


@dataclass
class Message:
    service: str
    type: str
    channel: str
    file: Path
    name: str
    payload: object

    def schema(self):
        return envelope_data((self.payload, self.file))


ENVELOPE_KEYS = {"specversion", "type", "source", "id", "subject", "tenantid"}


def envelope_data(ctx):
    """The data schema of a payload that models the whole CloudEvent (properties type, tenantid, ..., data, possibly through
    allOf with a shared Envelope), else the payload itself."""
    branches, names, todo = [], set(), [ctx]
    while todo:
        node, file = deref(*todo.pop())
        if not isinstance(node, dict):
            continue
        props = node.get("properties") or {}
        names |= set(props)
        if "data" in props:
            branches.append((props["data"], file))
        todo += [(sub, file) for k in COMPOSITIONS for sub in node.get(k) or []]
    if not branches or not names & ENVELOPE_KEYS:
        return ctx
    specific = [b for b in branches if not is_open(deref(*b)[0])] or branches
    return ({"anyOf": [{"x-ctx": b} for b in specific]}, ctx[1])


def asyncapi_files():
    return sorted(REPO.glob("services/*/events/asyncapi.yaml")) + sorted(REPO.glob("tools/*/events/asyncapi.yaml"))


def message_type(msg):
    return msg.get("x-depot-type") or msg.get("name")


def producer_messages(files=None):
    """event type -> the messages that a `send` operation of some AsyncAPI publishes with that type."""
    out = defaultdict(list)
    for f in files or asyncapi_files():
        spec = load_yaml(str(f))
        service = f.parts[-3]
        seen = set()
        for op in (spec.get("operations") or {}).values():
            if op.get("action") != "send":
                continue
            channel, _ = deref(op["channel"], f)
            refs = op.get("messages") or list((channel.get("messages") or {}).values())
            for ref in refs:
                msg, mfile = deref(ref, f)
                typ = message_type(msg)
                key = (typ, channel.get("address"))
                if typ and key not in seen:
                    seen.add(key)
                    out[typ].append(Message(service, typ, channel.get("address"), Path(mfile), msg.get("name", typ), msg.get("payload")))
    return out


def tokens(path):
    """'lines[].qty' -> ['lines', '[]', 'qty']."""
    out = []
    for part in path.split("."):
        name = part.rstrip("[]")
        if name:
            out.append(name)
        out += ["[]"] * ((len(part) - len(name)) // 2)
    return out


COMPOSITIONS = ("allOf", "anyOf", "oneOf")


def is_open(node):
    """A schema that declares no structure (no properties, items, composition or closed additionalProperties) accepts any key."""
    if node is True or node == {}:
        return True
    if not isinstance(node, dict):
        return False
    if any(k in node for k in ("properties", "items", "prefixItems", "$ref") + COMPOSITIONS):
        return False
    if node.get("additionalProperties") is False:
        return False
    types = node.get("type")
    types = types if isinstance(types, list) else [types]
    return any(t in (None, "object", "array") for t in types)


def schema_has(ctx, toks, ci=False, depth=0):
    """Whether the schema at ctx = (node, file) declares the path toks."""
    if not toks:
        return True
    if depth > 40:
        return False
    node, file = deref(*ctx)
    if not isinstance(node, dict):
        return node is True
    if any(schema_has((sub, file), toks, ci, depth + 1) for k in COMPOSITIONS for sub in node.get(k) or []):
        return True
    tok, rest = toks[0], toks[1:]
    if tok == "[]":
        if "items" in node:
            return schema_has((node["items"], file), rest, ci, depth + 1)
        return is_open(node)
    props = node.get("properties") or {}
    match = [k for k in props if k == tok] or ([k for k in props if k.lower() == tok.lower()] if ci else [])
    if match:
        return schema_has((props[match[0]], file), rest, ci, depth + 1)
    addl = node.get("additionalProperties")
    if isinstance(addl, dict):
        return schema_has((addl, file), rest, ci, depth + 1)
    if addl is True or node.get("patternProperties"):
        return True
    return is_open(node)


def schema_keys(ctx, depth=0):
    """Every property name the schema declares at any depth (for keys a consumer looks for anywhere)."""
    if depth > 40:
        return set()
    node, file = deref(*ctx)
    if not isinstance(node, dict):
        return set()
    out = set()
    for k in COMPOSITIONS:
        for sub in node.get(k) or []:
            out |= schema_keys((sub, file), depth + 1)
    for name, sub in (node.get("properties") or {}).items():
        out.add(name)
        out |= schema_keys((sub, file), depth + 1)
    for k in ("items", "additionalProperties"):
        if isinstance(node.get(k), dict):
            out |= schema_keys((node[k], file), depth + 1)
    return out


def written(path, emitted, ci):
    """Whether a payload with the JSON paths `emitted` (a producer's) has `path`: equal, or one is a prefix of the other
    (the consumer reads a whole object the producer fills, or the producer writes a value that ends the path)."""
    want = tokens(path)
    norm = (lambda t: [x.lower() for x in t]) if ci else (lambda t: t)
    want = norm(want)
    for e in emitted:
        have = norm(tokens(e))
        n = min(len(want), len(have))
        if want[:n] == have[:n]:
            return True
    return False


# ---------------------------------------------------------------------------------------------------------------------
# Go (deploy/ci/eventfields)

def go_command():
    return shutil.which("go")


@functools.lru_cache(maxsize=None)
def eventfields(dirs):
    """The analyser's report per module directory (repo-relative), for a tuple of directories."""
    if not dirs:
        return {}
    return {r["dir"]: r for r in run_eventfields(str(REPO), [str(REPO / d) for d in dirs])}


def run_eventfields(root, dirs):
    out = subprocess.run([go_command(), "run", ".", "-root", root] + dirs, cwd=EVENTFIELDS, capture_output=True, text=True)
    if out.returncode != 0:
        raise AssertionError(f"deploy/ci/eventfields failed (exit {out.returncode}):\n{out.stderr}")
    return json.loads(out.stdout)


def needs_go(cls):
    """Skip without Go only outside CI: in CI a missing toolchain fails the job instead of passing it empty."""
    if go_command() or os.environ.get("CI"):
        return cls
    return unittest.skip("go not on PATH")(cls)


def go_module_dir(service):
    root = argo.service_root(service)
    return root.relative_to(REPO).as_posix() if (root / "go.mod").is_file() else None


# ---------------------------------------------------------------------------------------------------------------------
# What each consumer reads

@dataclass
class Read:
    """The fields one consumer reads from one event type. fields: requirements, each a tuple of alternative paths of which
    one must be declared; anywhere: keys of which one must occur at some depth; ci: keys match case-insensitively (Go)."""
    consumer: str
    service: str
    type: str
    at: str
    fields: list = field(default_factory=list)
    anywhere: tuple = ()
    ci: bool = False
    site: str = ""  # Go: the handler map the entry belongs to


def go_reads(reports, consumers):
    """Reads of every handler entry of the Go consumer services, and the analyser's problems."""
    out, problems = [], []
    by_service = defaultdict(list)
    for c in consumers:
        by_service[c.service].append(c)
    for service, cs in sorted(by_service.items()):
        report = reports.get(go_module_dir(service)) or reports[service]
        problems += [f"{service}: {p}" for p in report["problems"]]
        groups = ", ".join(sorted(g for c in cs for g in c.groups))
        if not report["handlers"]:
            problems.append(f"{service}: a consumer ({groups}) whose handler map the analyser does not find")
        for h in report["handlers"]:
            if (service, h["type"]) in NOT_OUTBOX:
                continue
            problems += [f"{service} {h['type']}: {p}" for p in h.get("problems") or []]
            r = Read(f"{service} ({groups})", service, h["type"], h["at"], ci=True, site=h.get("map", ""))
            for d in h["decodes"]:
                if (service, h["type"]) not in OPEN_DECODES:
                    if d.get("open"):
                        problems.append(f"{service} {h['type']}: decodes the event data into {d['goType']} at {d['at']}, whose "
                                        "keys the check cannot list; decode into a struct (or list it in OPEN_DECODES with the reason)")
                    for f in d["fields"]:
                        if f.get("dynamic"):
                            problems.append(f"{service} {h['type']}: field {f['path']} of the decode at {d['at']} has no fixed shape "
                                            "(json.RawMessage, []byte, a map or an interface) and no nested decode the check follows "
                                            "reads it; decode it into a struct (or list it in OPEN_DECODES with the reason)")
                r.fields += [(f["path"],) for f in d["fields"]]
            out.append(r)
    return out, problems


def group_reads(consumer, reads, reports):
    """The reads of one Go consumer group: every read of its service when the service runs one group; otherwise the reads
    of the handler maps of the eventing.Config whose Name is the group (multi-group services name each consumer after its
    group). None when the group cannot be tied to its handler maps."""
    service_reads = [r for r in reads if r.service == consumer.service]
    groups_of_service = reports.get("__groups__", {}).get(consumer.service, 1)
    if groups_of_service == 1:
        return service_reads
    report = reports.get(go_module_dir(consumer.service)) or reports.get(consumer.service) or {}
    group = next(iter(consumer.groups))
    maps = {m for c in report.get("consumers") or [] if group in c["names"] or group.rstrip("-") in c["names"] for m in c["maps"]}
    if not maps:
        return None
    return [r for r in service_reads if r.site in maps]


def group_topic_problems(consumers, reads, topics_of, reports):
    """Each Go consumer group's subscribed topics are each the topic of at least one type a handler of that group reads (its
    own handler maps, tied through its eventing.Config), so a group whose handler map the analyser does not reach cannot
    pass unseen. topics_of(type) is the set of topics the type's producers relay it to."""
    problems = []
    counts = defaultdict(int)
    for c in consumers:
        counts[c.service] += 1
    reports = dict(reports, __groups__=counts)
    for c in consumers:
        group = next(iter(c.groups))
        if group.rstrip("-") in GROUP_TOPICS_EXEMPT or group in GROUP_TOPICS_EXEMPT:
            continue
        rs = group_reads(c, reads, reports)
        if rs is None:
            problems.append(f"{c.service} group {group} ({c.where}): no eventing.Config named {group} whose handler maps the "
                            "check finds, so it cannot tell what the group reads")
            continue
        covered = set()
        for r in rs:
            covered |= topics_of(r.type)
        for t in sorted(c.topics - covered):
            problems.append(f"{c.service} group {group} ({c.where}) subscribes to {t}, but no handler of the group the check "
                            "finds reads a type relayed there; the check cannot see what the group reads")
    return problems


TS_CAST = re.compile(r"\bas\s+([A-Z]\w*Event)\b")
TS_TYPE_CONST = re.compile(r"""export\s+const\s+(\w+?Types?)\s*=\s*(\[[^\]]*\]|"[^"]*"|'[^']*')""")


def ts_members(body):
    """Top-level members of a TypeScript object type body, split on ; , and newlines outside brackets."""
    out, depth, start = [], 0, 0
    for i, ch in enumerate(body):
        if ch in "{[(<":
            depth += 1
        elif ch in "}])>":
            depth -= 1
        elif depth == 0 and ch in ";,\n":
            out.append(body[start:i])
            start = i + 1
    out.append(body[start:])
    return [m.strip() for m in out if m.strip()]


def ts_fields(body, prefix="", file=None, depth=0):
    """JSON paths of a TypeScript object type literal's body; a member typed by an interface declared in file (or a module
    it imports from) is expanded."""
    out = []
    for member in ts_members(body):
        m = re.match(r"^(?:readonly\s+)?[\"']?(\w+)[\"']?\??\s*:\s*(.*)$", member, re.S)
        if not m:
            continue
        name, typ = m.group(1), m.group(2).strip()
        path = f"{prefix}.{name}" if prefix else name
        arrays = ""
        while True:
            if typ.endswith("[]"):
                typ, arrays = typ[:-2].strip(), arrays + "[]"
            elif re.match(r"^(?:Readonly)?Array<.*>$", typ, re.S):
                typ, arrays = typ[typ.index("<") + 1:-1].strip(), arrays + "[]"
            else:
                break
        if typ.startswith("{") and typ.endswith("}") and typ[1:-1].strip():
            out += ts_fields(typ[1:-1], path + arrays, file, depth + 1)
            continue
        if file is not None and depth < 8 and re.fullmatch(r"[A-Z]\w*", typ):
            nfile, nbody = ts_interface(typ, file)
            if nbody is not None and nbody.strip():
                out += ts_fields(nbody, path + arrays, nfile, depth + 1)
                continue
        out.append(path + arrays)
    return out


def ts_interface(name, file):
    """(file, the interface's body) for an interface declared in file or a module it imports name from."""
    text = kc.strip_comments(file.read_text())
    m = re.search(rf"\binterface\s+{name}\b[^{{]*\{{", text)
    if m:
        o = m.end() - 1
        return file, text[o + 1:kc.match(text, o)]
    imp = re.search(rf"""import\s*(?:type\s*)?\{{[^}}]*\b{name}\b[^}}]*\}}\s*from\s*["'](\.[^"']+)["']""", text)
    if imp:
        target = (file.parent / imp.group(1))
        for cand in (target, target.with_suffix(".ts"), Path(str(target).removesuffix(".js") + ".ts")):
            if cand.is_file() and cand != file:
                return ts_interface(name, cand)
    return None, None


def ts_reads(consumers):
    """Reads of the kafkajs consumers: each `event as <Stem>Event` cast in the consumer's file, the interface's `data`
    fields and the event types of the `<stem>Type(s)` constant beside it."""
    out, problems = [], []
    for service, where, values in consumers:
        file = REPO / where
        text = kc.strip_comments(file.read_text())
        casts = sorted(set(TS_CAST.findall(text)))
        if not casts and service not in TS_NO_DATA:
            problems.append(f"{where}: a kafkajs consumer that casts the event to no <Stem>Event interface, so the check finds "
                            "no field it reads (list it in TS_NO_DATA with the reason if it reads none)")
        for iface in casts:
            ifile, body = ts_interface(iface, file)
            if body is None:
                problems.append(f"{where}: casts the event to {iface}, whose declaration the check does not find")
                continue
            members = {m.split(":", 1)[0].strip().rstrip("?"): m.split(":", 1)[1].strip() for m in ts_members(body) if ":" in m}
            data = members.get("data", "")
            if not (data.startswith("{") and data.endswith("}")):
                problems.append(f"{where}: {iface} has no `data` object type")
                continue
            stem = iface[0].lower() + iface[1:-len("Event")]
            consts = {m.group(1): m.group(2) for m in TS_TYPE_CONST.finditer(kc.strip_comments(ifile.read_text()))}
            const = next((consts[k] for k in (stem + "Type", stem + "Types") if k in consts), None)
            if const is None:
                problems.append(f"{rel(ifile)}: {iface} has no {stem}Type or {stem}Types constant naming the event types it reads")
                continue
            types = re.findall(r"""["']([^"']+)["']""", const)
            fields = ts_fields(data[1:-1], file=ifile)
            for typ in types:
                out.append(Read(f"{service} ({iface})", service, typ, rel(ifile), [(f,) for f in fields]))
    return out, problems


def rel(path):
    return Path(path).resolve().relative_to(REPO).as_posix()


def py_value(node, module):
    """A module-level constant expression: strings, tuples/lists/sets, names, +, frozenset/tuple/set() and comprehensions."""
    if isinstance(node, ast.Constant):
        return node.value
    if isinstance(node, (ast.Tuple, ast.List, ast.Set)):
        return tuple(py_value(e, module) for e in node.elts)
    if isinstance(node, ast.Name):
        for stmt in module.body:
            if isinstance(stmt, ast.Assign) and any(isinstance(t, ast.Name) and t.id == node.id for t in stmt.targets):
                return py_value(stmt.value, module)
            if isinstance(stmt, ast.AnnAssign) and isinstance(stmt.target, ast.Name) and stmt.target.id == node.id and stmt.value:
                return py_value(stmt.value, module)
        raise ValueError(f"no module constant {node.id}")
    if isinstance(node, ast.BinOp) and isinstance(node.op, ast.Add):
        return py_value(node.left, module) + py_value(node.right, module)
    if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id in ("frozenset", "set", "tuple", "list") \
            and len(node.args) == 1:
        return tuple(py_value(node.args[0], module))
    if isinstance(node, (ast.GeneratorExp, ast.ListComp, ast.SetComp)) and len(node.generators) == 1:
        gen = node.generators[0]
        if isinstance(gen.target, ast.Name) and not gen.ifs:
            out = []
            for item in py_value(gen.iter, module):
                bound = ast.Module(body=[ast.Assign(targets=[ast.Name(gen.target.id)], value=ast.Constant(item))] + module.body,
                                   type_ignores=[])
                out.append(py_value(node.elt, bound))
            return tuple(out)
    raise ValueError(f"not a module constant: {ast.unparse(node)}")


def py_reads(consumers):
    """Reads of the kafka-python consumers (see the module docstring)."""
    out, problems = [], []
    for service, where, _ in consumers:
        file = REPO / where.split(":")[0]
        module = ast.parse(file.read_text())
        funcs = {f.name: f for f in module.body if isinstance(f, ast.FunctionDef)}
        found = False
        for fn in funcs.values():
            handled = None
            for node in ast.walk(fn):
                # env["type"] not in HANDLED / env.get("type") in HANDLED
                if isinstance(node, ast.Compare) and len(node.ops) == 1 and isinstance(node.ops[0], (ast.In, ast.NotIn)) \
                        and re.fullmatch(r"""\w+(\[["']type["']\]|\.get\(["']type["']\))""", ast.unparse(node.left)) \
                        and isinstance(node.comparators[0], ast.Name):
                    handled = node.comparators[0]
            if handled is None:
                continue
            found = True
            try:
                types = py_value(handled, module)
            except ValueError as e:
                problems.append(f"{where}: {e}")
                continue
            data_names = {t.id for node in ast.walk(fn) if isinstance(node, ast.Assign)
                          for t in node.targets if isinstance(t, ast.Name)
                          and re.fullmatch(r"""\w+\.get\(["']data["']\)( if .*)?""", ast.unparse(node.value))}
            if not data_names:
                problems.append(f"{where}: {fn.name} tests the event type but reads no `data` the check can follow")
                continue

            def key_of(node):
                s = ast.unparse(node)
                m = re.fullmatch(r"""(\w+)(?:\.get\(["'](\w+)["']\)|\[["'](\w+)["']\])""", s)
                return (m.group(2) or m.group(3)) if m and m.group(1) in data_names else None
            requirements, consumed = [], set()
            for node in ast.walk(fn):
                if isinstance(node, ast.BoolOp) and isinstance(node.op, ast.Or):
                    alts = [k for k in (key_of(v) for v in node.values) if k]
                    if alts:
                        requirements.append(tuple(alts))
                        consumed |= {id(v) for v in node.values}
            for node in ast.walk(fn):
                if id(node) not in consumed and key_of(node):
                    requirements.append((key_of(node),))
            anywhere = ()
            for node in ast.walk(fn):
                if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id in funcs \
                        and any(isinstance(a, ast.Name) and a.id in data_names for a in node.args):
                    helper = funcs[node.func.id]
                    recursive = any(isinstance(n, ast.Call) and isinstance(n.func, ast.Name) and n.func.id == helper.name
                                    for n in ast.walk(helper))
                    for n in ast.walk(helper):
                        if recursive and isinstance(n, ast.Compare) and isinstance(n.ops[0], ast.In) \
                                and isinstance(n.comparators[0], ast.Name) and n.comparators[0].id.endswith("_KEYS"):
                            anywhere += tuple(py_value(n.comparators[0], module))
            for typ in types:
                out.append(Read(f"{service} ({fn.name})", service, typ, f"{rel(file)}:{fn.lineno}", sorted(set(requirements)),
                                tuple(sorted(set(anywhere)))))
        if not found:
            problems.append(f"{where}: a kafka-python consumer whose event type test the check does not find")
    return out, problems


# ---------------------------------------------------------------------------------------------------------------------
# The comparison

def contract_problems(reads, messages, emits):
    """Problems of each read against the producers' schemas and code. emits: (service, type) -> the analyser's emits, or
    None when the producer's code was not analysed (not Go)."""
    problems = []
    anywhere = defaultdict(lambda: ([], []))
    for r in reads:
        if (r.service, r.type) in NOT_OUTBOX:
            continue
        producers = messages.get(r.type, [])
        if not producers:
            problems.append(f"{r.consumer}: reads {r.type} ({r.at}), which no AsyncAPI `send` operation declares")
            continue
        for m in producers:
            where = f"{rel(m.file)} {m.name}"
            for alts in r.fields:
                if not any(schema_has(m.schema(), tokens(a), r.ci) for a in alts):
                    problems.append(f"{r.consumer}: reads {' or '.join(alts)} of {r.type} ({r.at}); {where} does not declare it")
            if r.anywhere:
                anywhere[(r.consumer, r.anywhere)][0].append(bool(set(r.anywhere) & schema_keys(m.schema())))
            code = emits.get((m.service, r.type))
            if code is None or (m.service, r.type) in PRODUCER_CODE_UNSEEN:
                continue
            if not code:
                problems.append(f"{r.consumer}: reads {r.type}, but no events.New call of {m.service}'s code writes it "
                                "(list it in PRODUCER_CODE_UNSEEN with the reason if the analyser cannot see it)")
                continue
            for e in code:
                if e.get("open"):
                    continue
                paths = [f["path"] for f in e["fields"]]
                site = e["at"] + (f" via {e['via']}" if e.get("via") else "")
                for alts in r.fields:
                    if not any(written(a, paths, r.ci) for a in alts):
                        problems.append(f"{r.consumer}: reads {' or '.join(alts)} of {r.type} ({r.at}); {m.service} writes "
                                        f"{e['goType']} at {site}, which has no such field")
                if r.anywhere:
                    anywhere[(r.consumer, r.anywhere)][1].append(bool({k for p in paths for k in tokens(p)} & set(r.anywhere)))
    # Keys looked for anywhere are optional per event (prediction-py rescores the whole legal entity and only reports the
    # open items it finds), but a consumer that finds them in none of its events reads a key no producer writes.
    for (consumer, keys), (declared, coded) in sorted(anywhere.items()):
        if declared and not any(declared):
            problems.append(f"{consumer}: looks for {' or '.join(keys)} anywhere in its events; no producer schema declares one")
        if coded and not any(coded):
            problems.append(f"{consumer}: looks for {' or '.join(keys)} anywhere in its events; no producer code writes one")
    return problems


def producer_problems(types, messages, emits):
    """Each field the producers' code writes for the given types is declared by their schema."""
    problems = []
    for typ in sorted(types):
        for m in messages.get(typ, []):
            for e in emits.get((m.service, typ)) or []:
                if e.get("open"):
                    continue
                site = e["at"] + (f" via {e['via']}" if e.get("via") else "")
                for f in e["fields"]:
                    if not schema_has(m.schema(), tokens(f["path"])):
                        problems.append(f"{m.service} writes {f['path']} in {typ} ({e['goType']} at {site}); "
                                        f"{rel(m.file)} {m.name} does not declare it")
    return problems


def emits_by_type(reports):
    out = defaultdict(list)
    for d, r in reports.items():
        service = Path(d).name
        for e in r["emits"]:
            out[(service, e["type"])].append(e)
    return out


def routes_by_type(reports):
    """(service, event type) -> the aggregate types its outbox.Enqueue calls write it under: the analyser's, and those
    UNRESOLVED_ROUTES states for the calls it cannot trace."""
    out = defaultdict(set)
    for d, r in reports.items():
        for rt in r.get("routes") or []:
            out[(Path(d).name, rt["type"])].add(rt["aggregate"])
    for (file, _, _, _), (_, routes) in UNRESOLVED_ROUTES.items():
        service = file.split("/")[1]
        for typ, agg in routes.items():
            out[(service, typ)].add(agg)
    return out


def routed_topics(service, typ, conns, routes):
    """The topics outbox-<service> relays events of type typ to: through the EventRouter regex for a connector that routes by
    event type, as depot.<context>.<aggregate> for each aggregate the code enqueues the type under for one that routes by
    aggregate_type. None when the service has no outbox connector."""
    cfg = conns.get(service)
    if cfg is None:
        return None
    if cfg.get("transforms.outbox.route.by.field") == "type":
        matched, topic = argo.outbox_route(cfg, typ)
        return {topic} if matched else set()
    prefix = cfg["transforms.outbox.route.topic.replacement"].split("${", 1)[0]
    return {prefix + agg for agg in routes.get((service, typ), ())}


def channel_problems(types, messages, conns, routes):
    """Each producer's AsyncAPI channel for a consumed type is the topic its outbox connector routes the type to."""
    problems = []
    for typ in sorted(types):
        for m in messages.get(typ, []):
            topics = routed_topics(m.service, typ, conns, routes)
            if topics is None or (m.service, typ) in CHANNEL_MISMATCH:
                continue
            if not topics:
                problems.append(f"{typ}: the check cannot tell where outbox-{m.service} routes it (no outbox.Enqueue of the "
                                "type traced to an aggregate)")
            elif m.channel not in topics:
                problems.append(f"{typ}: {rel(m.file)} declares channel {m.channel}, but outbox-{m.service} routes it to "
                                f"{', '.join(sorted(topics))}")
    return problems


def unresolved_problems(reports, services, key, allowed):
    """The analyser's unresolved calls (report[key]) in the given services' modules, against the allowlist keyed (file of the
    call, file of the call that failed to bind it or None, its function): a new one and a stale entry are problems."""
    problems, seen = [], set()
    dirs = {go_module_dir(s) for s in services} - {None}
    for d in sorted(dirs):
        for u in reports[d].get(key) or []:
            k = (u["at"].rsplit(":", 1)[0], u["via"].rsplit(":", 1)[0] if u.get("via") else None, u["func"], u.get("text", ""))
            seen.add(k)
            if k not in allowed:
                problems.append(f"{u['at']}{' via ' + u['via'] if u.get('via') else ''} ({u['func']}): {u['reason']}; the "
                                f"check cannot see what this call writes (list {k!r} with the reason if it writes no consumed type)")
    for k in sorted(allowed, key=str):
        if any(k[0].startswith(d + "/") for d in dirs) and k not in seen:
            problems.append(f"{k}: listed as unresolved, but the analyser resolves it now (or it is gone); drop the entry")
    return problems


def topics_of(typ, messages, conns, routes):
    """Where events of type typ arrive: the routed topics of each producer, its declared channel when it has no connector."""
    out = set()
    for m in messages.get(typ, []):
        topics = routed_topics(m.service, typ, conns, routes)
        out |= topics if topics is not None else {m.channel}
    return out


# ---------------------------------------------------------------------------------------------------------------------
# Search sources (ADR-0013)

def connectors():
    return {d["metadata"]["name"].removeprefix("outbox-"): d["spec"]["config"] for d in argo.load_docs(DEPLOY / "kafka/connect.yaml")
            if argo.is_outbox_connector(d)}


def debezium_writable():
    (user,) = [d for d in argo.load_docs(DEPLOY / "kafka/connect.yaml") if d["kind"] == "KafkaUser" and d["metadata"]["name"] == "debezium"]
    acls = [a["resource"] for a in user["spec"]["authorization"]["acls"] if a["resource"]["type"] == "topic" and "Write" in a["operations"]]
    return lambda topic: any(topic == r["name"] if r.get("patternType", "literal") == "literal" else topic.startswith(r["name"]) for r in acls)


def search_receives(spec, file=SEARCH_ASYNCAPI):
    """(topic, event type, message) of every receive operation of the search AsyncAPI, and its problems."""
    def local(node):
        # Local references resolve in spec as given (the negative tests pass a mutated copy), others through the files.
        while isinstance(node, dict) and str(node.get("$ref", "")).startswith("#"):
            node = pointer(spec, node["$ref"][1:])
        return deref(node, file)
    out, problems = [], []
    for name, op in (spec.get("operations") or {}).items():
        if op.get("action") != "receive":
            problems.append(f"operation {name} is not a receive operation; search publishes nothing")
            continue
        channel, _ = local(op["channel"])
        refs = op.get("messages") or list((channel.get("messages") or {}).values())
        for ref in refs:
            msg, mfile = local(ref)
            out.append((channel["address"], message_type(msg), msg, Path(mfile)))
    return out, problems


def search_source_problems(spec, handled, topics, messages, conns, writable, routes):
    """The search AsyncAPI against the consumer's code and the connectors (see the module docstring)."""
    receives, problems = search_receives(spec)
    declared_topics = {t for t, _, _, _ in receives}
    declared_types = {typ for _, typ, _, _ in receives}
    for t in sorted(set(topics) - declared_topics):
        problems.append(f"the consumer subscribes to {t}, which no receive operation of {rel(SEARCH_ASYNCAPI)} declares")
    for t in sorted(declared_topics - set(topics)):
        problems.append(f"{rel(SEARCH_ASYNCAPI)} receives from {t}, which the consumer does not subscribe to")
    for typ in sorted(set(handled) - declared_types):
        problems.append(f"the index handles {typ}, which no receive operation of {rel(SEARCH_ASYNCAPI)} declares")
    for typ in sorted(declared_types - set(handled)):
        problems.append(f"{rel(SEARCH_ASYNCAPI)} receives {typ}, which no index handler handles")
    for topic, typ, msg, mfile in sorted(receives, key=lambda r: (r[0], r[1])):
        producers = messages.get(typ, [])
        if not producers:
            problems.append(f"{typ} on {topic}: no AsyncAPI send operation declares it")
            continue
        payload = msg.get("payload")
        if not (isinstance(payload, dict) and "$ref" in payload and not payload["$ref"].startswith("#")):
            problems.append(f"{typ} on {topic}: the payload must be a $ref to the producer's message payload, not a copy")
        else:
            node, pfile = deref(payload, mfile)
            if not any(Path(pfile).resolve() == Path(f).resolve() and node is n
                       for n, f in (deref(m.payload, m.file) for m in producers)):
                problems.append(f"{typ} on {topic}: the payload is not the producer's ({', '.join(rel(m.file) for m in producers)})")
        for m in producers:
            cfg = conns.get(m.service)
            if cfg is None:
                problems.append(f"{typ}: its producer {m.service} has no outbox-{m.service} connector in deploy/kafka/connect.yaml, "
                                "so the index never sees it (D2)")
                continue
            if cfg.get("transforms.outbox.route.by.field") == "type":
                matched, routed = argo.outbox_route(cfg, typ)
                if not matched or routed != topic:
                    problems.append(f"{typ}: outbox-{m.service} routes it to {routed if matched else 'no topic'}, the index reads {topic}")
            else:
                prefix = cfg["transforms.outbox.route.topic.replacement"].split("${", 1)[0]
                aggs = sorted(routes.get((m.service, typ), ()))
                if not aggs:
                    problems.append(f"{typ}: outbox-{m.service} routes by aggregate type, and no outbox.Enqueue of {m.service}'s "
                                    "code is traced to this type, so the check cannot tell which topic it lands on")
                elif topic not in {prefix + a for a in aggs}:
                    problems.append(f"{typ}: {m.service} enqueues it under aggregate {', '.join(aggs)}, so outbox-{m.service} "
                                    f"routes it to {', '.join(prefix + a for a in aggs)}; the index reads {topic}")
            if not writable(topic):
                problems.append(f"{typ}: the debezium user may not write {topic}")
    return problems


# ---------------------------------------------------------------------------------------------------------------------

@needs_go
class EventContractsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.go, _, go_disc = kc.go_consumers()
        cls.ts, ts_disc = kc.ts_consumers()
        cls.py, py_disc = kc.py_consumers()
        cls.discovery_problems = go_disc + ts_disc + py_disc
        cls.messages = producer_messages()
        consumer_dirs = {go_module_dir(c.service) for c in cls.go}
        cls.reads, cls.read_problems = [], []
        reports = eventfields(tuple(sorted(d for d in consumer_dirs if d)))
        for reader, args in ((go_reads, (reports, cls.go)), (ts_reads, (cls.ts,)), (py_reads, (cls.py,))):
            reads, problems = reader(*args)
            cls.reads += reads
            cls.read_problems += problems
        cls.read_types = {r.type for r in cls.reads if (r.service, r.type) not in NOT_OUTBOX}
        producer_services = {m.service for t in cls.read_types for m in cls.messages.get(t, [])}
        producer_dirs = {go_module_dir(s) for s in producer_services} - {None}
        cls.reports = dict(reports)
        cls.reports.update(eventfields(tuple(sorted(producer_dirs))))
        found = emits_by_type(cls.reports)
        cls.emits = {}
        for s in producer_services:
            for t in cls.read_types:
                if go_module_dir(s) is not None:
                    cls.emits[(s, t)] = found.get((s, t), [])
        cls.producer_services = producer_services
        cls.conns = connectors()
        cls.routes = routes_by_type(cls.reports)

    def test_consumers_are_found_and_their_reads_worked_out(self):
        self.assertEqual([], self.discovery_problems)
        self.assertEqual([], self.read_problems)
        services = {r.service for r in self.reads}
        for s in ("search", "inventory", "fulfillment", "workflow-ts", "prediction-py"):
            self.assertIn(s, services)
        self.assertGreater(len(self.reads), 60, "too few handler entries found; did the analyser stop following handlers?")
        # integration-hub-ts forwards whole events to subscribers and reads no data field, so it has no reads to check.
        self.assertIn("integration-hub-ts", {service for service, _, _ in self.ts})
        self.assertNotIn("integration-hub-ts", services)

    def test_every_consumer_reads_only_fields_its_producer_declares_and_writes(self):
        by_consumer = defaultdict(list)
        for r in self.reads:
            by_consumer[r.consumer].append(r)
        for consumer, reads in sorted(by_consumer.items()):
            with self.subTest(consumer=consumer):
                self.assertEqual([], contract_problems(reads, self.messages, self.emits))

    def test_producers_write_only_fields_their_schema_declares(self):
        self.assertEqual([], producer_problems(self.read_types, self.messages, self.emits))

    def test_every_consumer_group_reads_a_type_on_each_of_its_topics(self):
        self.assertEqual([], group_topic_problems(self.go, self.reads, self.topics_of, self.reports))

    def topics_of(self, typ):
        return topics_of(typ, self.messages, self.conns, self.routes)

    def test_producer_channels_are_the_topics_their_connector_routes_to(self):
        self.assertEqual([], channel_problems(self.read_types, self.messages, self.conns, self.routes))

    def test_unresolved_producer_calls_are_listed(self):
        aggregate_routed = {s for s in self.producer_services
                            if (self.conns.get(s) or {}).get("transforms.outbox.route.by.field") == "aggregate_type"}
        self.assertEqual([], unresolved_problems(self.reports, self.producer_services, "unresolved", UNRESOLVED_EMITS))
        self.assertEqual([], unresolved_problems(self.reports, aggregate_routed, "unresolvedRoutes", UNRESOLVED_ROUTES))

    def test_a_group_whose_handlers_are_not_found_fails(self):
        reads = [r for r in self.reads if not (r.service == "production" and "shopfloor" in r.type)]
        problems = group_topic_problems(self.go, reads, self.topics_of, self.reports)
        self.assertTrue(any("production-shopfloor" in p and "depot.shopfloor.registration" in p for p in problems), problems)
        # Per group, not pooled: shopfloor's reads moved to the planning map's site no longer cover the shopfloor group.
        planning = next(r.site for r in self.reads if r.service == "production" and "plannedorder" in r.type)
        moved = [copy.copy(r) for r in self.reads]
        for r in moved:
            if r.service == "production":
                r.site = planning
        problems = group_topic_problems(self.go, moved, self.topics_of, self.reports)
        self.assertTrue(any("production-shopfloor" in p for p in problems), problems)
        reports = copy.deepcopy(self.reports)
        reports["services/production"]["consumers"] = []
        problems = group_topic_problems(self.go, self.reads, self.topics_of, reports)
        self.assertTrue(any("no eventing.Config named production-planning" in p for p in problems), problems)

    def test_a_channel_that_is_not_the_routed_topic_fails(self):
        messages = copy.deepcopy(self.messages)
        for m in messages["dev.depot.payables.invoice.posted.v1"]:
            m.channel = "depot.payables.invoice"
        problems = channel_problems({"dev.depot.payables.invoice.posted.v1"}, messages, self.conns, self.routes)
        self.assertTrue(any("depot.payables.invoice" in p and "routes it to depot.receiving.vendorinvoice" in p for p in problems), problems)

    def test_a_new_or_stale_unresolved_call_fails(self):
        reports = copy.deepcopy(self.reports)
        reports["services/sales"]["unresolved"].append(
            {"at": "services/sales/internal/store/store.go:1", "via": "services/sales/internal/store/x.go:2", "func": "newHelper",
             "reason": "not a constant", "text": "enqueue(x)"})
        # A second unresolved call in a function that is already listed is a new call too.
        reports["services/sales"]["unresolved"].append(
            {"at": "services/sales/internal/store/store.go:430", "via": "services/sales/internal/store/rma.go:999",
             "func": "moveReturn", "reason": "not a constant", "text": "enqueue(ctx, tx, \"returnorder\", id, other, p.TenantID, x)"})
        problems = unresolved_problems(reports, {"sales"}, "unresolved", UNRESOLVED_EMITS)
        self.assertTrue(any("newHelper" in p for p in problems), problems)
        self.assertTrue(any("moveReturn" in p and "other" in p for p in problems), problems)
        stale = {**UNRESOLVED_EMITS, ("services/sales/internal/store/gone.go", None, "gone", "gone()"): "x"}
        self.assertTrue(any("gone" in p for p in unresolved_problems(self.reports, {"sales"}, "unresolved", stale)))

    def test_exemptions_are_still_needed(self):
        handled = {(Path(d).name, h["type"]) for d, r in self.reports.items() for h in r["handlers"]}
        self.assertEqual([], sorted(set(NOT_OUTBOX) - handled), "NOT_OUTBOX entries no consumer handles any more")
        for key in PRODUCER_CODE_UNSEEN:
            self.assertEqual([], self.emits.get(key, []), f"{key}: the analyser now sees the producer; drop the exemption")

    # Negative tests: each rule fails on a mutated schema, payload or consumer.

    def test_a_field_dropped_from_the_producer_schema_fails(self):
        read = next(r for r in self.reads if r.service == "search" and r.type == "dev.depot.sales.order.confirmed.v1")
        messages = copy.deepcopy(self.messages)
        (m,) = messages[read.type]
        node, file = deref(*m.schema())
        node = copy.deepcopy(node)
        node["properties"].pop("order_number", None)
        node["required"] = [k for k in node.get("required", []) if k != "order_number"]
        m.payload, m.file = node, file
        problems = contract_problems([read], messages, self.emits)
        self.assertTrue(any("order_number" in p and "does not declare it" in p for p in problems), problems)

    def test_a_field_the_producer_code_does_not_write_fails(self):
        read = next(r for r in self.reads if r.service == "inventory" and r.type == "dev.depot.receiving.receipt.posted.v1")
        emits = copy.deepcopy(self.emits)
        for e in emits[("receiving", read.type)]:
            e["fields"] = [f for f in e["fields"] if f["path"] != "lines[].unit_price"]
        problems = contract_problems([read], self.messages, emits)
        self.assertTrue(any("lines[].unit_price" in p and "has no such field" in p for p in problems), problems)
        emits[("receiving", read.type)] = []
        self.assertTrue(any("no events.New call" in p for p in contract_problems([read], self.messages, emits)))

    def test_a_field_the_producer_writes_but_does_not_declare_fails(self):
        typ = "dev.depot.sales.order.confirmed.v1"
        emits = copy.deepcopy(self.emits)
        emits[("sales", typ)][0]["fields"].append({"path": "undeclared_total"})
        problems = producer_problems({typ}, self.messages, emits)
        self.assertTrue(any("undeclared_total" in p for p in problems), problems)

    def test_ts_and_python_reads_fail_on_a_missing_field(self):
        ts = next(r for r in self.reads if r.service == "workflow-ts" and r.type == "dev.depot.tms.load.released.v1")
        self.assertIn(("legalEntityScope[]",), ts.fields)
        py = next(r for r in self.reads if r.service == "prediction-py" and r.type == "dev.depot.receivables.payment.received.v1")
        self.assertIn(("legal_entity_id", "legalEntityId"), py.fields)
        self.assertEqual(("credit_item_id", "open_item_id"), py.anywhere)
        bad = copy.deepcopy(ts)
        bad.fields = bad.fields + [("noSuchField",)]
        self.assertTrue(any("noSuchField" in p for p in contract_problems([bad], self.messages, self.emits)))
        bad = [copy.deepcopy(r) for r in self.reads if r.service == "prediction-py"]
        for r in bad:
            r.anywhere = ("no_such_item_id",)
        problems = contract_problems(bad, self.messages, self.emits)
        self.assertTrue(any("no_such_item_id" in p and "no producer schema" in p for p in problems), problems)
        self.assertTrue(any("no_such_item_id" in p and "no producer code" in p for p in problems), problems)

    def test_the_analyser_follows_handlers_helpers_and_parameters(self):
        # A synthetic module: a handler map in code main reaches, a decode through a helper taking `any`, a json.RawMessage
        # passed to a mapper, an unreached map, and producers that name the type and payload through a helper.
        with tempfile.TemporaryDirectory() as tmp:
            report = self.synth(tmp)
        self.assertEqual([], report["problems"])
        got = {h["type"]: sorted(f["path"] for d in h["decodes"] for f in d["fields"]) for h in report["handlers"]}
        self.assertEqual({
            "dev.depot.synth.thing.created.v1": ["id", "lines[].qty", "name"],
            "dev.depot.synth.thing.moved.v1": ["Where", "id"],
            "dev.depot.synth.thing.aliased.v1": ["a"],
            "dev.depot.synth.thing.bytes.v1": ["b"],
            "dev.depot.synth.thing.nested.v1": ["extra", "extra.nope", "id"],
            "dev.depot.synth.thing.dispatched.v1": ["id"],
            "dev.depot.synth.thing.mapped.v1": [],
            "dev.depot.synth.thing.streamed.v1": [],
            "dev.depot.synth.thing.helper.v1": ["extra", "extra.nope", "id"],
            "dev.depot.synth.thing.copied.v1": ["extra", "extra.nope", "id"],
            "dev.depot.synth.thing.rebytes.v1": ["extra", "extra.nope", "id"],
            "dev.depot.synth.thing.asserted.v1": ["id", "kind"],
            "dev.depot.synth.thing.unread.v1": ["extra", "id"],
            "dev.depot.synth.thing.closure.v1": ["c"],
            "dev.depot.synth.thing.remarshalled.v1": [],
        }, got)
        dynamic = {h["type"]: sorted(f["path"] for d in h["decodes"] for f in d["fields"] if f.get("dynamic")) for h in report["handlers"]}
        # N1, N2, N6: a RawMessage field decoded through a helper, a local copy or a []byte helper is read; N4b: an `any`
        # field and an unread RawMessage field stay dynamic, which go_reads fails.
        self.assertEqual({"dev.depot.synth.thing.asserted.v1": ["kind"], "dev.depot.synth.thing.unread.v1": ["extra"]},
                         {k: v for k, v in dynamic.items() if v})
        problems = {h["type"]: h["problems"] for h in report["handlers"]}
        self.assertEqual([], problems["dev.depot.synth.thing.aliased.v1"] + problems["dev.depot.synth.thing.bytes.v1"])
        # json.NewDecoder over the data is a read the check cannot follow; a decode into a map has keys it cannot list.
        self.assertTrue(any("does not follow" in p for p in problems["dev.depot.synth.thing.streamed.v1"]),
                        problems["dev.depot.synth.thing.streamed.v1"])
        (mapped,) = [h for h in report["handlers"] if h["type"] == "dev.depot.synth.thing.mapped.v1"]
        self.assertTrue(mapped["decodes"] and mapped["decodes"][0].get("open"), mapped)
        reads, read_problems = go_reads({"synth": report}, [kc.Consumer("synth", "synth", groups={"synth"})])
        self.assertTrue(any("mapped" in p and "OPEN_DECODES" in p for p in read_problems), read_problems)
        self.assertTrue(any("streamed" in p for p in read_problems), read_problems)
        self.assertTrue(any("asserted" in p and "field kind" in p for p in read_problems), read_problems)
        self.assertTrue(any("unread" in p and "field extra" in p for p in read_problems), read_problems)
        # N3: a closure taking the data is followed itself, though an unrelated function has its shape. N5: marshalling the
        # whole event is a problem.
        self.assertEqual([], problems["dev.depot.synth.thing.closure.v1"])
        self.assertTrue(any("marshals the whole event" in p for p in problems["dev.depot.synth.thing.remarshalled.v1"]),
                        problems["dev.depot.synth.thing.remarshalled.v1"])
        self.assertEqual([{"names": ["synth-main"], "maps": sorted({h["map"] for h in report["handlers"]
                                                                       if "dispatched" not in h["type"]})}],
                         [{"names": c["names"], "maps": c["maps"]} for c in report["consumers"]])
        emitted = {(e["type"], e["goType"]): sorted(f["path"] for f in e["fields"]) for e in report["emits"]}
        self.assertEqual({
            ("dev.depot.synth.thing.created.v1", "h.created"): ["id", "lines[].qty", "name"],
            ("dev.depot.synth.thing.moved.v1", "map[string]any"): ["id"],
        }, emitted)
        self.assertEqual(["thing"], report["aggregates"])
        self.assertEqual({("dev.depot.synth.thing.created.v1", "thing"), ("dev.depot.synth.thing.moved.v1", "thing")},
                         {(r["type"], r["aggregate"]) for r in report["routes"]})

    def synth(self, tmp):
        """Type-check the synthetic module SYNTH_HANDLERS/SYNTH_MAIN in tmp and return its report."""
        mod = Path(tmp) / "synth"
        (mod / "cmd/synth").mkdir(parents=True)
        (mod / "internal/h").mkdir(parents=True)
        gomod = (REPO / "services/search/go.mod").read_text()
        gomod = gomod.replace("module github.com/BenjaminSRussell/depot/services/search", "module example.com/synth")
        gomod = gomod.replace("=> ../../platform/go", f"=> {REPO / 'platform/go'}")
        (mod / "go.mod").write_text(gomod)
        shutil.copy(REPO / "services/search/go.sum", mod / "go.sum")
        (mod / "internal/h/h.go").write_text(SYNTH_HANDLERS)
        (mod / "cmd/synth/main.go").write_text(SYNTH_MAIN)
        (report,) = run_eventfields(tmp, [str(mod)])
        return report


SYNTH_HANDLERS = '''package h

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/outbox"
)

const (
	Created = "dev.depot.synth.thing.created.v1"
	moved   = "moved"
)

type created struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Lines []struct {
		Qty string `json:"qty"`
	} `json:"lines"`
	skip string
}

type movedData struct {
	ID    string `json:"id"`
	Where string
}

func decode(evt events.Event, into any) error { return json.Unmarshal(evt.Data, into) }

// decodeBytes takes the data as plain bytes.
func decodeBytes(b []byte, v any) error { return json.Unmarshal(b, v) }

type inner struct {
	Nope string `json:"nope"`
}

// parseInner decodes a RawMessage field of decoded data (N1).
func parseInner(b json.RawMessage) error {
	var in inner
	return json.Unmarshal(b, &in)
}

// reDecode decodes a field as plain bytes into the caller's value (N6).
func reDecode(b []byte, v any) error { return json.Unmarshal(b, v) }

// sameShape has the shape of the closure below but is not it (N3).
func sameShape(b []byte) error { return json.Unmarshal(b, new(struct{})) }

type asserted struct {
	ID   string `json:"id"`
	Kind any    `json:"kind"`
}

type wrapped struct {
	ID    string          `json:"id"`
	Extra json.RawMessage `json:"extra"`
}

// Registry hands out a handler map behind an interface; main reaches it only through the interface.
type Registry interface{ Handlers() map[string]eventing.Handler }

type registry struct{}

func NewRegistry() Registry { return registry{} }

func (registry) Handlers() map[string]eventing.Handler {
	return map[string]eventing.Handler{
		"dev.depot.synth.thing.dispatched.v1": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			var d struct {
				ID string `json:"id"`
			}
			return json.Unmarshal(evt.Data, &d)
		}),
	}
}

func fromRaw(data json.RawMessage) (movedData, error) {
	var m movedData
	return m, json.Unmarshal(data, &m)
}

func Handlers() map[string]eventing.Handler {
	h := func(f func(events.Event) error) eventing.Handler {
		return eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error { return f(evt) })
	}
	return map[string]eventing.Handler{
		Created: h(func(evt events.Event) error {
			var c created
			return decode(evt, &c)
		}),
		events.Type("synth", "thing", moved, 1): eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			_, err := fromRaw(evt.Data)
			return err
		}),
		"dev.depot.synth.thing.aliased.v1": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			data := evt.Data
			var d struct {
				A string `json:"a"`
			}
			return json.Unmarshal(data, &d)
		}),
		"dev.depot.synth.thing.bytes.v1": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			var d struct {
				B string `json:"b"`
			}
			return decodeBytes(evt.Data, &d)
		}),
		"dev.depot.synth.thing.nested.v1": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			var w wrapped
			if err := json.Unmarshal(evt.Data, &w); err != nil {
				return err
			}
			var inner struct {
				Nope string `json:"nope"`
			}
			return json.Unmarshal(w.Extra, &inner)
		}),
		"dev.depot.synth.thing.mapped.v1": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			var m map[string]any
			return json.Unmarshal(evt.Data, &m)
		}),
		"dev.depot.synth.thing.streamed.v1": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			var d struct {
				S string `json:"s"`
			}
			return json.NewDecoder(bytes.NewReader(evt.Data)).Decode(&d)
		}),
		"dev.depot.synth.thing.helper.v1": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			var w wrapped
			if err := json.Unmarshal(evt.Data, &w); err != nil {
				return err
			}
			return parseInner(w.Extra)
		}),
		"dev.depot.synth.thing.copied.v1": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			var w wrapped
			if err := json.Unmarshal(evt.Data, &w); err != nil {
				return err
			}
			raw := w.Extra
			var in inner
			return json.Unmarshal(raw, &in)
		}),
		"dev.depot.synth.thing.rebytes.v1": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			var w wrapped
			if err := json.Unmarshal(evt.Data, &w); err != nil {
				return err
			}
			var in inner
			return reDecode(w.Extra, &in)
		}),
		"dev.depot.synth.thing.asserted.v1": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			var d asserted
			if err := json.Unmarshal(evt.Data, &d); err != nil {
				return err
			}
			_ = d.Kind.(map[string]any)["nope"]
			return nil
		}),
		"dev.depot.synth.thing.unread.v1": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			var w wrapped
			return json.Unmarshal(evt.Data, &w)
		}),
		"dev.depot.synth.thing.closure.v1": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			parse := func(b []byte) error {
				var d struct {
					C string `json:"c"`
				}
				return json.Unmarshal(b, &d)
			}
			return parse(evt.Data)
		}),
		"dev.depot.synth.thing.remarshalled.v1": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			raw, err := json.Marshal(evt)
			if err != nil {
				return err
			}
			var env struct {
				Data struct {
					Nick string `json:"nick"`
				} `json:"data"`
			}
			return json.Unmarshal(raw, &env)
		}),
	}
}

// Run is the consumer config: its Name and handler map are bound at the call in main.
func Run(h map[string]eventing.Handler) eventing.Config {
	return eventing.Config{Name: "synth-main", Handlers: h, DeadLetterTopic: "dlq"}
}

// Unreached is a handler map no main package uses: it is not a consumer.
func Unreached() map[string]eventing.Handler {
	return map[string]eventing.Handler{"dev.depot.synth.thing.unreached.v1": nil}
}

func emit(ctx context.Context, tx pgx.Tx, typ string, data any) error {
	evt, err := events.New(typ, "synth", "s", "t", data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "thing", "id", evt)
}

func Publish(ctx context.Context, tx pgx.Tx) error {
	if err := emit(ctx, tx, Created, created{}); err != nil {
		return err
	}
	_ = sameShape(nil)
	return emit(ctx, tx, events.Type("synth", "thing", moved, 1), map[string]any{"id": "x"})
}
'''

SYNTH_MAIN = '''package main

import "example.com/synth/internal/h"

func main() {
	_ = h.Run(h.Handlers())
	var r h.Registry = h.NewRegistry()
	_ = r.Handlers()
}
'''


@needs_go
class SearchSourcesTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        go, _, _ = kc.go_consumers()
        (cls.consumer,) = [c for c in go if c.service == "search"]
        report = eventfields(("services/search",))["services/search"]
        cls.handled = {h["type"] for h in report["handlers"]}
        cls.messages = producer_messages()
        producers = sorted({m.service for t in cls.handled for m in cls.messages.get(t, [])})
        reports = eventfields(tuple(d for d in (go_module_dir(s) for s in producers) if d))
        cls.routes = routes_by_type(reports)
        cls.spec = yaml.safe_load(SEARCH_ASYNCAPI.read_text())
        cls.conns = connectors()
        cls.writable = staticmethod(debezium_writable())

    def problems(self, spec=None, conns=None, routes=None):
        return search_source_problems(spec or self.spec, self.handled, self.consumer.topics, self.messages,
                                      conns or self.conns, self.writable, routes or self.routes)

    def test_every_indexed_type_arrives_on_a_topic_its_producers_connector_relays(self):
        self.assertGreaterEqual(len(self.handled), 18)
        self.assertEqual([], self.problems())

    def test_a_source_without_a_connector_fails(self):
        conns = {k: v for k, v in self.conns.items() if k != "hr"}
        self.assertTrue(any("no outbox-hr connector" in p for p in self.problems(conns=conns)))

    def test_a_type_routed_to_another_topic_fails(self):
        spec = copy.deepcopy(self.spec)
        for ch in spec["channels"].values():
            if ch["address"] == "depot.fulfillment.packingslip":
                ch["address"] = "depot.fulfillment.shipment"
        problems = self.problems(spec=spec)
        self.assertTrue(any("routes it to depot.fulfillment.packingslip" in p for p in problems), problems)

    def test_a_type_enqueued_under_another_aggregate_fails(self):
        # Billing moving credit notes to aggregate creditnote would send them to depot.billing.creditnote.
        routes = copy.deepcopy(self.routes)
        routes[("billing", "dev.depot.billing.creditnote.posted.v1")] = {"creditnote"}
        problems = self.problems(routes=routes)
        self.assertTrue(any("creditnote.posted" in p and "depot.billing.creditnote" in p and "reads depot.billing.invoice" in p
                            for p in problems), problems)
        routes[("receiving", "dev.depot.payables.invoice.posted.v1")] = set()
        self.assertTrue(any("payables.invoice.posted" in p and "no outbox.Enqueue" in p for p in self.problems(routes=routes)))

    def test_an_undeclared_topic_or_type_fails(self):
        spec = copy.deepcopy(self.spec)
        name = next(k for k, op in spec["operations"].items() if op["channel"]["$ref"].endswith("/worker"))
        del spec["operations"][name]
        problems = self.problems(spec=spec)
        self.assertTrue(any("subscribes to depot.hr.worker" in p for p in problems), problems)
        self.assertTrue(any("handles dev.depot.hr.worker.created.v1" in p for p in problems), problems)


if __name__ == "__main__":
    unittest.main()
