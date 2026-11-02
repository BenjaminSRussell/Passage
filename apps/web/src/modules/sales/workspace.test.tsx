import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { findTable } from "../../test-utils";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import sales from "./module";
import { conversionText, DUTY_QUOTATION_MAINTAIN, expiryText, workspaceFilter } from "./SalesWorkspacePage";

const LE = "11111111-0000-4000-8000-000000000001";
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

type Reply = { status: number; body: unknown };
let handler: (url: URL, init: RequestInit) => Reply;
let calls: { method: string; url: URL }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url });
      const { status, body } = handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

function renderWorkspace(duties: string[] = ["sales.order.read", DUTY_QUOTATION_MAINTAIN]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={["/sales/workspace"]}>
        <Routes>
          {sales.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const summary = (overrides: Record<string, unknown> = {}) => ({
  activeHolds: 2,
  backorderLines: 5,
  quotationsExpiring: 3,
  quotationsLapsed: 2,
  openIntakeErrors: 7,
  pipeline: [{ currency: "USD", openCount: 4, openNet: "1200", weightedNet: "640.5" }],
  windowDays: 90,
  won: 3,
  lost: 3,
  expired: 1,
  conversionRatePct: "42.9",
  ...overrides,
});

describe("conversionText and expiryText", () => {
  it("formats the rate and the outcome of a run", () => {
    expect(conversionText("42.9")).toBe("42.9%");
    expect(conversionText()).toBe("—");
    expect(expiryText({ expired: [], count: 0, more: false })).toBe("No quotation had passed its validity.");
    expect(expiryText({ expired: [], count: 1, more: false })).toBe("1 quotation expired.");
    expect(expiryText({ expired: [], count: 500, more: true })).toContain("run it again");
  });
});

describe("workspaceFilter", () => {
  const base = { legalEntity: "", windowDays: 90, status: "" as const, minProbability: "", expiringWithin: "" };
  it("leaves unset filters out of the request", () => {
    expect(workspaceFilter(base)).toEqual({
      filter: { legalEntityId: undefined, windowDays: 90, status: undefined, minProbabilityPct: undefined, expiringWithinDays: undefined },
    });
  });
  it("keeps 0 as a minimum probability and refuses one outside 0 to 100", () => {
    expect(workspaceFilter({ ...base, minProbability: "0" })).toMatchObject({ filter: { minProbabilityPct: 0 } });
    for (const bad of ["101", "-1", "12.5", "x"]) {
      expect(workspaceFilter({ ...base, minProbability: bad })).toEqual({ errors: { minProbability: "Enter a whole percentage from 0 to 100." } });
    }
  });
});

describe("Sales workspace", () => {
  it("shows the tiles, the pipeline per currency and the conversion rate", async () => {
    handler = () => ({ status: 200, body: summary() });
    const { container } = renderWorkspace();
    const table = await findTable({ name: "Open quotation pipeline" });
    expect(within(table).getByText("640.5")).toBeInTheDocument();
    expect(screen.getByLabelText("Sales attention")).toHaveTextContent("5");
    expect(screen.getByLabelText("Sales attention")).toHaveTextContent(/Intake errors\s*7/);
    expect(screen.getByLabelText("Conversion over the last 90 days")).toHaveTextContent("42.9%");
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("passes the legal entity and period, and refuses a malformed legal entity", async () => {
    handler = () => ({ status: 200, body: summary() });
    renderWorkspace();
    await findTable({ name: "Open quotation pipeline" });
    fireEvent.change(screen.getByLabelText(/Legal entity/), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(await screen.findByText("Enter a UUID.")).toBeInTheDocument();
    const before = calls.length;
    fireEvent.change(screen.getByLabelText(/Legal entity/), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Conversion period"), { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(calls.length).toBeGreaterThan(before));
    const last = calls.at(-1)!.url;
    expect(last.searchParams.get("legalEntityId")).toBe(LE);
    expect(last.searchParams.get("windowDays")).toBe("30");
    expect(last.searchParams.has("status")).toBe(false);
    expect(last.searchParams.has("minProbabilityPct")).toBe(false);
  });

  it("narrows the pipeline by status, probability and validity", async () => {
    handler = (url) => ({ status: 200, body: url.searchParams.has("status") ? summary({ pipeline: [] }) : summary() });
    renderWorkspace();
    await findTable({ name: "Open quotation pipeline" });
    fireEvent.change(screen.getByLabelText("Pipeline status"), { target: { value: "SENT" } });
    fireEvent.change(screen.getByLabelText(/Minimum probability/), { target: { value: "150" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(await screen.findByText("Enter a whole percentage from 0 to 100.")).toBeInTheDocument();
    const before = calls.length;
    fireEvent.change(screen.getByLabelText(/Minimum probability/), { target: { value: "60" } });
    fireEvent.change(screen.getByLabelText("Valid until"), { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(await screen.findByRole("heading", { name: "No quotations match the filters" })).toBeInTheDocument();
    expect(calls.length).toBe(before + 1);
    const last = calls.at(-1)!.url;
    expect(last.searchParams.get("status")).toBe("SENT");
    expect(last.searchParams.get("minProbabilityPct")).toBe("60");
    expect(last.searchParams.get("expiringWithinDays")).toBe("30");
  });

  it("offers the expiry run only to a holder of the quotation duty", async () => {
    handler = () => ({ status: 200, body: summary() });
    renderWorkspace(["sales.order.read"]);
    expect(await screen.findByText(/needs the sales.quotation.maintain duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Expire lapsed quotations" })).not.toBeInTheDocument();
  });

  it("shows the empty pipeline", async () => {
    handler = () => ({ status: 200, body: summary({ pipeline: [], conversionRatePct: undefined, quotationsLapsed: 0 }) });
    renderWorkspace();
    expect(await screen.findByRole("heading", { name: "No open quotations" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Expire lapsed quotations" })).toBeDisabled();
  });

  it("shows a load failure with a retry", async () => {
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "store unavailable" } });
    renderWorkspace();
    expect(await screen.findByText(/COM-0500/)).toBeInTheDocument();
    handler = () => ({ status: 200, body: summary() });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByLabelText("Sales attention")).toBeInTheDocument();
  });

  it("runs the expiry, reports it and reloads the counts", async () => {
    let lapsed = 2;
    handler = (url) => {
      if (url.pathname.endsWith("/quotations:expire")) return { status: 200, body: { expired: [], count: 2, more: false } };
      return { status: 200, body: summary({ quotationsLapsed: lapsed }) };
    };
    renderWorkspace();
    const button = await screen.findByRole("button", { name: "Expire lapsed quotations" });
    lapsed = 0;
    fireEvent.click(button);
    expect(await screen.findByText("2 quotations expired.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST" && c.url.pathname.endsWith("/quotations:expire"))).toBe(true);
    await waitFor(() => expect(screen.getByRole("button", { name: "Expire lapsed quotations" })).toBeDisabled());
  });

  it("shows a refusal of the expiry run", async () => {
    handler = (url) =>
      url.pathname.endsWith("/quotations:expire")
        ? { status: 403, body: { code: "AUTH-0002", detail: "missing duty" } }
        : { status: 200, body: summary({ quotationsLapsed: 1 }) };
    renderWorkspace();
    fireEvent.click(await screen.findByRole("button", { name: "Expire lapsed quotations" }));
    expect(await screen.findByText(/AUTH-0002/)).toBeInTheDocument();
  });
});
