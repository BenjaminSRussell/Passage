// @vitest-environment node
/**
 * Guards the error rule in apps/web/docs/ux-writing.md: a failure's problem code
 * is never the message. A module shows a failure with `ProblemMessage` (or
 * `problemSummary` where it needs a string); the code and request id then sit
 * under "Details" for support.
 *
 * Flagged: a failure's `.code` (an `error`, `err`, `failure`, `problem` or
 * `issue`, including `x.error` and `x.problem`) rendered as text, either as a
 * JSX child, inside a template literal, or joined to a string with `+`. Reading
 * the code to decide what to do (`failure.code === "..."`, a lookup, a test of
 * `failure.code`) is fine, and so is the shell's own errors folder.
 */
import { parseAst } from "vite";

/**
 * Rendered failure codes left for a later pass, as `path/relative/to/src:expression`. The
 * purchase-to-pay and order-to-cash modules are clean; these belong to other areas. Remove an
 * entry when its page moves to ProblemMessage. Do not add to this list.
 */
const ALLOWLIST = new Set<string>([
  "modules/budgeting/api/client.ts:err.problem.code",
  "modules/costacct/api/client.ts:err.problem.code",
  "modules/costing/api/client.ts:err.problem.code",
  "modules/analytics/DashboardsPage.tsx:failure.code",
  "modules/analytics/DataQualityPage.tsx:failure.code",
  "modules/eam/PlansPage.tsx:rounds.error.code",
  "modules/eam/PlansPage.tsx:state.error.code",
  "modules/finreport/NewRunPage.tsx:hierarchies.error.code",
  "modules/finreport/NewRunPage.tsx:trees.error.code",
  "modules/finreport/TreeRevisions.tsx:list.error.code",
  "modules/hr/AssignmentForm.tsx:employments.error.code",
  "modules/hr/AssignmentForm.tsx:workers.error.code",
  "modules/integration/parts.tsx:issue.code",
  "modules/intercompany/PartnerDetailPage.tsx:failure.code",
  "modules/migration-factory/SetTolerancePage.tsx:state.error.code",
  "modules/mobile-flows/NewPrintDocumentPage.tsx:failure.code",
  "modules/mobile-flows/api/client.ts:error.code",
  "modules/org/HierarchyDesignerPage.tsx:units.error.code",
  "modules/org/StructurePages.tsx:calendars.error.code",
  "modules/org/StructurePages.tsx:warehouses.error.code",
  "modules/org/UomTabs.tsx:failure.code",
  "modules/production/module.ts:error.code",
  "modules/projects/ChangeOrderPages.tsx:wbs.error.code",
  "modules/quality/NewOrderPage.tsx:failure.code",
  "modules/quality/UsageDecisionForm.tsx:failure.code",
  "modules/scheduling/CapacityBlockDetailPage.tsx:failure.code",
  "modules/scheduling/EditResourcePage.tsx:failure.code",
  "modules/scheduling/NetworkDetailPage.tsx:failure.code",
  "modules/scheduling/ParametersPage.tsx:failure.code",
  "modules/service/AgreementsPage.tsx:profiles.error.code",
  "modules/service/CasesPage.tsx:profiles.error.code",
  "modules/shopfloor/KioskJobs.tsx:failure.code",
  "modules/shopfloor/KioskPage.tsx:failure.code",
  "modules/shopfloor/TimeApprovalPage.tsx:workers.error.code",
  "modules/shopfloor/WorkerCostRates.tsx:failure.code",
  "modules/shopfloor/WorkerHomeResource.tsx:failure.code",
  "modules/shopfloor/WorkerMaintenance.tsx:failure.code",
  "modules/shopfloor/WorkerMaintenance.tsx:stateFailure.code",
  "modules/shopfloor/WorkerPaySetup.tsx:failure.code",
]);

type Node = { type?: unknown; start?: number; end?: number; [key: string]: unknown };

const isNode = (value: unknown): value is Node => typeof value === "object" && value !== null;

/** The name an expression ends in: `failure` for `failure`, `error` for `state.error`. */
function lastName(node: unknown): string | undefined {
  if (!isNode(node)) return undefined;
  if (node.type === "Identifier") return String(node.name);
  if (node.type === "MemberExpression" && !node.computed && isNode(node.property)) return String(node.property.name);
  if (node.type === "ChainExpression") return lastName(node.expression);
  return undefined;
}

const FAILURE_NAME = /(?:^|[a-z])(?:[Ee]rror|[Ee]rr|[Ff]ailure|[Pp]roblem|[Ii]ssue)$|^(?:error|err|failure|problem|issue)$/;

function isFailureCode(node: Node): boolean {
  if (node.type === "ChainExpression") return isNode(node.expression) && isFailureCode(node.expression);
  if (node.type !== "MemberExpression" || node.computed || !isNode(node.property) || node.property.name !== "code") return false;
  const owner = lastName(node.object);
  return owner !== undefined && FAILURE_NAME.test(owner);
}

interface Finding {
  file: string;
  line: number;
  text: string;
}

/** The failure codes a source file renders as text. */
export function findBareCodes(source: string, file: string): Finding[] {
  const program = parseAst(source, { lang: file.endsWith(".tsx") ? "tsx" : "ts" }, file);
  const found: Finding[] = [];
  const lineOf = (offset: number) => source.slice(0, offset).split("\n").length;

  const visit = (node: unknown, parent: Node | null) => {
    if (Array.isArray(node)) {
      for (const child of node) visit(child, parent);
      return;
    }
    if (!isNode(node)) return;
    const rendered = parent !== null && (parent.type === "JSXExpressionContainer" || parent.type === "TemplateLiteral" || (parent.type === "BinaryExpression" && parent.operator === "+"));
    if (rendered && isFailureCode(node)) {
      found.push({ file, line: lineOf(node.start ?? 0), text: source.slice(node.start ?? 0, node.end ?? 0) });
    }
    for (const [key, child] of Object.entries(node)) {
      if (key === "type" || key === "start" || key === "end") continue;
      if (isNode(child) || Array.isArray(child)) visit(child, node);
    }
  };
  visit(program, null);
  return found;
}

const sources = import.meta.glob<string>(["../../modules/**/*.{ts,tsx}", "../**/*.{ts,tsx}", "!**/*.test.{ts,tsx}", "!**/*.d.ts", "!**/testing/**", "!../errors/**"], {
  query: "?raw",
  import: "default",
  eager: true,
});

describe("failure codes are not messages", () => {
  it("flags a code rendered as text, but not one read to make a decision", () => {
    const sample = [
      "function A({ failure, state }) {",
      "  if (failure.code === 'conflict_version') retry();",
      "  const text = failure.code ? `${failure.message} (${failure.code})` : failure.message;",
      "  const hint = 'Could not load (' + state.error.code + ').';",
      "  const label = MESSAGES[failure.code] ?? failure.message;",
      "  return <p>{failure.message} ({failure.code}) {record.code} {errors.code}</p>;",
      "}",
    ].join("\n");
    expect(findBareCodes(sample, "sample.tsx").map((f) => `${f.line}:${f.text}`)).toEqual(["3:failure.code", "4:state.error.code", "6:failure.code"]);
  });

  it("no module or shell source renders a failure's code as the message", () => {
    const files = Object.entries(sources);
    expect(files.length).toBeGreaterThan(100);
    const findings = files
      .flatMap(([path, source]) => findBareCodes(source, path.replace(/^\.\.\/\.\.\//, "").replace(/^\.\.\//, "shell/")))
      .filter((f) => !ALLOWLIST.has(`${f.file}:${f.text}`));
    const report = findings.map((f) => `${f.file}:${f.line}  ${f.text}`).join("\n");
    expect(report, "Show the failure with ProblemMessage (or problemSummary for a string); the code belongs under Details").toBe("");
  }, 60_000);
});
