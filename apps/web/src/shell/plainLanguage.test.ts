// @vitest-environment node
/**
 * Guards the plain-language rule in apps/web/docs/ux-writing.md: text people
 * read never cites spec ids (FR-, BR-, AC-n.n) or delivery waves and slices.
 * The reference can stay in a code comment; comments are not scanned.
 *
 * Every string literal, template literal part and JSX text in the modules'
 * and shell's source counts as user-facing, except import paths, object keys
 * and attributes that are never shown (class names, test ids, routes, ids).
 */
import { parseAst } from "vite";

const SPEC_REF = /\b(?:FR|BR|AC)-\d+\.\d+/;
const DELIVERY_REF = /\b(?:wave|slice)[ -]?\d+/i;

/** Attributes whose values are never shown to people. */
const HIDDEN_ATTRIBUTES = new Set(["className", "id", "key", "htmlFor", "name", "to", "href", "type", "role", "method", "rel", "target"]);

/**
 * Strings that may keep a reference because they are not shown to people,
 * as `path/relative/to/src:exact text`. Keep it empty unless truly needed.
 */
const ALLOWLIST = new Set<string>([]);

interface Finding {
  file: string;
  line: number;
  text: string;
}

type Node = { type?: unknown; start?: number; [key: string]: unknown };

const isNode = (value: unknown): value is Node => typeof value === "object" && value !== null;

function attributeName(node: Node): string | undefined {
  const name = node.name;
  if (!isNode(name)) return undefined;
  if (typeof name.name === "string") return name.name;
  if (isNode(name.name) && typeof name.name.name === "string") return `${String((name.namespace as Node | undefined)?.name ?? "")}:${name.name.name}`;
  return undefined;
}

/** The user-facing strings of one source file that cite a spec id, wave or slice. */
export function findReferences(source: string, file: string): Finding[] {
  const program = parseAst(source, { lang: file.endsWith(".tsx") ? "tsx" : "ts" }, file);
  const found: Finding[] = [];
  const lineOf = (offset: number) => source.slice(0, offset).split("\n").length;

  const visit = (node: unknown, parent: Node | null, key: string | null) => {
    if (Array.isArray(node)) {
      for (const child of node) visit(child, parent, key);
      return;
    }
    if (!isNode(node)) return;
    if (node.type === "ImportDeclaration" || node.type === "ExportAllDeclaration" || node.type === "TSImportType" || node.type === "ImportExpression") return;
    if (node.type === "ExportNamedDeclaration" && node.source) return;
    if (node.type === "JSXAttribute") {
      const name = attributeName(node);
      if (name && (HIDDEN_ATTRIBUTES.has(name) || name.startsWith("data-"))) return;
    }
    let text: string | undefined;
    if (node.type === "Literal" && typeof node.value === "string") text = node.value;
    else if (node.type === "JSXText" && typeof node.value === "string") text = node.value;
    else if (node.type === "TemplateElement" && isNode(node.value)) text = String(node.value.cooked ?? node.value.raw ?? "");
    const isKey = parent?.type === "Property" && key === "key" && parent.computed !== true;
    if (text !== undefined && !isKey && (SPEC_REF.test(text) || DELIVERY_REF.test(text))) {
      const flat = text.replace(/\s+/g, " ").trim();
      if (!ALLOWLIST.has(`${file}:${flat}`)) found.push({ file, line: lineOf(node.start ?? 0), text: flat });
    }
    for (const [childKey, child] of Object.entries(node)) {
      if (childKey === "type" || childKey === "start" || childKey === "end") continue;
      if (isNode(child) || Array.isArray(child)) visit(child, node, childKey);
    }
  };
  visit(program, null, null);
  return found;
}

/** Every module and shell source file except tests and test helpers, keyed by its path relative to `src/shell`. */
const sources = import.meta.glob<string>(
  ["../modules/**/*.{ts,tsx}", "./**/*.{ts,tsx}", "!**/*.test.{ts,tsx}", "!**/*.d.ts", "!**/testing/**", "!**/testHarness.{ts,tsx}"],
  { query: "?raw", import: "default", eager: true },
);

describe("plain-language copy", () => {
  it("flags references in JSX text, label and description props and plain strings, but not in comments", () => {
    const sample = [
      "// FR-1.2: kept in a comment",
      'const hint = "Working days each demand moves (FR-22.5).";',
      "export function Page() {",
      '  return <PageHeader title="Orders" description="Released in wave 7." data-testid="fr-1.2" className="slice-5">',
      "    Filled in with the AC-40.6 example.",
      "    {/* BR-3.4 stays in a comment */}",
      "  </PageHeader>;",
      "}",
      'const map = { "BR-9.9": "keys are not shown" };',
    ].join("\n");
    const found = findReferences(sample, "sample.tsx").map((f) => f.text);
    expect(found).toEqual(["Working days each demand moves (FR-22.5).", "Released in wave 7.", "Filled in with the AC-40.6 example."]);
  });

  it("no module or shell string cites a spec id, wave or slice", () => {
    const files = Object.entries(sources);
    expect(files.length).toBeGreaterThan(100);
    const findings = files.flatMap(([path, source]) => findReferences(source, path.replace(/^\.\.\//, "").replace(/^\.\//, "shell/")));
    const report = findings.map((f) => `${f.file}:${f.line}  ${f.text}`).join("\n");
    expect(report, "Rewrite these strings in plain language; keep the reference in a code comment if it helps").toBe("");
  }, 60_000);
});
