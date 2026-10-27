import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { loadModules } from "../registry";
import expense from "./module";

describe("expense module", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("uses its folder name as id and declares routes and nav", () => {
    expect(expense.id).toBe("expense");
    expect(expense.routes.length).toBeGreaterThan(0);
    expect(expense.nav.every((n) => n.to.startsWith("/expense"))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("expense");
  });

  it("gives every nav entry a route and a duty, with card import kept to the admin duty", () => {
    const paths = new Set(expense.routes.map((r) => r.path));
    for (const n of expense.nav) {
      expect(paths.has(n.to)).toBe(true);
      expect(n.duty).toMatch(/^expense\./);
    }
    expect(expense.nav.find((n) => n.to === "/expense/card-feeds/preview")?.duty).toBe("expense.card.import");
    expect(expense.routes.every((r) => r.element != null)).toBe(true);
  });

  it("shows the policy outcome for a line without a receipt", async () => {
    const body = {
      submittable: false,
      results: [
        { lineId: "draft-1", ruleId: "EXP-RCPT-025", ruleVersion: 1, outcome: "REQUIRE_RECEIPT", code: "EXP-REC-001", message: "Receipts are required from 25.00", satisfied: false },
      ],
      totals: { total: "80.00", reimbursable: "80.00", companyPaid: "0.00" },
    };
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(body), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    render(createElement(MemoryRouter, null, expense.routes[0].element));
    fireEvent.click(screen.getByRole("checkbox", { name: "Receipt attached" }));
    fireEvent.click(screen.getByRole("button", { name: "Check line" }));
    const table = await waitFor(() => {
      const t = screen.getByRole("table", { name: "Policy results" });
      expect(t).not.toHaveAttribute("aria-busy", "true");
      return t;
    });
    const row = within(table).getByText("EXP-RCPT-025").closest("tr") as HTMLElement;
    expect(within(row).getByText("Receipt required")).toBeInTheDocument();
    expect(within(row).getByText("Open")).toBeInTheDocument();
    expect(within(row).getByText("EXP-REC-001")).toBeInTheDocument();
    expect(within(row).getByText("Receipts are required from 25.00")).toBeInTheDocument();
    expect(screen.getByText("Cannot submit yet")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/expense/report-evaluations", expect.objectContaining({ method: "POST" }));
    const sent = JSON.parse(String((fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1].body));
    expect(sent.lines[0]).toMatchObject({ category: "MEALS", amount: "80.00", hasReceipt: false });
  });

  it("shows the service problem when the line cannot be checked", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ code: "COM-0500", detail: "internal error" }), { status: 500 })));
    render(createElement(MemoryRouter, null, expense.routes[0].element));
    fireEvent.click(screen.getByRole("button", { name: "Check line" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("internal error (COM-0500)");
    expect(screen.queryByRole("table", { name: "Policy results" })).not.toBeInTheDocument();
  });

  it("refuses an amount that is not a number without calling the service", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    render(createElement(MemoryRouter, null, expense.routes[0].element));
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "eighty" } });
    fireEvent.click(screen.getByRole("button", { name: "Check line" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Enter the amount as a number");
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
