"""Independent reference for the sscc-200 fixture (PRD section 5.28.9).

Run with: python3 -I testdata/gen_sscc200.py testdata/sscc-200.json

It shares no code with the Go engine. It writes:

- "sscc": exactly 200 SSCC-18 strings, valid and invalid, with the verdict of a
  GS1 mod-10 check written here from the GS1 General Specifications (section
  7.9: weights 3 and 1 alternating from the rightmost body digit, check digit =
  (10 - sum mod 10) mod 10). Invalid strings are derived from valid ones by
  single-digit substitution, adjacent transposition, truncation, extension and
  foreign characters. The verdict of every derived string is recomputed, never
  assumed: a transposition of two digits that differ by 5 keeps the mod-10 sum
  and is still a valid SSCC (a known blind spot of the GS1 check), so it is
  recorded as valid.
- "gs1": GS1-128 element strings over the application identifiers the pick flow
  reads (00 SSCC, 01 GTIN, 10 batch, 17 expiry, 21 serial, 30 and 37 count), in
  the bracketed human-readable form and the raw form with FNC1 (0x1D) after
  every variable-length field that is not last. Every non-empty combination of
  the seven identifiers that does not hold both counts (30 and 37 fill the same
  field) appears once in each form; expected fields are the generated values.
  Invalid strings name the GS1 rule they break and the error class the parser
  must return.

The script self-checks against the GS1 worked examples before writing.
"""

import json
import random
import sys

FNC1 = "\x1d"
# GS1 AI encodable character set 82 (GS1 General Specifications, figure 7.11-1).
CSET82 = "!\"%&'()*+,-./0123456789:;<=>?ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz"
assert len(CSET82) == 82
# The bracketed form cannot carry parentheses inside a value.
CSET82_HRI = CSET82.replace("(", "").replace(")", "")


def check_digit(body):
    total = 0
    for i, ch in enumerate(reversed(body)):
        total += (3 if i % 2 == 0 else 1) * (ord(ch) - 48)
    return (10 - total % 10) % 10


def sscc_verdict(s):
    """Return None for a valid SSCC-18, else 'format' or 'check'."""
    if len(s) != 18 or any(c not in "0123456789" for c in s):
        return "format"
    if check_digit(s[:17]) != ord(s[17]) - 48:
        return "check"
    return None


def make_sscc(ext, prefix, serial):
    width = 16 - len(prefix)
    body = str(ext) + prefix + str(serial).zfill(width)
    assert len(body) == 17
    return body + str(check_digit(body))


# Self-checks against published examples.
assert make_sscc(1, "0614141", 123456789) == "106141411234567897"  # GS1 General Specifications
assert check_digit("0061414100001") == 2  # GTIN-14 00614141000012
assert make_sscc(0, "0614141", 12345678) == "006141410123456789"
assert sscc_verdict("0061414101234567891") == "format"  # PRD AC-28.3 string has 19 digits


def build_sscc(rng):
    cases = []

    def add(kind, value, **extra):
        verdict = sscc_verdict(value)
        case = {"id": "S%03d" % (len(cases) + 1), "kind": kind, "input": value, "valid": verdict is None}
        if verdict:
            case["error"] = verdict
        case.update(extra)
        cases.append(case)

    add("gs1-example", "106141411234567897", extension=1, prefix="0614141", serial=123456789)
    add("ac-28.3-repo-example", "006141410123456789", extension=0, prefix="0614141", serial=12345678)
    add("ac-28.3-prd-string", "0061414101234567891")

    valid = []
    for plen in range(6, 13):
        width = 16 - plen
        for k in range(14):
            ext = rng.randrange(10)
            prefix = "".join(rng.choice("0123456789") for _ in range(plen))
            if k == 0:
                serial = 0
            elif k == 1:
                serial = 10 ** width - 1
            else:
                serial = rng.randrange(10 ** width)
            s = make_sscc(ext, prefix, serial)
            valid.append(s)
            add("valid-prefix-%d" % plen, s, extension=ext, prefix=prefix, serial=serial)

    # Derived strings: each from a different valid SSCC, chosen by the seed.
    def pick():
        return valid[rng.randrange(len(valid))]

    for _ in range(30):
        s = pick()
        pos = rng.randrange(18)
        d = (ord(s[pos]) - 48 + rng.randrange(1, 10)) % 10
        add("substitution", s[:pos] + str(d) + s[pos + 1:])
    n = 0
    while n < 30:
        s = pick()
        pos = rng.randrange(17)
        if s[pos] == s[pos + 1]:
            continue
        t = s[:pos] + s[pos + 1] + s[pos] + s[pos + 2:]
        add("adjacent-transposition", t)
        n += 1
    for _ in range(8):
        add("truncated-17", pick()[:17])
    for _ in range(8):
        s = pick()
        add("extended-19", s + str(rng.randrange(10)))
    for ch in ["A", " ", "-", "O", "x", "٠", "."]:
        s = pick()
        pos = rng.randrange(18)
        add("foreign-character", s[:pos] + ch + s[pos + 1:])
    add("empty", "")
    add("all-zeros", "000000000000000000")
    add("all-zeros-bad-check", "000000000000000001")
    add("hri-with-ai", "(00)" + pick())
    while len(cases) < 200:
        s = pick()
        add("check-digit-off-by-one", s[:17] + str((ord(s[17]) - 48 + 1) % 10))
    assert len(cases) == 200
    return cases


AIS = ["00", "01", "10", "17", "21", "30", "37"]
FIXED = {"00": 18, "01": 14, "17": 6}
MAXLEN = {"10": 20, "21": 20, "30": 8, "37": 8}
FIELD = {"00": "sscc", "01": "gtin", "10": "batch", "17": "expiry", "21": "serial", "30": "count", "37": "count"}


def days_in_month(yy, mm):
    if mm == 2:
        return 29 if yy % 4 == 0 else 28
    return 30 if mm in (4, 6, 9, 11) else 31


def gen_value(rng, ai, hri):
    if ai == "00":
        return make_sscc(rng.randrange(10), "0614141", rng.randrange(10 ** 9))
    if ai == "01":
        body = "".join(rng.choice("0123456789") for _ in range(13))
        return body + str(check_digit(body))
    if ai == "17":
        yy, mm = rng.randrange(100), rng.randrange(1, 13)
        dd = rng.choice([0, rng.randrange(1, days_in_month(yy, mm) + 1), days_in_month(yy, mm)])
        return "%02d%02d%02d" % (yy, mm, dd)
    if ai in ("10", "21"):
        cs = CSET82_HRI if hri else CSET82
        return "".join(rng.choice(cs) for _ in range(rng.randrange(1, MAXLEN[ai] + 1)))
    n = rng.randrange(1, MAXLEN[ai] + 1)
    return str(rng.randrange(1, 10 ** n))


def encode(pairs, hri, extra_fnc1=False):
    if hri:
        return "".join("(%s)%s" % p for p in pairs)
    out = ""
    for i, (ai, v) in enumerate(pairs):
        out += ai + v
        last = i == len(pairs) - 1
        if not last and (ai not in FIXED or extra_fnc1):
            out += FNC1
    return out


def expected(pairs):
    exp = {"sscc": "", "gtin": "", "batch": "", "serial": "", "expiry": "", "count": 0}
    for ai, v in pairs:
        exp[FIELD[ai]] = int(v) if FIELD[ai] == "count" else v
    return exp


def build_gs1(rng):
    cases = []
    for mask in range(1, 1 << len(AIS)):
        ais = [a for i, a in enumerate(AIS) if mask >> i & 1]
        if "30" in ais and "37" in ais:
            continue
        for hri in (True, False):
            order = ais[:]
            rng.shuffle(order)
            pairs = [(a, gen_value(rng, a, hri)) for a in order]
            # Every fourth raw string also carries a superfluous FNC1 after its
            # fixed-length fields, which a decoder must tolerate.
            extra = (not hri) and len(cases) % 4 == 0
            cases.append({"id": "G%03d" % (len(cases) + 1), "form": "hri" if hri else "raw", "ais": order,
                          "input": encode(pairs, hri, extra), "valid": True, "want": expected(pairs)})
    good_sscc = make_sscc(0, "0614141", 12345678)
    invalid = [
        # (rule, input, error class)
        ("AI 17 month 13 (GS1 GenSpecs 7.12: MM 01-12)", "(17)261305", "value"),
        ("AI 17 month 00", "(17)260015", "value"),
        ("AI 17 day 32", "(17)260132", "value"),
        ("AI 17 31 April", "(17)260431", "value"),
        ("AI 17 30 February", "(17)280230", "value"),
        ("AI 17 29 February in a non-leap year", "(17)270229", "value"),
        ("AI 17 not numeric", "(17)26013A", "value"),
        ("AI 17 raw month 13", "17261305", "value"),
        ("AI 10 space is outside character set 82", "(10)LOT 7", "value"),
        ("AI 10 '#' is outside character set 82", "(10)LOT#7", "value"),
        ("AI 21 '@' is outside character set 82", "(21)SN@1", "value"),
        ("AI 21 non-ASCII", "(21)SNé1", "value"),
        ("AI 10 raw with '~'", "10LOT~7", "value"),
        ("AI 10 twice with different values (GenSpecs 4.14)", "(10)A1(10)B2", "value"),
        ("AI 01 twice with different values", "(01)00614141000012(01)10614141000019", "value"),
        ("GTIN check digit wrong", "(01)00614141000013", "value"),
        ("SSCC check digit wrong", "(00)" + good_sscc[:17] + str((int(good_sscc[17]) + 1) % 10), "value"),
        ("AI 37 not numeric", "(37)5X", "value"),
        ("AI 01 too short", "(01)0061414100001", "parse"),
        ("AI 00 too long", "(00)" + good_sscc + "0", "parse"),
        ("AI 10 longer than 20", "(10)ABCDEFGHIJKLMNOPQRSTU", "parse"),
        ("AI 30 longer than 8", "(30)123456789", "parse"),
        ("empty variable-length value", "(10)(37)5", "parse"),
        ("raw fixed-length value truncated", "0100614141", "parse"),
        ("raw identifier truncated", "37" + "5" + FNC1 + "1", "parse"),
        ("unknown AI 99", "(99)123", "unknown"),
        ("raw unknown AI 02", "0200614141000012", "unknown"),
    ]
    for rule, value, cls in invalid:
        cases.append({"id": "G%03d" % (len(cases) + 1), "form": "hri" if value.startswith("(") else "raw",
                      "rule": rule, "input": value, "valid": False, "error": cls})
    # A repeated identifier with the same value is one element string, not a conflict.
    cases.append({"id": "G%03d" % (len(cases) + 1), "form": "hri", "ais": ["10", "10"], "input": "(10)LOT7(10)LOT7",
                  "valid": True, "want": expected([("10", "LOT7")])})
    return cases


def main():
    rng = random.Random(200)
    doc = {
        "fixture": "sscc-200",
        "source": "testdata/gen_sscc200.py (python3 -I), independent of the Go engine",
        "sscc": build_sscc(rng),
        "gs1": build_gs1(rng),
    }
    with open(sys.argv[1], "w", encoding="ascii") as f:
        json.dump(doc, f, indent=1, sort_keys=True, ensure_ascii=True)
        f.write("\n")


if __name__ == "__main__":
    main()
