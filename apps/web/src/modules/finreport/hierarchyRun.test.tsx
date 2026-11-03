import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { CellSummary, Definition, HierarchySummary, RunSummary } from "./api/client";
import { toRunRequest, validateRun, type RunValues } from "./NewRunPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const RUN = "0190f0a0-0000-7000-8000-000000000301";
const LEDGER = "0190f0a0-0000-7000-8000-000000000201";

const values: RunValues = {
  definition: "TB-STD",
  version: "",
  ledgerId: LEDGER,
  currencyType: "acct",
  year: "2026",
  period: "3",
  periodsPerYear: "12",
  firstYear: "2025",
  includeAdjustmentPeriods: false,
  hierarchy: "ACCT-GRP",
  hierarchyVersion: "",
  normalBalance: "debit",
  accountFrom: "",
  accountTo: "",
};

const hierarchies: HierarchySummary[] = [
  { code: "ACCT-GRP", name: "Account groups", subject: "main_account", strict: false, latestVersion: 2, hasDraft: false, assignedCount: 3, unassignedCount: 1, updatedAt: "2026-04-01T00:00:00Z" },
  { code: "REGIONS", name: "Regions", subject: "dimension_value", strict: true, latestVersion: 1, hasDraft: false, assignedCount: 5, unassignedCount: 0, updatedAt: "2026-04-01T00:00:00Z" },
  { code: "CATS", name: "Categories", subject: "main_account_category", strict: true, latestVersion: 1, hasDraft: false, assignedCount: 5, unassignedCount: 0, updatedAt: "2026-04-01T00:00:00Z" },
  { code: "NEW-ONE", name: "Never published", subject: "main_account", strict: true, latestVersion: 0, hasDraft: true, assignedCount: 0, unassignedCount: 0, updatedAt: "2026-04-01T00:00:00Z" },
];

const run: RunSummary = {
  id: RUN,
  definitionCode: "TB-STD",
  definitionVersion: 1,
  ledgerId: LEDGER,
  currencyType: "acct",
  periodYear: 2026,
  periodNo: 3,
  status: "completed",
  hwmByLedger: { [LEDGER]: 77 },
  resultHash: "cd".repeat(32),
  cellCount: 4,
  requestedBy: "u1",
  startedAt: "2026-04-02T08:00:00Z",
  hierarchy: { code: "ACCT-GRP", version: 2 },
  checks: [{ code: "REPORT-0034", severity: "warning", detail: "1 members have no node in ACCT-GRP version 2 and are shown in the Unassigned row: 9999." }],
};

const expanded: Definition = {
  schemaVersion: "1",
  code: "TB-STD",
  name: "Trial balance by Account groups",
  version: 1,
  status: "published",
  kind: "trial_balance",
  includeAdjustmentPeriods: false,
  rows: [
    { code: "CASH", kind: "account_range", label: "Cash", accounts: ["1000"], normalBalance: "debit", format: { indent: 1 } },
    { code: "ASSETS", kind: "total", label: "Assets", references: ["CASH"], normalBalance: "debit", format: { bold: true } },
    { code: "UNASSIGNED", kind: "account_range", label: "Unassigned", accounts: ["9999"], normalBalance: "debit", format: {} },
    { code: "TOTAL", kind: "total", label: "Total", references: ["ASSETS", "UNASSIGNED"], normalBalance: "debit", format: { bold: true, printZero: true } },
  ],
  columns: [{ code: "C1", label: "Closing", periodExpr: "P" }],
};

const cells: CellSummary[] = [
  { row: "CASH", column: "C1", value: "400.000000", blank: false, computed: false, keyCount: 1 },
  { row: "ASSETS", column: "C1", value: "400.000000", blank: false, computed: false, keyCount: 1 },
  { row: "UNASSIGNED", column: "C1", value: "-50.000000", blank: false, computed: false, keyCount: 1 },
  { row: "TOTAL", column: "C1", value: "350.000000", blank: false, computed: false, keyCount: 2 },
];

describe("run request over a hierarchy", () => {
  it("sends the hierarchy only when one is chosen", () => {
    expect(toRunRequest({ ...values, hierarchy: "" }).hierarchy).toBeUndefined();
    expect(toRunRequest(values).hierarchy).toEqual({ code: "ACCT-GRP" });
    expect(toRunRequest({ ...values, hierarchyVersion: "2", normalBalance: "credit", accountFrom: " 6000", accountTo: "6999 " }).hierarchy).toEqual({
      code: "ACCT-GRP",
      version: 2,
      normalBalance: "credit",
      accountFrom: "6000",
      accountTo: "6999",
    });
  });

  it("checks the hierarchy fields", () => {
    expect(validateRun(values)).toEqual({});
    expect(validateRun({ ...values, hierarchyVersion: "0" }).hierarchyVersion).toBeDefined();
    expect(validateRun({ ...values, accountFrom: "6000" }).accountTo).toBe("Enter both ends of the account range, or neither.");
    expect(validateRun({ ...values, accountFrom: "7000", accountTo: "6000" }).accountTo).toBe("The first account must not come after the last.");
    expect(validateRun({ ...values, hierarchy: "", hierarchyVersion: "x" })).toEqual({});
  });
});

describe("NewRunPage with hierarchies", () => {
  function formServer() {
    server.handler = (url, init) => {
      const p = url.pathname;
      if (p === "/api/v1/reporting/definitions") {
        return json(200, { items: [{ code: "TB-STD", name: "Trial balance", kind: "trial_balance", status: "published", latestVersion: 1, hasDraft: false, updatedAt: "" }] });
      }
      if (p === "/api/v1/reporting/hierarchies") return json(200, { items: hierarchies });
      if (p === "/api/v1/reporting/runs" && init.method === "POST") return json(201, run);
      if (p === `/api/v1/reporting/runs/${RUN}`) return json(200, run);
      if (p === `/api/v1/reporting/runs/${RUN}/definition`) return json(200, expanded);
      if (p === `/api/v1/reporting/runs/${RUN}/cells`) return json(200, { items: cells });
      return undefined;
    };
  }

  it("offers published account and dimension hierarchies only, and runs with the choice", async () => {
    formServer();
    const { container } = renderAt("/finreport/runs/new");
    const select = await screen.findByLabelText("Rows from hierarchy");
    await waitFor(() => expect(within(select).getAllByRole("option")).toHaveLength(3));
    expect(within(select).queryByText(/CATS/)).toBeNull();
    expect(within(select).queryByText(/NEW-ONE/)).toBeNull();
    expect(screen.queryByLabelText("First account")).toBeNull();

    fireEvent.change(select, { target: { value: "REGIONS" } });
    expect(await screen.findByLabelText("First account")).toBeInTheDocument();
    fireEvent.change(select, { target: { value: "ACCT-GRP" } });
    expect(screen.queryByLabelText("First account")).toBeNull();
    fireEvent.change(await screen.findByLabelText("Amounts shown as"), { target: { value: "credit" } });

    fireEvent.change(screen.getByLabelText("Definition"), { target: { value: "TB-STD" } });
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "3" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Run statement" }));
    expect(await screen.findByRole("heading", { name: "Trial balance by Account groups" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toMatchObject({ definition: "TB-STD", hierarchy: { code: "ACCT-GRP", normalBalance: "credit" } });
  });

  it("keeps the form usable when the hierarchies cannot be loaded", async () => {
    formServer();
    const ok = server.handler;
    server.handler = (url, init) =>
      url.pathname === "/api/v1/reporting/hierarchies" ? { status: 403, body: { type: "x", title: "Forbidden", status: 403, code: "AUTH-0003", detail: "no" } } : ok(url, init);
    renderAt("/finreport/runs/new");
    expect(await screen.findByText(/The hierarchies could not be loaded \(AUTH-0003\)/)).toBeInTheDocument();
    expect(screen.getByLabelText("Rows from hierarchy")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Run statement" })).toBeEnabled();
  });
});

describe("RunPage of a hierarchy run", () => {
  it("labels the rows with the hierarchy nodes, names the hierarchy version and shows the unassigned warning", async () => {
    server.handler = (url) => {
      const p = url.pathname;
      if (p === `/api/v1/reporting/runs/${RUN}`) return json(200, run);
      if (p === `/api/v1/reporting/runs/${RUN}/definition`) return json(200, expanded);
      if (p === `/api/v1/reporting/runs/${RUN}/cells`) return json(200, { items: cells });
      return undefined;
    };
    const { container } = renderAt(`/finreport/runs/${RUN}`);
    const table = await findTable({ name: "Statement" });
    expect(within(table).getByRole("button", { name: "Cash, Closing: 400.00. Show balances" })).toBeInTheDocument();
    expect(within(table).getByRole("button", { name: "Unassigned, Closing: -50.00. Show balances" })).toBeInTheDocument();
    expect(within(table).getByRole("button", { name: "Total, Closing: 350.00. Show balances" })).toBeInTheDocument();
    expect(screen.getByText(/rows from hierarchy ACCT-GRP version 2/)).toBeInTheDocument();
    expect(screen.getByText("Some members are not in the hierarchy")).toBeInTheDocument();
    expect(server.calls.some((c) => c.url.pathname.includes("/definitions/"))).toBe(false);
    expect(pageViolations(container)).toEqual([]);
  });
});
