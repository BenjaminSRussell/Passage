import type { Catalog } from "./catalog.js";
import { ToolInputError, type ToolDefinition } from "./tool.js";

export const SEARCH_TOOLS_NAME = "depot.meta.search_tools";
export const HELP_SEARCH_NAME = "depot.help.search";

export const COPILOT_DUTY = "mcp.copilot.use";
const MAX_QUERY_CHARS = 500;
const MAX_HELP_QUERY_CHARS = 1000;
const MAX_TOKENS = 20;
const DEFAULT_LIMIT = 10;
const MAX_DESCRIPTION_CHARS = 160;

export interface ToolHit {
  name: string;
  description: string;
}

function tokensOf(text: string): string[] {
  return text.toLowerCase().split(/[^a-z0-9]+/).filter((t) => t !== "");
}

function firstSentence(text: string): string {
  const end = text.search(/[.!?](\s|$)/);
  const sentence = end === -1 ? text : text.slice(0, end + 1);
  return sentence.length > MAX_DESCRIPTION_CHARS ? `${sentence.slice(0, MAX_DESCRIPTION_CHARS - 3)}...` : sentence;
}

const toolTokens = new WeakMap<ToolDefinition, { name: Set<string>; description: Set<string> }>();

function tokensOfTool(t: ToolDefinition): { name: Set<string>; description: Set<string> } {
  let tokens = toolTokens.get(t);
  if (tokens === undefined) {
    tokens = { name: new Set(tokensOf(t.name)), description: new Set(tokensOf(t.description)) };
    toolTokens.set(t, tokens);
  }
  return tokens;
}

/** Duty filter first, then lexical rank: a tool the caller cannot see is never scored or returned. */
export function searchTools(catalog: Catalog, duties: ReadonlySet<string>, query: string, limit: number): ToolHit[] {
  const wanted = new Set(tokensOf(query.slice(0, MAX_QUERY_CHARS).replace(/\p{Cc}/gu, " ")).slice(0, MAX_TOKENS));
  const size = Number.isFinite(limit) ? Math.max(1, Math.min(Math.trunc(limit), DEFAULT_LIMIT)) : DEFAULT_LIMIT;
  return catalog.tools
    .filter((t) => duties.has(t.requiredDuty))
    .map((t) => {
      const tokens = tokensOfTool(t);
      let score = 0;
      for (const w of wanted) score += (tokens.name.has(w) ? 3 : 0) + (tokens.description.has(w) ? 1 : 0);
      return { t, score };
    })
    .filter((s) => s.score > 0)
    .sort((a, b) => b.score - a.score || (a.t.name < b.t.name ? -1 : 1))
    .slice(0, size)
    .map((s) => ({ name: s.t.name, description: firstSentence(s.t.description) }));
}

const searchToolsTool: ToolDefinition = {
  name: SEARCH_TOOLS_NAME,
  description: "Find Depot tools by what they do. Returns the names and one-line descriptions of the tools you may call, best match first.",
  version: 1,
  sourceRef: "composite.search_tools",
  inputSchema: {
    type: "object",
    properties: { query: { type: "string", maxLength: MAX_QUERY_CHARS }, limit: { type: "integer" } },
    required: ["query"],
  },
  outputSchema: { type: "object" },
  requiredDuty: COPILOT_DUTY,
  approvalMode: "none",
  readOnly: true,
  idempotent: true,
  enforceMaxLength: true,
  local: ({ args, duties, catalog }) => {
    if (typeof args.query !== "string") throw new ToolInputError("query must be a string");
    const limit = typeof args.limit === "number" ? args.limit : DEFAULT_LIMIT;
    return { tools: searchTools(catalog, duties, args.query, limit) };
  },
};

const helpSearchTool: ToolDefinition = {
  name: HELP_SEARCH_NAME,
  description: "Search Depot help articles, documents and policies. Entity metadata and the message catalog are not indexed yet.",
  version: 1,
  method: "POST",
  path: "/api/v1/knowledge/search",
  sourceRef: "composite.help_search",
  inputSchema: {
    type: "object",
    properties: { query: { type: "string", maxLength: MAX_HELP_QUERY_CHARS } },
    required: ["query"],
  },
  outputSchema: {
    type: "object",
    properties: { chunks: { type: "array" } },
    required: ["chunks"],
  },
  requiredDuty: COPILOT_DUTY,
  approvalMode: "none",
  readOnly: true,
  idempotent: true,
  enforceMaxLength: true,
};

export function compositeTools(): ToolDefinition[] {
  return [searchToolsTool, helpSearchTool];
}
