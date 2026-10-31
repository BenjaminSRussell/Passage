import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { findAlert } from "../../test-utils";
import { checkAxeAA } from "../../shell/a11y";
import inventory from "./module";

type Handler = (url: URL) => { status: number; body: unknown };
let handler: Handler;
let calls: URL[];

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = new URL(input, "http://localhost");
      calls.push(url);
      const { status, body } = handler(url);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderWorkspace() {
  return render(
    <MemoryRouter initialEntries={["/inventory/workspace"]}>
      <Routes>
        {inventory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const summary = {
  journals: { draft: 4, awaitingApproval: 2 },
  negativeOnHandRows: 1,
  openCountSheets: 3,
  countSheetsCounted: 1,
  reconciliationDifferences: 0,
  lastReconciliationAt: "2026-10-27T02:00:00Z",
  activeReservations: 7,
  transferOrdersByStatus: { CREATED: 0, SHIPPED: 5, PARTIALLY_RECEIVED: 1, RECEIVED: 9, CANCELLED: 0 },
};

describe("Inventory workspace", () => {
  it("shows the tiles and the transfer orders by status", async () => {
    handler = () => ({ status: 200, body: summary });
    const { container } = renderWorkspace();
    const awaiting = await screen.findByText("Journals awaiting approval");
    expect(awaiting.closest("li")).toHaveTextContent("2");
    expect(screen.getByText("Active reservations").closest("li")).toHaveTextContent("7");
    expect(screen.getByText("Partially received").closest("li")).toHaveTextContent("1");
    expect(screen.getByText("Reconciliation differences").closest("li")).toHaveTextContent("Last run 2026-10-27 02:00 UTC");
    expect(screen.getByRole("link", { name: "Journals" })).toHaveAttribute("href", "/inventory/journals");
    expect(screen.getByRole("link", { name: "Negative on-hand" })).toHaveAttribute("href", "/inventory/reports/negative-onhand");
    expect(screen.getByRole("link", { name: "Reconciliation" })).toHaveAttribute("href", "/inventory/reconciliations");
    expect(screen.getByRole("link", { name: "Count sheets" })).toHaveAttribute("href", "/inventory/count-sheets");
    expect(screen.getByRole("link", { name: "Reservations" })).toHaveAttribute("href", "/inventory/reservations");
    expect(screen.getByRole("link", { name: "Transfer orders" })).toHaveAttribute("href", "/inventory/transfer-orders");
    // Some tiles carry work, so there is no "nothing needs attention" note.
    expect(screen.queryByText("Nothing needs attention")).not.toBeInTheDocument();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("says when nothing needs attention and when no reconciliation has run", async () => {
    handler = () => ({
      status: 200,
      body: {
        journals: { draft: 3, awaitingApproval: 0 },
        negativeOnHandRows: 0,
        openCountSheets: 0,
        countSheetsCounted: 0,
        reconciliationDifferences: 0,
        lastReconciliationAt: null,
        activeReservations: 0,
        transferOrdersByStatus: {},
      },
    });
    renderWorkspace();
    expect(await screen.findByText("Nothing needs attention")).toBeInTheDocument();
    expect(screen.getByText("Reconciliation differences").closest("li")).toHaveTextContent("No reconciliation has run");
    // A status the service left out still shows as zero.
    expect(screen.getByText("Shipped").closest("li")).toHaveTextContent("0");
    expect(screen.getByText("Draft journals").closest("li")).toHaveTextContent("3");
  });

  it("scopes to one legal entity and rejects a malformed id", async () => {
    handler = () => ({ status: 200, body: summary });
    renderWorkspace();
    await screen.findByText("Draft journals");
    const input = screen.getByLabelText(/^Legal entity/);
    fireEvent.change(input, { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(calls).toHaveLength(1);
    const id = "9e8d7c6b-0000-4000-8000-000000000003";
    fireEvent.change(input, { target: { value: id } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[1].searchParams.get("legalEntityId")).toBe(id);
  });

  it("reports a failure with a retry", async () => {
    handler = () => ({ status: 500, body: { title: "Server error", status: 500, code: "internal" } });
    renderWorkspace();
    expect(await findAlert()).toHaveTextContent("Could not load the workspace");
    handler = () => ({ status: 200, body: summary });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Draft journals")).toBeInTheDocument();
  });
});
