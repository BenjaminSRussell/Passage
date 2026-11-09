import { describe, expect, it } from "vitest";
import type { PassageModule } from "../../modules/registry";
import {
  effectivePrefs,
  groupByArea,
  loadWorkspaceConfig,
  OTHER_AREA_ID,
  resolveNavEntries,
  resolveWorkspaces,
} from "./model";

const modules: PassageModule[] = [
  {
    id: "admin",
    title: "Admin",
    routes: [],
    nav: [
      { label: "Audit", to: "/admin/audit" },
      { label: "Sequences", to: "/admin/sequences" },
      { label: "Brand new screen", to: "/admin/brand-new" },
    ],
  },
];

describe("resolveNavEntries", () => {
  it("applies the task label and decision, and keeps unclassified entries reachable under Other", () => {
    const [audit, sequences, unknown] = resolveNavEntries(modules);
    expect(audit).toMatchObject({ label: "Audit log", originalLabel: "Audit", decision: "keep", area: "administration" });
    expect(sequences).toMatchObject({ label: "Sequences", decision: "advanced" });
    expect(unknown).toMatchObject({ label: "Brand new screen", decision: "keep", area: OTHER_AREA_ID });
  });
});

describe("groupByArea", () => {
  it("splits kept entries from advanced ones and drops empty areas", () => {
    const groups = groupByArea(resolveNavEntries(modules), loadWorkspaceConfig().areas);
    expect(groups.map((g) => g.area.id)).toEqual(["administration", OTHER_AREA_ID]);
    expect(groups[0].main.map((e) => e.to)).toEqual(["/admin/audit"]);
    expect(groups[0].advanced.map((e) => e.to)).toEqual(["/admin/sequences"]);
  });
});

describe("loadWorkspaceConfig tenant override", () => {
  const replacement = { id: "buyer", label: "Purchasing desk", persona: "Buyer", description: "Custom.", entries: ["/purchasing/purchase-orders"] };
  const extra = { id: "custom", label: "Custom", persona: "Custom role", description: "Added by a tenant.", entries: ["/admin/audit"] };

  it("returns the shipped definitions without an override", () => {
    expect(loadWorkspaceConfig().workspaces).toHaveLength(10);
  });

  it("replaces a workspace by id, adds new ones and hides others", () => {
    const merged = loadWorkspaceConfig({ workspaces: [replacement, extra], hide: ["ap-clerk"] });
    const ids = merged.workspaces.map((w) => w.id);
    expect(ids).not.toContain("ap-clerk");
    expect(ids).toContain("custom");
    expect(merged.workspaces.find((w) => w.id === "buyer")).toMatchObject({ label: "Purchasing desk", entries: ["/purchasing/purchase-orders"] });
    expect(merged.workspaces).toHaveLength(10);
  });

  it("rejects a malformed override", () => {
    expect(() => loadWorkspaceConfig({ workspaces: [{ id: "x" }] })).toThrow();
  });

  it("resolves an overridden workspace against the visible entries", () => {
    const merged = loadWorkspaceConfig({ workspaces: [extra] });
    const resolved = resolveWorkspaces(merged, resolveNavEntries(modules));
    expect(resolved.map((w) => w.id)).toEqual(expect.arrayContaining(["custom", "tenant-admin"]));
    expect(resolved.find((w) => w.id === "custom")?.entries.map((e) => e.to)).toEqual(["/admin/audit"]);
  });
});

describe("effectivePrefs", () => {
  const available = ["a", "b", "c"].map((id) => ({ id, label: id, persona: id, description: "", entries: [] }));

  it("holds and activates the first available workspace by default", () => {
    const { held, active } = effectivePrefs({}, available);
    expect(held.map((w) => w.id)).toEqual(["a", "b", "c"]);
    expect(active?.id).toBe("a");
  });

  it("honours held and active choices that are still available", () => {
    const { held, active } = effectivePrefs({ held: ["b", "c", "gone"], active: "c" }, available);
    expect(held.map((w) => w.id)).toEqual(["b", "c"]);
    expect(active?.id).toBe("c");
  });

  it("falls back to the first held workspace when the active one is no longer held, and to all when none is held", () => {
    expect(effectivePrefs({ held: ["b"], active: "a" }, available).active?.id).toBe("b");
    expect(effectivePrefs({ held: ["gone"] }, available).held).toHaveLength(3);
    expect(effectivePrefs({}, []).active).toBeUndefined();
  });
});
