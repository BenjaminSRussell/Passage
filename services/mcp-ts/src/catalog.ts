import { toMcpTool, validateToolDefinition, type McpTool, type ToolDefinition } from "./tool.js";

export class CatalogError extends Error {
  readonly errors: string[];

  constructor(errors: string[]) {
    super(`tool catalog rejected: ${errors.join("; ")}`);
    this.name = "CatalogError";
    this.errors = errors;
  }
}

export interface Catalog {
  readonly tools: readonly ToolDefinition[];
  find(name: string): ToolDefinition | undefined;
}

/** Validates every definition and refuses duplicate names. Tools are sorted by name. */
export function buildCatalog(defs: readonly ToolDefinition[]): Catalog {
  const errors: string[] = [];
  const byName = new Map<string, ToolDefinition>();
  for (const d of defs) {
    errors.push(...validateToolDefinition(d));
    if (byName.has(d.name)) errors.push(`${d.name}: duplicate tool name`);
    byName.set(d.name, d);
  }
  if (errors.length > 0) throw new CatalogError(errors);
  const tools = [...defs].sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  return { tools, find: (name) => byName.get(name) };
}

/** tools/list: only tools whose required duty the caller holds (AC-47.1). */
export function visibleTools(cat: Catalog, duties: ReadonlySet<string>): McpTool[] {
  return cat.tools.filter((t) => duties.has(t.requiredDuty)).map(toMcpTool);
}
