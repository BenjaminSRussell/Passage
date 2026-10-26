import { fireEvent, render, screen } from "@testing-library/react";
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

  it("shows the policy outcome for a line without a receipt", async () => {
    const body = {
      submittable: false,
      results: [
        { lineId: "draft-1", ruleId: "EXP-RCPT-025", ruleVersion: 1, outcome: "REQUIRE_RECEIPT", code: "EXP-REC-001", message: "Receipt required", satisfied: false },
      ],
      totals: { total: "80.00", reimbursable: "80.00", companyPaid: "0.00" },
    };
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(body), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    render(createElement(MemoryRouter, null, expense.routes[0].element));
    fireEvent.click(screen.getByRole("checkbox", { name: "Receipt attached" }));
    fireEvent.click(screen.getByRole("button", { name: "Check line" }));
    expect(await screen.findByText(/EXP-RCPT-025: REQUIRE_RECEIPT \(open\) - EXP-REC-001/)).toBeInTheDocument();
    expect(screen.getByText("Cannot submit yet")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/expense/report-evaluations", expect.objectContaining({ method: "POST" }));
  });
});
