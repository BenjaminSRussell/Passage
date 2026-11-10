import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import inventory from "./module";
import { validateReservation, type ReservationValues } from "./NewReservationPage";
import { emptyDimensions } from "./parts";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];
const json = (status: number, body: unknown) => ({ status, body });
const bodyOf = (call?: { init: RequestInit }) => JSON.parse((call?.init.body as string) ?? "null");

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const { status, body } = handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {inventory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const reservation = (o: Record<string, unknown> = {}) => ({
  id: "7a7a7a7a-0000-4000-8000-000000000004", legalEntityId: ENTITY_ID, productId: PRODUCT_ID, dimensions: { site: "S1", warehouse: "W2" }, qty: "25",
  demandType: "SALES_ORDER", demandId: "SO-7", policy: "MANUAL", status: "ACTIVE", kind: "ORDERED", allocationNo: 1, createdAt: "2026-09-01T08:00:00Z", ...o,
});

describe("Reservations against expected receipts", () => {
  const values: ReservationValues = {
    legalEntityId: ENTITY_ID, productId: PRODUCT_ID, qty: "5", policy: "MANUAL", demandType: "SALES_ORDER", demandId: "SO-7", requiredDate: "2026-09-01",
    shelfLifeMarginDays: "0", ttlHours: "", allowPartial: false, dimensions: { ...emptyDimensions(), site: "S1" }, segmentType: "NONE", segmentRef: "",
    segmentFallback: true, against: "ORDERED",
  };

  it("needs a manual reservation of one dimension set", () => {
    expect(validateReservation(values)).toEqual({});
    expect(validateReservation({ ...values, policy: "FIFO" }).against).toMatch(/is manual/);
    expect(validateReservation({ ...values, dimensions: emptyDimensions() }).dimensions).toMatch(/names the site/);
  });

  it("sends against ORDERED with the policy forced to manual and shows what it holds", async () => {
    handler = () => json(201, { items: [reservation()], shortfall: "0" });
    renderAt("/inventory/reservations/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "25" } });
    fireEvent.change(screen.getByLabelText("Reserve"), { target: { value: "ORDERED" } });
    expect(screen.getByLabelText("Policy")).toHaveValue("MANUAL");
    fireEvent.change(screen.getByLabelText("Demand reference"), { target: { value: "SO-7" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.change(screen.getByLabelText("Warehouse"), { target: { value: "W2" } });
    fireEvent.click(screen.getByRole("button", { name: "Reserve" }));
    expect(await findTable({ name: "Allocations" })).toHaveTextContent("25");
    const post = calls.find((c) => c.method === "POST");
    expect(bodyOf(post)).toMatchObject({ against: "ORDERED", policy: "MANUAL", qty: "25", dimensions: { site: "S1", warehouse: "W2" } });
  });

  it("does not send against for stock on hand, and shows the service's shortfall rule", async () => {
    handler = () => json(422, { code: "INV-RES-001", detail: "only 5 expected to arrive and unreserved, 25 requested" });
    renderAt("/inventory/reservations/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "25" } });
    fireEvent.change(screen.getByLabelText("Demand reference"), { target: { value: "SO-7" } });
    fireEvent.click(screen.getByRole("button", { name: "Reserve" }));
    expect(await findAlert()).toHaveTextContent("only 5 expected to arrive and unreserved, 25 requested");
    expect(bodyOf(calls.find((c) => c.method === "POST"))).not.toHaveProperty("against");
  });

  it("lists what each reservation holds", async () => {
    handler = () => json(200, { items: [reservation(), reservation({ id: "r2", kind: "PHYSICAL", qty: "3" })] });
    renderAt("/inventory/reservations");
    const table = await findTable({ name: "Reservations" });
    expect(screen.getByRole("columnheader", { name: "Reserves" })).toBeInTheDocument();
    expect(table).toHaveTextContent("Expected receipt");
    expect(table).toHaveTextContent("Stock on hand");
  });

  it("marks a reservation whose expected receipt has arrived", async () => {
    handler = () => json(200, { items: [reservation({ kind: "PHYSICAL", convertedAt: "2026-09-03T09:30:00Z" })] });
    renderAt("/inventory/reservations");
    const table = await findTable({ name: "Reservations" });
    expect(table).toHaveTextContent("Stock on hand (receipt arrived");
    expect(table).not.toHaveTextContent("Expected receipt");
  });

  it("shows the quantity reserved against expected receipts in the on-hand inquiry", async () => {
    handler = () =>
      json(200, {
        items: [{ group: { product: PRODUCT_ID }, physicalInvent: "100", availPhysical: "100", availOrdered: "75", postedValue: "200.00", reservedPhysical: "0",
          reservedOrdered: "25", heldPhysical: "0", blocked: "0", currency: "USD", negative: false }],
        truncated: false,
      });
    renderAt("/inventory/onhand");
    const table = await findTable({ name: "On-hand" });
    expect(screen.getByRole("columnheader", { name: "Reserved on order" })).toBeInTheDocument();
    expect(table).toHaveTextContent("25");
  });
});
