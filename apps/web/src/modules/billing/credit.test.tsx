import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Invoice } from "./api/client";
import { emptyCredit, toCreditInput, validateCredit } from "./NewCreditNotePage";
import { emptyFreeText, toFreeTextInput, validateFreeText } from "./NewFreeTextInvoicePage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "0190f0a0-0000-7000-8000-000000000002";
const ACCT = "0190f0a0-0000-7000-8000-000000000003";
const INV = "0190f0a0-0000-7000-8000-000000000901";
const CN = "0190f0a0-0000-7000-8000-000000000902";

const original: Invoice = {
  id: INV,
  legalEntityId: LE,
  customerId: CUST,
  invoiceAccountId: ACCT,
  type: "FREE_TEXT",
  status: "POSTED",
  number: "INV-2027-0000001",
  currency: "EUR",
  invoiceDate: "2027-03-10",
  dueDate: "2027-05-09",
  postingDate: "2027-03-12",
  paymentTermDays: 60,
  totals: { net: "199.99", charges: "10.00", tax: "38.00", gross: "247.99" },
  lines: [
    { lineNo: 1, description: "Consulting", qty: "3", unitPrice: "33.33", discountPct: "0", net: "99.99", tax: "19.00", revenueAccount: "4900", taxCode: "DE19", creditedQty: "1" },
    { lineNo: 2, description: "Workshop", qty: "1", unitPrice: "100", discountPct: "0", net: "100.00", tax: "19.00", revenueAccount: "4910", taxCode: "DE19", creditedQty: "0" },
  ],
  installments: [
    { no: 1, percent: "30", offsetDays: 0, amount: "74.40", dueDate: "2027-03-10" },
    { no: 2, percent: "70", offsetDays: 60, amount: "173.59", dueDate: "2027-05-09" },
  ],
  credited: { gross: "39.66", remaining: "208.33" },
  version: 2,
};

const creditProposal: Invoice = {
  ...original,
  id: CN,
  type: "CREDIT_NOTE",
  status: "PROPOSAL",
  number: undefined,
  postingDate: undefined,
  invoiceDate: "2027-03-20",
  dueDate: "2027-03-20",
  paymentTermDays: 0,
  originalInvoiceId: INV,
  originalNumber: "INV-2027-0000001",
  reason: "Workshop cancelled",
  totals: { net: "100.00", charges: "0.00", tax: "19.00", gross: "119.00" },
  lines: [{ lineNo: 1, description: "Workshop", qty: "1", unitPrice: "100", discountPct: "0", net: "100.00", tax: "19.00", revenueAccount: "4910", taxCode: "DE19", originalLineNo: 2 }],
  installments: [],
  credited: undefined,
  version: 1,
};

const creditPosted: Invoice = { ...creditProposal, status: "POSTED", number: "CN-2027-0000002", postingDate: "2027-03-21", version: 2 };

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("Posted invoice", () => {
  it("shows what was credited, the installments and the credit notes, and offers a credit", async () => {
    server.handler = (url) => {
      if (url.pathname === `/api/v1/billing/invoices/${INV}`) return json(200, original);
      if (url.pathname === "/api/v1/billing/invoices") return json(200, { items: [creditPosted] });
      return undefined;
    };
    const { container } = renderAt(`/billing/invoices/${INV}`);
    expect(await screen.findByRole("heading", { name: "INV-2027-0000001" })).toBeInTheDocument();
    expect(screen.getByText("208.33 EUR")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Installments" })).toHaveTextContent("173.59");
    expect(screen.getByText("2027-05-09 (last installment)")).toBeInTheDocument();
    const credits = await findTable({ name: "Credit notes of this invoice" });
    await waitFor(() => expect(credits).toHaveTextContent("CN-2027-0000002"));
    const listCall = server.calls.find((c) => c.url.pathname === "/api/v1/billing/invoices");
    expect(listCall?.url.searchParams.get("type")).toBe("CREDIT_NOTE");
    expect(listCall?.url.searchParams.get("originalInvoiceId")).toBe(INV);
    expect(screen.getByRole("table", { name: "Invoice lines" })).toHaveTextContent("Credited qty");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("link", { name: "Credit invoice" }));
    expect(await screen.findByRole("heading", { name: "New credit note" })).toBeInTheDocument();
  });

  it("offers no credit once the invoice is fully credited", async () => {
    server.handler = (url) => {
      if (url.pathname === `/api/v1/billing/invoices/${INV}`) return json(200, { ...original, credited: { gross: "247.99", remaining: "0.00" } });
      if (url.pathname === "/api/v1/billing/invoices") return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/billing/invoices/${INV}`);
    expect(await screen.findByText("No credit notes")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Credit invoice" })).not.toBeInTheDocument();
  });
});

describe("New credit note", () => {
  it("credits the whole remainder with an Idempotency-Key and opens the proposal", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/billing/credit-notes" && init.method === "POST") return json(201, creditProposal);
      if (url.pathname === `/api/v1/billing/invoices/${INV}`) return json(200, original);
      if (url.pathname === `/api/v1/billing/invoices/${CN}`) return json(200, creditProposal);
      return undefined;
    };
    const { container } = renderAt(`/billing/credit-notes/new?invoice=${INV}`);
    expect(await screen.findByLabelText("What to credit")).toHaveTextContent("Everything left to credit (208.33 EUR)");
    expect(pageViolations(container)).toEqual([]);
    fill("Credit date", "2027-03-20");
    fireEvent.click(screen.getByRole("button", { name: "Save credit note proposal" }));
    expect(await screen.findByText("Enter the reason printed on the credit note, up to 200 characters.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);

    fill("Reason", " Workshop cancelled ");
    fireEvent.click(screen.getByRole("button", { name: "Save credit note proposal" }));
    expect(await screen.findByRole("heading", { name: "Credit note proposal" })).toBeInTheDocument();
    const create = server.calls.find((c) => c.method === "POST");
    expect(idempotencyKey(create!)).toMatch(/.+/);
    expect(create?.body).toEqual({ originalInvoiceId: INV, creditDate: "2027-03-20", reason: "Workshop cancelled" });
  });

  it("credits chosen line quantities and checks them against what is left", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/billing/credit-notes" && init.method === "POST") return json(201, creditProposal);
      if (url.pathname === `/api/v1/billing/invoices/${INV}`) return json(200, original);
      if (url.pathname === `/api/v1/billing/invoices/${CN}`) return json(200, creditProposal);
      return undefined;
    };
    renderAt(`/billing/credit-notes/new?invoice=${INV}`);
    fireEvent.change(await screen.findByLabelText("What to credit"), { target: { value: "LINES" } });
    expect(screen.getByRole("group", { name: "Line 1: Consulting" })).toHaveTextContent("Left to credit: 2.");
    fill("Reason", "Workshop cancelled");
    fill("Quantity to credit on line 1", "3");
    fireEvent.click(screen.getByRole("button", { name: "Save credit note proposal" }));
    expect(await screen.findByText("At most 2 can still be credited on this line.")).toBeInTheDocument();

    fill("Quantity to credit on line 1", "");
    fill("Quantity to credit on line 2", "1");
    fireEvent.click(screen.getByRole("button", { name: "Save credit note proposal" }));
    expect(await screen.findByRole("heading", { name: "Credit note proposal" })).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "POST")?.body).toMatchObject({ reason: "Workshop cancelled", lines: [{ lineNo: 2, qty: "1" }] });
  });

  it("explains a credit the service refuses and maps its field errors to the lines", async () => {
    let reply = json(422, { code: "BIL-0005", detail: "Credit exceeds remaining creditable amount 208.33" });
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/billing/credit-notes" && init.method === "POST") return reply;
      if (url.pathname === `/api/v1/billing/invoices/${INV}`) return json(200, original);
      return undefined;
    };
    renderAt(`/billing/credit-notes/new?invoice=${INV}`);
    fireEvent.change(await screen.findByLabelText("Reason"), { target: { value: "Price error" } });
    fireEvent.click(screen.getByRole("button", { name: "Save credit note proposal" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The credit exceeds what the invoice still allows");
    expect(alert).toHaveTextContent("BIL-0005");

    reply = json(400, { code: "COM-0001", detail: "request failed validation", errors: { "lines[0].qty": "must be a positive decimal" } });
    fireEvent.change(screen.getByLabelText("What to credit"), { target: { value: "LINES" } });
    fill("Quantity to credit on line 2", "1");
    fireEvent.click(screen.getByRole("button", { name: "Save credit note proposal" }));
    const line2 = await screen.findByRole("group", { name: "Line 2: Workshop" });
    expect(await within(line2).findByText("must be a positive decimal")).toBeInTheDocument();
  });

  it("refuses an invoice that cannot be credited and shows load failures", async () => {
    server.handler = (url) => (url.pathname === `/api/v1/billing/invoices/${INV}` ? json(200, { ...original, status: "PROPOSAL", number: undefined, credited: undefined }) : undefined);
    renderAt(`/billing/credit-notes/new?invoice=${INV}`);
    expect(await screen.findByText("Only a posted invoice can be credited; post the proposal first.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save credit note proposal" })).not.toBeInTheDocument();
  });

  it("shows a load error with a retry, and asks for an invoice when none is given", async () => {
    server.handler = () => json(404, { code: "BIL-0100", detail: "invoice not found" });
    renderAt(`/billing/credit-notes/new?invoice=${INV}`);
    expect(await findAlert()).toHaveTextContent("invoice not found");
    server.handler = (url) => (url.pathname === `/api/v1/billing/invoices/${INV}` ? json(200, original) : undefined);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByLabelText("What to credit")).toBeInTheDocument();
  });

  it("asks for the invoice when the page is opened without one", () => {
    renderAt("/billing/credit-notes/new");
    expect(screen.getByText("Choose the invoice to credit")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("validates dates and requires a line in line mode", () => {
    const v = { ...emptyCredit("2027-03-01"), scope: "LINES" as const, reason: "x" };
    expect(validateCredit(v, original)).toMatchObject({ creditDate: "The credit date cannot be before the invoice date 2027-03-10.", lines: "Enter a quantity on at least one line." });
    expect(toCreditInput({ ...v, qty: { 2: " 0.5 " }, chargeAmount: "5" }, original)).toEqual({
      originalInvoiceId: INV,
      creditDate: "2027-03-01",
      reason: "x",
      chargeAmount: "5",
      lines: [{ lineNo: 2, qty: "0.5" }],
    });
  });
});

describe("Credit note detail", () => {
  it("shows the original and reason and posts through the credit-note endpoint with If-Match", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/billing/credit-notes/${CN}:post` && init.method === "POST") return json(200, creditPosted);
      if (url.pathname === `/api/v1/billing/invoices/${CN}`) return json(200, creditProposal);
      return undefined;
    };
    const { container } = renderAt(`/billing/invoices/${CN}`);
    expect(await screen.findByRole("heading", { name: "Credit note proposal" })).toBeInTheDocument();
    expect(screen.getByText("Workshop cancelled")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Credit note lines" })).toHaveTextContent("Original line");
    expect(screen.getByRole("link", { name: "Original invoice" })).toHaveAttribute("href", `/billing/invoices/${INV}`);
    expect(screen.queryByRole("link", { name: "Credit invoice" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Post credit note" }));
    expect(await screen.findByRole("heading", { name: "CN-2027-0000002" })).toBeInTheDocument();
    expect(screen.getByText("Posted as CN-2027-0000002", { exact: false })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/billing/credit-notes/${CN}:post`);
    expect(new Headers(post?.init.headers).get("If-Match")).toBe('"1"');
  });

  it("explains a post refused because another credit note took the amount", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "BIL-0005", detail: "Credit exceeds remaining creditable amount 0.00" });
      if (url.pathname === `/api/v1/billing/invoices/${CN}`) return json(200, creditProposal);
      return undefined;
    };
    renderAt(`/billing/invoices/${CN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Post credit note" }));
    expect(await findAlert()).toHaveTextContent("The credit no longer fits the original invoice");
    expect(screen.getByRole("button", { name: "Post credit note" })).toBeInTheDocument();
  });
});

describe("Credit notes list", () => {
  it("lists credit notes with their original and filters by status", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/billing/invoices" ? json(200, { items: [creditPosted] }) : undefined);
    const { container } = renderAt("/billing/credit-notes");
    const table = await findTable({ name: "Credit notes" });
    await waitFor(() => expect(table).toHaveTextContent("CN-2027-0000002"));
    expect(table).toHaveTextContent("INV-2027-0000001");
    expect(table).toHaveTextContent("119.00 EUR");
    expect(server.calls[0].url.searchParams.get("type")).toBe("CREDIT_NOTE");
    expect(pageViolations(container)).toEqual([]);

    server.handler = () => json(200, { items: [] });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "PROPOSAL" } });
    expect(await screen.findByText("No proposal credit notes")).toBeInTheDocument();
    expect(server.calls.at(-1)?.url.searchParams.get("status")).toBe("PROPOSAL");
  });

  it("pages credit notes through the cursor, keeping the credit-note filter", async () => {
    server.handler = (url) =>
      url.searchParams.get("cursor") === "c1"
        ? json(200, { items: [{ ...creditPosted, id: "cn-older", number: "CN-2027-0000001" }] })
        : json(200, { items: [creditPosted], nextCursor: "c1" });
    renderAt("/billing/credit-notes");
    const table = await findTable({ name: "Credit notes" });
    fireEvent.click(await screen.findByRole("button", { name: "Load more credit notes" }));
    await waitFor(() => expect(table).toHaveTextContent("CN-2027-0000001"));
    expect(table).toHaveTextContent("CN-2027-0000002");
    const next = server.calls.at(-1)!.url.searchParams;
    expect([next.get("type"), next.get("cursor")]).toEqual(["CREDIT_NOTE", "c1"]);
  });

  it("shows the error state with a retry", async () => {
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty billing.invoice.view" });
    renderAt("/billing/credit-notes");
    expect(await findAlert()).toHaveTextContent("missing duty billing.invoice.view");
    server.handler = () => json(200, { items: [] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No credit notes yet")).toBeInTheDocument();
  });
});

describe("Installment plan on a free-text invoice", () => {
  const base = {
    ...emptyFreeText("2027-03-10"),
    legalEntityId: LE,
    customerId: CUST,
    invoiceAccountId: ACCT,
    lines: [{ description: "Machine", qty: "1", unitPrice: "1000.01", discountPct: "", revenueAccount: "4000", taxCode: "US0" }],
  };

  it("requires shares totalling 100% with offsets in order (AC-29.10 terms)", () => {
    const terms = [
      { percent: "30", offsetDays: "0" },
      { percent: "30", offsetDays: "30" },
      { percent: "40", offsetDays: "60" },
    ];
    expect(validateFreeText({ ...base, installments: terms })).toEqual({});
    expect(toFreeTextInput({ ...base, installments: terms }).installmentTerms).toEqual([
      { percent: "30", offsetDays: 0 },
      { percent: "30", offsetDays: 30 },
      { percent: "40", offsetDays: 60 },
    ]);
    expect(validateFreeText({ ...base, installments: terms.slice(0, 2) })).toEqual({ installmentTerms: "The shares total 60%; they must total 100%." });
    expect(validateFreeText({ ...base, installments: [{ percent: "50", offsetDays: "30" }, { percent: "50", offsetDays: "10" }] })).toHaveProperty(
      "installmentTerms[1].offsetDays",
    );
    expect(toFreeTextInput(base)).not.toHaveProperty("installmentTerms");
  });

  it("adds installment rows to the form and reports a wrong total before calling the service", async () => {
    renderAt("/billing/free-text-invoices/new");
    fireEvent.click(screen.getByRole("button", { name: "Add installment" }));
    fireEvent.click(screen.getByRole("button", { name: "Add installment" }));
    expect(screen.getByRole("group", { name: "Installment 2" })).toBeInTheDocument();
    const [first, second] = screen.getAllByLabelText(/Share %/);
    fireEvent.change(first, { target: { value: "30" } });
    fireEvent.change(second, { target: { value: "60" } });
    fireEvent.change(screen.getAllByLabelText(/Due after \(days\)/)[1], { target: { value: "30" } });
    fill("Legal entity", LE);
    fill("Customer", CUST);
    fill("Invoice account", ACCT);
    fill("Description", "Machine");
    fill("Unit price", "1000.01");
    fill("Revenue account", "4000");
    fill("Tax code", "US0");
    fireEvent.click(screen.getByRole("button", { name: "Save proposal" }));
    expect(await screen.findByText("The shares total 90%; they must total 100%.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Remove installment 2" }));
    expect(screen.queryByRole("group", { name: "Installment 2" })).not.toBeInTheDocument();
  });
});
