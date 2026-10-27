import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { fetchJournalLines, type JournalLine } from "./api/client";
import { formatDimensions, LINES_PAGE_SIZE, validateLineFilter } from "./LedgerLinesPage";
import { transactionsLink } from "./pages";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const ENTRY = "0190a000-0000-7000-8000-000000000003";

function line(n: number, patch: Partial<JournalLine> = {}): JournalLine {
  return {
    id: `0190a000-0000-7000-8000-0000000001${String(n).padStart(2, "0")}`,
    entryId: ENTRY,
    voucher: `GL-${String(n).padStart(6, "0")}`,
    postingSequence: n,
    postingDate: "2026-03-10",
    lineNumber: 1,
    account: "6010",
    displayName: "6010-D100",
    dimensions: { DEPT: "D100" },
    debitCredit: "D",
    amount: "125.00",
    currency: "USD",
    negativePosting: false,
    text: "Freight",
    sourceSystem: "ap",
    sourceType: "vendor_invoice",
    sourceId: `INV-${n}`,
    traceId: "4bf92f3577b34da6a3ce929d0e0e4736",
    ...patch,
  };
}

const empty = { ledgerId: "", accountFrom: "", accountTo: "", dateFrom: "", dateTo: "", voucher: "", dimensions: "" };

describe("ledger transaction filters", () => {
  it("requires a UUID ledger and well-formed, ordered ranges", () => {
    expect(validateLineFilter(empty).ledgerId).toMatch(/UUID/);
    expect(validateLineFilter({ ...empty, ledgerId: LEDGER })).toEqual({});
    expect(validateLineFilter({ ...empty, ledgerId: LEDGER, accountFrom: "60 10" }).accountFrom).toBeDefined();
    expect(validateLineFilter({ ...empty, ledgerId: LEDGER, dateFrom: "10/03/2026" }).dateFrom).toMatch(/YYYY-MM-DD/);
    expect(validateLineFilter({ ...empty, ledgerId: LEDGER, dateFrom: "2026-03-31", dateTo: "2026-03-01" }).dateTo).toMatch(/before/);
    expect(validateLineFilter({ ...empty, ledgerId: LEDGER, dimensions: "dept=D100" }).dimensions).toBeDefined();
    expect(validateLineFilter({ ...empty, ledgerId: LEDGER, dimensions: "DEPT=D100, DEPT=D200" }).dimensions).toBeDefined();
    expect(validateLineFilter({ ...empty, ledgerId: LEDGER, dimensions: "DEPT=D100, CC=C1" })).toEqual({});
  });

  it("formats dimensions sorted by attribute", () => {
    expect(formatDimensions({ DEPT: "D100", CC: "C1" })).toBe("CC=C1, DEPT=D100");
    expect(formatDimensions(undefined)).toBe("");
  });

  it("sends every filter as the API's query parameters", async () => {
    let seen: URL | undefined;
    const fetchImpl = async (input: string) => {
      seen = new URL(input, "http://localhost");
      return new Response(JSON.stringify({ items: [] }), { status: 200 });
    };
    await fetchJournalLines(
      { ledgerId: LEDGER, accountFrom: "6000", accountTo: "6999", dateFrom: "2026-03-01", dateTo: "2026-03-31", voucher: "GL-1", sourceSystem: "ap", dimensions: { DEPT: "D100" }, cursor: "7.2", limit: 50 },
      fetchImpl,
    );
    expect(seen?.pathname).toBe("/api/v1/ledger/journal-lines");
    expect(Object.fromEntries(seen?.searchParams ?? [])).toEqual({
      ledger: LEDGER,
      account_from: "6000",
      account_to: "6999",
      date_from: "2026-03-01",
      date_to: "2026-03-31",
      voucher: "GL-1",
      source_system: "ap",
      "dim.DEPT": "D100",
      cursor: "7.2",
      limit: "50",
    });
  });
});

describe("Ledger transactions page", () => {
  it("asks for a ledger and does not call the API with an invalid one", async () => {
    renderAt("/ledger/transactions");
    expect(screen.getByRole("heading", { name: "Ledger transactions" })).toBeInTheDocument();
    expect(screen.getByText("Enter a ledger id to list its posted lines.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: "not-a-uuid" } });
    fireEvent.click(screen.getByRole("button", { name: "Show lines" }));
    expect(await screen.findByText("Enter the ledger id as a UUID.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("searches with the filters, lists the lines and drills to the entry", async () => {
    server.handler = (url) =>
      url.pathname === "/api/v1/ledger/journal-lines" ? json(200, { items: [line(1), line(2, { debitCredit: "C", account: "2100", displayName: "2100", dimensions: undefined, negativePosting: true })] }) : undefined;
    renderAt("/ledger/transactions");
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.change(screen.getByLabelText(/Account from/), { target: { value: "2000" } });
    fireEvent.change(screen.getByLabelText(/Account to/), { target: { value: "6999" } });
    fireEvent.change(screen.getByLabelText(/Dimension values/), { target: { value: "DEPT=D100" } });
    fireEvent.click(screen.getByRole("button", { name: "Show lines" }));

    const table = await findTable({ name: "Ledger transactions" });
    await waitFor(() => expect(within(table).getByText("GL-000001")).toBeInTheDocument());
    const call = server.calls[0];
    expect(call.url.searchParams.get("ledger")).toBe(LEDGER);
    expect(call.url.searchParams.get("account_from")).toBe("2000");
    expect(call.url.searchParams.get("account_to")).toBe("6999");
    expect(call.url.searchParams.get("dim.DEPT")).toBe("D100");
    expect(call.url.searchParams.get("limit")).toBe(String(LINES_PAGE_SIZE));
    expect(within(table).getByText("Storno")).toBeInTheDocument();
    expect(within(table).getAllByText("125.00 USD")).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Load more lines" })).not.toBeInTheDocument();
    expect(pageViolations(document.body)).toEqual([]);

    fireEvent.click(within(table).getByText("GL-000001"));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/ledger/entries/${ENTRY}`));
  });

  it("reads filters from the address and pages with the keyset cursor", async () => {
    server.handler = (url) => {
      if (url.pathname !== "/api/v1/ledger/journal-lines") return undefined;
      return url.searchParams.get("cursor") === "1.1" ? json(200, { items: [line(2)] }) : json(200, { items: [line(1)], nextCursor: "1.1" });
    };
    renderAt(`/ledger/transactions?ledger=${LEDGER}&voucher=GL-000001`);
    const table = await findTable({ name: "Ledger transactions" });
    await waitFor(() => expect(within(table).getByText("GL-000001")).toBeInTheDocument());
    expect(server.calls[0].url.searchParams.get("voucher")).toBe("GL-000001");
    expect(screen.getByLabelText(/Voucher/)).toHaveValue("GL-000001");

    fireEvent.click(screen.getByRole("button", { name: "Load more lines" }));
    await waitFor(() => expect(within(table).getByText("GL-000002")).toBeInTheDocument());
    expect(server.calls[1].url.searchParams.get("cursor")).toBe("1.1");
    expect(within(table).getByText("GL-000001")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more lines" })).not.toBeInTheDocument();
  });

  it("shows the empty state when nothing matches", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt(`/ledger/transactions?ledger=${LEDGER}`);
    expect(await screen.findByText("No lines match")).toBeInTheDocument();
  });

  it("shows the API error with its field details and retries", async () => {
    let fail = true;
    server.handler = () =>
      fail ? json(404, { code: "LEDGER_NOT_FOUND", detail: "ledger not found" }) : json(200, { items: [line(1)] });
    renderAt(`/ledger/transactions?ledger=${LEDGER}`);
    const alert = await findAlert();
    expect(alert.textContent).toContain("Could not load the ledger lines");
    expect(alert.textContent).toContain("ledger not found");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("GL-000001")).toBeInTheDocument();
  });
});

describe("trial balance drill-down", () => {
  it("maps the account and periods to a transactions address", () => {
    expect(transactionsLink({ ledgerId: LEDGER, periodFrom: "2026-01", periodTo: "2026-02" }, "6010")).toBe(
      `/ledger/transactions?ledger=${LEDGER}&account_from=6010&account_to=6010&date_from=2026-01-01&date_to=2026-02-28`,
    );
    expect(transactionsLink({ ledgerId: LEDGER, periodTo: "2028-02" }, "1000")).toContain("date_to=2028-02-29");
    expect(transactionsLink({ ledgerId: LEDGER }, "1000")).not.toContain("date_");
  });

  it("opens the lines of a trial balance account", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/ledger/balances") {
        return json(200, {
          ledgerId: LEDGER,
          currency: "USD",
          rows: [{ account: "6010", name: "Freight", debit: "125.00", credit: "0.00", net: "125.00", lineCount: 1 }],
          debit: "125.00",
          credit: "125.00",
          net: "0.00",
          balanced: true,
        });
      }
      if (url.pathname === "/api/v1/ledger/journal-lines") return json(200, { items: [line(1)] });
      return undefined;
    };
    renderAt("/ledger/trial-balance");
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.click(screen.getByRole("button", { name: "Show trial balance" }));
    fireEvent.click(await screen.findByText("Freight"));
    expect(await screen.findByText("GL-000001")).toBeInTheDocument();
    const linesCall = server.calls.find((c) => c.url.pathname === "/api/v1/ledger/journal-lines");
    expect(linesCall?.url.searchParams.get("account_from")).toBe("6010");
    expect(linesCall?.url.searchParams.get("account_to")).toBe("6010");
  });
});
