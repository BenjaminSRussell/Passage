import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Reservation } from "./api/client";
import { canClose, canConsume, validateReservation } from "./logic";
import { CreateReservationPage, ReservationDetailPage, ReservationsPage } from "./ReservationsPage";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const R1 = "22222222-2222-4222-8222-222222222222";
const RES = "77777777-7777-4777-8777-777777777777";
const L1 = "55555555-5555-4555-8555-555555555555";
const PO = "44444444-4444-4444-8444-444444444444";
const POL = "88888888-8888-4888-8888-888888888888";
const ENC = "33333333-3333-4333-8333-333333333333";

const reservation: Reservation = {
  id: RES,
  legalEntityId: "11111111-1111-4111-8111-111111111111",
  ruleVersionId: R1,
  type: "FundsReservation",
  number: "FR-000001",
  description: "Trade show booth",
  expiryDate: "2026-12-31",
  status: "Open",
  total: "20000",
  consumed: "0",
  released: "0",
  open: "20000",
  version: 1,
  createdAt: "2026-10-01T08:00:00Z",
  lines: [{ lineId: L1, dimensionKey: "dept-10", spanKey: "FY2026", amount: "20000", consumed: "0", released: "0", open: "20000" }],
  consumptions: [],
};

type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status });

function stubRoutes(routes: Record<string, Handler>) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    const key = Object.keys(routes)
      .filter((k) => {
        const [m, prefix] = k.split(" ");
        return m === method && url.startsWith(prefix);
      })
      .sort((a, b) => b.length - a.length)[0];
    if (!key) return json(500, { title: "unrouted", code: "TEST-0000", detail: `${method} ${url}` });
    return routes[key](url, init);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function calls(fetchMock: ReturnType<typeof vi.fn>, method: string, fragment: string) {
  return (fetchMock.mock.calls as unknown as [string, RequestInit][]).filter(([url, init]) => (init?.method ?? "GET") === method && url.includes(fragment));
}

function at(path: string) {
  return render(
    createElement(
      MemoryRouter,
      { initialEntries: [path] },
      createElement(
        Routes,
        null,
        createElement(Route, { path: "/budgeting/reservations", element: createElement(ReservationsPage) }),
        createElement(Route, { path: "/budgeting/reservations/new", element: createElement(CreateReservationPage) }),
        createElement(Route, { path: "/budgeting/reservations/:id", element: createElement(ReservationDetailPage) }),
      ),
    ),
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("reservation rules", () => {
  it("allows consumption only on open funds reservations and precommitments (BR-38.14)", () => {
    expect(canConsume("FundsReservation", "PartiallyConsumed")).toBe(true);
    expect(canConsume("Precommitment", "Open")).toBe(true);
    expect(canConsume("FundsBlocking", "Open")).toBe(false);
    expect(canConsume("RevenueForecast", "Open")).toBe(false);
    expect(canConsume("FundsReservation", "Expired")).toBe(false);
  });

  it("closes funds blocking from Open only and never reopens a final status", () => {
    expect(canClose("FundsBlocking", "Open")).toBe(true);
    expect(canClose("FundsReservation", "Consumed")).toBe(true);
    expect(canClose("FundsBlocking", "Closed")).toBe(false);
    expect(canClose("Precommitment", "Expired")).toBe(false);
  });

  it("validates a document like the service", () => {
    const lines = [{ dimensionKey: "dept-10", spanKey: "FY2026", amount: "0" }];
    const errors = validateReservation({ ruleVersionId: "x", type: "FundsBlocking", description: "", expiryDate: "2026-12-31" }, lines);
    expect(errors).toEqual([
      "Enter the budget control rule version id (a UUID).",
      "Funds blocking does not expire; leave the expiry date empty.",
      "Line 1: amount must be a positive decimal with at most 4 places.",
    ]);
    expect(validateReservation({ ruleVersionId: R1, type: "FundsReservation", description: "", expiryDate: "" }, [{ ...lines[0], amount: "10.5" }])).toEqual([]);
  });
});

describe("ReservationsPage", () => {
  it("lists documents, filters by status and rule, and opens one", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/budget/reservations?": () => json(200, { items: [reservation] }),
      [`GET /api/v1/budget/reservations/${RES}`]: () => json(200, reservation),
    });
    const { container } = at(`/budgeting/reservations?rule=${R1}`);
    const table = await findTable({ name: "Earmarked funds" });
    expect(await within(table).findByText("Trade show booth")).toBeInTheDocument();
    expect(within(table).getAllByText("20000.00")).toHaveLength(2);
    expect(calls(fetchMock, "GET", `reservations?rule=${R1}`)).toHaveLength(1);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "PartiallyConsumed" } });
    await waitFor(() => expect(calls(fetchMock, "GET", "status=PartiallyConsumed")).toHaveLength(1));
    fireEvent.click(within(table).getByRole("button", { name: "FR-000001" }));
    expect(await screen.findByRole("heading", { name: "Funds reservation FR-000001" })).toBeInTheDocument();
  });

  it("shows the empty state, and an error with a retry", async () => {
    let fail = true;
    stubRoutes({
      "GET /api/v1/budget/reservations": () => (fail ? json(503, { title: "down", code: "COM-0500", detail: "internal error" }) : json(200, { items: [] })),
    });
    at("/budgeting/reservations");
    expect(await findAlert()).toHaveTextContent("Something went wrong on our side");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No reservation documents")).toBeInTheDocument();
  });
});

describe("CreateReservationPage", () => {
  it("validates, posts with an Idempotency-Key and opens the document", async () => {
    const fetchMock = stubRoutes({
      "POST /api/v1/budget/reservations": () => json(201, reservation),
      [`GET /api/v1/budget/reservations/${RES}`]: () => json(200, reservation),
    });
    const { container } = at(`/budgeting/reservations/new?rule=${R1}`);
    expect(screen.getByLabelText("Rule version")).toHaveValue(R1);
    fireEvent.click(screen.getByRole("button", { name: "Create reservation" }));
    expect(await screen.findByText("Line 1: dimension is required.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", "/reservations")).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Expiry date"), { target: { value: "2026-12-31" } });
    fireEvent.change(document.getElementById("reservation-dim-0")!, { target: { value: "dept-10" } });
    fireEvent.change(document.getElementById("reservation-span-0")!, { target: { value: "FY2026" } });
    fireEvent.change(document.getElementById("reservation-amount-0")!, { target: { value: "20000.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Create reservation" }));
    expect(await screen.findByRole("heading", { name: "Funds reservation FR-000001" })).toBeInTheDocument();
    const [, init] = calls(fetchMock, "POST", "/reservations")[0];
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(JSON.parse(init.body as string)).toEqual({
      ruleVersionId: R1,
      type: "FundsReservation",
      description: "",
      expiryDate: "2026-12-31",
      lines: [{ dimensionKey: "dept-10", spanKey: "FY2026", amount: "20000.00" }],
    });
  });

  it("clears and disables the expiry for funds blocking, and shows a failed check", async () => {
    stubRoutes({
      "POST /api/v1/budget/reservations": () => json(422, { title: "Unprocessable", code: "CO-0601", detail: "Budget check failed on dept-10 FY2026: available 100.00, requested 500.00" }),
    });
    at(`/budgeting/reservations/new?rule=${R1}`);
    fireEvent.change(screen.getByLabelText("Expiry date"), { target: { value: "2026-12-31" } });
    fireEvent.change(screen.getByLabelText("Type"), { target: { value: "FundsBlocking" } });
    expect(screen.getByLabelText("Expiry date")).toBeDisabled();
    expect(screen.getByLabelText("Expiry date")).toHaveValue("");
    fireEvent.change(document.getElementById("reservation-dim-0")!, { target: { value: "dept-10" } });
    fireEvent.change(document.getElementById("reservation-span-0")!, { target: { value: "FY2026" } });
    fireEvent.change(document.getElementById("reservation-amount-0")!, { target: { value: "500" } });
    fireEvent.click(screen.getByRole("button", { name: "Create reservation" }));
    expect(await screen.findByText(/Budget check failed on dept-10/)).toBeInTheDocument();
  });
});

describe("ReservationDetailPage", () => {
  it("consumes by a purchase order (AC-38.12) and reloads the document", async () => {
    let current = reservation;
    const fetchMock = stubRoutes({
      [`GET /api/v1/budget/reservations/${RES}`]: () => json(200, current),
      [`POST /api/v1/budget/reservations/${RES}:consume`]: () => {
        current = {
          ...reservation,
          status: "PartiallyConsumed",
          consumed: "8000",
          open: "12000",
          lines: [{ ...reservation.lines[0], consumed: "8000", open: "12000" }],
          consumptions: [{ id: "c1", lineId: L1, encumbranceId: ENC, documentType: "PurchaseOrder", documentId: PO, documentLineId: POL, amount: "8000", consumedAt: "2026-10-02T08:00:00Z" }],
        };
        return json(200, { reservationId: RES, lineId: L1, documentId: PO, encumbranceId: ENC, consumed: "8000", lineOpen: "12000", open: "12000", status: "PartiallyConsumed" });
      },
    });
    const { container } = at(`/budgeting/reservations/${RES}`);
    expect(await screen.findByText("Nothing consumed yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Document id"), { target: { value: PO } });
    fireEvent.change(screen.getByLabelText("Document line id"), { target: { value: POL } });
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "20000.01" } });
    fireEvent.click(screen.getByRole("button", { name: "Consume" }));
    expect(await screen.findByText("At most 20000.00 remains on this line.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", ":consume")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "8000" } });
    fireEvent.click(screen.getByRole("button", { name: "Consume" }));
    expect(await screen.findByText("Consumed 8000.00; 12000.00 remains on the reservation.")).toBeInTheDocument();
    expect(JSON.parse(calls(fetchMock, "POST", ":consume")[0][1].body as string)).toEqual({
      lineId: L1,
      documentType: "PurchaseOrder",
      documentId: PO,
      documentLineId: POL,
      amount: "8000",
    });
    const history = await findTable({ name: "Consumption history" });
    expect(await within(history).findByRole("link", { name: "Open encumbrance" })).toHaveAttribute("href", `/budgeting/encumbrances/${ENC}`);
  });

  it("closes a funds blocking document and offers no consumption", async () => {
    const blocking: Reservation = { ...reservation, type: "FundsBlocking", number: "FB-000001", expiryDate: undefined };
    const fetchMock = stubRoutes({
      [`GET /api/v1/budget/reservations/${RES}`]: () => json(200, blocking),
      [`POST /api/v1/budget/reservations/${RES}:close`]: () => json(200, { reservationId: RES, number: "FB-000001", released: "20000", status: "Closed" }),
    });
    at(`/budgeting/reservations/${RES}`);
    expect(await screen.findByText(/cannot be consumed/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Consume" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Close and release the remainder" }));
    expect(await screen.findByText("Closed FB-000001; 20000.00 released to available funds.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", ":close")).toHaveLength(1);
  });

  it("shows the problem when the document cannot be loaded", async () => {
    stubRoutes({ [`GET /api/v1/budget/reservations/${RES}`]: () => json(404, { title: "Not Found", code: "BUD-0005", detail: "reservation not found" }) });
    at(`/budgeting/reservations/${RES}`);
    expect(await findAlert()).toHaveTextContent("reservation not found");
  });
});
