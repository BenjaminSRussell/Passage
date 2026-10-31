#!/usr/bin/env python3
"""Independent reference for the Wave 49 synthetic-year proof (issue #49).

Standard library only. Run from anywhere with:  python3 -I synth_reference.py

It simulates one full legacy year (2026) of three legal entities for an AX 2012 tenant and for an
SAP ECC tenant: vendor and customer invoices with tax, partial and full payments, disputes, credit
notes, payroll through a clearing account, accruals and their reversals, depreciation and fixed
asset purchases, and project transactions. From that year it derives, per entity, the extracts the
migration factory takes at the cutoff (open_items, gl_balances, project_wip, already mapped to the
factory's request shape with legacy ids in each system's own conventions) and writes them to
ax2012.json and sapecc.json. It also writes the Passage postings of the first days after go-live
that the rollback rehearsal hands back for re-keying.

Separately it computes, from its own reading of FR-49.2 and the README, every control total the
factory must report (source value per total name and key), the input hashes, the quarantine, and
the re-keying files, and writes them to expected.json. It never runs or imports the Go engine.

Adversarial features are part of the generator, not tuned to any threshold:
- the same invoice numbers in every entity of a system (each entity numbers from 1), so only the
  company in the legacy_xref key keeps them apart;
- two vendors per entity onboarded on 1 December, after the master-data snapshot, whose open items
  fail the referential rule and must be released with the party added to master data;
- foreign-currency invoices (including JPY with no minor unit) next to the entity currency;
- vendor and customer credit notes (open items with the opposite sign);
- partial payments, disputed invoices that are never paid, payments after the cutoff;
- GL combinations that net to exactly zero (payroll clearing, reversed accruals);
- re-keying postings dated before T-0 (excluded), multi-currency journals, text with commas,
  quotes and non-ASCII characters.
The script prints the observed rates (quarantine share, open-item share, zero balances).
"""
import hashlib
import json
import os
from datetime import date, datetime, timedelta, timezone
from decimal import ROUND_HALF_UP, Decimal

HERE = os.path.dirname(os.path.abspath(__file__))
YEAR_START = date(2026, 1, 1)
CUTOFF_DAY = date(2026, 12, 31)
T0 = datetime(2027, 1, 1, tzinfo=timezone.utc)
FREEZE = "2026-12-31T18:00:00Z"  # legacy transaction freeze at T-6h; the extract cutoff
SIX = Decimal("0.000001")


class Rng:
    """splitmix64: a fixed, documented generator so the data does not depend on Python's random."""

    def __init__(self, seed):
        self.s = seed & 0xFFFFFFFFFFFFFFFF

    def next(self):
        self.s = (self.s + 0x9E3779B97F4A7C15) & 0xFFFFFFFFFFFFFFFF
        z = self.s
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & 0xFFFFFFFFFFFFFFFF
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & 0xFFFFFFFFFFFFFFFF
        return z ^ (z >> 31)

    def randint(self, lo, hi):
        return lo + self.next() % (hi - lo + 1)

    def random(self):
        return (self.next() >> 11) / float(1 << 53)

    def choice(self, seq):
        return seq[self.next() % len(seq)]


MINOR = {"JPY": 0}


def quant(ccy):
    return Decimal(1).scaleb(-MINOR.get(ccy, 2))


def money(rng, ccy, lo, hi):
    """A random amount between lo and hi major units, in the currency's minor unit."""
    q = quant(ccy)
    scale = 1 if MINOR.get(ccy, 2) == 0 else 100
    if ccy == "JPY":
        lo, hi = lo * 100, hi * 100  # yen amounts are about a hundred times larger
    return (Decimal(rng.randint(lo * scale, hi * scale)) / scale).quantize(q)


def pct_of(amount, rate, ccy):
    return (amount * rate).quantize(quant(ccy), rounding=ROUND_HALF_UP)


def fixed6(d):
    d = Decimal(d).quantize(SIX)
    if d == 0:
        d = Decimal(0).quantize(SIX)
    return format(d, "f")


def plain(d):
    d = Decimal(d)
    if d == 0:
        return "0"
    return format(d, "f")


SYSTEMS = [
    {
        "system": "AX2012",
        "file": "ax2012.json",
        "seed": 0xA2012,
        "entities": [("USMF", "USD", "0.07"), ("DEMF", "EUR", "0.19"), ("GBSI", "GBP", "0.20")],
        "acct": {
            "AR": "130100", "AP": "200110", "BANK": "110180", "REV": "401100", "EXP": ["600100", "605100", "610100"],
            "VAT_IN": "140200", "VAT_OUT": "222100", "SAL": "650100", "ACCR": "224100", "PAYCLR": "224200",
            "DEP": "680100", "ACCDEP": "150900", "FA": "150100", "EQ": "300100",
        },
        "unused": ["110190", "401900", "999999"],
        "bu": lambda e: [f"{e}-{b}" for b in ("001", "002", "003")],
        "pl": lambda e: [f"{e}-{b}-{c}" for b in ("001", "002", "003") for c in ("010", "020", "SALES")],
        "vendor": lambda e, n: f"{e[:2]}-V{n:04d}",
        "customer": lambda e, n: f"{e[:2]}-C{n:05d}",
        "inv_v": lambda n: f"VINV-26-{n:06d}",
        "inv_c": lambda n: f"CINV-26-{n:06d}",
        "cn_v": lambda n: f"VCN-26-{n:05d}",
        "cn_c": lambda n: f"CCN-26-{n:05d}",
        "gl_id": lambda e, n, ccy: f"GJAE:{e}:{5637144576 + n}:{ccy}",
        "project": lambda e, n: f"{e}-PRJ-{n:06d}",
        "proj_line": lambda e, n: f"PJT:{e}:{5637200000 + n}",
        "worker": lambda n: f"W{n:03d}",
        "activity": lambda n: f"ACT-{n:04d}",
    },
    {
        "system": "SAPECC",
        "file": "sapecc.json",
        "seed": 0x5A9ECC,
        "entities": [("1000", "EUR", "0.19"), ("2000", "USD", "0.07"), ("3000", "JPY", "0.10")],
        "acct": {
            "AR": "0000140000", "AP": "0000160000", "BANK": "0000113100", "REV": "0000800000",
            "EXP": ["0000400000", "0000417000", "0000476000"], "VAT_IN": "0000154000", "VAT_OUT": "0000175000",
            "SAL": "0000430000", "ACCR": "0000196000", "PAYCLR": "0000196100", "DEP": "0000211100",
            "ACCDEP": "0000011010", "FA": "0000011000", "EQ": "0000070000",
        },
        "unused": ["0000113109", "0000899999"],
        "bu": lambda e: [f"PC{e}-0{b}" for b in (1, 2, 3)],
        "pl": lambda e: [f"PC{e}-0{b}/CC{e[:1]}{c}" for b in (1, 2, 3) for c in ("100", "200", "410")],
        "vendor": lambda e, n: f"{100000 + n:010d}",
        "customer": lambda e, n: f"{200000 + n:010d}",
        "inv_v": lambda n: f"{5100000000 + n}-2026-001",
        "inv_c": lambda n: f"{9000000000 + n}-2026-001",
        "cn_v": lambda n: f"{5105000000 + n}-2026-001",
        "cn_c": lambda n: f"{9005000000 + n}-2026-001",
        "gl_id": lambda e, n, ccy: f"FAGLFLEXT:{e}:{n:08d}:{ccy}:2026",
        "project": lambda e, n: f"P-{e}-{n:04d}",
        "proj_line": lambda e, n: f"COEP:{e}:{n:010d}-001",
        "worker": lambda n: f"{50000000 + n}",
        "activity": lambda n: f"LA{n:02d}",
    },
]

FOREIGN = ["USD", "EUR", "GBP", "CHF", "JPY"]
N_VENDORS, N_LATE, N_CUSTOMERS, N_PROJECTS, N_WORKERS = 40, 2, 60, 6, 12
LATE_FROM = date(2026, 12, 1)


def business_days():
    d = YEAR_START
    while d <= CUTOFF_DAY:
        if d.weekday() < 5:
            yield d
        d += timedelta(days=1)


def gen_entity(sd, ent, rng):
    company, ccy, rate_s = ent
    rate = Decimal(rate_s)
    A = sd["acct"]
    bus, pls = sd["bu"](company), sd["pl"](company)
    vendors = [sd["vendor"](company, n) for n in range(1, N_VENDORS + 1)]
    late = [sd["vendor"](company, n) for n in range(N_VENDORS + 1, N_VENDORS + N_LATE + 1)]
    customers = [sd["customer"](company, n) for n in range(1, N_CUSTOMERS + 1)]
    postings = []  # (account, dims, currency, signed amount)
    docs = 0
    open_items = []
    seq = {"v": 0, "c": 0, "cnv": 0, "cnc": 0}
    invoices = 0

    def post(lines):
        nonlocal docs
        docs += 1
        by_ccy = {}
        for acct, dims, cur, amt in lines:
            postings.append((acct, dims, cur, amt))
            by_ccy[cur] = by_ccy.get(cur, Decimal(0)) + amt
        assert all(v == 0 for v in by_ccy.values()), lines  # every document balances per currency

    def doc_ccy():
        if rng.random() < 0.85:
            return ccy
        return rng.choice([c for c in FOREIGN if c != ccy])

    def settle(d, gross, cur, full_p, part_p):
        """Payment plan for an invoice: returns the amount paid on or before the cutoff and the
        payment lines to post (date, amount)."""
        terms = rng.choice([30, 45, 60])
        due = d + timedelta(days=terms)
        r = rng.random()
        pays = []
        if r < full_p:
            pays.append((due + timedelta(days=rng.randint(-5, 20)), gross))
        elif r < full_p + part_p:
            first = (gross * Decimal(rng.randint(30, 90)) / 100).quantize(quant(cur))
            pays.append((due + timedelta(days=rng.randint(-5, 20)), first))
            if rng.random() < 0.5:
                pays.append((due + timedelta(days=rng.randint(25, 60)), gross - first))
        # else: disputed, never paid
        return [(pd, amt) for pd, amt in pays if pd <= CUTOFF_DAY and amt != 0]

    capital = money(rng, ccy, 400000, 900000)
    post([(A["BANK"], "", ccy, capital), (A["EQ"], "", ccy, -capital)])

    for d in business_days():
        pool = vendors + (late if d >= LATE_FROM else [])
        for _ in range(rng.randint(2, 6)):
            invoices += 1
            seq["v"] += 1
            inv = sd["inv_v"](seq["v"])
            vend = rng.choice(pool)
            cur = doc_ccy()
            net = money(rng, cur, 50, 20000)
            tax = pct_of(net, rate, cur)
            gross = net + tax
            bu = bus[(vendors + late).index(vend) % len(bus)]
            lines = [(rng.choice(A["EXP"]), rng.choice(pls), cur, net), (A["AP"], bu, cur, -gross)]
            if tax:
                lines.append((A["VAT_IN"], bu, cur, tax))
            post(lines)
            paid = Decimal(0)
            for _pd, amt in settle(d, gross, cur, 0.80, 0.10):
                post([(A["AP"], bu, cur, amt), (A["BANK"], "", cur, -amt)])
                paid += amt
            if gross - paid != 0:
                open_items.append({"legacyId": inv, "party": vend, "account": A["AP"], "currency": cur, "amount": -(gross - paid)})
            if rng.random() < 0.02 and d + timedelta(days=10) <= CUTOFF_DAY:
                seq["cnv"] += 1
                cn = (net * Decimal(rng.randint(10, 50)) / 100).quantize(quant(cur))
                post([(A["AP"], bu, cur, cn), (rng.choice(A["EXP"]), rng.choice(pls), cur, -cn)])
                open_items.append({"legacyId": sd["cn_v"](seq["cnv"]), "party": vend, "account": A["AP"], "currency": cur, "amount": cn})
        for _ in range(rng.randint(3, 8)):
            invoices += 1
            seq["c"] += 1
            inv = sd["inv_c"](seq["c"])
            cust = rng.choice(customers)
            cur = doc_ccy()
            net = money(rng, cur, 80, 30000)
            tax = pct_of(net, rate, cur)
            gross = net + tax
            bu = bus[customers.index(cust) % len(bus)]
            lines = [(A["AR"], bu, cur, gross), (A["REV"], rng.choice(pls), cur, -net)]
            if tax:
                lines.append((A["VAT_OUT"], bu, cur, -tax))
            post(lines)
            paid = Decimal(0)
            for _pd, amt in settle(d, gross, cur, 0.78, 0.12):
                post([(A["BANK"], "", cur, amt), (A["AR"], bu, cur, -amt)])
                paid += amt
            if gross - paid != 0:
                open_items.append({"legacyId": inv, "party": cust, "account": A["AR"], "currency": cur, "amount": gross - paid})
            if rng.random() < 0.03 and d + timedelta(days=10) <= CUTOFF_DAY:
                seq["cnc"] += 1
                cn = (net * Decimal(rng.randint(10, 50)) / 100).quantize(quant(cur))
                post([(A["AR"], bu, cur, -cn), (A["REV"], rng.choice(pls), cur, cn)])
                open_items.append({"legacyId": sd["cn_c"](seq["cnc"]), "party": cust, "account": A["AR"], "currency": cur, "amount": -cn})
        if is_month_end_business_day(d):
            for bu in bus:  # payroll per business unit through the clearing account
                g = money(rng, ccy, 30000, 90000)
                pl = rng.choice([p for p in pls if p.startswith(bu)] or pls)
                post([(A["SAL"], pl, ccy, g), (A["PAYCLR"], "", ccy, -g)])
                post([(A["PAYCLR"], "", ccy, g), (A["BANK"], "", ccy, -g)])
            accr = money(rng, ccy, 5000, 20000)
            post([(A["SAL"], pls[0], ccy, accr), (A["ACCR"], "", ccy, -accr)])
            nxt = d + timedelta(days=1)
            while nxt.weekday() >= 5:
                nxt += timedelta(days=1)
            if nxt <= CUTOFF_DAY:  # the reversal of December's accrual falls in 2027
                post([(A["SAL"], pls[0], ccy, -accr), (A["ACCR"], "", ccy, accr)])
            dep = money(rng, ccy, 2000, 8000)
            post([(A["DEP"], rng.choice(pls), ccy, dep), (A["ACCDEP"], "", ccy, -dep)])
            if d.month % 3 == 0:
                fa = money(rng, ccy, 10000, 60000)
                post([(A["FA"], "", ccy, fa), (A["BANK"], "", ccy, -fa)])

    # gl_balances: the legacy balance per (account, dimension combination, currency) at the cutoff.
    bal = {}
    for acct, dims, cur, amt in postings:
        bal[(acct, dims, cur)] = bal.get((acct, dims, cur), Decimal(0)) + amt
    gl = []
    for n, k in enumerate(sorted(bal), start=1):
        gl.append({"legacyId": sd["gl_id"](company, n, k[2]), "account": k[0], "dimensions": k[1], "currency": k[2], "balance": bal[k]})
    for cur in {k[2] for k in bal}:
        assert sum(v for k, v in bal.items() if k[2] == cur) == 0  # the legacy trial balance balances
    accounts = sorted({k[0] for k in bal} | set(sd["unused"]))

    projects = gen_projects(sd, company, ccy, rng)
    return {
        "company": company, "currency": ccy, "parties": vendors + customers, "lateParties": late,
        "openItems": open_items, "accounts": accounts, "glBalances": gl, "projects": projects,
        "stats": {"documents": docs, "journalLines": len(postings), "invoices": invoices},
    }


def is_month_end_business_day(d):
    nxt = d + timedelta(days=1)
    while nxt.weekday() >= 5:
        nxt += timedelta(days=1)
    return nxt.month != d.month or nxt.year != d.year


def gen_projects(sd, company, ccy, rng):
    workers = [(sd["worker"](n), money(rng, ccy, 45, 95)) for n in range(1, N_WORKERS + 1)]
    out = []
    line_no = 0
    for p in range(1, N_PROJECTS + 1):
        pid = sd["project"](company, p)
        lines = []

        def status(d):
            r = rng.random()
            if d < date(2026, 10, 1):
                return "BILLED" if r < 0.75 else ("NON_CHARGEABLE" if r < 0.85 else "CHARGEABLE_UNBILLED")
            return "CHARGEABLE_UNBILLED" if r < 0.60 else ("BILLED" if r < 0.90 else "NON_CHARGEABLE")

        def add(d, cat, qty, cost, sales, bill, worker="", activity=""):
            nonlocal line_no
            line_no += 1
            if bill == "NON_CHARGEABLE":
                sales = Decimal(0).quantize(quant(ccy))
            wip_cost = cost if bill == "CHARGEABLE_UNBILLED" else Decimal(0)
            wip_sales = sales if bill == "CHARGEABLE_UNBILLED" else Decimal(0)
            lines.append({
                "legacyId": sd["proj_line"](company, line_no), "category": cat, "transactionDate": d.isoformat(),
                "qty": qty, "costAmount": cost, "salesAmount": sales, "currency": ccy, "workerId": worker,
                "activityId": activity, "billingStatus": bill, "wipCost": wip_cost, "wipSales": wip_sales,
            })

        for d in business_days():
            if rng.random() < 0.22:
                w, cost_rate = rng.choice(workers)
                hours = Decimal(rng.randint(2, 32)) / 4
                cost = (hours * cost_rate).quantize(quant(ccy), rounding=ROUND_HALF_UP)
                sales = (hours * cost_rate * Decimal("1.6")).quantize(quant(ccy), rounding=ROUND_HALF_UP)
                add(d, "HOUR", hours, cost, sales, status(d), w, sd["activity"](rng.randint(1, 20)))
            if rng.random() < 0.05:
                cost = money(rng, ccy, 20, 900)
                add(d, "EXPENSE", Decimal(1), cost, (cost * Decimal("1.1")).quantize(quant(ccy), rounding=ROUND_HALF_UP), status(d))
            if rng.random() < 0.03:
                qty = Decimal(rng.randint(1, 20))
                unit = money(rng, ccy, 5, 300)
                add(d, "ITEM", qty, qty * unit, (qty * unit * Decimal("1.25")).quantize(quant(ccy), rounding=ROUND_HALF_UP), status(d))
            if rng.random() < 0.02:
                add(d, "FEE", Decimal(1), Decimal(0).quantize(quant(ccy)), money(rng, ccy, 100, 2500), status(d))
            if is_month_end_business_day(d) and rng.random() < 0.5:
                add(d, "ON_ACCOUNT", Decimal(1), Decimal(0).quantize(quant(ccy)), money(rng, ccy, 1000, 15000), "BILLED")
        totals = {}
        for l in lines:
            t = totals.setdefault(l["category"], [Decimal(0)] * 4)
            for i, f in enumerate(("costAmount", "salesAmount", "wipCost", "wipSales")):
                t[i] += l[f]
        out.append({"projectId": pid, "legacyTotals": [
            {"category": c, "cost": t[0], "sales": t[1], "wipCost": t[2], "wipSales": t[3]} for c, t in sorted(totals.items())
        ], "lines": lines})
    return out


def gen_rekey(sd, entities, rng):
    """Passage postings of the first days after go-live (journals and documents), plus a few
    dated before T-0, that a rollback hands back for re-keying (FR-49.9)."""
    A = sd["acct"]
    out = []
    texts = ["Freight, January \"rush\" order", "Müller & Söhne GmbH", "Hypercare correction", "Rent; Q1", ""]
    n = 0
    for company, ccy, _r in entities:
        bus = sd["bu"](company)
        for j in range(1, 6):
            n += 1
            at = T0 + timedelta(days=1, hours=8, minutes=rng.randint(0, 600))
            ref = f"JV-27-{j:04d}"  # the same journal numbers in every entity
            cur = ccy if j != 4 else rng.choice([c for c in FOREIGN if c != ccy])
            a1, a2 = money(rng, cur, 100, 5000), money(rng, cur, 100, 5000)
            lines = [(A["EXP"][0], bus[0], a1), (A["EXP"][1], bus[1], a2), (A["BANK"], "", -(a1 + a2))]
            for k, (acct, dims, amt) in enumerate(lines):
                out.append({"kind": "JOURNAL", "entity": company, "documentRef": ref, "postedAt": at + timedelta(seconds=k),
                            "account": acct, "dimensions": dims, "currency": cur, "amount": amt, "text": texts[(j + k) % len(texts)]})
        for j in range(1, 9):
            at = T0 + timedelta(days=rng.randint(1, 2), hours=rng.randint(0, 20))
            ref = f"INV-27-{j:05d}"
            cur = ccy
            for k in range(rng.randint(1, 3)):
                out.append({"kind": "DOCUMENT", "entity": company, "documentRef": ref, "postedAt": at,
                            "account": A["AR"] if k == 0 else A["REV"], "dimensions": bus[k % len(bus)], "currency": cur,
                            "amount": money(rng, cur, 50, 8000) * (1 if k == 0 else -1), "text": texts[(j * 3 + k) % len(texts)]})
        for j in range(1, 3):  # made in the legacy system before T-0: excluded from the files
            at = T0 - timedelta(hours=rng.randint(1, 5))
            amt = money(rng, ccy, 10, 500)
            for k, (acct, sign) in enumerate(((A["EXP"][2], 1), (A["BANK"], -1))):
                out.append({"kind": "JOURNAL", "entity": company, "documentRef": f"LEG-26-{j:03d}", "postedAt": at, "account": acct,
                            "dimensions": "", "currency": ccy, "amount": amt * sign, "text": "posted in legacy"})
    # Interleave the entities, as a ledger query ordered by posting time would return them.
    out.sort(key=lambda p: (p["postedAt"], p["entity"], p["documentRef"], p["account"]))
    return out


def expected_rekey(postings):
    kept = [p for p in postings if p["postedAt"] >= T0]
    excluded = len(postings) - len(kept)
    first = {}
    for p in kept:
        k = (p["kind"], p["entity"], p["documentRef"])
        if k not in first or p["postedAt"] < first[k]:
            first[k] = p["postedAt"]
    kept = sorted(kept, key=lambda p: (p["entity"], first[(p["kind"], p["entity"], p["documentRef"])], p["documentRef"]))
    lines, files = {}, {"JOURNAL": [], "DOCUMENT": []}
    for p in kept:
        k = (p["kind"], p["entity"], p["documentRef"])
        lines[k] = lines.get(k, 0) + 1
        row = [p["entity"], p["documentRef"], str(lines[k]), iso(p["postedAt"]), p["account"], p["dimensions"], p["currency"]]
        if p["kind"] == "JOURNAL":
            row += ["", fixed6(-p["amount"])] if p["amount"] < 0 else [fixed6(p["amount"]), ""]
        else:
            row.append(fixed6(p["amount"]))
        row.append(p["text"])
        files[p["kind"]].append(row)
    return {"journalLines": len(files["JOURNAL"]), "documentLines": len(files["DOCUMENT"]), "excluded": excluded,
            "journals": files["JOURNAL"], "documents": files["DOCUMENT"]}


def iso(t):
    return t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def tkey(name, key):
    return name + "|" + "|".join(f"{k}={key[k]}" for k in sorted(key))


def sha(lines):
    return hashlib.sha256("\n".join(sorted(lines)).encode()).hexdigest()


def expected_entity(e):
    """Control totals per FR-49.2 / README, computed from the extract alone."""
    company = e["company"]
    master = set(e["parties"])
    oi = e["openItems"]
    quarantined = [{"legacyId": r["legacyId"], "party": r["party"], "rule": "REFERENTIAL"} for r in oi if r["party"] not in master]
    t = {}
    for r in oi:
        k = tkey("open_items.amount", {"company": company, "currency": r["currency"]})
        t[k] = t.get(k, Decimal(0)) + r["amount"]
        k = tkey("open_items.amount_by_account", {"company": company, "account": r["account"], "currency": r["currency"]})
        t[k] = t.get(k, Decimal(0)) + r["amount"]
    t[tkey("open_items.count", {"company": company})] = Decimal(len(oi))
    open_totals = {k: fixed6(v) for k, v in t.items()}

    g = {}
    nonzero = 0
    for b in e["glBalances"]:
        g[tkey("gl_balances.balance", {"company": company, "account": b["account"], "dimensions": b["dimensions"], "currency": b["currency"]})] = b["balance"]
        nonzero += b["balance"] != 0
    g[tkey("gl_balances.nonzero_count", {"company": company})] = Decimal(nonzero)
    for cur in sorted({b["currency"] for b in e["glBalances"]}):
        g[tkey("gl_balances.trial_balance", {"company": company, "currency": cur})] = sum((b["balance"] for b in e["glBalances"] if b["currency"] == cur), Decimal(0))
    control = {}
    for r in oi:
        k = (r["account"], r["currency"])
        control[k] = control.get(k, Decimal(0)) + r["amount"]
    controls = {}
    for (acct, cur), open_sum in control.items():
        ledger = sum((b["balance"] for b in e["glBalances"] if b["account"] == acct and b["currency"] == cur), Decimal(0))
        assert ledger == open_sum, (company, acct, cur, ledger, open_sum)  # BR-49.2 holds in the legacy books
        controls[tkey("gl_balances.control_account", {"company": company, "account": acct, "currency": cur})] = fixed6(ledger)
    gl_totals = {k: fixed6(v) for k, v in g.items()}

    p = {}
    for pr in e["projects"]:
        for lt in pr["legacyTotals"]:
            key = {"project": pr["projectId"], "category": lt["category"]}
            for name, f in (("cost", "cost"), ("sales", "sales"), ("wip_cost", "wipCost"), ("wip_sales", "wipSales")):
                p[tkey("project_wip." + name, key)] = fixed6(lt[f])
    lines = sum(len(pr["lines"]) for pr in e["projects"])

    return {
        "openItems": {
            "records": len(oi), "quarantined": quarantined, "totals": open_totals,
            # migration.quarantine_max_pct defaults to 1 (9.49.13): above it the load stops with MIG-1504.
            "exceedsDefaultQuarantineMax": Decimal(len(quarantined)) * 100 / Decimal(len(oi)) > 1,
            "inputHash": sha([r["legacyId"] + "=" + fixed6(r["amount"]) for r in oi]),
        },
        "glBalances": {
            "records": len(e["glBalances"]), "totals": gl_totals, "controlAccounts": controls,
            "inputHash": sha([b["legacyId"] + "|" + b["account"] + "|" + b["dimensions"] + "|" + b["currency"] + "=" + fixed6(b["balance"]) for b in e["glBalances"]]),
            "zeroBalances": len(e["glBalances"]) - nonzero,
        },
        "projectWip": {"lines": lines, "projects": len(e["projects"]), "totals": p},
    }


def to_wire(obj):
    if isinstance(obj, Decimal):
        return plain(obj)
    if isinstance(obj, datetime):
        return iso(obj)
    if isinstance(obj, dict):
        return {k: to_wire(v) for k, v in obj.items()}
    if isinstance(obj, list):
        return [to_wire(v) for v in obj]
    return obj


def dump(path, obj):
    """JSON with one record per line, so a regenerated fixture diffs line by line."""

    def enc(v, ind):
        pad = " " * ind
        if isinstance(v, dict) and any(isinstance(x, (list, dict)) for x in v.values()):
            items = [f'{pad} {json.dumps(k)}: {enc(x, ind + 1).lstrip()}' for k, x in v.items()]
            return pad + "{\n" + ",\n".join(items) + "\n" + pad + "}"
        if isinstance(v, list) and v and isinstance(v[0], (dict, list)):
            return pad + "[\n" + ",\n".join(enc(x, ind + 1) for x in v) + "\n" + pad + "]"
        return pad + json.dumps(v, ensure_ascii=False, separators=(",", ":"))

    with open(path, "w", encoding="utf-8") as f:
        f.write(enc(obj, 0) + "\n")


def main():
    expected = {}
    for sd in SYSTEMS:
        rng = Rng(sd["seed"])
        ents = [gen_entity(sd, e, rng) for e in sd["entities"]]
        rekey = gen_rekey(sd, sd["entities"], rng)
        data = {"system": sd["system"], "cutoffTs": FREEZE, "freezeTs": FREEZE, "t0": iso(T0),
                "entities": [{k: v for k, v in e.items() if k != "stats"} for e in ents], "rekeyPostings": rekey}
        dump(os.path.join(HERE, sd["file"]), to_wire(data))
        exp = {"entities": {}, "rekey": expected_rekey(rekey)}
        for e in ents:
            x = expected_entity(e)
            x["stats"] = e["stats"]
            exp["entities"][e["company"]] = x
            oi = x["openItems"]
            print(f'{sd["system"]} {e["company"]}: {e["stats"]["documents"]} documents, {e["stats"]["journalLines"]} journal lines, '
                  f'{e["stats"]["invoices"]} invoices -> {oi["records"]} open items ({100 * oi["records"] / e["stats"]["invoices"]:.1f}% open), '
                  f'{len(oi["quarantined"])} quarantined ({100 * len(oi["quarantined"]) / oi["records"]:.2f}%), '
                  f'{x["glBalances"]["records"]} GL balances ({x["glBalances"]["zeroBalances"]} zero), '
                  f'{x["projectWip"]["lines"]} project lines; totals: {len(oi["totals"])} + {len(x["glBalances"]["totals"]) + len(x["glBalances"]["controlAccounts"])} + {len(x["projectWip"]["totals"])}')
        r = exp["rekey"]
        print(f'{sd["system"]} rekey: {r["journalLines"]} journal lines, {r["documentLines"]} document lines, {r["excluded"]} excluded')
        expected[sd["system"]] = exp
    dump(os.path.join(HERE, "expected.json"), expected)


if __name__ == "__main__":
    main()
