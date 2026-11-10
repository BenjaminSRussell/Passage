import { findAlert } from "../../test-utils";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { BatchOrder } from "./api/client";
import processmfg from "./module";

const ORDER = "b0000000-0000-4000-8000-000000000001";
const FORMULA = "2f000000-0000-4000-8000-000000000001";
const MAIN = "1a000000-0000-4000-8000-000000000001";
const FLOUR = "1a000000-0000-4000-8000-000000000002";
const CO = "1a000000-0000-4000-8000-000000000003";
const BY = "1a000000-0000-4000-8000-000000000004";

type Reply = { status: number; body: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

function renderOrder() {
  return render(
    <MemoryRouter initialEntries={[`/processmfg/batch-orders/${ORDER}`]}>
      <Routes>
        {processmfg.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const base: BatchOrder = {
  id: ORDER,
  legalEntityId: "1e000000-0000-4000-8000-000000000001",
  orderNumber: "BO-000007",
  formulaId: FORMULA,
  formulaCode: "DOUGH-1",
  formulaVersion: 1,
  itemId: MAIN,
  unit: "kg",
  formulaSize: "1000",
  batchSize: "2000",
  scaleFactor: "2",
  status: "planned",
  version: 1,
  createdBy: "planner-1",
  createdAt: "2026-10-21T08:00:00Z",
  updatedAt: "2026-10-21T08:00:00Z",
  lines: [
    { lineNumber: 1, itemId: FLOUR, type: "ingredient", formulaQty: "900", qty: "1800", percent: "0", scalable: true, percentControlled: false },
    { lineNumber: 4, itemId: CO, type: "co-product", formulaQty: "100", qty: "200", percent: "0", scalable: true, percentControlled: false },
    { lineNumber: 5, itemId: BY, type: "by-product", formulaQty: "50", qty: "100", percent: "0", scalable: true, percentControlled: false },
  ],
};

function serve(order: BatchOrder, onCommand?: (url: URL, body: unknown) => Reply | undefined) {
  handler = (url, init) => {
    if (url.pathname === `/api/v1/production/batch-orders/${ORDER}` && !init.method) return { status: 200, body: order };
    if (init.method === "POST") return onCommand?.(url, JSON.parse(String(init.body)));
    return undefined;
  };
}

describe("batch order lifecycle", () => {
  it("releases a planned order with its version and shows the start step", async () => {
    serve(base, () => ({ status: 200, body: { ...base, status: "released", version: 2 } }));
    const { container } = renderOrder();
    fireEvent.click(await screen.findByRole("button", { name: "Release order" }));
    expect(await screen.findByRole("button", { name: "Start order" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe(`/api/v1/production/batch-orders/${ORDER}:release`);
    expect(post.body).toEqual({ version: 1 });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("offers a reload when the order moved meanwhile", async () => {
    serve(base, () => ({ status: 409, body: { code: "PMF-0106", detail: "batch order BO-000007 is released; release needs a planned order" } }));
    renderOrder();
    fireEvent.click(await screen.findByRole("button", { name: "Release order" }));
    expect(await findAlert()).toHaveTextContent("PMF-0106");
    expect(screen.getByRole("button", { name: "Reload order" })).toBeInTheDocument();
  });

  it("uses a fresh key after a definite refusal but keeps it when the outcome is unknown", async () => {
    let n = 0;
    serve(base, () => {
      n += 1;
      if (n === 1) return { status: 409, body: { code: "PMF-0104", detail: "batch order BO-000007 is at version 2, not 1" } };
      if (n === 2) return { status: 503, body: { code: "COM-0500", detail: "service unavailable" } };
      return { status: 200, body: { ...base, status: "released", version: 2 } };
    });
    renderOrder();
    const release = await screen.findByRole("button", { name: "Release order" });
    fireEvent.click(release);
    expect(await findAlert()).toHaveTextContent("PMF-0104");
    fireEvent.click(screen.getByRole("button", { name: "Release order" }));
    expect(await findAlert()).toHaveTextContent("COM-0500");
    fireEvent.click(screen.getByRole("button", { name: "Release order" }));
    expect(await screen.findByRole("button", { name: "Start order" })).toBeInTheDocument();
    const keys = calls.filter((c) => c.method === "POST").map((c) => new Headers(c.init.headers).get("Idempotency-Key"));
    expect(keys).toHaveLength(3);
    expect(keys[1]).not.toBe(keys[0]);
    expect(keys[2]).toBe(keys[1]);
  });

  it("lists the fields the service refused", async () => {
    serve({ ...base, status: "reported", version: 4, actualQty: "1800" }, () => ({
      status: 400,
      body: { code: "COM-0001", detail: "the request has invalid fields", errors: { batchCost: "must be zero or more with at most 0 decimals for JPY" } },
    }));
    renderOrder();
    fireEvent.change(await screen.findByLabelText("Allocation method"), { target: { value: "none" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "JPY" } });
    fireEvent.change(screen.getByLabelText("Batch cost"), { target: { value: "100.5" } });
    fireEvent.click(screen.getByRole("button", { name: "End order and allocate cost" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("batchCost must be zero or more with at most 0 decimals for JPY");
  });

  it("reports a started order with the planned quantities as defaults", async () => {
    serve({ ...base, status: "started", version: 3 }, () => ({
      status: 200,
      body: {
        ...base,
        status: "reported",
        version: 4,
        actualQty: "1800",
        lines: base.lines!.map((l) => (l.lineNumber > 1 ? { ...l, rafQty: l.lineNumber === 4 ? "190" : l.qty } : l)),
      },
    }));
    renderOrder();
    fireEvent.change(await screen.findByLabelText("Finished quantity (kg)"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Report as finished" }));
    expect(await screen.findByText("Enter a positive quantity with at most 6 decimals.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Finished quantity (kg)"), { target: { value: "1800" } });
    fireEvent.change(screen.getByLabelText(/Line 4 co-product/), { target: { value: "190" } });
    fireEvent.click(screen.getByRole("button", { name: "Report as finished" }));
    expect(await screen.findByRole("button", { name: "End order and allocate cost" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe(`/api/v1/production/batch-orders/${ORDER}:report-finished`);
    expect(post.body).toEqual({ version: 3, mainQty: "1800", outputs: [{ lineNumber: 4, qty: "190" }, { lineNumber: 5, qty: "100" }] });
  });

  it("ends a reported order by total cost allocation and shows the split", async () => {
    const reported: BatchOrder = { ...base, status: "reported", version: 4, actualQty: "1800" };
    const ended: BatchOrder = {
      ...reported,
      status: "ended",
      version: 5,
      allocation: {
        method: "tca",
        currency: "USD",
        batchCost: "10000",
        shares: [
          { role: "main", lineNumber: 0, itemId: MAIN, rafQty: "1800", amount: "9331.5789", percent: "94.7368" },
          { role: "co-product", lineNumber: 4, itemId: CO, rafQty: "200", amount: "518.4211", percent: "5.2632" },
          { role: "by-product-burden", lineNumber: 5, itemId: BY, rafQty: "100", amount: "150", percent: "0" },
        ],
      },
    };
    serve(reported, () => ({ status: 200, body: ended }));
    const { container } = renderOrder();
    fireEvent.click(await screen.findByRole("button", { name: "End order and allocate cost" }));
    expect(await screen.findByText("Enter the batch cost, zero or more, with at most 4 decimals.")).toBeInTheDocument();
    expect(screen.getByText("Enter the standard cost per unit of the main item.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Batch cost"), { target: { value: "10000" } });
    fireEvent.change(screen.getByLabelText("Main item: standard cost per unit"), { target: { value: "40" } });
    fireEvent.change(screen.getByLabelText("Co-product line 4: standard cost per unit"), { target: { value: "20" } });
    fireEvent.change(screen.getByLabelText("By-product line 5: rate"), { target: { value: "1.5" } });
    fireEvent.click(screen.getByRole("button", { name: "End order and allocate cost" }));
    const table = await screen.findByRole("table", { name: "Cost allocation" });
    expect(within(table).getByText("9331.5789")).toBeInTheDocument();
    expect(within(table).getByText("By-product burden")).toBeInTheDocument();
    expect(screen.getByText(/add up to the batch cost of 10000 USD/)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe(`/api/v1/production/batch-orders/${ORDER}:end`);
    expect(post.body).toEqual({
      version: 4,
      method: "tca",
      currency: "USD",
      batchCost: "10000",
      main: { standardCost: "40" },
      coProducts: [{ lineNumber: 4, standardCost: "20" }],
      byProducts: [{ lineNumber: 5, costType: "per-quantity", rate: "1.5" }],
    });
    expect(screen.queryByRole("button", { name: "End order and allocate cost" })).toBeNull();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("sends no weights for the quantity method and shows a rule refusal", async () => {
    serve({ ...base, status: "reported", version: 4, actualQty: "1800" }, () => ({
      status: 422,
      body: { code: "PMF-0021", detail: "by-product burden 500.0000 exceeds batch cost 10.0000" },
    }));
    renderOrder();
    fireEvent.change(await screen.findByLabelText("Allocation method"), { target: { value: "quantity" } });
    expect(screen.queryByLabelText("Main item: standard cost per unit")).toBeNull();
    fireEvent.change(screen.getByLabelText("Batch cost"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("By-product line 5: rate"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "End order and allocate cost" }));
    expect(await findAlert()).toHaveTextContent("by-product burden 500.0000 exceeds batch cost 10.0000");
    expect(calls.find((c) => c.method === "POST")!.body).toMatchObject({ method: "quantity", main: {}, coProducts: [] });
  });

  it("shows the stored allocation of an ended order without any form", async () => {
    serve({
      ...base,
      status: "ended",
      version: 5,
      allocation: { method: "none", currency: "EUR", batchCost: "100", shares: [{ role: "main", lineNumber: 0, itemId: MAIN, rafQty: "1800", amount: "100", percent: "100" }] },
    });
    renderOrder();
    expect(await screen.findByRole("table", { name: "Cost allocation" })).toHaveTextContent("Main item");
    expect(screen.queryByRole("button", { name: /End order|Report as finished|Release order|Start order/ })).toBeNull();
  });
});
