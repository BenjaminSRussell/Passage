import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { CellSummary, Definition, RunRequest, RunSummary } from "./api/client";
import { toRunRequest, validateRun, type RunValues } from "./NewRunPage";
import { header, json, pageViolations, problem, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const RUN = "0190f0a0-0000-7000-8000-000000000101";
const LEDGER = "0190f0a0-0000-7000-8000-000000000201";

const run: RunSummary = {
  id: RUN,
  definitionCode: "PL-STD",
  definitionVersion: 2,
  ledgerId: LEDGER,
  currencyType: "acct",
  periodYear: 2026,
  periodNo: 3,
  status: "completed",
  hwmByLedger: { [LEDGER]: 4711 },
  resultHash: "ab".repeat(32),
  cellCount: 6,
  requestedBy: "u1",
  startedAt: "2026-04-02T08:00:00Z",
  durationMs: 12,
};

const definition: Definition = {
  schemaVersion: "1",
  code: "PL-STD",
  name: "Income statement",
  version: 2,
  status: "published",
  kind: "income_statement",
  includeAdjustmentPeriods: false,
  rows: [
    { code: "REV", kind: "account_range", label: "Revenue", accountFrom: "4000", accountTo: "4999", normalBalance: "credit", format: {} },
    { code: "COGS", kind: "account_range", label: "Cost of sales", accountFrom: "5000", accountTo: "5999", normalBalance: "debit", format: {} },
    { code: "GM", kind: "calculation", label: "Gross margin", expression: "REV-COGS", normalBalance: "credit", format: { bold: true } },
  ],
  columns: [
    { code: "C1", label: "March", periodExpr: "P" },
    { code: "PY", label: "March last year", periodExpr: "PY" },
  ],
};

const cells: CellSummary[] = [
  { row: "REV", column: "C1", value: "1000.000000", blank: false, computed: false, keyCount: 2 },
  { row: "REV", column: "PY", value: "0.000000", blank: true, computed: false, note: "REPORT-0010", keyCount: 0 },
  { row: "COGS", column: "C1", value: "600.000000", blank: false, computed: false, keyCount: 1 },
  { row: "COGS", column: "PY", value: "0.000000", blank: true, computed: false, note: "REPORT-0010", keyCount: 0 },
  { row: "GM", column: "C1", value: "400.000000", blank: false, computed: true, keyCount: 0 },
  { row: "GM", column: "PY", value: "0.000000", blank: true, computed: true, keyCount: 0 },
];

function statementServer(drill: (row: string, column: string) => ReturnType<typeof json>) {
  server.handler = (url) => {
    const p = url.pathname;
    if (p === `/api/v1/reporting/runs/${RUN}`) return json(200, run);
    if (p === `/api/v1/reporting/runs/${RUN}/cells`) return json(200, { items: cells });
    if (p === "/api/v1/reporting/definitions/PL-STD" && url.searchParams.get("version") === "2") return json(200, definition);
    const m = /^\/api\/v1\/reporting\/runs\/[^/]+\/cells\/([^/]+)\/([^/]+)\/balances$/.exec(p);
    if (m) return drill(m[1], m[2]);
    return undefined;
  };
}

describe("RunsPage", () => {
  it("lists the runs newest first and shows the read-model lag", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/runs") return json(200, { items: [run] });
      if (url.pathname === "/api/v1/reporting/lag") return json(200, { items: [{ ledgerId: LEDGER, postingSequence: 4711, updatedAt: "2026-04-02T07:59:00Z" }] });
      return undefined;
    };
    const { container } = renderAt("/finreport");
    const table = await screen.findByRole("table", { name: "Statement runs" });
    expect(await within(table).findByRole("button", { name: "PL-STD v2" })).toBeInTheDocument();
    expect(within(table).getByText("2026-03")).toBeInTheDocument();
    expect(within(table).getByText("abababababab")).toBeInTheDocument();
    const lag = await screen.findByRole("list", { name: "Read-model lag" });
    expect(lag).toHaveTextContent("As of sequence 4711");
    expect(pageViolations(container)).toEqual([]);
  });

  it("filters by definition code", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/runs") return json(200, { items: [] });
      if (url.pathname === "/api/v1/reporting/lag") return json(200, { items: [] });
      return undefined;
    };
    renderAt("/finreport");
    fireEvent.change(screen.getByLabelText("Definition code"), { target: { value: "bs-std" } });
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("definition") === "BS-STD")).toBe(true));
  });

  it("shows empty states when nothing has run yet", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/runs") return json(200, { items: [] });
      if (url.pathname === "/api/v1/reporting/lag") return json(200, { items: [] });
      return undefined;
    };
    renderAt("/finreport");
    expect(await screen.findByText("No statement runs yet")).toBeInTheDocument();
    expect(await screen.findByText("No postings yet")).toBeInTheDocument();
  });

  it("shows the service error and retries", async () => {
    let fail = true;
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/runs") return fail ? problem(500, "COM-0500", "internal error") : json(200, { items: [run] });
      if (url.pathname === "/api/v1/reporting/lag") return json(200, { items: [] });
      return undefined;
    };
    renderAt("/finreport");
    expect(await screen.findByText("Could not load the statement runs")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "PL-STD v2" })).toBeInTheDocument();
  });
});

describe("NewRunPage", () => {
  const values: RunValues = {
    definition: "PL-STD",
    version: "",
    ledgerId: LEDGER,
    currencyType: "rpt",
    year: "2026",
    period: "3",
    periodsPerYear: "12",
    firstYear: "2025",
    includeAdjustmentPeriods: false,
  };

  it("checks the form before sending", () => {
    expect(validateRun(values)).toEqual({});
    expect(validateRun({ ...values, ledgerId: "L1", period: "13", definition: "" })).toEqual({
      definition: "Choose a published definition.",
      ledgerId: "Enter the ledger id (a UUID).",
      period: "Enter a period from 0 to 12.",
    });
    expect(validateRun({ ...values, firstYear: "2027" }).firstYear).toBeDefined();
  });

  it("builds a request whose calendar covers the prior year", () => {
    const req: RunRequest = toRunRequest(values);
    expect(req.period).toEqual({ year: 2026, no: 3, quarter: 1 });
    expect(req.calendar).toHaveLength(26);
    expect(req.definitionVersion).toBeUndefined();
    expect(toRunRequest({ ...values, version: "2" }).definitionVersion).toBe(2);
  });

  it("runs a published definition with an Idempotency-Key and opens the statement", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/reporting/definitions") {
        return json(200, {
          items: [
            { code: "PL-STD", name: "Income statement", kind: "income_statement", latestVersion: 2, hasDraft: false, updatedAt: "2026-04-01T00:00:00Z" },
            { code: "DRAFT-ONLY", name: "Draft", kind: "custom", latestVersion: 0, hasDraft: true, updatedAt: "2026-04-01T00:00:00Z" },
          ],
        });
      }
      if (url.pathname === "/api/v1/reporting/runs" && init.method === "POST") return json(201, run);
      return undefined;
    };
    statementServerFallback();
    const { container } = renderAt("/finreport/runs/new");
    const select = await screen.findByLabelText("Definition");
    await waitFor(() => expect(within(select).getAllByRole("option")).toHaveLength(2));
    expect(within(select).queryByText(/DRAFT-ONLY/)).toBeNull();
    fireEvent.change(select, { target: { value: "PL-STD" } });
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.change(screen.getByLabelText("Fiscal year"), { target: { value: "2026" } });
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("Calendar starts in"), { target: { value: "2025" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Run statement" }));
    expect(await screen.findByRole("heading", { name: "Income statement" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post && header(post, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(post?.body).toMatchObject({ definition: "PL-STD", ledgerId: LEDGER, currencyType: "acct", period: { year: 2026, no: 3, quarter: 1 } });
  });

  it("shows field errors without calling the service, and service errors after", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/reporting/definitions") {
        return json(200, { items: [{ code: "PL-STD", name: "Income statement", kind: "income_statement", latestVersion: 2, hasDraft: false, updatedAt: "" }] });
      }
      if (url.pathname === "/api/v1/reporting/runs" && init.method === "POST") return problem(422, "REPORT-0001", "Definition PL-STD has no published version.");
      return undefined;
    };
    renderAt("/finreport/runs/new");
    fireEvent.click(await screen.findByRole("button", { name: "Run statement" }));
    expect(await screen.findByText("Choose a published definition.")).toBeInTheDocument();
    expect(screen.getByText("Enter the ledger id (a UUID).")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);

    const select = screen.getByLabelText("Definition");
    await waitFor(() => expect(within(select).getAllByRole("option")).toHaveLength(2));
    fireEvent.change(select, { target: { value: "PL-STD" } });
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.click(screen.getByRole("button", { name: "Run statement" }));
    expect(await screen.findByText("The statement did not run")).toBeInTheDocument();
    expect(screen.getByText(/has no published version/)).toBeInTheDocument();
  });
});

/** Adds the run page endpoints behind whatever handler a test installed. */
function statementServerFallback() {
  const first = server.handler;
  statementServer(() => json(404, {}));
  const second = server.handler;
  server.handler = (url, init) => first(url, init) ?? second(url, init);
}

describe("RunPage", () => {
  it("renders the statement grid with labels, amounts and the as-of stamp", async () => {
    statementServer(() => json(404, {}));
    const { container } = renderAt(`/finreport/runs/${RUN}`);
    const table = await screen.findByRole("table", { name: "Statement" });
    expect(within(table).getByRole("columnheader", { name: "March last year" })).toBeInTheDocument();
    expect(within(table).getByRole("button", { name: "Revenue, March: 1,000.00. Show balances" })).toBeInTheDocument();
    expect(within(table).getAllByText("n/a")).toHaveLength(2);
    expect(screen.getByText("4711")).toBeInTheDocument();
    expect(screen.getByText("ab".repeat(32))).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("drills from an amount to the balance keys that sum to it", async () => {
    statementServer((row, column) =>
      json(200, {
        runId: RUN,
        row,
        column,
        value: "1000.000000",
        blank: false,
        computed: false,
        sumOfKeys: "1000.000000",
        keys: [
          { ledgerId: LEDGER, period: "2026-03", account: "4000", combination: "CC100", currencyType: "acct", amount: "700.000000" },
          { ledgerId: LEDGER, period: "2026-03", account: "4100", combination: "CC200", currencyType: "acct", amount: "300.000000" },
        ],
      }),
    );
    renderAt(`/finreport/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: /^Revenue, March/ }));
    await screen.findByText("4100");
    const balances = screen.getByRole("table", { name: "Balances" });
    expect(within(balances).getByText("4100")).toBeInTheDocument();
    expect(within(balances).getByText("700.00")).toBeInTheDocument();
    expect(screen.getByText(/2 balances sum to/)).toBeInTheDocument();
    expect(server.calls.some((c) => c.url.pathname.endsWith("/cells/REV/C1/balances"))).toBe(true);
  });

  it("explains computed cells and warns when the balances do not add up", async () => {
    statementServer((row, column) =>
      row === "GM"
        ? json(200, { runId: RUN, row, column, value: "400.000000", blank: false, computed: true, sumOfKeys: "0.000000", keys: [] })
        : json(200, {
            runId: RUN,
            row,
            column,
            value: "600.000000",
            blank: false,
            computed: false,
            sumOfKeys: "599.990000",
            keys: [{ ledgerId: LEDGER, period: "2026-03", account: "5000", combination: "-", currencyType: "acct", amount: "599.990000" }],
          }),
    );
    renderAt(`/finreport/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: /^Gross margin, March/ }));
    expect(await screen.findByText(/calculated from other rows or columns/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /^Cost of sales, March/ }));
    expect(await screen.findByText("The balances do not add up to this amount")).toBeInTheDocument();
    expect(screen.getByText(/the difference is 0.010000/)).toBeInTheDocument();
  });

  it("shows a missing run as an error", async () => {
    server.handler = () => problem(404, "REPORT-0024", "statement run x does not exist");
    renderAt(`/finreport/runs/${RUN}`);
    expect(await screen.findByText("This statement could not be loaded")).toBeInTheDocument();
    expect(screen.getByText(/REPORT-0024/)).toBeInTheDocument();
  });
});
