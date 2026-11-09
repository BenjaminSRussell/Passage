import { z } from "zod";
import type { PassageModule } from "../../modules/registry";
import { recordError } from "../telemetry";
import auditData from "./navAudit.json";
import baseConfigData from "./workspaces.json";

export const decisions = ["keep", "merge", "advanced"] as const;
export type Decision = (typeof decisions)[number];

export const ALL_FEATURES_PATH = "/features";
export const OTHER_AREA_ID = "other";
export const TOP_TASK_COUNT = 5;
export const MAX_DEFAULT_ENTRIES = 25;
export const HOME_WORKSPACE_LIMIT = 4;

const areaSchema = z.strictObject({ id: z.string().min(1), label: z.string().min(1), description: z.string() });
const workspaceSchema = z.strictObject({
  id: z.string().min(1),
  label: z.string().min(1),
  persona: z.string().min(1),
  description: z.string(),
  entries: z.array(z.string().startsWith("/")),
});
const uniqueIds = (items: { id: string }[]) => new Set(items.map((i) => i.id)).size === items.length;
const uniqueIdsMessage = "ids must be unique";
const configSchema = z.strictObject({
  areas: z.array(areaSchema).refine(uniqueIds, uniqueIdsMessage),
  workspaces: z.array(workspaceSchema).refine(uniqueIds, uniqueIdsMessage),
});
const overrideSchema = z.strictObject({
  areas: z.array(areaSchema).refine(uniqueIds, uniqueIdsMessage).optional(),
  workspaces: z.array(workspaceSchema).refine(uniqueIds, uniqueIdsMessage).optional(),
  hide: z.array(z.string()).optional(),
});
const auditSchema = z.strictObject({
  entries: z.array(
    z.strictObject({
      to: z.string().startsWith("/"),
      module: z.string().min(1),
      label: z.string().min(1),
      area: z.string().min(1),
      decision: z.enum(decisions),
      taskLabel: z.string().optional(),
      mergeInto: z.string().optional(),
      note: z.string().optional(),
    }),
  ),
});

export type Area = z.infer<typeof areaSchema>;
export type WorkspaceDefinition = z.infer<typeof workspaceSchema>;
export type WorkspaceConfig = z.infer<typeof configSchema>;
export type AuditEntry = z.infer<typeof auditSchema>["entries"][number];

export const navAudit: AuditEntry[] = auditSchema.parse(auditData).entries;
export const auditByTo: ReadonlyMap<string, AuditEntry> = new Map(navAudit.map((e) => [e.to, e]));

function mergeById<T extends { id: string }>(base: T[], replacements: T[] = [], hidden: string[] = []): T[] {
  const byId = new Map(replacements.map((r) => [r.id, r]));
  const kept = base.filter((b) => !hidden.includes(b.id)).map((b) => byId.get(b.id) ?? b);
  const known = new Set(base.map((b) => b.id));
  return [...kept, ...replacements.filter((r) => !known.has(r.id) && !hidden.includes(r.id))];
}

export function loadWorkspaceConfig(tenantOverride?: unknown): WorkspaceConfig {
  const base = configSchema.parse(baseConfigData);
  if (tenantOverride === undefined) {
    return base;
  }
  try {
    const override = overrideSchema.parse(tenantOverride);
    return {
      areas: mergeById(base.areas, override.areas, override.hide),
      workspaces: mergeById(base.workspaces, override.workspaces, override.hide),
    };
  } catch (error) {
    recordError(error instanceof Error ? error : new Error(String(error)));
    return base;
  }
}

const otherArea: Area = { id: OTHER_AREA_ID, label: "Other", description: "" };

export interface NavEntry {
  to: string;
  label: string;
  originalLabel: string;
  moduleId: string;
  moduleTitle: string;
  area: string;
  areaLabel: string;
  decision: Decision;
  gated: boolean;
}

export function resolveNavEntries(modules: PassageModule[], areas: Area[]): NavEntry[] {
  const areaLabels = new Map(areas.map((a) => [a.id, a.label]));
  return modules.flatMap((m) =>
    m.nav.map((n) => {
      const audited = auditByTo.get(n.to);
      const area = audited?.area ?? OTHER_AREA_ID;
      return {
        to: n.to,
        label: audited?.taskLabel ?? n.label,
        originalLabel: n.label,
        moduleId: m.id,
        moduleTitle: m.title,
        area,
        areaLabel: areaLabels.get(area) ?? otherArea.label,
        decision: audited?.decision ?? "keep",
        gated: n.duty !== undefined,
      };
    }),
  );
}

export interface ResolvedWorkspace {
  id: string;
  label: string;
  persona: string;
  description: string;
  entries: NavEntry[];
}

export function resolveWorkspaces(config: WorkspaceConfig, entries: NavEntry[]): ResolvedWorkspace[] {
  const byTo = new Map(entries.map((e) => [e.to, e]));
  return config.workspaces
    .map((w) => ({
      id: w.id,
      label: w.label,
      persona: w.persona,
      description: w.description,
      entries: w.entries.flatMap((to) => byTo.get(to) ?? []),
    }))
    .filter((w) => w.entries.length > 0);
}

export interface AreaGroup {
  area: Area;
  main: NavEntry[];
  advanced: NavEntry[];
}

export function groupByArea(entries: NavEntry[], areas: Area[]): AreaGroup[] {
  return [...areas, otherArea]
    .map((area) => {
      const inArea = entries.filter((e) => e.area === area.id);
      return {
        area,
        main: inArea.filter((e) => e.decision === "keep"),
        advanced: inArea.filter((e) => e.decision !== "keep"),
      };
    })
    .filter((g) => g.main.length + g.advanced.length > 0);
}

export function matchesQuery(entry: NavEntry, query: string): boolean {
  const needle = query.trim().toLowerCase();
  return (
    needle === "" ||
    [entry.label, entry.originalLabel, entry.moduleTitle, entry.areaLabel].some((text) => text.toLowerCase().includes(needle))
  );
}

export interface WorkspacePrefs {
  dropped?: string[];
  active?: string;
}

export interface EffectivePrefs {
  held: ResolvedWorkspace[];
  active: ResolvedWorkspace | undefined;
}

function gatedCount(workspace: ResolvedWorkspace): number {
  return workspace.entries.filter((e) => e.gated).length;
}

export function effectivePrefs(prefs: WorkspacePrefs, available: ResolvedWorkspace[]): EffectivePrefs {
  const kept = available.filter((w) => !prefs.dropped?.includes(w.id));
  const held = kept.length > 0 ? kept : available;
  const broadest = held.reduce<ResolvedWorkspace | undefined>(
    (best, w) => (best === undefined || gatedCount(w) > gatedCount(best) ? w : best),
    undefined,
  );
  return { held, active: held.find((w) => w.id === prefs.active) ?? broadest };
}
