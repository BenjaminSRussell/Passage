import { describe, expect, it } from "vitest";
import { deviationBeyondLimit, formatRate } from "./validate";

describe("deviationBeyondLimit", () => {
  it("is silent up to and including 10 percent and reports the size beyond it", () => {
    expect(deviationBeyondLimit("1.0000", "1.1000")).toBeNull();
    expect(deviationBeyondLimit("1.0000", "0.9000")).toBeNull();
    expect(deviationBeyondLimit("1.0000", "1.1001")).toEqual({ percent: "10.01", direction: "above" });
    expect(deviationBeyondLimit("1", "0.5")).toEqual({ percent: "50.00", direction: "below" });
    expect(deviationBeyondLimit("1.08", "1.20")).toEqual({ percent: "11.11", direction: "above" });
    expect(deviationBeyondLimit("1", "1.10005")).toEqual({ percent: "10.01", direction: "above" });
    expect(deviationBeyondLimit("1", "1.100049")).toEqual({ percent: "10.00", direction: "above" });
    expect(deviationBeyondLimit("0", "1")).toBeNull();
  });
});

describe("formatRate", () => {
  it.each([
    ["1.0855", "1.085500"],
    ["0.64", "0.640000"],
    ["1", "1.000000"],
    ["1.0000005", "1.000001"],
    ["1.0000004999", "1.000000"],
    ["0.9999995", "1.000000"],
    ["123456789.1234567891", "123456789.123457"],
    ["-2.5000005", "-2.500001"],
    ["not a number", "not a number"],
  ])("formats %s as %s", (input, expected) => {
    expect(formatRate(input)).toBe(expected);
  });
});
