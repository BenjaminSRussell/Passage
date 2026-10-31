import { describe, expect, it } from "vitest";
import {
  GS,
  checkScan,
  classifyScan,
  expandYear,
  implyDecimals,
  matchScanClass,
  mod10Valid,
  parseDate,
  parseGS1,
  startsWithAI,
  toGtin14,
} from "./gs1";

// Reference year for every date assertion so the century window is stable.
const NOW = new Date("2026-01-15T00:00:00Z");

// PRD BR-32.1 example. Its GTIN check digit is wrong (should be 1), so the vector uses 09501234567891.
const SPEC_EXAMPLE = "]C1" + "01" + "09501234567891" + "10123ABC" + GS + "17261231" + "21SN0007";

function checkDigit(payload: string): string {
  let sum = 0;
  for (let i = 0; i < payload.length; i++) {
    const d = payload.charCodeAt(payload.length - 1 - i) - 48;
    sum += d * (i % 2 === 0 ? 3 : 1);
  }
  return String((10 - (sum % 10)) % 10);
}

describe("parseGS1 vectors", () => {
  const cases: { name: string; raw: string; fields: Record<string, string>; errors: string[] }[] = [
    {
      name: "spec example with symbology identifier and FNC1 between variable and fixed AIs",
      raw: SPEC_EXAMPLE,
      fields: { "01": "09501234567891", "10": "123ABC", "17": "2026-12-31", "21": "SN0007" },
      errors: [],
    },
    {
      name: "unprefixed GTIN",
      raw: "0109501234567891",
      fields: { "01": "09501234567891" },
      errors: [],
    },
    {
      name: "SSCC with valid check digit",
      raw: "00340123456789012340",
      fields: { "00": "340123456789012340" },
      errors: [],
    },
    {
      name: "SSCC with bad check digit is reported, value dropped",
      raw: "00340123456789012341",
      fields: {},
      errors: ["MOB-0102"],
    },
    {
      name: "GTIN with bad check digit",
      raw: "0100614141123453",
      fields: {},
      errors: ["MOB-0102"],
    },
    {
      name: "net weight 3102 implies two decimals",
      raw: "3102000125",
      fields: { "3102": "1.25" },
      errors: [],
    },
    {
      name: "net weight 3103 keeps leading digit after implied point",
      raw: "3103001234",
      fields: { "3103": "1.234" },
      errors: [],
    },
    {
      name: "net weight 3100 has no decimals",
      raw: "3100000500",
      fields: { "3100": "500" },
      errors: [],
    },
    {
      name: "expiry DD=00 resolves to last day of month",
      raw: "17261200",
      fields: { "17": "2026-12-31" },
      errors: [],
    },
    {
      name: "leap-day expiry in 2028",
      raw: "17280229",
      fields: { "17": "2028-02-29" },
      errors: [],
    },
    {
      name: "invalid day for February",
      raw: "17260230",
      fields: {},
      errors: ["MOB-0104"],
    },
    {
      name: "month 13 is a date error",
      raw: "17261300",
      fields: {},
      errors: ["MOB-0104"],
    },
    {
      name: "production date AI 11 with lot and serial",
      raw: "11260101" + "10LOT-9" + GS + "21SER/1",
      // (variable AIs end at GS; the GS before 21 is the separator)
      fields: { "11": "2026-01-01", "10": "LOT-9", "21": "SER/1" },
      errors: [],
    },
    {
      name: "count AI 37 numeric variable",
      raw: "3712" + GS + "10A",
      fields: { "37": "12", "10": "A" },
      errors: [],
    },
    {
      name: "PO number AI 400 and GLN AI 410",
      // 1234567890128 is GS1's own valid check-digit vector (lint_csum.c).
      raw: "400" + "0045" + GS + "410" + "1234567890128",
      fields: { "400": "0045", "410": "1234567890128" },
      errors: [],
    },
    {
      name: "GLN with bad check digit (AI 410 and 412 are N13 with check digit)",
      raw: "410" + "1234567890123",
      fields: {},
      errors: ["MOB-0102"],
    },
    {
      name: "an AI repeated with a different value is malformed",
      raw: "10ABC" + GS + "10ABD",
      fields: { "10": "ABC" },
      errors: ["MOB-0103"],
    },
    {
      name: "a symbology identifier with no element string is malformed",
      raw: "]C1",
      fields: {},
      errors: ["MOB-0103"],
    },
    {
      name: "ship-to postal code AI 420 followed by a GTIN",
      raw: "420" + "90210" + GS + "0109501234567891",
      fields: { "420": "90210", "01": "09501234567891" },
      errors: [],
    },
    {
      name: "the print service's GS1 carton label: SSCC, PO (AI 400) and ship-to postal code (AI 420)",
      raw: "]C1" + "00340123456789012340" + "400PO-7" + GS + "42090210",
      fields: { "00": "340123456789012340", "400": "PO-7", "420": "90210" },
      errors: [],
    },
    {
      name: "ship-to postal code over 20 characters is malformed",
      raw: "420" + "9".repeat(21),
      fields: {},
      errors: ["MOB-0103"],
    },
    {
      name: "variable AI over its maximum length",
      raw: "10" + "A".repeat(21),
      fields: {},
      errors: ["MOB-0103"],
    },
    {
      name: "lot with characters outside set 82",
      raw: "10AB CD",
      fields: {},
      errors: ["MOB-0103"],
    },
    {
      name: "unknown AI reports position",
      raw: "0109501234567891" + "ZZ123",
      fields: { "01": "09501234567891" },
      errors: ["MOB-0103"],
    },
  ];

  for (const c of cases) {
    it(c.name, () => {
      const got = parseGS1(c.raw, { now: NOW });
      expect(got.fields).toEqual(c.fields);
      expect(got.errors.map((e) => e.code)).toEqual(c.errors);
      expect(got.ok).toBe(c.errors.length === 0);
    });
  }

  it("reports the position of an unknown AI", () => {
    const got = parseGS1("0109501234567891ZZ", { now: NOW });
    expect(got.errors[0]).toMatchObject({ code: "MOB-0103", position: 16 });
  });

  it("accepts the FNC1 substitute configured for the warehouse", () => {
    const got = parseGS1("10ABC~17261231", { now: NOW, fnc1Substitute: "~" });
    expect(got.fields).toEqual({ "10": "ABC", "17": "2026-12-31" });
    expect(got.ok).toBe(true);
  });

  it("keeps AIs in read order, which numeric-looking object keys would not", () => {
    expect(parseGS1(SPEC_EXAMPLE, { now: NOW }).order).toEqual(["01", "10", "17", "21"]);
    expect(parseGS1("17261231" + "01" + "09501234567891", { now: NOW }).order[0]).toBe("17");
  });

  it("accepts the other symbology identifiers", () => {
    expect(parseGS1("]d20109501234567891", { now: NOW }).symbology).toBe("d2");
    expect(parseGS1("]Q30109501234567891", { now: NOW }).fields["01"]).toBe("09501234567891");
  });
});

describe("expandYear and parseDate", () => {
  it("applies the -49/+50 window around the reference year", () => {
    expect(expandYear(77, 2026)).toBe(1977);
    expect(expandYear(76, 2026)).toBe(2076);
    expect(expandYear(0, 2026)).toBe(2000);
    expect(expandYear(26, 2026)).toBe(2026);
  });

  it("returns null for impossible dates and ISO strings for valid ones", () => {
    expect(parseDate("260101", 2026)).toBe("2026-01-01");
    expect(parseDate("261032", 2026)).toBeNull();
    expect(parseDate("230229", 2026)).toBeNull();
    expect(parseDate("240229", 2026)).toBe("2024-02-29");
  });
});

describe("digit helpers", () => {
  it("implies decimal places without floats", () => {
    expect(implyDecimals("000125", 2)).toBe("1.25");
    expect(implyDecimals("000000", 2)).toBe("0.00");
    expect(implyDecimals("012345", 0)).toBe("12345");
  });

  it("verifies mod-10 with GS1 weights (table-driven)", () => {
    const cases: [string, boolean][] = [
      ["09501101020917", true],
      ["00614141123452", true],
      ["00614141123453", false],
      ["340123456789012340", true],
      ["340123456789012341", false],
      ["", false],
      ["1", false],
      ["12a4", false],
    ];
    for (const [v, want] of cases) expect(mod10Valid(v), v).toBe(want);
  });

  it("pads short GTINs to GTIN-14", () => {
    expect(toGtin14("12345670")).toBe("00000012345670");
  });
});

describe("classifyScan and scan routing (BR-32.2)", () => {
  it("treats an unprefixed GS1 string as GS1 only when the step expects GS1", () => {
    expect(classifyScan("0109501234567891", { expectGs1: true, now: NOW }).kind).toBe("gs1");
    expect(classifyScan("0109501234567891").kind).toBe("plain");
  });

  it("treats everything as plain when a symbology identifier is required but missing", () => {
    expect(classifyScan("0109501234567891", { requireSymbologyId: true, expectGs1: true }).kind).toBe("plain");
    expect(classifyScan("]C10109501234567891", { requireSymbologyId: true }).kind).toBe("gs1");
  });

  it("detects a leading AI only for known identifiers", () => {
    expect(startsWithAI("17261231")).toBe(true);
    expect(startsWithAI("LP-000123")).toBe(false);
  });

  it("accepts LP scans as SSCC or LP format and rejects a GTIN", () => {
    expect(matchScanClass("LP", classifyScan("00340123456789012340", { expectGs1: true }))).toBeNull();
    expect(matchScanClass("LP", classifyScan("LP-000123"))).toBeNull();
    expect(matchScanClass("LP", classifyScan("0109501234567891", { expectGs1: true }))).toBe("Expected LP, scanned GTIN.");
  });

  it("accepts a location and rejects an LP when a location is expected", () => {
    expect(matchScanClass("LOCATION", classifyScan("A-01-02"))).toBeNull();
    expect(matchScanClass("LOCATION", classifyScan("LP-000123"))).toBe("Expected location, scanned LP.");
  });

  it("accepts item barcodes and GS1 GTINs for GTIN steps", () => {
    expect(matchScanClass("GTIN", classifyScan("09501101020917"))).toBeNull();
    expect(matchScanClass("GTIN", classifyScan("0109501234567891", { expectGs1: true }))).toBeNull();
    expect(matchScanClass("GTIN", classifyScan("09501101020918"))).not.toBeNull();
  });
});

describe("company-internal AIs and symbology identifiers", () => {
  it("parses AI 91 to 99 as variable fields up to 90 characters", () => {
    const got = parseGS1("]C1" + "91BIN-7" + GS + "99" + "X".repeat(90), { now: NOW });
    expect(got.ok).toBe(true);
    expect(got.fields).toEqual({ "91": "BIN-7", "99": "X".repeat(90) });
    expect(parseGS1("]C1" + "92" + "X".repeat(91), { now: NOW }).errors[0]).toMatchObject({ code: "MOB-0103", ai: "92" });
  });

  it("treats a non-GS1 symbology identifier as a plain value without its prefix", () => {
    expect(classifyScan("]C0LP-000123", { expectGs1: true })).toEqual({ kind: "plain", value: "LP-000123" });
    expect(classifyScan("]E009501101020917")).toEqual({ kind: "plain", value: "09501101020917" });
    expect(matchScanClass("LP", classifyScan("]C0LP-000123"))).toBeNull();
  });
});

describe("checkScan", () => {
  it("reports a label's check-digit failure before any class mismatch", () => {
    const bad = classifyScan("]C1" + "00340123456789012341", { now: NOW });
    expect(checkScan("LOCATION", bad)).toEqual({ code: "MOB-0102", message: "Check digit invalid for AI 00." });
  });

  it("reports MOB-0101 with BR-32.2 wording for a well-formed scan of the wrong class", () => {
    expect(checkScan("LOCATION", classifyScan("]C1" + "00340123456789012340"))).toEqual({
      code: "MOB-0101",
      message: "Expected location, scanned LP.",
    });
    expect(checkScan("LP", classifyScan("]C1" + "00340123456789012340"))).toBeNull();
  });
});

describe("parser properties (deterministic random walk)", () => {
  // Linear congruential generator so failures reproduce exactly.
  function rng(seed: number) {
    let s = seed >>> 0;
    return () => {
      s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
      return s / 0x100000000;
    };
  }

  it("round-trips random valid GTIN-14 and lot values", () => {
    const rand = rng(42);
    for (let i = 0; i < 300; i++) {
      let payload = "";
      for (let j = 0; j < 13; j++) payload += String(Math.floor(rand() * 10));
      const gtin = payload + checkDigit(payload);
      const lot = Array.from({ length: 1 + Math.floor(rand() * 20) }, () => "ABCDEFGHJKLMNPQRSTUVWXYZ0123456789"[Math.floor(rand() * 34)]).join("");
      const got = parseGS1(`01${gtin}10${lot}`, { now: NOW });
      expect(got.ok, `${gtin} ${lot}`).toBe(true);
      expect(got.fields).toEqual({ "01": gtin, "10": lot });
    }
  });

  it("never throws on arbitrary input", () => {
    const rand = rng(7);
    const alphabet = "0123456789]CdQ1017ABZ" + GS;
    for (let i = 0; i < 500; i++) {
      const len = Math.floor(rand() * 40);
      let s = "";
      for (let j = 0; j < len; j++) s += alphabet[Math.floor(rand() * alphabet.length)];
      expect(() => parseGS1(s, { now: NOW })).not.toThrow();
    }
  });
});
