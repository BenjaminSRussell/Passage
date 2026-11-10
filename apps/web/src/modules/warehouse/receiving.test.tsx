import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Receipt, ReceiptSummary, Received, Work } from "./api/client";
import { validateAsnDraft } from "./ReceivingPages";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const WH = "0190b2a4-7c1e-7d3a-9b5f-2a4c6e8f0a11";
const RECEIPT = "0190f0a0-0000-7000-8000-000000000701";
const WORK = "0190f0a0-0000-7000-8000-000000000702";

const pallet = (n: number) => ({
  lineNo: n,
  itemId: "BOLT",
  lpId: `LP-${n}`,
  expectedQty: "100",
  weight: "40",
  volume: "0",
  status: "EXPECTED" as const,
  receivedQty: "0",
  varianceQty: "0",
  variancePct: "0",
  discrepancy: false,
});

const asn3: Receipt = {
  id: RECEIPT,
  warehouseId: WH,
  asnRef: "ASN-3",
  tolerancePct: "2",
  status: "OPEN",
  version: 1,
  createdAt: "2026-10-27T08:00:00Z",
  updatedAt: "2026-10-27T08:00:00Z",
  lines: [pallet(1), pallet(2), pallet(3)],
};

const summary: ReceiptSummary = {
  id: RECEIPT,
  warehouseId: WH,
  asnRef: "ASN-3",
  tolerancePct: "2",
  status: "PARTIALLY_RECEIVED",
  lines: 3,
  received: 1,
  discrepancies: 1,
  createdAt: "2026-10-27T08:00:00Z",
  updatedAt: "2026-10-27T09:00:00Z",
};

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("ReceiptsPage", () => {
  it("lists receipts with their pallet counts and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/warehouse/receipts") return json(200, { items: [summary] });
      if (url.pathname === `/api/v1/warehouse/receipts/${RECEIPT}`) return json(200, asn3);
      return undefined;
    };
    const { container } = renderAt(`/warehouse/receipts?warehouse=${WH}`);
    const table = await findTable({ name: "Receipts" });
    await waitFor(() => expect(table).toHaveTextContent("ASN-3"));
    expect(table).toHaveTextContent("1 of 3");
    expect(table).toHaveTextContent("Partially received");
    expect(table).toHaveTextContent("±2%");
    expect(server.calls[0].url.searchParams.get("warehouseId")).toBe(WH);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "ASN-3" }));
    expect(await screen.findByRole("heading", { name: "ASN ASN-3" })).toBeInTheDocument();
  });

  it("asks for a warehouse, filters by status and shows the empty state", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/warehouse/receipts?warehouse=");
    expect(screen.getByText("Choose a warehouse")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    fill("Warehouse", WH);
    expect(await screen.findByText("No receipts")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "OPEN" } });
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("status")).toBe("OPEN"));
  });

  it("shows a load failure with a retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(403, { code: "AUTH-0003", detail: "missing duty warehouse.inquiry.view" }) : json(200, { items: [summary] }));
    renderAt(`/warehouse/receipts?warehouse=${WH}`);
    expect(await screen.findByText("missing duty warehouse.inquiry.view (AUTH-0003)")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Receipts" })).toHaveTextContent("ASN-3");
  });
});

describe("NewReceiptPage", () => {
  it("validates the pallets, then registers the ASN and opens the receipt", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname === "/api/v1/warehouse/receipts:from-asn") return json(201, asn3);
      if (url.pathname === `/api/v1/warehouse/receipts/${RECEIPT}`) return json(200, asn3);
      return undefined;
    };
    const { container } = renderAt(`/warehouse/receipts/new?warehouse=${WH}`);
    fireEvent.click(screen.getByRole("button", { name: "Register ASN" }));
    expect(await screen.findByText("Enter the ASN reference, at most 100 characters.")).toBeInTheDocument();
    expect(screen.getByText("Enter a quantity above 0.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fill("ASN reference", " ASN-3 ");
    fill("Pallet 1 item", "BOLT");
    fill("Pallet 1 license plate", "LP-1");
    fill("Pallet 1 expected quantity", "100");
    fill("Pallet 1 weight (kg)", "40");
    fireEvent.click(screen.getByRole("button", { name: "Add pallet" }));
    fill("Pallet 2 item", "BOLT");
    fill("Pallet 2 license plate", "LP-1");
    fill("Pallet 2 expected quantity", "100");
    fireEvent.click(screen.getByRole("button", { name: "Register ASN" }));
    expect(await screen.findByText("Another pallet uses this license plate.")).toBeInTheDocument();
    fill("Pallet 2 license plate", "LP-2");
    fill("Pallet 2 PO line", "PO-7/2");
    fill("Variance tolerance (%)", "5");
    fireEvent.click(screen.getByRole("button", { name: "Register ASN" }));

    expect(await screen.findByRole("heading", { name: "ASN ASN-3" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({
      warehouseId: WH,
      asnRef: "ASN-3",
      tolerancePct: "5",
      lines: [
        { lineNo: 1, itemId: "BOLT", lpId: "LP-1", expectedQty: "100", weight: "40" },
        { lineNo: 2, itemId: "BOLT", lpId: "LP-2", poLineRef: "PO-7/2", expectedQty: "100" },
      ],
    });
    expect(post && idempotencyKey(post)).toBeTruthy();
  });

  it("shows why the service refused the ASN", async () => {
    server.handler = () => json(409, { code: "WMS-DUPLICATE", detail: "a record with the same name, sequence, door code or ASN reference already exists" });
    renderAt(`/warehouse/receipts/new?warehouse=${WH}`);
    fill("ASN reference", "ASN-3");
    fill("Pallet 1 item", "BOLT");
    fill("Pallet 1 license plate", "LP-1");
    fill("Pallet 1 expected quantity", "100");
    fireEvent.click(screen.getByRole("button", { name: "Register ASN" }));
    expect(await screen.findByText(/already exists \(WMS-DUPLICATE\)/)).toBeInTheDocument();
  });

  it("refuses a tolerance above 20% and quantities with more than 6 decimals", () => {
    const errors = validateAsnDraft({
      warehouseId: WH,
      asnRef: "A",
      tolerancePct: "25",
      pallets: [{ itemId: "B", lpId: "LP 1", poLineRef: "", expectedQty: "1.0000001", weight: "", volume: "" }],
    });
    expect(errors.tolerancePct).toBeDefined();
    expect(errors["pallets.0.lpId"]).toBeDefined();
    expect(errors["pallets.0.expectedQty"]).toBeDefined();
  });
});

describe("ReceiptDetailPage", () => {
  it("receives pallet 2 at 95 of 100 and shows the −5% discrepancy and its put-away work (AC-31.7)", async () => {
    const work: Work = {
      id: WORK,
      warehouseId: WH,
      workType: "PURCHASE_PUTAWAY",
      status: "OPEN",
      priority: 50,
      version: 1,
      createdAt: "2026-10-27T09:00:00Z",
      updatedAt: "2026-10-27T09:00:00Z",
      lines: [],
    };
    const detail = "WMS-0107: Received 95 differs from ASN 100 by -5.00%; discrepancy recorded.";
    const after: Received = {
      receipt: {
        ...asn3,
        status: "PARTIALLY_RECEIVED",
        version: 2,
        lines: [
          pallet(1),
          { ...pallet(2), status: "RECEIVED", receivedQty: "95", varianceQty: "-5", variancePct: "-5", discrepancy: true, reason: detail, workId: WORK },
          pallet(3),
        ],
      },
      work,
      traces: [],
      warnings: [{ code: "WMS-0107", detail }],
    };
    server.handler = (url, init) => {
      if (init.method === "POST") return json(200, after);
      if (url.pathname === `/api/v1/warehouse/receipts/${RECEIPT}`) return json(200, asn3);
      return undefined;
    };
    const { container } = renderAt(`/warehouse/receipts/${RECEIPT}`);
    const table = await findTable({ name: "Pallets" });
    expect(table).toHaveTextContent("LP-2");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Receive pallet 2" }));
    expect(screen.getByLabelText("Quantity received")).toHaveValue("100");
    fill("Quantity received", "0");
    fireEvent.click(screen.getByRole("button", { name: "Receive pallet" }));
    expect(await screen.findByText("Enter the quantity that arrived, above 0.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);

    fill("Quantity received", "95");
    fireEvent.click(screen.getByRole("button", { name: "Receive pallet" }));
    expect(await screen.findByText(detail)).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/warehouse/receipts/${RECEIPT}/lines/2:receive`);
    expect(post?.body).toEqual({ qty: "95" });
    expect(new Headers(post?.init.headers).get("If-Match")).toBe('"1"');
    expect(post && idempotencyKey(post)).toBeTruthy();

    const rows = within(screen.getByRole("table", { name: "Pallets" })).getAllByRole("row");
    expect(rows[2]).toHaveTextContent("-5 (-5.00%)");
    expect(rows[2]).toHaveTextContent("Discrepancy");
    expect(within(rows[2]).getByRole("link", { name: "0190f0a0" })).toHaveAttribute("href", `/warehouse/work/${WORK}`);
    expect(screen.getByRole("button", { name: "Receive pallet 1" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Receive pallet 2" })).toBeNull();
  });

  it("reloads the receipt after a version conflict and keeps the same key on retry", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(409, { code: "WMS-VERSION-CONFLICT", detail: "store: version conflict: current version is 2" });
      if (url.pathname === `/api/v1/warehouse/receipts/${RECEIPT}`) return json(200, asn3);
      return undefined;
    };
    renderAt(`/warehouse/receipts/${RECEIPT}`);
    fireEvent.click(await screen.findByRole("button", { name: "Receive pallet 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Receive pallet" }));
    expect(await screen.findByText(/current version is 2 \(WMS-VERSION-CONFLICT\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Receive pallet" }));
    await waitFor(() => expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(2));
    const [a, b] = server.calls.filter((c) => c.method === "POST");
    expect(idempotencyKey(a)).toBe(idempotencyKey(b));
    expect(server.calls.filter((c) => c.method === "GET" && c.url.pathname.endsWith(RECEIPT)).length).toBeGreaterThanOrEqual(2);
  });

  it("shows a missing receipt", async () => {
    server.handler = () => json(404, { code: "WMS-RECEIPT-NOT-FOUND", detail: "not found" });
    renderAt(`/warehouse/receipts/${RECEIPT}`);
    expect(await screen.findByText("not found (WMS-RECEIPT-NOT-FOUND)")).toBeInTheDocument();
  });
});
