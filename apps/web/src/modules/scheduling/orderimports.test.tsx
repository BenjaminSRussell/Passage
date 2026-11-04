import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { OrderImport, Run } from "./api/client";
import { runSummary } from "./OrderImportDetailPage";
import scheduling from "./module";

const ORDER = "0d000000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };
type Handler = (url: URL) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

let handler: Handler;
let calls: URL[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = new URL(input, "http://localhost");
      calls.push(url);
      const reply = handler(url) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {scheduling.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const partial: OrderImport = {
  orderId: ORDER,
  orderNumber: "PO-100",
  orderVersion: 4,
  reason: "redate",
  eventId: "e1",
  status: "partial",
  operationsTotal: 3,
  created: 1,
  updated: 1,
  unchanged: 0,
  removed: 0,
  keptFirmed: 0,
  skipped: [{ operNumber: 30, code: "unknown-resource", detail: "no resource of the operation is a scheduling resource" }],
  replanRequired: true,
  orderStart: "2031-03-03T06:00:00Z",
  orderEnd: "2031-03-03T18:00:00Z",
  receivedAt: "2031-03-03T06:05:00Z",
  reimportable: true,
  withdrawnAt: null,
  withdrawnTo: null,
};

describe("Production orders received", () => {
  it("lists the orders with their outcome and filters by result", async () => {
    handler = (url) => ({ status: 200, body: { items: url.searchParams.get("status") === "rejected" ? [] : [partial] } });
    const { container } = renderAt("/scheduling/order-imports");
    const table = await findTable({ name: "Production orders received" });
    expect(table).toHaveTextContent("PO-100");
    expect(table).toHaveTextContent("Partly imported");
    expect(table).toHaveTextContent("Schedule again");
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);

    fireEvent.change(screen.getByLabelText("Result"), { target: { value: "rejected" } });
    expect(await screen.findByText("No orders with that result")).toBeInTheDocument();
    expect(calls.some((c) => c.searchParams.get("status") === "rejected")).toBe(true);
  });

  it("explains an empty list and a failure with retry", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const first = renderAt("/scheduling/order-imports");
    expect(await screen.findByText("No production orders received yet")).toBeInTheDocument();
    first.unmount();

    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "internal error" } });
    renderAt("/scheduling/order-imports");
    expect(await findAlert()).toHaveTextContent("COM-0500");
    handler = () => ({ status: 200, body: { items: [partial] } });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Production orders received" })).toBeInTheDocument();
  });

  it("opens an order to show what was left out and offers a new run", async () => {
    handler = () => ({ status: 200, body: partial });
    const { container } = renderAt(`/scheduling/order-imports/${ORDER}`);
    expect(await screen.findByText("Production order PO-100")).toBeInTheDocument();
    const table = await findTable({ name: "Operations not registered" });
    expect(within(table).getByText("Resource not in scheduling")).toBeInTheDocument();
    expect(screen.getByText("Schedule this order again")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Start run" })).toHaveAttribute("href", "/scheduling/runs/new");
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("shows an order with nothing left out and an unknown order as an error", async () => {
    handler = () => ({ status: 200, body: { ...partial, status: "imported", skipped: [], replanRequired: false } });
    const first = renderAt(`/scheduling/order-imports/${ORDER}`);
    expect(await screen.findByText("Nothing left out")).toBeInTheDocument();
    expect(screen.queryByText("Schedule this order again")).not.toBeInTheDocument();
    first.unmount();

    handler = () => ({ status: 404, body: { code: "SCHED-0131", detail: "no production order with that id has been received" } });
    renderAt(`/scheduling/order-imports/${ORDER}`);
    expect(await findAlert()).toHaveTextContent("SCHED-0131");
  });

  it("imports a received order again after a resource was mapped", async () => {
    const done = { ...partial, status: "imported", created: 2, skipped: [], replanRequired: false };
    handler = (url) => (url.pathname.endsWith(":reimport") ? { status: 200, body: done } : { status: 200, body: partial });
    renderAt(`/scheduling/order-imports/${ORDER}`);
    expect(await screen.findByText("Some operations name a resource scheduling does not know")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Import again" }));
    expect(await screen.findByText("The order was imported again: 2 created, 1 updated, 0 unchanged, 0 left out.")).toBeInTheDocument();
    expect(calls.some((c) => c.pathname.endsWith(`/order-imports/${ORDER}:reimport`))).toBe(true);
    expect(screen.getByText("Nothing left out")).toBeInTheDocument();
  });

  it("shows why an import again failed, and offers no button for an order that was not kept", async () => {
    handler = (url) =>
      url.pathname.endsWith(":reimport")
        ? { status: 409, body: { code: "SCHED-0133", detail: "the received order was not kept" } }
        : { status: 200, body: partial };
    const first = renderAt(`/scheduling/order-imports/${ORDER}`);
    fireEvent.click(await screen.findByRole("button", { name: "Import again" }));
    expect(await findAlert()).toHaveTextContent("SCHED-0133");
    first.unmount();

    handler = () => ({ status: 200, body: { ...partial, reimportable: false } });
    renderAt(`/scheduling/order-imports/${ORDER}`);
    expect(await screen.findByText(/received before scheduling kept events/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Import again" })).not.toBeInTheDocument();
  });

  it("explains an order production withdrew from schedule", async () => {
    const withdrawn: OrderImport = { ...partial, status: "withdrawn", skipped: [], replanRequired: false, reimportable: false, removed: 2, keptFirmed: 1, withdrawnAt: "2031-03-04T08:00:00Z", withdrawnTo: "Estimated" };
    handler = () => ({ status: 200, body: withdrawn });
    renderAt(`/scheduling/order-imports/${ORDER}`);
    expect(await screen.findByText("Production took this order out of schedule")).toBeInTheDocument();
    expect(screen.getAllByText("Withdrawn").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Estimated").length).toBeGreaterThan(0);
    expect(screen.getByText(/held by a firmed job were kept and still hold their capacity/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Import again" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Schedule this order" })).not.toBeInTheDocument();
  });

  it("schedules one order from its page and links to the run, or shows the failure", async () => {
    const run = { id: "77777777-7777-4777-8777-777777777777", scope: "order", operationsCount: 3, messages: [], warnings: [] };
    handler = (url) => (url.pathname.endsWith(":schedule") ? { status: 201, body: run } : { status: 200, body: partial });
    const first = renderAt(`/scheduling/order-imports/${ORDER}`);
    fireEvent.click(await screen.findByRole("button", { name: "Schedule this order" }));
    expect(await screen.findByText("The order was scheduled")).toBeInTheDocument();
    expect(screen.getByText(/3 of 3 operations placed\./)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open the run" })).toHaveAttribute("href", `/scheduling/runs/${run.id}`);
    expect(calls.some((c) => c.pathname.endsWith(`/orders/${ORDER}:schedule`))).toBe(true);
    first.unmount();

    handler = (url) =>
      url.pathname.endsWith(":schedule") ? { status: 404, body: { code: "SCHED-0135", detail: "no operations are known for that order" } } : { status: 200, body: partial };
    renderAt(`/scheduling/order-imports/${ORDER}`);
    fireEvent.click(await screen.findByRole("button", { name: "Schedule this order" }));
    expect(await findAlert()).toHaveTextContent("SCHED-0135");
  });

  it("says how many operations a per-order run could not place, and when firmed jobs held them all", () => {
    const base = { id: "r", scope: "order" as const, mode: "jobs", direction: "forward", finite: true, warnings: [] };
    expect(runSummary({ ...base, operationsCount: 4, messages: ["op: no open interval"] } as unknown as Run)).toBe(
      "3 of 4 operations placed; 1 could not be placed (see the run).",
    );
    expect(runSummary({ ...base, operationsCount: 0, messages: [] } as unknown as Run)).toMatch(/held by a firmed job/);
  });
});
