import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { CellSummary, Definition, DrillLine, LinePage, RunSummary } from "./api/client";
import { httpUrl, sourceLink } from "./drill";
import { json, pageViolations, problem, renderAt, server, useFakeServer, type Reply } from "./testServer";

useFakeServer();

const RUN = "0190f0a0-0000-7000-8000-000000000301";
const LEDGER = "0190f0a0-0000-7000-8000-000000000302";

const run: RunSummary = {
  id: RUN,
  definitionCode: "PL-STD",
  definitionVersion: 1,
  ledgerId: LEDGER,
  currencyType: "acct",
  periodYear: 2026,
  periodNo: 3,
  status: "completed",
  hwmByLedger: { [LEDGER]: 42 },
  resultHash: "cd".repeat(32),
  cellCount: 2,
  requestedBy: "u1",
  startedAt: "2026-04-02T08:00:00Z",
  durationMs: 9,
};

const definition: Definition = {
  schemaVersion: "1",
  code: "PL-STD",
  name: "Income statement",
  version: 1,
  status: "published",
  kind: "income_statement",
  includeAdjustmentPeriods: false,
  rows: [
    { code: "REV", kind: "account_range", label: "Revenue", accountFrom: "4000", accountTo: "4999", normalBalance: "credit", format: {} },
    { code: "GM", kind: "calculation", label: "Gross margin", expression: "REV", normalBalance: "credit", format: {} },
  ],
  columns: [{ code: "C1", label: "March", periodExpr: "P" }],
};

const cells: CellSummary[] = [
  { row: "REV", column: "C1", value: "135.500000", blank: false, computed: false, keyCount: 2 },
  { row: "GM", column: "C1", value: "135.500000", blank: false, computed: true, keyCount: 0 },
];

const balances = {
  runId: RUN,
  row: "REV",
  column: "C1",
  value: "135.500000",
  blank: false,
  computed: false,
  sumOfKeys: "135.500000",
  keys: [
    { ledgerId: LEDGER, period: "2026-03", account: "4000", combination: "-", currencyType: "acct", amount: "110.000000" },
    { ledgerId: LEDGER, period: "2026-03", account: "4100", combination: "-", currencyType: "acct", amount: "25.500000" },
  ],
};

function line(over: Partial<DrillLine>): DrillLine {
  return {
    key: 0,
    ledgerId: LEDGER,
    postingDate: "2026-03-05",
    postingSequence: 1,
    lineNumber: 1,
    voucher: "GJ-1",
    period: "2026-03",
    account: "4000",
    combination: "-",
    currencyType: "acct",
    debit: "0.000000",
    credit: "10.000000",
    amount: "10.000000",
    closingEntry: false,
    ...over,
  };
}

function page(over: Partial<LinePage>): LinePage {
  return {
    runId: RUN,
    row: "REV",
    column: "C1",
    key: null,
    blank: false,
    computed: false,
    value: "135.500000",
    hwm: 42,
    limit: 500,
    items: [],
    sumOfLines: "0.000000",
    cumulativeSum: "0.000000",
    nextCursor: null,
    linesTotal: "135.500000",
    difference: "0.000000",
    unreconciledKeys: 0,
    reconciled: true,
    ...over,
  };
}

const firstPage = page({
  items: [
    line({ voucher: "AP-7", sourceUri: "/payables/invoices/INV-1", traceId: "4bf92f3577b34da6", traceUrl: "https://grafana.example/explore?traceId=4bf92f3577b34da6" }),
    line({ postingSequence: 2, voucher: "GJ-2", credit: "100.000000", amount: "100.000000", sourceUri: "https://dms.example.com/doc/9", traceId: "plain-trace" }),
  ],
  sumOfLines: "110.000000",
  cumulativeSum: "110.000000",
  nextCursor: "CURSOR-1",
});

const secondPage = page({
  items: [line({ key: 1, postingDate: "2026-03-20", postingSequence: 4, account: "4100", voucher: "GJ-4", credit: "25.500000", amount: "25.500000", sourceUri: "javascript:alert(1)", closingEntry: true })],
  sumOfLines: "25.500000",
  cumulativeSum: "135.500000",
});

function drillServer(lines: (url: URL) => Reply | undefined) {
  server.handler = (url) => {
    const p = url.pathname;
    if (p === `/api/v1/reporting/runs/${RUN}`) return json(200, run);
    if (p === `/api/v1/reporting/runs/${RUN}/cells`) return json(200, { items: cells });
    if (p === "/api/v1/reporting/definitions/PL-STD") return json(200, definition);
    if (p.endsWith("/cells/REV/C1/balances")) return json(200, balances);
    if (p.endsWith("/cells/GM/C1/balances")) return json(200, { ...balances, row: "GM", computed: true, keys: [], sumOfKeys: "0.000000" });
    if (/\/cells\/[^/]+\/[^/]+\/lines$/.test(p)) return lines(url);
    return undefined;
  };
}

async function openRevenueBalances() {
  const view = renderAt(`/finreport/runs/${RUN}`);
  fireEvent.click(await screen.findByRole("button", { name: /^Revenue, March/ }));
  await findTable({ name: "Balances" });
  await screen.findByText("4100");
  return view;
}

const lineCalls = () => server.calls.filter((c) => c.url.pathname.endsWith("/lines"));

describe("drill level 2: journal lines", () => {
  it("names the ledger of each line when the lines span ledgers (consolidated column)", async () => {
    const other = "0190f0a0-0000-7000-8000-000000000303";
    drillServer(() =>
      json(
        200,
        page({
          hwm: 0,
          items: [line({ voucher: "GJ-A" }), line({ ledgerId: other, voucher: "GJ-B", credit: "100.000000", amount: "100.000000" })],
          sumOfLines: "110.000000",
          cumulativeSum: "110.000000",
        }),
      ),
    );
    await openRevenueBalances();
    fireEvent.click(screen.getByRole("button", { name: "Show all journal lines" }));
    const table = await findTable({ name: "Journal lines" });
    expect(within(table).getByRole("columnheader", { name: "Ledger" })).toBeInTheDocument();
    const b = within(table).getByText("GJ-B").closest("tr") as HTMLElement;
    expect(within(b).getByTitle(other)).toHaveTextContent(other.slice(0, 8));
    expect(screen.getByText(/Showing 2 lines, running sum/)).toHaveTextContent("Showing 2 lines, running sum 110.00, each ledger as of its posting sequence in the run.");
  });

  it("shows no ledger column for the lines of one ledger", async () => {
    drillServer(() => json(200, firstPage));
    await openRevenueBalances();
    fireEvent.click(screen.getByRole("button", { name: "Show all journal lines" }));
    const table = await findTable({ name: "Journal lines" });
    await within(table).findByText("AP-7");
    expect(within(table).queryByRole("columnheader", { name: "Ledger" })).not.toBeInTheDocument();
  });

  it("lists every line of a cell page by page with source and trace links", async () => {
    drillServer((url) => (url.searchParams.get("cursor") === "CURSOR-1" ? json(200, secondPage) : json(200, firstPage)));
    const { container } = await openRevenueBalances();
    fireEvent.click(screen.getByRole("button", { name: "Show all journal lines" }));

    expect(await screen.findByText("AP-7")).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Journal lines" });
    expect(screen.getByText(/The journal lines sum to/)).toHaveTextContent("The journal lines sum to 135.50, the amount shown.");
    expect(screen.getByRole("heading", { name: "Journal lines for Revenue, March" })).toBeInTheDocument();
    expect(lineCalls()[0].url.searchParams.has("key")).toBe(false);

    // A Passage route opens in the app; an http(s) document opens in a new tab.
    expect(within(table).getByRole("link", { name: "Open source document of AP-7" })).toHaveAttribute("href", "/payables/invoices/INV-1");
    const external = within(table).getByRole("link", { name: "Open source document of GJ-2 (opens in a new tab)" });
    expect(external).toHaveAttribute("href", "https://dms.example.com/doc/9");
    expect(external).toHaveAttribute("rel", "noopener noreferrer");
    // The trace opens the configured viewer; without a link the id is shown as text.
    expect(within(table).getByRole("link", { name: "Open trace 4bf92f3577b34da6 (opens in a new tab)" })).toHaveAttribute(
      "href",
      "https://grafana.example/explore?traceId=4bf92f3577b34da6",
    );
    expect(within(table).getByText("plain-trace")).toBeInTheDocument();
    expect(screen.getByText(/Showing 2 lines, running sum/)).toHaveTextContent("Showing 2 lines, running sum 110.00, as of posting sequence 42.");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Load more lines" }));
    expect(await within(table).findByText("GJ-4")).toBeInTheDocument();
    expect(lineCalls()[1].url.searchParams.get("cursor")).toBe("CURSOR-1");
    expect(screen.getByText(/Showing 3 lines/)).toHaveTextContent("Showing 3 lines, running sum 135.50, as of posting sequence 42.");
    expect(screen.queryByRole("button", { name: "Load more lines" })).not.toBeInTheDocument();
    // An unsafe source link is never rendered as a link; closing entries are marked.
    const gj4 = within(table).getByText("GJ-4").closest("tr") as HTMLElement;
    expect(within(gj4).queryByRole("link")).not.toBeInTheDocument();
    expect(within(gj4).getAllByText("None")).toHaveLength(2); // no source link, no trace
    expect(within(gj4).getByText("Closing")).toBeInTheDocument();
  });

  it("drills one balance to its own lines", async () => {
    drillServer(() => json(200, page({ key: 1, value: "25.500000", linesTotal: "25.500000", items: secondPage.items, sumOfLines: "25.500000", cumulativeSum: "25.500000" })));
    await openRevenueBalances();
    fireEvent.click(screen.getByRole("button", { name: "Show journal lines of account 4100, 2026-03" }));
    expect(await screen.findByRole("heading", { name: "Journal lines for Revenue, March, account 4100, 2026-03" })).toBeInTheDocument();
    expect(lineCalls()[0].url.searchParams.get("key")).toBe("1");
    expect(await screen.findByText("GJ-4")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Close lines" }));
    expect(screen.queryByRole("table", { name: "Journal lines" })).not.toBeInTheDocument();
  });

  it("warns when the lines do not add up instead of hiding the gap", async () => {
    drillServer(() =>
      json(200, page({ items: firstPage.items, sumOfLines: "110.000000", cumulativeSum: "110.000000", linesTotal: "110.000000", difference: "25.500000", unreconciledKeys: 1, reconciled: false })),
    );
    await openRevenueBalances();
    fireEvent.click(screen.getByRole("button", { name: "Show all journal lines" }));
    expect(await screen.findByText("The journal lines do not add up to this amount")).toBeInTheDocument();
    expect(screen.getByText(/They sum to 110.000000, not 135.500000: 25.500000 is not explained/)).toHaveTextContent("(1 balance affected)");
    expect(screen.queryByText(/The journal lines sum to/)).not.toBeInTheDocument();
  });

  it("shows an empty state when no lines sit behind the amount", async () => {
    drillServer(() => json(200, page({ linesTotal: "0.000000", difference: "135.500000", unreconciledKeys: 2, reconciled: false })));
    await openRevenueBalances();
    fireEvent.click(screen.getByRole("button", { name: "Show all journal lines" }));
    expect(await screen.findByText("No journal lines")).toBeInTheDocument();
    expect(screen.getByText("The journal lines do not add up to this amount")).toBeInTheDocument();
  });

  it("explains a missing duty and lets other errors be retried", async () => {
    drillServer(() => problem(403, "AUTH-0003", "missing duty ledger.journal.read"));
    await openRevenueBalances();
    fireEvent.click(screen.getByRole("button", { name: "Show all journal lines" }));
    expect(await screen.findByText("You cannot see journal lines")).toBeInTheDocument();
    expect(screen.getByText(/View ledger transactions duty/)).toBeInTheDocument();

    let fail = true;
    drillServer(() => (fail ? problem(500, "COM-0500", "database unavailable") : json(200, firstPage)));
    fireEvent.click(screen.getByRole("button", { name: "Show journal lines of account 4000, 2026-03" }));
    expect(await screen.findByText("Journal lines are unavailable")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("AP-7")).toBeInTheDocument();
  });

  it("offers no lines for a computed amount", async () => {
    drillServer(() => json(200, firstPage));
    renderAt(`/finreport/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: /^Gross margin, March/ }));
    expect(await screen.findByText(/calculated from other rows or columns of the statement/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Show all journal lines" })).not.toBeInTheDocument();
    await waitFor(() => expect(lineCalls()).toHaveLength(0));
  });
});

describe("source and trace links", () => {
  it("only links Passage routes and http(s) URLs", () => {
    expect(sourceLink("/payables/invoices/1")).toEqual({ kind: "route", to: "/payables/invoices/1" });
    expect(sourceLink("https://dms.example.com/a")).toEqual({ kind: "external", href: "https://dms.example.com/a" });
    for (const unsafe of ["javascript:alert(1)", "data:text/html,x", "//evil.example.com", "/\\evil.example.com", "relative/path", "", undefined]) {
      expect(sourceLink(unsafe)).toBeNull();
    }
    expect(httpUrl("https://grafana.example/explore?traceId=1")).toBe("https://grafana.example/explore?traceId=1");
    expect(httpUrl("javascript:alert(1)")).toBeNull();
    expect(httpUrl("not a url")).toBeNull();
  });
});
