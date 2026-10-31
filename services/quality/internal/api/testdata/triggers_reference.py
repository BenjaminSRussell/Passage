"""Trigger fixture and its independent expectation (Wave 33 proof, AC1 / AC-33.1).

Writes, next to this file:
  quality-triggers-100.json           the fixture: test groups, overlapping quality
                                      associations and 100 trigger events (60 purchase
                                      receipts, 40 production RAFs) in posting order
  quality-triggers-100.expected.json  what posting them must produce, worked out
                                      here from the PRD rules, not by the service:
      BR-33.3  score = 4 item | 3 item group, + 2 party, + 1 site; "all" scores 0
               and a scoped dimension that differs does not match; highest score
               wins, ties go to the lowest sequence; a trigger only matches its own
               associations; no match is QM-0100
      BR-33.3  one order per source line per trigger: a second event on the same
               line is QM-0116; a new line of the same receipt is its own order
      API      the same Idempotency-Key replays the order it created (200)
      FR-33.2  FIXED = min(n, lot); PERCENT = ceil(lot x p / 100), at least 1, at
               most lot; AQL = Z1.4 level II normal plan from
               ../../domain/testdata/z14-tables.json (itself built by
               z14_reference.py), the sample capped at the lot; Ac/Re 0/1 otherwise

The fixture is composed to the shape AC-33.1 states (60 receipts, 40 RAFs, 73
orders). The 27 events that create no order are spelled out in the expected
file: unmatched combinations of item, vendor and site, re-posted source lines
and replayed keys, each listed with its reason.

Run: python3 -I triggers_reference.py   (standard library only; deterministic)
"""

import json
import math
import os
import uuid

HERE = os.path.dirname(os.path.abspath(__file__))


def uid(name):
    return str(uuid.uuid5(uuid.NAMESPACE_URL, "passage:quality:triggers-100:" + name))


SITES = ["S1", "S2"]
VENDORS = ["V1", "V2", "V3"]
ITEMS = ["I1", "I2", "I3", "I4", "I5", "I6"]
GROUP_OF = {"I1": "G1", "I2": "G1", "I3": "G1", "I4": "G2", "I5": "G2", "I6": None}
WAREHOUSE_OF = {"S1": "W1", "S2": "W2"}

FIXED5 = {"method": "FIXED", "fixedQty": "5"}
PCT10 = {"method": "PERCENT", "percent": "10"}
PCT2_5 = {"method": "PERCENT", "percent": "2.5"}


def aql(v):
    return {"method": "AQL", "aql": v}


# (code, trigger, item scope/ref, party scope/ref, site scope/ref, sequence, plan)
ASSOCIATIONS = [
    ("RCV-I1", "PURCHASE_RECEIPT", "ITEM", "I1", "ALL", None, "ALL", None, 10, aql("2.5")),
    ("RCV-I1-V1", "PURCHASE_RECEIPT", "ITEM", "I1", "PARTY", "V1", "ALL", None, 20, aql("1.0")),
    ("RCV-G1-S1", "PURCHASE_RECEIPT", "ITEM_GROUP", "G1", "ALL", None, "SITE", "S1", 5, FIXED5),
    ("RCV-V2", "PURCHASE_RECEIPT", "ALL", None, "PARTY", "V2", "ALL", None, 30, PCT10),
    ("RCV-S2", "PURCHASE_RECEIPT", "ALL", None, "ALL", None, "SITE", "S2", 40, aql("4.0")),
    ("RCV-G2-V2-S2", "PURCHASE_RECEIPT", "ITEM_GROUP", "G2", "PARTY", "V2", "SITE", "S2", 1, aql("2.5")),
    ("RCV-I4-S2", "PURCHASE_RECEIPT", "ITEM", "I4", "ALL", None, "SITE", "S2", 60, PCT2_5),
    ("RCV-I2-V3-S1", "PURCHASE_RECEIPT", "ITEM", "I2", "PARTY", "V3", "SITE", "S1", 70, FIXED5),
    ("RCV-I5-A", "PURCHASE_RECEIPT", "ITEM", "I5", "ALL", None, "ALL", None, 15, FIXED5),
    ("RCV-I5-B", "PURCHASE_RECEIPT", "ITEM", "I5", "ALL", None, "ALL", None, 12, aql("1.0")),
    ("RAF-G1", "PRODUCTION_RAF", "ITEM_GROUP", "G1", "ALL", None, "ALL", None, 10, aql("2.5")),
    ("RAF-I3-S2", "PRODUCTION_RAF", "ITEM", "I3", "ALL", None, "SITE", "S2", 20, FIXED5),
    ("RAF-S1", "PRODUCTION_RAF", "ALL", None, "ALL", None, "SITE", "S1", 30, PCT10),
    ("RAF-I3", "PRODUCTION_RAF", "ITEM", "I3", "ALL", None, "ALL", None, 25, aql("4.0")),
    ("RAF-I4-S1", "PRODUCTION_RAF", "ITEM", "I4", "ALL", None, "SITE", "S1", 50, aql("1.0")),
    ("RAF-I6-V1", "PRODUCTION_RAF", "ITEM", "I6", "PARTY", "V1", "ALL", None, 0, FIXED5),
    # Distractors on other triggers: they match everything, but never a receipt or a RAF.
    ("PICK-ALL", "SALES_PICK", "ALL", None, "ALL", None, "ALL", None, 0, FIXED5),
    ("XFER-ALL", "TRANSFER_RECEIPT", "ALL", None, "ALL", None, "ALL", None, 0, FIXED5),
]

LOTS = [5, 50, 51, 120, 500, 501, 1200, 3200, 10001, 35000, 7, 26, 90, 151, 280, 2, 15, 16, 1201, 150001]


def ref(name):
    return uid(name) if name else None


def build_fixture():
    assocs = []
    for code, trig, isc, iref, psc, pref, ssc, sref, seq, plan in ASSOCIATIONS:
        assocs.append({"testGroup": code, "trigger": trig, "itemScope": isc, "itemRef": ref(iref),
                       "partyScope": psc, "partyRef": ref(pref), "siteScope": ssc, "siteRef": ref(sref),
                       "sequence": seq, "samplingPlan": plan})
    events = []
    lot_i = [0]

    def event(kind, src_name, line, item, party, site, key=None):
        trig = "PURCHASE_RECEIPT" if kind == "receipt" else "PRODUCTION_RAF"
        body = {"trigger": trig,
                "sourceRef": {"type": "receipt_line" if kind == "receipt" else "production_output",
                              "id": uid(src_name), "line": line},
                "itemId": uid(item), "siteId": uid(site), "warehouseId": uid(WAREHOUSE_OF[site]),
                "lotQty": str(LOTS[lot_i[0] % len(LOTS)]), "uom": "EA"}
        lot_i[0] += 1
        if GROUP_OF[item]:
            body["itemGroupId"] = uid(GROUP_OF[item])
        if party:
            body["partyId"] = uid(party)
        n = len(events) + 1
        events.append({"seq": n, "kind": kind, "key": key or uid("key-%d" % n), "body": body,
                       "names": {"item": item, "party": party, "site": site}})
        return events[-1]

    # Receipts: every item x vendor x site once (36), 14 second lines, 3 receipts
    # without a vendor, 4 re-posted lines, 3 replayed keys (one of a refused event).
    first = []
    r = 0
    for item in ITEMS:
        for vendor in VENDORS:
            for site in SITES:
                r += 1
                first.append(event("receipt", "PO-RCPT-%02d" % r, "1", item, vendor, site))
    second_combos = [("I1", "V2", "S1"), ("I2", "V1", "S2"), ("I3", "V3", "S1"), ("I4", "V2", "S2"),
                     ("I5", "V3", "S1"), ("I6", "V2", "S1"), ("I1", "V3", "S2"), ("I4", "V1", "S1"),
                     ("I6", "V3", "S1"), ("I2", "V3", "S1"), ("I5", "V1", "S2"), ("I3", "V2", "S2"),
                     ("I1", "V1", "S1"), ("I5", "V2", "S2")]
    for i, (item, vendor, site) in enumerate(second_combos):
        event("receipt", "PO-RCPT-%02d" % (i * 3 + 1), "2", item, vendor, site)
    for i, (item, site) in enumerate([("I1", "S2"), ("I6", "S1"), ("I3", "S1")]):
        event("receipt", "PO-RCPT-NV-%d" % i, "1", item, None, site)
    for k in [0, 7, 14, 21]:  # same source line, new key
        e = first[k]
        n = event("receipt", "", "", "I1", None, "S1")
        n["body"] = dict(e["body"])
        n["names"] = dict(e["names"])
    for k in [2, 9, 30]:  # same key, same body
        e = first[k]
        n = event("receipt", "", "", "I1", None, "S1", key=e["key"])
        n["body"] = dict(e["body"])
        n["names"] = dict(e["names"])
    assert len(events) == 60, len(events)

    # RAFs: every item x site on two production orders (24), 10 second output
    # lines, 3 re-posted lines, 3 replayed keys.
    rafs = []
    p = 0
    for po in range(2):
        for item in ITEMS:
            for site in SITES:
                p += 1
                rafs.append(event("raf", "PROD-%02d" % p, "1", item, None, site))
    for i, (item, site) in enumerate([("I1", "S2"), ("I3", "S2"), ("I4", "S1"), ("I6", "S1"),
                                      ("I2", "S1"), ("I5", "S1"), ("I3", "S1"), ("I4", "S2"),
                                      ("I5", "S1"), ("I1", "S1")]):
        event("raf", "PROD-%02d" % (i + 1), "2", item, None, site)
    for k in [1, 4, 10]:
        e = rafs[k]
        n = event("raf", "", "", "I1", None, "S1")
        n["body"] = dict(e["body"])
        n["names"] = dict(e["names"])
    for k in [3, 12, 20]:
        e = rafs[k]
        n = event("raf", "", "", "I1", None, "S1", key=e["key"])
        n["body"] = dict(e["body"])
        n["names"] = dict(e["names"])
    assert len(events) == 100, len(events)
    return {"testGroups": sorted({a[0] for a in ASSOCIATIONS}), "associations": assocs, "events": events}


def load_z14():
    with open(os.path.join(HERE, "..", "..", "domain", "testdata", "z14-tables.json"), encoding="utf-8") as f:
        return json.load(f)


def size(plan, lot, z14):
    if plan["method"] == "FIXED":
        return min(int(plan["fixedQty"]), lot), 0, 1
    if plan["method"] == "PERCENT":
        pct = float(plan["percent"])
        n = math.ceil(lot * pct / 100 - 1e-9)
        return min(max(n, 1), lot), 0, 1
    letters = z14["tableI"]["II"]
    band = next(i for i, b in enumerate(z14["bands"]) if b is None or lot <= b)
    col = z14["aqls"].index(plan["aql"])
    n, ac, re = z14["plans"]["NORMAL"][letters[band]][col]
    return min(n, lot), ac, re


def matches(a, e):
    names = e["names"]
    group = GROUP_OF[names["item"]]
    score = 0
    if a["itemScope"] == "ITEM":
        if a["itemRef"] != uid(names["item"]):
            return None
        score += 4
    elif a["itemScope"] == "ITEM_GROUP":
        if group is None or a["itemRef"] != uid(group):
            return None
        score += 3
    if a["partyScope"] == "PARTY":
        if names["party"] is None or a["partyRef"] != uid(names["party"]):
            return None
        score += 2
    if a["siteScope"] == "SITE":
        if a["siteRef"] != uid(names["site"]):
            return None
        score += 1
    return score


def expect(fixture, z14):
    out, lines, keys = [], {}, {}
    created = 0
    for e in fixture["events"]:
        b = e["body"]
        cands = []
        for a in fixture["associations"]:
            if a["trigger"] != b["trigger"]:
                continue
            s = matches(a, e)
            if s is not None:
                cands.append((-s, a["sequence"], a))
        if not cands:
            out.append({"seq": e["seq"], "outcome": "no_association", "code": "QM-0100"})
            continue
        cands.sort(key=lambda c: (c[0], c[1]))
        best = cands[0][2]
        line = (b["trigger"], b["sourceRef"]["type"], b["sourceRef"]["id"], b["sourceRef"]["line"])
        if e["key"] in keys:
            out.append({"seq": e["seq"], "outcome": "replay", "replayOf": keys[e["key"]]})
            continue
        if line in lines:
            out.append({"seq": e["seq"], "outcome": "duplicate", "code": "QM-0116", "lineOf": lines[line]})
            continue
        n, ac, re = size(best["samplingPlan"], int(b["lotQty"]), z14)
        lines[line] = e["seq"]
        keys[e["key"]] = e["seq"]
        created += 1
        out.append({"seq": e["seq"], "outcome": "created", "testGroup": best["testGroup"],
                    "score": -cands[0][0], "sampleQty": str(n), "acceptNo": ac, "rejectNo": re})
    tally = {}
    for o in out:
        tally[o["outcome"]] = tally.get(o["outcome"], 0) + 1
    by_group = {}
    for o in out:
        if o["outcome"] == "created":
            by_group[o["testGroup"]] = by_group.get(o["testGroup"], 0) + 1
    return {"orders": created, "tally": tally, "ordersByTestGroup": by_group, "events": out}


def main():
    fixture = build_fixture()
    expected = expect(fixture, load_z14())
    for name, doc in (("quality-triggers-100.json", fixture), ("quality-triggers-100.expected.json", expected)):
        with open(os.path.join(HERE, name), "w", encoding="utf-8") as f:
            json.dump(doc, f, indent=1, sort_keys=True)
            f.write("\n")
    print(expected["orders"], expected["tally"], expected["ordersByTestGroup"])


if __name__ == "__main__":
    main()
