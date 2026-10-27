import { describe, expect, it } from "vitest";
import { formatRate } from "./validate";

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
