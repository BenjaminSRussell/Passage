import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import type { ReactElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { configureApi } from "../src/api";
import { Documents } from "../src/pages/Documents";
import { formatOf, Invoices } from "../src/pages/Invoices";
import { PurchaseOrderDetail } from "../src/pages/PurchaseOrders";
import { RequestDetail } from "../src/pages/Requests";
import { NewShipment } from "../src/pages/Shipments";

const PO = "22222222-2222-4222-8222-222222222222";
const po = {
  id: PO, number: "PO-1", currency: "USD", approvalStatus: "APPROVED", versionNo: 3, total: "100.00", acknowledgements: [],
  lines: [
    { lineNo: 1, description: "Bolt", qty: "10", unit: "EA", unitPrice: "2.5", lineAmount: "25", deliveryDate: "2026-12-01", receivedQty: "0", status: "OPEN" },
    { lineNo: 2, description: "Nut", qty: "5", unit: "EA", unitPrice: "1", lineAmount: "5", deliveryDate: "2026-12-02", receivedQty: "0", status: "OPEN" },
  ],
};

interface Call { method: string; url: string; body?: Record<string, unknown>; headers: Record<string, string> }
let calls: Call[];
let routes: Record<string, unknown>;

beforeEach(() => {
  calls = [];
  routes = {};
  configureApi({ csrfToken: "csrf-1", legalEntityId: "le-1", onSignedOut: () => undefined });
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    calls.push({ method, url, headers: init?.headers as Record<string, string>, ...(typeof init?.body === "string" ? { body: JSON.parse(init.body) as Record<string, unknown> } : {}) });
    const key = `${method} ${url.split("?")[0]}`;
    if (!(key in routes)) return new Response(JSON.stringify({ code: "NF", detail: `no stub for ${key}` }), { status: 404 });
    const r = routes[key];
    return r instanceof Response ? r : new Response(JSON.stringify(r), { status: 200, headers: { "content-type": "application/json" } });
  }));
});

function at(path: string, pattern: string, el: ReactElement) {
  return render(<MemoryRouter initialEntries={[path]}><Routes><Route path={pattern} element={el} /></Routes></MemoryRouter>);
}

async function accessible(container: HTMLElement) {
  const r = await axe.run(container, { rules: { "color-contrast": { enabled: false } } });
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.html}`)).toEqual([]);
}

describe("purchase order acknowledgement", () => {
  beforeEach(() => {
    routes[`GET /api/v1/purchase-orders/${PO}`] = po;
    routes[`POST /api/v1/purchase-orders/${PO}/acknowledgements`] = { id: "a1" };
  });

  it("accepts the version on screen, with the CSRF token and the legal entity and no vendor", async () => {
    const { container } = at(`/purchase-orders/${PO}`, "/purchase-orders/:id", <PurchaseOrderDetail />);
    await userEvent.click(await screen.findByRole("button", { name: "Send answer" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST") as Call;
    expect(post.body).toEqual({ versionNo: 3, outcome: "ACCEPTED" });
    expect(post.headers["x-csrf-token"]).toBe("csrf-1");
    expect(post.headers["x-legal-entity"]).toBe("le-1");
    expect(JSON.stringify(post)).not.toMatch(/vendor/i);
    await accessible(container);
  });

  it("will not reject without a reason", async () => {
    at(`/purchase-orders/${PO}`, "/purchase-orders/:id", <PurchaseOrderDetail />);
    await userEvent.click(await screen.findByLabelText("Reject"));
    await userEvent.click(screen.getByRole("button", { name: "Send answer" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Say why");
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    await userEvent.type(screen.getByLabelText("Reason"), "Cannot supply");
    await userEvent.click(screen.getByRole("button", { name: "Send answer" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST")?.body).toEqual({ versionNo: 3, outcome: "REJECTED", reason: "Cannot supply" }));
  });

  it("proposes only the cells that were filled in", async () => {
    at(`/purchase-orders/${PO}`, "/purchase-orders/:id", <PurchaseOrderDetail />);
    await userEvent.click(await screen.findByLabelText("Propose changes"));
    await userEvent.click(screen.getByRole("button", { name: "Send answer" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("at least one line");
    await userEvent.type(screen.getByLabelText("Unit price for line 2"), "1.2");
    await userEvent.type(screen.getByLabelText("Quantity for line 1"), "8");
    await userEvent.click(screen.getByRole("button", { name: "Send answer" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST")?.body).toEqual({ versionNo: 3, outcome: "CHANGES_PROPOSED", changes: [{ lineNo: 1, qty: "8" }, { lineNo: 2, unitPrice: "1.2" }] }));
  });

  it("shows the service's refusal", async () => {
    routes[`POST /api/v1/purchase-orders/${PO}/acknowledgements`] = new Response(JSON.stringify({ code: "PRC-ACK-001", detail: "The order changed since you opened it." }), { status: 409 });
    at(`/purchase-orders/${PO}`, "/purchase-orders/:id", <PurchaseOrderDetail />);
    await userEvent.click(await screen.findByRole("button", { name: "Send answer" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("changed since you opened");
  });
});

describe("advance ship notice", () => {
  it("is built from the order's lines and sends only shipped quantities", async () => {
    routes["GET /api/v1/purchase-orders"] = { items: [{ id: PO, number: "PO-1", approvalStatus: "APPROVED" }] };
    routes[`GET /api/v1/purchase-orders/${PO}`] = po;
    routes["POST /api/v1/asns"] = { id: "11111111-1111-4111-8111-111111111111" };
    routes["GET /api/v1/asns/11111111-1111-4111-8111-111111111111"] = { id: "x", status: "OPEN", purchaseOrderId: PO, lines: [] };
    at("/shipments/new", "/shipments/new", <NewShipment />);
    await userEvent.selectOptions(await screen.findByLabelText("Purchase order"), PO);
    await userEvent.type(await screen.findByLabelText("Tracking number"), "TRK-9");
    await userEvent.click(screen.getByRole("button", { name: "Send notice" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("at least one line");
    await userEvent.type(screen.getByLabelText("Quantity shipped for line 1"), "4");
    await userEvent.type(screen.getByLabelText("Batch for line 1"), "B1");
    await userEvent.click(screen.getByRole("button", { name: "Send notice" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST")?.body).toEqual({ purchaseOrderId: PO, carrier: "", trackingNumber: "TRK-9", lines: [{ poLineNo: 1, qty: "4", batch: "B1" }] }));
  });
});

describe("invoices", () => {
  it("chooses UBL or OCR from the file and refuses other files before sending", async () => {
    expect(formatOf({ name: "i.xml", type: "" })).toEqual({ format: "UBL", mediaType: "application/xml" });
    expect(formatOf({ name: "i.pdf", type: "application/pdf" })).toEqual({ format: "OCR", mediaType: "application/pdf" });
    expect(formatOf({ name: "i.exe", type: "application/x-msdownload" })).toBeUndefined();

    routes["GET /api/v1/invoices"] = { items: [{ id: "i1", invoiceNumber: "INV-1", invoiceDate: "2026-11-01", currency: "USD", total: "25.00", status: "ACCEPTED" }] };
    routes["GET /api/v1/invoice-documents"] = { items: [] };
    routes["POST /api/v1/invoice-documents"] = { id: "c1", status: "CAPTURED", invoiceNumber: "INV-9", total: "25", reviewReasons: [] };
    const { container } = at("/invoices", "/invoices", <Invoices />);
    expect(await screen.findByText("INV-1")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Send invoice" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Choose the invoice file");
    await userEvent.upload(screen.getByLabelText(/Invoice file/), new File(["<Invoice/>"], "inv.xml", { type: "text/xml" }));
    await userEvent.click(screen.getByRole("button", { name: "Send invoice" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST")?.body).toEqual({ format: "UBL", mediaType: "application/xml", content: btoa("<Invoice/>") }));
    expect(await screen.findByRole("status")).toHaveTextContent("Received invoice INV-9");
    await accessible(container);
  });
});

describe("documents", () => {
  beforeEach(() => {
    routes["GET /api/v1/documents"] = { items: [{ id: "d1", direction: "TO_VENDOR", title: "Spec", fileName: "spec.pdf", contentType: "application/pdf", sizeBytes: 2048, at: "2026-11-01T00:00:00Z" }] };
    routes["GET /api/v1/distributions"] = { items: [{ id: "c1", docNo: "QA-001", revision: 2, title: "Quality manual", fileName: "qa.pdf", requiresAck: true, sentAt: "2026-11-02T00:00:00Z" }] };
    routes["POST /api/v1/distributions/c1/acknowledge"] = { id: "c1" };
    routes["POST /api/v1/documents"] = { id: "d2" };
  });

  it("acknowledges a controlled document", async () => {
    const { container } = at("/documents", "/documents", <Documents />);
    await userEvent.click(await screen.findByRole("button", { name: "Acknowledge QA-001" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url === "/api/v1/distributions/c1/acknowledge")).toBe(true));
    await accessible(container);
  });

  it("refuses a file that is too large or the wrong type before sending, and sends base64 otherwise", async () => {
    at("/documents", "/documents", <Documents />);
    await userEvent.type(await screen.findByLabelText("Title"), "Certificate");
    const input = screen.getByLabelText(/^File/);
    await userEvent.upload(input, new File(["x"], "run.exe", { type: "application/x-msdownload" }));
    await userEvent.click(screen.getByRole("button", { name: "Send document" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Send a PDF");
    const big = new File(["x"], "big.pdf", { type: "application/pdf" });
    Object.defineProperty(big, "size", { value: 6 * 1024 * 1024 });
    await userEvent.upload(input, big);
    await userEvent.click(screen.getByRole("button", { name: "Send document" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("larger than 5 MB");
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    await userEvent.upload(input, new File(["hello"], "cert.pdf", { type: "application/pdf" }));
    await userEvent.click(screen.getByRole("button", { name: "Send document" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST")?.body).toEqual({ title: "Certificate", fileName: "cert.pdf", contentType: "application/pdf", contentBase64: btoa("hello") }));
  });
});

describe("onboarding questionnaire", () => {
  const RID = "33333333-3333-4333-8333-333333333333";
  beforeEach(() => {
    routes[`GET /api/v1/requests/${RID}`] = {
      id: RID, kind: "ONBOARDING", status: "INVITED", subject: "", details: "", createdAt: "2026-11-01T00:00:00Z", answers: {},
      questions: [{ id: "iso", label: "ISO 9001 certified?", type: "YESNO", required: true }, { id: "notes", label: "Anything else", type: "TEXT", required: false }],
    };
    routes[`POST /api/v1/requests/${RID}/submit`] = { id: RID };
  });

  it("will not send until every required question is answered", async () => {
    const { container } = at(`/requests/${RID}`, "/requests/:id", <RequestDetail />);
    await userEvent.click(await screen.findByRole("button", { name: "Send answers" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("marked required");
    expect(screen.getByLabelText(/ISO 9001/)).toHaveAttribute("aria-invalid", "true");
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    await userEvent.selectOptions(screen.getByLabelText(/ISO 9001/), "YES");
    await userEvent.click(screen.getByRole("button", { name: "Send answers" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST")?.body).toEqual({ answers: { iso: "YES" } }));
    await accessible(container);
  });
});

describe("sign-out", () => {
  it("goes to Keycloak's end-session on success, and reports a failure instead of pretending", async () => {
    const { signOut } = await import("../src/api");
    routes["POST /bff/logout"] = { logoutUrl: "https://id.example.invalid/logout?client_id=passage-portal-supplier" };
    const went: string[] = [];
    expect(await signOut((u) => went.push(u))).toBeNull();
    expect(went).toEqual(["https://id.example.invalid/logout?client_id=passage-portal-supplier"]);
    routes["POST /bff/logout"] = new Response(JSON.stringify({ code: "SUP-BFF-503", detail: "the portal cannot reach its session store" }), { status: 503 });
    const message = await signOut((u) => went.push(u));
    expect(message).toMatch(/Sign-out did not finish/);
    expect(went).toHaveLength(1);
  });
});
