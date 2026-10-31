"""Independent reference for the Wave 34 rating fixture (AC-34.2, issue #34).

Run with `python3 -I gen_rating_200.py [out.json]` (default: rating-200.json next to
this file). It prices 200 shipments against 12 rate cards for 9 carrier services
and writes every expected quote. Nothing here imports or calls the Go engine: the
rules are coded from the PRD (docs/prd/phase-6-logistics.md BR-34.2, BR-34.10) and
the rating conventions listed in services/transport/README.md, in exact rational
arithmetic (fractions.Fraction), so expected charges are never re-derived from the
code under test.

Rules (BR-34.2, BR-34.10):
  * per (carrier, service) rate every card valid on the ship date (validFrom <=
    day <= validTo, validTo inclusive, null open); a service with cards but none
    valid yields one NO_VALID_RATE_CARD quote with total 0 and no charges;
  * chargeable weight = max(actual kg, volume m3 * 1e6 / dimDivisor), to 0.01 kg
    half-up; distance in miles = km / 1.609344 to 0.01 mi half-up (both are the
    quantities the explain trace shows);
  * row lookup: origin and destination equal to the shipment's or "*", quantity in
    [breakFrom, breakTo); the row with the most exact zone matches wins. The
    quantity is chargeable kg (KG), pallets (PALLET), km (KM), miles (MILE), or 1
    (CONTAINER: a shipment is one container; SHIPMENT). No row: NO_LANE_RATE;
  * base = round(rate * quantity, 2); on WEIGHT_BREAK cards with a KG row that has
    an upper bound, the next break (same lane, starts at breakTo) priced at its
    lower bound replaces the base when lower (deficit weight rule);
  * minimum charge lifts the base;
  * each requested accessorial on the card: percent of base when pct > 0, else the
    flat amount; codes not on the card are not charged;
  * fuel = base * fuelPct / 100 when fuelPct > 0;
  * every charge rounded to cents half-up; total = sum of charges;
  * ranking: OK quotes by total, then carrier id, then service id; then the other
    quotes by carrier id and service id. Equal keys keep card order.

The fixture is built to be adversarial rather than tuned: weights and distances sit
on and next to every break (including 402.33 km, which is 249.997 mi and so rates
in the 250-mile band once rounded), shipments below every minimum, volumes that
make dimensional weight govern, zero pallets, ship dates on and around every card
boundary, unknown accessorial codes, and half-cent results that test half-up
rounding. The generator refuses to write a fixture in which a lookup ties between
two equally specific rows or in which the two readings of the deficit rule (the
same lane's next row vs a fresh lookup at the next break weight) disagree, because
the PRD does not settle those cases.

Before writing, it reproduces hand-worked examples (see HAND_CASES).
"""

from __future__ import annotations

import json
import random
import sys
from datetime import date, timedelta
from fractions import Fraction as F
from pathlib import Path

CENT = F(1, 100)
KM_PER_MILE = F("1.609344")


def r2(x: F) -> F:
    """Round a non-negative amount to 0.01, half up."""
    assert x >= 0, x
    n = x * 100
    whole = n.numerator // n.denominator
    if n - whole >= F(1, 2):
        whole += 1
    return F(whole, 100)


def money(x: F) -> str:
    assert x.denominator in (1, 2, 4, 5, 10, 20, 25, 50, 100), x
    cents = int(x * 100)
    return f"{cents // 100}.{cents % 100:02d}"


def dec(s: str | None) -> F | None:
    return None if s is None else F(s)


# --- rate cards -----------------------------------------------------------------

def row(o, d, unit, frm, to, rate):
    return {"origin": o, "destination": d, "unit": unit, "breakFrom": frm, "breakTo": to, "rate": rate}


def acc(code, flat="0", pct="0"):
    return {"code": code, "flat": flat, "pct": pct}


CARDS = [
    {"id": "rc-a-ltl-2026", "carrierId": "car-a", "serviceId": "ltl", "basis": "LANE", "currency": "USD",
     "validFrom": "2026-01-01", "validTo": "2026-12-31", "minimumCharge": "15.00", "dimDivisor": 5000, "fuelPct": "18.5",
     "rows": [row("CHI", "DAL", "KG", "0", "100", "0.50"), row("CHI", "DAL", "KG", "100", "500", "0.42"),
              row("CHI", "*", "KG", "0", None, "0.61"), row("*", "DAL", "KG", "0", "50", "0.66"),
              row("*", "*", "KG", "0", None, "0.75")],
     "accessorials": [acc("LIFTGATE", flat="25"), acc("RESI", pct="5"), acc("INSIDE", flat="40.50"), acc("HAZMAT", pct="12.5")]},
    {"id": "rc-a-ltl-2027", "carrierId": "car-a", "serviceId": "ltl", "basis": "LANE", "currency": "USD",
     "validFrom": "2027-01-01", "validTo": None, "minimumCharge": "17.50", "dimDivisor": 5000, "fuelPct": "21.25",
     "rows": [row("*", "*", "KG", "0", None, "0.55"), row("CHI", "DAL", "KG", "0", None, "0.47")],
     "accessorials": [acc("LIFTGATE", flat="27")]},
    {"id": "rc-b-pallet", "carrierId": "car-b", "serviceId": "pallet", "basis": "PER_PALLET", "currency": "USD",
     "validFrom": "2026-03-01", "validTo": "2026-12-31", "minimumCharge": "60.00", "dimDivisor": 5000, "fuelPct": "15",
     "rows": [row("CHI", "*", "PALLET", "1", "6", "85"), row("CHI", "*", "PALLET", "6", None, "72.50"),
              row("NYC", "*", "PALLET", "1", None, "95.25"), row("*", "*", "PALLET", "1", None, "110")],
     "accessorials": [acc("LIFTGATE", flat="30"), acc("APPT", flat="15")]},
    {"id": "rc-c-tl", "carrierId": "car-c", "serviceId": "tl", "basis": "DISTANCE", "currency": "USD",
     "validFrom": "2026-01-01", "validTo": "2026-12-31", "minimumCharge": "450.00", "dimDivisor": 5000, "fuelPct": "22",
     "rows": [row("*", "*", "MILE", "0", "250", "2.85"), row("*", "*", "MILE", "250", "1000", "2.15"),
              row("*", "*", "MILE", "1000", None, "1.95")],
     "accessorials": [acc("STOPOFF", flat="75"), acc("TARP", flat="100")]},
    {"id": "rc-c-tlkm", "carrierId": "car-c", "serviceId": "tl-km", "basis": "DISTANCE", "currency": "USD",
     "validFrom": "2026-06-01", "validTo": "2026-08-31", "minimumCharge": "300.00", "dimDivisor": 5000, "fuelPct": "0",
     "rows": [row("*", "*", "KM", "0", None, "1.33")],
     "accessorials": [acc("STOPOFF", flat="60")]},
    {"id": "rc-d-parcel", "carrierId": "car-d", "serviceId": "parcel", "basis": "WEIGHT_BREAK", "currency": "USD",
     "validFrom": "2026-01-01", "validTo": "2027-06-30", "minimumCharge": "9.95", "dimDivisor": 6000, "fuelPct": "9.75",
     "rows": [row("*", "*", "KG", "0", "45", "1.20"), row("*", "*", "KG", "45", "100", "0.98"),
              row("*", "*", "KG", "100", "300", "0.81"), row("*", "*", "KG", "300", "500", "0.70"),
              row("*", "*", "KG", "500", None, "0.66"),
              row("CHI", "NYC", "KG", "0", "45", "1.05"), row("CHI", "NYC", "KG", "45", "100", "0.90"),
              row("CHI", "NYC", "KG", "100", None, "0.79")],
     "accessorials": [acc("RESI", flat="4.65"), acc("SIGNATURE", flat="6.10"), acc("DAS", pct="3")]},
    {"id": "rc-e-ocean", "carrierId": "car-e", "serviceId": "ocean", "basis": "PER_CONTAINER", "currency": "USD",
     "validFrom": "2026-01-01", "validTo": "2026-12-31", "minimumCharge": "0", "dimDivisor": 5000, "fuelPct": "0",
     "rows": [row("SHA", "LAX", "CONTAINER", "0", None, "2450"), row("SHA", "*", "CONTAINER", "0", None, "2875.50"),
              row("*", "*", "CONTAINER", "0", None, "3300")],
     "accessorials": [acc("BAF", pct="11.5"), acc("THC", flat="385")]},
    {"id": "rc-f-flat", "carrierId": "car-f", "serviceId": "expedite", "basis": "FLAT", "currency": "USD",
     "validFrom": "2026-01-01", "validTo": "2026-12-31", "minimumCharge": "0", "dimDivisor": 5000, "fuelPct": "18",
     "rows": [row("*", "*", "SHIPMENT", "0", None, "1250")],
     "accessorials": [acc("LIFTGATE", flat="35.35")]},
    {"id": "rc-f-dist", "carrierId": "car-f", "serviceId": "expedite", "basis": "DISTANCE", "currency": "USD",
     "validFrom": "2026-01-01", "validTo": "2026-12-31", "minimumCharge": "400.00", "dimDivisor": 5000, "fuelPct": "18",
     "rows": [row("*", "*", "KM", "0", None, "2.10")],
     "accessorials": [acc("LIFTGATE", flat="35.35"), acc("HAZMAT", pct="7.5")]},
    {"id": "rc-g-old", "carrierId": "car-g", "serviceId": "ltl-old", "basis": "LANE", "currency": "USD",
     "validFrom": "2025-01-01", "validTo": "2025-12-31", "minimumCharge": "12.00", "dimDivisor": 5000, "fuelPct": "14",
     "rows": [row("*", "*", "KG", "0", None, "0.45")],
     "accessorials": [acc("LIFTGATE", flat="20")]},
    {"id": "rc-h-regional", "carrierId": "car-h", "serviceId": "regional", "basis": "LANE", "currency": "USD",
     "validFrom": "2026-01-01", "validTo": "2026-12-31", "minimumCharge": "20.00", "dimDivisor": 5000, "fuelPct": "16",
     "rows": [row("CHI", "DAL", "KG", "0", None, "0.38"), row("DAL", "CHI", "KG", "0", None, "0.39")],
     "accessorials": [acc("RESI", pct="2.5")]},
    {"id": "rc-h-regional-zone", "carrierId": "car-h", "serviceId": "regional", "basis": "ZONE", "currency": "USD",
     "validFrom": "2026-07-01", "validTo": "2026-09-30", "minimumCharge": "45.00", "dimDivisor": 4000, "fuelPct": "16",
     "rows": [row("CHI", "*", "PALLET", "1", None, "64.40"), row("*", "CHI", "PALLET", "1", None, "66.60")],
     "accessorials": []},
]

ZONES = ["CHI", "DAL", "NYC", "LAX", "SHA", "ATL"]
ACC_CODES = ["LIFTGATE", "RESI", "INSIDE", "HAZMAT", "APPT", "TARP", "STOPOFF", "SIGNATURE", "DAS", "THC", "BAF", "BOGUS"]


# --- the rules --------------------------------------------------------------------

def valid_on(card, day: date) -> bool:
    if day < date.fromisoformat(card["validFrom"]):
        return False
    return card["validTo"] is None or day <= date.fromisoformat(card["validTo"])


def chargeable(ship, divisor: int) -> F:
    dim = F(ship["volumeM3"]) * 1_000_000 / divisor
    return r2(max(F(ship["weightKg"]), dim))


def qty_for(unit: str, ship, cw: F) -> F:
    if unit == "KG":
        return cw
    if unit == "PALLET":
        return F(ship["pallets"])
    if unit == "KM":
        return F(ship["distanceKm"])
    if unit == "MILE":
        return r2(F(ship["distanceKm"]) / KM_PER_MILE)
    return F(1)  # CONTAINER (one per shipment) and SHIPMENT


def in_band(r, q: F) -> bool:
    return F(r["breakFrom"]) <= q and (r["breakTo"] is None or q < F(r["breakTo"]))


def lookup(rows, ship, cw):
    """Most specific matching row; raises on a tie the PRD does not settle."""
    found = []
    for r in rows:
        if r["origin"] not in ("*", ship["originZone"]) or r["destination"] not in ("*", ship["destinationZone"]):
            continue
        q = qty_for(r["unit"], ship, cw)
        if in_band(r, q):
            found.append(((r["origin"] != "*") + (r["destination"] != "*"), r, q))
    if not found:
        return None, None
    best = max(s for s, _, _ in found)
    top = [(r, q) for s, r, q in found if s == best]
    if len(top) > 1:
        raise SystemExit(f"ambiguous lookup {ship} -> {top}")
    return top[0]


def rate(card, ship):
    out = {"carrierId": card["carrierId"], "serviceId": card["serviceId"], "cardId": card["id"]}
    day = date.fromisoformat(ship["shipDate"])
    if not valid_on(card, day):
        return {**out, "status": "NO_VALID_RATE_CARD", "total": F(0), "charges": [], "flags": {}}
    cw = chargeable(ship, card["dimDivisor"])
    r, q = lookup(card["rows"], ship, cw)
    if r is None:
        return {**out, "status": "NO_LANE_RATE", "total": F(0), "charges": [], "flags": {}}
    flags = {"dimGoverns": r["unit"] == "KG" and F(ship["volumeM3"]) * 1_000_000 / card["dimDivisor"] > F(ship["weightKg"])}
    base = r2(F(r["rate"]) * q)
    flags["weightBreakStep"] = False
    flags["deficit"] = False
    if card["basis"] == "WEIGHT_BREAK" and r["unit"] == "KG" and r["breakTo"] is not None:
        nxt = [n for n in card["rows"] if n["origin"] == r["origin"] and n["destination"] == r["destination"]
               and n["unit"] == r["unit"] and F(n["breakFrom"]) == F(r["breakTo"])]
        if nxt:
            flags["weightBreakStep"] = True
            deficit = r2(F(nxt[0]["rate"]) * F(nxt[0]["breakFrom"]))
            # The other reading: look the shipment up afresh at the next break weight.
            probe = dict(ship, weightKg=str(F(r["breakTo"])), volumeM3="0")
            r_alt, q_alt = lookup(card["rows"], probe, F(r["breakTo"]))
            if r_alt is None or r2(F(r_alt["rate"]) * q_alt) != deficit:
                raise SystemExit(f"deficit readings disagree for {ship} on {card['id']}")
            if deficit < base:
                base, flags["deficit"] = deficit, True
    minimum = F(card["minimumCharge"])
    flags["minimumApplied"] = base < minimum
    if base < minimum:
        base = r2(minimum)
    charges = [("BASE", base)]
    half_cent = 0
    for code in ship["accessorials"]:
        a = next((x for x in card["accessorials"] if x["code"] == code), None)
        if a is None:
            continue
        if F(a["pct"]) > 0:
            exact = base * F(a["pct"]) / 100
            half_cent += (exact * 100) % 1 == F(1, 2)
            charges.append(("ACC:" + code, r2(exact)))
        else:
            charges.append(("ACC:" + code, r2(F(a["flat"]))))
    if F(card["fuelPct"]) > 0:
        exact = base * F(card["fuelPct"]) / 100
        half_cent += (exact * 100) % 1 == F(1, 2)
        charges.append(("FUEL", r2(exact)))
    flags["halfCent"] = half_cent
    return {**out, "status": "OK", "total": sum((c[1] for c in charges), F(0)), "charges": charges, "flags": flags}


def shop(cards, ship):
    groups: dict[tuple[str, str], list] = {}
    for c in cards:
        groups.setdefault((c["carrierId"], c["serviceId"]), []).append(c)
    quotes = []
    for key, cs in groups.items():
        valid = [c for c in cs if valid_on(c, date.fromisoformat(ship["shipDate"]))]
        quotes.extend(rate(c, ship) for c in (valid or cs[:1]))
    ok = [q for q in quotes if q["status"] == "OK"]
    rest = [q for q in quotes if q["status"] != "OK"]
    ok.sort(key=lambda q: (q["total"], q["carrierId"], q["serviceId"]))
    rest.sort(key=lambda q: (q["carrierId"], q["serviceId"]))
    return ok + rest


# --- hand-worked checks --------------------------------------------------------------

HAND_CARD = {"id": "hand", "carrierId": "car-a", "serviceId": "ltl", "basis": "LANE", "currency": "USD",
             "validFrom": "2026-01-01", "validTo": "2026-12-31", "minimumCharge": "15", "dimDivisor": 5000, "fuelPct": "18.5",
             "rows": [row("CHI", "DAL", "KG", "0", "100", "0.50"), row("*", "*", "KG", "100", None, "0.40")],
             "accessorials": [acc("LIFTGATE", flat="25"), acc("RESI", pct="5")]}
HAND_WB = {"id": "hand-wb", "carrierId": "car-d", "serviceId": "parcel", "basis": "WEIGHT_BREAK", "currency": "USD",
           "validFrom": "2026-01-01", "validTo": None, "minimumCharge": "0", "dimDivisor": 5000, "fuelPct": "0",
           "rows": [row("*", "*", "KG", "0", "100", "1.00"), row("*", "*", "KG", "100", None, "0.80")], "accessorials": []}
HAND_MI = {"id": "hand-mi", "carrierId": "car-c", "serviceId": "tl", "basis": "DISTANCE", "currency": "USD",
           "validFrom": "2026-01-01", "validTo": None, "minimumCharge": "0", "dimDivisor": 5000, "fuelPct": "0",
           "rows": [row("*", "*", "MILE", "0", None, "2.00")], "accessorials": []}


def ship_of(o="CHI", d="DAL", kg="0", m3="0", pallets=1, km="0", day="2026-06-01", accs=()):
    return {"originZone": o, "destinationZone": d, "weightKg": kg, "volumeM3": m3, "pallets": pallets,
            "distanceKm": km, "shipDate": day, "accessorials": list(accs)}


# (card, shipment, total worked by hand, why)
HAND_CASES = [
    (HAND_CARD, ship_of(kg="80", accs=["LIFTGATE"]), "72.40", "80 x 0.50 = 40.00; +25.00; fuel 18.5% of 40 = 7.40"),
    (HAND_CARD, ship_of(kg="10"), "17.78", "10 x 0.50 = 5 < minimum 15; fuel 2.775 -> 2.78 half-up"),
    (HAND_CARD, ship_of(kg="20", m3="0.5"), "47.40", "dim 0.5e6/5000 = 100 kg -> * row 0.40 = 40; fuel 7.40"),
    (HAND_CARD, ship_of(kg="80", accs=["RESI"]), "49.40", "40 + 5% 2.00 + fuel 7.40"),
    (HAND_CARD, ship_of(kg="80", accs=["BOGUS"]), "47.40", "unknown code not charged"),
    (HAND_CARD, ship_of(kg="80", day="2027-01-01"), "0.00", "card ended 2026-12-31: NO_VALID_RATE_CARD"),
    (HAND_WB, ship_of(kg="90"), "80.00", "90 x 1.00 = 90 > 100 x 0.80 = 80: deficit"),
    (HAND_WB, ship_of(kg="60"), "60.00", "60 x 1.00 = 60 < 80: no deficit"),
    (HAND_MI, ship_of(km="160.9344"), "200.00", "100 mi x 2.00"),
    (HAND_MI, ship_of(km="1"), "1.24", "1 km = 0.621371 mi -> 0.62 mi x 2.00 = 1.24"),
]


def self_check() -> None:
    for card, ship, want, why in HAND_CASES:
        got = money(rate(card, ship)["total"])
        if got != want:
            raise SystemExit(f"hand case failed ({why}): got {got}, want {want}")
    assert r2(F("2.775")) == F("2.78") and r2(F("2.774999")) == F("2.77")


# --- shipments -------------------------------------------------------------------------

def shipments() -> list[dict]:
    rng = random.Random(3402)
    edge_kg = ["0.4", "9.99", "44.99", "45", "45.01", "50", "99.99", "100", "100.01", "299.99", "300", "499.99", "500", "500.01"]
    edge_km = ["0", "1", "402.33", "402.34", "402.3", "1609.34", "1609.35", "225.56", "1609.344"]
    edge_days = ["2025-12-31", "2026-01-01", "2026-02-28", "2026-03-01", "2026-05-31", "2026-06-01", "2026-06-30",
                 "2026-07-01", "2026-08-31", "2026-09-01", "2026-09-30", "2026-12-31", "2027-01-01", "2027-06-30", "2027-07-01"]
    out = []
    for i in range(200):
        o, d = rng.sample(ZONES, 2)
        if i % 9 == 0:
            o, d = "CHI", "DAL"
        elif i % 9 == 1:
            o, d = "CHI", "NYC"
        elif i % 9 == 2:
            o, d = "SHA", rng.choice(["LAX", "NYC"])
        elif i % 9 == 3:
            o, d = "DAL", "CHI"
        kg = rng.choice(edge_kg) if i % 3 == 0 else f"{rng.uniform(0.5, 2400):.2f}"
        m3 = "0" if i % 4 == 0 else f"{rng.choice([rng.uniform(0, 0.4), rng.uniform(0.4, 6.5)]):.3f}"
        km = rng.choice(edge_km) if i % 5 == 0 else f"{rng.uniform(5, 3200):.1f}"
        pallets = rng.choice([0, 1, 5, 6, 7, rng.randint(1, 26)])
        day = rng.choice(edge_days) if i % 2 == 0 else (date(2025, 12, 1) + timedelta(days=rng.randint(0, 420))).isoformat()
        accs = rng.sample(ACC_CODES, rng.choice([0, 0, 1, 2, 3, 4]))
        out.append({"id": f"S{i + 1:03d}", **ship_of(o, d, kg, m3, pallets, km, day, accs)})
    return out


def main() -> None:
    self_check()
    target = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).with_name("rating-200.json")
    cases, stats = [], {"quotes": 0, "OK": 0, "NO_VALID_RATE_CARD": 0, "NO_LANE_RATE": 0, "deficit": 0,
                        "weightBreakStep": 0, "minimumApplied": 0, "dimGoverns": 0, "halfCentRoundings": 0}
    for s in shipments():
        quotes = shop(CARDS, s)
        exp = []
        for q in quotes:
            stats["quotes"] += 1
            stats[q["status"]] += 1
            f = q["flags"]
            for k in ("deficit", "weightBreakStep", "minimumApplied", "dimGoverns"):
                stats[k] += bool(f.get(k))
            stats["halfCentRoundings"] += f.get("halfCent", 0)
            exp.append({"carrierId": q["carrierId"], "serviceId": q["serviceId"], "status": q["status"],
                        "total": money(q["total"]), "charges": [{"code": c, "amount": money(a)} for c, a in q["charges"]],
                        "weightBreakStep": bool(f.get("weightBreakStep")), "deficit": bool(f.get("deficit")),
                        "minimumApplied": bool(f.get("minimumApplied"))})
        sid = s.pop("id")
        cases.append({"id": sid, "shipment": s, "expected": exp})
    # One card and one case per line keeps the file small and its diffs readable.
    lines = ['{"generator":"gen_rating_200.py",', '"stats":' + json.dumps(stats, separators=(",", ":")) + ",", '"cards":[']
    lines += [json.dumps(c, separators=(",", ":")) + ("," if i < len(CARDS) - 1 else "") for i, c in enumerate(CARDS)]
    lines += ["],", '"cases":[']
    lines += [json.dumps(c, separators=(",", ":")) + ("," if i < len(cases) - 1 else "") for i, c in enumerate(cases)]
    lines += ["]}"]
    target.write_text("\n".join(lines) + "\n")


if __name__ == "__main__":
    main()
