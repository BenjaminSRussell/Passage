// @vitest-environment node
/**
 * The UI consistency audit (#508): which module pages bypass the shared components. Two rules.
 *
 * 1. Raw controls. A module renders a table, button, page title, select, textarea or text-like input with the HTML
 *    element instead of `DataTable`, `Button`, `PageHeader`, `Select`, `Textarea` or `Input` (inside `FormField`).
 *    Checkboxes, radios, file pickers and hidden inputs have no shared component and are not counted. The
 *    procure-to-pay and order-to-cash modules must have none. The other modules are listed in
 *    `rawControlsAllowlist.json` with their counts; the audit fails when a count grows, a page is added, or a listed page
 *    no longer has the control (remove it from the list).
 * 2. List pages that are not on `ListPage`. A page with a header and a table whose rows open a record, and no
 *    `ListPage`, in a procure-to-pay or order-to-cash module. A record's detail page or a create page is not a list
 *    page: its table holds the record's related rows. They are listed in `listPageBacklog.json`; the audit fails
 *    when a page is added to that shape and when a listed page has moved to `ListPage` (remove it from the list).
 *
 * Run `npx vitest run src/shell/ui/consistency.test.ts` to see the report; the failure message lists what to change.
 */
import { describe, expect, it } from "vitest";
import backlog from "./listPageBacklog.json";
import allowlist from "./rawControlsAllowlist.json";

const sources = import.meta.glob<string>(["../../modules/**/*.tsx", "!../../modules/**/*.test.tsx"], { query: "?raw", import: "default", eager: true });

/** Modules of the procure-to-pay and order-to-cash flows. */
export const FLOW_MODULES = ["purchasing", "receiving", "ap-invoices", "payables", "sales", "fulfillment", "billing", "receivables"];

const NON_TEXT_INPUT = /^(?:checkbox|radio|file|hidden|range|color)$/;

function openingTags(source: string, tag: string): string[] {
  return source.match(new RegExp(`<${tag}(?=[\\s/>])[^>]*>?`, "g")) ?? [];
}

/** How many raw controls of each kind a source renders. */
export function rawControls(source: string): Record<string, number> {
  const found: Record<string, number> = {};
  const add = (kind: string, count: number) => {
    if (count > 0) found[kind] = (found[kind] ?? 0) + count;
  };
  for (const tag of ["table", "button", "h1", "select", "textarea"]) add(tag, openingTags(source, tag).length);
  add(
    "input",
    openingTags(source, "input").filter((t) => {
      const type = /type=["']([a-z]+)["']/.exec(t)?.[1] ?? "text";
      return !NON_TEXT_INPUT.test(type);
    }).length,
  );
  return found;
}

/** A page with a header and a table whose rows open a record, and no ListPage. */
export function isUnconvertedList(source: string): boolean {
  return source.includes("<PageHeader") && source.includes("<DataTable") && !source.includes("<ListPage") && /onRowOpen/.test(source);
}

/**
 * A record's detail page or a create page: a table there lists the record's related rows (the credit notes of an
 * invoice, the packing slips of a shipment), which open their own record but are not a list screen of their own.
 */
export function isRecordPage(file: string): boolean {
  return /\/(?:[A-Za-z]+Detail|New[A-Za-z]+)Page\.tsx$/.test(file);
}

const moduleOf = (path: string) => path.replace("../../modules/", "").split("/")[0];
const fileOf = (path: string) => path.replace("../../", "");

describe("UI consistency audit", () => {
  it("has no raw controls in the procure-to-pay and order-to-cash modules, and no new ones elsewhere", () => {
    const listed = allowlist.entries as Record<string, Record<string, number>>;
    const problems: string[] = [];
    for (const [path, source] of Object.entries(sources)) {
      const file = fileOf(path);
      const found = rawControls(source);
      const allowed = listed[file] ?? {};
      if (FLOW_MODULES.includes(moduleOf(path)) && Object.keys(found).length > 0) {
        problems.push(`${file}: a procure-to-pay or order-to-cash page renders ${JSON.stringify(found)}; use the shared component`);
        continue;
      }
      for (const kind of new Set([...Object.keys(found), ...Object.keys(allowed)])) {
        if ((found[kind] ?? 0) > (allowed[kind] ?? 0)) problems.push(`${file}: ${found[kind]} raw <${kind}> (listed ${allowed[kind] ?? 0}); use the shared component`);
        if ((found[kind] ?? 0) < (allowed[kind] ?? 0)) problems.push(`${file}: ${found[kind] ?? 0} raw <${kind}> now (listed ${allowed[kind]}); lower the count in rawControlsAllowlist.json`);
      }
    }
    expect(problems).toEqual([]);
  });

  it("lists every procure-to-pay and order-to-cash list page that is not on ListPage", () => {
    const listed = new Set<string>(backlog.pages);
    const now = Object.entries(sources)
      .filter(([path, source]) => FLOW_MODULES.includes(moduleOf(path)) && !isRecordPage(path) && isUnconvertedList(source))
      .map(([path]) => fileOf(path))
      .sort();
    expect(now.filter((f) => !listed.has(f)), "new list pages use ListPage (docs/lists.md)").toEqual([]);
    expect([...listed].filter((f) => !now.includes(f)), "these moved to ListPage: remove them from listPageBacklog.json").toEqual([]);
  });

  it("recognises the shapes it looks for", () => {
    expect(rawControls('<table className="x"><button type="button">Go</button><input type="checkbox" /><input /><input type="number"/>')).toEqual({ table: 1, button: 1, input: 2 });
    expect(rawControls("<Table><Button><DataTable /><PageHeader /></Button></Table>")).toEqual({});
    expect(isUnconvertedList("<PageHeader /><DataTable onRowOpen={open} />")).toBe(true);
    expect(isUnconvertedList("<ListPage /><PageHeader /><DataTable onRowOpen={open} />")).toBe(false);
    expect(isUnconvertedList("<PageHeader /><DataTable />")).toBe(false);
    expect(isRecordPage("../../modules/billing/InvoiceDetailPage.tsx")).toBe(true);
    expect(isRecordPage("../../modules/billing/NewShipmentProposalPage.tsx")).toBe(true);
    expect(isRecordPage("../../modules/billing/InvoicesPage.tsx")).toBe(false);
  });
});
