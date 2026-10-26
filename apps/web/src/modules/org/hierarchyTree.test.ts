import { describe, expect, it } from "vitest";
import type { HierarchyNode, HierarchyVersionSummary } from "./api/hierarchy";
import { addDays, buildTree, changeMap, dayNumber, previousVersion, sliderRange, versionAsOf } from "./hierarchyTree";

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
