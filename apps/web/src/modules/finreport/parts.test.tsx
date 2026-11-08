import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ColumnSet, RowSet } from "./api/client";
import { header, json, pageViolations, problem, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const BASE = "/api/v1/reporting";

const revenue: RowSet = {
  code: "REV",
  name: "Revenue lines",
  rows: [{ code: "R", kind: "account_range", label: "Revenue", accountFrom: "400000", accountTo: "499999", normalBalance: "credit", format: {} }],
  updatedBy: "u1",
};

const template: ColumnSet = {
  code: "CMP-PY",
  name: "Period against prior year",
  description: "The period, the same period a year earlier, the variance and the variance percent.",
  columns: [
    { code: "C1", label: "Period", periodExpr: "P" },
    { code: "C2", label: "Prior year", periodExpr: "PY" },
    { code: "C3", label: "Period variance", periodExpr: "CALC", calcExpression: "C1-C2" },
    { code: "C4", label: "Period variance %", periodExpr: "CALC", calcExpression: "(C1-C2)/C2*100" },
  ],
};

describe("RowSetsPage", () => {
  it("lists row sets, with an empty state and a load error", async () => {
    server.handler = (url) => (url.pathname === `${BASE}/row-sets` ? json(200, { items: [revenue] }) : undefined);
    const { container, unmount } = renderAt("/finreport/row-sets");
    const table = await findTable({ name: "Row sets" });
    expect(await within(table).findByRole("button", { name: "REV" })).toBeInTheDocument();
    expect(within(table).getByText("Revenue lines")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    unmount();

    server.handler = () => json(200, { items: [] });
    const empty = renderAt("/finreport/row-sets");
    expect(await screen.findByText("No row sets yet")).toBeInTheDocument();
    empty.unmount();

    server.handler = () => problem(403, "AUTH-0003", "missing duty reporting.definition.maintain");
    renderAt("/finreport/row-sets");
    expect(await screen.findByText("Could not load the row sets")).toBeInTheDocument();
    expect(screen.getByText(/AUTH-0003/)).toBeInTheDocument();
  });
});

describe("RowSetEditorPage", () => {
  it("creates a row set without If-Match and moves to its own page", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `${BASE}/row-sets/REV` && init.method === "PUT") return json(200, { ...JSON.parse(String(init.body)), updatedBy: "u1" }, { ETag: '"1"' });
      if (url.pathname === `${BASE}/row-sets/REV`) return json(200, revenue, { ETag: '"1"' });
      return undefined;
    };
    const { container } = renderAt("/finreport/row-sets/new");
    fireEvent.change(await screen.findByLabelText("Row set code"), { target: { value: "rev" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Revenue lines" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Label"), { target: { value: "Revenue" } });
    fireEvent.change(screen.getByLabelText("Account from"), { target: { value: "400000" } });
    fireEvent.change(screen.getByLabelText("Account to"), { target: { value: "499999" } });
    fireEvent.click(screen.getByRole("button", { name: "Save row set" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "GET" && c.url.pathname === `${BASE}/row-sets/REV`)).toBe(true));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put && header(put, "If-Match")).toBeNull();
    expect(put?.body).toMatchObject({ code: "REV", name: "Revenue lines", rows: [{ code: "R1", kind: "account_range", label: "Revenue", accountFrom: "400000", accountTo: "499999" }] });
    expect(await screen.findByLabelText("Row set code")).toHaveAttribute("readonly");
  });

  it("refuses a bad code without calling the service", async () => {
    server.handler = () => undefined;
    renderAt("/finreport/row-sets/new");
    fireEvent.change(await screen.findByLabelText("Row set code"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Save row set" }));
    expect(await screen.findByText(/Use 2 to 30 upper-case letters/)).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("replaces with the ETag it read and offers a reload on a conflict", async () => {
    server.handler = (url, init) => {
      if (url.pathname !== `${BASE}/row-sets/REV`) return undefined;
      if (init.method === "PUT") return problem(412, "conflict_version", "row set revision is 3, not 2");
      return json(200, revenue, { ETag: '"2"' });
    };
    renderAt("/finreport/row-sets/REV");
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Revenue" } });
    fireEvent.click(screen.getByRole("button", { name: "Save row set" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Someone else changed this row set");
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put && header(put, "If-Match")).toBe('"2"');
    expect(within(alert).getByRole("button", { name: "Reload the row set" })).toBeInTheDocument();
  });

  it("explains a service refusal and a missing row set", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `${BASE}/row-sets/REV` && init.method === "PUT") return problem(422, "REPORT-0002", "rows.T: reference X is not a row of this definition");
      if (url.pathname === `${BASE}/row-sets/REV`) return json(200, revenue, { ETag: '"2"' });
      return problem(404, "REPORT-0037", "row set GONE does not exist");
    };
    const first = renderAt("/finreport/row-sets/REV");
    await screen.findByLabelText("Name");
    fireEvent.click(screen.getByRole("button", { name: "Save row set" }));
    expect(await findAlert()).toHaveTextContent("The row set was not saved");
    expect(screen.getByText(/REPORT-0002/)).toBeInTheDocument();
    first.unmount();
    renderAt("/finreport/row-sets/GONE");
    expect(await screen.findByText("This row set does not exist")).toBeInTheDocument();
  });
});

describe("ColumnSetsPage", () => {
  it("lists column sets and the shipped comparative templates", async () => {
    server.handler = (url) => {
      if (url.pathname === `${BASE}/column-sets`) return json(200, { items: [{ ...template, code: "MINE", name: "My comparison" }] });
      if (url.pathname === `${BASE}/column-set-templates`) return json(200, { items: [template] });
      return undefined;
    };
    const { container } = renderAt("/finreport/column-sets");
    const mine = await findTable({ name: "Column sets" });
    expect(await within(mine).findByRole("button", { name: "MINE" })).toBeInTheDocument();
    const templates = await findTable({ name: "Comparative templates" });
    expect(await within(templates).findByText("P PY CALC CALC")).toBeInTheDocument();
    expect(within(templates).getByRole("link", { name: "Copy template CMP-PY" })).toHaveAttribute("href", "/finreport/column-sets/new?template=CMP-PY");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows empty states and load errors for both lists", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/finreport/column-sets");
    expect(await screen.findByText("No column sets yet")).toBeInTheDocument();
    expect(await screen.findByText("No templates")).toBeInTheDocument();
    first.unmount();
    server.handler = () => problem(500, "COM-0500", "down");
    renderAt("/finreport/column-sets");
    expect(await screen.findByText("Could not load the column sets")).toBeInTheDocument();
    expect(await screen.findByText("Could not load the templates")).toBeInTheDocument();
  });
});

describe("ColumnSetEditorPage", () => {
  it("starts a new column set from a template and saves it under the chosen code", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `${BASE}/column-set-templates`) return json(200, { items: [template] });
      if (url.pathname === `${BASE}/column-sets/MINE` && init.method === "PUT") return json(200, JSON.parse(String(init.body)), { ETag: '"1"' });
      if (url.pathname === `${BASE}/column-sets/MINE`) return json(200, { ...template, code: "MINE" }, { ETag: '"1"' });
      return undefined;
    };
    const { container } = renderAt("/finreport/column-sets/new?template=CMP-PY");
    await waitFor(() => expect(screen.getByLabelText("Name")).toHaveValue("Period against prior year"));
    expect(screen.getByLabelText("Column set code")).toHaveValue("");
    expect(screen.getByText("Unsaved changes.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Column set code"), { target: { value: "mine" } });
    fireEvent.click(screen.getByRole("button", { name: "Save column set" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "PUT")).toBe(true));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.body).toMatchObject({ code: "MINE", columns: [{ periodExpr: "P" }, { periodExpr: "PY" }, { periodExpr: "CALC", calcExpression: "C1-C2" }, { periodExpr: "CALC" }] });
    expect(put && header(put, "If-Match")).toBeNull();
  });

  it("sets a dimension filter on period columns only and saves it", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `${BASE}/column-sets/MINE` && init.method === "PUT") return json(200, JSON.parse(String(init.body)), { ETag: '"1"' });
      return undefined;
    };
    renderAt("/finreport/column-sets/new");
    fireEvent.change(await screen.findByLabelText("Column set code"), { target: { value: "mine" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "By cost center" } });
    const filters = screen.getAllByLabelText(/^Dimension filter/);
    expect(filters).toHaveLength(1);
    fireEvent.change(filters[0], { target: { value: "cost_center=CC100, CC200" } });
    fireEvent.blur(filters[0]);
    fireEvent.click(screen.getByRole("button", { name: "Add column" }));
    fireEvent.change(screen.getAllByLabelText(/^Period/)[1], { target: { value: "calc" } });
    // A CALC column reads other columns, so it offers no filter of its own.
    expect(screen.getAllByLabelText(/^Dimension filter/)).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Save column set" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "PUT")).toBe(true));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.body).toMatchObject({ columns: [{ periodExpr: "P", dimensionFilter: { cost_center: ["CC100", "CC200"] } }, { periodExpr: "CALC" }] });
    expect((put?.body as { columns: { dimensionFilter?: unknown }[] }).columns[1].dimensionFilter).toBeUndefined();
  });

  it("sets a balance side on a period column and saves it", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `${BASE}/column-sets/MINE` && init.method === "PUT") return json(200, JSON.parse(String(init.body)), { ETag: '"1"' });
      return undefined;
    };
    renderAt("/finreport/column-sets/new");
    fireEvent.change(await screen.findByLabelText("Column set code"), { target: { value: "mine" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Debit balances" } });
    fireEvent.change(screen.getByLabelText("Balance side"), { target: { value: "debit" } });
    fireEvent.click(screen.getByRole("button", { name: "Add column" }));
    fireEvent.change(screen.getAllByLabelText(/^Period/)[1], { target: { value: "calc" } });
    // A CALC column reads other columns, so it offers no side of its own.
    expect(screen.getAllByLabelText("Balance side")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Save column set" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "PUT")).toBe(true));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.body).toMatchObject({ columns: [{ periodExpr: "P", balanceSide: "debit" }, { periodExpr: "CALC" }] });
  });

  it("reports a template that does not exist", async () => {
    server.handler = (url) => (url.pathname === `${BASE}/column-set-templates` ? json(200, { items: [template] }) : undefined);
    renderAt("/finreport/column-sets/new?template=NOPE");
    expect(await screen.findByText("This column set could not be loaded")).toBeInTheDocument();
    expect(screen.getByText(/There is no template NOPE/)).toBeInTheDocument();
  });
});

const DESIGNER = ["reporting.statement.view", "reporting.definition.maintain"];

describe("Report designer parts picker", () => {
  it("is left out for a viewer without the designer duty, who cannot read the parts", async () => {
    server.handler = () => undefined;
    renderAt("/finreport/definitions/new", ["reporting.statement.view"]);
    expect(await screen.findByText("Rows (1)")).toBeInTheDocument();
    expect(screen.queryByText("Start from saved parts")).not.toBeInTheDocument();
    expect(server.calls.some((c) => c.url.pathname.startsWith(`${BASE}/row-sets`) || c.url.pathname.startsWith(`${BASE}/column-set`))).toBe(false);
  });

  it("replaces the rows with a row set and the columns with a template", async () => {
    server.handler = (url) => {
      if (url.pathname === `${BASE}/row-sets`) return json(200, { items: [revenue] });
      if (url.pathname === `${BASE}/row-sets/REV`) return json(200, revenue, { ETag: '"1"' });
      if (url.pathname === `${BASE}/column-sets`) return json(200, { items: [] });
      if (url.pathname === `${BASE}/column-set-templates`) return json(200, { items: [template] });
      return undefined;
    };
    const { container } = renderAt("/finreport/definitions/new", DESIGNER);
    await screen.findByRole("option", { name: "REV: Revenue lines" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText(/Row set/), { target: { value: "REV" } });
    fireEvent.click(screen.getByRole("button", { name: "Use rows" }));
    expect(await screen.findByText("Rows replaced with the 1 rows of REV.")).toBeInTheDocument();
    expect(screen.getByLabelText("Account from")).toHaveValue("400000");
    fireEvent.change(screen.getByLabelText(/Column set or comparative template/), { target: { value: "CMP-PY" } });
    fireEvent.click(screen.getByRole("button", { name: "Use columns" }));
    expect(await screen.findByText("Columns replaced with the 4 columns of CMP-PY.")).toBeInTheDocument();
    expect(screen.getByText("Columns (4)")).toBeInTheDocument();
  });

  it("warns when the parts cannot be loaded and retries", async () => {
    let up = false;
    server.handler = (url) => {
      if (!up) return problem(500, "COM-0500", "down");
      if (url.pathname === `${BASE}/row-sets` || url.pathname === `${BASE}/column-sets`) return json(200, { items: [] });
      if (url.pathname === `${BASE}/column-set-templates`) return json(200, { items: [template] });
      return undefined;
    };
    renderAt("/finreport/definitions/new", DESIGNER);
    expect(await screen.findByText("Row sets and column sets could not be loaded")).toBeInTheDocument();
    up = true;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("option", { name: "No row sets saved yet" })).toBeInTheDocument();
  });
});
