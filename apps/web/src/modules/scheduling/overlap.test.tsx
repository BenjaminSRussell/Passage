import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Operation, Resource } from "./api/client";
import { compareDecimal, trimDecimal } from "./forms";
import scheduling from "./module";
import { emptyOperation, validateOperation } from "./NewOperationPage";
import { initialValues, validateOperationEdit } from "./OperationDetailPage";

const MIX = "3f1c0000-0000-4000-8000-000000000001";
const OP = "7a000000-0000-4000-8000-000000000001";
const ORDER = "b2000000-0000-4000-8000-000000000002";

type Reply = { status: number; body?: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: "no fake" } };
      return new Response(reply.body === undefined ? null : JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

const shifts = [1, 2, 3, 4, 5].map((weekday) => ({ weekday, startMinute: 480, endMinute: 960 }));
const resources: Resource[] = [
  { id: MIX, code: "MIX-1", name: "Mixing vessel", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z", engineeringResourceId: null },
];
const idsByCode = { "MIX-1": MIX };
const codes = { [MIX]: "MIX-1" };

const op: Operation = {
  id: OP,
  orderId: ORDER,
  operNumber: 10,
  minutes: 240,
  priority: 100,
  resourceIds: [MIX],
  secondaryResourceIds: [],
  setupGroup: null,
  setupMinutes: 0,
  queueBeforeMinutes: 0,
  queueAfterMinutes: 0,
  quantity: "100.0000",
  overlapQuantity: "25.0000",
  attributes: {},
  networkId: null,
  status: "scheduled",
  createdAt: "2026-10-26T06:00:00Z",
  version: 2,
};

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

describe("decimal text helpers", () => {
  it("trims and compares without a float", () => {
    expect(trimDecimal("100.0000")).toBe("100");
    expect(trimDecimal("2.5000")).toBe("2.5");
    expect(trimDecimal("100")).toBe("100");
    expect(compareDecimal("25.5", "25.50")).toBe(0);
    expect(compareDecimal("9", "10")).toBe(-1);
    expect(compareDecimal("0.30000000000000004", "0.3")).toBe(1);
    expect(compareDecimal("100.0001", "100")).toBe(1);
  });
});

describe("New operation overlap fields", () => {
  const valid = { ...emptyOperation, orderId: ORDER, minutes: "60", resourceIds: [MIX] };
  it("sends the quantity and the overlap", () => {
    const { errors, input } = validateOperation({ ...valid, quantity: "100", overlapQuantity: "25.5" });
    expect(errors).toEqual({});
    expect(input).toMatchObject({ quantity: "100", overlapQuantity: "25.5" });
    expect(validateOperation(valid).input).not.toHaveProperty("quantity");
  });
  it("mirrors the service rules", () => {
    expect(validateOperation({ ...valid, quantity: "0" }).errors.quantity).toBeTruthy();
    expect(validateOperation({ ...valid, quantity: "1.00001" }).errors.quantity).toBeTruthy();
    expect(validateOperation({ ...valid, overlapQuantity: "5" }).errors.overlapQuantity).toMatch(/needs the quantity/);
    expect(validateOperation({ ...valid, quantity: "10", overlapQuantity: "11" }).errors.overlapQuantity).toMatch(/exceed/);
    expect(validateOperation({ ...valid, quantity: "10", overlapQuantity: "10" }).errors).toEqual({});
  });
});

describe("Operation edit overlap fields", () => {
  const same = initialValues(op, codes);
  it("shows the stored quantities without trailing zeros", () => {
    expect(same.quantity).toBe("100");
    expect(same.overlapQuantity).toBe("25");
  });
  it("sends only what changed, and zero to clear the overlap", () => {
    expect(validateOperationEdit({ ...same, overlapQuantity: "30" }, op, idsByCode).patch).toEqual({ overlapQuantity: "30" });
    expect(validateOperationEdit({ ...same, quantity: "120" }, op, idsByCode).patch).toEqual({ quantity: "120" });
    expect(validateOperationEdit({ ...same, overlapQuantity: "" }, op, idsByCode).patch).toEqual({ overlapQuantity: "0" });
    expect(validateOperationEdit({ ...same, quantity: "100.0" }, op, idsByCode).errors.form).toBe("Nothing changed.");
  });
  it("mirrors the service rules", () => {
    expect(validateOperationEdit({ ...same, quantity: "" }, op, idsByCode).errors.quantity).toMatch(/cannot be removed/);
    expect(validateOperationEdit({ ...same, quantity: "20" }, op, idsByCode).errors.overlapQuantity).toMatch(/exceed/);
    expect(validateOperationEdit({ ...same, overlapQuantity: "abc" }, op, idsByCode).errors.overlapQuantity).toBeTruthy();
    const none: Operation = { ...op, quantity: null, overlapQuantity: null };
    expect(validateOperationEdit({ ...initialValues(none, codes), overlapQuantity: "5" }, none, idsByCode).errors.overlapQuantity).toMatch(/needs a quantity/);
  });
});

describe("Operation detail overlap", () => {
  it("shows the quantities and sends a cleared overlap", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 200, body: { ...op, overlapQuantity: null, status: "open", version: 3, jobsRemoved: true } };
      if (url.pathname.endsWith(`/operations/${OP}`)) return { status: 200, body: op };
      if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: resources } };
      return undefined;
    };
    renderAt(`/scheduling/operations/${OP}`);
    const overlap = (await screen.findByLabelText(/^Overlap quantity/)) as HTMLInputElement;
    expect(overlap.value).toBe("25");
    expect(screen.getAllByText("Overlap quantity").length).toBeGreaterThan(0);
    fireEvent.change(overlap, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The operation was saved (version 3).")).toBeTruthy();
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ overlapQuantity: "0" });
  });

  it("shows the service's overlap refusal on the field", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 400, body: { code: "validation_failed", detail: "invalid", errors: { overlapQuantity: "must not exceed the quantity" } } };
      if (url.pathname.endsWith(`/operations/${OP}`)) return { status: 200, body: op };
      if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: resources } };
      return undefined;
    };
    renderAt(`/scheduling/operations/${OP}`);
    fireEvent.change(await screen.findByLabelText(/^Overlap quantity/), { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("must not exceed the quantity")).toBeTruthy();
  });
});
