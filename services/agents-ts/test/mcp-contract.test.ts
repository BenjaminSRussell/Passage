// ponytail: scraped, not imported, because services/mcp-ts is a separate package (own tsconfig rootDir, ESM .js specifiers, node_modules); a cross-package import would couple the builds, upgrade by publishing the constants from a shared package.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { COPILOT_DUTY, HELP_SEARCH_TOOL, SEARCH_TOOLS_TOOL } from "../src/runtime/copilot.js";

const composite = readFileSync(new URL("../../mcp-ts/src/composite.ts", import.meta.url), "utf8");
const literal = (name: string): string => {
  const value = new RegExp(`const ${name} = "([^"]+)"`).exec(composite)?.[1];
  if (value === undefined) throw new Error(`services/mcp-ts/src/composite.ts no longer declares const ${name} = "..."; update this contract test with the rename`);
  return value;
};

describe("task tools against services/mcp-ts composite tools", () => {
  it("names the same tools and the same duty as the gateway", () => {
    expect(literal("SEARCH_TOOLS_NAME")).toBe(SEARCH_TOOLS_TOOL);
    expect(literal("HELP_SEARCH_NAME")).toBe(HELP_SEARCH_TOOL);
    expect(literal("COPILOT_DUTY")).toBe(COPILOT_DUTY);
  });
});
