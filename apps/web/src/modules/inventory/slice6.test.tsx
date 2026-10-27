import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import inventory from "./module";
import { movesFor } from "./TransactionPage";

const TXN_ID = "0190a0b0-0000-7000-8000-000000000061";
const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const CHANGE_ID = "0190a0b0-0000-7000-8000-000000000062";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];

const json = (status: number, body: unknown) => ({ status, body });
const sent = (method: string) => calls.filter((c) => c.method === method);
const header = (call: { init: RequestInit } | undefined, name: string) => new Headers(call?.init.headers).get(name);

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const { status, body } = handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
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
        {inventory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

function change(overrides: Record<string, unknown> = {}) {
  return {
    id: CHANGE_ID,
    legalEntityId: ENTITY_ID,
    transactionId: TXN_ID,
    command: "CREATE",
    toStatus: "REGISTERED",
    qty: "5",
    date: "2026-10-01",
    occurredAt: "2026-10-01T08:00:00Z",
    ...overrides,
  };
}

function txn(overrides: Record<string, unknown> = {}) {
  return {
    id: TXN_ID,
    legalEntityId: ENTITY_ID,
    originId: "o1",
    sourceType: "RECEIPT_LINE",
    sourceId: "s1",
    reference: "GR-0042",
    productId: PRODUCT_ID,
    dimensions: { site: "S1", warehouse: "W1" },
    qty: "5",
    enteredQty: "5",
    enteredUnit: "EA",
    receiptStatus: "REGISTERED",
    physicalDate: "2026-10-01",
    currency: "USD",
    costAmountPhysical: "10.00",
    createdAt: "2026-10-01T08:00:00Z",
    statusChanges: [change()],
    ...overrides,
  };
}

describe("named backward moves", () => {
  it("offers exactly the moves of BR-16.1 for each status", () => {
    expect(movesFor("PICKED")).toEqual([{ command: "UNPICK", to: "RESERVED_PHYSICAL" }]);
    expect(movesFor("REGISTERED")).toEqual([{ command: "UNREGISTER", to: "ARRIVED" }]);
    expect(movesFor("RESERVED_ORDERED")).toEqual([{ command: "CANCEL_RESERVATION", to: "ON_ORDER" }]);
    expect(movesFor("SOLD")).toEqual([]);
    expect(movesFor("RECEIVED")).toEqual([]);
    expect(movesFor(undefined)).toEqual([]);
  });
});

describe("Transaction detail", () => {
  it("shows the transaction, its history and unregisters a registered receipt (AC-16.8)", async () => {
    let state = txn();
    handler = (url, init) => {
      if (init.method === "POST") {
        state = txn({
          receiptStatus: "ARRIVED",
          statusChanges: [change(), change({ id: "c2", command: "UNREGISTER", fromStatus: "REGISTERED", toStatus: "ARRIVED", date: "2026-10-02" })],
        });
        return json(201, change({ id: "c2", command: "UNREGISTER", fromStatus: "REGISTERED", toStatus: "ARRIVED" }));
      }
      expect(url.pathname).toBe(`/api/v1/inventory/transactions/${TXN_ID}`);
      return json(200, state);
    };
    const { container } = renderAt(`/inventory/transactions/${TXN_ID}`);
    expect(await screen.findByRole("heading", { name: "Transaction 0190a0b0" })).toBeInTheDocument();
    expect(screen.getByText("Receipt · Registered")).toBeInTheDocument();
    expect(screen.getByText("Receipt line GR-0042")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Status history" })).toHaveTextContent("Registered");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Date of the move"), { target: { value: "2026-10-02" } });
    fireEvent.click(screen.getByRole("button", { name: "Unregister to arrived" }));
    expect(await screen.findByText("Unregister: registered to arrived.")).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post?.url.pathname).toBe(`/api/v1/inventory/transactions/${TXN_ID}:unregister`);
    expect(JSON.parse(post?.init.body as string)).toEqual({ date: "2026-10-02" });
    expect(header(post, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    await waitFor(() => expect(screen.getByText("Receipt · Arrived")).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: /Unregister/ })).toBeNull();
    expect(screen.getByRole("table", { name: "Status history" })).toHaveTextContent("Unregister");
  });

  it("retries a failed move with the same Idempotency-Key and shows the problem", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? json(422, { code: "INV-NEG-001", detail: "issue would make available physical stock negative" })
        : json(200, txn({ receiptStatus: undefined, issueStatus: "PICKED", qty: "-2", sourceType: "SALES_LINE", statusChanges: [] }));
    renderAt(`/inventory/transactions/${TXN_ID}`);
    const unpick = await screen.findByRole("button", { name: "Unpick to reserved physical" });
    fireEvent.click(unpick);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The status was not changed");
    expect(alert).toHaveTextContent("(INV-NEG-001)");
    fireEvent.click(screen.getByRole("button", { name: "Unpick to reserved physical" }));
    await waitFor(() => expect(sent("POST")).toHaveLength(2));
    const [first, second] = sent("POST");
    expect(header(first, "Idempotency-Key")).toBe(header(second, "Idempotency-Key"));
    expect(screen.getByRole("table", { name: "Status history" })).toHaveTextContent("No status changes");
  });

  it("explains when no backward move applies", async () => {
    handler = () => json(200, txn({ receiptStatus: "PURCHASED", financialDate: "2026-10-03", costAmountPosted: "10.00" }));
    renderAt(`/inventory/transactions/${TXN_ID}`);
    expect(await screen.findByText(/No backward move applies at purchased/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /to / })).toBeNull();
  });

  it("shows not found for an unknown transaction", async () => {
    handler = () => json(404, { code: "INV-TXN-001", detail: "inventory transaction not found" });
    renderAt(`/inventory/transactions/${TXN_ID}`);
    expect(await screen.findByText("Transaction not found")).toBeInTheDocument();
  });

  it("reports a load failure with a retry", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "unexpected error" });
    renderAt(`/inventory/transactions/${TXN_ID}`);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load the transaction");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("Status changes inquiry", () => {
  it("lists changes newest first, links to the transaction and filters by command", async () => {
    handler = (url) =>
      json(200, {
        items: [change({ id: url.searchParams.get("cursor") ? "older" : CHANGE_ID, command: "UNPICK", fromStatus: "PICKED", toStatus: "RESERVED_PHYSICAL", qty: "-2" })],
        nextCursor: url.searchParams.get("cursor") ? undefined : CHANGE_ID,
      });
    const { container } = renderAt("/inventory/status-changes");
    const table = await findTable({ name: "Status changes" });
    expect(table).toHaveTextContent("Unpick");
    expect(table).toHaveTextContent("Reserved physical");
    expect(screen.getByRole("link", { name: "0190a0b0" })).toHaveAttribute("href", `/inventory/transactions/${TXN_ID}`);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText(/Command/), { target: { value: "UNPICK" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("command")).toBe("UNPICK"));

    fireEvent.click(await screen.findByRole("button", { name: "Load more status changes" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("cursor")).toBe(CHANGE_ID));
  });

  it("validates the transaction filter before calling the service", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/inventory/status-changes");
    expect(await screen.findByText("No status changes match")).toBeInTheDocument();
    const before = calls.length;
    fireEvent.change(screen.getByLabelText(/Transaction/), { target: { value: "not-an-id" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("Enter the transaction id (a UUID).")).toBeInTheDocument();
    expect(calls.length).toBe(before);
  });

  it("reports a failed load", async () => {
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty inventory.read" });
    renderAt("/inventory/status-changes");
    expect(await findAlert()).toHaveTextContent("Could not load status changes");
  });
});

describe("Transaction inquiry", () => {
  it("links each transaction to its detail and names external origins", async () => {
    handler = () => json(200, { items: [txn({ statusChanges: undefined })] });
    renderAt("/inventory/transactions");
    expect(await screen.findByRole("link", { name: "Transaction 0190a0b0" })).toHaveAttribute("href", `/inventory/transactions/${TXN_ID}`);
    expect(screen.getByRole("table", { name: "Transactions" })).toHaveTextContent("Receipt line");
  });
});
