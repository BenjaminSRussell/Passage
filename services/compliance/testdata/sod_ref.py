"""Independent reference for fx-sod-50-distinct (PSG-48 AC2).

Run with `python3 -I sod_ref.py <fixture>` (stdlib only). It restates the eleven seed rules of FR-48.4
and matches duty patterns with fnmatch, sharing no code with the Go engine, and checks that every
user's hand-derived `expect` list equals the rules a user's effective duties violate, that the 50
planted role combinations are pairwise distinct and cover every seed rule, and that the near-miss
users violate nothing. Exits non-zero on the first disagreement.
"""

import json
import sys
from fnmatch import fnmatchcase

SEED_RULES = [
    ("P2P-001", "MaintainVendors", "ProcessVendorPayments"),
    ("P2P-002", "CreatePurchaseOrders", "ApprovePurchaseOrders"),
    ("P2P-003", "PostVendorInvoices", "ApproveVendorInvoices"),
    ("P2P-004", "ReceiveGoods", "PostVendorInvoices"),
    ("O2C-001", "MaintainCustomers", "PostCustomerPayments"),
    ("O2C-002", "CreateSalesOrders", "ApproveCreditLimits"),
    ("O2C-003", "IssueCreditNotes", "PostCustomerPayments"),
    ("GL-001", "PostGeneralJournals", "ApproveGeneralJournals"),
    ("GL-002", "MaintainChartOfAccounts", "PostGeneralJournals"),
    ("SEC-001", "MaintainUsersAndRoles", "*Post*"),
    ("HR-001", "MaintainWorkerBankDetails", "RunPayroll"),
]


def violations(duties):
    out = []
    for rule_id, a, b in SEED_RULES:
        ma = {d for d in duties if fnmatchcase(d, a)}
        mb = {d for d in duties if fnmatchcase(d, b)}
        if any(x != y for x in ma for y in mb):
            out.append(rule_id)
    return sorted(out)


def main():
    fx = json.load(open(sys.argv[1], encoding="utf-8"))
    catalog = fx["roles"]
    combos = set()
    covered = set()
    for group in ("users", "nearMisses"):
        for u in fx[group]:
            duties = {d for r in u["roles"] for d in catalog[r]}
            got = violations(duties)
            if got != u["expect"]:
                print("MISMATCH %s: rules %s, fixture expects %s" % (u["userId"], got, u["expect"]))
                sys.exit(1)
            if group == "users":
                if not got:
                    print("MISMATCH %s: a planted user must conflict" % u["userId"])
                    sys.exit(1)
                combos.add(frozenset(u["roles"]))
                covered.update(got)
    if len(fx["users"]) != 50 or len(combos) != 50:
        print("MISMATCH: %d planted users, %d distinct role combinations" % (len(fx["users"]), len(combos)))
        sys.exit(1)
    if covered != {r[0] for r in SEED_RULES}:
        print("MISMATCH: seed rules not covered: %s" % sorted({r[0] for r in SEED_RULES} - covered))
        sys.exit(1)
    total = sum(len(u["expect"]) for u in fx["users"])
    print("ok: 50 distinct combinations, %d conflicts, %d near misses clean, 11 rules covered" % (total, len(fx["nearMisses"])))


if __name__ == "__main__":
    main()
