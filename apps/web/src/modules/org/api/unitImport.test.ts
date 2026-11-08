import { afterEach, describe, expect, it, vi } from "vitest";
import {
  importOperatingUnits,
  MAX_IMPORT_ROWS,
  parseOperatingUnitCsv,
} from "./unitImport";

afterEach(() => vi.unstubAllGlobals());

describe("parseOperatingUnitCsv", () => {
  it("reads a header, quoted fields and CRLF line ends", () => {
    const { rows, problems } = parseOperatingUnitCsv(
      '﻿Type,Code,Name,Legal Entity\r\ndepartment,d1,"Ops, ""East""",usmf\r\ncost_center,C2,Plant,\r\n',
    );
    expect(problems).toEqual([]);
    expect(rows).toEqual([
      {
        type: "department",
        code: "d1",
        name: 'Ops, "East"',
        legalEntity: "usmf",
      },
      { type: "cost_center", code: "C2", name: "Plant", legalEntity: "" },
    ]);
  });

  it("allows a quoted line break and skips blank lines", () => {
    const { rows } = parseOperatingUnitCsv(
      'type,code,name\n\ndepartment,D1,"Two\nlines"\n\n',
    );
    expect(rows).toEqual([
      { type: "department", code: "D1", name: "Two\nlines" },
    ]);
  });

  it("names the missing columns", () => {
    expect(parseOperatingUnitCsv("code,title\nA,B").problems).toEqual([
      "The header has no type column.",
      "The header has no name column.",
    ]);
  });

  it("reports an empty file and a header without rows", () => {
    expect(parseOperatingUnitCsv("").problems).toEqual(["The file is empty."]);
    expect(parseOperatingUnitCsv("type,code,name\n").problems).toEqual([
      "The file has a header but no rows.",
    ]);
  });

  it("refuses more rows than the service takes", () => {
    const body = Array.from(
      { length: MAX_IMPORT_ROWS + 1 },
      (_, i) => `department,D${i},x`,
    ).join("\n");
    expect(
      parseOperatingUnitCsv(`type,code,name\n${body}`).problems[0],
    ).toContain("at most 1000");
  });

  it("fills a short row with empty cells", () => {
    expect(parseOperatingUnitCsv("type,code,name\ndepartment,D1").rows).toEqual(
      [{ type: "department", code: "D1", name: "" }],
    );
  });
});

describe("importOperatingUnits", () => {
  it("posts the rows and adds dryRun to the query", async () => {
    const fetchMock = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            dryRun: true,
            applied: false,
            created: 1,
            unchanged: 0,
            failed: 0,
            rows: [],
          }),
        ),
    );
    vi.stubGlobal("fetch", fetchMock);
    await importOperatingUnits(
      [{ type: "department", code: "D1", name: "x" }],
      true,
    );
    const [url, init] = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ];
    expect(url).toBe("/api/v1/org/operating-units:import?dryRun=true");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      rows: [{ type: "department", code: "D1", name: "x" }],
    });
  });
});
