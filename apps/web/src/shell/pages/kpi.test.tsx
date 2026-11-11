import { render, screen, waitFor, within } from "@testing-library/react";
import { matchPath, MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { loadModules, type PassageModule } from "../../modules/registry";
import { PrincipalProvider } from "../principal";
import { Shell } from "../Shell";
import { json, pageViolations, server, useFakeServer } from "../testing/fakeServer";
import { loadWorkspaceConfig } from "../workspaces/model";
import { HomePage } from "./HomePage";
import { addDays, countRows, KPI_PAGE_SIZE, workspaceKpis, type KpiContext } from "./kpi";
import { WorkspaceKpis } from "./WorkspaceKpis";

useFakeServer();

const ctx: KpiContext = { legalEntityId: "", today: "2026-11-10" };
const rows = (n: number, row: Record<string, unknown> = {}) => Array.from({ length: n }, (_, i) => ({ id: String(i), ...row }));

function renderTiles(workspaceId: string, duties: string[]) {
  return render(
    <MemoryRouter>
      <PrincipalProvider duties={duties}>
        <WorkspaceKpis workspace={{ id: workspaceId, label: "Accounts payable" }} />
      </PrincipalProvider>
    </MemoryRouter>,
  );
}

const tile = (label: string) => screen.getByRole("link", { name: label }).closest("li") as HTMLElement;
const paths = () => server.calls.map((c) => c.url.pathname);

describe("workspace tiles", () => {
  it("shows the accounts payable numbers, each linked to its list", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/payables/payment-proposals") return json(200, { items: rows(2) });
      if (url.searchParams.get("held") === "true") return json(200, { items: rows(3) });
      return json(200, { items: [...rows(4, { status: "OPEN" }), ...rows(1, { status: "PARTIALLY_SETTLED" }), ...rows(2, { status: "SETTLED" })] });
    };
    const { container } = renderTiles("ap-clerk", ["open-item.read", "payment-proposal.read"]);
    await waitFor(() => expect(tile("Invoices on payment hold")).toHaveTextContent("3"));
    expect(tile("Invoices due in the next 7 days")).toHaveTextContent("5");
    expect(tile("Payment proposals to approve")).toHaveTextContent("2");
    expect(screen.getByRole("link", { name: "Invoices on payment hold" })).toHaveAttribute("href", "/payables/open-items");
    expect(screen.getByRole("link", { name: "Payment proposals to approve" })).toHaveAttribute("href", "/payables");
    expect(screen.getByRole("heading", { name: "Accounts payable at a glance" })).toBeInTheDocument();
    const due = server.calls.find((c) => c.url.searchParams.has("due_from"));
    expect(due?.url.searchParams.get("due_to")).toBe(addDays(due?.url.searchParams.get("due_from") ?? "", 6));
    expect(server.calls.find((c) => c.url.pathname.endsWith("/payment-proposals"))?.url.searchParams.get("status")).toBe("DRAFT");
    expect(server.calls.every((c) => c.method === "GET")).toBe(true);
    expect(pageViolations(container)).toEqual([]);
  });

  it("leaves out a tile the person has no duty for, without asking the server for it", async () => {
    server.handler = () => json(200, { items: rows(1) });
    renderTiles("ap-clerk", ["payment-proposal.read"]);
    await waitFor(() => expect(tile("Payment proposals to approve")).toHaveTextContent("1"));
    expect(screen.queryByText("Invoices on payment hold")).not.toBeInTheDocument();
    expect(paths()).toEqual(["/api/v1/payables/payment-proposals"]);
  });

  it("hides a tile when the service refuses the read, and keeps the others", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/payables/payment-proposals" ? json(403, { code: "AUTH-0003", detail: "duty missing" }) : json(200, { items: rows(2, { status: "OPEN" }) }));
    renderTiles("ap-clerk", ["open-item.read", "payment-proposal.read"]);
    await waitFor(() => expect(screen.queryByText("Payment proposals to approve")).not.toBeInTheDocument());
    expect(tile("Invoices on payment hold")).toHaveTextContent("2");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows nothing at all when every read is refused", async () => {
    server.handler = () => json(403, { code: "AUTH-0003", detail: "duty missing" });
    const { container } = renderTiles("controller", ["ledger.journal.read"]);
    await waitFor(() => expect(container).toBeEmptyDOMElement());
  });

  it("says the number is missing when a service fails, without an error message in the way", async () => {
    server.handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderTiles("controller", ["ledger.journal.read"]);
    await waitFor(() => expect(tile("Journals in draft")).toHaveTextContent("This number could not be loaded right now."));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("links a controller's tiles to the journal list filtered by status", async () => {
    server.handler = (url) => json(200, { items: rows(url.searchParams.get("status") === "in_approval" ? 4 : 0) });
    renderTiles("controller", ["ledger.journal.read"]);
    await waitFor(() => expect(tile("Journals waiting for approval")).toHaveTextContent("4"));
    expect(screen.getByRole("link", { name: "Journals waiting for approval" })).toHaveAttribute("href", "/ledger/journal-batches?status=in_approval");
    expect(screen.getByRole("link", { name: "Journals in draft" })).toHaveAttribute("href", "/ledger/journal-batches?status=draft");
    expect(tile("Journals in draft")).toHaveTextContent("0");
  });

  it("marks a count as a floor when the list has more pages", async () => {
    server.handler = () => json(200, { items: rows(KPI_PAGE_SIZE), nextCursor: "next" });
    renderTiles("buyer", ["procurement.purchase-order.inquire"]);
    await waitFor(() => expect(tile("Purchase order lines overdue")).toHaveTextContent(`${KPI_PAGE_SIZE}+`));
    expect(tile("Purchase order lines overdue")).toHaveTextContent("The list has more than were counted.");
  });

  it("reads a list once when two tiles count it, and counts overdue customer invoices from the due date", async () => {
    server.handler = () => json(200, { items: [{ dueDate: "2000-01-31", side: "DEBIT" }, { dueDate: "2000-01-31", side: "CREDIT" }, { dueDate: "2999-01-31", side: "DEBIT" }] });
    renderTiles("ar-clerk", ["receivables.view"]);
    await waitFor(() => expect(tile("Open customer items")).toHaveTextContent("3"));
    expect(tile("Overdue customer invoices")).toHaveTextContent("1");
    expect(paths()).toEqual(["/api/v1/receivables/open-items"]);
  });

  it("renders nothing, and asks nothing, for a workspace without tiles or a person without duties", () => {
    const first = renderTiles("hr-admin", ["open-item.read"]);
    expect(first.container).toBeEmptyDOMElement();
    first.unmount();
    const second = renderTiles("ap-clerk", []);
    expect(second.container).toBeEmptyDOMElement();
    expect(server.calls).toHaveLength(0);
  });

  it("counts list responses the way the services send them", () => {
    const [onHold, dueSoon] = workspaceKpis["ap-clerk"];
    expect(countRows({ items: rows(2) }, onHold, ctx)).toEqual({ value: 2, more: false });
    expect(countRows(rows(3), onHold, ctx)).toEqual({ value: 3, more: false });
    expect(countRows({ items: rows(1), nextCursor: "c" }, onHold, ctx)).toEqual({ value: 1, more: true });
    expect(countRows(null, onHold, ctx)).toEqual({ value: 0, more: false });
    expect(countRows({ items: [{ status: "SETTLED" }, { status: "OPEN" }, null] }, dueSoon, ctx)).toEqual({ value: 1, more: false });
    expect(addDays("2026-12-29", 6)).toBe("2027-01-04");
  });

  it("only names workspaces that ship and screens that exist, and reads lists only", () => {
    const shipped = new Set(loadWorkspaceConfig().workspaces.map((w) => w.id));
    const routes = loadModules().flatMap((m) => m.routes.map((r) => r.path ?? ""));
    for (const [workspace, definitions] of Object.entries(workspaceKpis)) {
      expect(shipped.has(workspace), workspace).toBe(true);
      expect(definitions.length).toBeLessThanOrEqual(4);
      for (const d of definitions) {
        const target = d.to.split("?")[0];
        expect(routes.some((path) => matchPath(path, target) !== null), `${d.id} links to ${d.to}`).toBe(true);
        expect(d.path(ctx), d.id).toMatch(/^\/api\/v1\/[a-z-]+\//);
      }
    }
  });
});

describe("role home with tiles", () => {
  const ledger: PassageModule = { id: "ledger", title: "General ledger", routes: [], nav: [{ label: "Journal batches", to: "/ledger/journal-batches", duty: "ledger.journal.read" }] };

  const onlyController = loadWorkspaceConfig({ hide: loadWorkspaceConfig().workspaces.map((w) => w.id).filter((id) => id !== "controller") });

  it("shows the current workspace's tiles on home", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/ledger/journal-batches" ? json(200, { items: rows(2) }) : undefined);
    render(
      <MemoryRouter>
        <Routes>
          <Route element={<Shell modules={[ledger]} duties={["ledger.journal.read"]} workspaceConfig={onlyController} />}>
            <Route path="/" element={<HomePage />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const main = within(screen.getByRole("main"));
    const tiles = within(await main.findByRole("list", { name: "Accounting and close at a glance" }));
    await waitFor(() => expect(tiles.getByRole("link", { name: "Journals in draft" }).closest("li")).toHaveTextContent("2"));
    expect(tiles.getAllByRole("link")).toHaveLength(3);
  });
});
