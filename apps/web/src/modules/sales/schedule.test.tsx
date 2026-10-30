import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { BackorderItem, Order, PromiseLineResult, ScheduleLine } from "./api/client";
import { lateness, validateBackorderFilter } from "./BackordersPage";
import sales from "./module";
import { scheduleSource } from "./OrderSchedule";
import { promiseOutcome, validatePromise } from "./PromiseLinePage";

const ORDER_ID = "66666666-0000-4000-8000-000000000006";
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = await handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
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
        {sales.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

function order(overrides: Partial<Order> = {}): Order {
  return {
    id: ORDER_ID,
    legalEntityId: "11111111-0000-4000-8000-000000000001",
    orderNumber: "SO-0000042",
    customerId: "22222222-0000-4000-8000-000000000002",
    invoiceAccountId: "22222222-0000-4000-8000-000000000002",
    orderType: "ORDER",
    status: "CONFIRMED",
    currency: "USD",
    requestedShipDate: "2027-03-04",
    channel: "UI",
    netTotal: "57",
    version: 3,
    lines: [
      { id: "l1", lineNo: 1, productId: "p1", quantity: "5", unit: "EA", siteId: "s", warehouseId: "w", unitPrice: "10", discPct: "0", lineNet: "50", requestedShipDate: "2027-03-04", confirmedShipDate: "2027-03-04", lineStatus: "OPEN" },
      { id: "l2", lineNo: 2, productId: "p2", quantity: "1", unit: "EA", siteId: "s", warehouseId: "w", unitPrice: "7", discPct: "0", lineNet: "7", requestedShipDate: "2027-03-04", confirmedShipDate: "2027-03-04", lineStatus: "OPEN" },
    ],
    holds: [],
    createdAt: "2027-03-01T09:00:00Z",
    updatedAt: "2027-03-01T09:00:00Z",
    ...overrides,
  };
}


const SPLIT: ScheduleLine[] = [
  { id: "s1", lineNo: 1, seq: 1, quantity: "40", deliveryDate: "2027-03-01", status: "CONFIRMED", source: "ATP", bucketDate: "2027-03-01" },
  { id: "s2", lineNo: 1, seq: 2, quantity: "5", deliveryDate: "2027-03-04", status: "BACKORDER", source: "ATP", bucketDate: "2027-03-04" },
];

function backorder(overrides: Partial<BackorderItem> = {}): BackorderItem {
  return {
    scheduleLineId: "s2",
    legalEntityId: "11111111-0000-4000-8000-000000000001",
    orderId: ORDER_ID,
    orderNumber: "SO-0000042",
    orderVersion: 3,
    customerId: "22222222-0000-4000-8000-000000000002",
    lineId: "l1",
    lineNo: 1,
    productId: "33333333-0000-4000-8000-000000000003",
    siteId: "s",
    warehouseId: "w",
    quantity: "5",
    requestedShipDate: "2027-03-01",
    promisedDate: "2027-03-04",
    daysLate: 3,
    status: "BACKORDER",
    ...overrides,
  };
}

describe("validatePromise", () => {
  it("builds the request from a valid form", () => {
    const { errors, input, lineNo } = validatePromise({
      lineNo: "2",
      completeOnly: true,
      periods: [
        { date: "2027-03-01", supply: "40", demand: "0" },
        { date: "2027-03-04", supply: "5", demand: "0" },
      ],
    });
    expect(errors).toEqual({});
    expect(lineNo).toBe(2);
    expect(input?.completeOnly).toBe(true);
    expect(input?.periods).toHaveLength(2);
  });

  it("refuses what the service would refuse", () => {
    const { errors, input } = validatePromise({
      lineNo: "",
      completeOnly: false,
      periods: [
        { date: "2027-03-04", supply: "-1", demand: "0" },
        { date: "2027-03-01", supply: "1", demand: "x" },
      ],
    });
    expect(Object.keys(errors).sort()).toEqual(["lineNo", "periods[0].supply", "periods[1].date", "periods[1].demand"]);
    expect(input).toBeUndefined();
    expect(validatePromise({ lineNo: "1", completeOnly: false, periods: [] }).errors.periods).toBeDefined();
  });
});

describe("promiseOutcome and backorder filter", () => {
  it("describes the split in a sentence", () => {
    const result = (lines: ScheduleLine[]) => ({ order: order(), scheduleLines: lines }) as PromiseLineResult;
    expect(promiseOutcome(result([SPLIT[0]]), 1)).toBe("Line 1 can ship in full by the requested date.");
    expect(promiseOutcome(result(SPLIT), 1)).toBe("Line 1 was split: 1 later delivery on backorder.");
    expect(promiseOutcome(result([SPLIT[0], { id: "s3", lineNo: 1, seq: 2, quantity: "15", status: "UNPROMISED", source: "ATP" }]), 1)).toBe("Line 1 was split: 15 with no supply on backorder.");
    expect(promiseOutcome(result([{ ...SPLIT[1], quantity: "45" }]), 1)).toBe("Line 1 is on backorder: 1 later delivery.");
  });

  it("names the source of a schedule line", () => {
    expect(scheduleSource(SPLIT[1])).toBe("ATP bucket 2027-03-04");
    expect(scheduleSource({ ...SPLIT[0], source: "LINE", bucketDate: undefined })).toBe("Line date");
    expect(scheduleSource({ id: "s3", lineNo: 1, seq: 3, quantity: "15", status: "UNPROMISED", source: "ATP" })).toBe("ATP, no supply");
  });

  it("validates filters and words lateness", () => {
    expect(validateBackorderFilter({ legalEntityId: "x", productId: "", customerId: " ", promisedTo: "soon" })).toEqual({ legalEntityId: "Enter a UUID.", promisedTo: "Choose a date." });
    expect(lateness(backorder())).toBe("3 days late");
    expect(lateness(backorder({ daysLate: 1 }))).toBe("1 day late");
    expect(lateness(backorder({ daysLate: undefined }))).toBe("—");
  });
});

describe("Backorders page", () => {
  it("lists backorders with the promised date and filters by product", async () => {
    handler = () => ({ status: 200, body: { items: [backorder(), backorder({ scheduleLineId: "s3", lineId: "l2", lineNo: 2, promisedDate: undefined, daysLate: undefined, status: "UNPROMISED", quantity: "15" })] } });
    const { container } = renderAt("/sales/backorders");
    const table = await findTable({ name: "Backorder lines" });
    expect(within(table).getAllByRole("link", { name: "SO-0000042" })[0]).toHaveAttribute("href", `/sales/orders/${ORDER_ID}`);
    expect(within(table).getByText("2027-03-04")).toBeInTheDocument();
    expect(within(table).getByText("3 days late")).toBeInTheDocument();
    expect(within(table).getByText("Not promised")).toBeInTheDocument();
    expect(within(table).getByRole("link", { name: "Re-promise SO-0000042 line 2" })).toHaveAttribute("href", `/sales/orders/${ORDER_ID}/promise?line=2`);
    expect(await screen.findByText("2 lines on backorder.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    const product = "33333333-0000-4000-8000-000000000003";
    fireEvent.change(screen.getByLabelText(/Product/), { target: { value: product } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("productId") === product)).toBe(true));
  });

  it("refuses a malformed filter without calling the service", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/sales/backorders");
    expect(await screen.findByRole("heading", { name: "No backorders" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Customer/), { target: { value: "C1" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(await screen.findByText("Enter a UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(1);
  });

  it("shows the problem and retries", async () => {
    let fail = true;
    handler = () => (fail ? { status: 500, body: { status: 500, code: "COM-0500", detail: "boom" } } : { status: 200, body: { items: [] } });
    renderAt("/sales/backorders");
    expect(await screen.findByText("Could not load the backorders")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { name: "No backorders" })).toBeInTheDocument();
  });
});

describe("Promise order line page", () => {
  it("promises a line with If-Match and shows the split (AC-27.8)", async () => {
    const result: PromiseLineResult = { order: order({ version: 4 }), scheduleLines: SPLIT };
    handler = (_url, init) => (init.method === "POST" ? { status: 200, body: result, etag: '"4"' } : { status: 200, body: order(), etag: '"3"' });
    const { container } = renderAt(`/sales/orders/${ORDER_ID}/promise`);
    expect(await screen.findByRole("heading", { name: "Promise order SO-0000042" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Line"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Date"), { target: { value: "2027-03-01" } });
    fireEvent.change(screen.getByLabelText("Supply"), { target: { value: "40" } });
    fireEvent.click(screen.getByRole("button", { name: "Add bucket" }));
    fireEvent.change(screen.getByLabelText("Date", { selector: "#promise-1-date" }), { target: { value: "2027-03-04" } });
    fireEvent.change(screen.getByLabelText("Supply", { selector: "#promise-1-supply" }), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Promise line" }));

    expect(await screen.findByText("Line 1 was split: 1 later delivery on backorder.")).toBeInTheDocument();
    const table = await findTable({ name: "Schedule lines" });
    expect(within(table).getByText("2027-03-04")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/orders/${ORDER_ID}/lines/1:promise`);
    expect(post?.headers.get("If-Match")).toBe('"3"');
    expect(post?.body).toEqual({
      completeOnly: false,
      periods: [
        { date: "2027-03-01", supply: "40", demand: "0" },
        { date: "2027-03-04", supply: "5", demand: "0" },
      ],
    });
  });

  it("preselects the line named in the link from the backorder list", async () => {
    handler = () => ({ status: 200, body: order(), etag: '"3"' });
    renderAt(`/sales/orders/${ORDER_ID}/promise?line=2`);
    await screen.findByRole("heading", { name: "Promise order SO-0000042" });
    expect(screen.getByLabelText("Line")).toHaveValue("2");
  });

  it("refuses a line from the link that cannot be promised", async () => {
    handler = () => ({ status: 200, body: order(), etag: '"3"' });
    renderAt(`/sales/orders/${ORDER_ID}/promise?line=9`);
    await screen.findByRole("heading", { name: "Promise order SO-0000042" });
    fireEvent.change(screen.getByLabelText("Date"), { target: { value: "2027-03-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Promise line" }));
    expect(await screen.findByText("Choose the line to promise.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("offers a reload when someone else changed the order first", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? { status: 412, body: { status: 412, code: "SLS-0006", detail: "the order changed; reload it" } } : { status: 200, body: order(), etag: '"3"' };
    renderAt(`/sales/orders/${ORDER_ID}/promise`);
    await screen.findByRole("heading", { name: "Promise order SO-0000042" });
    fireEvent.change(screen.getByLabelText("Line"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Date"), { target: { value: "2027-03-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Promise line" }));
    expect(await screen.findByText("Someone else changed this order")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload order" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "GET")).toHaveLength(2));
  });

  it("only promises a confirmed order", async () => {
    handler = () => ({ status: 200, body: order({ status: "DRAFT" }), etag: '"1"' });
    renderAt(`/sales/orders/${ORDER_ID}/promise`);
    expect(await screen.findByText("Only a confirmed order can be promised")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Promise line" })).toBeNull();
  });

  it("does not promise a journal order", async () => {
    handler = () => ({ status: 200, body: order({ orderType: "JOURNAL" }), etag: '"3"' });
    renderAt(`/sales/orders/${ORDER_ID}/promise`);
    expect(await screen.findByText("A journal order is not promised")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Promise line" })).toBeNull();
  });

  it("shows a load failure", async () => {
    handler = () => ({ status: 404, body: { status: 404, code: "SLS-0100", detail: "order not found" } });
    renderAt(`/sales/orders/${ORDER_ID}/promise`);
    expect(await screen.findByText("Could not load the order")).toBeInTheDocument();
  });
});

describe("Schedule lines on the order detail", () => {
  it("lists the schedule lines of a confirmed order with a link to promise", async () => {
    handler = (url) => (url.pathname.endsWith("/schedule-lines") ? { status: 200, body: { items: SPLIT } } : { status: 200, body: order(), etag: '"3"' });
    renderAt(`/sales/orders/${ORDER_ID}`);
    const table = await findTable({ name: "Schedule lines" });
    expect(within(table).getByText("2027-03-04")).toBeInTheDocument();
    expect(within(table).getByText("Backorder")).toBeInTheDocument();
    expect(within(table).getByText("ATP bucket 2027-03-04")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Promise against ATP" })).toHaveAttribute("href", `/sales/orders/${ORDER_ID}/promise`);
  });

  it("shows a failure and retries", async () => {
    let fail = true;
    handler = (url) =>
      url.pathname.endsWith("/schedule-lines")
        ? fail
          ? { status: 500, body: { status: 500, code: "COM-0500", detail: "boom" } }
          : { status: 200, body: { items: SPLIT } }
        : { status: 200, body: order(), etag: '"3"' };
    renderAt(`/sales/orders/${ORDER_ID}`);
    expect(await screen.findByText("Could not load the schedule lines")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Schedule lines" })).toBeInTheDocument();
  });

  it("shows an empty state", async () => {
    handler = (url) => (url.pathname.endsWith("/schedule-lines") ? { status: 200, body: { items: [] } } : { status: 200, body: order(), etag: '"3"' });
    renderAt(`/sales/orders/${ORDER_ID}`);
    expect(await screen.findByRole("heading", { name: "No schedule lines" })).toBeInTheDocument();
  });

  it("is not shown for a draft order", async () => {
    handler = () => ({ status: 200, body: order({ status: "DRAFT" }), etag: '"1"' });
    renderAt(`/sales/orders/${ORDER_ID}`);
    await screen.findByRole("table", { name: "Order lines" });
    expect(screen.queryByRole("link", { name: "Promise against ATP" })).toBeNull();
  });
});
