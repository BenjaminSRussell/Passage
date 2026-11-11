import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { OpenItem, Payment } from "./api/client";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "0190f0a0-0000-7000-8000-0000000000c1";
const PAY = "0190f0a0-0000-7000-8000-000000000301";
const ITEM = "0190f0a0-0000-7000-8000-000000000401";

const payment: Payment = {
  id: PAY,
  legalEntityId: LE,
  customerId: CUST,
  paymentMode: "ACH",
  source: "MANUAL",
  sourceRef: "ACH-7",
  amount: "980.00",
  unappliedAmount: "980.00",
  currency: "USD",
  receivedDate: "2027-03-08",
  valueDate: "2027-03-08",
  payerName: "Contoso",
  remittance: [{ reference: "INV-30001", amount: "980.00" }],
  status: "RECEIVED",
  version: 1,
};

const item: OpenItem = {
  id: ITEM,
  legalEntityId: LE,
  customerId: CUST,
  sourceType: "INVOICE",
  documentNumber: "INV-30001",
  documentDate: "2027-03-01",
  dueDate: "2027-03-31",
  currency: "USD",
  amount: "1000.00",
  openAmount: "1000.00",
  cashDiscountDate: "2027-03-11",
  cashDiscountAmount: "20.00",
  dunningLevel: 0,
  dunningBlock: false,
  status: "OPEN",
  version: 1,
};

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("AR workspace", () => {
  it("shows aging by currency and the payments waiting for application", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/aging")) {
        return json(200, { asOf: "2027-03-31", totals: [{ currency: "USD", bucket: "1-30", amount: "1250.00" }] });
      }
      if (url.pathname.endsWith("/payments")) return json(200, { items: url.searchParams.get("status") === "RECEIVED" ? [payment] : [] });
      if (url.pathname.endsWith("/disputes")) return json(200, { items: url.searchParams.get("status") === "active" ? [{ id: "d" }, { id: "e" }] : [] });
      return undefined;
    };
    const { container } = renderAt("/receivables");
    const table = await findTable({ name: "Aging by currency" });
    await waitFor(() => expect(table).toHaveTextContent("1250.00"));
    expect(screen.getByRole("list", { name: "Receivables tiles" })).toHaveTextContent("Payments to apply 1");
    expect(screen.getByRole("list", { name: "Receivables tiles" })).toHaveTextContent("Currencies with overdue items 1");
    expect(screen.getByRole("list", { name: "Receivables tiles" })).toHaveTextContent("Active disputes 2");
    expect(server.calls.some((c) => c.url.pathname.endsWith("/aging") && c.url.searchParams.has("asOf"))).toBe(true);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state, and an error with a retry", async () => {
    server.handler = (url) => (url.pathname.endsWith("/aging") ? json(200, { asOf: "2027-03-31", totals: [] }) : json(200, { items: [] }));
    const first = renderAt("/receivables");
    expect(await screen.findByText("No open items")).toBeInTheDocument();
    first.unmount();

    server.handler = () => json(503, { code: "COM-0500", detail: "internal error" });
    renderAt("/receivables");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load the AR workspace");
    expect(alert).toHaveTextContent("COM-0500");
    server.handler = (url) => (url.pathname.endsWith("/aging") ? json(200, { asOf: "2027-03-31", totals: [] }) : json(200, { items: [] }));
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No open items")).toBeInTheDocument();
  });
});

describe("Open items", () => {
  it("lists items with open amount and discount and filters by customer", async () => {
    server.handler = (url) => (url.pathname.endsWith("/open-items") ? json(200, { items: [item] }) : undefined);
    const { container } = renderAt("/receivables/open-items");
    const table = await findTable({ name: "Open items" });
    await waitFor(() => expect(table).toHaveTextContent("INV-30001"));
    expect(table).toHaveTextContent("1000.00 USD");
    expect(table).toHaveTextContent("20.00 USD by 2027-03-11");
    expect(pageViolations(container)).toEqual([]);

    fill("Customer", "nope");
    expect(server.calls.some((c) => c.url.searchParams.get("customer") === "nope")).toBe(false);
    fill("Customer", CUST);
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("customer") === CUST)).toBe(true));
  });

  it("offers dispute, promise and deduction on debits only, not on credit-side items", async () => {
    const credit: OpenItem = {
      ...item,
      id: "0190f0a0-0000-7000-8000-000000000402",
      sourceType: "CREDIT_NOTE",
      documentNumber: "CN-30002",
      amount: "-150.00",
      openAmount: "-150.00",
      cashDiscountDate: null,
      cashDiscountAmount: "0",
      side: "CREDIT",
    };
    server.handler = (url) => (url.pathname.endsWith("/open-items") ? json(200, { items: [{ ...item, side: "DEBIT" }, credit] }) : undefined);
    renderAt("/receivables/open-items");
    const table = await findTable({ name: "Open items" });
    await waitFor(() => expect(table).toHaveTextContent("CN-30002"));
    expect(within(table).getByRole("link", { name: "Dispute INV-30001" })).toBeInTheDocument();
    expect(within(table).getByRole("link", { name: "Deduction on INV-30001" })).toBeInTheDocument();
    expect(within(table).queryByRole("link", { name: "Dispute CN-30002" })).not.toBeInTheDocument();
    expect(within(table).queryByRole("link", { name: "Promise for CN-30002" })).not.toBeInTheDocument();
    expect(within(table).queryByRole("link", { name: "Deduction on CN-30002" })).not.toBeInTheDocument();
  });

  it("shows the empty state and the error state", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/receivables/open-items");
    expect(await screen.findByText("Items appear here once invoices post for the customer.")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty receivables.view" });
    renderAt("/receivables/open-items");
    expect(await findAlert()).toHaveTextContent("AUTH-0003");
  });
});

describe("Payments", () => {
  it("lists payments, filters by status and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/payments")) return json(200, { items: [payment] });
      if (url.pathname.endsWith(`/payments/${PAY}`)) return json(200, payment);
      if (url.pathname.endsWith("/open-items")) return json(200, { items: [item] });
      return undefined;
    };
    const { container } = renderAt("/receivables/payments");
    const table = await findTable({ name: "Payments" });
    await waitFor(() => expect(table).toHaveTextContent("ACH-7"));
    expect(table).toHaveTextContent("980.00 USD");
    expect(table).toHaveTextContent("Received");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "ON_ACCOUNT" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("status") === "ON_ACCOUNT")).toBe(true));
    expect(server.calls.at(-1)?.url.searchParams.get("limit")).toBe("500");

    fireEvent.click(within(await findTable({ name: "Payments" })).getByRole("button", { name: "2027-03-08" }));
    expect(await screen.findByRole("heading", { name: "Payment ACH-7" })).toBeInTheDocument();
  });

  it("shows the empty state with the next step", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/receivables/payments");
    expect(await screen.findByText("No payments")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Record the first payment" })).toHaveAttribute("href", "/receivables/payments/new");
  });

  it("says nothing matches the filters and clears them", async () => {
    server.handler = (url) => json(200, { items: url.searchParams.get("status") === "REVERSED" ? [] : [payment] });
    renderAt("/receivables/payments");
    await waitFor(() => expect(screen.getByRole("table", { name: "Payments" })).toHaveTextContent("ACH-7"));
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "REVERSED" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("No payments match these filters")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    await waitFor(() => expect(screen.getByRole("table", { name: "Payments" })).toHaveTextContent("ACH-7"));
  });

  it("explains a failure with a retry, and no access without one", async () => {
    server.handler = () => json(500, { code: "COM-0500", detail: "store unavailable" });
    const { unmount } = renderAt("/receivables/payments");
    expect(await screen.findByText("Could not load payments")).toBeInTheDocument();
    server.handler = () => json(200, { items: [payment] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(screen.getByRole("table", { name: "Payments" })).toHaveTextContent("ACH-7"));
    unmount();

    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty" });
    renderAt("/receivables/payments");
    expect(await screen.findByText("You can't view payments")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
  });
});

describe("Record payment", () => {
  it("validates, then creates the payment with an Idempotency-Key and opens it", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/payments") && init.method === "POST") return json(201, payment);
      if (url.pathname.endsWith(`/payments/${PAY}`)) return json(200, payment);
      if (url.pathname.endsWith("/open-items")) return json(200, { items: [item] });
      return undefined;
    };
    const { container } = renderAt("/receivables/payments/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Record payment" }));
    expect(await screen.findByText("Choose a legal entity.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(screen.getByLabelText("Legal entity")).toHaveFocus();
    expect(screen.getByRole("button", { name: "Legal entity: Choose a legal entity." })).toBeInTheDocument();

    fill("Legal entity", LE);
    fill("Customer", CUST);
    fill("Amount", "980.00");
    fill("Check or bank reference", "ACH-7");
    fill("Reference 1", "INV-30001");
    fireEvent.click(screen.getByRole("button", { name: "Record payment" }));
    expect(await screen.findByRole("heading", { name: "Payment ACH-7" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(idempotencyKey(post)).toBeTruthy();
    expect(post.body).toMatchObject({ legalEntityId: LE, customerId: CUST, amount: "980.00", source: "MANUAL", remittance: [{ reference: "INV-30001", amount: "0" }] });
  });

  it("is not dirty because the working legal entity was filled in, and is dirty after an edit", async () => {
    window.localStorage.setItem("passage.workContext.tester", JSON.stringify({ legalEntityId: LE, siteId: "" }));
    server.handler = () => json(200, { id: LE, name: "Acme", code: "ACME" });
    renderAt("/receivables/payments/new");
    await screen.findByLabelText("Amount");
    await new Promise((resolve) => setTimeout(resolve, 50));
    const untouched = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(untouched);
    expect(untouched.defaultPrevented).toBe(false);
    fill("Amount", "5");
    const edited = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(edited);
    expect(edited.defaultPrevented).toBe(true);
    window.localStorage.clear();
  });

  it("shows the service's field errors and refusals", async () => {
    server.handler = () => json(409, { code: "AR-PAYMENT-DUPLICATE", detail: "a payment or open item with this reference already exists" });
    renderAt("/receivables/payments/new");
    fill("Legal entity", LE);
    fill("Amount", "10");
    fireEvent.click(screen.getByRole("button", { name: "Record payment" }));
    expect(await findAlert()).toHaveTextContent("AR-PAYMENT-DUPLICATE");
  });
});

describe("Payment detail", () => {
  function detailServer(apply: () => ReturnType<typeof json>) {
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`/payments/${PAY}:match`)) {
        return json(200, { decision: "AUTO_APPLY", candidates: [{ openItemIds: [ITEM], score: 93, rules: ["R1", "R3", "R6"] }] });
      }
      if (url.pathname.endsWith(`/payments/${PAY}:apply`) && init.method === "POST") return apply();
      if (url.pathname.endsWith(`/payments/${PAY}`)) return json(200, payment);
      if (url.pathname.endsWith("/open-items")) return json(200, { items: [item] });
      return undefined;
    };
  }

  it("scores candidates, uses the best one and applies with If-Match and an Idempotency-Key", async () => {
    detailServer(() =>
      json(200, {
        payment: { ...payment, status: "APPLIED", unappliedAmount: "0", version: 2 },
        settlements: [{ openItemId: ITEM, amount: "980.00", cashDiscount: "20.00", writeOff: "0", openAfter: "0", itemStatus: "SETTLED" }],
      }),
    );
    const { container } = renderAt(`/receivables/payments/${PAY}`);
    expect(await screen.findByRole("heading", { name: "Payment ACH-7" })).toBeInTheDocument();
    expect(await findTable({ name: "Open items to settle" })).toHaveTextContent("INV-30001");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Find matches" }));
    const candidates = await findTable({ name: "Match candidates" });
    expect(candidates).toHaveTextContent("93");
    expect(screen.getByText(/Auto-apply/)).toBeInTheDocument();
    fireEvent.click(within(candidates).getByRole("button", { name: "Use INV-30001" }));
    expect(screen.getByLabelText("Apply for INV-30001")).toHaveValue("980.00");
    expect(screen.getByLabelText("Cash discount for INV-30001")).toHaveValue("20.00");
    expect(screen.getByText(/0.00 USD goes on account/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Apply payment" }));
    expect(await screen.findByText(/1 item\(s\) settled/)).toBeInTheDocument();
    const call = server.calls.find((c) => c.url.pathname.endsWith(":apply"))!;
    expect(new Headers(call.init.headers).get("If-Match")).toBe('"1"');
    expect(idempotencyKey(call)).toBeTruthy();
    expect(call.body).toEqual({ settlements: [{ openItemId: ITEM, amount: "980.00", cashDiscount: "20.00", writeOff: "0" }], onAccount: "0.00" });
  });

  it("offers only debit items as apply targets, never a credit note or unapplied payment", async () => {
    const credit: OpenItem = {
      ...item,
      id: "0190f0a0-0000-7000-8000-000000000403",
      sourceType: "CREDIT_NOTE",
      documentNumber: "CN-30003",
      amount: "-75.00",
      openAmount: "-75.00",
      side: "CREDIT",
    };
    server.handler = (url) => {
      if (url.pathname.endsWith(`/payments/${PAY}`)) return json(200, payment);
      if (url.pathname.endsWith("/open-items")) return json(200, { items: [{ ...item, side: "DEBIT" }, credit] });
      return undefined;
    };
    renderAt(`/receivables/payments/${PAY}`);
    const table = await findTable({ name: "Open items to settle" });
    await waitFor(() => expect(table).toHaveTextContent("INV-30001"));
    expect(table).not.toHaveTextContent("CN-30003");
    expect(screen.queryByLabelText("Apply for CN-30003")).not.toBeInTheDocument();
  });

  it("explains a stale version and blocks a plan above the unapplied amount", async () => {
    detailServer(() => json(412, { code: "conflict_version", detail: "payment changed since it was read" }));
    renderAt(`/receivables/payments/${PAY}`);
    await findTable({ name: "Open items to settle" });
    fill("Apply for INV-30001", "1000.00");
    expect(screen.getByText("The settlements exceed the 980.00 still unapplied.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Apply payment" })).toBeDisabled();
    fill("Apply for INV-30001", "900.00");
    expect(screen.getByText(/80.00 USD goes on account/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Apply payment" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The payment changed since you opened it");
    expect(alert).toHaveTextContent("conflict_version");
  });

  it("shows a load error with a retry and hides apply for an applied payment", async () => {
    server.handler = () => json(404, { code: "AR-PAYMENT-NOT-FOUND", detail: "payment not found" });
    const first = renderAt(`/receivables/payments/${PAY}`);
    expect(await findAlert()).toHaveTextContent("AR-PAYMENT-NOT-FOUND");
    first.unmount();

    server.handler = (url) => {
      if (url.pathname.endsWith(`/payments/${PAY}`)) return json(200, { ...payment, status: "APPLIED", unappliedAmount: "0" });
      if (url.pathname.endsWith("/open-items")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/receivables/payments/${PAY}`);
    expect(await screen.findByText(/applied with nothing left to apply/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply payment" })).not.toBeInTheDocument();
  });
});
