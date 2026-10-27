import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Hierarchy, HierarchySummary } from "./api/client";
import { cleanHierarchy } from "./HierarchyEditorPage";
import { formatRules, parseMembers, parseRules } from "./hierarchyText";
import { header, json, pageViolations, problem, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const summary: HierarchySummary = {
  code: "HIER-BS",
  name: "Balance sheet accounts",
  subject: "main_account",
  strict: true,
  latestVersion: 2,
  hasDraft: true,
  assignedCount: 40,
  unassignedCount: 0,
  updatedAt: "2026-04-01T09:00:00Z",
};

const tree: Hierarchy = {
  schemaVersion: "1",
  code: "HIER-BS",
  name: "Balance sheet accounts",
  version: 0,
  status: "draft",
  subject: "main_account",
  strict: true,
  ownerUserId: "owner-1",
  nodes: [
    { code: "TOTAL", label: "Total" },
    { code: "ASSETS", label: "Assets", parent: "TOTAL", rules: [{ from: "100000", to: "199999" }] },
    { code: "LIAB", label: "Liabilities", parent: "TOTAL", rules: [{ values: ["2*"] }] },
  ],
};

describe("HierarchiesPage", () => {
  it("lists hierarchies with version, members, unassigned count and open draft", async () => {
    server.handler = (url) =>
      url.pathname === "/api/v1/reporting/hierarchies"
        ? json(200, { items: [summary, { ...summary, code: "HIER-CC", subject: "dimension_value", strict: false, hasDraft: false, unassignedCount: 3 }] })
        : undefined;
    const { container } = renderAt("/finreport/hierarchies");
    const table = await findTable({ name: "Derived hierarchies" });
    expect(await within(table).findByRole("button", { name: "HIER-BS" })).toBeInTheDocument();
    expect(within(table).getAllByText("v2")).toHaveLength(2);
    expect(within(table).getByText("3 unassigned")).toBeInTheDocument();
    expect(within(table).getByText("Dimension values")).toBeInTheDocument();
    expect(within(table).getByText("Open draft")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New hierarchy" })).toHaveAttribute("href", "/finreport/hierarchies/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an empty state and a load error", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/finreport/hierarchies");
    expect(await screen.findByText("No hierarchies yet")).toBeInTheDocument();
    first.unmount();
    server.handler = () => problem(403, "AUTH-0003", "missing duty reporting.definition.maintain");
    renderAt("/finreport/hierarchies");
    expect(await screen.findByText("Could not load the hierarchies")).toBeInTheDocument();
    expect(screen.getByText(/AUTH-0003/)).toBeInTheDocument();
  });
});

describe("HierarchyEditorPage", () => {
  it("creates a hierarchy: saves the first draft without If-Match and opens it", async () => {
    let saved: Hierarchy | null = null;
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/reporting/hierarchies/HIER-PL" && init.method === "PUT") {
        saved = { ...(JSON.parse(String(init.body)) as Hierarchy), ownerUserId: "me" };
        return json(200, saved, { ETag: '"1"' });
      }
      if (url.pathname === "/api/v1/reporting/hierarchies/HIER-PL") {
        return url.searchParams.get("version") === "draft" && saved ? json(200, saved, { ETag: '"1"' }) : problem(404, "REPORT-0027", "none");
      }
      if (url.pathname === "/api/v1/reporting/hierarchies/HIER-PL/members") return problem(404, "REPORT-0027", "none");
      return undefined;
    };
    const { container } = renderAt("/finreport/hierarchies/new");
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText(/Use 2 to 30 upper-case letters/)).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Hierarchy code"), { target: { value: "hier-pl" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "P&L accounts" } });
    const leaf = screen.getByRole("group", { name: "Node 2 (leaf)" });
    fireEvent.change(within(leaf).getByLabelText("Code"), { target: { value: "rev" } });
    fireEvent.change(within(leaf).getByLabelText("Label"), { target: { value: "Revenue" } });
    const rules = within(leaf).getByLabelText("Membership rules");
    fireEvent.change(rules, { target: { value: "400000..499999\n7*, 8100" } });
    fireEvent.blur(rules);
    expect(screen.getByRole("button", { name: "Publish" })).toBeDisabled();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByRole("heading", { name: "Derived hierarchy HIER-PL" })).toBeInTheDocument();
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(header(put, "If-Match")).toBeNull();
    expect(put.body).toEqual({
      schemaVersion: "1",
      code: "HIER-PL",
      name: "P&L accounts",
      version: 0,
      status: "draft",
      subject: "main_account",
      strict: true,
      nodes: [
        { code: "TOTAL", label: "Total" },
        { code: "REV", label: "Revenue", parent: "TOTAL", rules: [{ from: "400000", to: "499999" }, { values: ["7*", "8100"] }] },
      ],
    });
  });

  it("saves an open draft with If-Match, publishes it with members and shows the membership", async () => {
    server.handler = (url, init) => {
      const p = url.pathname;
      if (p === "/api/v1/reporting/hierarchies/HIER-BS" && init.method === "PUT") return json(200, JSON.parse(String(init.body)), { ETag: '"5"' });
      if (p === "/api/v1/reporting/hierarchies/HIER-BS") {
        return url.searchParams.get("version") === "draft" ? json(200, tree, { ETag: '"4"' }) : json(200, { ...tree, version: 2, status: "published" });
      }
      if (p === "/api/v1/reporting/hierarchies/HIER-BS/members") return json(200, { version: 2, assigned: { a1: "ASSETS" }, unassigned: [] });
      if (p === "/api/v1/reporting/hierarchies/HIER-BS:publish") {
        return json(200, {
          hierarchy: { ...tree, version: 3, status: "published" },
          membership: { assigned: { "acct-100100": "ASSETS", "acct-200100": "LIAB", "acct-210000": "LIAB" }, unassigned: ["acct-400100"] },
        });
      }
      return undefined;
    };
    const { container } = renderAt("/finreport/hierarchies/HIER-BS");
    expect(await screen.findByText("Membership of version 2")).toBeInTheDocument();
    expect(screen.getByText("v2 published")).toBeInTheDocument();
    expect(within(screen.getByRole("group", { name: "Node 2: Assets (leaf)" })).getByLabelText("Membership rules")).toHaveValue("100000..199999");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "BS accounts" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Draft saved.")).toBeInTheDocument();
    expect(header(server.calls.find((c) => c.method === "PUT")!, "If-Match")).toBe('"4"');

    const members = screen.getByLabelText("Members to classify");
    fireEvent.change(members, { target: { value: "acct-100100\n" } });
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    expect(await screen.findByText(/Line 1: write the member id and key first/)).toBeInTheDocument();
    expect(server.calls.some((c) => c.url.pathname.endsWith(":publish"))).toBe(false);

    fireEvent.change(members, { target: { value: "acct-100100, 100100\nacct-200100, 200100, type=payable\nacct-210000, 210000\nacct-400100, 400100" } });
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    expect(await screen.findByText(/Published version 3. 1 member matched no leaf; the owner is notified./)).toBeInTheDocument();
    const post = server.calls.find((c) => c.url.pathname.endsWith(":publish"))!;
    expect((post.body as { members: unknown[] }).members[1]).toEqual({ id: "acct-200100", key: "200100", attributes: { type: "payable" } });
    expect(screen.getByText("Membership of version 3")).toBeInTheDocument();
    const leaves = screen.getByRole("table", { name: "Members per leaf" });
    const liab = within(leaves).getByText("LIAB").closest("tr")!;
    expect(within(liab).getByText("2")).toBeInTheDocument();
    expect(screen.getByText("acct-400100")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Publish" })).toBeDisabled();
  });

  it("explains a strict publish refused for unassigned members and a stale draft", async () => {
    server.handler = (url, init) => {
      const p = url.pathname;
      if (p === "/api/v1/reporting/hierarchies/HIER-BS" && init.method === "PUT") return problem(412, "conflict_version", "draft revision is 5, not 4");
      if (p === "/api/v1/reporting/hierarchies/HIER-BS") return url.searchParams.get("version") === "draft" ? json(200, tree, { ETag: '"4"' }) : problem(404, "REPORT-0027", "none");
      if (p === "/api/v1/reporting/hierarchies/HIER-BS/members") return problem(404, "REPORT-0027", "none");
      if (p === "/api/v1/reporting/hierarchies/HIER-BS:publish") {
        return problem(422, "REPORT-0013", "1 members are not assigned to any leaf of HIER-BS: acct-400100.");
      }
      return undefined;
    };
    renderAt("/finreport/hierarchies/HIER-BS");
    expect(await screen.findByText(/No published version yet/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Members to classify"), { target: { value: "acct-400100, 400100" } });
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    expect(await screen.findByText("Some members match no leaf")).toBeInTheDocument();
    expect(screen.getByText(/acct-400100\. \(REPORT-0013\)/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Someone else changed this draft")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload the draft" }));
    await waitFor(() => expect(server.calls.filter((c) => c.url.searchParams.get("version") === "draft")).toHaveLength(2));
  });

  it("shows a hierarchy that does not exist and a load failure", async () => {
    server.handler = () => problem(404, "REPORT-0027", "none");
    const first = renderAt("/finreport/hierarchies/NOPE");
    expect(await screen.findByText("This hierarchy does not exist")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to derived hierarchies" })).toBeInTheDocument();
    first.unmount();
    server.handler = () => problem(500, "COM-0500", "database unavailable");
    renderAt("/finreport/hierarchies/HIER-BS");
    expect(await screen.findByText("This hierarchy could not be loaded")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("hierarchy text forms", () => {
  it("parses and formats rules: ranges, values with wildcards and attribute tests", () => {
    const rules = parseRules("100000..199999\n 61*, 6200 \n\nregion=EMEA, APAC\nsegment=10..19");
    expect(rules).toEqual([
      { from: "100000", to: "199999" },
      { values: ["61*", "6200"] },
      { attribute: "region", values: ["EMEA", "APAC"] },
      { attribute: "segment", from: "10", to: "19" },
    ]);
    expect(formatRules(rules)).toBe("100000..199999\n61*, 6200\nregion=EMEA, APAC\nsegment=10..19");
  });

  it("parses members and names the first line it cannot read", () => {
    expect(parseMembers("a1, 1000, type=bank, region=EU\n\na2,2000")).toEqual({
      members: [
        { id: "a1", key: "1000", attributes: { type: "bank", region: "EU" } },
        { id: "a2", key: "2000" },
      ],
    });
    expect(parseMembers("a1, 1000\na2, 2000, bank").error).toBe('Line 2: "bank" is not an attribute=value pair.');
    expect(parseMembers("type=bank, 1000").error).toMatch(/^Line 1: write the member id and key first/);
  });

  it("drops the dimension attribute and empty fields the subject does not use", () => {
    const cleaned = cleanHierarchy({ ...tree, dimensionAttribute: "COST_CENTER", effectiveFrom: "", nodes: [{ code: " A ", label: "A", parent: "", rules: [] }] });
    expect(cleaned).not.toHaveProperty("dimensionAttribute");
    expect(cleaned).not.toHaveProperty("effectiveFrom");
    expect(cleaned.nodes).toEqual([{ code: "A", label: "A" }]);
  });
});
