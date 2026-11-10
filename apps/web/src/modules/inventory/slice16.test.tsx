import { findStatus } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import inventory from "./module";
import { validateHold } from "./NewHoldPage";
import { validateReleaseQty } from "./HoldsPage";
import { validateReservation, type ReservationValues } from "./NewReservationPage";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const HOLD_ID = "8a8a8a8a-0000-4000-8000-000000000008";
const RES_ID = "7a7a7a7a-0000-4000-8000-000000000007";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];

const json = (status: number, body: unknown) => ({ status, body });
const sent = (method: string) => calls.filter((c) => c.method === method);
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

afterEach(() => {
  vi.unstubAllGlobals();
});

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

const hold = (overrides: Record<string, unknown> = {}) => ({
  id: HOLD_ID,
  legalEntityId: ENTITY_ID,
  productId: PRODUCT_ID,
  dimensions: { site: "S1", warehouse: "W1", batch: "B1" },
  qty: "10",
  releasedQty: "4",
  heldQty: "6",
  wholeLot: false,
  reason: "QUALITY",
  sourceType: "QUALITY_ORDER",
  sourceRef: "QO-1",
  state: "ACTIVE",
  version: 2,
  ...overrides,
});

describe("Holds list", () => {
  it("lists active holds with their source and releases part of one at its version", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(200, hold({ releasedQty: "6", heldQty: "4", version: 3 })) : json(200, { items: [hold()] }));
    renderAt("/inventory/holds");
    expect(await screen.findByText("QUALITY_ORDER QO-1")).toBeInTheDocument();
    expect(calls[0].url.searchParams.get("state")).toBe("ACTIVE");
    fireEvent.change(screen.getByLabelText(/Quantity to release from hold/), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: /Release hold/ }));
    expect(await findStatus()).toHaveTextContent("Released 2 of hold");
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/holds/${HOLD_ID}:release`);
    expect((post.init.headers as Headers).get("If-Match")).toBe('"2"');
    expect(bodyOf(post)).toEqual({ qty: "2" });
  });

  it("releases the whole hold when no quantity is entered", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(200, hold({ state: "RELEASED", heldQty: "0" })) : json(200, { items: [hold()] }));
    renderAt("/inventory/holds");
    fireEvent.click(await screen.findByRole("button", { name: /Release hold/ }));
    await findStatus();
    expect(bodyOf(sent("POST")[0])).toEqual({});
  });

  it("refuses a release above what is held without calling the service", async () => {
    handler = () => json(200, { items: [hold()] });
    renderAt("/inventory/holds");
    fireEvent.change(await screen.findByLabelText(/Quantity to release from hold/), { target: { value: "7" } });
    fireEvent.click(screen.getByRole("button", { name: /Release hold/ }));
    expect(await screen.findByText("Only 6 is still held.")).toBeInTheDocument();
    expect(sent("POST")).toHaveLength(0);
    expect(validateReleaseQty("", "6")).toBeUndefined();
    expect(validateReleaseQty("-1", "6")).toMatch(/above zero/);
  });

  it("shows the empty state and the load failure with a retry", async () => {
    handler = () => json(200, { items: [] });
    const first = renderAt("/inventory/holds");
    expect(await screen.findByText("No holds")).toBeInTheDocument();
    first.unmount();
    handler = () => json(500, { code: "COM-0500", detail: "down" });
    renderAt("/inventory/holds");
    expect(await screen.findByText("Could not load holds")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("shows a stale-version refusal and rereads the list so the next try uses the current version", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(412, { code: "conflict_version", detail: "hold changed since it was read; reread and retry" }) : json(200, { items: [hold()] });
    renderAt("/inventory/holds");
    fireEvent.click(await screen.findByRole("button", { name: /Release hold/ }));
    expect(await screen.findByText("The hold was not released")).toBeInTheDocument();
    expect(screen.getByText(/hold changed since it was read/)).toBeInTheDocument();
    await waitFor(() => expect(sent("GET")).toHaveLength(2));
    expect(calls[0].url.searchParams.get("limit")).toBe("200");
  });

  it("shows a release-quantity refusal without rereading, and says when the list may be cut off", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(422, { code: "INV-HLD-004", detail: "release quantity must be between 0 and the held 6" }) : json(200, { items: [hold()] });
    const first = renderAt("/inventory/holds");
    fireEvent.click(await screen.findByRole("button", { name: /Release hold/ }));
    expect(await screen.findByText(/release quantity must be between/)).toBeInTheDocument();
    expect(screen.getByText("INV-HLD-004")).toBeInTheDocument();
    expect(sent("GET")).toHaveLength(1);
    first.unmount();
    handler = () => json(200, { items: Array.from({ length: 200 }, (_, i) => hold({ id: `h-${i}` })) });
    renderAt("/inventory/holds");
    expect(await screen.findByText(/Showing the first 200 holds/)).toBeInTheDocument();
  });
});

describe("Place hold", () => {
  const values = {
    legalEntityId: ENTITY_ID,
    productId: PRODUCT_ID,
    wholeLot: false,
    qty: "5",
    allowPartial: false,
    reason: "QUALITY",
    sourceType: "QUALITY_ORDER",
    sourceRef: "QO-1",
    note: "",
    dimensions: { site: "S1", warehouse: "W1", location: "", batch: "", serial: "" },
  };

  it("checks the quantity, the source and the dimensions the kind of hold needs", () => {
    expect(validateHold(values)).toEqual({});
    expect(validateHold({ ...values, qty: "0", sourceType: "q", sourceRef: " " })).toEqual({
      qty: "Enter a quantity above zero.",
      sourceType: "Enter 2 to 32 characters starting with a capital letter: capital letters, digits or underscore.",
      sourceRef: "Enter the document that asked for the hold (up to 60 characters).",
    });
    // The service's pattern: a leading digit or underscore is refused too.
    expect(validateHold({ ...values, sourceType: "1QO" }).sourceType).toMatch(/starting with a capital letter/);
    expect(validateHold({ ...values, sourceType: "_QO" }).sourceType).toMatch(/starting with a capital letter/);
    expect(validateHold({ ...values, wholeLot: true, qty: "" }).dimensions).toBe("A whole-lot hold names the batch.");
    expect(validateHold({ ...values, wholeLot: true, qty: "", dimensions: { ...values.dimensions, batch: "B1" } })).toEqual({});
    expect(validateHold({ ...values, dimensions: { ...values.dimensions, site: "" } }).dimensions).toMatch(/names the site/);
  });

  function fill() {
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Source reference"), { target: { value: "QO-1" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
  }

  it("places a quantity hold with an idempotency key and reports the shortfall", async () => {
    handler = () => json(201, { items: [hold({ qty: "3", heldQty: "3", releasedQty: "0" })], shortfall: "2" });
    renderAt("/inventory/holds/new");
    fill();
    fireEvent.click(screen.getByLabelText("Hold what is available if not all of it is"));
    fireEvent.click(screen.getByRole("button", { name: "Place hold" }));
    expect(await findStatus()).toHaveTextContent("Held part of the request; 2 was not available to hold.");
    const post = sent("POST")[0];
    expect((post.init.headers as Headers).get("Idempotency-Key")).toBeTruthy();
    expect(bodyOf(post)).toMatchObject({ legalEntityId: ENTITY_ID, productId: PRODUCT_ID, qty: "5", allowPartial: true, reason: "QUALITY", sourceType: "QUALITY_ORDER", sourceRef: "QO-1", dimensions: { site: "S1" } });
    expect(bodyOf(post).wholeLot).toBeUndefined();
  });

  it("places a whole-lot hold without a quantity", async () => {
    handler = () => json(201, { items: [hold()], shortfall: "0" });
    renderAt("/inventory/holds/new");
    fireEvent.click(screen.getByLabelText(/Hold the whole lot/));
    expect(screen.queryByLabelText("Quantity")).toBeNull();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Source reference"), { target: { value: "RECALL-9" } });
    fireEvent.change(screen.getByLabelText("Batch"), { target: { value: "B1" } });
    fireEvent.click(screen.getByRole("button", { name: "Place hold" }));
    expect(await findStatus()).toHaveTextContent("Held in 1 dimension set.");
    const body = bodyOf(sent("POST")[0]);
    expect(body.wholeLot).toBe(true);
    expect(body.qty).toBeUndefined();
  });

  it("does not call the service while the form has errors, and shows the service refusal", async () => {
    handler = () => json(422, { code: "INV-HLD-001", detail: "only 1 is available" });
    renderAt("/inventory/holds/new");
    fireEvent.click(screen.getByRole("button", { name: "Place hold" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(sent("POST")).toHaveLength(0);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Place hold" }));
    expect(await screen.findByText(/only 1 is available/)).toBeInTheDocument();
    expect(screen.getByText(/INV-HLD-001/)).toBeInTheDocument();
  });
});

describe("Reservation time to live", () => {
  const reservation = (overrides: Record<string, unknown> = {}) => ({
    id: RES_ID,
    legalEntityId: ENTITY_ID,
    productId: PRODUCT_ID,
    dimensions: { site: "S1" },
    qty: "2",
    demandType: "SALES_ORDER",
    demandId: "SO-1",
    policy: "FIFO",
    status: "ACTIVE",
    allocationNo: 1,
    createdAt: "2026-09-01T08:00:00Z",
    expiresAt: "2026-09-02T08:00:00Z",
    ...overrides,
  });

  it("shows when a reservation expires and releases the expired ones on request", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(200, { released: 2, more: true, items: [] }) : json(200, { items: [reservation(), reservation({ id: "x", expiresAt: undefined })] }));
    renderAt("/inventory/reservations");
    expect(await screen.findByRole("columnheader", { name: "Expires" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Release expired" }));
    expect(await findStatus()).toHaveTextContent("Released 2 expired reservations; more are due, run it again.");
    expect(sent("POST")[0].url.pathname).toBe("/api/v1/inventory/reservations:expire");
  });

  it("says nothing expired, and shows a refusal", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(200, { released: 0, more: false, items: [] }) : json(200, { items: [] }));
    const first = renderAt("/inventory/reservations");
    fireEvent.click(await screen.findByRole("button", { name: "Release expired" }));
    expect(await findStatus()).toHaveTextContent("No reservation has expired.");
    first.unmount();
    handler = (_url, init) => (init.method === "POST" ? json(403, { code: "COM-0403", detail: "no" }) : json(200, { items: [] }));
    renderAt("/inventory/reservations");
    fireEvent.click(await screen.findByRole("button", { name: "Release expired" }));
    expect(await screen.findByText("The action did not complete")).toBeInTheDocument();
  });

  const values: ReservationValues = {
    legalEntityId: ENTITY_ID,
    productId: PRODUCT_ID,
    qty: "2",
    policy: "FIFO",
    demandType: "SALES_ORDER",
    demandId: "SO-1",
    requiredDate: "2026-09-01",
    shelfLifeMarginDays: "0",
    ttlHours: "",
    allowPartial: false,
    dimensions: { site: "", warehouse: "", location: "", batch: "", serial: "" },
    segmentType: "NONE",
    segmentRef: "",
    segmentFallback: true,
  };

  it("validates the hours and sends them on the request", async () => {
    expect(validateReservation(values)).toEqual({});
    expect(validateReservation({ ...values, ttlHours: "72" })).toEqual({});
    // 0 means no expiry (the PRD default), not "expire now"; the request then carries no ttlHours.
    expect(validateReservation({ ...values, ttlHours: "0" })).toEqual({});
    expect(validateReservation({ ...values, ttlHours: "9000" }).ttlHours).toMatch(/1 to 8760/);
    expect(validateReservation({ ...values, ttlHours: "1.5" }).ttlHours).toMatch(/1 to 8760/);
    handler = () => json(201, { items: [reservation()], shortfall: "0" });
    renderAt("/inventory/reservations/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Demand reference"), { target: { value: "SO-1" } });
    fireEvent.change(screen.getByLabelText(/Expires after/), { target: { value: "72" } });
    fireEvent.click(screen.getByRole("button", { name: "Reserve" }));
    await waitFor(() => expect(sent("POST")).toHaveLength(1));
    expect(bodyOf(sent("POST")[0]).ttlHours).toBe(72);
  });

  it("sends no ttlHours when the hours are 0, so the reservation has no expiry", async () => {
    handler = () => json(201, { items: [reservation({ expiresAt: undefined })], shortfall: "0" });
    renderAt("/inventory/reservations/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Demand reference"), { target: { value: "SO-1" } });
    fireEvent.change(screen.getByLabelText(/Expires after/), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Reserve" }));
    await waitFor(() => expect(sent("POST")).toHaveLength(1));
    expect(bodyOf(sent("POST")[0])).not.toHaveProperty("ttlHours");
  });
});

describe("Held quantity on the on-hand inquiry", () => {
  it("shows a Held column", async () => {
    handler = () =>
      json(200, { items: [{ group: { product: PRODUCT_ID, site: "S1" }, physicalInvent: "10", availPhysical: "4", availOrdered: "4", reservedPhysical: "0", heldPhysical: "6", blocked: "0", postedValue: "10", currency: "USD", negative: false }], truncated: false });
    renderAt("/inventory/onhand");
    expect(await screen.findByRole("columnheader", { name: "Held" })).toBeInTheDocument();
    expect(screen.getByText("6")).toBeInTheDocument();
  });
});
