import { canReconcile, formatKey, formatOffset, localToUtc, money, overrunTone, parseRecords, splitList, type ControlTotal } from "./domain";

describe("overrunTone", () => {
  const cases: [number, number, string][] = [
    [100, 100, "ok"],
    [100, 109, "ok"],
    [100, 110, "amber"],
    [100, 124, "amber"],
    [100, 125, "red"],
    [0, 999, "ok"],
  ];
  it.each(cases)("planned %i actual %i is %s", (planned, actual, want) => {
    expect(overrunTone(planned, actual)).toBe(want);
  });
});

describe("formatOffset", () => {
  const cases: [number, string][] = [
    [0, "T-0"],
    [-20160, "T-14d"],
    [-360, "T-6h"],
    [1800, "T+30h"],
    [2880, "T+2d"],
    [90, "T+90m"],
  ];
  it.each(cases)("%i renders %s", (minutes, want) => {
    expect(formatOffset(minutes)).toBe(want);
  });
});

describe("canReconcile", () => {
  const total = (status: ControlTotal["status"]): ControlTotal => ({
    name: "n",
    key: {},
    source: "1.000000",
    target: "1.000000",
    delta: "0.000000",
    status,
  });

  it("allows reconciliation only when no total is MISMATCH", () => {
    expect(canReconcile([total("MATCH"), total("WITHIN_TOLERANCE")])).toBe(true);
    expect(canReconcile([total("MATCH"), total("MISMATCH")])).toBe(false);
    expect(canReconcile([])).toBe(true);
  });
});

describe("formatKey", () => {
  it("joins key parts in insertion order", () => {
    expect(formatKey({ company: "USMF", currency: "USD" })).toBe("company=USMF, currency=USD");
  });
});

describe("parseRecords", () => {
  it("reads one open item per line, skips a header and keeps amounts as strings", () => {
    const { records, errors } = parseRecords("legacyId,party,account,currency,amount\nOI-1, V001, 2100, usd, 1250.10\n\nOI-2;C9;1200;EUR;-3");
    expect(errors).toEqual([]);
    expect(records).toEqual([
      { legacyId: "OI-1", party: "V001", account: "2100", currency: "USD", amount: "1250.10" },
      { legacyId: "OI-2", party: "C9", account: "1200", currency: "EUR", amount: "-3" },
    ]);
  });

  it("reports the line of each malformed record", () => {
    const { records, errors } = parseRecords("OI-1,V1,2100,USD\n,V1,2100,USD,1\nOI-3,V1,2100,USD,1.2345678\nOI-4,V1,2100,USD,1e3");
    expect(records).toEqual([]);
    expect(errors).toEqual([
      "Line 1: expected 5 values (legacyId, party, account, currency, amount), found 4.",
      "Line 2: the legacy id is blank.",
      'Line 3: amount "1.2345678" is not a decimal such as 1250.00.',
      'Line 4: amount "1e3" is not a decimal such as 1250.00.',
    ]);
  });
});

describe("form helpers", () => {
  it("reads datetime-local values as UTC", () => {
    expect(localToUtc("2026-12-31T18:00")).toBe("2026-12-31T18:00:00Z");
    expect(localToUtc("2026-12-31")).toBeNull();
    expect(localToUtc("")).toBeNull();
  });

  it("splits lists and drops repeats", () => {
    expect(splitList("V001, V002\nV001 ;C1")).toEqual(["V001", "V002", "C1"]);
    expect(splitList("  ")).toEqual([]);
  });

  it("trims six-place decimals to at least two places without floats", () => {
    expect(money("125000.000000")).toBe("125000.00");
    expect(money("-1.250000")).toBe("-1.25");
    expect(money("0.123456")).toBe("0.123456");
    expect(money("7")).toBe("7.00");
  });
});
