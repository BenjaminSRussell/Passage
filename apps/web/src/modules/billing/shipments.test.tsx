import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Invoice, InvoiceTrace } from "./api/client";
import { emptyShipment, toShipmentInput, validateShipment, type ShipmentValues } from "./NewShipmentProposalPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "0190f0a0-0000-7000-8000-000000000002";
const ACCT = "0190f0a0-0000-7000-8000-000000000003";
const SLIP = "0190f0a0-0000-7000-8000-000000000101";
const ORDER = "0190f0a0-0000-7000-8000-000000000201";
const SLIP_LINE = "0190f0a0-0000-7000-8000-000000005001";
const ORDER_LINE = "0190f0a0-0000-7000-8000-000000006001";
const PRODUCT = "0190f0a0-0000-7000-8000-000000007001";
const INV = "0190f0a0-0000-7000-8000-000000000901";
const INV2 = "0190f0a0-0000-7000-8000-000000000902";

const shipment: Invoice = {
  id: INV,
  legalEntityId: LE,
  customerId: CUST,
  invoiceAccountId: ACCT,
  type: "SHIPMENT",
  status: "PROPOSAL",
  currency: "EUR",
  invoiceDate: "2027-03-10",
  dueDate: "2027-04-09",
  paymentTermDays: 30,
  totals: { net: "100.00", charges: "12.50", tax: "19.00", gross: "131.50" },
  lines: [
    {
      lineNo: 1,
      description: "Widget",
      qty: "2",
      unitPrice: "50",
      discountPct: "0",
      net: "100.00",
      tax: "19.00",
      revenueAccount: "4000",
      taxCode: "DE19",
      packingSlipId: SLIP,
      packingSlipLineId: SLIP_LINE,
      orderId: ORDER,
      orderLineId: ORDER_LINE,
      productId: PRODUCT,
      priceExplainRef: "px-1",
    },
  ],
  installments: [],
  version: 1,
};

function filled(): ShipmentValues {
  const v = emptyShipment("2027-03-10");
  return {
    ...v,
    legalEntityId: LE,
    customerId: CUST,
    invoiceAccountId: ACCT,
    slips: [
      {
        id: SLIP,
        orderId: ORDER,
        date: "2027-03-09",
        chargeAmount: "12.50",
        lines: [
          {
            id: SLIP_LINE,
            orderLineId: ORDER_LINE,
            productId: PRODUCT,
            description: "Widget",
            qty: "2",
            unitPrice: "50",
            discountPct: "",
            revenueAccount: "4000",
            taxCode: "DE19",
            priceExplainRef: "px-1",
          },
        ],
      },
    ],
  };
}

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

function fillForm() {
  fill("Legal entity id", LE);
  fill("Customer id", CUST);
  fill("Invoice account id", ACCT);
  fill("Invoice date", "2027-03-10");
  fill("Packing slip id", SLIP);
  fill("Sales order id", ORDER);
  fill("Slip date", "2027-03-09");
  fill("Slip charge", "12.50");
  fill("Slip line id", SLIP_LINE);
  fill("Order line id", ORDER_LINE);
  fill("Product id", PRODUCT);
  fill("Description", "Widget");
  fill("Shipped quantity", "2");
  fill("Unit price", "50");
  fill("Revenue account", "4000");
  fill("Tax code", "DE19");
  fill("Price explanation", "px-1");
}

describe("shipment proposal rules", () => {
  it("accepts a complete request and builds the service body", () => {
    expect(validateShipment(filled())).toEqual({});
    const input = toShipmentInput(filled());
    expect(input).toMatchObject({ consolidate: false, paymentTermDays: 30, slips: [{ id: SLIP, chargeAmount: "12.50", lines: [{ id: SLIP_LINE, priceExplainRef: "px-1" }] }] });
    expect(input.slips[0].lines[0]).not.toHaveProperty("discountPct");
  });

  it("refuses a slip dated after the invoice (BIL-0006) and a slip line listed twice", () => {
    const v = filled();
    v.slips[0].date = "2027-03-11";
    v.slips.push({ ...v.slips[0], id: "0190f0a0-0000-7000-8000-000000000102", date: "2027-03-09" });
    const errors = validateShipment(v);
    expect(errors["slips[0].date"]).toMatch(/BIL-0006/);
    expect(errors["slips[1].lines[0].id"]).toMatch(/invoiced once/);
  });
});

describe("Invoice shipments", () => {
  it("shows the field errors and sends nothing when the form is incomplete", async () => {
    const { container } = renderAt("/billing/proposals/new");
    expect(await screen.findByRole("heading", { name: "Invoice shipments" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create proposals" }));
    expect(await screen.findByText("Enter the packing slip id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter the shipped quantity, above zero.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("creates one proposal with an Idempotency-Key and opens it", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/billing/proposals" && init.method === "POST") return json(201, { items: [shipment] });
      if (url.pathname === `/api/v1/billing/invoices/${INV}`) return json(200, shipment);
      return undefined;
    };
    renderAt("/billing/proposals/new");
    fillForm();
    fireEvent.change(screen.getByLabelText("Invoicing"), { target: { value: "yes" } });
    fireEvent.click(screen.getByRole("button", { name: "Create proposals" }));
    expect(await screen.findByRole("heading", { name: "Proposal" })).toBeInTheDocument();
    const call = server.calls.find((c) => c.url.pathname === "/api/v1/billing/proposals");
    expect(call && idempotencyKey(call)).toBeTruthy();
    expect(call?.body).toMatchObject({
      legalEntityId: LE,
      consolidate: true,
      slips: [{ id: SLIP, orderId: ORDER, date: "2027-03-09", chargeAmount: "12.50", lines: [{ id: SLIP_LINE, orderLineId: ORDER_LINE, productId: PRODUCT, taxCode: "DE19" }] }],
    });
  });

  it("lists the invoices when the slips became several proposals", async () => {
    server.handler = (url) =>
      url.pathname === "/api/v1/billing/proposals" ? json(201, { items: [shipment, { ...shipment, id: INV2, totals: { ...shipment.totals, gross: "238.00" } }] }) : undefined;
    const { container } = renderAt("/billing/proposals/new");
    fillForm();
    fireEvent.click(screen.getByRole("button", { name: "Create proposals" }));
    const table = await findTable({ name: "Created invoice proposals" });
    expect(table).toHaveTextContent("131.50 EUR");
    expect(table).toHaveTextContent("238.00 EUR");
    expect(screen.getByText("Created 2 invoice proposals.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains a slip line that is already invoiced and keeps the form", async () => {
    server.handler = () => json(409, { code: "BIL-0104", detail: "a packing slip line of this request is already on an invoice or proposal" });
    renderAt("/billing/proposals/new");
    fillForm();
    fireEvent.click(screen.getByRole("button", { name: "Create proposals" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("A packing slip line is already invoiced");
    expect(alert).toHaveTextContent("BIL-0104");
    expect(screen.getByLabelText("Packing slip id")).toHaveValue(SLIP);
  });

  it("puts the service's field errors next to the slip line fields", async () => {
    server.handler = () => json(400, { code: "COM-0001", detail: "request failed validation", errors: { "slips[0].lines[0].revenueAccount": "is required (BIL-0004)" } });
    renderAt("/billing/proposals/new");
    fillForm();
    fireEvent.click(screen.getByRole("button", { name: "Create proposals" }));
    expect(await screen.findByText("is required (BIL-0004)")).toBeInTheDocument();
  });

  it("adds and removes packing slips and lines", async () => {
    renderAt("/billing/proposals/new");
    fireEvent.click(screen.getByRole("button", { name: "Add packing slip" }));
    expect(screen.getAllByLabelText("Packing slip id")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "Add line to slip 2" }));
    expect(screen.getAllByLabelText("Slip line id")).toHaveLength(3);
    fireEvent.click(screen.getByRole("button", { name: "Remove packing slip 1" }));
    expect(screen.getAllByLabelText("Packing slip id")).toHaveLength(1);
    expect(screen.getAllByLabelText("Slip line id")).toHaveLength(2);
  });
});

const trace: InvoiceTrace = {
  invoice: { ...shipment, type: "CONSOLIDATED" },
  packingSlips: [{ id: SLIP, date: "2027-03-09", orderId: ORDER }],
  lines: [
    {
      lineNo: 1,
      description: "Widget",
      qty: "2",
      net: "100.00",
      revenueAccount: "4000",
      packingSlipId: SLIP,
      packingSlipLineId: SLIP_LINE,
      orderId: ORDER,
      orderLineId: ORDER_LINE,
      productId: PRODUCT,
      priceExplainRef: "px-1",
    },
  ],
};

describe("Invoice trace", () => {
  it("shows each line with its packing slip, order line and price explanation", async () => {
    server.handler = (url) => (url.pathname === `/api/v1/billing/invoices/${INV}/trace` ? json(200, trace) : undefined);
    const { container } = renderAt(`/billing/invoices/${INV}/trace`);
    expect(await screen.findByRole("heading", { name: "Trace of Proposal" })).toBeInTheDocument();
    const slips = screen.getByRole("table", { name: "Packing slips billed" });
    expect(slips).toHaveTextContent(SLIP);
    expect(slips).toHaveTextContent("2027-03-09");
    const lines = screen.getByRole("table", { name: "Trace lines" });
    expect(lines).toHaveTextContent("Order line");
    expect(lines).toHaveTextContent("px-1");
    expect(screen.getByRole("link", { name: "Open invoice" })).toHaveAttribute("href", `/billing/invoices/${INV}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when a free-text invoice has no packing slips behind it", async () => {
    server.handler = () =>
      json(200, { invoice: { ...shipment, type: "FREE_TEXT" }, packingSlips: [], lines: [{ lineNo: 1, description: "Consulting", qty: "1", net: "100.00", revenueAccount: "4900" }] });
    renderAt(`/billing/invoices/${INV}/trace`);
    expect(await screen.findByText("No packing slips")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Trace lines" })).toHaveTextContent("Consulting");
  });

  it("links a credit note's trace to its original invoice", async () => {
    const original = { ...shipment, id: INV2, status: "POSTED" as const, number: "INV-2027-0000007" };
    server.handler = () =>
      json(200, { ...trace, invoice: { ...shipment, type: "CREDIT_NOTE", originalInvoiceId: INV2 }, original, lines: [{ ...trace.lines[0], originalLineNo: 1 }] });
    renderAt(`/billing/invoices/${INV}/trace`);
    expect(await screen.findByRole("link", { name: "Trace of INV-2027-0000007" })).toHaveAttribute("href", `/billing/invoices/${INV2}/trace`);
    expect(screen.getByRole("columnheader", { name: "Original line" })).toBeInTheDocument();
  });

  it("offers a retry when the trace cannot be loaded", async () => {
    let fail = true;
    server.handler = () => (fail ? json(404, { code: "BIL-0100", detail: "invoice not found" }) : json(200, trace));
    renderAt(`/billing/invoices/${INV}/trace`);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("BIL-0100");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Trace lines" })).toBeInTheDocument();
  });
});

describe("Invoices list and detail for shipments", () => {
  it("filters by invoice type through the service", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/billing");
    expect(await screen.findByText("No invoices yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Type"), { target: { value: "CONSOLIDATED" } });
    expect(await screen.findByText("No consolidated invoices")).toBeInTheDocument();
    expect(server.calls.at(-1)?.url.searchParams.get("type")).toBe("CONSOLIDATED");
    expect(screen.getByRole("link", { name: "Invoice shipments" })).toHaveAttribute("href", "/billing/proposals/new");
  });

  it("links an invoice to its trace", async () => {
    server.handler = (url) => (url.pathname === `/api/v1/billing/invoices/${INV}` ? json(200, shipment) : undefined);
    renderAt(`/billing/invoices/${INV}`);
    await waitFor(() => expect(screen.getByRole("link", { name: "Trace" })).toHaveAttribute("href", `/billing/invoices/${INV}/trace`));
  });
});
