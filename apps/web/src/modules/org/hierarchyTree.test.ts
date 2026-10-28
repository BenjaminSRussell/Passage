import { describe, expect, it } from "vitest";
import type { HierarchyNode, HierarchyVersionSummary } from "./api/hierarchy";
import {
  addDays,
  addNode,
  buildTree,
  changeMap,
  dayNumber,
  hierarchyCsv,
  indentNode,
  isDescendant,
  moveNode,
  outdentNode,
  parentChoices,
  previousVersion,
  replaceNode,
  shiftNode,
  siblingIds,
  sliderRange,
  versionAsOf,
} from "./hierarchyTree";

const version = (versionNo: number, status: HierarchyVersionSummary["status"], effectiveFrom: string | null, effectiveTo: string | null) => ({
  id: `v${versionNo}`,
  hierarchyTypeId: "t1",
  versionNo,
  status,
  effectiveFrom,
  effectiveTo,
  nodeCount: 3,
});

const v1 = version(1, "superseded", "2026-01-01", "2026-05-31");
const v2 = version(2, "published", "2026-06-01", null);
const v3 = version(3, "draft", null, null);
const versions = [v3, v2, v1];

describe("versionAsOf", () => {
  it.each([
    ["empty list", [], "2026-06-01", null],
    ["gap before the first version", versions, "2025-12-31", null],
    ["first day of the first range", versions, "2026-01-01", v1],
    ["closing day of a superseded range", versions, "2026-05-31", v1],
    ["day after the superseded range", versions, "2026-06-01", v2],
    ["open range", versions, "2031-01-01", v2],
    ["only a draft", [v3], "2026-06-01", null],
    ["empty date", versions, "", null],
    ["impossible date", versions, "2026-13-40", null],
    ["overlap picks the highest version number", [v2, version(4, "published", "2026-03-01", null)], "2026-07-01", version(4, "published", "2026-03-01", null)],
  ])("%s", (_name, list, date, expected) => {
    expect(versionAsOf(list, date)).toEqual(expected);
  });
});

describe("previousVersion", () => {
  it.each([
    ["lowest version", v1, undefined],
    ["draft sees the published one", v3, v2],
    ["gap in version numbers", version(9, "draft", null, null), v3],
  ])("%s", (_name, target, expected) => {
    expect(previousVersion(versions, target)).toEqual(expected);
  });
});

const node = (orgUnitId: string, parentOrgUnitId: string | null, sortOrder: number): HierarchyNode => ({
  orgUnitId,
  kind: "operating_unit",
  parentOrgUnitId,
  sortOrder,
});

describe("buildTree", () => {
  it("returns an empty forest for no nodes", () => {
    expect(buildTree([])).toEqual([]);
  });

  it("orders siblings by sortOrder then org unit id and nests children", () => {
    const tree = buildTree([node("c", "r", 2), node("b", "r", 1), node("a", "r", 1), node("r", null, 0), node("d", "b", 0)]);
    expect(tree.map((root) => root.node.orgUnitId)).toEqual(["r"]);
    expect(tree[0].children.map((child) => child.node.orgUnitId)).toEqual(["a", "b", "c"]);
    expect(tree[0].children[1].children.map((child) => child.node.orgUnitId)).toEqual(["d"]);
  });

  it("keeps every root of a forest and treats an unknown parent as a root", () => {
    const tree = buildTree([node("r2", null, 2), node("r1", null, 1), node("orphan", "missing", 3)]);
    expect(tree.map((root) => root.node.orgUnitId)).toEqual(["r1", "r2", "orphan"]);
  });
});

describe("changeMap", () => {
  it("maps added and moved nodes and ignores removed", () => {
    const changes = changeMap({
      hierarchyTypeId: "t1",
      fromVersionNo: 2,
      toVersionNo: 3,
      added: [{ orgUnitId: "a", kind: "operating_unit", parentOrgUnitId: "r" }],
      removed: [{ orgUnitId: "x", kind: "operating_unit", parentOrgUnitId: "r" }],
      moved: [{ orgUnitId: "m", kind: "operating_unit", oldParentOrgUnitId: "p", newParentOrgUnitId: "q" }],
    });
    expect(Object.fromEntries(changes)).toEqual({ a: { kind: "added" }, m: { kind: "moved", oldParentOrgUnitId: "p" } });
  });

  it("treats wrong-typed arrays as empty", () => {
    const empty = { hierarchyTypeId: "t1", fromVersionNo: 1, toVersionNo: 2, added: null, removed: null, moved: null };
    expect(changeMap(empty as never).size).toBe(0);
  });
});

describe("date helpers", () => {
  it("round-trips ISO dates through day numbers without Date", () => {
    expect(dayNumber("1970-01-01")).toBe(0);
    expect(addDays("2026-02-28", 1)).toBe("2026-03-01");
    expect(addDays("2028-02-28", 1)).toBe("2028-02-29");
    expect(addDays("2026-12-31", 365)).toBe("2027-12-31");
    expect(dayNumber("2026-02-30")).toBeNull();
  });

  it("derives the slider range from effective dates and falls back to a year from the date", () => {
    expect(sliderRange(versions, "2026-06-01")).toEqual({ min: "2026-01-01", max: "2027-06-01" });
    expect(sliderRange([v1], "2026-06-01")).toEqual({ min: "2026-01-01", max: "2026-05-31" });
    expect(sliderRange([v3], "2026-06-01")).toEqual({ min: "2026-06-01", max: "2027-06-01" });
  });
});

const sample = [node("r", null, 0), node("a", "r", 0), node("b", "r", 1), node("c", "r", 2), node("a1", "a", 0)];
const shape = (nodes: readonly HierarchyNode[] | null) =>
  nodes === null ? null : Object.fromEntries(nodes.map((entry) => [entry.orgUnitId, `${entry.parentOrgUnitId ?? "-"}:${entry.sortOrder}`]));

describe("tree editing", () => {
  it("knows ancestors and refuses to loop on a corrupt cycle", () => {
    expect(isDescendant(sample, "r", "a1")).toBe(true);
    expect(isDescendant(sample, "a", "b")).toBe(false);
    expect(isDescendant([node("x", "y", 0), node("y", "x", 0)], "z", "x")).toBe(false);
    expect(siblingIds(sample, "r")).toEqual(["a", "b", "c"]);
  });

  it("shifts a node among its siblings and renumbers them", () => {
    expect(shape(shiftNode(sample, "a", 1))).toMatchObject({ a: "r:1", b: "r:0", c: "r:2" });
    expect(shape(shiftNode(sample, "c", -1))).toMatchObject({ a: "r:0", c: "r:1", b: "r:2" });
  });

  it.each([
    ["first sibling up", () => shiftNode(sample, "a", -1)],
    ["last sibling down", () => shiftNode(sample, "c", 1)],
    ["root up", () => shiftNode(sample, "r", -1)],
    ["first sibling indent", () => indentNode(sample, "a")],
    ["root indent", () => indentNode(sample, "r")],
    ["child of root outdent", () => outdentNode(sample, "a")],
    ["root outdent", () => outdentNode(sample, "r")],
    ["root reparent", () => moveNode(sample, "r", "a")],
    ["move into self", () => moveNode(sample, "a", "a")],
    ["move into a descendant", () => moveNode(sample, "a", "a1")],
    ["move under an unknown node", () => moveNode(sample, "a", "zzz")],
    ["move an unknown node", () => moveNode(sample, "zzz", "a")],
  ])("refuses %s", (_name, run) => {
    expect(run()).toBeNull();
  });

  it("indents under the previous sibling as its last child and outdents after the parent", () => {
    expect(shape(indentNode(sample, "b"))).toMatchObject({ b: "a:1", a1: "a:0", c: "r:1" });
    expect(shape(outdentNode(sample, "a1"))).toMatchObject({ a1: "r:1", b: "r:2", c: "r:3", a: "r:0" });
  });

  it("moves under a chosen parent at an index, clamped to the sibling count", () => {
    expect(shape(moveNode(sample, "c", "a", 0))).toMatchObject({ c: "a:0", a1: "a:1", b: "r:1" });
    expect(shape(moveNode(sample, "c", "a", 99))).toMatchObject({ c: "a:1", a1: "a:0" });
  });

  it("offers every node except itself, its parent and its descendants as a new parent", () => {
    expect(parentChoices(sample, "a")).toEqual(["b", "c"]);
    expect(parentChoices(sample, "a1")).toEqual(["r", "b", "c"]);
    expect(parentChoices(sample, "r")).toEqual([]);
  });

  it("adds a node last under a known parent, or as the only root of an empty tree", () => {
    expect(shape(addNode(sample, "n", "operating_unit", "a"))).toMatchObject({ n: "a:1" });
    expect(addNode([], "n", "legal_entity", null)).toEqual([{ orgUnitId: "n", kind: "legal_entity", parentOrgUnitId: null, sortOrder: 0 }]);
    expect(addNode([], "n", "legal_entity", "a")).toBeNull();
    expect(addNode(sample, "n", "operating_unit", null)).toBeNull();
    expect(addNode(sample, "n", "operating_unit", "zzz")).toBeNull();
    expect(addNode(sample, "b", "operating_unit", "a")).toBeNull();
  });

  it("replaces a node in place, re-pointing its children and taking the new kind", () => {
    const replaced = replaceNode(sample, "a", "n", "legal_entity");
    expect(shape(replaced)).toEqual({ r: "-:0", n: "r:0", b: "r:1", c: "r:2", a1: "n:0" });
    expect(replaced?.find((entry) => entry.orgUnitId === "n")?.kind).toBe("legal_entity");
    expect(replaceNode(sample, "zzz", "n", "operating_unit")).toBeNull();
    expect(replaceNode(sample, "a", "b", "operating_unit")).toBeNull();
  });

  it("does not mutate its input", () => {
    const before = JSON.stringify(sample);
    shiftNode(sample, "a", 1);
    indentNode(sample, "b");
    moveNode(sample, "c", "a");
    replaceNode(sample, "a", "n", "operating_unit");
    expect(JSON.stringify(sample)).toBe(before);
  });
});

describe("hierarchyCsv", () => {
  const names: Record<string, { name: string; code: string }> = {
    r: { name: "Root, Inc", code: "R" },
    a: { name: '=HYPERLINK("x")', code: "A" },
    b: { name: 'Say "hi"', code: "B" },
  };

  it("writes one row per node with level and parent code, quoting and neutralising formulas", () => {
    const csv = hierarchyCsv(buildTree([node("r", null, 0), node("a", "r", 0), node("b", "r", 1)]), (id) => names[id]);
    expect(csv.split("\r\n")).toEqual([
      "Level,Name,Code,Kind,Parent code",
      '1,"Root, Inc",R,operating_unit,',
      "2,\"'=HYPERLINK(\"\"x\"\")\",A,operating_unit,R",
      '2,"Say ""hi""",B,operating_unit,R',
    ]);
  });

  it("writes only the header for an empty tree", () => {
    expect(hierarchyCsv([], (id) => ({ name: id, code: id }))).toBe("Level,Name,Code,Kind,Parent code");
  });
});
