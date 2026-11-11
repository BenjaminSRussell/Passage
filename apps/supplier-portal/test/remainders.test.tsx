import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import type { ReactElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { configureApi } from "../src/api";
import { Documents } from "../src/pages/Documents";
import { InvoiceLines, lineAmount } from "../src/pages/InvoiceLines";
import { Payments, whenOf } from "../src/pages/Payments";
import { AcceptInvite, Register } from "../src/pages/Public";
import { RequestDetail, Requests } from "../src/pages/Requests";
import { Users } from "../src/pages/Users";

const PO = "22222222-2222-4222-8222-222222222222";
const RECEIPT = "55555555-5555-4555-8555-555555555555";

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

const problem = (status: number, body: Record<string, unknown>) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/problem+json" } });

describe("invoice by received line", () => {
  const line = { receiptId: RECEIPT, receiptLineNo: 1, packingSlip: "PS-1", receiptDate: "2026-11-02", purchaseOrderId: PO, poLineNo: 1, receivedQty: "3", availableQty: "2", unitPrice: "2.5", currency: "USD" };
  beforeEach(() => {
    routes["GET /api/v1/invoiceable-lines"] = { items: [line] };
    routes["GET /api/v1/invoice-submissions"] = { items: [{ id: "s1", invoiceNumber: "INV-0", invoiceDate: "2026-10-30", currency: "USD", total: "5", status: "REJECTED", decisionNote: "Price differs", createdAt: "2026-10-30T00:00:00Z" }, { id: "s2", invoiceNumber: "INV-00", invoiceDate: "2026-10-31", currency: "USD", total: "7", status: "SUBMITTED", createdAt: "2026-10-31T00:00:00Z" }] };
    routes[`GET /api/v1/purchase-orders/${PO}`] = { id: PO, number: "PO-1001", currency: "USD", approvalStatus: "APPROVED", versionNo: 1, total: "10", acknowledgements: [], lines: [{ lineNo: 1, description: "Hex bolt M8", qty: "3", unit: "EA", unitPrice: "2.5", lineAmount: "7.5", deliveryDate: "2026-11-01", receivedQty: "3", status: "OPEN" }] };
    routes["POST /api/v1/invoice-submissions"] = { id: "s3" };
    routes["POST /api/v1/invoice-submissions/s2/withdraw"] = { id: "s2" };
  });

  it("shows item and order names, never ids, and sends the chosen quantity and price with no vendor", async () => {
    const { container } = at("/invoices/received", "/invoices/received", <InvoiceLines />);
    expect(await screen.findByText("Hex bolt M8")).toBeInTheDocument();
    expect(screen.getByText("PO-1001")).toBeInTheDocument();
    expect(container.textContent).not.toContain(RECEIPT);
    expect(container.textContent).not.toContain(PO);
    await userEvent.click(screen.getByLabelText(/Bill Hex bolt M8/));
    const qty = screen.getByLabelText("Quantity of Hex bolt M8");
    expect(qty).toHaveValue("2");
    await userEvent.clear(qty);
    await userEvent.type(qty, "1");
    await userEvent.type(screen.getByLabelText("Invoice number"), "INV-77");
    await userEvent.type(screen.getByLabelText("Invoice date"), "2026-11-05");
    expect(screen.getByText(/1 line chosen · total 2.50 USD/)).toBeInTheDocument();
    await accessible(container);
    await userEvent.click(screen.getByRole("button", { name: "Send invoice" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST" && c.url === "/api/v1/invoice-submissions")?.body).toEqual({
      invoiceNumber: "INV-77", invoiceDate: "2026-11-05", currency: "USD", note: "", lines: [{ receiptId: RECEIPT, receiptLineNo: 1, qty: "1", unitPrice: "2.5" }],
    }));
    expect(JSON.stringify(calls.find((c) => c.method === "POST"))).not.toMatch(/vendor/i);
    expect(await screen.findByText("Invoice INV-77 was sent for review.")).toBeInTheDocument();
  });

  it("will not send a line over the quantity left, with no line, or without a number", async () => {
    at("/invoices/received", "/invoices/received", <InvoiceLines />);
    await userEvent.click(await screen.findByRole("button", { name: "Send invoice" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Choose at least one received line");
    await userEvent.click(screen.getByLabelText(/Bill Hex bolt M8/));
    await userEvent.click(screen.getByRole("button", { name: "Send invoice" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Enter your invoice number");
    await userEvent.type(screen.getByLabelText("Invoice number"), "INV-1");
    await userEvent.type(screen.getByLabelText("Invoice date"), "2026-11-05");
    const qty = screen.getByLabelText("Quantity of Hex bolt M8");
    await userEvent.clear(qty);
    await userEvent.type(qty, "3");
    await userEvent.click(screen.getByRole("button", { name: "Send invoice" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("only 2 can still be invoiced");
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("shows the service's refusal and lets the supplier withdraw an invoice in review", async () => {
    routes["POST /api/v1/invoice-submissions"] = problem(422, { code: "SUP-INV-003", detail: "receipt line 1 has 0 left to invoice" });
    at("/invoices/received", "/invoices/received", <InvoiceLines />);
    await userEvent.click(await screen.findByLabelText(/Bill Hex bolt M8/));
    await userEvent.type(screen.getByLabelText("Invoice number"), "INV-1");
    await userEvent.type(screen.getByLabelText("Invoice date"), "2026-11-05");
    await userEvent.click(screen.getByRole("button", { name: "Send invoice" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("0 left to invoice");
    expect(screen.getByText("Price differs")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Withdraw INV-0" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Withdraw INV-00" }));
    await waitFor(() => expect(calls.some((c) => c.url === "/api/v1/invoice-submissions/s2/withdraw")).toBe(true));
  });

  it("computes amounts without float noise", () => {
    expect(lineAmount("3", "0.1")).toBe(0.3);
    expect(lineAmount("", "2")).toBe(0);
  });
});

describe("payment status", () => {
  const items = [
    { invoiceId: "i1", invoiceNumber: "INV-1", invoiceDate: "2026-10-01", dueDate: "2026-11-01", currency: "USD", amount: "100", paidAmount: "100", openAmount: "0", status: "PAID", paidAt: "2026-10-28" },
    { invoiceId: "i2", invoiceNumber: "INV-2", invoiceDate: "2026-10-05", dueDate: "2026-11-05", currency: "USD", amount: "200", paidAmount: "0", openAmount: "200", status: "SCHEDULED", scheduledFor: "2026-11-04" },
    { invoiceId: "i3", invoiceNumber: "INV-3", invoiceDate: "2026-10-09", dueDate: "2026-11-09", currency: "USD", amount: "300", paidAmount: "50", openAmount: "250", status: "PARTIALLY_PAID", paidAt: "2026-11-02" },
  ];
  it("lists invoices with when they are or will be paid, and filters by status", async () => {
    routes["GET /api/v1/payments"] = { items };
    const { container } = at("/payments", "/payments", <Payments />);
    expect(await screen.findByText("Paid 2026-10-28")).toBeInTheDocument();
    expect(screen.getByText("Payment run 2026-11-04")).toBeInTheDocument();
    expect(screen.getByText("Part paid 2026-11-02, rest due 2026-11-09")).toBeInTheDocument();
    expect(container.textContent).not.toContain("i1");
    await accessible(container);
    await userEvent.selectOptions(screen.getByLabelText("Show"), "PAID");
    await waitFor(() => expect(calls.some((c) => c.url === "/api/v1/payments?limit=100&status=PAID")).toBe(true));
  });

  it("words each status", () => {
    const base = items[0]!;
    expect(whenOf({ ...base, status: "OPEN" })).toBe("Due 2026-11-01");
    expect(whenOf({ ...base, status: "CANCELLED" })).toBe("Cancelled");
  });
});

describe("profile change requests", () => {
  const RID = "33333333-3333-4333-8333-333333333333";
  it("sends only the details that change and refuses an empty change", async () => {
    routes["GET /api/v1/requests"] = { items: [] };
    routes["POST /api/v1/requests"] = { id: RID };
    at("/requests", "/requests", <Requests />);
    await userEvent.click(await screen.findByRole("button", { name: "Send request" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("at least one company detail");
    await userEvent.type(screen.getByLabelText("Legal name"), " Acme Industrial Ltd ");
    await userEvent.type(screen.getByLabelText("Subject"), "Renamed");
    await userEvent.type(screen.getByLabelText("Details"), "We rebranded.");
    await userEvent.click(screen.getByRole("button", { name: "Send request" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST")?.body).toEqual({ kind: "PROFILE_CHANGE", subject: "Renamed", details: "We rebranded.", changes: { legalName: "Acme Industrial Ltd" } }));
  });

  it("shows that an approved change was applied", async () => {
    routes[`GET /api/v1/requests/${RID}`] = { id: RID, kind: "PROFILE_CHANGE", status: "APPROVED", subject: "Renamed", details: "d", createdAt: "2026-11-01T00:00:00Z", answers: {}, changes: { legalName: "Acme Industrial Ltd" }, appliedAt: "2026-11-03T10:00:00Z" };
    at(`/requests/${RID}`, "/requests/:id", <RequestDetail />);
    expect(await screen.findByText("Legal name: Acme Industrial Ltd")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("updated your company details on 2026-11-03");
  });
});

describe("document virus scan", () => {
  it("shows the refusal when a file is infected or cannot be checked", async () => {
    routes["GET /api/v1/documents"] = { items: [] };
    routes["GET /api/v1/distributions"] = { items: [] };
    routes["POST /api/v1/documents"] = problem(422, { code: "SUP-BFF-422", detail: "the file was refused because it contains a virus" });
    at("/documents", "/documents", <Documents />);
    await userEvent.type(await screen.findByLabelText("Title"), "Certificate");
    await userEvent.upload(screen.getByLabelText(/^File/), new File(["x"], "cert.pdf", { type: "application/pdf" }));
    await userEvent.click(screen.getByRole("button", { name: "Send document" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("contains a virus");
    routes["POST /api/v1/documents"] = problem(503, { code: "SUP-BFF-503", detail: "documents cannot be checked for viruses right now; try again later" });
    await userEvent.click(screen.getByRole("button", { name: "Send document" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("cannot be checked for viruses"));
  });
});

describe("vendor-admin invitations", () => {
  it("is closed to a user who is not an administrator and calls nothing", () => {
    const { container } = at("/users", "/users", <Users isAdmin={false} />);
    expect(container).toHaveTextContent("Only a vendor administrator");
    expect(calls).toHaveLength(0);
  });

  it("invites a colleague, shows the link once, and revokes a pending invitation", async () => {
    routes["GET /api/v1/invites"] = { items: [{ id: "n1", email: "pat@acme.example", status: "PENDING", expiresAt: "2026-11-18T00:00:00Z", createdAt: "2026-11-11T00:00:00Z" }, { id: "n2", email: "old@acme.example", status: "ACCEPTED", expiresAt: "2026-11-01T00:00:00Z", createdAt: "2026-10-25T00:00:00Z" }] };
    routes["POST /api/v1/invites"] = { invite: { id: "n3", email: "casey@acme.example" }, token: "t", acceptUrl: "https://supplier.example/accept-invite?token=t" };
    routes["POST /api/v1/invites/n1/revoke"] = { id: "n1" };
    const { container } = at("/users", "/users", <Users isAdmin />);
    expect(await screen.findByText("pat@acme.example")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Email address"), "not an address");
    await userEvent.click(screen.getByRole("button", { name: "Create invitation" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Enter your colleague's email");
    await userEvent.clear(screen.getByLabelText("Email address"));
    await userEvent.type(screen.getByLabelText("Email address"), "casey@acme.example");
    await userEvent.click(screen.getByRole("button", { name: "Create invitation" }));
    expect(await screen.findByLabelText("Invitation link")).toHaveValue("https://supplier.example/accept-invite?token=t");
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ email: "casey@acme.example", displayName: "" });
    await accessible(container);
    expect(screen.queryByRole("button", { name: /Revoke invitation for old@/ })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Revoke invitation for pat@acme.example" }));
    await waitFor(() => expect(calls.some((c) => c.url === "/api/v1/invites/n1/revoke")).toBe(true));
  });
});

describe("public registration page", () => {
  it("is closed when the portal says so", async () => {
    routes["GET /bff/registration"] = { open: false };
    at("/register", "/register", <Register />);
    expect(await screen.findByText(/registration is closed/)).toBeInTheDocument();
  });

  it("checks the form, sends it with no CSRF token, keeps the honeypot empty and thanks the visitor", async () => {
    routes["GET /bff/registration"] = { open: true };
    routes["POST /bff/prospects"] = new Response(JSON.stringify({ status: "RECEIVED" }), { status: 202 });
    const { container } = at("/register", "/register", <Register />);
    await userEvent.click(await screen.findByRole("button", { name: "Send registration" }));
    expect((await screen.findAllByRole("alert")).map((a) => a.textContent).join(" ")).toContain("Enter your company name");
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    await accessible(container);
    await userEvent.type(screen.getByLabelText("Company name"), "Birch Valve Co");
    await userEvent.type(screen.getByLabelText("Contact name"), "Sam Birch");
    await userEvent.type(screen.getByLabelText("Email address"), "sam@birch.example");
    await userEvent.selectOptions(screen.getByLabelText("Country"), "United States");
    await userEvent.click(screen.getByRole("button", { name: "Send registration" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      companyName: "Birch Valve Co", contactName: "Sam Birch", email: "sam@birch.example", phone: "", country: "US", taxId: "", website: "", message: "", homepage: "",
    }));
    expect(calls.find((c) => c.method === "POST")?.headers["x-csrf-token"]).toBeUndefined();
    expect(await screen.findByText(/We received your registration/)).toBeInTheDocument();
  });

  it("keeps the honeypot out of sight of people and assistive technology", async () => {
    routes["GET /bff/registration"] = { open: true };
    at("/register", "/register", <Register />);
    const trap = (await screen.findByText("Leave this field empty")).closest("div") as HTMLElement;
    expect(trap).toHaveAttribute("aria-hidden", "true");
    expect(within(trap).getByRole("textbox", { hidden: true })).toHaveAttribute("tabindex", "-1");
  });

  it("tells the visitor when there are too many registrations from their address", async () => {
    routes["GET /bff/registration"] = { open: true };
    routes["POST /bff/prospects"] = problem(429, { code: "SUP-BFF-429", detail: "too many requests; try again later" });
    at("/register", "/register", <Register />);
    await userEvent.type(await screen.findByLabelText("Company name"), "B");
    await userEvent.type(screen.getByLabelText("Contact name"), "S");
    await userEvent.type(screen.getByLabelText("Email address"), "s@b.example");
    await userEvent.selectOptions(screen.getByLabelText("Country"), "United States");
    await userEvent.click(screen.getByRole("button", { name: "Send registration" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Too many registrations");
  });
});

describe("accepting an invitation", () => {
  const TOKEN = "inv1.t.l.v.i.secret";
  const at2 = () => render(<MemoryRouter initialEntries={[`/accept-invite?token=${TOKEN}`]}><Routes><Route path="/accept-invite" element={<AcceptInvite />} /></Routes></MemoryRouter>);

  it("shows who it is for, then accepts with the name the invitee gives", async () => {
    routes["POST /bff/invites/verify"] = { email: "casey@acme.example", displayName: "Casey", vendorName: "Acme Valves" };
    routes["POST /bff/invites/accept"] = { status: "ACCEPTED", provisioned: false, email: "casey@acme.example" };
    const { container } = at2();
    expect(await screen.findByText("Acme Valves")).toBeInTheDocument();
    await accessible(container);
    const name = screen.getByLabelText("Your name");
    await userEvent.clear(name);
    await userEvent.type(name, "Casey Q");
    await userEvent.click(screen.getByRole("button", { name: "Accept invitation" }));
    await waitFor(() => expect(calls.find((c) => c.url === "/bff/invites/accept")?.body).toEqual({ token: TOKEN, displayName: "Casey Q" }));
    expect(await screen.findByRole("status")).toHaveTextContent("administrator finishes setting up your account");
  });

  it("says so plainly when the invitation is not valid", async () => {
    routes["POST /bff/invites/verify"] = problem(404, { code: "SUP-BFF-404", detail: "x" });
    at2();
    expect(await screen.findByRole("alert")).toHaveTextContent("not valid, has expired, or was already used");
  });
});
