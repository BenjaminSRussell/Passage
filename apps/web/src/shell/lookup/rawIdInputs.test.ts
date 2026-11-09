import { describe, expect, it } from "vitest";
import allowlist from "./rawIdAllowlist.json";

const RAW_ID_LABEL = /\blabel="([^"]*\s[iI][dD])"/g;

const sources = import.meta.glob<string>(["../../**/*.tsx", "!../../**/*.test.tsx", "!../../shell/lookup/**"], {
  query: "?raw",
  import: "default",
  eager: true,
});

function rawIdLabels(): Record<string, string[]> {
  const found: Record<string, string[]> = {};
  for (const [path, source] of Object.entries(sources)) {
    const labels = [...source.matchAll(RAW_ID_LABEL)].map((match) => match[1]).sort();
    if (labels.length > 0) found[path.replace("../../", "")] = labels;
  }
  return found;
}

const allowed = allowlist.entries as Record<string, { labels: string[]; reason: string }>;

describe("raw id inputs", () => {
  it("has no label=\"... id\" field outside the allowlist", () => {
    const unlisted = Object.entries(rawIdLabels()).filter(([file, labels]) => {
      const listed = [...(allowed[file]?.labels ?? [])];
      return labels.some((label) => {
        const at = listed.indexOf(label);
        if (at === -1) return true;
        listed.splice(at, 1);
        return false;
      });
    });
    expect(unlisted.map(([file, labels]) => `${file}: ${labels.join(", ")}`)).toEqual([]);
  });

  it("lists only fields that still exist, so the allowlist shrinks as fields are converted", () => {
    const found = rawIdLabels();
    const stale = Object.entries(allowed)
      .filter(([file, entry]) => JSON.stringify([...entry.labels].sort()) !== JSON.stringify(found[file] ?? []))
      .map(([file]) => file);
    expect(stale).toEqual([]);
  });

  it("gives every exception a reason", () => {
    expect(Object.entries(allowed).filter(([, entry]) => entry.reason.trim() === "")).toEqual([]);
  });
});
