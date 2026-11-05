import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BalanceSnapshot, Payment, Statement } from "./api/client";
import { emptyPayment, toPaymentInput, validatePayment } from "./NewPaymentPage";
import { sourceLabel, statementLink } from "./Statements";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "0190f0a0-0000-7000-8000-0000000000c1";
const CUST2 = "0190f0a0-0000-7000-8000-0000000000c2";
const PAY = "0190f0a0-0000-7000-8000-000000000301";

const statement: Statement = {
  legalEntityId: LE,
  customerId: CUST,
  asOf: "2027-03-31",
  sections: [
    {
      currency: "USD",
      closingBalance: "800",
      aging: [
        { currency: "USD", bucket: "CURRENT", amount: "500" },
        { currency: "USD", bucket: "31-60", amount: "100" },
      ],
      lines: [
        { sourceType: "INVOICE", documentNumber: "INV-S1", documentDate: "2027-01-05", dueDate: "2027-02-04", amount: "1000", openAmount: "100", runningBalance: "100" },
        { sourceType: "INVOICE", documentNumber: "INV-S2", documentDate: "2027-03-30", dueDate: "2027-04-29", amount: "500", openAmount: "500", runningBalance: "600" },
        { sourceType: "DEDUCTION", documentNumber: "INV-S1", documentDate: "2027-03-20", dueDate: null, amount: "300", openAmount: "300", runningBalance: "900" },
        { sourceType: "PAYMENT", documentNumber: "WIRE-77", documentDate: "2027-03-20", dueDate: null, amount: "-700", openAmount: "-100", runningBalance: "800" },
      ],
    },
  ],
};

const snapshot = (customerId: string, patch: Partial<BalanceSnapshot> = {}): BalanceSnapshot => ({
  legalEntityId: LE,
  customerId,
  currency: "USD",
  asOf: "2027-03-31",
  totalOpen: "500.00",
  overdue: "400.00",
  oldestItemDays: 55,
  dunningLevel: 3,
  riskScore: 62,
  takenAt: "2027-03-31T01:30:00Z",
  ...patch,
});

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("Customer statement", () => {
  it("asks for the legal entity and customer before loading anything", async () => {
    const { container } = renderAt("/receivables/statements");
    expect(await screen.findByText("Choose a customer")).toBeInTheDocument();
    fill("Customer id", "not-a-uuid");
    expect(screen.getByText("Enter the customer id (a UUID).")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("loads from the address, lists the lines with the running balance, the closing balance and the aging footer", async () => {
    server.handler = (url) => (url.pathname.endsWith("/statements") ? json(200, statement) : undefined);
    const { container } = renderAt(`/receivables/statements?legalEntityId=${LE}&customer=${CUST}&asOf=2027-03-31`);
    const lines = await findTable({ name: "USD statement lines" });
    const call = server.calls[0];
    expect(call.url.searchParams.get("legalEntityId")).toBe(LE);
    expect(call.url.searchParams.get("customer")).toBe(CUST);
    expect(call.url.searchParams.get("asOf")).toBe("2027-03-31");
    const rows = within(lines).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(4);
    expect(rows[0]).toHaveTextContent("INV-S1");
    expect(rows[2]).toHaveTextContent("Deduction");
    expect(rows[3]).toHaveTextContent("Unapplied payment");
    expect(rows[3]).toHaveTextContent("-100");
    expect(rows[3]).toHaveTextContent("800");
    expect(screen.getByText("800.00 USD")).toBeInTheDocument();
    const aging = screen.getByRole("table", { name: "USD aging" });
    expect(aging).toHaveTextContent("500.00");
    expect(aging).toHaveTextContent("600.00");
    expect(pageViolations(container)).toEqual([]);
  });

  it("reloads for another statement date", async () => {
    server.handler = (url) => (url.pathname.endsWith("/statements") ? json(200, { ...statement, asOf: url.searchParams.get("asOf") }) : undefined);
    renderAt(`/receivables/statements?legalEntityId=${LE}&customer=${CUST}&asOf=2027-03-31`);
    await findTable({ name: "USD statement lines" });
    fill("Statement date", "2027-01-31");
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("asOf")).toBe("2027-01-31"));
  });

  it("shows the empty and error states", async () => {
    server.handler = () => json(200, { ...statement, sections: [] });
    const first = renderAt(`/receivables/statements?legalEntityId=${LE}&customer=${CUST}&asOf=2027-03-31`);
    expect(await screen.findByText("Nothing open")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty receivables.view" });
    renderAt(`/receivables/statements?legalEntityId=${LE}&customer=${CUST}`);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load the statement");
    expect(alert).toHaveTextContent("AUTH-0003");
  });

  it("labels lines and builds links", () => {
    expect(sourceLabel({ sourceType: "DEDUCTION", dueDate: null })).toBe("Deduction");
    expect(sourceLabel({ sourceType: "DEDUCTION", dueDate: "2027-04-01" })).toBe("Deduction re-bill");
    expect(sourceLabel({ sourceType: "FEE", dueDate: "2027-04-01" })).toBe("Dunning fee");
    expect(sourceLabel({ sourceType: "SOMETHING", dueDate: null })).toBe("SOMETHING");
    expect(statementLink(LE, CUST)).toBe(`/receivables/statements?legalEntityId=${LE}&customer=${CUST}`);
    expect(statementLink(LE, CUST, "2027-03-31")).toContain("asOf=2027-03-31");
  });
});

describe("Balance snapshots", () => {
  it("lists the latest snapshot riskiest first and opens a customer's statement", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/aging-snapshots") && (init.method ?? "GET") === "GET") {
        return json(200, { items: [snapshot(CUST), snapshot(CUST2, { overdue: "0", dunningLevel: 0, riskScore: 10 })] });
      }
      if (url.pathname.endsWith("/statements")) return json(200, statement);
      return undefined;
    };
    const { container } = renderAt("/receivables/balance-snapshots");
    expect(await screen.findByText("Choose a legal entity")).toBeInTheDocument();
    fill("Legal entity id", LE);
    const table = await findTable({ name: "Balance snapshots for 2027-03-31" });
    expect(server.calls[0].url.searchParams.get("legalEntityId")).toBe(LE);
    expect(server.calls[0].url.searchParams.has("asOf")).toBe(false);
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows[0]).toHaveTextContent("62");
    expect(rows[0]).toHaveTextContent("400.00 USD");
    const tiles = screen.getByRole("list", { name: "Snapshot totals" });
    expect(within(tiles).getByText("With overdue balances").parentElement).toHaveTextContent("1");
    expect(within(tiles).getByText("At dunning level 3 or 4").parentElement).toHaveTextContent("1");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(rows[0]);
    expect(await screen.findByRole("heading", { name: "Customer statement" })).toBeInTheDocument();
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("customer")).toBe(CUST));
  });

  it("runs the snapshot for the chosen date, reports it and reloads the list", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/aging-snapshots") && init.method === "POST") return json(200, { legalEntityId: LE, asOf: "2027-03-30", rows: 2, changed: 1 });
      if (url.pathname.endsWith("/aging-snapshots")) return json(200, { items: [] });
      return undefined;
    };
    renderAt("/receivables/balance-snapshots");
    fill("Legal entity id", LE);
    fill("Key date", "2027-03-30");
    expect(await screen.findByText("No snapshot")).toBeInTheDocument();
    const lists = server.calls.filter((c) => c.method === "GET").length;
    fireEvent.click(screen.getByRole("button", { name: "Run snapshot now" }));
    expect(await findStatus()).toHaveTextContent("2 balance(s) stored for 2027-03-30; 1 change(s) announced.");
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ legalEntityId: LE, asOf: "2027-03-30" });
    await waitFor(() => expect(server.calls.filter((c) => c.method === "GET").length).toBeGreaterThan(lists));
  });

  it("shows a refused run and a failed load", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(403, { code: "AUTH-0003", detail: "missing duty receivables.aging.run" });
      if (url.pathname.endsWith("/aging-snapshots")) return json(200, { items: [] });
      return undefined;
    };
    const first = renderAt("/receivables/balance-snapshots");
    fill("Legal entity id", LE);
    await screen.findByText("No snapshot");
    fireEvent.click(screen.getByRole("button", { name: "Run snapshot now" }));
    const refused = await findAlert();
    expect(refused).toHaveTextContent("You may not run the snapshot");
    expect(refused).toHaveTextContent("receivables.aging.run");
    first.unmount();
    server.handler = () => json(500, { code: "COM-0500", detail: "database unavailable" });
    renderAt("/receivables/balance-snapshots");
    fill("Legal entity id", LE);
    const failed = await findAlert();
    expect(failed).toHaveTextContent("Could not load the balance snapshots");
    expect(failed).toHaveTextContent("COM-0500");
  });
});

describe("Record payment: payer identity", () => {
  it("validates the payer account and short-pay reason and sends them", () => {
    const draft = { ...emptyPayment("2027-03-05"), legalEntityId: LE, amount: "995.00" };
    expect(validatePayment({ ...draft, payerAccount: "12-34" })).toHaveProperty("payerAccount");
    expect(validatePayment({ ...draft, shortPayReason: "x".repeat(121) })).toHaveProperty("shortPayReason");
    expect(validatePayment({ ...draft, payerAccount: "DE89 3704 0044 0532 0130 00", shortPayReason: "promo" })).toEqual({});
    const input = toPaymentInput({ ...draft, payerAccount: " DE89 3704 0044 0532 0130 00 ", shortPayReason: " promo " });
    expect(input.payerAccount).toBe("DE89 3704 0044 0532 0130 00");
    expect(input.shortPayReason).toBe("promo");
    expect(toPaymentInput(draft)).toMatchObject({ payerAccount: null, shortPayReason: null });
  });

  it("posts the account and reason from the form and shows them on the payment", async () => {
    const created: Payment = {
      id: PAY,
      legalEntityId: LE,
      customerId: null,
      paymentMode: "CHECK",
      source: "MANUAL",
      sourceRef: "CHK-9",
      amount: "995.00",
      unappliedAmount: "995.00",
      currency: "USD",
      receivedDate: "2027-03-05",
      valueDate: "2027-03-05",
      payerName: "Acme Inc",
      payerAccountHash: "a".repeat(64),
      shortPayReason: "damaged pallet",
      remittance: [],
      status: "RECEIVED",
      version: 1,
    };
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/payments") && init.method === "POST") return json(201, created);
      if (url.pathname.endsWith(`/payments/${PAY}`)) return json(200, created);
      if (url.pathname.endsWith("/open-items")) return json(200, { items: [] });
      return undefined;
    };
    renderAt("/receivables/payments/new");
    fill("Legal entity id", LE);
    fill("Amount", "995.00");
    fill("Check or bank reference", "CHK-9");
    fill("Payer bank account", "DE89 3704 0044 0532 0130 00");
    fill("Short-pay reason", "damaged pallet");
    fireEvent.click(screen.getByRole("button", { name: "Record payment" }));
    expect(await screen.findByRole("heading", { name: "Payment CHK-9" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(idempotencyKey(post)).toBeTruthy();
    expect(post.body).toMatchObject({ payerAccount: "DE89 3704 0044 0532 0130 00", shortPayReason: "damaged pallet" });
    expect(screen.getByText("Given (kept as a hash)")).toBeInTheDocument();
    expect(screen.getByText("damaged pallet")).toBeInTheDocument();
  });
});
