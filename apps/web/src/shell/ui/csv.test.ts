import { describe, expect, it } from "vitest";
import { csvCell, toCsv } from "./csv";

describe("csv", () => {
  it("quotes commas, quotes and line breaks", () => {
    expect(csvCell('Acme, "Best" Supply')).toBe('"Acme, ""Best"" Supply"');
    expect(csvCell("two\nlines")).toBe('"two\nlines"');
    expect(csvCell("plain")).toBe("plain");
  });

  it("neutralises cells that a spreadsheet would run as a formula", () => {
    expect(csvCell("=SUM(A1:A9)")).toBe("'=SUM(A1:A9)");
    expect(csvCell("+1+1")).toBe("'+1+1");
    expect(csvCell("-2+3")).toBe("'-2+3");
    expect(csvCell("@cmd")).toBe("'@cmd");
    expect(csvCell("\t=1")).toBe("'\t=1");
    expect(csvCell('=HYPERLINK("http://x","y")')).toBe(`"'=HYPERLINK(""http://x"",""y"")"`);
  });

  it("keeps plain negative amounts as numbers", () => {
    expect(csvCell("-15.00")).toBe("-15.00");
    expect(csvCell(-3)).toBe("-3");
  });

  it("writes empty for missing values and starts with a byte order mark", () => {
    const text = toCsv(["A", "B"], [[null, undefined], ["x", 1]]);
    expect(text).toBe("﻿A,B\r\n,\r\nx,1\r\n");
  });
});
