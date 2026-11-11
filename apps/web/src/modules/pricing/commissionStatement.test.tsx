import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { CommissionStatement, CommissionTransaction } from "./api/client";
import { validateStatement } from "./CommissionStatementPage";
import { json, pageViolations, renderAt, server, text, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const SG = "0190f0a0-0000-7000-8000-000000000701";

function txn(patch: Partial<CommissionTransaction>): CommissionTransaction {
  return {
    id: "t-1",
    legalEntityId: LE,
    invoiceLineId: "INV-1/1",
    invoiceDate: "2027-03-10",
    periodId: "2027-03",
    customerId: "C1",
    customerGroup: "",
    itemGroup: "",
    salesGroupId: SG,
    repId: "R1",
    sharePct: "60",
    basis: "REVENUE",
    lineNet: "10000",
    basisAmount: "10000",
    ratePct: "5",
    amount: "300.00",
    currency: "USD",
    debitAccount: "6200",
    creditAccount: "2170",
    status: "POSTED",
    ...patch,
  };
}

const statement: CommissionStatement = {
  legalEntityId: LE,
  repId: "R1",
  period: "2027-03",
  lineCount: 2,
  linesTruncated: false,
  totals: [{ currency: "USD", lineCount: 2, earnedAmount: "300.00", creditedAmount: "-60.00", netAmount: "240.00" }],
  lines: [txn({}), txn({ id: "t-2", invoiceLineId: "CN-1/1", reversesInvoiceLineId: "INV-1/1", amount: "-60.00" })],
};

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

function ask(rep = "R1", month = "2027-03") {
  fill("Legal entity", LE);
  fill("Rep id", rep);
  fill("Invoice month", month);
  fireEvent.click(screen.getByRole("button", { name: "Show statement" }));
}

describe("Commission statement", () => {
  it("checks the form before calling the service", () => {
    expect(validateStatement({ legalEntityId: "x", repId: "", period: "2027-13" })).toEqual({
      legalEntityId: "Choose a legal entity.",
      repId: "Enter the rep id (up to 64 characters).",
      period: "Use a month such as 2027-03.",
    });
    expect(validateStatement({ legalEntityId: LE, repId: "R1", period: "2027-03" })).toEqual({});
    for (const repId of ["EU/R1", "worker:7", ".."]) {
      expect(validateStatement({ legalEntityId: LE, repId, period: "2027-03" }).repId).toBe("A rep id has no / or : and is not . or ..");
    }
  });

  it("shows the lines and the exact totals of the rep's month", async () => {
    server.handler = (url) => (url.pathname.endsWith("/commission/reps/R1/statement") ? json(200, statement) : undefined);
    const { container } = renderAt("/pricing/commission/statement");
    expect(screen.getByText(/Choose a rep and a month/)).toBeInTheDocument();
    ask();
    const table = await findTable({ name: "Statement lines" });
    await waitFor(() => expect(table).toHaveTextContent("CN-1/1 (credits INV-1/1)"));
    const totals = screen.getByRole("list", { name: "Statement totals" });
    expect(totals).toHaveTextContent("Earned USD 300.00");
    expect(totals).toHaveTextContent("Credited USD -60.00");
    expect(totals).toHaveTextContent("Owed USD 240.00");
    const call = server.calls.find((c) => c.url.pathname.endsWith("/statement"))!;
    expect(call.url.searchParams.get("legalEntityId")).toBe(LE);
    expect(call.url.searchParams.get("period")).toBe("2027-03");
    expect(pageViolations(container)).toEqual([]);
  });

  it("refuses a bad form without a request and shows the empty month", async () => {
    server.handler = (url) =>
      url.pathname.endsWith("/statement") ? json(200, { ...statement, lineCount: 0, totals: [], lines: [] }) : undefined;
    renderAt("/pricing/commission/statement");
    ask("R1", "2027-13");
    expect(await screen.findByText("Use a month such as 2027-03.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.url.pathname.endsWith("/statement"))).toHaveLength(0);
    ask("R1", "2027-04");
    expect(await screen.findByText("No commission in this month")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download CSV" })).toBeDisabled();
  });

  it("shows a refused statement with its code and retries", async () => {
    server.handler = () => json(500, { code: "COM-0500", detail: "database unavailable" });
    renderAt("/pricing/commission/statement");
    ask();
    expect(await findAlert()).toHaveTextContent("database unavailable");
    server.handler = () => json(200, statement);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("INV-1/1")).toBeInTheDocument();
  });

  it("notes a cut list and downloads the full CSV", async () => {
    const csv = "rowType,period\nLINE,2027-03\n";
    server.handler = (url) =>
      url.searchParams.get("format") === "csv"
        ? text(200, csv, { "Content-Type": "text/csv", "Content-Disposition": 'attachment; filename="commission-statement-2027-03.csv"' })
        : json(200, { ...statement, lineCount: 5001, linesTruncated: true });
    const blobs: Blob[] = [];
    Object.assign(URL, { createObjectURL: vi.fn((b: Blob) => (blobs.push(b), "blob:x")), revokeObjectURL: vi.fn() });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    renderAt("/pricing/commission/statement");
    ask();
    expect(await screen.findByText(/Showing the first 2 of 5001 transactions/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Download CSV" }));
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    expect(await blobs[0]!.text()).toBe(csv);
    expect(await screen.findByText("Downloaded commission-statement-2027-03.csv.")).toBeInTheDocument();
  });
});
