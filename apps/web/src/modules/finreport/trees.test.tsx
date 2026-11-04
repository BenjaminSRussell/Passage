import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Tree } from "./api/client";
import { toRunRequest, validateRun, type RunValues } from "./NewRunPage";
import { parseValues } from "./TreeEditor";
import { header, json, pageViolations, problem, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const BASE = "/api/v1/reporting";

const tree: Tree = {
  code: "PC-TREE",
  name: "Profit centers",
  attribute: "profit_center",
  units: [
    { code: "ALL", name: "Group" },
    { code: "DE", name: "Germany", parent: "ALL", values: ["PC10", "PC11"] },
    { code: "US", name: "United States", parent: "ALL", values: ["PC30"] },
  ],
  updatedBy: "u1",
};

describe("TreesPage", () => {
  it("lists trees, with an empty state and a load error", async () => {
    server.handler = (url) => (url.pathname === `${BASE}/trees` ? json(200, { items: [tree] }) : undefined);
    const { container, unmount } = renderAt("/finreport/trees");
    const table = await findTable({ name: "Reporting trees" });
    expect(await within(table).findByRole("button", { name: "PC-TREE" })).toBeInTheDocument();
    expect(within(table).getByText("profit_center")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    unmount();

    server.handler = () => json(200, { items: [] });
    const empty = renderAt("/finreport/trees");
    expect(await screen.findByText("No reporting trees yet")).toBeInTheDocument();
    empty.unmount();

    server.handler = () => problem(403, "AUTH-0003", "missing duty reporting.definition.maintain");
    renderAt("/finreport/trees");
    expect(await screen.findByText("Could not load the trees")).toBeInTheDocument();
    expect(screen.getByText(/AUTH-0003/)).toBeInTheDocument();
  });
});

describe("TreeEditorPage", () => {
  it("creates a tree without If-Match with parents and values", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `${BASE}/trees/PC-TREE` && init.method === "PUT") return json(200, { ...JSON.parse(String(init.body)), updatedBy: "u1" }, { ETag: '"1"' });
      if (url.pathname === `${BASE}/trees/PC-TREE`) return json(200, tree, { ETag: '"1"' });
      return undefined;
    };
    const { container } = renderAt("/finreport/trees/new");
    fireEvent.change(await screen.findByLabelText("Tree code"), { target: { value: "pc-tree" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Profit centers" } });
    fireEvent.change(screen.getByLabelText("Dimension attribute"), { target: { value: "profit_center" } });
    fireEvent.click(screen.getByRole("button", { name: "Add unit" }));
    expect(pageViolations(container)).toEqual([]);
    const codes = screen.getAllByLabelText("Code");
    fireEvent.change(codes[0], { target: { value: "all" } });
    fireEvent.change(codes[1], { target: { value: "de" } });
    const names = screen.getAllByLabelText("Unit name");
    fireEvent.change(names[0], { target: { value: "Group" } });
    fireEvent.change(names[1], { target: { value: "Germany" } });
    fireEvent.change(screen.getAllByLabelText("Parent unit")[1], { target: { value: "ALL" } });
    const values = screen.getAllByLabelText(/^Values/);
    fireEvent.change(values[1], { target: { value: "PC10, PC11" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tree" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "GET" && c.url.pathname === `${BASE}/trees/PC-TREE`)).toBe(true));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put && header(put, "If-Match")).toBeNull();
    expect(put?.body).toEqual({
      code: "PC-TREE",
      name: "Profit centers",
      attribute: "profit_center",
      units: [
        { code: "ALL", name: "Group" },
        { code: "DE", name: "Germany", parent: "ALL", values: ["PC10", "PC11"] },
      ],
    });
  });

  it("replaces with the ETag it read and offers a reload on a conflict", async () => {
    server.handler = (url, init) => {
      if (url.pathname !== `${BASE}/trees/PC-TREE`) return undefined;
      if (init.method === "PUT") return problem(412, "conflict_version", "tree revision is 3, not 2");
      return json(200, tree, { ETag: '"2"' });
    };
    renderAt("/finreport/trees/PC-TREE");
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Profit centres" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tree" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Someone else changed this tree");
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put && header(put, "If-Match")).toBe('"2"');
    expect(within(alert).getByRole("button", { name: "Reload the tree" })).toBeInTheDocument();
  });

  it("keeps typed values when Enter saves before the field loses focus, and children follow a renamed parent", async () => {
    server.handler = (url, init) => {
      if (url.pathname !== `${BASE}/trees/PC-TREE`) return undefined;
      if (init.method === "PUT") return json(200, JSON.parse(String(init.body)), { ETag: '"3"' });
      return json(200, tree, { ETag: '"2"' });
    };
    renderAt("/finreport/trees/PC-TREE");
    await screen.findByLabelText("Name");
    fireEvent.change(screen.getAllByLabelText("Code")[0], { target: { value: "group" } });
    const values = screen.getAllByLabelText(/^Values/);
    // A trailing comma survives typing; the value list is current on every keystroke.
    fireEvent.change(values[2], { target: { value: "PC30, " } });
    expect(values[2]).toHaveValue("PC30, ");
    fireEvent.change(values[2], { target: { value: "PC30, PC31" } });
    fireEvent.submit(values[2].closest("form") as HTMLFormElement);
    await waitFor(() => expect(server.calls.some((c) => c.method === "PUT")).toBe(true));
    const put = server.calls.find((c) => c.method === "PUT");
    expect((put?.body as Tree).units).toEqual([
      { code: "GROUP", name: "Group" },
      { code: "DE", name: "Germany", parent: "GROUP", values: ["PC10", "PC11"] },
      { code: "US", name: "United States", parent: "GROUP", values: ["PC30", "PC31"] },
    ]);
  });

  it("removing a unit detaches its children and explains a service refusal", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `${BASE}/trees/PC-TREE` && init.method === "PUT") return problem(422, "REPORT-0039", "units[1].values[0]: value PC10 already belongs to unit US");
      if (url.pathname === `${BASE}/trees/PC-TREE`) return json(200, tree, { ETag: '"2"' });
      return problem(404, "REPORT-0037", "tree GONE does not exist");
    };
    const first = renderAt("/finreport/trees/PC-TREE");
    await screen.findByLabelText("Name");
    fireEvent.click(screen.getByRole("button", { name: "Remove unit 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Save tree" }));
    expect(await findAlert()).toHaveTextContent("The tree was not saved");
    expect(screen.getByText(/REPORT-0039/)).toBeInTheDocument();
    const put = server.calls.find((c) => c.method === "PUT");
    expect((put?.body as Tree).units).toEqual([
      { code: "DE", name: "Germany", values: ["PC10", "PC11"] },
      { code: "US", name: "United States", values: ["PC30"] },
    ]);
    first.unmount();
    renderAt("/finreport/trees/GONE");
    expect(await screen.findByText("This tree does not exist")).toBeInTheDocument();
  });
});

describe("tree runs", () => {
  const base: RunValues = {
    definition: "PL-STD",
    version: "",
    ledgerId: "0190f0a0-0000-7000-8000-000000000201",
    currencyType: "acct",
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
  };

  it("sends the tree and unit, or the whole tree, or nothing", () => {
    expect(validateRun({ ...base, tree: "PC-TREE", treeUnit: "DE" })).toEqual({});
    expect(toRunRequest({ ...base, tree: "PC-TREE", treeUnit: "DE" }).tree).toEqual({ code: "PC-TREE", unit: "DE" });
    expect(toRunRequest({ ...base, tree: "PC-TREE", treeUnit: "" }).tree).toEqual({ code: "PC-TREE" });
    expect(toRunRequest(base).tree).toBeUndefined();
  });

  it("offers the units of the chosen tree on the run form", async () => {
    server.handler = (url) => {
      if (url.pathname === `${BASE}/trees`) return json(200, { items: [tree] });
      if (url.pathname === `${BASE}/definitions` || url.pathname === `${BASE}/hierarchies`) return json(200, { items: [] });
      return undefined;
    };
    renderAt("/finreport/runs/new");
    const select = await screen.findByLabelText(/Limit to reporting tree/);
    await waitFor(() => expect(within(select).getByRole("option", { name: "PC-TREE Profit centers" })).toBeInTheDocument());
    fireEvent.change(select, { target: { value: "PC-TREE" } });
    const unit = await screen.findByLabelText(/Tree unit/);
    expect(within(unit).getAllByRole("option").map((o) => o.textContent)).toEqual(["Whole tree", "ALL Group", "DE Germany", "US United States"]);
  });

  it("parses comma separated values", () => {
    expect(parseValues(" PC10, PC11 ,,PC12 ")).toEqual(["PC10", "PC11", "PC12"]);
    expect(parseValues("")).toEqual([]);
  });
});
