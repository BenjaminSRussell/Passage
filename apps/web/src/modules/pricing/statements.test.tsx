import { findAlert } from "../../test-utils";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Journal, RebateAgreement, RebateStatement } from "./api/client";
import { json, pageViolations, renderAt, server, text, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const A1 = "0190f0a0-0000-7000-8000-000000000401";
const J1 = "0190f0a0-0000-7000-8000-000000000301";

const agreement: RebateAgreement = {
  id: A1,
  legalEntityId: LE,
  name: "Acme quarterly",
  partyRole: "CUSTOMER",
  customerId: "0190f0a0-0000-7000-8000-000000000501",
  basis: "NET_AMOUNT",
  period: "QUARTER",
  tiers: [
    { from: "0", ratePct: "1", amountPerUnit: "0" },
    { from: "5000", ratePct: "2.5", amountPerUnit: "0" },
  ],
  currency: "USD",
  validFrom: "2027-01-01",
  validTo: "2027-12-31",
  accrualAccount: "2150",
  expenseAccount: "4150",
  status: "ACTIVE",
  version: 1,
};

const statement: RebateStatement = {
  agreementId: A1,
  name: "Acme quarterly",
  partyRole: "CUSTOMER",
  basis: "NET_AMOUNT",
  currency: "USD",
  period: "2027-Q2",
  tier: { from: "5000", ratePct: "2.5", amountPerUnit: "0" },
  basisValue: "6000",
  basisQuantity: "0",
  targetAmount: "150.00",
  accruedAmount: "150.00",
  linesAccruedAmount: "150.00",
  settledAmount: "30.00",
  openAmount: "120.00",
  reconciled: true,
  lineCount: 2,
  linesTruncated: false,
  lines: [
    { invoiceLineId: "INV-1/1", invoiceDate: "2027-04-02", basisValue: "3000", basisQuantity: "0", amount: "30.00", status: "SETTLED", retroactive: false, source: "INVOICE" },
    { invoiceLineId: "INV-2/1", invoiceDate: "2027-05-09", basisValue: "3000", basisQuantity: "0", amount: "120.00", status: "ACCRUED", retroactive: false, source: "INVOICE" },
  ],
};

function rebateServer(onStatement: (url: URL) => ReturnType<typeof json> | undefined) {
  server.handler = (url) => {
    if (url.pathname.endsWith(`/rebates/agreements/${A1}/statement`)) return onStatement(url);
    if (url.pathname.endsWith(`/rebates/agreements/${A1}`)) return json(200, agreement);
    if (url.pathname.endsWith("/rebates/accruals")) return json(200, { items: [] });
    return undefined;
  };
}

afterEach(() => vi.restoreAllMocks());

describe("Rebate statement", () => {
  it("asks for a period, then shows the tier, the lines and the accrued, settled and open totals", async () => {
    rebateServer(() => json(200, statement));
    const { container } = renderAt(`/pricing/rebates/${A1}`);
    expect(await screen.findByText("Choose a period to see what the agreement accrued, what was settled and what is still open.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/^Statement period/), { target: { value: "2027-Q2" } });
    fireEvent.click(screen.getByRole("button", { name: "Show statement" }));
    expect(await screen.findByText("INV-2/1")).toBeInTheDocument();
    const call = server.calls.find((c) => c.url.pathname.endsWith("/statement"))!;
    expect(call.url.searchParams.get("period")).toBe("2027-Q2");
    expect(screen.getByText("150.00 USD")).toBeInTheDocument();
    expect(screen.getByText("30.00 USD")).toBeInTheDocument();
    expect(screen.getByText("120.00 USD")).toBeInTheDocument();
    expect(screen.getByText(/Tier in force: from 5000, 2.5% of net value/)).toBeInTheDocument();
    expect(screen.getByText("Matches the tier's amount")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("refuses a period that is not the agreement's own form without calling the service", async () => {
    rebateServer(() => json(200, statement));
    renderAt(`/pricing/rebates/${A1}`);
    fireEvent.change(await screen.findByLabelText(/^Statement period/), { target: { value: "2027-04" } });
    fireEvent.click(screen.getByRole("button", { name: "Show statement" }));
    expect(await screen.findByText("Enter the quarter such as 2027-Q1.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.url.pathname.endsWith("/statement"))).toBe(false);
  });

  it("says so when the period has no accruals, and flags a total that differs from the tier", async () => {
    rebateServer(() => json(200, { ...statement, lines: [], lineCount: 0, accruedAmount: "0.00", settledAmount: "0.00", openAmount: "0.00", tier: undefined }));
    renderAt(`/pricing/rebates/${A1}`);
    fireEvent.change(await screen.findByLabelText(/^Statement period/), { target: { value: "2027-Q3" } });
    fireEvent.click(screen.getByRole("button", { name: "Show statement" }));
    expect(await screen.findByText("Nothing accrued in this period")).toBeInTheDocument();
    expect(screen.getByText(/No tier is reached yet/)).toBeInTheDocument();

    rebateServer(() => json(200, { ...statement, reconciled: false, linesAccruedAmount: "149.99" }));
    fireEvent.change(screen.getByLabelText(/^Statement period/), { target: { value: "2027-Q2" } });
    fireEvent.click(screen.getByRole("button", { name: "Show statement" }));
    expect(
      await screen.findByText("Does not reconcile: the tier owes 150.00 USD, the period shows 150.00 and its rows add up to 149.99"),
    ).toBeInTheDocument();
  });

  it("notes a listed page shorter than the period and points to the CSV for every row", async () => {
    rebateServer(() => json(200, { ...statement, lineCount: 5002, linesTruncated: true }));
    renderAt(`/pricing/rebates/${A1}`);
    fireEvent.change(await screen.findByLabelText(/^Statement period/), { target: { value: "2027-Q2" } });
    fireEvent.click(screen.getByRole("button", { name: "Show statement" }));
    expect(await screen.findByText("Showing the first 2 of 5002 accrual rows; the totals cover all of them. The CSV lists every one.")).toBeInTheDocument();
  });

  it("labels a replayed invoice and a retroactive delta, which has no basis of its own", async () => {
    const delta = { invoiceLineId: "retro:run-1:2027-Q2", invoiceDate: "2027-06-30", amount: "0.00", status: "ACCRUED" as const, retroactive: true, source: "RETRO_DELTA" as const };
    const replayed = { ...statement.lines[0]!, invoiceLineId: "INV-0/1", status: "ACCRUED" as const, amount: "0.00", retroactive: true, source: "REPLAYED" as const };
    rebateServer(() => json(200, { ...statement, lineCount: 4, lines: [replayed, ...statement.lines, delta] }));
    renderAt(`/pricing/rebates/${A1}`);
    fireEvent.change(await screen.findByLabelText(/^Statement period/), { target: { value: "2027-Q2" } });
    fireEvent.click(screen.getByRole("button", { name: "Show statement" }));
    const row = (await screen.findByText("retro:run-1:2027-Q2")).closest("tr")!;
    expect(row).toHaveTextContent("Retroactive delta");
    expect(row).toHaveTextContent("—");
    expect(screen.getByText("INV-0/1").closest("tr")!).toHaveTextContent("Replayed invoice");
    expect(screen.getByText("INV-1/1").closest("tr")!).toHaveTextContent("Invoice feed");
  });

  it("shows a refused statement with its code and retries", async () => {
    rebateServer(() => json(500, { code: "COM-0500", detail: "database unavailable" }));
    renderAt(`/pricing/rebates/${A1}`);
    fireEvent.change(await screen.findByLabelText(/^Statement period/), { target: { value: "2027-Q2" } });
    fireEvent.click(screen.getByRole("button", { name: "Show statement" }));
    expect(await screen.findByText("Could not load the statement")).toBeInTheDocument();
    expect(await findAlert()).toHaveTextContent("(COM-0500)");
    rebateServer(() => json(200, statement));
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("INV-1/1")).toBeInTheDocument();
  });

  it("downloads the statement CSV for the chosen period", async () => {
    const csv = "rowType,period\nLINE,2027-Q2\n";
    rebateServer((url) =>
      url.searchParams.get("format") === "csv"
        ? text(200, csv, { "Content-Type": "text/csv", "Content-Disposition": `attachment; filename="rebate-statement-${A1}-2027-Q2.csv"` })
        : json(200, statement),
    );
    const blobs: Blob[] = [];
    Object.assign(URL, { createObjectURL: vi.fn((b: Blob) => (blobs.push(b), "blob:x")), revokeObjectURL: vi.fn() });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    renderAt(`/pricing/rebates/${A1}`);
    fireEvent.change(await screen.findByLabelText(/^Statement period/), { target: { value: "2027-Q2" } });
    fireEvent.click(screen.getByRole("button", { name: "Show statement" }));
    await screen.findByText("INV-1/1");
    fireEvent.click(screen.getByRole("button", { name: "Download CSV" }));
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    const call = server.calls.find((c) => c.url.searchParams.get("format") === "csv")!;
    expect(call.url.searchParams.get("period")).toBe("2027-Q2");
    expect(await blobs[0]!.text()).toBe(csv);
    expect(await screen.findByText(/^Downloaded rebate-statement-.*\.csv\.$/)).toBeInTheDocument();
  });
});

const posted: Journal = { id: J1, legalEntityId: LE, name: "Autumn prices", status: "POSTED", smartRound: true, lineCount: 1, errorCount: 0, createdAt: "2027-09-01T10:00:00Z", errors: [], lines: [] };

describe("Price journal audit extract", () => {
  it("offers the download only once the journal is posted or reversed", async () => {
    server.handler = (url) => (url.pathname.endsWith(`/journals/${J1}`) ? json(200, { ...posted, status: "VALIDATED" }) : undefined);
    renderAt(`/pricing/journals/${J1}`);
    expect(await screen.findByRole("heading", { name: "Autumn prices" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Download audit extract" })).not.toBeInTheDocument();
  });

  it("downloads the extract of a posted journal", async () => {
    const csv = "journalId,lineNo\nj,1\n";
    server.handler = (url) => {
      if (url.pathname.endsWith("/audit-extract")) return text(200, csv, { "Content-Type": "text/csv", "Content-Disposition": `attachment; filename="price-journal-${J1}-audit.csv"` });
      if (url.pathname.endsWith(`/journals/${J1}`)) return json(200, posted);
      return undefined;
    };
    const blobs: Blob[] = [];
    const revoke = vi.fn();
    Object.assign(URL, { createObjectURL: vi.fn((b: Blob) => (blobs.push(b), "blob:x")), revokeObjectURL: revoke });
    // The object URL must outlive the click's task, or some browsers cancel the download.
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => expect(revoke).not.toHaveBeenCalled());
    renderAt(`/pricing/journals/${J1}`);
    fireEvent.click(await screen.findByRole("button", { name: "Download audit extract" }));
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(revoke).toHaveBeenCalledWith("blob:x"));
    expect(server.calls.find((c) => c.url.pathname.endsWith("/audit-extract"))!.url.searchParams.get("format")).toBe("csv");
    expect(await blobs[0]!.text()).toBe(csv);
    expect(await screen.findByText(`Downloaded price-journal-${J1}-audit.csv.`)).toBeInTheDocument();
  });

  it("shows the service's refusal", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/audit-extract")) return json(409, { code: "PRC-0064", detail: "price journal is VALIDATED" });
      if (url.pathname.endsWith(`/journals/${J1}`)) return json(200, posted);
      return undefined;
    };
    renderAt(`/pricing/journals/${J1}`);
    fireEvent.click(await screen.findByRole("button", { name: "Download audit extract" }));
    expect(await screen.findByText("Could not download the audit extract")).toBeInTheDocument();
    expect(await findAlert()).toHaveTextContent("(PRC-0064)");
  });
});
