import { describe, expect, it } from "vitest";
import { loadModules } from "../../modules/registry";
import { visibleModules } from "../dutyGate";
import {
  auditByTo,
  groupByArea,
  loadWorkspaceConfig,
  matchesQuery,
  MAX_DEFAULT_ENTRIES,
  navAudit,
  resolveNavEntries,
  resolveWorkspaces,
  TOP_TASK_COUNT,
} from "./model";

const modules = loadModules();
const moduleEntries = modules.flatMap((m) => m.nav.map((n) => ({ module: m.id, ...n })));
const config = loadWorkspaceConfig();
const areaIds = new Set(config.areas.map((a) => a.id));
const allDuties = modules.flatMap((m) => m.nav.flatMap((n) => (n.duty === undefined ? [] : [n.duty])));
const bannedInUserText = /\b(FR|BR|AC|NFR)-\d|\b(wave|slice)\s*\d/i;

describe("navigation audit", () => {
  it("classifies every module nav entry, so a new entry fails until it is audited", () => {
    const unclassified = moduleEntries.filter((e) => !auditByTo.has(e.to)).map((e) => `${e.module} ${e.to}`);
    expect(unclassified, "add each entry to src/shell/workspaces/navAudit.json with keep, merge or advanced").toEqual([]);
  });

  it("holds no stale or duplicate rows and records the owning module and current label", () => {
    const live = new Map(moduleEntries.map((e) => [e.to, e]));
    expect(navAudit.filter((r) => !live.has(r.to)).map((r) => r.to)).toEqual([]);
    expect(new Set(navAudit.map((r) => r.to)).size).toBe(navAudit.length);
    expect(navAudit.filter((r) => live.get(r.to)?.module !== r.module || live.get(r.to)?.label !== r.label).map((r) => r.to)).toEqual([]);
    expect(navAudit).toHaveLength(moduleEntries.length);
  });

  it("assigns every row to one of the business areas", () => {
    expect(config.areas).toHaveLength(8);
    expect(navAudit.filter((r) => !areaIds.has(r.area)).map((r) => r.to)).toEqual([]);
  });

  it("points every merge at a kept or advanced entry, never at itself or another merge", () => {
    const merges = navAudit.filter((r) => r.decision === "merge");
    expect(merges.length).toBeGreaterThan(0);
    for (const row of merges) {
      const target = row.mergeInto === undefined ? undefined : auditByTo.get(row.mergeInto);
      expect(target, row.to).toBeDefined();
      expect(target?.decision, row.to).not.toBe("merge");
      expect(row.mergeInto, row.to).not.toBe(row.to);
    }
    expect(navAudit.filter((r) => r.decision !== "merge" && r.mergeInto !== undefined)).toEqual([]);
  });

  it("uses plain task language in every label", () => {
    const labels = [
      ...navAudit.flatMap((r) => (r.taskLabel === undefined ? [] : [r.taskLabel])),
      ...config.workspaces.flatMap((w) => [w.label, w.persona, w.description]),
      ...config.areas.flatMap((a) => [a.label, a.description]),
    ];
    expect(labels.filter((l) => bannedInUserText.test(l))).toEqual([]);
  });
});

describe("persona workspaces", () => {
  it("defines the ten target personas", () => {
    expect(config.workspaces.map((w) => w.persona)).toEqual([
      "AP clerk",
      "AR and collections clerk",
      "Buyer",
      "Receiving and warehouse worker",
      "Sales rep or order desk",
      "Production planner",
      "Shop-floor supervisor",
      "Accountant or controller",
      "HR or payroll admin",
      "Tenant admin",
    ]);
  });

  it("curates 10 to 20 kept, task-labelled, unique entries per workspace", () => {
    for (const w of config.workspaces) {
      expect(w.entries.length, w.id).toBeGreaterThanOrEqual(10);
      expect(w.entries.length, w.id).toBeLessThanOrEqual(20);
      expect(new Set(w.entries).size, w.id).toBe(w.entries.length);
      const rows = w.entries.map((to) => auditByTo.get(to));
      expect(rows.every((r) => r?.decision === "keep"), w.id).toBe(true);
      const labels = rows.map((r) => r?.taskLabel);
      expect(labels.every((l) => l !== undefined && l !== ""), w.id).toBe(true);
      expect(new Set(labels).size, w.id).toBe(labels.length);
    }
  });

  it("shows each persona at most 25 entries by default, with every entry still reachable through All features", () => {
    const visible = visibleModules(modules, allDuties);
    const entries = resolveNavEntries(visible, config.areas);
    for (const w of resolveWorkspaces(config, entries)) {
      expect(w.entries.length, w.id).toBeLessThanOrEqual(MAX_DEFAULT_ENTRIES);
      expect(w.entries.length, w.id).toBeGreaterThanOrEqual(TOP_TASK_COUNT);
    }
    const reachable = groupByArea(entries, config.areas).flatMap((g) => [...g.main, ...g.advanced]);
    expect(reachable.map((e) => e.to).sort()).toEqual(entries.map((e) => e.to).sort());
    expect(entries).toHaveLength(moduleEntries.length);
  });

  it("applies duty gating inside every workspace", () => {
    const withoutDuties = resolveNavEntries(visibleModules(modules, []), config.areas);
    const ungated = new Set(moduleEntries.filter((e) => e.duty === undefined).map((e) => e.to));
    for (const w of resolveWorkspaces(config, withoutDuties)) {
      expect(w.entries.filter((e) => !ungated.has(e.to)).map((e) => e.to), w.id).toEqual([]);
    }
    const gatedInWorkspace = config.workspaces.flatMap((w) => w.entries).filter((to) => !ungated.has(to));
    expect(gatedInWorkspace.length).toBeGreaterThan(0);
  });

  it("finds each top task by its task label and by its original screen name", () => {
    const entries = resolveNavEntries(visibleModules(modules, allDuties), config.areas);
    for (const w of resolveWorkspaces(config, entries)) {
      for (const e of w.entries.slice(0, TOP_TASK_COUNT)) {
        expect(matchesQuery(e, e.label), e.to).toBe(true);
        expect(matchesQuery(e, e.originalLabel), e.to).toBe(true);
      }
    }
  });
});
