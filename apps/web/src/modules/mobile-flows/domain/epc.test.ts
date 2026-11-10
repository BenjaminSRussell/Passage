import { describe, expect, it } from "vitest";
import { classifyScan } from "./gs1";
import { checkDigitOf, decodeEpc, encodeSgtin96, epcToScan, EpcError } from "./epc";

describe("EPC decoding (GS1 TDS vectors)", () => {
  it("decodes the SGTIN-96 of the TDS example urn:epc:id:sgtin:0614141.812345.6789", () => {
    const epc = decodeEpc("3074257BF7194E4000001A85");
    expect(epc).toMatchObject({ scheme: "SGTIN-96", filter: 3, partition: 5, companyPrefix: "0614141", gtin: "80614141123458", serial: "6789" });
  });

  it("decodes the SSCC-96 of the TDS example urn:epc:id:sscc:0614141.1234567890", () => {
    const epc = decodeEpc("3174257BF4499602D2000000");
    expect(epc).toMatchObject({ scheme: "SSCC-96", filter: 3, partition: 5, companyPrefix: "0614141", sscc: "106141412345678908" });
  });

  it("round-trips an SGTIN-96 for every company prefix length", () => {
    for (const digits of [6, 7, 8, 9, 10, 11, 12]) {
      const body = "1" + "2".repeat(digits) + "3".repeat(12 - digits);
      const gtin = body + checkDigitOf(body);
      const hex = encodeSgtin96(gtin, 274877906943n, digits, 2);
      expect(decodeEpc(hex)).toMatchObject({ gtin, serial: "274877906943", filter: 2, companyPrefix: "2".repeat(digits) });
    }
  });

  it("turns a tag into the scan a GS1-128 barcode would give", () => {
    expect(epcToScan("3074257BF7194E4000001A85")).toBe("]C10180614141123458216789");
    expect(epcToScan("3174257BF4499602D2000000")).toBe("]C100106141412345678908");
    const sgtin = classifyScan(epcToScan("3074257BF7194E4000001A85"), {});
    expect(sgtin.kind).toBe("gs1");
    expect(sgtin.gs1?.ok).toBe(true);
    expect(sgtin.gs1?.fields["01"]).toBe("80614141123458");
    expect(sgtin.gs1?.fields["21"]).toBe("6789");
  });

  it("refuses what it cannot read with MOB-0118", () => {
    for (const bad of ["", "XYZ", "3074257BF7194E4000001A8", "3074257BF7194E4000001A85FF", "2074257BF7194E4000001A85"]) {
      expect(() => decodeEpc(bad), bad).toThrow(EpcError);
    }
    expect(() => encodeSgtin96("80614141123459", 1n, 7)).toThrow(/not valid/);
    expect(() => encodeSgtin96("80614141123458", 1n << 38n, 7)).toThrow(/38 bits/);
    try {
      decodeEpc("zz");
    } catch (e) {
      expect((e as EpcError).code).toBe("MOB-0118");
    }
  });

  it("computes the GS1 check digit", () => {
    expect(checkDigitOf("8061414112345")).toBe("8");
    expect(checkDigitOf("10614141234567890")).toBe("8");
  });
});
