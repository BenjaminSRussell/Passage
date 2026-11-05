import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Definition, DefinitionSummary, StandardDefinition } from "./api/client";
import { cleanColumn, cleanRow } from "./DefinitionEditorPage";
import { header, json, pageViolations, problem, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const summary: DefinitionSummary = {
  code: "BS-STD",
  name: "Balance sheet",
  kind: "balance_sheet",
  status: "published",
  latestVersion: 3,
  hasDraft: true,
  updatedAt: "2026-04-01T09:00:00Z",
};

const bs: Definition = {
  schemaVersion: "1",
  code: "BS-STD",
  name: "Balance sheet",
  version: 0,
  status: "draft",
  kind: "balance_sheet",
  includeAdjustmentPeriods: false,
  rows: [
    { code: "CASH", kind: "account_range", label: "Cash", accountFrom: "1000", accountTo: "1099", normalBalance: "debit", format: {} },
    { code: "TA", kind: "total", label: "Total assets", references: ["CASH"], normalBalance: "debit", format: { bold: true } },
  ],
  columns: [{ code: "C1", label: "Closing", periodExpr: "P", currencyType: "rpt" }],
};

const standard: StandardDefinition[] = [
  { code: "TB-STD", name: "Trial balance", kind: "trial_balance", rows: 6, columns: 3 },
  { code: "BS-STD", name: "Balance sheet", kind: "balance_sheet", rows: 6, columns: 3 },
];

describe("DefinitionsPage", () => {
  it("lists definitions with their status, published version and open draft", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/definitions") {
        return json(200, { items: [summary, { ...summary, code: "OLD-PL", status: "retired", latestVersion: 0, hasDraft: false }] });
      }
      if (url.pathname === "/api/v1/reporting/standard-definitions") return json(200, { items: standard });
      return undefined;
    };
    const { container } = renderAt("/finreport/definitions");
    const table = await findTable({ name: "Report definitions" });
    expect(await within(table).findByRole("button", { name: "BS-STD" })).toBeInTheDocument();
    expect(within(table).getByText("v3")).toBeInTheDocument();
    expect(within(table).getByText("Open draft")).toBeInTheDocument();
    expect(within(table).getByText("Published")).toBeInTheDocument();
    expect(within(table).getByText("Retired")).toBeInTheDocument();
    expect(await within(screen.getByRole("table", { name: "Standard definitions" })).findByText("TB-STD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("installs the standard definitions, reports skipped codes and refreshes the list", async () => {
    let installed = false;
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/reporting/definitions") return json(200, { items: installed ? [summary] : [] });
      if (url.pathname === "/api/v1/reporting/standard-definitions") return json(200, { items: standard });
      if (url.pathname === "/api/v1/reporting/standard-definitions:install" && init.method === "POST") {
        installed = true;
        return json(200, {
          items: [
            { code: "TB-STD", action: "installed", version: 1 },
            { code: "BS-STD", action: "skipped", reason: "the code already has a draft version" },
          ],
        });
      }
      return undefined;
    };
    renderAt("/finreport/definitions");
    expect(await screen.findByText("No report definitions yet")).toBeInTheDocument();
    await screen.findByText("TB-STD");
    fireEvent.click(screen.getByRole("button", { name: "Install standard definitions" }));
    expect(await screen.findByText("Installed TB-STD")).toBeInTheDocument();
    expect(screen.getByText(/skipped, the code already has a draft version/)).toBeInTheDocument();
    expect(await within(screen.getByRole("table", { name: "Report definitions" })).findByRole("button", { name: "BS-STD" })).toBeInTheDocument();
  });

  it("explains a refused install", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/definitions") return json(200, { items: [] });
      if (url.pathname === "/api/v1/reporting/standard-definitions") return json(200, { items: standard });
      if (url.pathname === "/api/v1/reporting/standard-definitions:install") return problem(403, "AUTH-0003", "missing duty reporting.definition.maintain");
      return undefined;
    };
    renderAt("/finreport/definitions");
    await screen.findByText("TB-STD");
    fireEvent.click(screen.getByRole("button", { name: "Install standard definitions" }));
    expect(await screen.findByText("The standard definitions were not installed")).toBeInTheDocument();
    expect(screen.getByText(/missing duty reporting.definition.maintain/)).toBeInTheDocument();
  });

  it("shows an empty state and a load error", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/finreport/definitions");
    expect(await screen.findByText("No report definitions yet")).toBeInTheDocument();
    first.unmount();
    server.handler = () => problem(403, "AUTH-0003", "missing duty reporting.statement.view");
    renderAt("/finreport/definitions");
    expect(await screen.findByText("Could not load the report definitions")).toBeInTheDocument();
    expect(await screen.findByText("Could not load the standard definitions")).toBeInTheDocument();
    expect(screen.getAllByText(/AUTH-0003/)).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Install standard definitions" })).toBeDisabled();
  });

  it("imports pasted JSON, refusing a duplicate code until overwrite is ticked", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/definitions") return json(200, { items: [] });
      if (url.pathname === "/api/v1/reporting/definitions/import") {
        return url.searchParams.get("overwrite") === "true"
          ? json(201, bs, { ETag: '"1"' })
          : problem(409, "REPORT-0011", "Definition code BS-STD exists; use overwrite to replace.");
      }
      if (url.pathname === "/api/v1/reporting/definitions/BS-STD") return json(200, bs, { ETag: '"1"' });
      return undefined;
    };
    renderAt("/finreport/definitions");
    const json_ = screen.getByLabelText("Definition JSON");
    fireEvent.change(json_, { target: { value: "not json" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(await screen.findByText("Paste the JSON of an exported definition.")).toBeInTheDocument();

    fireEvent.change(json_, { target: { value: JSON.stringify(bs) } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(await screen.findByText(/use overwrite to replace/)).toBeInTheDocument();
    const firstKey = header(server.calls.filter((c) => c.method === "POST").at(-1)!, "Idempotency-Key");

    fireEvent.click(screen.getByLabelText("Replace an existing definition with the same code"));
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(await screen.findByRole("heading", { name: "Report definition BS-STD" })).toBeInTheDocument();
    const posts = server.calls.filter((c) => c.method === "POST");
    expect(posts.at(-1)?.body).toMatchObject({ code: "BS-STD", rows: bs.rows });
    expect(header(posts.at(-1)!, "Idempotency-Key")).not.toBe(firstKey);
  });
});

describe("DefinitionEditorPage", () => {
  it("creates a new definition: saves a draft without If-Match and opens it", async () => {
    let saved: Definition | null = null;
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/reporting/definitions/PL-NEW" && init.method === "PUT") {
        saved = JSON.parse(String(init.body)) as Definition;
        return json(200, saved, { ETag: '"1"' });
      }
      if (url.pathname === "/api/v1/reporting/definitions/PL-NEW") {
        return url.searchParams.get("version") === "draft" && saved ? json(200, saved, { ETag: '"1"' }) : problem(404, "REPORT-0020", "no published version");
      }
      return undefined;
    };
    const { container } = renderAt("/finreport/definitions/new");
    fireEvent.change(screen.getByLabelText("Definition code"), { target: { value: "pl-new" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "New P&L" } });
    fireEvent.change(screen.getByLabelText("Statement kind"), { target: { value: "income_statement" } });
    const row1 = screen.getByRole("group", { name: "Row 1" });
    fireEvent.change(within(row1).getByLabelText("Label"), { target: { value: "Revenue" } });
    fireEvent.change(within(row1).getByLabelText("Account from"), { target: { value: "4000" } });
    fireEvent.change(within(row1).getByLabelText("Account to"), { target: { value: "4999" } });
    fireEvent.change(within(row1).getByLabelText("Normal balance"), { target: { value: "credit" } });
    fireEvent.click(screen.getByRole("button", { name: "Add row" }));
    const row2 = screen.getByRole("group", { name: "Row 2" });
    fireEvent.change(within(row2).getByLabelText("Kind"), { target: { value: "calculation" } });
    fireEvent.change(within(row2).getByLabelText("Label"), { target: { value: "Doubled" } });
    fireEvent.change(within(row2).getByLabelText("Expression"), { target: { value: "R1*2" } });
    expect(pageViolations(container)).toEqual([]);
    expect(screen.getByRole("button", { name: "Publish" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByRole("heading", { name: "Report definition PL-NEW" })).toBeInTheDocument();
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(header(put, "If-Match")).toBeNull();
    expect(put.body).toMatchObject({
      code: "PL-NEW",
      kind: "income_statement",
      rows: [
        { code: "R1", kind: "account_range", label: "Revenue", accountFrom: "4000", accountTo: "4999", normalBalance: "credit" },
        { code: "R2", kind: "calculation", label: "Doubled", expression: "R1*2" },
      ],
    });
    expect((put.body as Definition).rows[1]).not.toHaveProperty("accountFrom");
  });

  it("edits an open draft with If-Match, publishes it and offers a run", async () => {
    server.handler = (url, init) => {
      const p = url.pathname;
      if (p === "/api/v1/reporting/definitions/BS-STD" && init.method === "PUT") return json(200, JSON.parse(String(init.body)), { ETag: '"5"' });
      if (p === "/api/v1/reporting/definitions/BS-STD") {
        return url.searchParams.get("version") === "draft" ? json(200, bs, { ETag: '"4"' }) : json(200, { ...bs, version: 3, status: "published" });
      }
      if (p === "/api/v1/reporting/definitions/BS-STD:publish") return json(200, { ...bs, name: "Balance sheet v4", version: 4, status: "published" });
      return undefined;
    };
    renderAt("/finreport/definitions/BS-STD");
    const name = await screen.findByLabelText("Name");
    expect(screen.getByText("Draft")).toBeInTheDocument();
    expect(screen.getByText("v3 published")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Run this statement" })).toHaveAttribute("href", "/finreport/runs/new?definition=BS-STD");
    const total = screen.getByRole("group", { name: "Row 2: Total assets" });
    expect(within(total).getByLabelText("Rows to total")).toHaveValue("CASH");

    fireEvent.change(name, { target: { value: "Balance sheet v4" } });
    expect(screen.getByRole("button", { name: "Publish" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Draft saved.")).toBeInTheDocument();
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(header(put, "If-Match")).toBe('"4"');

    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    expect(await screen.findByText("Published version 4. Runs can use it now.")).toBeInTheDocument();
    expect(screen.getByText("v4 published")).toBeInTheDocument();
  });

  it("retires a published definition after confirmation", async () => {
    server.handler = (url, init) => {
      const p = url.pathname;
      if (p === "/api/v1/reporting/definitions/BS-STD:retire" && init.method === "POST") return json(200, { code: "BS-STD", status: "retired", retiredVersions: 3 });
      if (p === "/api/v1/reporting/definitions/BS-STD") {
        return url.searchParams.get("version") === "draft" ? problem(404, "REPORT-0020", "no draft") : json(200, { ...bs, version: 3, status: "published" });
      }
      return undefined;
    };
    const { container } = renderAt("/finreport/definitions/BS-STD");
    fireEvent.click(await screen.findByRole("button", { name: "Retire" }));
    expect(screen.getByText("Retire BS-STD?")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Keep it" }));
    expect(server.calls.some((c) => c.url.pathname.endsWith(":retire"))).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "Retire" }));
    fireEvent.click(screen.getByRole("button", { name: "Retire definition" }));
    expect(await screen.findByText(/Retired BS-STD. New runs can no longer use it/)).toBeInTheDocument();
    expect(screen.getByText("v3 retired")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Run this statement" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Retire" })).toBeNull();
  });

  it("shows a retired definition without the run link", async () => {
    server.handler = (url) => {
      if (url.pathname !== "/api/v1/reporting/definitions/OLD-PL") return undefined;
      return url.searchParams.get("version") === "draft" ? problem(404, "REPORT-0020", "no draft") : json(200, { ...bs, code: "OLD-PL", version: 2, status: "retired" });
    };
    renderAt("/finreport/definitions/OLD-PL");
    expect(await screen.findByText("v2 retired")).toBeInTheDocument();
    expect(screen.getByText(/Saving starts a draft from retired version 2/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Run this statement" })).toBeNull();
    expect(screen.getByRole("button", { name: "Export JSON" })).toBeInTheDocument();
  });

  it("starts from the latest published version when no draft is open", async () => {
    server.handler = (url) => {
      if (url.pathname !== "/api/v1/reporting/definitions/BS-STD") return undefined;
      return url.searchParams.get("version") === "draft" ? problem(404, "REPORT-0020", "no draft") : json(200, { ...bs, version: 3, status: "published" });
    };
    renderAt("/finreport/definitions/BS-STD");
    expect(await screen.findByText(/Saving starts a draft from published version 3/)).toBeInTheDocument();
    expect(screen.getByLabelText("Definition code")).toHaveValue("BS-STD");
    expect(screen.getByRole("button", { name: "Publish" })).toBeDisabled();
  });

  it("reports a stale draft and validation findings", async () => {
    server.handler = (url, init) => {
      const p = url.pathname;
      if (p === "/api/v1/reporting/definitions/BS-STD" && init.method === "PUT") return problem(412, "conflict_version", "draft revision is 5, not 4");
      if (p === "/api/v1/reporting/definitions/BS-STD") return url.searchParams.get("version") === "draft" ? json(200, bs, { ETag: '"4"' }) : problem(404, "REPORT-0020", "none");
      if (p === "/api/v1/reporting/definitions/BS-STD:validate") {
        return json(200, { valid: false, issues: [{ field: "rows.TA", code: "REPORT-0002", detail: "reference X is not a row of this definition" }] });
      }
      return undefined;
    };
    renderAt("/finreport/definitions/BS-STD");
    await screen.findByLabelText("Name");
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(await screen.findByText("1 problem to fix")).toBeInTheDocument();
    expect(screen.getByText(/reference X is not a row/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Someone else changed this draft")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload the draft" }));
    await waitFor(() => expect(server.calls.filter((c) => c.url.searchParams.get("version") === "draft")).toHaveLength(2));
  });

  it("shows a definition that does not exist", async () => {
    server.handler = () => problem(404, "REPORT-0020", "none");
    renderAt("/finreport/definitions/NOPE");
    expect(await screen.findByText("This definition does not exist")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to report definitions" })).toBeInTheDocument();
  });

  it("sends only the fields each row and column kind uses", () => {
    expect(cleanRow({ code: " T ", kind: "total", label: "T", accountFrom: "1", expression: "x", references: ["A"], normalBalance: "debit", format: {} })).toEqual({
      code: "T",
      kind: "total",
      label: "T",
      references: ["A"],
      normalBalance: "debit",
      format: {},
    });
    expect(cleanColumn({ code: "V", label: "Variance", periodExpr: "CALC", currencyType: "acct", calcExpression: "C1-C2" })).toEqual({
      code: "V",
      label: "Variance",
      periodExpr: "CALC",
      calcExpression: "C1-C2",
    });
    expect(cleanColumn({ code: "C1", label: "P", periodExpr: "P", calcExpression: "x" })).toEqual({ code: "C1", label: "P", periodExpr: "P" });
    // A column dimension filter is kept on a period column, dropped when empty or on a CALC column.
    const filter = { cost_center: ["CC100", "CC200"] };
    expect(cleanColumn({ code: "C1", label: "P", periodExpr: "P", dimensionFilter: filter }).dimensionFilter).toEqual(filter);
    expect(cleanColumn({ code: "C1", label: "P", periodExpr: "P", dimensionFilter: {} })).toEqual({ code: "C1", label: "P", periodExpr: "P" });
    expect(cleanColumn({ code: "V", label: "V", periodExpr: "CALC", calcExpression: "C1", dimensionFilter: filter }).dimensionFilter).toBeUndefined();
  });
});
