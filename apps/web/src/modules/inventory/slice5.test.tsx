import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { validateLine } from "./JournalDetailPage";
import { validateConsignment, type ConsignmentValues } from "./NewConsignmentPage";
import { validateReservation, type ReservationValues } from "./NewReservationPage";
import inventory from "./module";
import { emptyDimensions } from "./parts";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const JOURNAL_ID = "1a2b3c4d-0000-4000-8000-000000000001";
const ORDER_ID = "8c8c8c8c-0000-4000-8000-000000000021";
const LINE_ID = "8d8d8d8d-0000-4000-8000-000000000022";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];

const json = (status: number, body: unknown) => ({ status, body });
const sent = (method: string) => calls.filter((c) => c.method === method);
const bodyOf = (call?: { init: RequestInit }) => JSON.parse((call?.init.body as string) ?? "null");
const header = (call: { init: RequestInit } | undefined, name: string) => new Headers(call?.init.headers).get(name);

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

function journal(overrides: Record<string, unknown> = {}) {
  return {
    id: JOURNAL_ID,
    legalEntityId: ENTITY_ID,
    journalType: "ADJUSTMENT",
    status: "DRAFT",
    postingDate: "2026-10-01",
    currency: "USD",
    version: 2,
    lineCount: 1,
    createdAt: "2026-10-01T08:00:00Z",
    lines: [{ id: "l1", lineNo: 1, productId: PRODUCT_ID, dimensions: { site: "S1" }, qty: "-4", enteredQty: "-4", enteredUnit: "EA", costPrice: "0" }],
    ...overrides,
  };
}

describe("Journal approval workflow", () => {
  it("validates a draft without changing it", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(200, { valid: true, journalId: JOURNAL_ID, status: "DRAFT", version: 2, lineCount: 1 }) : json(200, journal());
    const { container } = renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Validate" }));
    expect(await screen.findByText("The journal would post now: 1 line passed every posting check.")).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post?.url.pathname).toBe(`/api/v1/inventory/journals/${JOURNAL_ID}:validate`);
    expect(screen.getByRole("button", { name: "Submit for approval" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the problem the post would raise", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(422, { code: "INV-NEG-001", detail: "issue would make available physical stock negative (shortfall 1)" }) : json(200, journal());
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Validate" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The journal would not post");
    expect(alert).toHaveTextContent("INV-NEG-001");
  });

  it("submits a draft with its version, then offers approve and reject only", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(200, journal({ status: "SUBMITTED", version: 3 })) : json(200, journal()));
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Submit for approval" }));
    expect(await screen.findByText("Journal submitted for approval.")).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post?.url.pathname).toBe(`/api/v1/inventory/journals/${JOURNAL_ID}:submit`);
    expect(header(post, "If-Match")).toBe('"2"');
    expect(screen.getByText("Awaiting approval")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reject" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Post journal" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Add line" })).toBeNull();
  });

  it("approves a submitted journal and then posts it", async () => {
    let state = journal({ status: "SUBMITTED", version: 3 });
    handler = (url, init) => {
      if (init.method !== "POST") return json(200, state);
      if (url.pathname.endsWith(":approve")) state = journal({ status: "APPROVED", version: 4 });
      if (url.pathname.endsWith(":post")) state = journal({ status: "POSTED", version: 5, postedAt: "2026-10-01T09:00:00Z" });
      return json(200, state);
    };
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Journal approved. It can now be posted.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Post journal" }));
    expect(await screen.findByText("Journal posted: 1 line.")).toBeInTheDocument();
    const posts = sent("POST");
    expect(posts.map((c) => c.url.pathname)).toEqual([`/api/v1/inventory/journals/${JOURNAL_ID}:approve`, `/api/v1/inventory/journals/${JOURNAL_ID}:post`]);
    expect(header(posts[1], "If-Match")).toBe('"4"');
  });

  it("reopens or cancels a rejected journal", async () => {
    handler = (url, init) =>
      init.method === "POST" ? json(200, journal({ status: url.pathname.endsWith(":reopen") ? "DRAFT" : "CANCELLED", version: 6 })) : json(200, journal({ status: "REJECTED", version: 5 }));
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    expect(await screen.findByText("Reopen the journal as a draft to correct it, or cancel it.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reopen as draft" }));
    expect(await screen.findByText("Journal reopened as a draft.")).toBeInTheDocument();
    expect(header(sent("POST")[0], "If-Match")).toBe('"5"');
    expect(screen.getByRole("heading", { name: "Add line" })).toBeInTheDocument();
  });

  it("offers a reload when the journal changed meanwhile", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(412, { code: "conflict_version", detail: "journal changed since it was read" }) : json(200, journal()));
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel journal" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The journal was not cancelled");
    expect(within(alert).getByRole("button", { name: "Reload journal" })).toBeInTheDocument();
  });
});

describe("Ownership change journals", () => {
  const ownership = journal({ journalType: "OWNERSHIP_CHANGE", lines: [], lineCount: 0, version: 1 });

  it("asks for the vendor and the consignment price on an ownership change line", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? json(201, { ...ownership, version: 2, lines: [{ id: "l1", lineNo: 1, productId: PRODUCT_ID, dimensions: { site: "S1", owner: "V1" }, qty: "120", enteredQty: "120", enteredUnit: "EA", costPrice: "4" }] })
        : json(200, ownership);
    const { container } = renderAt(`/inventory/journals/${JOURNAL_ID}`);
    expect(await screen.findByRole("heading", { name: "Ownership change journal" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "120" } });
    fireEvent.change(screen.getByLabelText("Consignment price"), { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    expect(await screen.findByText("Enter the vendor that owns the consignment stock.")).toBeInTheDocument();
    expect(sent("POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Owner (vendor)"), { target: { value: "V1" } });
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    await waitFor(() => expect(sent("POST")).toHaveLength(1));
    expect(bodyOf(sent("POST")[0]).lines[0]).toMatchObject({ productId: PRODUCT_ID, qty: "120", costPrice: "4", dimensions: { site: "S1", owner: "V1" } });
    expect(pageViolations(container)).toEqual([]);
  });

  it("does not offer a reversal of a posted ownership change", async () => {
    handler = () => json(200, journal({ journalType: "OWNERSHIP_CHANGE", status: "POSTED" }));
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    expect(await screen.findByRole("heading", { name: "Ownership change journal" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reverse journal" })).toBeNull();
  });

  it("keeps owner off other journal types and checks segments", () => {
    const base = { productId: PRODUCT_ID, qty: "2", enteredUnit: "EA", costPrice: "", dimensions: { site: "S1", warehouse: "", location: "", batch: "", serial: "", inventory_status: "", owner: "", segment: "" } };
    expect(validateLine(base, "ADJUSTMENT")).toEqual({});
    expect(validateLine({ ...base, dimensions: { ...base.dimensions, segment: "P1" } }, "ADJUSTMENT").segment).toBe("Use SALES_ORDER:reference or PROJECT:reference.");
    expect(validateLine({ ...base, dimensions: { ...base.dimensions, segment: "PROJECT:P1" } }, "ADJUSTMENT")).toEqual({});
    expect(validateLine({ ...base, qty: "-2" }, "OWNERSHIP_CHANGE")).toMatchObject({ qty: expect.any(String), owner: expect.any(String) });
    expect(validateLine({ ...base, dimensions: { ...base.dimensions, owner: "V1" } }, "OWNERSHIP_CHANGE")).toEqual({});
  });

  it("is preselected from the consignment stock report", async () => {
    renderAt("/inventory/journals/new?type=OWNERSHIP_CHANGE");
    expect(await screen.findByLabelText("Journal type")).toHaveValue("OWNERSHIP_CHANGE");
    expect(screen.queryByLabelText("Offset account")).toBeNull();
  });
});

const order = {
  id: ORDER_ID,
  legalEntityId: ENTITY_ID,
  number: "CRO-00000000A1B2",
  vendorId: "V1",
  siteId: "S1",
  warehouseId: "W1",
  requestedDate: "2026-10-01",
  currency: "USD",
  status: "CREATED",
  version: 1,
  createdAt: "2026-10-01T08:00:00Z",
  lines: [
    {
      id: LINE_ID,
      lineNo: 1,
      productId: PRODUCT_ID,
      dimensions: { site: "S1", warehouse: "W1", owner: "V1", batch: "B1" },
      unit: "EA",
      qty: "500",
      receivedQty: "0",
      openQty: "500",
      consignmentPrice: "4",
    },
  ],
};

describe("Consignment orders", () => {
  it("lists orders with status and received progress, and an empty state", async () => {
    handler = (url) => json(200, { items: url.searchParams.get("status") === "RECEIVED" ? [] : [{ ...order, status: "PARTIALLY_RECEIVED", lines: [{ ...order.lines[0], receivedQty: "120", openQty: "380" }] }] });
    const { container } = renderAt("/inventory/consignment-orders");
    const table = await findTable({ name: "Consignment orders" });
    expect(within(table).getByRole("link", { name: "CRO-00000000A1B2" })).toHaveAttribute("href", `/inventory/consignment-orders/${ORDER_ID}`);
    expect(table).toHaveTextContent("Partially received");
    expect(table).toHaveTextContent("120 of 500");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "RECEIVED" } });
    expect(await screen.findByText("No consignment orders in this status")).toBeInTheDocument();
  });

  it("shows a load failure with a retry", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "database unavailable" });
    renderAt("/inventory/consignment-orders");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load consignment orders");
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("creates an order with an idempotency key and opens it", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, order) : json(200, order));
    const { container } = renderAt("/inventory/consignment-orders/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Vendor"), { target: { value: "V1" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.change(screen.getByLabelText("Warehouse"), { target: { value: "W1" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "500" } });
    fireEvent.change(screen.getByLabelText("Consignment price"), { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText("Batch"), { target: { value: "B1" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create consignment order" }));
    expect(await screen.findByRole("heading", { name: "Consignment order CRO-00000000A1B2" })).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post?.url.pathname).toBe("/api/v1/inventory/consignment-orders");
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    expect(bodyOf(post)).toMatchObject({
      legalEntityId: ENTITY_ID,
      vendorId: "V1",
      siteId: "S1",
      warehouseId: "W1",
      currency: "USD",
      lines: [{ productId: PRODUCT_ID, qty: "500", unit: "EA", consignmentPrice: "4", dimensions: { batch: "B1" } }],
    });
  });

  it("maps service field errors onto the form", async () => {
    handler = () => json(400, { code: "COM-0001", detail: "one or more fields are invalid", errors: { vendorId: "must be at most 60 characters", "lines[0].qty": "must be a positive decimal number" } });
    renderAt("/inventory/consignment-orders/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Vendor"), { target: { value: "V1" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.change(screen.getByLabelText("Warehouse"), { target: { value: "W1" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "USD" } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Create consignment order" }));
    expect(await screen.findByText("must be at most 60 characters")).toBeInTheDocument();
    expect(screen.getByText("must be a positive decimal number")).toBeInTheDocument();
  });

  it("validates an order before sending it", () => {
    const values: ConsignmentValues = {
      legalEntityId: ENTITY_ID,
      vendorId: "V1",
      siteId: "S1",
      warehouseId: "W1",
      requestedDate: "2026-10-01",
      currency: "USD",
      lines: [{ productId: PRODUCT_ID, qty: "5", unit: "EA", consignmentPrice: "4", dimensions: { location: "", batch: "", serial: "" } }],
    };
    expect(validateConsignment(values)).toEqual({ header: {}, lines: [{}] });
    const bad = validateConsignment({
      ...values,
      vendorId: " ",
      currency: "us",
      lines: [{ productId: "x", qty: "2", unit: "", consignmentPrice: "-1", dimensions: { location: "", batch: "", serial: "SN1" } }],
    });
    expect(Object.keys(bad.header).sort()).toEqual(["currency", "vendorId"]);
    expect(bad.lines[0]).toEqual({
      productId: "Enter the product id (a UUID).",
      qty: "A serial number arrives one unit at a time.",
      unit: "Enter a unit code such as EA.",
      consignmentPrice: "Enter a price of zero or more.",
    });
  });

  it("receives vendor-owned stock against the order with its version", async () => {
    const received = { ...order, status: "PARTIALLY_RECEIVED", version: 2, lines: [{ ...order.lines[0], receivedQty: "120", openQty: "380" }] };
    handler = (_url, init) => (init.method === "POST" ? json(200, received) : json(200, order));
    const { container } = renderAt(`/inventory/consignment-orders/${ORDER_ID}`);
    const qty = await screen.findByLabelText("Line 1: received EA");
    expect(qty).toHaveValue("500");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(qty, { target: { value: "600" } });
    fireEvent.click(screen.getByRole("button", { name: "Book receipt" }));
    expect(await screen.findByText("At most 500 EA are still open.")).toBeInTheDocument();
    expect(sent("POST")).toHaveLength(0);
    fireEvent.change(qty, { target: { value: "120" } });
    fireEvent.click(screen.getByRole("button", { name: "Book receipt" }));
    expect(await screen.findByText("Receipt booked as vendor-owned stock.")).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post?.url.pathname).toBe(`/api/v1/inventory/consignment-orders/${ORDER_ID}:receive`);
    expect(header(post, "If-Match")).toBe('"1"');
    expect(bodyOf(post).lines).toEqual([{ lineId: LINE_ID, qty: "120" }]);
    expect(screen.getByRole("table", { name: "Consignment lines" })).toHaveTextContent("380");
  });

  it("shows a refused receipt", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(422, { code: "INV-CNS-002", detail: "receipt of 600 on line 1 exceeds the open quantity 500" }) : json(200, order));
    renderAt(`/inventory/consignment-orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Book receipt" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The receipt was not booked");
    expect(alert).toHaveTextContent("INV-CNS-002");
  });

  it("explains an unknown order", async () => {
    handler = () => json(404, { code: "INV-CNS-001", detail: "replenishment order not found" });
    renderAt(`/inventory/consignment-orders/${ORDER_ID}`);
    expect(await screen.findByRole("heading", { name: "Consignment order not found" })).toBeInTheDocument();
  });
});

describe("Consignment stock", () => {
  const item = {
    legalEntityId: ENTITY_ID,
    vendorId: "V1",
    productId: PRODUCT_ID,
    dimensions: { site: "S1", warehouse: "W1", owner: "V1" },
    qty: "380",
    consignmentPrice: "4",
    currency: "USD",
    statisticalValue: "1520",
    oldestReceipt: "2026-10-01",
    ageDays: 25,
  };

  it("lists vendor-owned stock with age and statistical value, filtered by vendor", async () => {
    handler = (url) => json(200, { items: url.searchParams.get("vendor") === "V9" ? [] : [item] });
    const { container } = renderAt("/inventory/consignment/stock");
    const table = await findTable({ name: "Consignment stock" });
    expect(table).toHaveTextContent("380");
    expect(table).toHaveTextContent("1520 USD");
    expect(table).toHaveTextContent("25");
    expect(screen.getByRole("link", { name: "New ownership change" })).toHaveAttribute("href", "/inventory/journals/new?type=OWNERSHIP_CHANGE");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Vendor"), { target: { value: "V9" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(await screen.findByText("No stock owned by V9")).toBeInTheDocument();
    expect(calls.at(-1)?.url.searchParams.get("vendor")).toBe("V9");
  });
});

describe("Segmented reservations", () => {
  const values: ReservationValues = {
    legalEntityId: ENTITY_ID,
    productId: PRODUCT_ID,
    qty: "5",
    policy: "FIFO",
    demandType: "SALES_ORDER",
    demandId: "SO-1",
    requiredDate: "2026-10-01",
    shelfLifeMarginDays: "0",
    ttlHours: "",
    allowPartial: false,
    dimensions: emptyDimensions(),
    segmentType: "PROJECT",
    segmentRef: "",
    segmentFallback: true,
  };

  it("requires a reference for a segmented demand", () => {
    expect(validateReservation(values).segmentRef).toBe("Enter the sales order or project reference (up to 48 characters).");
    expect(validateReservation({ ...values, segmentRef: "P-1" })).toEqual({});
  });

  it("sends the demand segment and fallback", async () => {
    handler = () => json(201, { items: [], shortfall: "0" });
    renderAt("/inventory/reservations/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Demand reference"), { target: { value: "SO-1" } });
    fireEvent.change(screen.getByLabelText("Demand segment"), { target: { value: "PROJECT" } });
    fireEvent.change(screen.getByLabelText("Segment reference"), { target: { value: "P-1" } });
    fireEvent.click(screen.getByLabelText("Use unsegmented stock when the segment runs short"));
    fireEvent.click(screen.getByRole("button", { name: "Reserve" }));
    await waitFor(() => expect(sent("POST")).toHaveLength(1));
    expect(bodyOf(sent("POST")[0])).toMatchObject({ segmentType: "PROJECT", segmentRef: "P-1", segmentFallback: false });
  });

  it("leaves the segment out for unsegmented demand", async () => {
    handler = () => json(201, { items: [], shortfall: "0" });
    renderAt("/inventory/reservations/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Demand reference"), { target: { value: "SO-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Reserve" }));
    await waitFor(() => expect(sent("POST")).toHaveLength(1));
    const body = bodyOf(sent("POST")[0]);
    expect(body.segmentType).toBeUndefined();
    expect(body.segmentFallback).toBeUndefined();
  });

  it("shows the demand segment in the reservation inquiry", async () => {
    handler = () =>
      json(200, {
        items: [
          {
            id: "r1",
            legalEntityId: ENTITY_ID,
            productId: PRODUCT_ID,
            dimensions: { site: "S1", segment: "PROJECT:P-1" },
            qty: "4",
            demandType: "SALES_ORDER",
            demandId: "SO-1",
            demandSegment: "PROJECT:P-1",
            policy: "FIFO",
            status: "RELEASED",
            allocationNo: 1,
            createdAt: "2026-10-01T08:00:00Z",
          },
        ],
      });
    renderAt("/inventory/reservations");
    expect(await findTable()).toHaveTextContent("Sales order SO-1 (segment PROJECT:P-1)");
  });
});

describe("On-hand by owner and segment", () => {
  it("filters and groups by owner and segment", async () => {
    handler = () => json(200, { items: [], truncated: false });
    renderAt("/inventory/onhand");
    // The filter field comes first; the group-by checkbox shares its label.
    const [ownerFilter, ownerGroup] = await screen.findAllByLabelText("Owner (vendor)");
    fireEvent.change(ownerFilter!, { target: { value: "V1" } });
    fireEvent.change(screen.getAllByLabelText("Segment")[0]!, { target: { value: "PROJECT:P1" } });
    fireEvent.click(ownerGroup!);
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("owner")).toBe("V1"));
    const url = calls.at(-1)?.url;
    expect(url?.searchParams.get("segment")).toBe("PROJECT:P1");
    expect(url?.searchParams.get("groupBy")).toBe("product,site,owner");
  });
});
