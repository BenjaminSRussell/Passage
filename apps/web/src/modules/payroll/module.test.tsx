import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { loadModules } from "../registry";
import payroll from "./module";

const statement = {
  ruleSet: "US-FED-2025.1",
  hash: "abc",
  gross: "4000.00",
  taxableWagesFed: "3760.00",
  ficaWages: "4000.00",
  net: "2783.90",
  employerCost: "450.00",
  lines: [
    {
      code: "FED_WH",
      kind: "TAX",
      amount: "504.66",
      basisAmount: "3760.00",
      ruleVersion: "US-FED-2025.1",
      bracket: { schedule: "STANDARD", filingStatus: "SINGLE", over: "54875.00", rate: "0.22", baseTax: "5578.50", adjustedAnnualWage: "89160.00" },
    },
  ],
  arrears: [],
};

describe("payroll module", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("uses its folder name as id and declares routes and nav", () => {
    expect(payroll.id).toBe("payroll");
    expect(payroll.routes.length).toBeGreaterThan(0);
    expect(payroll.nav.every((n) => n.to.startsWith("/payroll"))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("payroll");
  });

  it("previews a statement from the calculation endpoint with the chosen W-4", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => new Response(JSON.stringify(statement), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    render(createElement(MemoryRouter, null, payroll.routes[0].element));
    fireEvent.change(screen.getByRole("combobox", { name: "W-4 filing status" }), { target: { value: "HEAD_OF_HOUSEHOLD" } });
    fireEvent.click(screen.getByRole("checkbox", { name: "W-4 Step 2 checkbox (multiple jobs)" }));
    fireEvent.click(screen.getByRole("button", { name: "Calculate" }));
    expect(await screen.findByText("2783.90")).toBeInTheDocument();
    expect(screen.getByText(/standard schedule, over 54875.00 at 0.22/)).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/payroll/calculations", expect.objectContaining({ method: "POST" }));
    const sent = JSON.parse(String(fetchMock.mock.calls[0][1]?.body));
    expect(sent.w4).toEqual({ filingStatus: "HEAD_OF_HOUSEHOLD", multipleJobs: true });
  });

  it("shows the stable problem code when the rule set is missing", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify({ code: "PAY-RULE-001", detail: "no rule set" }), { status: 422 })),
    );
    render(createElement(MemoryRouter, null, payroll.routes[0].element));
    fireEvent.click(screen.getByRole("button", { name: "Calculate" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("PAY-RULE-001"));
  });
});
