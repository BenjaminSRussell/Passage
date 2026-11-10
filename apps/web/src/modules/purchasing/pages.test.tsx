import { findStatus, findTable } from "../../test-utils";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { setLookupFetcher } from "../../shell/lookup/entities";
import { WorkContextProvider } from "../../shell/lookup/workContext";
import { WorkContext } from "../../shell/lookup/workContextState";
import purchasing from "./module";

const LE = "11111111-1111-4111-8111-111111111111";
const VENDOR = "22222222-2222-4222-8222-222222222222";
const PRODUCT = "33333333-3333-4333-8333-333333333333";
const REQ_ID = "44444444-4444-4444-8444-444444444444";
const PO_ID = "55555555-5555-4555-8555-555555555555";
const TA_ID = "66666666-6666-4666-8666-666666666666";

const json = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });

const problem = (status: number, code: string, detail: string) =>
  json({ type: `https://passage.dev/problems/${code}`, title: "Problem", status, code, detail }, status);

type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init: RequestInit = {}) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

// A plain MemoryRouter: the data router builds fetch Requests that jsdom's
// AbortSignal cannot satisfy on navigation.
function renderAt(path: string) {
  const router = { state: { location: { pathname: path } } };
  function Probe() {
    router.state.location.pathname = useLocation().pathname;
    return null;
  }
  const view = render(
    <WorkContextProvider identity="tester">
      <MemoryRouter initialEntries={[path]}>
        <Probe />
        <Routes>
          {purchasing.routes.map((r) => (
            <Route key={r.path} path={r.path} element={r.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </WorkContextProvider>,
  );
  return { router, ...view };
}

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

// Shell-level rules (one h1, nav, main, skip link) belong to the shell, which
// these page tests do not mount; everything else must pass.
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (root: ParentNode) => checkAxeAA(root).filter((v) => !shellRules.has(v.id));

const requisition = {
  id: REQ_ID,
  number: "PR-00000007",
  legalEntityId: LE,
  requesterId: "77777777-7777-4777-8777-777777777777",
  status: "DRAFT",
  currency: "USD",
  total: "5000",
  approverChain: [],
  version: 1,
  lines: [
    {
      id: "88888888-8888-4888-8888-888888888888",
      lineNo: 1,
      description: "Laptop",
      category: "IT",
      qty: "2",
      unit: "EA",
      estimatedUnitPrice: "2500",
      amount: "5000",
      requestedDate: "2026-11-02",
      status: "OPEN",
    },
  ],
  createdAt: "2026-10-26T10:00:00Z",
  updatedAt: "2026-10-26T10:00:00Z",
};

describe("requisition list", () => {
  it("shows a loading skeleton, then rows that open the detail page", async () => {
    let release: (r: Response) => void = () => undefined;
    stubFetch(() => new Promise<Response>((resolve) => (release = resolve)));
    const { router } = renderAt("/purchasing/requisitions");
    expect(await screen.findByRole("table", { name: "Requisitions" })).toHaveAttribute("aria-busy", "true");
    release(
      json({
        items: [
          { id: REQ_ID, number: "PR-00000007", legalEntityId: LE, requesterId: requisition.requesterId, status: "IN_REVIEW", currency: "USD", total: "5000", lineCount: 1, version: 2, createdAt: "2026-10-26T10:00:00Z" },
        ],
      }),
    );
    const table = await findTable({ name: "Requisitions" });
    await waitFor(() => expect(within(table).getByText("PR-00000007")).toBeInTheDocument());
    expect(within(table).getByText("In review")).toBeInTheDocument();
    expect(within(table).getByText("5000 USD")).toBeInTheDocument();
    fireEvent.click(within(table).getByRole("button", { name: "PR-00000007" }));
    expect(router.state.location.pathname).toBe(`/purchasing/requisitions/${REQ_ID}`);
  });

  it("explains an empty list", async () => {
    stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/requisitions");
    expect(await screen.findByRole("heading", { name: "No requisitions yet" })).toBeInTheDocument();
  });

  it("shows the problem code on error and retries", async () => {
    const fetchMock = stubFetch(() => problem(500, "COM-0500", "internal error"));
    renderAt("/purchasing/requisitions");
    expect(await screen.findByText(/internal error/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  });

  it("filters by status and by the caller's own requisitions", async () => {
    const fetchMock = stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/requisitions");
    await screen.findByRole("heading", { name: "No requisitions yet" });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "APPROVED" } });
    fireEvent.click(screen.getByLabelText("Only my requisitions"));
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/v1/procurement/requisitions?status=APPROVED&mine=true"));
  });
});

describe("new requisition", () => {
  it("validates before calling the service", async () => {
    const fetchMock = stubFetch(() => json({}));
    renderAt("/purchasing/requisitions/new");
    fireEvent.click(await screen.findByRole("button", { name: "Save requisition" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter a product id, or a description and a category.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("warns on tab close only once the form has unsaved lines", async () => {
    stubFetch(() => json({}));
    renderAt("/purchasing/requisitions/new");
    const description = await screen.findByLabelText("Description");
    const clean = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(clean);
    expect(clean.defaultPrevented).toBe(false);
    fireEvent.change(description, { target: { value: "Laptop" } });
    const dirty = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(dirty);
    expect(dirty.defaultPrevented).toBe(true);
  });

  it("is not dirty just because the working legal entity was filled in", async () => {
    window.localStorage.setItem("passage.workContext.tester", JSON.stringify({ legalEntityId: LE, siteId: "" }));
    stubFetch(() => json({ id: LE, name: "Acme", code: "ACME" }));
    renderAt("/purchasing/requisitions/new");
    const description = await screen.findByLabelText("Description");
    await new Promise((resolve) => setTimeout(resolve, 50));
    const untouched = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(untouched);
    expect(untouched.defaultPrevented).toBe(false);
    fireEvent.change(description, { target: { value: "Laptop" } });
    const edited = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(edited);
    expect(edited.defaultPrevented).toBe(true);
  });

  it("is not dirty when the working legal entity arrives after the form is open", async () => {
    stubFetch(() => json({ id: LE, name: "Acme", code: "ACME" }));
    let choose: (id: string) => void = () => undefined;
    function Late() {
      const [legalEntityId, setLegalEntityId] = useState("");
      choose = setLegalEntityId;
      return (
        <WorkContext.Provider value={{ identity: "late", legalEntityId, siteId: "", setLegalEntityId, setSiteId: () => undefined }}>
          <MemoryRouter initialEntries={["/purchasing/requisitions/new"]}>
            <Routes>
              {purchasing.routes.map((r) => (
                <Route key={r.path} path={r.path} element={r.element} />
              ))}
            </Routes>
          </MemoryRouter>
        </WorkContext.Provider>
      );
    }
    render(<Late />);
    const description = await screen.findByLabelText("Description");
    act(() => choose(LE));
    await waitFor(() => expect(screen.getByLabelText("Legal entity")).not.toHaveValue(""));
    const untouched = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(untouched);
    expect(untouched.defaultPrevented).toBe(false);
    fireEvent.change(description, { target: { value: "Laptop" } });
    const edited = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(edited);
    expect(edited.defaultPrevented).toBe(true);
  });

  it("opens More options and focuses the operating unit when it is invalid, and sends it when valid", async () => {
    const fetchMock = stubFetch((_url, init) => (init.method === "POST" ? json(requisition, 201) : json(requisition, 200, { ETag: '"1"' })));
    const { container } = renderAt("/purchasing/requisitions/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Laptop" } });
    fireEvent.change(screen.getByLabelText("Category"), { target: { value: "IT" } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "2" } });
    const details = container.querySelector("details")!;
    expect(details.open).toBe(false);
    fireEvent.change(screen.getByLabelText("Operating unit id"), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Save requisition" }));
    await waitFor(() => expect(screen.getByLabelText("Operating unit id")).toHaveFocus());
    expect(details.open).toBe(true);
    expect(fetchMock).not.toHaveBeenCalledWith(expect.stringContaining("requisitions"), expect.objectContaining({ method: "POST" }));

    fireEvent.change(screen.getByLabelText("Operating unit id"), { target: { value: VENDOR } });
    fireEvent.click(screen.getByRole("button", { name: "Save requisition" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit)?.method === "POST")).toBe(true));
    const post = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "POST")!;
    expect(JSON.parse(String((post[1] as RequestInit).body)).operatingUnitId).toBe(VENDOR);
  });

  it("posts the lines with an Idempotency-Key and opens the new requisition with its warnings", async () => {
    const fetchMock = stubFetch((_url, init) => {
      if (init.method === "POST") {
        return json({ ...requisition, warnings: [{ code: "PRC-REQ-003", lineNo: 1, detail: "No price found for Laptop; estimated price set to 0" }] }, 201);
      }
      return json(requisition, 200, { ETag: '"1"' });
    });
    const { router } = renderAt("/purchasing/requisitions/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Laptop" } });
    fireEvent.change(screen.getByLabelText("Category"), { target: { value: "IT" } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save requisition" }));

    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/requisitions/${REQ_ID}`));
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/procurement/requisitions");
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/[0-9a-f-]{36}/);
    const body = JSON.parse(String(init.body));
    expect(body).toMatchObject({ legalEntityId: LE, currency: "USD", lines: [{ description: "Laptop", category: "IT", qty: "2", unit: "EA" }] });
    expect(body.lines[0].estimatedUnitPrice).toBeUndefined();
    expect(await screen.findByText(/No price found for Laptop/)).toBeInTheDocument();
  });

  it("tells the requester the blank estimate can come from the last order and shows that warning", async () => {
    stubFetch((_url, init) => {
      if (init.method === "POST") {
        return json({ ...requisition, warnings: [{ code: "PRC-REQ-012", lineNo: 1, detail: "Estimated price taken from the last order of P1, PO-00000042 (2026-02-03)" }] }, 201);
      }
      return json(requisition, 200, { ETag: '"1"' });
    });
    renderAt("/purchasing/requisitions/new");
    expect(await screen.findByText(/or the price of the product's last approved order/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "0197c1f2-0000-7000-8000-0000000000aa" } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save requisition" }));
    expect(await screen.findByText(/taken from the last order of P1, PO-00000042/)).toBeInTheDocument();
  });

  it("adds and removes lines", async () => {
    stubFetch(() => json({}));
    renderAt("/purchasing/requisitions/new");
    fireEvent.click(await screen.findByRole("button", { name: "Add line" }));
    expect(screen.getByRole("group", { name: "Line 2" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove line 2" }));
    expect(screen.queryByRole("group", { name: "Line 2" })).not.toBeInTheDocument();
  });

  it("maps a service field error back onto the form", async () => {
    stubFetch(() =>
      json({ type: "t", title: "Unprocessable", status: 422, code: "COM-0001", detail: "legalEntityId must be a UUID", errors: { "lines[0].qty": "must be positive" } }, 422),
    );
    renderAt("/purchasing/requisitions/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Pens" } });
    fireEvent.change(screen.getByLabelText("Category"), { target: { value: "OFFICE" } });
    fireEvent.click(screen.getByRole("button", { name: "Save requisition" }));
    expect(await screen.findByText("must be positive")).toBeInTheDocument();
    expect(screen.getAllByRole("alert").some((a) => a.textContent?.includes("COM-0001"))).toBe(true);
  });
});

describe("requisition detail", () => {
  it("submits with If-Match and shows the approver chain", async () => {
    const fetchMock = stubFetch((_url, init) =>
      init.method === "POST"
        ? json({ ...requisition, status: "IN_REVIEW", version: 2, approverChain: ["MANAGER"] }, 200, { ETag: '"2"' })
        : json(requisition, 200, { ETag: '"1"' }),
    );
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    expect(await screen.findByRole("heading", { name: "Requisition PR-00000007" })).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Requisition lines" })).toHaveTextContent("Laptop");
    fireEvent.click(screen.getByRole("button", { name: "Submit for approval" }));
    expect(await screen.findByText("Submitted for approval to Manager.")).toBeInTheDocument();
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe(`/api/v1/procurement/requisitions/${REQ_ID}:submit`);
    expect((init.headers as Record<string, string>)["If-Match"]).toBe('"1"');
    expect(screen.getByRole("list", { name: "Approver chain" })).toHaveTextContent("1. Manager");
    expect(screen.queryByRole("button", { name: "Submit for approval" })).not.toBeInTheDocument();
  });

  it("links a consolidated line to the purchase order that orders it", async () => {
    const closed = { ...requisition, status: "CLOSED", lines: [{ ...requisition.lines[0], status: "CONSOLIDATED", purchaseOrderId: PO_ID, purchaseOrderLineNo: 2 }] };
    stubFetch(() => json(closed));
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    const lines = await findTable({ name: "Requisition lines" });
    expect(within(lines).getByText("Consolidated")).toBeInTheDocument();
    expect(within(lines).getByRole("link", { name: "Line 2 of the order" })).toHaveAttribute("href", `/purchasing/purchase-orders/${PO_ID}`);
  });

  it("shows the conflict code when someone else changed it", async () => {
    stubFetch((_url, init) =>
      init.method === "POST" ? problem(409, "PRC-VERSION-CONFLICT", "requisition is at version 2, not 1") : json(requisition),
    );
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Submit for approval" }));
    expect(await screen.findByText(/PRC-VERSION-CONFLICT/)).toBeInTheDocument();
  });

  it("explains a missing requisition", async () => {
    stubFetch(() => problem(404, "PRC-REQ-NOT-FOUND", "requisition not found"));
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    expect(await screen.findByRole("heading", { name: "Requisition not found" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to requisitions" })).toHaveAttribute("href", "/purchasing/requisitions");
  });
});

const purchaseOrder = {
  id: PO_ID,
  number: "PO-00000003",
  legalEntityId: LE,
  vendorId: VENDOR,
  currency: "USD",
  approvalStatus: "DRAFT",
  documentStatus: "NONE",
  versionNo: 0,
  changeManagement: true,
  subtotal: "1050",
  discountTotal: "100",
  chargesTotal: "34.5",
  taxTotal: "72",
  total: "1056.5",
  rowVersion: 1,
  lines: [
    { lineNo: 1, productId: PRODUCT, qty: "100", unit: "EA", unitPrice: "10", priceUnit: "1", discountPct: "10", grossAmount: "1000", discountAmount: "100", lineAmount: "900", taxAmount: "72", deliveryDate: "2026-12-01", priceSource: `TRADE_AGREEMENT:${TA_ID}`, status: "OPEN" },
    { lineNo: 2, description: "Setup", qty: "1", unit: "EA", unitPrice: "50", priceUnit: "1", discountPct: "0", grossAmount: "50", discountAmount: "0", lineAmount: "50", taxAmount: "0", deliveryDate: "2026-12-01", priceSource: "MANUAL", status: "OPEN" },
  ],
  charges: [{ seq: 1, calc: "FIXED", value: "25", amount: "25" }],
  createdAt: "2026-10-26T10:00:00Z",
  updatedAt: "2026-10-26T10:00:00Z",
};

describe("purchase orders", () => {
  it("lists orders and explains an empty list", async () => {
    stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/purchase-orders");
    expect(await screen.findByRole("heading", { name: "No purchase orders yet" })).toBeInTheDocument();
  });

  it("drops loaded pages and the cursor when the filter changes, then when it is cleared", async () => {
    const summary = (id: string, number: string) => ({
      id, number, legalEntityId: LE, vendorId: VENDOR, currency: "USD", approvalStatus: "DRAFT", documentStatus: "OPEN", total: "10.00", lineCount: 1, rowVersion: 1, createdAt: "2026-10-26T10:00:00Z",
    });
    const fetchMock = stubFetch((url) => {
      if (url.includes("approvalStatus=APPROVED")) return json({ items: [summary("c", "PO-C")] });
      if (url.includes("cursor=next")) return json({ items: [summary("b", "PO-B")] });
      return json({ items: [summary("a", "PO-A")], nextCursor: "next" });
    });
    renderAt("/purchasing/purchase-orders");
    expect(await screen.findByText("PO-A")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load more purchase orders" }));
    expect(await screen.findByText("PO-B")).toBeInTheDocument();
    expect(screen.getByText("PO-A")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Approval status"), { target: { value: "APPROVED" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("PO-C")).toBeInTheDocument();
    expect(screen.queryByText("PO-A")).toBeNull();
    expect(screen.queryByText("PO-B")).toBeNull();
    expect(screen.queryByRole("button", { name: "Load more purchase orders" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Clear all filters" }));
    expect(await screen.findByText("PO-A")).toBeInTheDocument();
    expect(screen.queryByText("PO-B")).toBeNull();
    expect(screen.queryByText("PO-C")).toBeNull();
    expect(screen.getByRole("button", { name: "Load more purchase orders" })).toBeInTheDocument();
    const urls = fetchMock.mock.calls.map(([url]) => String(url));
    expect(urls[urls.length - 1]).not.toContain("cursor=");
  });

  it("keeps the loaded rows when Load more fails", async () => {
    const summary = (id: string, number: string) => ({
      id, number, legalEntityId: LE, vendorId: VENDOR, currency: "USD", approvalStatus: "DRAFT", documentStatus: "OPEN", total: "10.00", lineCount: 1, rowVersion: 1, createdAt: "2026-10-26T10:00:00Z",
    });
    stubFetch((url) => (url.includes("cursor=next") ? json({ code: "COM-0503", detail: "down" }, 503) : json({ items: [summary("a", "PO-A")], nextCursor: "next" })));
    renderAt("/purchasing/purchase-orders");
    expect(await screen.findByText("PO-A")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load more purchase orders" }));
    expect(await screen.findByText("Could not load purchase orders")).toBeInTheDocument();
    expect(screen.getByText("PO-A")).toBeInTheDocument();
  });

  it("creates an order with a trade-agreement-priced line and a charge", async () => {
    const fetchMock = stubFetch((_url, init) => (init.method === "POST" ? json(purchaseOrder, 201) : json(purchaseOrder)));
    const { router } = renderAt("/purchasing/purchase-orders/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Vendor"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "Add charge" }));
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "25" } });
    fireEvent.click(screen.getByRole("button", { name: "Save purchase order" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/purchase-orders/${PO_ID}`));
    const body = JSON.parse(String((fetchMock.mock.calls.find(([, i]) => (i as RequestInit).method === "POST") as [string, RequestInit])[1].body));
    expect(body.lines[0]).toMatchObject({ productId: PRODUCT, qty: "100", unit: "EA", priceUnit: "1" });
    expect(body.lines[0].unitPrice).toBeUndefined();
    expect(body.charges).toEqual([{ calc: "FIXED", value: "25" }]);
  });

  it("takes the unit from the picked product's base unit and still lets the person change it", async () => {
    setLookupFetcher(async () => json({ items: [{ id: PRODUCT, productNumber: "BOLT", name: "Hex bolt", baseUom: "KG" }] }));
    const fetchMock = stubFetch((_url, init) => (init.method === "POST" ? json(purchaseOrder, 201) : json(purchaseOrder)));
    const { router } = renderAt("/purchasing/purchase-orders/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Vendor"), { target: { value: VENDOR } });
    expect(screen.getByLabelText("Unit")).toHaveValue("EA");
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "bolt" } });
    fireEvent.click(await screen.findByText("Hex bolt"));
    expect(screen.getByLabelText("Unit")).toHaveValue("KG");
    fireEvent.change(screen.getByLabelText("Unit"), { target: { value: "BOX" } });
    fireEvent.click(screen.getByRole("button", { name: "Save purchase order" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/purchase-orders/${PO_ID}`));
    const body = JSON.parse(String((fetchMock.mock.calls.find(([, i]) => (i as RequestInit).method === "POST") as [string, RequestInit])[1].body));
    expect(body.lines[0]).toMatchObject({ productId: PRODUCT, unit: "BOX" });
  });

  it("keeps a typed unit when a product is picked, and explains tracking under the product", async () => {
    setLookupFetcher(async (url) =>
      json(url.includes("item-groups") ? { items: [{ id: "g1", name: "Raw material" }] } : { items: [{ id: PRODUCT, productNumber: "BOLT", name: "Hex bolt", baseUom: "KG", itemGroupId: "g1", serialTracked: true }] }),
    );
    stubFetch(() => json({}));
    renderAt("/purchasing/purchase-orders/new");
    fireEvent.change(await screen.findByLabelText("Unit"), { target: { value: "BOX" } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "bolt" } });
    fireEvent.click(await screen.findByText("Hex bolt"));
    expect(screen.getByLabelText("Unit")).toHaveValue("BOX");
    const note = await screen.findByText("Item group: Raw material. Serial tracked: serial numbers are recorded at receipt.");
    expect(screen.getByLabelText("Product").getAttribute("aria-describedby")).toContain(note.id);
  });

  it("keeps the typed unit when the product is pasted by id", async () => {
    stubFetch(() => json({}));
    renderAt("/purchasing/purchase-orders/new");
    fireEvent.change(await screen.findByLabelText("Unit"), { target: { value: "BOX" } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT } });
    expect(screen.getByLabelText("Unit")).toHaveValue("BOX");
  });

  it("requires a unit price on a line without a product", async () => {
    const fetchMock = stubFetch(() => json({}));
    renderAt("/purchasing/purchase-orders/new");
    fireEvent.change(await screen.findByLabelText("Description"), { target: { value: "Freight" } });
    fireEvent.click(screen.getByRole("button", { name: "Save purchase order" }));
    expect(await screen.findByText("A line without a product needs a unit price.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("shows PRC-PRICE-001 when no agreement prices the line", async () => {
    stubFetch(() => problem(422, "PRC-PRICE-001", "no trade agreement price; enter a unit price"));
    renderAt("/purchasing/purchase-orders/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Vendor"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT } });
    fireEvent.click(screen.getByRole("button", { name: "Save purchase order" }));
    expect(await screen.findByText(/PRC-PRICE-001/)).toBeInTheDocument();
  });

  it("releases a line against an agreement and shows the PRC-AGR-004 warning after create", async () => {
    const AGR_LINE = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
    const released = {
      ...purchaseOrder,
      lines: [{ ...purchaseOrder.lines[0], priceSource: `AGREEMENT:${AGR_LINE}`, agreementLineId: AGR_LINE }],
      warnings: [{ code: "PRC-AGR-004", lineNo: 1, detail: "Agreement line 1 is at 95 % of commitment" }],
    };
    const fetchMock = stubFetch((_url, init) => (init.method === "POST" ? json(released, 201) : json(released)));
    const { router } = renderAt("/purchasing/purchase-orders/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Vendor"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT } });
    fireEvent.change(screen.getByLabelText("Agreement line id"), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Save purchase order" }));
    expect(await screen.findByText("Agreement line id must be a UUID.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, i]) => (i as RequestInit).method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("Agreement line id"), { target: { value: AGR_LINE } });
    fireEvent.click(screen.getByRole("button", { name: "Save purchase order" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/purchase-orders/${PO_ID}`));
    expect(JSON.parse(String((fetchMock.mock.calls.find(([, i]) => (i as RequestInit).method === "POST") as [string, RequestInit])[1].body)).lines[0].agreementLineId).toBe(AGR_LINE);
    expect(await screen.findByText(/at 95 % of commitment \(PRC-AGR-004\)/)).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Purchase order lines" })).getByText("Purchase agreement")).toBeInTheDocument();
  });

  it("shows totals, price sources and charges on the detail page", async () => {
    stubFetch(() => json(purchaseOrder));
    const { container } = renderAt(`/purchasing/purchase-orders/${PO_ID}`);
    expect(await screen.findByRole("heading", { name: "Purchase order PO-00000003" })).toBeInTheDocument();
    const totals = screen.getByLabelText("Totals");
    expect(totals).toHaveTextContent("1056.5 USD");
    expect(totals).toHaveTextContent("−100 USD");
    const lines = screen.getByRole("table", { name: "Purchase order lines" });
    expect(within(lines).getByRole("link", { name: "Trade agreement" })).toHaveAttribute("href", `/purchasing/trade-agreements/${TA_ID}`);
    expect(within(lines).getByText("Manual")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Charges" })).toHaveTextContent("Fixed");
    expect(pageViolations(container)).toEqual([]);
  });
});

const agreement = {
  id: TA_ID,
  relation: "PRICE",
  accountCode: "VENDOR",
  accountId: VENDOR,
  itemCode: "PRODUCT",
  itemId: PRODUCT,
  currency: "USD",
  unit: "EA",
  fromQty: "0",
  toQty: "100",
  price: "10",
  priceUnit: "1",
  validFrom: "2026-01-01",
  consignment: false,
  status: "ACTIVE",
};

describe("trade agreements", () => {
  it("lists rows with their level, break and validity", async () => {
    stubFetch(() => json({ items: [agreement] }));
    renderAt("/purchasing/trade-agreements");
    const table = await findTable({ name: "Trade agreements" });
    await waitFor(() => expect(within(table).getByText("10 USD / 1 EA")).toBeInTheDocument());
    expect(within(table).getByText("0 to under 100")).toBeInTheDocument();
    expect(within(table).getByText("Open")).toBeInTheDocument();
    expect(within(table).getByText("Active")).toBeInTheDocument();
  });

  it("creates an all-vendors row without ids and reports an overlap", async () => {
    const fetchMock = stubFetch(() => problem(409, "PRC-TA-002", "overlapping trade agreement row"));
    renderAt("/purchasing/trade-agreements/new");
    fireEvent.change(await screen.findByLabelText("Vendor level"), { target: { value: "ALL" } });
    expect(screen.queryByLabelText("Vendor or group id")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Item level"), { target: { value: "ALL" } });
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "8" } });
    fireEvent.click(screen.getByRole("button", { name: "Save price row" }));
    expect(await screen.findByText(/PRC-TA-002/)).toBeInTheDocument();
    const body = JSON.parse(String((fetchMock.mock.calls[0] as [string, RequestInit])[1].body));
    expect(body).toMatchObject({ relation: "PRICE", accountCode: "ALL", itemCode: "ALL", price: "8", currency: "USD", unit: "EA" });
    expect(body.accountId).toBeUndefined();
  });

  it("rejects a to quantity at or below the from quantity", async () => {
    const fetchMock = stubFetch(() => json({}));
    renderAt("/purchasing/trade-agreements/new");
    fireEvent.change(await screen.findByLabelText("Vendor or group id"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Product or group id"), { target: { value: PRODUCT } });
    fireEvent.change(screen.getByLabelText("Price"), { target: { value: "8" } });
    fireEvent.change(screen.getByLabelText("From quantity"), { target: { value: "100" } });
    fireEvent.change(screen.getByLabelText("To quantity"), { target: { value: "50" } });
    fireEvent.click(screen.getByRole("button", { name: "Save price row" }));
    expect(await screen.findByText("Enter a quantity above the from quantity.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("shows one row on its detail page", async () => {
    stubFetch(() => json(agreement));
    renderAt(`/purchasing/trade-agreements/${TA_ID}`);
    expect(await screen.findByText("10 USD per 1 EA")).toBeInTheDocument();
    expect(screen.getByText("Open-ended")).toBeInTheDocument();
  });
});

describe("price lookup", () => {
  it("explains the winner and every candidate", async () => {
    const fetchMock = stubFetch(() =>
      json({
        winner: agreement,
        candidates: [
          { agreementId: TA_ID, specificity: 8, matched: true, rank: 1, reason: "" },
          { agreementId: "99999999-9999-4999-8999-999999999999", specificity: 0, matched: true, rank: 2, reason: "lower rank than a more specific row" },
        ],
      }),
    );
    renderAt("/purchasing/pricing");
    fireEvent.change(await screen.findByLabelText("Vendor id"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Product id"), { target: { value: PRODUCT } });
    fireEvent.change(screen.getByLabelText("Price date"), { target: { value: "2026-03-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Find price" }));
    expect(await findStatus()).toHaveTextContent("10 USD per 1 EA");
    const table = screen.getByRole("table", { name: "Candidates considered" });
    expect(within(table).getByText("Winner")).toBeInTheDocument();
    expect(within(table).getByText("lower rank than a more specific row")).toBeInTheDocument();
    expect(String(fetchMock.mock.calls[0][0])).toBe(
      `/api/v1/procurement/pricing/lookup?vendor=${VENDOR}&product=${PRODUCT}&qty=1&date=2026-03-01&currency=USD&unit=EA`,
    );
  });

  it("says when nothing matches", async () => {
    stubFetch(() => json({ winner: null, candidates: [] }));
    renderAt("/purchasing/pricing");
    fireEvent.change(await screen.findByLabelText("Vendor id"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Product id"), { target: { value: PRODUCT } });
    fireEvent.click(screen.getByRole("button", { name: "Find price" }));
    expect(await screen.findByRole("heading", { name: "No trade agreement matches" })).toBeInTheDocument();
  });

  it("validates group ids before calling the service", async () => {
    const fetchMock = stubFetch(() => json({}));
    renderAt("/purchasing/pricing");
    fireEvent.change(await screen.findByLabelText("Vendor group ids"), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Find price" }));
    expect(await screen.findByText("Enter UUIDs separated by commas.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("workspace", () => {
  it("counts each queue and marks a full page with +", async () => {
    stubFetch((url) => {
      if (url.includes("status=IN_REVIEW")) return json({ items: [{}, {}], nextCursor: undefined });
      if (url.includes("status=APPROVED")) return json({ items: new Array(200).fill({}), nextCursor: "x" });
      if (url.includes("approvalStatus=DRAFT")) return json({ items: [{}] });
      if (url.includes("approvalStatus=IN_REVIEW")) return json({ items: [{}, {}, {}, {}] });
      if (url.includes("sourcing=NEEDS_SOURCING")) return json({ items: [{}, {}, {}] });
      if (url.includes("status=EFFECTIVE")) return json({ items: [{}] });
      if (url.includes("/rfqs?status=SENT%2CREPLIES_OPEN")) return json({ items: [{}, {}, {}, {}, {}] });
      if (url.includes("/inquiries/near-limit-agreements")) return json({ items: [{}, {}, {}, {}, {}, {}] });
      if (url.includes("/inquiries/change-orders")) return json({ items: [{}, {}] });
      return json({ items: [] });
    });
    const { container } = renderAt("/purchasing");
    const tiles = await screen.findByRole("list", { name: "Procurement tiles" });
    expect(tiles).toHaveTextContent("Requisitions in review 2");
    expect(tiles).toHaveTextContent("Approved requisitions to source 200+");
    expect(tiles).toHaveTextContent("Draft purchase orders 1");
    expect(tiles).toHaveTextContent("Purchase orders in review 4");
    expect(tiles).toHaveTextContent("Purchase orders to confirm 0");
    expect(tiles).toHaveTextContent("Active price rows 0");
    expect(tiles).toHaveTextContent("Lines needing a vendor 3");
    expect(tiles).toHaveTextContent("Effective purchase agreements 1");
    expect(tiles).toHaveTextContent("RFQs awaiting replies 5");
    expect(tiles).toHaveTextContent("Agreement lines at 90 % or more 6");
    expect(tiles).toHaveTextContent("Changes waiting for re-approval 2");
    expect(screen.getByRole("link", { name: "New RFQ" })).toHaveAttribute("href", "/purchasing/rfqs/new");
    expect(screen.getByRole("link", { name: "Consolidate approved lines" })).toHaveAttribute("href", "/purchasing/consolidation");
    expect(screen.getByRole("link", { name: "Purchase agreements" })).toHaveAttribute("href", "/purchasing/agreements");
    expect(screen.getByRole("link", { name: "New purchase order" })).toHaveAttribute("href", "/purchasing/purchase-orders/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an error with its code when the counts cannot load", async () => {
    stubFetch(() => problem(503, "COM-0503", "unavailable"));
    renderAt("/purchasing");
    expect(await screen.findByText(/unavailable/)).toBeInTheDocument();
  });
});
