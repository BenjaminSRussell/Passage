import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { CashDesk, CashDeskDay, CashDeskEntry } from "./api/client";
import { header, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";
import { denominationTotal, validateClose, validateCount, validateCounterAccount, validateEntry } from "./CashDesks";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const DESK = "0190f0a0-0000-7000-8000-000000000801";

const desk: CashDesk = { id: DESK, legalEntityId: LE, code: "FRONT-1", name: "Front office", currency: "USD", mainAccountId: DESK, maxBalance: "5000.00", status: "active", version: 1, openDate: "2026-11-10", balance: "379.50" };
const day: CashDeskDay = { deskId: DESK, businessDate: "2026-11-10", status: "open", openingBalance: "0", bookBalance: "379.50", openedBy: "ana" };
const entries: CashDeskEntry[] = [
  { id: "e1", lineNo: 1, kind: "receipt", amount: "500.00", description: "Cash sale", createdBy: "ana", balanceAfter: "500.00" },
  { id: "e2", lineNo: 2, kind: "payment", amount: "120.50", description: "Taxi", createdBy: "ana", balanceAfter: "379.50" },
];

function serve() {
  server.handler = (url, init) => {
    const p = url.pathname;
    if (p === "/api/v1/treasury/cash-desks" && init.method === undefined) return json(200, { items: [desk] });
    if (p === `/api/v1/treasury/cash-desks/${DESK}`) return json(200, desk);
    if (p === `/api/v1/treasury/cash-desks/${DESK}/days` && init.method === undefined) return json(200, { items: [day] });
    if (p === `/api/v1/treasury/cash-desks/${DESK}/days/2026-11-10/entries`) return json(200, { items: entries });
    if (p === `/api/v1/treasury/cash-desks/${DESK}/entries`) return json(201, { ...entries[0], id: "e3" });
    if (p.endsWith(":close")) return json(422, { code: "TRS-0542", detail: "the count differs" });
    return undefined;
  };
}

describe("cash desk rules in the form", () => {
  it("checks entries and the count before the round trip", () => {
    expect(validateEntry("receipt", "25.00", "Sale")).toEqual({});
    expect(Object.keys(validateEntry("transfer", "0", " "))).toEqual(["kind", "amount", "description"]);
    expect(validateClose("379.50", "379.50", "")).toEqual({});
    expect(validateClose("379", "379.50", "").reason).toMatch(/differs/);
    expect(validateClose("379", "379.50", "coin").reason).toBeUndefined();
    expect(validateClose("-1", "379.50", "").counted).toMatch(/cannot be negative/);
  });
});

describe("cash desk count and posting rules in the form", () => {
  it("adds notes and coins without floating point drift", () => {
    expect(denominationTotal([{ value: "20", count: "3" }, { value: "0.25", count: "4" }])).toBe("61.00");
    expect(denominationTotal([{ value: "0.1", count: "3" }])).toBe("0.30");
    expect(denominationTotal([{ value: "", count: "3" }])).toBeNull();
    expect(validateCount([])).toHaveProperty("counted");
    expect(validateCount([{ value: "5", count: "1" }, { value: "5.00", count: "2" }]).counted).toMatch(/once/);
    expect(validateCount([{ value: "5", count: "-1" }]).counted).toMatch(/whole number/);
    expect(validateCount([{ value: "5", count: "2" }])).toEqual({});
  });

  it("asks for the other side of the cash only where the desk posts to the ledger", () => {
    expect(validateCounterAccount(false, "account", "", "")).toEqual({});
    expect(validateCounterAccount(true, "account", " ", "")).toHaveProperty("counterAccount");
    expect(validateCounterAccount(true, "account", "8000", "")).toEqual({});
    expect(validateCounterAccount(true, "account", "no spaces", "")).toHaveProperty("counterAccount");
    expect(validateCounterAccount(true, "bank", "", "")).toHaveProperty("bankAccountId");
    expect(validateCounterAccount(true, "bank", "", DESK)).toEqual({});
  });
});

describe("cash desk screens", () => {
  it("lists desks with their open day and cash on hand", async () => {
    serve();
    renderAt(`/treasury/cash-desks?legalEntityId=${LE}`);
    await screen.findByText("FRONT-1");
    expect(screen.getByText("379.50 USD")).toBeTruthy();
    expect(screen.getByText("2026-11-10")).toBeTruthy();
  });

  it("shows the journal with the balance after each line", async () => {
    serve();
    const { container } = renderAt(`/treasury/cash-desks/${DESK}`);
    await screen.findByText("Cash sale");
    expect(screen.getByText("-120.50 USD")).toBeTruthy();
    expect(screen.getAllByText("379.50 USD").length).toBeGreaterThan(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("books an entry with an Idempotency-Key", async () => {
    serve();
    renderAt(`/treasury/cash-desks/${DESK}`);
    await screen.findByText("Cash sale");
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "25.00" } });
    fireEvent.change(screen.getByLabelText("What it was for"), { target: { value: "Postage" } });
    fireEvent.click(screen.getByRole("button", { name: "Book it" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "POST" && c.url.pathname.endsWith("/entries"))).toBe(true));
    const call = server.calls.find((c) => c.method === "POST" && c.url.pathname.endsWith("/entries"))!;
    expect(call.body).toEqual({ kind: "receipt", amount: "25.00", description: "Postage" });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
  });

  it("will not send a count that differs from the books without a reason, and shows the service's refusal", async () => {
    serve();
    renderAt(`/treasury/cash-desks/${DESK}`);
    await screen.findByText("Cash sale");
    fireEvent.change(screen.getByLabelText("Cash counted"), { target: { value: "379.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Close the day" }));
    await screen.findByText("The count differs from the books. Say why.");
    expect(server.calls.some((c) => c.url.pathname.endsWith(":close"))).toBe(false);
    fireEvent.change(screen.getByLabelText(/Reason for a difference/), { target: { value: "a coin missing" } });
    fireEvent.click(screen.getByRole("button", { name: "Close the day" }));
    await screen.findByText("The count differs from the books; say why");
    expect(server.calls.find((c) => c.url.pathname.endsWith(":close"))!.body).toEqual({ countedBalance: "379.00", reason: "a coin missing" });
  });
});

describe("cash desk second signature, ledger posting and settings", () => {
  const counted: CashDeskDay = { ...day, status: "closing", countedBalance: "379.50", difference: "0.00", closedBy: "ana" };
  const dual: CashDesk = { ...desk, dualControl: true };

  it("shows a counted day with approve and send back, and approves it", async () => {
    server.handler = (url, init) => {
      const p = url.pathname;
      if (p === `/api/v1/treasury/cash-desks/${DESK}`) return json(200, dual);
      if (p === `/api/v1/treasury/cash-desks/${DESK}/days` && init.method === undefined) return json(200, { items: [counted] });
      if (p === `/api/v1/treasury/cash-desks/${DESK}/days/2026-11-10/entries`) return json(200, { items: entries });
      if (p.endsWith(":approve")) return json(200, { ...counted, status: "closed", approvedBy: "ben" });
      if (p.includes("/bank-accounts")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/treasury/cash-desks/${DESK}`);
    await screen.findByText("Second signature");
    expect(screen.queryByRole("button", { name: "Book it" })).toBeNull();
    expect(screen.getByRole("button", { name: "Send back for a recount" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Approve and close the day" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "POST" && c.url.pathname.endsWith("/days/2026-11-10:approve"))).toBe(true));
  });

  it("counts by notes and coins and sends the denominations, not a float total", async () => {
    server.handler = (url, init) => {
      const p = url.pathname;
      if (p === `/api/v1/treasury/cash-desks/${DESK}`) return json(200, { ...desk, balance: "60.00" });
      if (p === `/api/v1/treasury/cash-desks/${DESK}/days` && init.method === undefined) return json(200, { items: [{ ...day, bookBalance: "60.00" }] });
      if (p === `/api/v1/treasury/cash-desks/${DESK}/days/2026-11-10/entries`) return json(200, { items: entries });
      if (p.endsWith(":close")) return json(200, { ...day, status: "closed", countedBalance: "60.00" });
      if (p.includes("/bank-accounts")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/treasury/cash-desks/${DESK}`);
    await screen.findByText("Cash sale");
    fireEvent.change(screen.getByLabelText("Count by"), { target: { value: "coins" } });
    fireEvent.change(screen.getByLabelText("Value 1"), { target: { value: "20" } });
    fireEvent.change(screen.getByLabelText("How many of value 1"), { target: { value: "3" } });
    await screen.findByText("Total: 60.00 USD");
    fireEvent.click(screen.getByRole("button", { name: "Close the day" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname.endsWith(":close"))).toBe(true));
    expect(server.calls.find((c) => c.url.pathname.endsWith(":close"))!.body).toEqual({ denominations: [{ value: "20", count: 3 }] });
  });

  it("flags lines still waiting for the ledger and posts them again", async () => {
    const waiting: CashDeskEntry[] = [{ ...entries[0], postingStatus: "pending", counterAccount: "8000" }, { ...entries[1], postingStatus: "posted", counterAccount: "6400" }];
    server.handler = (url, init) => {
      const p = url.pathname;
      if (p === `/api/v1/treasury/cash-desks/${DESK}`) return json(200, desk);
      if (p === `/api/v1/treasury/cash-desks/${DESK}/days` && init.method === undefined) return json(200, { items: [day] });
      if (p === `/api/v1/treasury/cash-desks/${DESK}/days/2026-11-10/entries`) return json(200, { items: waiting });
      if (p.endsWith(":post")) return json(200, day);
      if (p.includes("/bank-accounts")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/treasury/cash-desks/${DESK}`);
    await screen.findByText("1 line is saved but not in the ledger yet");
    fireEvent.click(screen.getByRole("button", { name: "Post to the ledger" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "POST" && c.url.pathname.endsWith("/days/2026-11-10:post"))).toBe(true));
  });

  it("books a bank transfer with the chosen bank account", async () => {
    const bank = { id: "0190f0a0-0000-7000-8000-000000000901", accountCode: "OPS-USD", ibanMasked: "US** 4821", currency: "USD", status: "active" };
    server.handler = (url, init) => {
      const p = url.pathname;
      if (p === `/api/v1/treasury/cash-desks/${DESK}`) return json(200, desk);
      if (p === `/api/v1/treasury/cash-desks/${DESK}/days` && init.method === undefined) return json(200, { items: [day] });
      if (p === `/api/v1/treasury/cash-desks/${DESK}/days/2026-11-10/entries`) return json(200, { items: entries });
      if (p === `/api/v1/treasury/cash-desks/${DESK}/entries`) return json(201, { ...entries[1], id: "e9" });
      if (p === "/api/v1/treasury/bank-accounts") return json(200, { items: [bank, { ...bank, id: "other", currency: "EUR" }] });
      return undefined;
    };
    renderAt(`/treasury/cash-desks/${DESK}`);
    await screen.findByText("Cash sale");
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "payment" } });
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "100.00" } });
    fireEvent.change(screen.getByLabelText("What it was for"), { target: { value: "Bank deposit" } });
    fireEvent.change(screen.getByLabelText("Other side of the cash"), { target: { value: "bank" } });
    await screen.findByRole("option", { name: "OPS-USD (US** 4821)" });
    expect(screen.queryByRole("option", { name: /EUR|other/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Book it" }));
    await screen.findByText("Choose the bank account.");
    fireEvent.change(screen.getByLabelText("Bank account"), { target: { value: bank.id } });
    fireEvent.click(screen.getByRole("button", { name: "Book it" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "POST" && c.url.pathname.endsWith("/entries"))).toBe(true));
    expect(server.calls.find((c) => c.method === "POST" && c.url.pathname.endsWith("/entries"))!.body).toEqual({ kind: "payment", amount: "100.00", description: "Bank deposit", bankAccountId: bank.id });
  });

  it("saves only the changed settings with the desk's version and deactivates a desk with no open day", async () => {
    const idle: CashDesk = { ...desk, openDate: undefined, balance: undefined };
    server.handler = (url, init) => {
      const p = url.pathname;
      if (p === `/api/v1/treasury/cash-desks/${DESK}` && init.method === "PATCH") return json(200, { ...idle, name: "Reception", version: 2 });
      if (p === `/api/v1/treasury/cash-desks/${DESK}`) return json(200, idle);
      if (p === `/api/v1/treasury/cash-desks/${DESK}/days` && init.method === undefined) return json(200, { items: [{ ...day, status: "closed" }] });
      if (p === `/api/v1/treasury/cash-desks/${DESK}/days/2026-11-10/entries`) return json(200, { items: entries });
      if (p.endsWith(":deactivate")) return json(200, { ...idle, status: "inactive", version: 2 });
      if (p.includes("/bank-accounts")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/treasury/cash-desks/${DESK}`);
    await screen.findByText("Desk settings");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Reception" } });
    fireEvent.click(screen.getByRole("button", { name: "Save the desk" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "PATCH")).toBe(true));
    const patch = server.calls.find((c) => c.method === "PATCH")!;
    expect(patch.body).toEqual({ name: "Reception" });
    expect(header(patch, "If-Match")).toBe('"1"');
    fireEvent.click(screen.getByRole("button", { name: "Deactivate the desk" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname.endsWith(":deactivate"))).toBe(true));
  });
});
