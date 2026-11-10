import { describe, expect, it } from "vitest";
import allowlist from "./rawIdAllowlist.json";

const ID_TEXT = String.raw`(["'\x60])((?:(?!\1)[^\n]){0,60}?\s[iI][dD])\1`;
const RAW_ID_LABEL = new RegExp(
  String.raw`(?:\b(?:label|placeholder)=\{?\s*|\blabel:\s*|\b[A-Za-z_]\w*\(\s*(?:[^()\n]*?,\s*)?)${ID_TEXT}`,
  "g",
);

const sources = import.meta.glob<string>(["../../**/*.tsx", "!../../**/*.test.tsx", "!../../shell/lookup/**"], {
  query: "?raw",
  import: "default",
  eager: true,
});

function rawIdLabels(): Record<string, string[]> {
  const found: Record<string, string[]> = {};
  for (const [path, source] of Object.entries(sources)) {
    const labels = [...source.matchAll(RAW_ID_LABEL)].map((match) => match[2]).sort();
    if (labels.length > 0) found[path.replace("../../", "")] = labels;
  }
  return found;
}

const allowed = allowlist.entries as Record<string, { labels: string[]; reason: string }>;

describe("raw id inputs", () => {
  it("has no label, placeholder or helper-built \"... id\" field outside the allowlist", () => {
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

  it("catches helper-built, template, object and placeholder id labels", () => {
    const samples = [
      'label="Vendor id"',
      'text("vendorId", "Vendor id", { mono: true })',
      'label={`${name} id`}',
      'placeholder="Product id"',
      '{ name: "vendor", label: "Vendor ID" }',
    ];
    expect(samples.map((sample) => [...sample.matchAll(RAW_ID_LABEL)].length)).toEqual([1, 1, 1, 1, 1]);
    expect([...'throw new Error("Missing record.")'.matchAll(RAW_ID_LABEL)]).toEqual([]);
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
