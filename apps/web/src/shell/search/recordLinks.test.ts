import { matchRoutes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import apiSource from "../../../../../services/search/internal/api/api.go?raw";
import storeSource from "../../../../../services/search/internal/store/store.go?raw";
import { loadModules } from "../../modules/registry";
import { RECORD_TYPES } from "./records";

/** The record types and web links the search service returns, read from its source table. */
function serviceSources(): { type: string; link: string }[] {
  const typeNames = new Map([...storeSource.matchAll(/^\s*(Type\w+)\s*=\s*"(\w+)"/gm)].map((m) => [m[1], m[2]]));
  return [...apiSource.matchAll(/\{store\.(Type\w+), \[\]string\{[^}]*\}, "([^"]+)"/g)].map((m) => ({ type: typeNames.get(m[1]) ?? m[1], link: m[2] }));
}

describe("record search results", () => {
  const sources = serviceSources();

  it("knows every record type the service returns, and no other", () => {
    expect(sources.map((s) => s.type).sort()).toEqual([...RECORD_TYPES].sort());
  });

  it.each(sources)("opens a $type on a page of its own ($link)", ({ link }) => {
    const routes = loadModules().flatMap((m) => m.routes);
    const matched = matchRoutes(routes, link.replace("%s", "0b0f6a52-9d5e-4c1b-8a57-3f1f4c2d9e10"));
    const path = matched?.[matched.length - 1]?.route.path ?? "";
    expect(path).toContain(":");
    expect(path).not.toBe("*");
  });
});
