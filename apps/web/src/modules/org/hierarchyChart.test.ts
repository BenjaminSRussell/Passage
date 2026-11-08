import { describe, expect, it } from "vitest";
import type { HierarchyNode } from "./api/hierarchy";
import { chartSize, escapeXml, hierarchyChartSvg } from "./hierarchyChart";
import { buildTree } from "./hierarchyTree";

const nodes = [
  { orgUnitId: "le", kind: "legal_entity" as const, parentOrgUnitId: null, sortOrder: 0 },
  { orgUnitId: "a", kind: "operating_unit" as const, parentOrgUnitId: "le", sortOrder: 1 },
  { orgUnitId: "b", kind: "operating_unit" as const, parentOrgUnitId: "le", sortOrder: 2 },
];
const header = { typeCode: "REP", typeName: "Reporting <main>", purposes: ["reporting"], asOf: "2026-06-01" };
const footer = { versionNo: 2, printedAt: "2026-11-04T10:00:00Z" };
const parse = (svg: string) => new DOMParser().parseFromString(svg, "image/svg+xml");
const texts = (svg: string) => Array.from(parse(svg).querySelectorAll("text"), (text) => text.textContent);

describe("hierarchyChartSvg", () => {
  const svg = hierarchyChartSvg(
    buildTree(nodes),
    (id) => ({ code: id.toUpperCase(), name: id === "a" ? "Plant & Co" : `Unit ${id}`, manager: id === "a" ? "assigned" : id === "b" ? null : undefined }),
    header,
    footer,
  );

  it("carries the header, a box per node with code, name and manager, and the footer", () => {
    expect(texts(svg)).toEqual([
      "REP · Reporting <main>",
      "Purpose: reporting    As of: 2026-06-01",
      "LE",
      "Unit le",
      "A",
      "Plant & Co",
      "Manager: assigned",
      "B",
      "Unit b",
      "Manager: none",
      "Version 2    Printed 2026-11-04T10:00:00Z",
    ]);
    expect(svg.match(/<path /g)).toHaveLength(2);
  });

  it("centres a parent over its children and joins them with connectors", () => {
    const boxes = Array.from(parse(svg).querySelectorAll("svg > svg"), (box) => [Number(box.getAttribute("x")), Number(box.getAttribute("y"))]);
    const [root, a, b] = boxes;
    expect(root[0]).toBe((a[0] + b[0]) / 2);
    expect(a[1]).toBe(b[1]);
    expect(a[1]).toBeGreaterThan(root[1]);
    expect(b[0] - a[0]).toBe(184);
  });

  it("is well-formed XML an assistive technology can name and describe", () => {
    const root = parse(svg).documentElement;
    expect(root.querySelector("parsererror")).toBeNull();
    expect(root.getAttribute("role")).toBe("img");
    expect(root.querySelector(`#${root.getAttribute("aria-labelledby")}`)?.textContent).toBe("Organization chart, REP Reporting <main>, as of 2026-06-01");
    expect(root.querySelector(`#${root.getAttribute("aria-describedby")}`)?.textContent).toBe(
      "3 nodes, 2 levels. Purpose: reporting. Version 2, printed 2026-11-04T10:00:00Z.",
    );
  });

  it("is the same for the same input, since the print time is passed in", () => {
    expect(hierarchyChartSvg(buildTree(nodes), (id) => ({ code: id, name: id }), header, footer)).toBe(
      hierarchyChartSvg(buildTree(nodes), (id) => ({ code: id, name: id }), header, footer),
    );
  });

  it("draws an empty hierarchy as header and footer only", () => {
    const empty = hierarchyChartSvg([], (id) => ({ code: id, name: id }), header, footer);
    expect(parse(empty).querySelectorAll("svg > svg")).toHaveLength(0);
    expect(texts(empty)).toContain("Version 2    Printed 2026-11-04T10:00:00Z");
  });
});

describe("hostile names", () => {
  const hostile = [
    `<script>alert(1)</script>`,
    `"/><image href="https://evil.example/x.png" onload="alert(1)`,
    `]]><foreignObject><img src=x onerror=alert(1)></foreignObject>`,
    `Tab\there, bell\u0007, nul\u0000, lone \uD800 surrogate, ￾`,
    `<!-- comment --> <?xml-stylesheet href="x"?> &amp; &#x3C;`,
  ];
  const svg = hierarchyChartSvg(
    buildTree(hostile.map((_, index) => ({ orgUnitId: `n${index}`, kind: "operating_unit" as const, parentOrgUnitId: null, sortOrder: index }))),
    (id) => ({ code: hostile[Number(id.slice(1))], name: hostile[Number(id.slice(1))], manager: hostile[Number(id.slice(1))] }),
    { typeCode: hostile[0], typeName: hostile[2], purposes: [hostile[1]], asOf: hostile[4] },
    { versionNo: 1, printedAt: hostile[3] },
  );
  const doc = parse(svg);

  it("stays well-formed and adds no element, attribute or link from the data", () => {
    expect(doc.querySelector("parsererror")).toBeNull();
    expect(new Set(Array.from(doc.querySelectorAll("*"), (element) => element.tagName))).toEqual(new Set(["svg", "title", "desc", "rect", "text"]));
    const attributes = Array.from(doc.querySelectorAll("*"), (element) => element.getAttributeNames()).flat();
    expect(attributes.filter((name) => name.startsWith("on") || name.includes("href"))).toEqual([]);
    expect(svg).not.toContain("<script");
    expect(svg).not.toContain("<!--");
    expect(svg).not.toContain("<?xml-stylesheet");
  });

  it("keeps the names as text, minus the characters XML cannot carry", () => {
    expect(texts(svg)[0]).toBe(`${hostile[0]} · ${hostile[2]}`);
    expect(texts(svg)[1]).toBe(`Purpose: ${hostile[1]}    As of: ${hostile[4]}`);
    expect(doc.querySelector("title")?.textContent).toContain(hostile[0]);
    expect(texts(svg).at(-1)).toBe("Version 1    Printed Tab\there, bell, nul, lone  surrogate, ");
  });

  it("escapes every markup character", () => {
    expect(escapeXml(`<a href="x">&'`)).toBe("&lt;a href=&quot;x&quot;&gt;&amp;&apos;");
    expect(escapeXml("]]>")).toBe("]]&gt;");
  });
});

describe("clipping", () => {
  it("shortens long codes and names without splitting a character", () => {
    const svg = hierarchyChartSvg(
      buildTree([{ orgUnitId: "x", kind: "operating_unit", parentOrgUnitId: null, sortOrder: 0 }]),
      () => ({ code: "C".repeat(40), name: "😀".repeat(30), manager: "M".repeat(40) }),
      header,
      footer,
    );
    const [code, name, manager] = texts(svg).slice(2, 5);
    expect(code).toBe(`${"C".repeat(21)}…`);
    expect(name).toBe(`${"😀".repeat(25)}…`);
    expect(manager).toBe(`Manager: ${"M".repeat(23)}…`);
  });
});

describe("scale (PRD: up to 50,000 nodes, depth 15)", () => {
  const big = (count: number, fanOut: number) => {
    const list: HierarchyNode[] = [{ orgUnitId: "n0", kind: "legal_entity", parentOrgUnitId: null, sortOrder: 0 }];
    for (let index = 1; index < count; index += 1) {
      list.push({ orgUnitId: `n${index}`, kind: "operating_unit", parentOrgUnitId: `n${Math.floor((index - 1) / fanOut)}`, sortOrder: index });
    }
    return list;
  };

  it("lays out 50,000 nodes in linear time with a size a browser can address", () => {
    const flat = buildTree(big(50_000, 50_000));
    const started = performance.now();
    const svg = hierarchyChartSvg(flat, (id) => ({ code: id, name: id, manager: null }), header, footer);
    expect(performance.now() - started).toBeLessThan(5_000);
    const { width, height } = chartSize(flat);
    expect(svg.startsWith(`<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}"`)).toBe(true);
    expect(width).toBe(49_999 * 184 - 16 + 48);
    expect(width).toBeLessThan(2 ** 24);
    expect(height).toBe(24 + 64 + 2 * 94 - 36 + 36 + 24);
  });

  it("stacks depth 15 into fifteen rows", () => {
    const chain = buildTree(big(15, 1));
    expect(chartSize(chain)).toEqual({ width: 480, height: 24 + 64 + 15 * 94 - 36 + 36 + 24 });
  });
});
