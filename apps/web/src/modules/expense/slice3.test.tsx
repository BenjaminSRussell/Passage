import { findAlert } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { CardMatchPage, emptyCardMatch, parseMatchItems, validateCardMatch } from "./CardMatchPage";
import { emptyLodging, LodgingCheckPage, toLodgingInput, validateLodging } from "./LodgingCheckPage";
import { emptyMileage, MileagePage, validateMileage } from "./MileagePage";
import { ExpenseHome } from "./pages";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

type Call = [string, RequestInit];

function stubFetch(status: number, body: unknown) {
  const fetchMock = vi.fn(async () => new Response(JSON.stringify(body), { status }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function sentBody(fetchMock: ReturnType<typeof vi.fn>, i = 0) {
  return JSON.parse(String((fetchMock.mock.calls[i] as unknown as Call)[1].body));
}

function type(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

async function loadedTable(name: string) {
  return waitFor(() => {
    const table = screen.getByRole("table", { name });
    expect(table).not.toHaveAttribute("aria-busy", "true");
    return table;
  });
}

afterEach(() => vi.unstubAllGlobals());

describe("line check with the travel requisition gate", () => {
  it("sends the categories upper-cased with the requisition flag and shows the EXP-TR-002 block", async () => {
    const fetchMock = stubFetch(200, {
      submittable: false,
      results: [{ lineId: "draft-1", ruleId: "EXP-TR-002", ruleVersion: 1, outcome: "BLOCK", code: "EXP-TR-002", message: "Requisition required for category AIRFARE", satisfied: false }],
      totals: { total: "20.00", reimbursable: "20.00", companyPaid: "0.00" },
    });
    render(<ExpenseHome />);
    fireEvent.change(screen.getByLabelText("Category"), { target: { value: "AIRFARE" } });
    type("Amount", "20.00");
    type("Categories that need a travel requisition", "airfare, lodging");
    fireEvent.click(screen.getByRole("button", { name: "Check line" }));
    const table = await loadedTable("Policy results");
    const row = within(table).getAllByText("EXP-TR-002")[0].closest("tr") as HTMLElement;
    expect(within(row).getByText("Blocked")).toBeInTheDocument();
    expect(within(row).getByText("Requisition required for category AIRFARE")).toBeInTheDocument();
    expect(sentBody(fetchMock)).toMatchObject({ requisitionCategories: ["AIRFARE", "LODGING"], hasRequisition: false, lines: [{ category: "AIRFARE" }] });
  });

  it("sends hasRequisition when the report has one", async () => {
    const fetchMock = stubFetch(200, { submittable: true, results: [], totals: { total: "20.00", reimbursable: "20.00", companyPaid: "0.00" } });
    render(<ExpenseHome />);
    fireEvent.click(screen.getByLabelText("Report has an approved travel requisition"));
    fireEvent.click(screen.getByRole("button", { name: "Check line" }));
    expect(await screen.findByText("Ready to submit")).toBeInTheDocument();
    expect(sentBody(fetchMock)).toMatchObject({ requisitionCategories: [], hasRequisition: true });
  });
});

describe("lodging cap page", () => {
  it("validates the stay and leaves a blank justification out", () => {
    expect(Object.keys(validateLodging({ ...emptyLodging, amount: "x", nights: "0", lodgingRate: "" })).sort()).toEqual(["amount", "lodgingRate", "nights"]);
    expect(toLodgingInput({ amount: " 450 ", nights: "2", lodgingRate: "150", justification: "  " })).toEqual({ amount: "450", nights: 2, lodgingRate: "150" });
  });

  it("starts empty, then flags a stay over the cap and shows the open result", async () => {
    const fetchMock = stubFetch(200, {
      capPerNight: "225.00",
      cap: "450.00",
      overCap: true,
      satisfied: false,
      result: { lineId: "", ruleId: "EXP-LODG-150", ruleVersion: 1, outcome: "REQUIRE_JUSTIFICATION", code: "EXP-POL-001", message: "Lodging above 1.5 times the per-diem lodging rate needs a justification", satisfied: false },
    });
    const { container } = render(<LodgingCheckPage />);
    expect(screen.getByRole("heading", { name: "No stay checked yet" })).toBeInTheDocument();
    type("Cost of the stay", "500.00");
    type("Nights", "2");
    type("Per-diem lodging rate per night", "150.00");
    fireEvent.click(screen.getByRole("button", { name: "Check stay" }));
    const table = await loadedTable("Policy results");
    const row = within(table).getByText("EXP-LODG-150").closest("tr") as HTMLElement;
    expect(within(row).getByText("Open")).toBeInTheDocument();
    expect(within(row).getByText("EXP-POL-001")).toBeInTheDocument();
    expect(screen.getByText("Over the cap: add a justification")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Lodging cap" })).toHaveTextContent("Cap for the stay 450.00");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/expense/lodging-checks", expect.objectContaining({ method: "POST" }));
    expect(sentBody(fetchMock)).toEqual({ amount: "500.00", nights: 2, lodgingRate: "150.00" });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty result table for a stay within the cap", async () => {
    stubFetch(200, { capPerNight: "225.00", cap: "225.00", overCap: false, satisfied: true });
    render(<LodgingCheckPage />);
    type("Cost of the stay", "200");
    type("Per-diem lodging rate per night", "150");
    fireEvent.click(screen.getByRole("button", { name: "Check stay" }));
    expect(await screen.findByText("Within the lodging cap")).toBeInTheDocument();
    expect(screen.getByText("No lodging rule applies to this stay")).toBeInTheDocument();
  });

  it("puts a service field error next to its input and does not call the service while invalid", async () => {
    const fetchMock = stubFetch(400, { code: "COM-0001", detail: "request failed validation", errors: { nights: "must be 1 to 366" } });
    render(<LodgingCheckPage />);
    fireEvent.click(screen.getByRole("button", { name: "Check stay" }));
    expect(fetchMock).not.toHaveBeenCalled();
    type("Cost of the stay", "200");
    type("Per-diem lodging rate per night", "150");
    fireEvent.click(screen.getByRole("button", { name: "Check stay" }));
    expect(await findAlert()).toHaveTextContent("must be 1 to 366");
    expect(screen.getByLabelText("Nights")).toHaveAttribute("aria-invalid", "true");
  });
});

describe("mileage page", () => {
  it("validates distance and rate", () => {
    expect(Object.keys(validateMileage({ ...emptyMileage, distance: "-1", rate: "abc" })).sort()).toEqual(["distance", "rate"]);
    expect(validateMileage({ ...emptyMileage, distance: "100", rate: "0.70" })).toEqual({});
  });

  it("starts empty, then shows the amount from the service", async () => {
    const fetchMock = stubFetch(200, { amount: "70.00" });
    const { container } = render(<MileagePage />);
    expect(screen.getByRole("heading", { name: "No mileage worked out yet" })).toBeInTheDocument();
    type("Distance", "100");
    type("Rate per unit", "0.70");
    fireEvent.click(screen.getByRole("button", { name: "Compute mileage" }));
    const stats = await screen.findByRole("list", { name: "Mileage claim" });
    expect(stats).toHaveTextContent("Amount 70.00");
    expect(stats).toHaveTextContent("100 miles");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/expense/mileage-computations", expect.objectContaining({ method: "POST" }));
    expect(sentBody(fetchMock)).toEqual({ distance: "100", rate: "0.70" });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a network failure as an alert", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new TypeError("offline"))));
    render(<MileagePage />);
    type("Distance", "100");
    type("Rate per unit", "0.70");
    fireEvent.click(screen.getByRole("button", { name: "Compute mileage" }));
    expect(await findAlert()).toHaveTextContent("NETWORK");
    expect(screen.queryByRole("list", { name: "Mileage claim" })).not.toBeInTheDocument();
  });
});

describe("card matching page", () => {
  const cards = "c1, w1, 2026-04-01, 100.00, Hotel, Downtown\nc2, w2, 2026-04-01, 12.00, Cafe";
  const receipts = "r1, w1, 2026-04-02, 99.50, Hotel, Downtown";

  it("reads one item per line with the merchant as the rest of the line", () => {
    expect(parseMatchItems(cards, "card transaction").items).toEqual([
      { id: "c1", workerId: "w1", date: "2026-04-01", amount: "100.00", merchant: "Hotel, Downtown" },
      { id: "c2", workerId: "w2", date: "2026-04-01", amount: "12.00", merchant: "Cafe" },
    ]);
    expect(parseMatchItems("c1, w1, 2026-04-01, 1, A\nc1, w1, 2026-04-01, 1, A", "card transaction").error).toMatch(/^Line 2: the card transaction id must be unique/);
    expect(parseMatchItems("c1, w1, 04/01/2026, 1, A", "receipt").error).toMatch(/^Line 1: enter the date/);
    expect(parseMatchItems("c1, w1, 2026-04-01", "receipt").error).toMatch(/^Line 1: enter id, worker/);
  });

  it("needs at least one card and a tolerance within range", () => {
    const { errors, input } = validateCardMatch({ ...emptyCardMatch, tolerancePct: "11", toleranceDays: "x" });
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["cards", "toleranceDays", "tolerancePct"]);
  });

  it("starts empty, then lists matches, unmatched items and the rate", async () => {
    const fetchMock = stubFetch(200, { matches: [{ cardId: "c1", receiptId: "r1" }], unmatchedCards: ["c2"], unmatchedReceipts: [], autoMatchRate: "0.5000" });
    const { container } = render(<CardMatchPage />);
    expect(screen.getByRole("heading", { name: "Nothing matched yet" })).toBeInTheDocument();
    type("Card transactions", cards);
    type("Receipts", receipts);
    fireEvent.click(screen.getByRole("button", { name: "Match" }));
    const matches = await loadedTable("Matches");
    expect(within(matches).getByText("c1")).toBeInTheDocument();
    expect(within(matches).getByText("r1")).toBeInTheDocument();
    const unmatched = screen.getByRole("table", { name: "Unmatched" });
    expect(within(unmatched).getByText("c2")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Match summary" })).toHaveTextContent("Auto-match rate 50.00%");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/expense/card-matches", expect.objectContaining({ method: "POST" }));
    expect(sentBody(fetchMock)).toMatchObject({ tolerancePct: "1", toleranceDays: 2, cards: [{ id: "c1", merchant: "Hotel, Downtown" }, { id: "c2" }], receipts: [{ id: "r1" }] });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty matches table when nothing pairs", async () => {
    stubFetch(200, { matches: [], unmatchedCards: ["c1", "c2"], unmatchedReceipts: [], autoMatchRate: "0.0000" });
    render(<CardMatchPage />);
    type("Card transactions", cards);
    fireEvent.click(screen.getByRole("button", { name: "Match" }));
    expect(await screen.findByText("No transaction matched a receipt")).toBeInTheDocument();
  });

  it("shows a line error without calling the service, and a service problem as an alert", async () => {
    const fetchMock = stubFetch(500, { code: "COM-0500", detail: "internal error" });
    render(<CardMatchPage />);
    type("Card transactions", "c1, w1, tomorrow, 1, A");
    fireEvent.click(screen.getByRole("button", { name: "Match" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Line 1: enter the date as YYYY-MM-DD.");
    expect(fetchMock).not.toHaveBeenCalled();
    type("Card transactions", cards);
    fireEvent.click(screen.getByRole("button", { name: "Match" }));
    expect(await findAlert()).toHaveTextContent("internal error");
  });
});
