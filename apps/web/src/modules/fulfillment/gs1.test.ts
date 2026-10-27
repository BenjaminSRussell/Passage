import { describe, expect, it } from "vitest";
import { FNC1, Gs1ParseError, gs1CheckDigit, hasValidCheckDigit, parseGs1 } from "./gs1";

describe("parseGs1", () => {
  it("AC-28.9: bracketed scan fills GTIN, batch and quantity", () => {
    const scan = parseGs1("(01)00614141000012(10)LOT7(37)5");
    expect(scan).toEqual({ gtin: "00614141000012", batch: "LOT7", qty: 5 });
  });

  it("parses the raw FNC1-terminated form", () => {
    const scan = parseGs1(`010061414100001210LOT7${FNC1}375`);
    expect(scan).toEqual({ gtin: "00614141000012", batch: "LOT7", qty: 5 });
  });

  it("parses an SSCC and an expiry", () => {
    const sscc = "006141410123456789";
    expect(parseGs1(`(00)${sscc}(17)271231`)).toEqual({ sscc, expiry: "271231" });
  });

  it("rejects a GTIN with a wrong check digit (FUL-0017)", () => {
    expect(() => parseGs1("(01)00614141000013")).toThrow(Gs1ParseError);
    try {
      parseGs1("(01)00614141000013");
    } catch (e) {
      expect((e as Gs1ParseError).code).toBe("FUL-0017");
    }
  });

  it("rejects unknown application identifiers and wrong lengths", () => {
    expect(() => parseGs1("(99)abc")).toThrow(Gs1ParseError);
    expect(() => parseGs1("(17)2712")).toThrow(/AI 17 must be 6 characters/);
    expect(() => parseGs1("")).toThrow(Gs1ParseError);
  });

  it("rejects non-numeric quantities", () => {
    expect(() => parseGs1("(37)x")).toThrow(/must be numeric/);
  });

  it("round-trips every subset of AIs in both forms", () => {
    const parts: [string, string][] = [
      ["01", "00614141000012"],
      ["00", "006141410123456789"],
      ["10", "LOT7"],
      ["21", "SN-42"],
      ["17", "271231"],
      ["37", "5"],
    ];
    for (let mask = 1; mask < 1 << parts.length; mask++) {
      const chosen = parts.filter((_, i) => (mask & (1 << i)) !== 0);
      const bracketed = chosen.map(([ai, v]) => `(${ai})${v}`).join("");
      const raw = chosen
        .map(([ai, v], i) => {
          const variable = ai === "10" || ai === "21" || ai === "37";
          return variable && i < chosen.length - 1 ? `${ai}${v}${FNC1}` : `${ai}${v}`;
        })
        .join("");
      const fromBracket = parseGs1(bracketed);
      const fromRawForm = parseGs1(raw);
      expect(fromRawForm).toEqual(fromBracket);
      expect(Object.keys(fromBracket).length).toBeGreaterThan(0);
    }
  });
});

describe("GS1 check digit", () => {
  it("matches the GTIN-14 and SSCC examples", () => {
    expect(gs1CheckDigit("0061414100001")).toBe(2);
    expect(hasValidCheckDigit("006141410123456789")).toBe(true);
    expect(hasValidCheckDigit("006141410123456780")).toBe(false);
  });
});
