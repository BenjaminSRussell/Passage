import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { CashDesk, CashDeskDay, CashDeskEntry } from "./api/client";
import { header, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";
import { validateClose, validateEntry } from "./CashDesks";

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
