import type { IconName } from "./ui";

const byModule: Record<string, IconName> = {
  admin: "shield",
  close: "calendarCheck",
  engineering: "cog",
  metadata: "database",
  payables: "receipt",
  product: "box",
  tax: "percent",
  workflow: "workflow",
};

export function workspaceIcon(moduleId: string): IconName {
  return byModule[moduleId] ?? "grid";
}
