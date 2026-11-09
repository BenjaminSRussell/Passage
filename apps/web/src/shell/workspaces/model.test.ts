import { describe, expect, it } from "vitest";
import type { PassageModule } from "../../modules/registry";
import { recordedErrors } from "../telemetry";
import {
  effectivePrefs,
  groupByArea,
  loadWorkspaceConfig,
  matchesQuery,
  OTHER_AREA_ID,
  resolveNavEntries,
  resolveWorkspaces,
  type NavEntry,
  type ResolvedWorkspace,
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
const { areas } = loadWorkspaceConfig();

describe("resolveNavEntries", () => {
  it("applies the task label and decision, and keeps unclassified entries reachable under Other", () => {
    const [audit, sequences, unknown] = resolveNavEntries(modules, areas);
    expect(audit).toMatchObject({ label: "Audit log", originalLabel: "Audit", decision: "keep", area: "administration", areaLabel: "Administration" });
    expect(sequences).toMatchObject({ label: "Sequences", decision: "advanced" });
    expect(unknown).toMatchObject({ label: "Brand new screen", decision: "keep", area: OTHER_AREA_ID, areaLabel: "Other" });
  });

  it("marks entries that need a duty as gated", () => {
    const [entry] = resolveNavEntries([{ ...modules[0], nav: [{ label: "Audit", to: "/admin/audit", duty: "audit.read" }] }], areas);
    expect(entry.gated).toBe(true);
  });
});

describe("matchesQuery", () => {
  const [audit] = resolveNavEntries(modules, areas);

  it("matches the task label, the original screen name, the module and the business area", () => {
    for (const query of ["audit log", "Audit", "admin", "administration", "  "]) {
      expect(matchesQuery(audit, query), query).toBe(true);
    }
    expect(matchesQuery(audit, "finance")).toBe(false);
  });
});

describe("groupByArea", () => {
  it("splits kept entries from advanced ones and drops empty areas", () => {
    const groups = groupByArea(resolveNavEntries(modules, areas), areas);
    expect(groups.map((g) => g.area.id)).toEqual(["administration", OTHER_AREA_ID]);
    expect(groups[0].main.map((e) => e.to)).toEqual(["/admin/audit"]);
    expect(groups[0].advanced.map((e) => e.to)).toEqual(["/admin/sequences"]);
  });
});

describe("loadWorkspaceConfig tenant override", () => {
  const replacement = { id: "buyer", label: "Purchasing desk", persona: "Buyer", description: "Custom.", entries: ["/purchasing/purchase-orders"] };
  const extra = { id: "custom", label: "Custom", persona: "Custom role", description: "Added by a tenant.", entries: ["/admin/audit"] };
  const shipped = loadWorkspaceConfig();

  function rejected(override: unknown) {
    const before = recordedErrors().length;
    expect(loadWorkspaceConfig(override)).toEqual(shipped);
    expect(recordedErrors().length).toBe(before + 1);
  }

  it("returns the shipped definitions without an override", () => {
    expect(shipped.workspaces).toHaveLength(10);
  });

  it("replaces a workspace by id, adds new ones and hides others", () => {
    const merged = loadWorkspaceConfig({ workspaces: [replacement, extra], hide: ["ap-clerk"] });
    const ids = merged.workspaces.map((w) => w.id);
    expect(ids).not.toContain("ap-clerk");
    expect(ids).toContain("custom");
    expect(merged.workspaces.find((w) => w.id === "buyer")).toMatchObject({ label: "Purchasing desk", entries: ["/purchasing/purchase-orders"] });
    expect(merged.workspaces).toHaveLength(10);
  });

  it("applies hide to areas as well as workspaces", () => {
    expect(loadWorkspaceConfig({ hide: ["people"] }).areas.map((a) => a.id)).not.toContain("people");
  });

  it("keeps the shipped config and records the error for a malformed override", () => {
    rejected({ workspaces: [{ id: "x" }] });
  });

  it("rejects misspelt keys and duplicate ids", () => {
    rejected({ workspace: [extra] });
    rejected({ workspaces: [{ ...extra, entrys: [] }] });
    rejected({ workspaces: [extra, extra] });
    rejected({ areas: [shipped.areas[0], shipped.areas[0]] });
  });

  it("resolves an overridden workspace against the visible entries", () => {
    const merged = loadWorkspaceConfig({ workspaces: [extra] });
    const resolved = resolveWorkspaces(merged, resolveNavEntries(modules, areas));
    expect(resolved.map((w) => w.id)).toEqual(expect.arrayContaining(["custom", "tenant-admin"]));
    expect(resolved.find((w) => w.id === "custom")?.entries.map((e) => e.to)).toEqual(["/admin/audit"]);
  });
});

describe("effectivePrefs", () => {
  function workspace(id: string, gatedEntries: number): ResolvedWorkspace {
    const entries = Array.from({ length: gatedEntries }, (_, i) => ({ to: `/${id}/${i}`, gated: true }) as NavEntry);
    return { id, label: id, persona: id, description: "", entries };
  }
  const available = [workspace("a", 1), workspace("b", 3), workspace("c", 3)];

  it("holds every workspace and starts in the one with the most gated entries the user can see", () => {
    const { held, active } = effectivePrefs({}, available);
    expect(held.map((w) => w.id)).toEqual(["a", "b", "c"]);
    expect(active?.id).toBe("b");
  });

  it("holds everything except the dropped ones, so workspaces gained later still appear", () => {
    const { held, active } = effectivePrefs({ dropped: ["a", "gone"], active: "c" }, available);
    expect(held.map((w) => w.id)).toEqual(["b", "c"]);
    expect(active?.id).toBe("c");
  });

  it("falls back to the broadest held workspace when the chosen one is dropped, and to all when all are dropped", () => {
    expect(effectivePrefs({ dropped: ["b"], active: "b" }, available).active?.id).toBe("c");
    expect(effectivePrefs({ dropped: ["a", "b", "c"] }, available).held).toHaveLength(3);
    expect(effectivePrefs({}, []).active).toBeUndefined();
  });
});
