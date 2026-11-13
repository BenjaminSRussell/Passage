import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { toSpecInput } from "./ServicePages";
import type { ModelServiceSpec, PurchaseOrder, ServiceSpecResult } from "./api/client";
import { draftFromOutline, newOutlineRow, toOutlineInput } from "./outline";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const PO_ID = "66666666-6666-4666-8666-666666666666";
const LE = "11111111-1111-4111-8111-111111111111";
const MODEL = "77777777-7777-4777-8777-777777777777";
const BASE = "/api/v1/procurement";
const SERVICE_URL = `${BASE}/purchase-orders/${PO_ID}/lines/1/service`;
const PAGE = `/purchasing/purchase-orders/${PO_ID}/lines/1/service`;

afterEach(() => vi.unstubAllGlobals());

const order = (approvalStatus: PurchaseOrder["approvalStatus"]): PurchaseOrder => ({
  id: PO_ID,
  number: "PO-00000031",
  legalEntityId: LE,
  vendorId: "22222222-2222-4222-8222-222222222222",
  currency: "USD",
  approvalStatus,
  documentStatus: "NONE",
  versionNo: 0,
  changeManagement: true,
  subtotal: "1000.00",
  discountTotal: "0",
  chargesTotal: "0",
  taxTotal: "0",
  total: "1000.00",
  rowVersion: 6,
  lines: [
    { lineNo: 1, description: "Press maintenance", qty: "1", unit: "EA", unitPrice: "1000", priceUnit: "1", discountPct: "0", grossAmount: "1000.00", discountAmount: "0", lineAmount: "1000.00", taxAmount: "0", deliveryDate: "2027-02-01", priceSource: "MANUAL", status: "OPEN" },
    { lineNo: 2, description: "Washer", qty: "1", unit: "EA", unitPrice: "1", priceUnit: "1", discountPct: "0", grossAmount: "1", discountAmount: "0", lineAmount: "1", taxAmount: "0", deliveryDate: "2027-02-01", priceSource: "MANUAL", status: "CANCELLED" },
  ],
  charges: [],
  createdAt: "2026-10-26T10:00:00Z",
  updatedAt: "2026-10-26T10:00:00Z",
});

const spec: ServiceSpecResult = {
  purchaseOrderId: PO_ID,
  lineNo: 1,
  currency: "USD",
  lineAmount: "1000.00",
  valueLimit: "2000",
  expectedValue: "500",
  plannedTotal: "650",
  outline: [
    { seq: 1, depth: 1, kind: "LEVEL", description: "Maintenance", qty: "0", unitPrice: "0", amount: "650" },
    { seq: 2, parentSeq: 1, depth: 2, kind: "ITEM", description: "Inspection", qty: "3", unit: "h", unitPrice: "100", amount: "300" },
    { seq: 3, parentSeq: 1, depth: 2, kind: "ITEM", description: "Report", qty: "1", unit: "ea", unitPrice: "350", amount: "350" },
  ],
  updatedAt: "2026-10-26T10:00:00Z",
  poRowVersion: 7,
};

const model: ModelServiceSpec = {
  id: MODEL,
  legalEntityId: LE,
  code: "MAINT",
  name: "Maintenance visit",
  currency: "USD",
  status: "ACTIVE",
  plannedTotal: "650",
  outline: spec.outline,
  version: 3,
  createdAt: "2026-03-01T10:00:00Z",
  updatedAt: "2026-03-01T10:00:00Z",
};

type Init = RequestInit & { headers?: Record<string, string> };

function serve(status: PurchaseOrder["approvalStatus"], current: ServiceSpecResult | null, write: (url: string, init: Init) => Response = () => json(spec)) {
  return stubFetch((url, init) => {
    if (init.method === "PUT" || init.method === "DELETE") return write(url, init as Init);
    if (url === SERVICE_URL && current) return json(current, 200, { ETag: `"${current.poRowVersion}"` });
    if (url.endsWith("/service")) return problem(404, "PRC-SRV-NOT-FOUND", "line 1 has no service specification");
    if (url.startsWith(`${SERVICE_URL}/limit-check`)) {
      const base = { lineNo: 1, currency: "USD", valueLimit: "2000", unplannedEntered: "1900", valueRemaining: "100", remaining: "100" };
      return url.includes("entry=200")
        ? json({ ...base, entry: "200", allowed: false, code: "PRC-SRV-003", detail: "Unplanned services 2100 exceed limit 2000 on PO line 1" })
        : json({ ...base, entry: "100", allowed: true });
    }
    if (url.startsWith(`${BASE}/model-service-specs`)) return json({ items: [{ ...model, outline: [] }] });
    if (url.endsWith("/service-lines") || url.endsWith("/delivery-schedules") || url.endsWith("/invoicing-plans") || url.endsWith("/versions")) return json({ items: [] });
    return json(order(status), 200, { ETag: '"6"' });
  });
}

describe("outline editor rules", () => {
  const item = (depth: number, extra: Partial<ReturnType<typeof newOutlineRow>> = {}) =>
    newOutlineRow({ depth, description: "Inspection", qty: "3", unit: "h", unitPrice: "100", ...extra });

  it("nests rows under the level above them", () => {
    const rows = [
      newOutlineRow({ kind: "LEVEL", depth: 1, description: "Maintenance" }),
      item(2),
      item(2, { description: "", productId: LE, qty: "1.5", unitPrice: "20.25" }),
      item(1, { description: "Call-out" }),
    ];
    const built = toOutlineInput(rows);
    expect(built.errors).toEqual({});
    expect(built.outline).toEqual([
      {
        kind: "LEVEL",
        description: "Maintenance",
        children: [
          { kind: "ITEM", description: "Inspection", productId: undefined, qty: "3", unit: "h", unitPrice: "100" },
          { kind: "ITEM", description: undefined, productId: LE, qty: "1.5", unit: "h", unitPrice: "20.25" },
        ],
      },
      { kind: "ITEM", description: "Call-out", productId: undefined, qty: "3", unit: "h", unitPrice: "100" },
    ]);
  });

  it("names each malformed field and the nesting rules", () => {
    const built = toOutlineInput([
      item(2),
      newOutlineRow({ kind: "LEVEL", depth: 1, description: "" }),
      item(3, { qty: "0", unit: "", unitPrice: "x", productId: "p", description: "" }),
    ]);
    expect(built.outline).toBeUndefined();
    expect(Object.keys(built.errors).sort()).toEqual(
      [
        "outline[0].depth",
        "outline[1].description",
        "outline[2].depth",
        "outline[2].productId",
        "outline[2].qty",
        "outline[2].unit",
        "outline[2].unitPrice",
      ].sort(),
    );
    expect(toOutlineInput([item(1), item(2)]).errors["outline[1].depth"]).toMatch(/no rows below/);
    expect(toOutlineInput([item(1, { qty: "1.0000001" })]).errors["outline[0].qty"]).toMatch(/six decimals/);
    expect(toOutlineInput([]).outline).toEqual([]);
  });

  it("starts a replacement from the stored rows", () => {
    const rows = draftFromOutline(spec.outline);
    expect(rows.map((r) => [r.kind, r.depth, r.description, r.qty])).toEqual([
      ["LEVEL", 1, "Maintenance", ""],
      ["ITEM", 2, "Inspection", "3"],
      ["ITEM", 2, "Report", "1"],
    ]);
    expect(toOutlineInput(rows).errors).toEqual({});
  });

  it("builds the PUT body: a model replaces the rows, the limits are decimals", () => {
    const form = { valueLimit: "2000", expectedValue: "500", contractAgreementLineId: "", modelId: MODEL };
    expect(toSpecInput(form, [item(1, { qty: "x" })])).toEqual({ errors: {}, input: { valueLimit: "2000", expectedValue: "500", contractAgreementLineId: undefined, modelId: MODEL, outline: undefined } });
    expect(toSpecInput({ ...form, modelId: "", valueLimit: "lots" }, []).errors.valueLimit).toBeDefined();
    expect(toSpecInput({ ...form, modelId: "", expectedValue: "3000" }, []).errors.expectedValue).toMatch(/above the limit/);
    expect(toSpecInput({ ...form, modelId: "", contractAgreementLineId: "x" }, []).errors.contractAgreementLineId).toBeDefined();
  });
});

describe("service line page", () => {
  it("explains a plain line and saves an outline with the PO row version", async () => {
    const fetchMock = serve("DRAFT", null);
    const { container } = renderAt(PAGE);
    expect(await screen.findByRole("heading", { name: "Not a service line yet" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Service line: PO-00000031 line 1" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Unplanned limit (USD)"), { target: { value: "2000" } });
    fireEvent.change(screen.getByLabelText("Expected unplanned (USD)"), { target: { value: "500" } });
    fireEvent.click(screen.getByRole("button", { name: "Add row" }));
    const row = screen.getByRole("group", { name: "Outline row 1" });
    fireEvent.change(within(row).getByLabelText("Description"), { target: { value: "Inspection" } });
    fireEvent.change(within(row).getByLabelText("Quantity"), { target: { value: "3" } });
    fireEvent.change(within(row).getByLabelText("Unit"), { target: { value: "h" } });
    fireEvent.change(within(row).getByLabelText("Unit price"), { target: { value: "100" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save specification" }));
    expect(await screen.findByText("Service specification saved: planned services 650 USD, unplanned limit 2000 USD.")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "PUT") as [string, Init];
    expect(put[0]).toBe(SERVICE_URL);
    expect(put[1].headers?.["If-Match"]).toBe('"6"');
    expect(JSON.parse(String(put[1].body))).toEqual({
      valueLimit: "2000",
      expectedValue: "500",
      outline: [{ kind: "ITEM", description: "Inspection", qty: "3", unit: "h", unitPrice: "100" }],
    });
    expect(await findTable({ name: "Service outline" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Replace specification" })).toBeInTheDocument();
  });

  it("copies a model's outline instead of the rows", async () => {
    const fetchMock = serve("DRAFT", null);
    renderAt(PAGE);
    fireEvent.change(await screen.findByLabelText("Unplanned limit (USD)"), { target: { value: "100" } });
    fireEvent.change(await screen.findByLabelText("Outline from"), { target: { value: MODEL } });
    expect(await screen.findByText("The outline is copied from the model")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add row" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Save specification" }));
    await screen.findByText(/Service specification saved/);
    const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "PUT") as [string, Init];
    expect(JSON.parse(String(put[1].body))).toEqual({ valueLimit: "100", expectedValue: "0", modelId: MODEL });
  });

  it("validates before calling the service", async () => {
    const fetchMock = serve("DRAFT", null);
    renderAt(PAGE);
    fireEvent.change(await screen.findByLabelText("Unplanned limit (USD)"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Save specification" }));
    expect(await screen.findByText("Enter the limit as a decimal; 0 allows no unplanned services.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit).method === "PUT")).toBe(false);
  });

  it("shows the service's outline sum check with its code", async () => {
    serve("DRAFT", null, () => problem(422, "PRC-SRV-002", "the planned services (650) are worth more than the line (500)"));
    renderAt(PAGE);
    await screen.findByLabelText("Unplanned limit (USD)");
    fireEvent.click(screen.getByRole("button", { name: "Save specification" }));
    expect(await screen.findByText("the planned services (650) are worth more than the line (500)")).toBeInTheDocument();
  });

  it("shows the outline and the limits and removes the specification after confirmation", async () => {
    const fetchMock = serve("DRAFT", spec, () => new Response(null, { status: 204, headers: { ETag: '"8"' } }));
    const { container } = renderAt(PAGE);
    const table = await findTable({ name: "Service outline" });
    expect(within(table).getAllByRole("row")).toHaveLength(4);
    expect(within(table).getByText("Inspection")).toBeInTheDocument();
    expect(screen.getAllByText("2000 USD").length).toBeGreaterThan(0);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Remove specification" }));
    fireEvent.click(screen.getByRole("button", { name: "Yes, remove it" }));
    expect(await screen.findByText("Service specification removed. The line is a plain line again.")).toBeInTheDocument();
    const del = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "DELETE") as [string, Init];
    expect(del[0]).toBe(SERVICE_URL);
    expect(del[1].headers?.["If-Match"]).toBe('"7"');
    expect(screen.getByRole("heading", { name: "Not a service line yet" })).toBeInTheDocument();
  });

  it("checks an entry against the unplanned limit (AC-17.17)", async () => {
    const fetchMock = serve("CONFIRMED", spec);
    renderAt(PAGE);
    fireEvent.change(await screen.findByLabelText("Unplanned already entered"), { target: { value: "1900" } });
    fireEvent.change(screen.getByLabelText("This entry"), { target: { value: "200" } });
    fireEvent.click(screen.getByRole("button", { name: "Check entry" }));
    expect(await screen.findByText("Unplanned services 2100 exceed limit 2000 on PO line 1 (PRC-SRV-003). 100 USD can still be entered.")).toBeInTheDocument();
    expect(screen.queryByText(/The entry fits/)).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("This entry"), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "Check entry" }));
    expect(await screen.findByText(/The entry fits: 100 of the 2000 limit was still free/)).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url) === `${SERVICE_URL}/limit-check?unplanned=1900&entry=100`)).toBe(true);
    fireEvent.change(screen.getByLabelText("This entry"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Check entry" }));
    expect(await screen.findByText(/Enter the unplanned amount already entered/)).toBeInTheDocument();
  });

  it("offers no form on a confirmed order, a cancelled line or a missing line", async () => {
    serve("CONFIRMED", spec);
    const confirmed = renderAt(PAGE);
    expect(await screen.findByText("The specification is fixed")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Replace specification" })).toBeNull();
    confirmed.unmount();
    serve("DRAFT", null);
    const cancelled = renderAt(`/purchasing/purchase-orders/${PO_ID}/lines/2/service`);
    expect(await screen.findByText("The specification is fixed")).toBeInTheDocument();
    cancelled.unmount();
    renderAt(`/purchasing/purchase-orders/${PO_ID}/lines/9/service`);
    expect(await screen.findByRole("heading", { name: "Line 9 not found" })).toBeInTheDocument();
  });

  it("reports a stale order and a purchase order that cannot be loaded", async () => {
    serve("DRAFT", null, () => problem(412, "PRC-PO-009", "changed"));
    const stale = renderAt(PAGE);
    await screen.findByLabelText("Unplanned limit (USD)");
    fireEvent.click(screen.getByRole("button", { name: "Save specification" }));
    expect(await screen.findByText("Someone else changed this order. Reload the page and try again.")).toBeInTheDocument();
    stale.unmount();
    stubFetch(() => problem(404, "PRC-PO-NOT-FOUND", "purchase order not found"));
    renderAt(PAGE);
    expect(await screen.findByRole("heading", { name: "Purchase order not found" })).toBeInTheDocument();
  });

  it("reports a failure to load the specification with a retry", async () => {
    stubFetch((url) => (url === SERVICE_URL ? problem(500, "COM-0500", "boom") : json(order("DRAFT"))));
    renderAt(PAGE);
    expect(await screen.findByText(/Could not load the service specification/)).toBeInTheDocument();
  });
});

describe("model service specifications", () => {
  it("lists models, then opens one, with the empty state when there are none", async () => {
    let resolve: (r: Response) => void = () => undefined;
    stubFetch(() => new Promise<Response>((r) => (resolve = r)));
    const first = renderAt("/purchasing/model-service-specs");
    expect(await screen.findByRole("table", { name: "Model service specifications" })).toHaveAttribute("aria-busy", "true");
    resolve(json({ items: [{ ...model, outline: [] }] }));
    const row = await screen.findByText("MAINT");
    expect(screen.getByText("Maintenance visit")).toBeInTheDocument();
    fireEvent.click(row);
    await waitFor(() => expect(first.router.state.location.pathname).toBe(`/purchasing/model-service-specs/${MODEL}`));
    first.unmount();
    stubFetch(() => json({ items: [] }));
    const empty = renderAt("/purchasing/model-service-specs");
    expect(await screen.findByText("No model service specifications yet")).toBeInTheDocument();
    expect(pageViolations(empty.container)).toEqual([]);
  });

  it("filters by status and text and shows a load failure with retry", async () => {
    const fetchMock = stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/model-service-specs");
    await screen.findByText("No model service specifications yet");
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "INACTIVE" } });
    fireEvent.change(screen.getByLabelText("Code or name"), { target: { value: "maint" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url) === `${BASE}/model-service-specs?status=INACTIVE&q=maint`)).toBe(true));
    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "COM-0500", "boom"));
    renderAt("/purchasing/model-service-specs");
    expect(await screen.findByText(/Could not load model service specifications/)).toBeInTheDocument();
  });

  it("creates a model with an outline under one idempotency key", async () => {
    const fetchMock = stubFetch((_url, init) => (init.method === "POST" ? json(model, 201) : json(model)));
    const { router } = renderAt("/purchasing/model-service-specs/new");
    fireEvent.click(await screen.findByRole("button", { name: "Save model" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit).method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "maint" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Maintenance visit" } });
    const row = screen.getByRole("group", { name: "Outline row 1" });
    fireEvent.change(within(row).getByLabelText("Description"), { target: { value: "Inspection" } });
    fireEvent.change(within(row).getByLabelText("Quantity"), { target: { value: "3" } });
    fireEvent.change(within(row).getByLabelText("Unit"), { target: { value: "h" } });
    fireEvent.change(within(row).getByLabelText("Unit price"), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "Save model" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/model-service-specs/${MODEL}`));
    const post = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "POST") as [string, Init];
    expect(post[0]).toBe(`${BASE}/model-service-specs`);
    expect(post[1].headers?.["Idempotency-Key"]).toBeTruthy();
    expect(JSON.parse(String(post[1].body))).toEqual({
      legalEntityId: LE,
      code: "MAINT",
      name: "Maintenance visit",
      currency: "USD",
      status: "ACTIVE",
      outline: [{ kind: "ITEM", description: "Inspection", qty: "3", unit: "h", unitPrice: "100" }],
    });
  });

  it("shows the service's refusal of a duplicate code", async () => {
    stubFetch((_url, init) => (init.method === "POST" ? problem(409, "PRC-SRV-006", "the legal entity already has a model service specification MAINT") : json({ items: [] })));
    renderAt("/purchasing/model-service-specs/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "MAINT" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Maintenance visit" } });
    fireEvent.click(screen.getByRole("button", { name: "Remove row 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Save model" }));
    expect(await screen.findByText(/already has a model service specification MAINT/)).toBeInTheDocument();
  });

  it("changes a model under its version and reports a stale one", async () => {
    let status = 200;
    const fetchMock = stubFetch((_url, init) => {
      if (init.method === "PUT") return status === 200 ? json({ ...model, version: 4, name: "Maintenance v2" }) : problem(412, "PRC-SRV-007", "stale");
      return json(model);
    });
    const { container } = renderAt(`/purchasing/model-service-specs/${MODEL}`);
    expect(await screen.findByRole("heading", { name: "Model MAINT" })).toBeInTheDocument();
    expect(await findTable({ name: "Service outline" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Maintenance v2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/Model saved as version 4/)).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "PUT") as [string, Init];
    expect(put[0]).toBe(`${BASE}/model-service-specs/${MODEL}`);
    expect(put[1].headers?.["If-Match"]).toBe('"3"');
    expect(JSON.parse(String(put[1].body)).outline).toHaveLength(1);
    status = 412;
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/Someone else changed this model since you opened it/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload model" })).toBeInTheDocument();
  });

  it("reports a model that cannot be loaded", async () => {
    stubFetch(() => problem(404, "PRC-SRV-MODEL-NOT-FOUND", "model service specification not found"));
    renderAt(`/purchasing/model-service-specs/${MODEL}`);
    expect(await screen.findByText(/Could not load the model service specification/)).toBeInTheDocument();
  });
});
