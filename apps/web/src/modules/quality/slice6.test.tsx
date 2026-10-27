import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Order, ReceiptCheck } from "./api/client";
import quality from "./module";
import { prefill } from "./NewOrderPage";
import { receivingOrderLink } from "./ReceivingChecksPage";

const PO = "0195f0a0-0000-7000-8000-000000000601";
const LINE = "0195f0a0-0000-7000-8000-000000000602";
const ITEM = "0195f0a0-0000-7000-8000-000000000603";
const SITE = "0195f0a0-0000-7000-8000-000000000604";
const WH = "0195f0a0-0000-7000-8000-000000000605";
const ORDER = "0195f0a0-0000-7000-8000-000000000606";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const posts = () => calls.filter((c) => c.method === "POST");

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
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
        {quality.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const partial: ReceiptCheck = {
  id: "0195f0a0-0000-7000-8000-000000000610",
  poLineId: PO,
  batch: "C",
  receiptLineId: LINE,
  receivedQty: "600.000000",
  acceptedQty: "400.000000",
  coveredBefore: "0.000000",
  skip: false,
  coveredQty: "400.000000",
  normalQty: "200.000000",
  message: "Source inspection covers 400 of batch C; 200 needs receiving inspection.",
  checkedBy: "rcv-1",
  checkedAt: "2026-10-27T08:00:00Z",
};

const created: Order = {
  id: ORDER,
  trigger: "SOURCE_INSPECTION",
  sourceRef: { type: "po_line", id: PO },
  itemId: ITEM,
  siteId: SITE,
  warehouseId: WH,
  dims: { batch: "B-17" },
  lotQty: "1000.000000",
  sampleQty: "80.000000",
  uom: "KG",
  testGroupId: ORDER,
  testGroupVersion: 1,
  samplingMethod: "AQL",
  severity: "NORMAL",
  codeLetter: "J",
  acceptNo: 5,
  rejectNo: 6,
  status: "CREATED",
  version: 1,
  createdAt: "2026-10-27T08:00:00Z",
  certificateRequired: "NONE",
};

describe("receiving quality order for the uncovered quantity", () => {
  it("links a check that does not skip to a prefilled receiving order for the normal quantity", async () => {
    handler = (url, init) => {
      if (url.pathname !== "/api/v1/quality/receipt-checks") return undefined;
      return init.method === "POST" ? json(201, partial) : json(200, { items: [] });
    };
    const { container } = renderAt("/quality/receiving");
    await screen.findByText("No receipt lines checked yet");
    const form = screen.getByRole("form", { name: "Check a receipt line" });
    fireEvent.change(within(form).getByLabelText("PO line"), { target: { value: PO } });
    fireEvent.change(within(form).getByLabelText("Batch"), { target: { value: "C" } });
    fireEvent.change(within(form).getByLabelText("Receipt line"), { target: { value: LINE } });
    fireEvent.change(within(form).getByLabelText("Received quantity"), { target: { value: "600" } });
    fireEvent.click(within(form).getByRole("button", { name: "Check receipt" }));
    const link = await screen.findByRole("link", { name: "Create the receiving quality order for 200" });
    const href = new URL(link.getAttribute("href") ?? "", "http://localhost");
    expect(href.pathname).toBe("/quality/orders/new");
    expect(Object.fromEntries(href.searchParams)).toEqual({ trigger: "PURCHASE_RECEIPT", sourceType: "receipt_line", sourceId: LINE, batch: "C", lotQty: "200" });
    expect(pageViolations(container)).toEqual([]);
  });

  it("offers no order link when source inspection covers the receipt", async () => {
    handler = (url, init) => {
      if (url.pathname !== "/api/v1/quality/receipt-checks") return undefined;
      return init.method === "POST" ? json(201, { ...partial, skip: true, code: "QM-0128", normalQty: "0", coveredQty: "600" }) : json(200, { items: [] });
    };
    renderAt("/quality/receiving");
    await screen.findByText("No receipt lines checked yet");
    const form = screen.getByRole("form", { name: "Check a receipt line" });
    fireEvent.change(within(form).getByLabelText("PO line"), { target: { value: PO } });
    fireEvent.change(within(form).getByLabelText("Batch"), { target: { value: "C" } });
    fireEvent.change(within(form).getByLabelText("Receipt line"), { target: { value: LINE } });
    fireEvent.change(within(form).getByLabelText("Received quantity"), { target: { value: "600" } });
    fireEvent.click(within(form).getByRole("button", { name: "Check receipt" }));
    expect(await screen.findByText("Receiving inspection skipped")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Create the receiving quality order/ })).toBeNull();
  });

  it("builds the link from the check and the form reads it back", () => {
    const params = new URL(receivingOrderLink(partial), "http://localhost").searchParams;
    const values = prefill(params);
    expect(values).toMatchObject({ trigger: "PURCHASE_RECEIPT", sourceType: "receipt_line", sourceId: LINE, batch: "C", lotQty: "200", uom: "EA" });
    expect(prefill(new URLSearchParams("trigger=BOGUS")).trigger).toBe("MANUAL");
  });
});

describe("batch on the quality order", () => {
  it("creates a source inspection naming the PO line and the batch it covers", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/quality/test-groups") return json(200, { items: [] });
      if (url.pathname === "/api/v1/quality/quality-orders" && init.method === "POST") return json(201, created);
      return undefined;
    };
    renderAt(`/quality/orders/new?trigger=SOURCE_INSPECTION&sourceType=po_line&sourceId=${PO}&uom=KG`);
    expect(screen.getByLabelText("Trigger")).toHaveValue("SOURCE_INSPECTION");
    expect(screen.getByLabelText("Source id")).toHaveValue(PO);
    for (const [label, value] of [["Item id", ITEM], ["Site id", SITE], ["Warehouse id", WH], ["Lot quantity", "1000"]]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value } });
    }
    fireEvent.click(screen.getByRole("button", { name: "Create quality order" }));
    expect(await screen.findByText("A source inspection names the batch it inspects at the vendor.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);

    fireEvent.change(screen.getByLabelText(/^Batch/), { target: { value: " B-17 " } });
    fireEvent.click(screen.getByRole("button", { name: "Create quality order" }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].body).toEqual({
      trigger: "SOURCE_INSPECTION",
      sourceRef: { type: "po_line", id: PO },
      itemId: ITEM,
      siteId: SITE,
      warehouseId: WH,
      dims: { batch: "B-17" },
      lotQty: "1000",
      uom: "KG",
    });
  });

  it("refuses a batch longer than receipt checks match and maps the service's dims error to it", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/quality/test-groups") return json(200, { items: [] });
      if (init.method === "POST") return json(400, { code: "COM-0001", detail: "invalid", errors: { dims: "names must be 1 to 40 characters and values 1 to 60" } });
      return undefined;
    };
    renderAt(`/quality/orders/new?trigger=PURCHASE_RECEIPT&sourceType=receipt_line&sourceId=${LINE}&lotQty=200`);
    for (const [label, value] of [["Item id", ITEM], ["Site id", SITE], ["Warehouse id", WH]]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value } });
    }
    fireEvent.change(screen.getByLabelText(/^Batch/), { target: { value: "B".repeat(61) } });
    fireEvent.click(screen.getByRole("button", { name: "Create quality order" }));
    expect(await screen.findByText("Keep the batch to 60 characters.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);

    fireEvent.change(screen.getByLabelText(/^Batch/), { target: { value: "C" } });
    fireEvent.click(screen.getByRole("button", { name: "Create quality order" }));
    expect(await screen.findByText("names must be 1 to 40 characters and values 1 to 60")).toBeInTheDocument();
    expect(posts()[0].body).toMatchObject({ trigger: "PURCHASE_RECEIPT", sourceRef: { type: "receipt_line", id: LINE }, dims: { batch: "C" }, lotQty: "200" });
  });

  it("shows the order's batch on its detail page", async () => {
    handler = (url) => {
      if (url.pathname === `/api/v1/quality/quality-orders/${ORDER}`) {
        return json(200, {
          ...created,
          testGroup: { id: ORDER, code: "SRC-STEEL", version: 1, tests: [] },
          results: [],
          evaluation: { defectiveSamples: 0, passed: true, missingRequired: [] },
          certificateStatus: "NOT_REQUIRED",
        });
      }
      if (url.pathname === "/api/v1/quality/instruments") return json(200, { items: [] });
      if (url.pathname === "/api/v1/quality/samples") return json(200, { items: [] });
      if (url.pathname.endsWith("/certificates")) return json(200, { required: "NONE", certificateStatus: "NOT_REQUIRED", items: [] });
      return undefined;
    };
    renderAt(`/quality/orders/${ORDER}`);
    const term = await screen.findByText("Batch");
    expect(term.nextElementSibling).toHaveTextContent("B-17");
  });
});
