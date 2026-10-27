import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { AdvanceAgingPage, emptyAging, toAgingInput, validateAging } from "./AdvanceAgingPage";
import { CardFeedPage } from "./CardFeedPage";
import { daysBetween, splitProblem } from "./format";
import { emptyPerDiem, PerDiemPage, toPerDiemInput, validatePerDiem } from "./PerDiemPage";
import { RequisitionCheckPage, toRequisitionInput, validateRequisition, emptyRequisition } from "./RequisitionCheckPage";

/** Rules the shell (not a page rendered alone) satisfies. */
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

/** The named table once the request has finished (the loading skeleton shares its name). */
async function loadedTable(name: string) {
  return waitFor(() => {
    const table = screen.getByRole("table", { name });
    expect(table).not.toHaveAttribute("aria-busy", "true");
    return table;
  });
}

afterEach(() => vi.unstubAllGlobals());

describe("format helpers", () => {
  it("lists every day of a trip and refuses inverted or over-long ranges", () => {
    expect(daysBetween("2026-02-27", "2026-03-02")).toEqual(["2026-02-27", "2026-02-28", "2026-03-01", "2026-03-02"]);
    expect(daysBetween("2026-03-02", "2026-03-01")).toEqual([]);
    expect(daysBetween("2026-01-01", "2026-12-31")).toEqual([]);
  });

  it("keeps known field errors on the form and turns the rest into one failure", () => {
    const split = splitProblem({ code: "COM-0001", message: "request failed validation", fields: { "rate.currency": "bad", days: "must be consecutive" } }, ["rate.currency"]);
    expect(split.fields).toEqual({ "rate.currency": "bad" });
    expect(split.failure).toEqual({ code: "COM-0001", message: "days must be consecutive" });
    expect(splitProblem({ code: "COM-0500", message: "internal error", fields: {} }, []).failure?.message).toBe("internal error");
  });
});

describe("per diem page", () => {
  const result = {
    currency: "USD",
    lines: [
      { date: "2026-03-01", partial: true, lodging: "150.00", mie: "60.00", total: "210.00" },
      { date: "2026-03-02", partial: false, lodging: "150.00", mie: "64.00", total: "214.00" },
      { date: "2026-03-03", partial: true, lodging: "0.00", mie: "60.00", total: "60.00" },
    ],
    totals: { lodging: "300.00", mie: "184.00", total: "484.00" },
  };

  it("validates the rate and the trip before sending", () => {
    const errors = validatePerDiem({ ...emptyPerDiem, currency: "usd", from: "2026-03-03", to: "2026-03-01" });
    expect(Object.keys(errors).sort()).toEqual(["currency", "lodging", "mealsIncidentals", "to"]);
  });

  it("sends one consecutive day per travel date, a charged night on all but the last", () => {
    const input = toPerDiemInput({ ...emptyPerDiem, lodging: "150", mealsIncidentals: "80", from: "2026-03-01", to: "2026-03-03" });
    expect(input.days.map((d) => [d.date, d.lodging])).toEqual([
      ["2026-03-01", true],
      ["2026-03-02", true],
      ["2026-03-03", false],
    ]);
  });

  it("starts empty, then shows totals and the day table from the service", async () => {
    const fetchMock = stubFetch(200, result);
    const { container } = render(<PerDiemPage />);
    expect(screen.getByRole("heading", { name: "No per diem worked out yet" })).toBeInTheDocument();
    type("Lodging per night", "150.00");
    type("M&IE per day", "80.00");
    type("First day of travel", "2026-03-01");
    type("Last day of travel", "2026-03-03");
    const day2 = screen.getByRole("group", { name: "2026-03-02" });
    fireEvent.click(within(day2).getByLabelText("Breakfast provided"));
    fireEvent.click(screen.getByRole("button", { name: "Compute per diem" }));

    const table = await loadedTable("Per diem by day");
    expect(within(table).getAllByText("First or last day (75%)")).toHaveLength(2);
    expect(within(table).getByText("64.00 USD")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Per diem totals" })).toHaveTextContent("Total 484.00 USD");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/expense/per-diem-computations", expect.objectContaining({ method: "POST" }));
    const body = sentBody(fetchMock);
    expect(body.rate).toEqual({ currency: "USD", lodging: "150.00", mealsIncidentals: "80.00" });
    expect(body.days[1]).toEqual({ date: "2026-03-02", lodging: true, breakfastProvided: true, lunchProvided: false, dinnerProvided: false });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows field errors from the service next to the input and others as an alert", async () => {
    stubFetch(400, { code: "COM-0001", detail: "request failed validation", errors: { "rate.currency": "must be a three-letter upper-case currency code", days: "must be consecutive calendar days in date order, each once" } });
    render(<PerDiemPage />);
    type("Lodging per night", "150");
    type("M&IE per day", "80");
    type("First day of travel", "2026-03-01");
    type("Last day of travel", "2026-03-01");
    fireEvent.click(screen.getByRole("button", { name: "Compute per diem" }));
    const alerts = await screen.findAllByRole("alert");
    expect(alerts.map((a) => a.textContent).join(" ")).toContain("must be a three-letter upper-case currency code");
    expect(alerts.map((a) => a.textContent).join(" ")).toContain("days must be consecutive calendar days");
    expect(screen.queryByRole("table", { name: "Per diem by day" })).not.toBeInTheDocument();
  });

  it("does not call the service while the form is invalid", () => {
    const fetchMock = stubFetch(200, result);
    render(<PerDiemPage />);
    fireEvent.click(screen.getByRole("button", { name: "Compute per diem" }));
    expect(fetchMock).not.toHaveBeenCalled();
    expect(screen.getAllByRole("alert").length).toBeGreaterThan(0);
  });
});

describe("cash advance aging page", () => {
  it("validates unique ids and amounts and leaves empty cleared amounts out", () => {
    const v = emptyAging("2026-02-01");
    v.advances = [
      { id: "ADV-1", tripEnd: "2026-01-01", paid: "1000", settled: "820", returned: "", recovered: "", writtenOff: "" },
      { id: "ADV-1", tripEnd: "", paid: "x", settled: "", returned: "", recovered: "", writtenOff: "y" },
    ];
    expect(Object.keys(validateAging(v)).sort()).toEqual(["advances[1].id", "advances[1].paid", "advances[1].tripEnd", "advances[1].writtenOff"]);
    expect(toAgingInput({ ...v, advances: [v.advances[0]] }).advances[0]).toEqual({ id: "ADV-1", tripEnd: "2026-01-01", paid: "1000", settled: "820" });
  });

  it("warns that new advances are blocked and lists the overdue ones", async () => {
    const fetchMock = stubFetch(200, { newAdvancesBlocked: true, code: "EXP-ADV-002", overdue: [{ id: "ADV-1", dueDate: "2026-01-31", daysPastDue: 1, open: "180.00" }] });
    const { container } = render(<AdvanceAgingPage />);
    expect(screen.getByRole("heading", { name: "No advances checked yet" })).toBeInTheDocument();
    type("Check on", "2026-02-01");
    type("Advance", "ADV-1");
    type("Trip end", "2026-01-01");
    type("Paid", "1000");
    type("Settled by reports", "820");
    fireEvent.click(screen.getByRole("button", { name: "Check aging" }));
    const table = await loadedTable("Overdue advances");
    expect(within(table).getByText("180.00")).toBeInTheDocument();
    expect(screen.getByText("New advances are blocked")).toBeInTheDocument();
    expect(screen.getByText(/EXP-ADV-002/)).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Aging summary" })).toHaveTextContent("Past due 1");
    expect(sentBody(fetchMock)).toEqual({ asOf: "2026-02-01", limitDays: 30, advances: [{ id: "ADV-1", tripEnd: "2026-01-01", paid: "1000", settled: "820" }] });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty overdue table when nothing is past due", async () => {
    stubFetch(200, { newAdvancesBlocked: false, overdue: [] });
    render(<AdvanceAgingPage />);
    type("Advance", "ADV-9");
    type("Trip end", "2026-01-01");
    type("Paid", "100");
    fireEvent.click(screen.getByRole("button", { name: "Add advance" }));
    expect(screen.getByRole("group", { name: "Advance 2" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove advance 2" }));
    fireEvent.click(screen.getByRole("button", { name: "Check aging" }));
    expect(await screen.findByText("No overdue advances")).toBeInTheDocument();
    expect(screen.getByText("New advances are allowed")).toBeInTheDocument();
  });

  it("puts a service field error on the advance it names", async () => {
    stubFetch(400, { code: "COM-0001", detail: "request failed validation", errors: { "advances[0].paid": "settled, returned, recovered and written off together must not exceed paid" } });
    render(<AdvanceAgingPage />);
    type("Advance", "ADV-1");
    type("Trip end", "2026-01-01");
    type("Paid", "100");
    type("Returned", "200");
    fireEvent.click(screen.getByRole("button", { name: "Check aging" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("must not exceed paid");
    expect(screen.getByLabelText("Paid")).toHaveAttribute("aria-invalid", "true");
  });
});

describe("travel requisition check page", () => {
  it("validates the window and the dates and leaves an empty last travel date out", () => {
    const v = { ...emptyRequisition("2026-05-20"), from: "2026-03-14", to: "2026-03-10", travelDates: "2026-03-11, tomorrow" };
    expect(Object.keys(validateRequisition(v)).sort()).toEqual(["travelDates", "trip.to"]);
    const input = toRequisitionInput({ ...v, to: "2026-03-20", travelDates: "2026-03-11\n2026-03-12" });
    expect(input).toEqual({ trip: { from: "2026-03-14", to: "2026-03-20" }, travelDates: ["2026-03-11", "2026-03-12"], asOf: "2026-05-20", reportPosted: false });
  });

  it("flags report dates outside the trip and says when the requisition closes", async () => {
    const fetchMock = stubFetch(200, { outOfRangeDates: ["2026-03-09", "2026-03-15"], closeDate: "2026-05-13", closeDue: false });
    const { container } = render(<RequisitionCheckPage />);
    expect(screen.getByRole("heading", { name: "No requisition checked yet" })).toBeInTheDocument();
    type("Approved from", "2026-03-10");
    type("Approved to", "2026-03-14");
    type("Dates on the report", "2026-03-09, 2026-03-11, 2026-03-15");
    type("Check on", "2026-04-01");
    fireEvent.click(screen.getByRole("button", { name: "Check requisition" }));
    const table = await loadedTable("Dates outside the approved trip");
    expect(within(table).getByText("2026-03-09")).toBeInTheDocument();
    expect(within(table).getAllByText("Warning")).toHaveLength(2);
    expect(screen.getByText("It closes on 2026-05-13 unless its linked report posts first.")).toBeInTheDocument();
    expect(sentBody(fetchMock).travelDates).toEqual(["2026-03-09", "2026-03-11", "2026-03-15"]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("says a posted report closes the requisition and shows the empty date table", async () => {
    stubFetch(200, { outOfRangeDates: [], closeDate: "2026-05-13", closeDue: true });
    render(<RequisitionCheckPage />);
    type("Approved from", "2026-03-10");
    type("Approved to", "2026-03-14");
    fireEvent.click(screen.getByLabelText("Linked report is posted"));
    fireEvent.click(screen.getByRole("button", { name: "Check requisition" }));
    expect(await screen.findByText("The requisition is due to close")).toBeInTheDocument();
    expect(screen.getByText("Its linked report is posted.")).toBeInTheDocument();
    expect(screen.getByText("Every date is inside the approved trip")).toBeInTheDocument();
  });

  it("shows a network failure as an alert", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new TypeError("offline"))));
    render(<RequisitionCheckPage />);
    type("Approved from", "2026-03-10");
    type("Approved to", "2026-03-14");
    fireEvent.click(screen.getByRole("button", { name: "Check requisition" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Check your connection and try again. (NETWORK)");
  });
});

describe("card feed preview page", () => {
  const feed = {
    format: "CSV",
    fileHash: "a".repeat(64),
    transactions: [{ line: 2, cardLast4: "4242", txnDate: "2026-03-02", postDate: "2026-03-03", merchant: "ACME AIR", mcc: "3000", amount: "412.50", currency: "USD" }],
    rejected: [{ line: 3, reason: "merchant is required and at most 200 characters" }],
  };

  it("requires content before sending", () => {
    const fetchMock = stubFetch(200, feed);
    render(<CardFeedPage />);
    fireEvent.click(screen.getByRole("button", { name: "Preview import" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Paste the card statement");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("shows accepted transactions, rejected rows and the file hash", async () => {
    const fetchMock = stubFetch(200, feed);
    const { container } = render(<CardFeedPage />);
    expect(screen.getByRole("heading", { name: "No statement previewed yet" })).toBeInTheDocument();
    type("Statement content", "card_last4,txn_date,post_date,merchant,amount,currency\n4242,2026-03-02,2026-03-03,ACME AIR,412.50,USD\n");
    fireEvent.click(screen.getByRole("button", { name: "Preview import" }));
    const txns = await loadedTable("Card transactions");
    expect(within(txns).getByText("ACME AIR")).toBeInTheDocument();
    expect(within(txns).getByText("412.50 USD")).toBeInTheDocument();
    const rejected = screen.getByRole("table", { name: "Rejected rows" });
    expect(within(rejected).getByText(/merchant is required/)).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Feed summary" })).toHaveTextContent("Rejected rows 1");
    expect(screen.getByText("a".repeat(64))).toBeInTheDocument();
    expect(sentBody(fetchMock).format).toBe("CSV");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/expense/card-transactions:parse", expect.objectContaining({ method: "POST" }));
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state when a file has no transactions", async () => {
    stubFetch(200, { ...feed, transactions: [], rejected: [] });
    render(<CardFeedPage />);
    fireEvent.change(screen.getByLabelText("Format"), { target: { value: "OFX" } });
    type("Statement content", "<OFX></OFX>");
    fireEvent.click(screen.getByRole("button", { name: "Preview import" }));
    expect(await screen.findByText("No transactions in this file")).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Rejected rows" })).not.toBeInTheDocument();
  });

  it("shows a refused file next to the content and a missing duty as an alert", async () => {
    stubFetch(400, { code: "COM-0001", detail: "request failed validation", errors: { content: "is not an OFX statement" } });
    const { unmount } = render(<CardFeedPage />);
    type("Statement content", "plain text");
    fireEvent.click(screen.getByRole("button", { name: "Preview import" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("is not an OFX statement");
    unmount();

    stubFetch(403, { code: "AUTH-0003", detail: "missing duty expense.card.import" });
    render(<CardFeedPage />);
    type("Statement content", "x");
    fireEvent.click(screen.getByRole("button", { name: "Preview import" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("missing duty expense.card.import (AUTH-0003)");
  });
});
