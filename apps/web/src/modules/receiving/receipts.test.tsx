import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Receipt, ReceiptLine } from "./api/client";
import { emptyArrival, pickPOLine, toArrivalInput, validateArrival } from "./NewArrivalPage";
import { validateCorrection, validatePostingDate, validateRegister } from "./ReceiptPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const ID = "0190a000-0000-7000-8000-000000000010";
const UUID_A = "11111111-1111-4111-8111-111111111111";
const UUID_B = "22222222-2222-4222-8222-222222222222";
const UUID_C = "33333333-3333-4333-8333-333333333333";
const UUID_D = "44444444-4444-4444-8444-444444444444";

function line(patch: Partial<ReceiptLine> = {}): ReceiptLine {
  return {
    lineNo: 1,
    poLineId: UUID_C,
    productId: UUID_D,
    orderedRemaining: "10",
    overDeliveryPct: "5",
    unitPrice: "2.5",
    unit: "EA",
    batchTracked: false,
    serialTracked: false,
    qty: "10",
    batch: "",
    serials: [],
    locationId: "",
    inventoryStatus: "AVAILABLE",
    amount: null,
    amountAcct: null,
    ...patch,
  };
}

function receipt(patch: Partial<Receipt> = {}): Receipt {
  return {
    id: ID,
    receiptNumber: "RCV-00000001",
    state: "ARRIVED",
    version: 1,
    vendorId: UUID_A,
    purchaseOrderId: UUID_B,
    legalEntityId: UUID_A,
    packingSlip: "PS-1",
    carrier: "DHL",
    receiptDate: "2026-10-25",
    currency: "USD",
    exchangeRate: "1",
    createdAt: "2026-10-25T10:00:00Z",
    lines: [line()],
    ...patch,
  };
}

const method = (init: RequestInit) => init.method ?? "GET";
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const commandCalls = () => server.calls.filter((c) => c.method === "POST");

function serve(current: Receipt, onCommand?: (url: URL, init: RequestInit) => ReturnType<typeof json>) {
  server.handler = (url, init) => {
    if (method(init) === "POST") return onCommand?.(url, init);
    if (url.pathname === `/api/v1/receiving/receipts/${current.id}`) return json(200, current);
    return undefined;
  };
}

describe("client-side checks", () => {
  it("accepts a complete arrival and rejects each bad field", () => {
    const ok = {
      ...emptyArrival,
      vendorId: UUID_A,
      purchaseOrderId: UUID_B,
      legalEntityId: UUID_A,
      packingSlip: "PS-1",
      receiptDate: "2026-10-25",
      lines: [{ ...emptyArrival.lines[0], poLineNo: "2", productId: UUID_D, unit: "EA" }],
    };
    expect(validateArrival(ok)).toEqual({});
    expect(toArrivalInput(ok).lines[0]).toEqual({ poLineNo: 2, productId: UUID_D, overDeliveryPct: "0", unit: "EA", batchTracked: false, serialTracked: false });
    const bad = validateArrival({ ...ok, vendorId: "x", packingSlip: " ", currency: "usd", lines: [{ ...ok.lines[0], poLineNo: "0", overDeliveryPct: "101", unit: "" }] });
    expect(Object.keys(bad).sort()).toEqual(["currency", "lines[0].overDeliveryPct", "lines[0].poLineNo", "lines[0].unit", "packingSlip", "vendorId"]);
    expect(validateArrival({ ...ok, lines: [{ ...ok.lines[0], poLineNo: "x" }] })).toHaveProperty("lines[0].poLineNo");
    expect(validateArrival({ ...ok, lines: [ok.lines[0], ok.lines[0]] })).toEqual({ "lines[1].poLineNo": "Use each purchase order line once." });
  });

  it("copies product and unit from the picked purchase order line", () => {
    const poLines = [{ lineNo: 2, productId: UUID_D, qty: "10", unit: "BOX", status: "OPEN" }, { lineNo: 3, qty: "1", unit: "HR", status: "OPEN" }];
    const base = { ...emptyArrival.lines[0], productId: UUID_C };
    expect(pickPOLine(base, "2", poLines)).toMatchObject({ poLineNo: "2", productId: UUID_D, unit: "BOX" });
    expect(pickPOLine(base, "3", poLines)).toMatchObject({ poLineNo: "3", productId: UUID_C, unit: "HR" });
    expect(pickPOLine(base, "", poLines)).toEqual({ ...base, poLineNo: "" });
  });

  it("requires one serial per unit and a batch where tracked", () => {
    const r = receipt({ lines: [line({ serialTracked: true, batchTracked: true })] });
    const draft = { qty: "2", batch: "", serials: "S1", locationId: UUID_A, inventoryStatus: "AVAILABLE" };
    expect(Object.keys(validateRegister(r, [draft])).sort()).toEqual(["lines[0].batch", "lines[0].serials"]);
    expect(validateRegister(r, [{ ...draft, batch: "B1", serials: "S1\nS2\n" }])).toEqual({});
    expect(validateRegister(r, [{ ...draft, qty: "0", locationId: "nope" }])).toHaveProperty("lines[0].qty");
  });

  it("needs at least one non-zero correction", () => {
    const r = receipt({ state: "POSTED", lines: [line(), line({ lineNo: 2 })] });
    expect(validateCorrection(r, ["", " "])).toHaveProperty("lines");
    expect(validateCorrection(r, ["0", ""])).toHaveProperty("lines[0].qtyDelta");
    expect(validateCorrection(r, ["-2", "1.5"])).toEqual({});
    expect(validateCorrection(r, ["x", ""])).toHaveProperty("lines[0].qtyDelta");
  });
});

describe("receipts list", () => {
  it("lists receipts, filters by state and pages with the cursor", async () => {
    server.handler = (url) => {
      if (url.pathname !== "/api/v1/receiving/receipts") return undefined;
      if (url.searchParams.get("cursor") === "next") return json(200, { items: [receipt({ id: UUID_B, receiptNumber: "RCV-00000002", state: "POSTED" })] });
      return json(200, { items: [receipt()], nextCursor: url.searchParams.get("state") === "POSTED" ? undefined : "next" });
    };
    const { container } = renderAt("/receiving/receipts");

    expect(await screen.findByRole("button", { name: "RCV-00000001" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Load more receipts" }));
    expect(await screen.findByRole("button", { name: "RCV-00000002" })).toBeInTheDocument();
    expect(server.calls.at(-1)?.url.searchParams.get("cursor")).toBe("next");
    expect(screen.queryByRole("button", { name: "Load more receipts" })).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("State"), { target: { value: "POSTED" } });
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("state")).toBe("POSTED"));
  });

  it("opens a receipt from its number", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/receiving/receipts" ? json(200, { items: [receipt()] }) : json(200, receipt()));
    renderAt("/receiving/receipts");
    fireEvent.click(await screen.findByRole("button", { name: "RCV-00000001" }));
    expect(await screen.findByRole("heading", { name: "Receipt RCV-00000001" })).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(`/receiving/receipts/${ID}`);
  });

  it("shows an empty state and a retryable error", async () => {
    server.handler = () => json(200, { items: [] });
    const { container, unmount } = renderAt("/receiving/receipts");
    expect(await screen.findByText("No receipts yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    unmount();

    server.handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/receiving/receipts");
    expect(await screen.findByRole("alert")).toHaveTextContent("internal error");
    server.handler = () => json(200, { items: [receipt()] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "RCV-00000001" })).toBeInTheDocument();
  });
});

describe("record arrival", () => {
  function fill(label: string, value: string) {
    fireEvent.change(screen.getByLabelText(label), { target: { value } });
  }

  function fillHeader() {
    fill("Vendor ID", UUID_A);
    fill("Purchase order ID", UUID_B);
    fill("Legal entity ID", UUID_A);
    fill("Packing slip", "PS-1");
    fill("Receipt date", "2026-10-25");
  }

  function fillValid() {
    fillHeader();
    fill("Line 1 PO line", "1");
    fill("Line 1 product ID", UUID_D);
    fill("Line 1 unit", "EA");
  }

  const poPath = `/api/v1/procurement/purchase-orders/${UUID_B}`;
  const poLines = [
    { lineNo: 1, productId: UUID_C, qty: "5", unit: "EA", status: "CANCELLED" },
    { lineNo: 2, productId: UUID_D, description: "Bolts", qty: "10", unit: "BOX", status: "OPEN" },
  ];

  it("blocks an empty form with field errors and sends nothing", async () => {
    const { container } = renderAt("/receiving/receipts/new");
    fireEvent.click(screen.getByRole("button", { name: "Record arrival" }));
    expect(await screen.findAllByRole("alert")).not.toHaveLength(0);
    expect(screen.getByLabelText("Vendor ID")).toBeInvalid();
    expect(screen.getByLabelText("Line 1 unit")).toBeInvalid();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("posts poLineNo with an Idempotency-Key and opens the new receipt", async () => {
    serve(receipt(), () => json(201, receipt()));
    renderAt("/receiving/receipts/new");
    fillValid();
    expect(await screen.findByText("Could not read the purchase order's lines; enter the line number.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Record arrival" }));

    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent(`/receiving/receipts/${ID}`));
    const [call] = commandCalls();
    expect(call.url.pathname).toBe("/api/v1/receiving/arrivals");
    expect(idempotencyKey(call)).toMatch(/^[0-9a-f-]{36}$/);
    expect(call.body).toMatchObject({ packingSlip: "PS-1", currency: "USD" });
    expect((call.body as { lines: unknown[] }).lines).toEqual([{ poLineNo: 1, productId: UUID_D, overDeliveryPct: "0", unit: "EA", batchTracked: false, serialTracked: false }]);
  });

  it("picks the line from the purchase order and fills its product and unit", async () => {
    server.handler = (url, init) => {
      if (method(init) === "POST") return json(201, receipt());
      if (url.pathname === poPath) return json(200, { id: UUID_B, number: "PO-1", lines: poLines });
      if (url.pathname === `/api/v1/receiving/receipts/${ID}`) return json(200, receipt());
      return undefined;
    };
    const { container } = renderAt("/receiving/receipts/new");
    fillHeader();
    const select = await screen.findByRole("combobox", { name: "Line 1 PO line" });
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["Choose a line", "2: Bolts (10 BOX)"]);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(select, { target: { value: "2" } });
    expect(screen.getByLabelText("Line 1 product ID")).toHaveValue(UUID_D);
    expect(screen.getByLabelText("Line 1 unit")).toHaveValue("BOX");
    fireEvent.click(screen.getByRole("button", { name: "Record arrival" }));

    await waitFor(() => expect(commandCalls()).toHaveLength(1));
    expect(server.calls.some((c) => c.method === "GET" && c.url.pathname === poPath)).toBe(true);
    expect((commandCalls()[0].body as { lines: unknown[] }).lines).toEqual([{ poLineNo: 2, productId: UUID_D, overDeliveryPct: "0", unit: "BOX", batchTracked: false, serialTracked: false }]);
  });

  it("requires a PO line before sending", async () => {
    server.handler = (url) => (url.pathname === poPath ? json(200, { id: UUID_B, lines: poLines }) : undefined);
    renderAt("/receiving/receipts/new");
    fillHeader();
    await screen.findByRole("combobox", { name: "Line 1 PO line" });
    fireEvent.click(screen.getByRole("button", { name: "Record arrival" }));
    expect(await screen.findByText("Choose the purchase order line.")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Line 1 PO line" })).toBeInvalid();
    expect(commandCalls()).toHaveLength(0);
  });

  it("shows the service's field error and keeps what was typed", async () => {
    serve(receipt(), () => json(409, { code: "RCV-DUP-001", detail: "Packing slip already used on receipt RCV-00000009", errors: { packingSlip: "already received" } }));
    renderAt("/receiving/receipts/new");
    fillValid();
    fireEvent.click(screen.getByRole("button", { name: "Record arrival" }));

    expect(await screen.findByText(/Packing slip already used on receipt RCV-00000009 \(RCV-DUP-001\)/)).toBeInTheDocument();
    expect(screen.getByLabelText("Packing slip")).toBeInvalid();
    expect(screen.getByLabelText("Packing slip")).toHaveValue("PS-1");
  });

  it("adds and removes lines with unique labels", () => {
    renderAt("/receiving/receipts/new");
    expect(screen.queryByRole("button", { name: "Remove line 1" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    expect(screen.getByLabelText("Line 2 unit")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove line 2" }));
    expect(screen.queryByLabelText("Line 2 unit")).not.toBeInTheDocument();
  });
});

describe("receipt page", () => {
  it("registers an arrived receipt with the row version and a key", async () => {
    serve(receipt(), () => json(200, receipt({ state: "REGISTERED", version: 2 })));
    const { container } = renderAt(`/receiving/receipts/${ID}`);
    expect(await screen.findByRole("heading", { name: "Receipt RCV-00000001" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Register receipt" }));
    expect(await screen.findByText("Enter a location UUID.")).toBeInTheDocument();
    expect(commandCalls()).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Line 1 location ID"), { target: { value: UUID_A } });
    fireEvent.change(screen.getByLabelText("Line 1 quantity received"), { target: { value: "8" } });
    fireEvent.click(screen.getByRole("button", { name: "Register receipt" }));

    expect(await screen.findByRole("button", { name: "Post receipt" })).toBeInTheDocument();
    const [call] = commandCalls();
    expect(call.url.pathname).toBe(`/api/v1/receiving/receipts/${ID}:register`);
    expect(header(call, "If-Match")).toBe('"1"');
    expect(idempotencyKey(call)).toBeTruthy();
    expect(call.body).toEqual({ lines: [{ lineNo: 1, qty: "8", locationId: UUID_A, inventoryStatus: "AVAILABLE" }] });
  });

  it("posts a registered receipt and then offers corrections", async () => {
    serve(receipt({ state: "REGISTERED", version: 2 }), () => json(200, receipt({ state: "POSTED", version: 3, ledgerEntryId: "je-1", postingDate: "2026-10-25", lines: [line({ amount: "25.00" })] })));
    const { container } = renderAt(`/receiving/receipts/${ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Post receipt" }));
    expect(commandCalls()).toHaveLength(0);
    expect(screen.getByRole("button", { name: "Confirm post" })).toHaveFocus();
    fireEvent.click(screen.getByRole("button", { name: "Confirm post" }));

    expect(await screen.findByRole("form", { name: "Correct receipt" })).toBeInTheDocument();
    expect(screen.getByText("je-1")).toBeInTheDocument();
    const [call] = commandCalls();
    expect(call.url.pathname).toBe(`/api/v1/receiving/receipts/${ID}:post`);
    expect(header(call, "If-Match")).toBe('"2"');
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains a stale version and reloads on request", async () => {
    serve(receipt({ state: "REGISTERED", version: 2 }), () => json(412, { code: "COM-0412", detail: "receipt is at version 3, If-Match carried 2" }));
    renderAt(`/receiving/receipts/${ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Post receipt" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm post" }));

    expect(await screen.findByText(/receipt is at version 3, If-Match carried 2 \(COM-0412\)/)).toBeInTheDocument();
    const before = server.calls.filter((c) => c.method === "GET").length;
    fireEvent.click(screen.getByRole("button", { name: "Reload receipt" }));
    await waitFor(() => expect(server.calls.filter((c) => c.method === "GET").length).toBe(before + 1));
  });

  it("sends only the lines that changed when correcting a posted receipt", async () => {
    const posted = receipt({ state: "POSTED", version: 3, lines: [line({ amount: "25.00" }), line({ lineNo: 2, amount: "25.00" })] });
    serve(posted, () => json(200, { ...posted, version: 4 }));
    renderAt(`/receiving/receipts/${ID}`);
    await screen.findByRole("form", { name: "Correct receipt" });

    fireEvent.click(screen.getByRole("button", { name: "Post correction" }));
    expect(await screen.findByText("Enter a change for at least one line.")).toBeInTheDocument();
    expect(commandCalls()).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Line 2 quantity change"), { target: { value: "-4" } });
    fireEvent.click(screen.getByRole("button", { name: "Post correction" }));
    expect(commandCalls()).toHaveLength(0);
    expect(screen.getByRole("button", { name: "Confirm correction" })).toHaveFocus();
    fireEvent.click(screen.getByRole("button", { name: "Keep editing" }));
    fireEvent.click(screen.getByRole("button", { name: "Post correction" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm correction" }));
    await waitFor(() => expect(commandCalls()).toHaveLength(1));
    expect(commandCalls()[0].url.pathname).toBe(`/api/v1/receiving/receipts/${ID}:correct`);
    expect(commandCalls()[0].body).toEqual({ lines: [{ lineNo: 2, qtyDelta: "-4" }] });
  });

  it("asks before cancelling and reports a refusal", async () => {
    serve(receipt({ state: "POSTED", version: 3, lines: [line({ amount: "25.00" })] }), () => json(409, { code: "RCV-STA-001", detail: "receipt has invoiced quantity and cannot be cancelled" }));
    renderAt(`/receiving/receipts/${ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel receipt" }));
    expect(commandCalls()).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Keep receipt" }));
    expect(screen.queryByRole("button", { name: "Confirm cancel" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Cancel receipt" }));
    expect(screen.getByRole("button", { name: "Confirm cancel" })).toHaveFocus();
    fireEvent.click(screen.getByRole("button", { name: "Confirm cancel" }));
    expect(await screen.findByText(/invoiced quantity and cannot be cancelled \(RCV-STA-001\)/)).toBeInTheDocument();
    expect(commandCalls()[0].url.pathname).toBe(`/api/v1/receiving/receipts/${ID}:cancel`);
    expect(header(commandCalls()[0], "If-Match")).toBe('"3"');
  });

  it("offers no actions on a cancelled receipt and reports a missing one", async () => {
    serve(receipt({ state: "CANCELLED", version: 4 }));
    const { container, unmount } = renderAt(`/receiving/receipts/${ID}`);
    expect(await screen.findByText("This receipt is cancelled")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Cancel receipt|Post receipt|Register receipt/ })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    unmount();

    server.handler = () => json(404, { code: "RCV-NF-001", detail: "receipt not found" });
    renderAt(`/receiving/receipts/${ID}`);
    expect(await screen.findByRole("alert")).toHaveTextContent("receipt not found");
  });

  it("sends an optional posting date with a correction and with a cancellation", async () => {
    const posted = receipt({ state: "POSTED", version: 3, lines: [line({ amount: "25.00" })] });
    serve(posted, () => json(200, { ...posted, version: 4 }));
    renderAt(`/receiving/receipts/${ID}`);
    await screen.findByRole("form", { name: "Correct receipt" });

    fireEvent.change(screen.getByLabelText("Line 1 quantity change"), { target: { value: "-1" } });
    fireEvent.change(screen.getByLabelText("Posting date", { selector: "#cor-date" }), { target: { value: "2026-10-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Post correction" }));
    expect(await screen.findByText("Use 2026-10-25 or later.")).toBeInTheDocument();
    expect(commandCalls()).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Posting date", { selector: "#cor-date" }), { target: { value: "2026-11-02" } });
    fireEvent.click(screen.getByRole("button", { name: "Post correction" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm correction" }));
    await waitFor(() => expect(commandCalls()).toHaveLength(1));
    expect(commandCalls()[0].body).toEqual({ lines: [{ lineNo: 1, qtyDelta: "-1" }], postingDate: "2026-11-02" });

    fireEvent.change(screen.getByLabelText("Posting date", { selector: "#cancel-date" }), { target: { value: "2026-11-05" } });
    fireEvent.click(screen.getByRole("button", { name: "Cancel receipt" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm cancel" }));
    await waitFor(() => expect(commandCalls()).toHaveLength(2));
    expect(commandCalls()[1].url.pathname).toBe(`/api/v1/receiving/receipts/${ID}:cancel`);
    expect(commandCalls()[1].body).toEqual({ postingDate: "2026-11-05" });
  });

  it("checks a posting date against the receipt date", () => {
    const r = receipt();
    expect(validatePostingDate(r, "")).toBeUndefined();
    expect(validatePostingDate(r, "2026-10-25")).toBeUndefined();
    expect(validatePostingDate(r, "2026-10-24")).toBe("Use 2026-10-25 or later.");
    expect(validatePostingDate(r, "25/10/2026")).toBe("Enter a date.");
  });
});
