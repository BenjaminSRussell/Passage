"""Independent GL revenue reference for fx-gl-revenue-recon.json (Wave 46, AC2).

Computes, per (tenant, legal entity, posting date) with any journal line, the
general ledger's revenue: credit turnover minus debit turnover on accounts of
type 'revenue'. Turnover follows the ledger's own balance rebuild
(services/ledger/internal/store/balance.go):

    debit  = D and not negative -> +amount;  C and negative -> -amount
    credit = C and not negative -> +amount;  D and negative -> -amount

It does not read the dbt mart, the Cube model or any analytics code, and it
does not use the analytics account_category_map (that is an analytics
concept, not a GL one). It also checks that every entry balances.

Run (isolated, from the repository root):
    python3 -I services/analytics/testdata/gl_revenue_reference.py \
        services/analytics/testdata/fx-gl-revenue-recon.json \
        > services/analytics/testdata/fx-gl-revenue-recon.expected.json
"""

import json
import sys
from collections import defaultdict
from decimal import Decimal


def main(path):
    with open(path, encoding="utf-8") as f:
        fx = json.load(f)
    ledgers = {(l["tenant_id"], l["id"]): l for l in fx["ledgers"]}
    accounts = {(a["tenant_id"], a["id"]): a for a in fx["accounts"]}
    entries = {(e["tenant_id"], e["id"]): e for e in fx["entries"]}

    revenue = defaultdict(Decimal)
    currency = {}
    entry_net = defaultdict(Decimal)
    for line in fx["lines"]:
        tenant = line["tenant_id"]
        entry = entries[(tenant, line["entry_id"])]
        ledger = ledgers[(tenant, entry["ledger_id"])]
        account = accounts[(tenant, line["main_account_id"])]
        amount = Decimal(line["amount_acct"])
        side, negative = line["debit_credit"], line["negative_posting"]
        debit = credit = Decimal(0)
        if side == "D" and not negative:
            debit = amount
        elif side == "C" and negative:
            debit = -amount
        elif side == "C" and not negative:
            credit = amount
        else:  # D and negative
            credit = -amount
        entry_net[(tenant, entry["id"])] += debit - credit

        key = (tenant, line["legal_entity_id"], entry["posting_date"])
        currency[key] = ledger["currency"]
        revenue[key] += Decimal(0)
        if account["account_type"] == "revenue":
            revenue[key] += credit - debit

    unbalanced = {k: v for k, v in entry_net.items() if v != 0}
    if unbalanced:
        raise SystemExit(f"unbalanced entries: {unbalanced}")

    out = [
        {
            "tenantId": t,
            "legalEntityId": le,
            "date": d,
            "currency": currency[(t, le, d)],
            "glRevenue": format(revenue[(t, le, d)].quantize(Decimal("0.000001")), "f"),
        }
        for (t, le, d) in sorted(revenue)
    ]
    json.dump(out, sys.stdout, indent=2)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main(sys.argv[1])
