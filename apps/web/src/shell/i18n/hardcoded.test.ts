import baseline from "./hardcodedBaseline.json";

/**
 * Guard against hard-coded user-facing text in the shell (#509). There is no ESLint here, so this test reads the
 * shell's components and counts text that sits in JSX or in a text attribute (aria-label, title, placeholder, alt)
 * instead of coming from `copy`. A file may not have more of it than the baseline says, so it can only shrink: move
 * the string into shell/copy.ts (and its Spanish into shell/i18n/es.ts) and lower the baseline.
 * To list the current counts after fixing strings: VITE_I18N_BASELINE=print npx vitest run src/shell/i18n/hardcoded, then copy the BASELINE line into hardcodedBaseline.json.
 */
const files = import.meta.glob(["../*.tsx", "../ui/*.tsx", "../pages/*.tsx", "../help/*.tsx", "../lookup/*.tsx", "../errors/*.tsx", "../auth/*.tsx"], {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;

const JSX_TEXT = />\s*([A-Za-z][A-Za-z0-9 ,.'’!?:()/-]{2,}[A-Za-z.!?)])\s*</g;
const JSX_LINE = /^\s*([A-Z][a-z]+(?: [A-Za-z0-9,.'’!?-]+)+)\s*$/;
const ATTR = /\b(?:aria-label|title|placeholder|alt)="([A-Za-z][^"{}]{2,})"/g;
const CODE_WORDS = /^(?:import|export|return|const|let|type|interface|function|if|else|case|default)\b/;

export function hardcodedIn(source: string): string[] {
  const found: string[] = [];
  const lines = source.split("\n");
  lines.forEach((line, index) => {
    const trimmed = line.trim();
    if (trimmed.startsWith("//") || trimmed.startsWith("*") || trimmed.startsWith("/*")) return;
    for (const m of line.matchAll(JSX_TEXT)) if (m[1] && !/^(?:=|\()/.test(m[1])) found.push(m[1]);
    for (const m of line.matchAll(ATTR)) if (m[1]) found.push(m[1]);
    const prev = lines[index - 1]?.trim() ?? "";
    const inJsxChildren = /[>}]$/.test(prev) && !/=>$/.test(prev) && !prev.endsWith("=>");
    if (inJsxChildren && JSX_LINE.test(line) && !CODE_WORDS.test(trimmed) && !/[;=]/.test(trimmed)) found.push(trimmed);
  });
  return found;
}

describe("hard-coded shell text", () => {
  const counts: Record<string, number> = {};
  for (const [path, source] of Object.entries(files)) {
    // Tests, and the component catalog (a developer page that documents components in English), are not user text.
    if (path.endsWith(".test.tsx") || path.endsWith("ComponentCatalogPage.tsx")) continue;
    const n = hardcodedIn(source).length;
    if (n > 0) counts[path.replace("../", "")] = n;
  }

  it("reads the shell's components", () => {
    expect(Object.keys(files).length).toBeGreaterThan(20);
  });

  it("finds the kinds of text it is meant to find", () => {
    expect(hardcodedIn('<button aria-label="Close dialog">Save changes</button>')).toEqual(["Save changes", "Close dialog"]);
    expect(hardcodedIn("<p>{copy.help.noEntry}</p>")).toEqual([]);
    expect(hardcodedIn("  // <b>Not shown</b>")).toEqual([]);
  });

  it("does not grow beyond the baseline", () => {
    if (import.meta.env.VITE_I18N_BASELINE === "print") {
      console.log(`BASELINE ${JSON.stringify(counts)}`);
      return;
    }
    const grown = Object.entries(counts)
      .filter(([file, n]) => n > ((baseline as Record<string, number>)[file] ?? 0))
      .map(([file, n]) => `${file}: ${n} hard-coded strings, baseline ${(baseline as Record<string, number>)[file] ?? 0}`);
    expect(grown).toEqual([]);
  });

  it("keeps the new i18n code and the help panel free of hard-coded text", () => {
    expect(Object.keys(counts).filter((f) => f.startsWith("help/") || f.startsWith("i18n/"))).toEqual([]);
  });
});
