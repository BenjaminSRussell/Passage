import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { CellSummary, Definition, RunSummary } from "./api/client";
import { parseConsolidation, toRunRequest, validateRun, type RunValues } from "./NewRunPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const RUN = "0190f0a0-0000-7000-8000-000000000401";
const A = "0190f0a0-0000-7000-8000-0000000002a1";
const B = "0190f0a0-0000-7000-8000-0000000002b2";
const C = "0190f0a0-0000-7000-8000-0000000002c3";

const values: RunValues = {
  definition: "TB-STD",
  version: "",
  ledgerId: "",
  currencyType: "rpt",
  year: "2026",
  period: "3",
  periodsPerYear: "12",
  firstYear: "2025",
  includeAdjustmentPeriods: false,
  hierarchy: "",
  hierarchyVersion: "",
  normalBalance: "debit",
  accountFrom: "",
  accountTo: "",
  consolidation: `${A}, Plant A\n${B} Plant B\n\n${C}`,
};

const run: RunSummary = {
  id: RUN,
  definitionCode: "TB-STD",
  definitionVersion: 1,
  ledgerId: A,
  currencyType: "rpt",
  periodYear: 2026,
  periodNo: 3,
  status: "completed",
  hwmByLedger: { [A]: 5, [B]: 6 },
  resultHash: "ab".repeat(32),
  cellCount: 3,
  requestedBy: "u1",
  startedAt: "2026-04-02T08:00:00Z",
  consolidation: [
    { ledgerId: A, label: "Plant A" },
    { ledgerId: B, label: "Plant B" },
  ],
};

const expanded: Definition = {
  schemaVersion: "1",
  code: "TB-STD",
  name: "Trial balance",
  version: 1,
  status: "published",
  kind: "trial_balance",
  includeAdjustmentPeriods: false,
  rows: [{ code: "CASH", kind: "account_range", label: "Cash", accountFrom: "1000", accountTo: "1000", normalBalance: "debit", format: {} }],
  columns: [
    { code: "P", label: "Period - Consolidated", periodExpr: "P", currencyType: "rpt" },
    { code: "P_E1", label: "Period - Plant A", periodExpr: "P", currencyType: "rpt" },
    { code: "P_E2", label: "Period - Plant B", periodExpr: "P", currencyType: "rpt" },
  ],
};

const cells: CellSummary[] = [
  { row: "CASH", column: "P", value: "30.00", blank: false, computed: false, keyCount: 2 },
  { row: "CASH", column: "P_E1", value: "10.00", blank: false, computed: false, keyCount: 1 },
  { row: "CASH", column: "P_E2", value: "20.00", blank: false, computed: false, keyCount: 1 },
];

describe("consolidated run request", () => {
  it("parses ledgers with and without labels", () => {
    expect(parseConsolidation(values.consolidation)).toEqual([{ ledgerId: A, label: "Plant A" }, { ledgerId: B, label: "Plant B" }, { ledgerId: C }]);
    expect(parseConsolidation(undefined)).toEqual([]);
  });

  it("sends the ledgers instead of a ledger id", () => {
    const req = toRunRequest(values);
    expect(req.ledgerId).toBeUndefined();
    expect(req.consolidation?.ledgers).toHaveLength(3);
    expect(toRunRequest({ ...values, consolidation: "", ledgerId: A }).consolidation).toBeUndefined();
  });

  it("checks the list before sending", () => {
    expect(validateRun(values)).toEqual({});
    expect(validateRun({ ...values, consolidation: A }).consolidation).toMatch(/2 to 12/);
    expect(validateRun({ ...values, consolidation: `${A}\n${A.toUpperCase()}` }).consolidation).toMatch(/twice/);
    expect(validateRun({ ...values, consolidation: `${A}\nnot-a-uuid` }).consolidation).toMatch(/UUID/);
    expect(validateRun({ ...values, consolidation: `${A} ${"x".repeat(41)}\n${B}` }).consolidation).toMatch(/40/);
    expect(validateRun({ ...values, currencyType: "acct" }).consolidation).toMatch(/reporting currency/);
    expect(validateRun({ ...values, tree: "T" }).consolidation).toMatch(/hierarchy or a reporting tree/);
    expect(validateRun({ ...values, consolidation: "", ledgerId: "" }).ledgerId).toBeDefined();
  });
});

describe("consolidated run pages", () => {
  function pageServer() {
    server.handler = (url, init) => {
      const p = url.pathname;
      if (p === "/api/v1/reporting/definitions") {
        return json(200, { items: [{ code: "TB-STD", name: "Trial balance", kind: "trial_balance", status: "published", latestVersion: 1, hasDraft: false, updatedAt: "" }] });
      }
      if (p === "/api/v1/reporting/hierarchies" || p === "/api/v1/reporting/trees") return json(200, { items: [] });
      if (p === "/api/v1/reporting/runs" && init.method === "POST") return json(201, run);
      if (p === `/api/v1/reporting/runs/${RUN}`) return json(200, run);
      if (p === `/api/v1/reporting/runs/${RUN}/definition`) return json(200, expanded);
      if (p === `/api/v1/reporting/runs/${RUN}/cells`) return json(200, { items: cells });
      return undefined;
    };
  }

  it("runs consolidated and shows a column per entity and the total", async () => {
    pageServer();
    const { container } = renderAt("/finreport/runs/new");
    fireEvent.change(await screen.findByLabelText("Definition"), { target: { value: "TB-STD" } });
    await waitFor(() => expect(screen.getByLabelText("Definition")).toHaveValue("TB-STD"));
    fireEvent.change(screen.getByLabelText("Currency type"), { target: { value: "rpt" } });
    fireEvent.change(screen.getByLabelText("Consolidate by entity"), { target: { value: values.consolidation } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Run statement" }));
    expect(await screen.findByText("Period - Plant A")).toBeInTheDocument();
    expect(screen.getByText("Period - Plant B")).toBeInTheDocument();
    expect(screen.getByText("Period - Consolidated")).toBeInTheDocument();
    expect(screen.getByText(/consolidated by entity over 2 ledgers \(Plant A, Plant B\)/)).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toMatchObject({ definition: "TB-STD", currencyType: "rpt", consolidation: { ledgers: [{ ledgerId: A, label: "Plant A" }, { ledgerId: B, label: "Plant B" }, { ledgerId: C }] } });
    expect((post?.body as { ledgerId?: string }).ledgerId).toBeUndefined();
  });

  it("shows the service's refusal and the form error", async () => {
    pageServer();
    const ok = server.handler;
    server.handler = (url, init) =>
      url.pathname === "/api/v1/reporting/runs" && init.method === "POST"
        ? { status: 404, body: { type: "x", title: "Not found", status: 404, code: "REPORT-0031", detail: `ledger ${C} has no reporting data visible to the caller` } }
        : ok(url, init);
    renderAt("/finreport/runs/new");
    fireEvent.change(await screen.findByLabelText("Consolidate by entity"), { target: { value: A } });
    fireEvent.click(screen.getByRole("button", { name: "Run statement" }));
    expect(await screen.findByText(/List 2 to 12 ledgers/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Definition"), { target: { value: "TB-STD" } });
    fireEvent.change(screen.getByLabelText("Currency type"), { target: { value: "rpt" } });
    fireEvent.change(screen.getByLabelText("Consolidate by entity"), { target: { value: `${A}\n${C}` } });
    fireEvent.click(screen.getByRole("button", { name: "Run statement" }));
    expect(await screen.findByText(/no reporting data visible to the caller \(REPORT-0031\)/)).toBeInTheDocument();
  });
});
