#!/usr/bin/env python3
# Regenerates gs1-ai-vectors.json, the PRD 6.32.9 fixture `gs1-ai-vectors` for the
# Wave 32 proof "GS1 application-identifier parser passes the GS1 test suite"
# (AC-32.1, FR-32.3, BR-32.1).
#
#   python3 -I gen_gs1_ai_vectors.py > gs1-ai-vectors.json
#
# Two kinds of vector:
#
# 1. GS1 suite vectors, transcribed from the GS1 Barcode Syntax Engine unit tests
#    (https://github.com/gs1/gs1-syntax-engine, Apache-2.0, commit pinned in
#    GS1_COMMIT). Each carries its source file and line and GS1's own verdict
#    (pass or fail). Raw element strings use "^" for FNC1 as the suite does; a
#    leading "^" is FNC1 in first position, which the scanner reports as the
#    symbology identifier, so it is dropped, and every other "^" becomes GS (0x1D).
#    Component-linter vectors (check digit, date, character set) are wrapped in a
#    BR-32.1 AI that uses that component: a 14-, 13-, 12- or 8-digit check-digit
#    string in AI 01 (right-justified with zeros, as GS1 encodes GTIN-8/12/13), a
#    13-digit one also in AI 410 and AI 412, an 18-digit one in AI 00, a date in
#    AI 17 and AI 11, a set-82 string in AI 10 and a numeric string in AI 37.
#
# 2. Passage edge vectors (60), written here by hand for BR-32.1 boundaries the
#    suite does not reach: every AI's length limits, 310n decimals, the century
#    window, DD=00 in leap and common years, repeated AIs, AIs embedded in data,
#    and an adjacent-digit transposition the mod-10 check cannot see.
#
# The expected result of every vector comes from the reference parser below, which
# is written from BR-32.1 and the GS1 General Specifications (7.9 check digit,
# 7.11 character set 82, 7.12 century, 5.10.1 predefined-length AIs, 4.14 repeated
# AIs), not from gs1.ts; nothing is shared with the TypeScript engine. For every
# GS1 suite vector the script also checks that the reference parser agrees with
# GS1's verdict and stops if it does not, so the suite's verdicts are the
# authority and the reference parser only adds the normalised AI map.
#
# Excluded suite tests are listed in the output with the reason (AIs outside the
# BR-32.1 table, association rules, encoder-only limits, bracketed-input syntax).
import calendar
import json
import sys
from decimal import Decimal

GS1_REPO = "https://github.com/gs1/gs1-syntax-engine"
GS1_COMMIT = "58c1dc85d9ff4eb9368293bcf5264a9fe70bfc72"
REFERENCE_DATE = "2026-01-15"
REF_YEAR = 2026

GS = "\x1d"
CHECK, MALFORMED, DATE = "MOB-0102", "MOB-0103", "MOB-0104"
DIGITS = "0123456789"
# GS1 General Specifications figure 7.11-1, character set 82.
SET82 = set(DIGITS + "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz" + "!\"%&'()*+,-./:;<=>?_")


class Spec:
    def __init__(self, charset, lo, hi, predefined, kind=None):
        self.charset, self.lo, self.hi, self.predefined, self.kind = charset, lo, hi, predefined, kind


# BR-32.1 AI table. predefined = fixed length with no FNC1 after it (GS1 GS 5.10.1:
# AIs starting 00, 01, 11..20, 31..36 and 41 have a predefined length).
TABLE = {
    "00": Spec("N", 18, 18, True, "csum"),
    "01": Spec("N", 14, 14, True, "csum"),
    "10": Spec("X", 1, 20, False),
    "11": Spec("N", 6, 6, True, "date"),
    "17": Spec("N", 6, 6, True, "date"),
    "21": Spec("X", 1, 20, False),
    "37": Spec("N", 1, 8, False),
    "400": Spec("X", 1, 30, False),
    "410": Spec("N", 13, 13, True, "csum"),
    "412": Spec("N", 13, 13, True, "csum"),
    "420": Spec("X", 1, 20, False),
}
for n in range(6):
    TABLE["310%d" % n] = Spec("N", 6, 6, True, "weight")
for ai in range(91, 100):
    TABLE[str(ai)] = Spec("X", 1, 90, False)


def all_digits(s):
    return len(s) > 0 and all(c in DIGITS for c in s)


def check_digit(payload):
    """GS1 GS 7.9.1: pad to 17 digits; odd positions (from the left) weigh 3."""
    p = payload.rjust(17, "0")
    total = sum(int(c) * (3 if i % 2 == 0 else 1) for i, c in enumerate(p))
    return str((10 - total % 10) % 10)


def csum_ok(s):
    return check_digit(s[:-1]) == s[-1]


def full_year(yy):
    """GS1 GS 7.12: a difference of 51..99 years is the previous century, -99..-50 the next."""
    cur = REF_YEAR % 100
    century = REF_YEAR - cur
    diff = yy - cur
    if 51 <= diff <= 99:
        century -= 100
    elif -99 <= diff <= -50:
        century += 100
    return century + yy


def iso_date(yymmdd):
    year, month, day = full_year(int(yymmdd[:2])), int(yymmdd[2:4]), int(yymmdd[4:])
    if not 1 <= month <= 12:
        return None
    last = calendar.monthrange(year, month)[1]
    if day == 0:
        day = last
    if day > last:
        return None
    return "%04d-%02d-%02d" % (year, month, day)


def match_ai(data, i):
    for size in (4, 3, 2):
        ai = data[i:i + size]
        if len(ai) == size and ai in TABLE:
            return ai
    return None


def reference_parse(data):
    """Returns (ok, fields, order, first_error) for a raw element string."""
    if data.replace(GS, "") == "":
        return False, {}, [], MALFORMED
    fields, order, raw_seen = {}, [], {}
    i = 0
    while i < len(data):
        if data[i] == GS:
            i += 1
            continue
        ai = match_ai(data, i)
        if ai is None:
            return False, fields, order, MALFORMED
        spec = TABLE[ai]
        i += len(ai)
        if spec.predefined:
            raw = data[i:i + spec.hi]
            i += spec.hi
            if len(raw) != spec.hi or not all_digits(raw):
                return False, fields, order, MALFORMED
        else:
            end = data.find(GS, i)
            raw = data[i:] if end < 0 else data[i:end]
            i += len(raw)
            if not spec.lo <= len(raw) <= spec.hi:
                return False, fields, order, MALFORMED
            if spec.charset == "N" and not all_digits(raw):
                return False, fields, order, MALFORMED
            if spec.charset == "X" and not all(c in SET82 for c in raw):
                return False, fields, order, MALFORMED
        value = raw
        if spec.kind == "csum" and not csum_ok(raw):
            return False, fields, order, CHECK
        if spec.kind == "date":
            value = iso_date(raw)
            if value is None:
                return False, fields, order, DATE
        if spec.kind == "weight":
            value = format(Decimal(int(raw)).scaleb(-int(ai[3])), "f")
        # GS1 GS 4.14: an AI may repeat only with an identical element string.
        if ai in raw_seen:
            if raw_seen[ai] != raw:
                return False, fields, order, MALFORMED
            continue
        raw_seen[ai] = raw
        fields[ai] = value
        order.append(ai)
    return True, fields, order, None


vectors = []


def add(vid, origin, data, gs1_ok=None):
    ok, fields, order, first = reference_parse(data)
    if gs1_ok is not None and ok != gs1_ok:
        sys.exit("reference parser disagrees with GS1 on %s (%s): GS1 %s, reference %s" % (vid, origin, gs1_ok, ok))
    vectors.append({
        "id": vid,
        "origin": origin,
        "data": data,
        "ok": ok,
        "fields": fields if ok else {},
        "order": order if ok else [],
        "firstError": first,
    })


def raw(s):
    """GS1 suite raw form: leading '^' is FNC1 in first position, other '^' are GS."""
    if s.startswith("^"):
        s = s[1:]
    return s.replace("^", GS)


def src(f, line):
    return "gs1-syntax-engine:src/c-lib/%s:%d" % (f, line)


# ---- GS1 suite: ai.c test_ai_parseAIdata (expected raw output of the OK cases) ----
for line, out in [
    (1400, "^0112345678901231"),
    (1401, "^1012345"),
    (1402, "^01123456789012311012345"),
    (1403, "^31001234561012345"),
    (1404, "^1012345^11991225"),
    (1406, "^1012345(11)991225"),
    (1407, "^1012345("),
    (1451, "^99" + "A" * 90),
]:
    add("gs1-parse-%d" % line, src("ai.c", line), raw(out), True)
add("gs1-parse-1455", src("ai.c", 1455), raw("^99" + "A" * 91), False)

# ---- GS1 suite: ai.c test_ai_linters (bracketed input, converted to raw) ----
for line, data, ok in [
    (1554, "00123456789012345675", True),
    (1555, "00A23456789012345675", False),
    (1556, "10 ", False),
    (1560, "00123456789012345670", False),
    (1561, "00123456789012345675", True),
    (1672, "01950123456789033103000123", True),
    (1673, "01950123456789023103000123", False),
    (1674, "019501234567890311131313", False),
]:
    add("gs1-lint-%d" % line, src("ai.c", line), data, ok)

# ---- GS1 suite: ai.c test_ai_processAIdata (raw input) ----
for line, data, ok in [
    (1758, "^", False),
    (1759, "^891234", False),
    (1761, "^991234", True),
    (1763, "^99~ABC", False),
    (1764, "^99ABC~", False),
    (1766, "^0112345678901231", True),
    (1767, "^01A2345678901231", False),
    (1768, "^011234567890123A", False),
    (1769, "^0112345678901234", False),
    (1770, "^011234567890123", False),
    (1771, "^01123456789012312", False),
    (1773, "^0112345678901231^", True),
    (1774, "^011234567890123^", False),
    (1775, "^01123456789012345^", False),
    (1776, "^01123456789012345^991234", False),
    (1778, "^0112345678901231991234", True),
    (1779, "^0112345678901231^991234", True),
    (1864, "^99" + "A" * 90, True),
    (1871, "^99" + "A" * 91, False),
    (1900, "^" + "^".join(["99XY"] * 64), True),
]:
    add("gs1-process-%d" % line, src("ai.c", line), raw(data), ok)

# ---- GS1 suite: ai.c test_ai_validateAIs, repeated AIs (GS 4.14) ----
for line, data, ok in [
    (2106, "^400ABC", True),
    (2107, "^400ABC^400ABC", True),
    (2108, "^400ABC^99DEF^400ABC", True),
    (2109, "^99ABC^400XYZ^400XYZ", True),
    (2110, "^400ABC^400AB", False),
    (2111, "^400ABC^400ABCD", False),
    (2112, "^400ABC^400ABC^400XYZ", False),
    (2113, "^400ABC^400XYZ^400ABC", False),
    (2114, "^400ABC^400XYZ^400XYZ", False),
    (2115, "^400ABC^99DEF^400XYZ", False),
    (2116, "^99ABC^400ABC^400XYZ", False),
]:
    add("gs1-repeat-%d" % line, src("ai.c", line), raw(data), ok)

# ---- GS1 suite: lint_csum.c, wrapped in the AIs that carry a check digit ----
CSUM = [
    (137, "02345673", True), (138, "12345673", False),
    (140, "416000336108", True), (141, "416000336109", False),
    (143, "1234567890128", True), (144, "1234567890129", False),
    (146, "12345678901231", True), (147, "12345678901232", False),
    (149, "123456789012345675", True), (150, "123456789012345670", False),
    (152, " 23456789012345675", False), (153, "12345678 012345675", False), (154, "12345678901234567 ", False),
]
for line, v, ok in CSUM:
    if len(v) == 18:
        add("gs1-csum-%d-ai00" % line, src("syntax/lint_csum.c", line), "00" + v, ok)
        continue
    add("gs1-csum-%d-ai01" % line, src("syntax/lint_csum.c", line), "01" + v.rjust(14, "0"), ok)
    if len(v) == 13:
        add("gs1-csum-%d-ai410" % line, src("syntax/lint_csum.c", line), "410" + v, ok)
        add("gs1-csum-%d-ai412" % line, src("syntax/lint_csum.c", line), "412" + v, ok)

# ---- GS1 suite: lint_yymmd0.c, wrapped in AI 17 and AI 11 ----
YYMMD0 = [
    (140, "000606", True), (141, "250606", True), (142, "500606", True), (143, "750606", True),
    (144, "990606", True), (145, "200131", True),
    (147, "x00131", False), (148, "2x0131", False), (149, "20x131", False), (150, "200x31", False),
    (151, "2001x1", False), (152, "20013x", False),
    (154, "", False), (155, "1", False), (156, "11", False), (157, "111", False), (158, "1111", False),
    (159, "11111", False), (160, "1111111", False),
    (162, "200010", False), (163, "201310", False), (164, "209910", False),
    (166, "200131", True), (167, "200132", False), (168, "200229", True), (169, "200230", False),
    (170, "200331", True), (171, "200332", False), (172, "200430", True), (173, "200431", False),
    (174, "200531", True), (175, "200532", False), (176, "200630", True), (177, "200631", False),
    (178, "200731", True), (179, "200732", False), (180, "200831", True), (181, "200832", False),
    (182, "200930", True), (183, "200931", False), (184, "201031", True), (185, "201032", False),
    (186, "201130", True), (187, "201131", False), (188, "201231", True), (189, "201232", False),
    (191, "210228", True), (192, "210229", False), (193, "220228", True), (194, "220229", False),
    (195, "230228", True), (196, "230229", False), (197, "240229", True), (198, "240230", False),
    (200, "000229", True), (201, "000230", False), (202, "040229", True), (203, "040230", False),
    (204, "960229", True), (205, "960230", False),
    (207, "200600", True),
]
for line, v, ok in YYMMD0:
    for ai in ("17", "11"):
        add("gs1-yymmd0-%d-ai%s" % (line, ai), src("syntax/lint_yymmd0.c", line), ai + v, ok)

# ---- GS1 suite: lint_cset82.c, wrapped in AI 10 ----
for line, v, ok in [
    (104, "!\"%&'()*+,-/012345", True), (105, "6789:;<=>?ABCDEFGHIJ", True),
    (106, "KLMNOPQRSTUVWXYZ_abc", True), (107, "defghijklmnopqrstuvw", True), (108, "xyz", True),
    (110, "ABC ", False), (111, "AB C", False), (112, " ABC", False),
]:
    add("gs1-cset82-%d" % line, src("syntax/lint_cset82.c", line), "10" + v, ok)
# Lines 114-368 test every byte 0x01-0xFF on its own. GS1's passing characters,
# copied from the UNIT_TEST_PASS lines; every other byte is a UNIT_TEST_FAIL.
GS1_CSET82_PASS = "!\"%&'()*+,-./0123456789:;<=>?ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz"
for b in range(0x01, 0x100):
    if b == 0x1D:
        continue  # GS is the separator in a raw element string, so it cannot be AI 10 data.
    add("gs1-cset82-byte-%02x" % b, "gs1-syntax-engine:src/c-lib/syntax/lint_cset82.c:114-368", "10" + chr(b), chr(b) in GS1_CSET82_PASS)

# ---- GS1 suite: lint_csetnumeric.c, wrapped in AI 37 (N..8) ----
for line, v, ok in [
    (85, "0", True), (86, "9", True), (87, "00", True), (88, "09", True), (89, "90", True), (90, "99", True),
    (93, "/", False), (94, ":", False), (95, "a0", False), (96, "0a", False),
]:
    add("gs1-csetnumeric-%d" % line, src("syntax/lint_csetnumeric.c", line), "37" + v, ok)

GS1_COUNT = len(vectors)

# ---- Passage edge vectors (60) ----
G = GS


def gtin(payload13):
    return payload13 + check_digit(payload13)


def sscc(payload17):
    return payload17 + check_digit(payload17)


def gln(payload12):
    return payload12 + check_digit(payload12)


def wrong(code):
    """The same code with its check digit off by one."""
    return code[:-1] + str((int(code[-1]) + 1) % 10)


G1 = gtin("0950110153000")
G2 = gtin("4006381333931")
S1 = sscc("37612345000001009")
L1 = gln("761234500001")
L2 = gln("401234500002")
# Adjacent digits 0 and 5 swapped: 3a+b and a+3b differ by 10, so mod 10 cannot tell.
G_SWAP_BASE = gtin("0950510153000")
G_SWAP = G_SWAP_BASE[1] + G_SWAP_BASE[0] + G_SWAP_BASE[2:]

PASSAGE = [
    ("sscc-valid", "00" + S1),
    ("sscc-bad-check", "00" + wrong(S1)),
    ("sscc-17-digits-at-end", "00" + S1[:17]),
    ("gtin14-valid", "01" + G1),
    ("gtin14-bad-check", "01" + wrong(G1)),
    ("gtin13-zero-padded", "010" + G2[1:]),
    ("gtin-transposed-0-5-undetectable", "01" + G_SWAP),
    ("gtin-15-digits-runs-into-unknown-ai", "01" + G1 + "5"),
    ("gtin-letter", "01" + G1[:5] + "O" + G1[6:]),
    ("gln410-valid", "410" + L1),
    ("gln410-bad-check", "410" + wrong(L1)),
    ("gln412-valid", "412" + L2),
    ("gln412-bad-check", "412" + wrong(L2)),
    ("gln410-412-predefined-no-separator", "410" + L1 + "412" + L2),
    ("gln410-12-digits-at-end", "410" + L1[:12]),
    ("lot-20-chars", "10" + "ABCDEFGHIJKLMNOPQRST"),
    ("lot-21-chars", "10" + "ABCDEFGHIJKLMNOPQRSTU"),
    ("lot-empty-before-separator", "10" + G + "17261231"),
    ("lot-all-set82-specials", "10" + "!\"%&'()*+,-./:;<=>?_"),
    ("lot-lowercase", "10" + "lot-a1"),
    ("lot-hash", "10" + "LOT#1"),
    ("lot-looks-like-gtin-and-date", "10" + "01172612" + G + "21X"),
    ("serial-20-chars", "21" + "S" * 20),
    ("serial-21-chars", "21" + "S" * 21),
    ("serial-space", "21" + "SN 1"),
    ("count-8-digits", "37" + "12345678"),
    ("count-9-digits", "37" + "123456789"),
    ("count-letter", "37" + "12A"),
    ("count-leading-zeros-kept", "37" + "0005" + G + "10A"),
    ("po-30-chars", "400" + "P" * 30),
    ("po-31-chars", "400" + "P" * 31),
    ("postal-20-chars", "420" + "9" * 20),
    ("postal-21-chars", "420" + "9" * 21),
    ("weight-3100-zero", "3100" + "000000"),
    ("weight-3101-one-tenth", "3101" + "000001"),
    ("weight-3104-trailing-zeros", "3104" + "100000"),
    ("weight-3105-all-decimals", "3105" + "123456"),
    ("weight-3106-unknown", "3106" + "123456"),
    ("weight-5-digits-at-end", "3102" + "12345"),
    ("expiry-dd00-leap-feb", "17" + "280200"),
    ("expiry-dd00-common-feb", "17" + "270200"),
    ("expiry-century-previous", "17" + "770101"),
    ("expiry-century-next", "17" + "760101"),
    ("prod-date-april-31", "11" + "260431"),
    ("prod-date-month-00", "11" + "260001"),
    ("expiry-letter", "17" + "26O231"),
    ("internal-91-90-chars", "91" + "X" * 90),
    ("internal-95-empty-before-separator", "95" + G + "10A"),
    ("internal-98-91-chars", "98" + "X" * 91),
    ("repeat-lot-same", "10ABC" + G + "10ABC"),
    ("repeat-lot-different", "10ABC" + G + "10ABD"),
    ("repeat-gtin-same", "01" + G1 + "01" + G1),
    ("repeat-gtin-different", "01" + G1 + "01" + G2),
    ("repeat-expiry-same-date-different-string", "17261200" + "17261231"),
    ("unknown-ai-first", "8912"),
    ("unknown-ai-after-variable", "10A" + G + "8912"),
    ("only-separators", G + G),
    ("logistic-label-sscc-po-postal", "00" + S1 + "400PO-7" + G + "42090210"),
    ("item-label-gtin-expiry-lot-serial", "01" + G1 + "17261231" + "10LOT9" + G + "21SN1"),
    ("fixed-chain-gtin-prod-expiry-weight", "01" + G1 + "11260101" + "17270630" + "3102" + "000125"),
]
assert len(PASSAGE) == 60, len(PASSAGE)
for name, data in PASSAGE:
    add("passage-" + name, "passage", data)

ids = [v["id"] for v in vectors]
assert len(ids) == len(set(ids)), "duplicate vector id"

EXCLUDED = [
    {"origin": src("ai.c", 1409) + "-1414", "count": 5, "reason": "bracketed-input syntax with AI 235 (outside the BR-32.1 table); the device parses raw scans, not bracketed text"},
    {"origin": src("ai.c", 1416) + "-1429", "count": 14, "reason": "bracketed-input syntax errors (empty brackets, escapes, '^' in bracketed data); not reachable from a scan"},
    {"origin": src("ai.c", 1405), "count": 1, "reason": "AI 3900 outside the BR-32.1 table"},
    {"origin": src("ai.c", 1466) + "-1509", "count": 3, "reason": "encoder limits (MAX_AIS = 64, output buffer capacity), not GS1 syntax rules"},
    {"origin": src("ai.c", 1557) + "-1669 (except 1560, 1561)", "count": 91, "reason": "linters for AIs outside the BR-32.1 table (8010, 8030, 8013, 401, 7023, 7040, 8001, 8003, 8011, 4321, 426, 7030, 4307, 3910, 8007, 4326, 4324, 8008, 8026, 4300, 8112, 8110, 4309, 4330, 7252, 7258)"},
    {"origin": src("ai.c", 1756), "count": 1, "reason": "empty raw string without FNC1: framing of the encoder's raw input; a scan always has a symbology identifier or data"},
    {"origin": src("ai.c", 1757), "count": 1, "reason": "unprefixed '991234' is MISSING_FNC1 to the encoder; BR-32.1 deliberately parses an unprefixed element string when the step expects GS1"},
    {"origin": src("ai.c", 1781) + "-1846", "count": 59, "reason": "AIs outside the BR-32.1 table (2421, 8111, 8001, 7007, 8003, 7230, 235, 423)"},
    {"origin": src("ai.c", 1909) + "-1916", "count": 2, "reason": "MAX_AIS encoder limit and permitUnknownAIs mode"},
    {"origin": src("ai.c", 2117) + "-2122", "count": 6, "reason": "AI 89 is not a GS1 AI; the suite admits it only with permitUnknownAIs"},
    {"origin": src("ai.c", 2129) + "-2197", "count": 51, "reason": "AI association rules (GS1 GS 4.13 mandatory and 4.14 invalid pairs, 'req'/'ex'); BR-32.1 parses each label on its own and does not apply them"},
    {"origin": src("ai.c", 2205) + "-2215", "count": 9, "reason": "digital-signature rules for AIs 253, 255, 8003, 8030 (outside the BR-32.1 table)"},
    {"origin": src("syntax/lint_csum.c", 135), "count": 1, "reason": "empty check-digit component cannot occur in a fixed-length AI"},
    {"origin": src("syntax/lint_cset82.c", 103), "count": 1, "reason": "empty set-82 component is valid to the linter but an empty AI 10 is a length error"},
    {"origin": src("syntax/lint_cset82.c", 142), "count": 1, "reason": "byte 0x1D is GS, the separator of a raw element string"},
    {"origin": src("syntax/lint_csetnumeric.c", 84), "count": 1, "reason": "empty numeric component; an empty AI 37 is a length error"},
    {"origin": src("syntax/lint_csetnumeric.c", 91), "count": 1, "reason": "10-digit numeric component is longer than AI 37 (N..8), the only BR-32.1 variable numeric AI"},
]

json.dump({
    "description": "AC-32.1 GS1 AI parser vectors: GS1 Barcode Syntax Engine suite vectors within the BR-32.1 AI table plus 60 Passage edge vectors. Generated by gen_gs1_ai_vectors.py; do not edit by hand.",
    "gs1Suite": {"repository": GS1_REPO, "commit": GS1_COMMIT, "license": "Apache-2.0"},
    "referenceDate": REFERENCE_DATE,
    "symbologyPrefixes": ["", "]C1", "]d2", "]Q3"],
    "counts": {"gs1": GS1_COUNT, "passage": len(vectors) - GS1_COUNT, "total": len(vectors)},
    "vectors": vectors,
    "excluded": EXCLUDED,
}, sys.stdout, indent=1, ensure_ascii=True)
sys.stdout.write("\n")
