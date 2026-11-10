import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Journal } from "./api/client";
import { previousMonth, validateSettlement } from "./CommissionSettlementPage";
import { describeFaults, validateImport } from "./ImportJournalPage";
import { idempotencyKey, json, pageViolations, renderAt, server, text, useFakeServer } from "./testServer";
import { describeAccruals } from "./WorkspacePage";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const J1 = "0190f0a0-0000-7000-8000-000000000301";
const CSV_HEADER = "op,recordId,expectedVersion,adjustPct";

/** jsdom's Blob has no text(); FileReader reads it. */
function blobText(blob: Blob): Promise<string> {
  return new Promise((resolve) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.readAsText(blob);
  });
}

function csvFile(content: string, name = "prices.csv"): File {
  return new File([content], name, { type: "text/csv" });
}

const created: Journal = {
  id: J1,
  legalEntityId: LE,
  name: "Spring import",
  status: "OPEN",
  smartRound: true,
  lineCount: 2,
  errorCount: 0,
  createdAt: "2027-03-01T10:00:00Z",
  errors: [],
  lines: [],
};

function fillImport(file: File) {
  fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
  fireEvent.change(screen.getByLabelText("Journal name"), { target: { value: "Spring import" } });
  fireEvent.change(screen.getByLabelText("CSV file"), { target: { files: [file] } });
}

describe("Price journal CSV import", () => {
  it("uploads the file as text/csv with the header in the query and an Idempotency-Key, then opens the journal", async () => {
    const body = `${CSV_HEADER}\nUPDATE,0190f0a0-0000-7000-8000-000000000201,2,5\nDELETE,0190f0a0-0000-7000-8000-000000000202,2,\n`;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/journals:import-csv")) return json(201, created);
      if (url.pathname.endsWith(`/journals/${J1}`)) return json(200, created);
      return undefined;
    };
    const { container } = renderAt("/pricing/journals/import");
    expect(screen.getByRole("heading", { name: "Import price journal" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fillImport(csvFile(body));
    fireEvent.click(screen.getByLabelText("Smart rounding"));
    fireEvent.click(screen.getByRole("button", { name: "Import journal" }));
    expect(await screen.findByRole("heading", { name: "Spring import" })).toBeInTheDocument();
    const upload = server.calls.find((c) => c.method === "POST")!;
    expect(upload.url.pathname).toBe("/api/v1/pricing/journals:import-csv");
    expect(Object.fromEntries(upload.url.searchParams)).toEqual({ legalEntityId: LE, name: "Spring import", smartRound: "true" });
    expect(new Headers(upload.init.headers).get("Content-Type")).toBe("text/csv");
    expect(idempotencyKey(upload)).toMatch(/.+/);
    expect(upload.body).toBe(body);
  });

  it("checks the form before uploading", async () => {
    renderAt("/pricing/journals/import");
    fireEvent.click(screen.getByRole("button", { name: "Import journal" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter a name of up to 120 characters.")).toBeInTheDocument();
    expect(screen.getByText("Choose a CSV file.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("lists the service's row faults by row number, header first", async () => {
    server.handler = () =>
      json(422, {
        code: "COM-0001",
        detail: "request is invalid",
        errors: { "rows[3].searchNext": "must be true or false", "csv.header": 'unknown column "colour"', "lines[0].record.amount": "must not be negative" },
      });
    renderAt("/pricing/journals/import");
    fillImport(csvFile(`${CSV_HEADER},colour\n`));
    fireEvent.click(screen.getByRole("button", { name: "Import journal" }));
    const alert = await findAlert();
    expect(within(alert).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      'Header: unknown column "colour"',
      "Row 1, record.amount: must not be negative",
      "Row 3, searchNext: must be true or false",
    ]);
  });

  it("shows a service failure that names no field", async () => {
    server.handler = () => json(413, { code: "COM-0001", detail: "the CSV file is larger than 32 MB" });
    renderAt("/pricing/journals/import");
    fillImport(csvFile(`${CSV_HEADER}\n`));
    fireEvent.click(screen.getByRole("button", { name: "Import journal" }));
    expect(await findAlert()).toHaveTextContent("the CSV file is larger than 32 MB");
  });

  it("validates the draft and refuses a file over 32 MB", () => {
    const big = { size: 33 * 1024 * 1024 } as File;
    expect(validateImport({ legalEntityId: LE, name: "X", smartRound: false, file: big })).toEqual({ file: "The file is larger than 32 MB. Split it into several journals." });
    expect(validateImport({ legalEntityId: LE, name: "X", smartRound: false, file: csvFile("op\n") })).toEqual({});
    expect(describeFaults({ csv: "150 rows have errors; the first 100 are listed", lines: "at least one line is required" })).toEqual([
      "150 rows have errors; the first 100 are listed",
      "at least one line is required",
    ]);
  });

  it("is reachable from the price journal log", async () => {
    server.handler = (url) => (url.pathname.endsWith("/journals") ? json(200, { items: [] }) : undefined);
    renderAt("/pricing/journals");
    fireEvent.click(await screen.findByRole("link", { name: "Import CSV" }));
    expect(await screen.findByRole("heading", { name: "Import price journal" })).toBeInTheDocument();
  });
});

const SETTLEMENT_CSV = "target,rep_id,currency,amount,transactions,from,to\r\nPAYROLL,R1,USD,240.00,3,2027-03-01,2027-03-31\r\nPAYROLL,\"R,2\",USD,160.00,3,2027-03-01,2027-03-31\r\n";

describe("Commission settlement", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  function fillSettlement() {
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("First invoice date"), { target: { value: "2027-03-01" } });
    fireEvent.change(screen.getByLabelText("Last invoice date"), { target: { value: "2027-03-31" } });
    fireEvent.change(screen.getByLabelText("Settle through"), { target: { value: "PAYROLL" } });
  }

  it("previews the totals per rep from the service's CSV and downloads the same file", async () => {
    server.handler = (url) =>
      url.pathname.endsWith("/commission/settlement-export")
        ? text(200, SETTLEMENT_CSV, { "Content-Type": "text/csv", "Content-Disposition": 'attachment; filename="commission-settlement-PAYROLL-2027-03-01-2027-03-31.csv"' })
        : undefined;
    const { container } = renderAt("/pricing/commission/settlement");
    expect(screen.getByRole("heading", { name: "Commission settlement" })).toBeInTheDocument();
    fillSettlement();
    fireEvent.click(screen.getByRole("button", { name: "Preview settlement" }));
    const table = await findTable({ name: "Commission settlement" });
    expect(table).toHaveTextContent("240.00 USD");
    expect(within(table).getByText("R,2")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Settlement totals" })).toHaveTextContent("Commission 400 USD");
    const call = server.calls[0];
    expect(Object.fromEntries(call.url.searchParams)).toEqual({ legalEntityId: LE, from: "2027-03-01", to: "2027-03-31", target: "PAYROLL" });
    expect(pageViolations(container)).toEqual([]);

    const blobs: Blob[] = [];
    const create = vi.fn((b: Blob) => {
      blobs.push(b);
      return "blob:settlement";
    });
    const revoke = vi.fn();
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: revoke });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      expect(this.download).toBe("commission-settlement-PAYROLL-2027-03-01-2027-03-31.csv");
    });
    fireEvent.click(screen.getByRole("button", { name: "Download CSV" }));
    expect(click).toHaveBeenCalledTimes(1);
    expect(await blobText(blobs[0])).toBe(SETTLEMENT_CSV);
    expect(revoke).toHaveBeenCalledWith("blob:settlement");
  });

  it("shows the empty state and no download when the period has no commission", async () => {
    server.handler = () => text(200, "target,rep_id,currency,amount,transactions,from,to\n", { "Content-Type": "text/csv" });
    renderAt("/pricing/commission/settlement");
    fillSettlement();
    fireEvent.click(screen.getByRole("button", { name: "Preview settlement" }));
    expect(await screen.findByText("No commission in this period")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Download CSV" })).toBeNull();
  });

  it("puts the service's field errors on the fields and shows other failures", async () => {
    server.handler = () => json(422, { code: "COM-0001", detail: "request is invalid", errors: { to: "an export covers at most 366 days" } });
    renderAt("/pricing/commission/settlement");
    fillSettlement();
    fireEvent.click(screen.getByRole("button", { name: "Preview settlement" }));
    expect(await screen.findByText("an export covers at most 366 days")).toBeInTheDocument();

    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty pricing.commission.view" });
    fireEvent.click(screen.getByRole("button", { name: "Preview settlement" }));
    expect(await findAlert()).toHaveTextContent("missing duty pricing.commission.view");
  });

  it("validates the period before asking the service", () => {
    expect(validateSettlement({ legalEntityId: "x", from: "2027-03-31", to: "2027-03-01", target: "AP" })).toEqual({
      legalEntityId: "Enter the legal entity id (a UUID).",
      to: "The last date must not be before the first.",
    });
    expect(validateSettlement({ legalEntityId: LE, from: "2026-01-01", to: "2027-01-02", target: "AP" }).to).toBe("An export covers at most 366 days.");
    expect(validateSettlement({ legalEntityId: LE, from: "2027-03-01", to: "2027-03-31", target: "AP" })).toEqual({});
    expect(previousMonth("2027-03-15")).toEqual({ from: "2027-02-01", to: "2027-02-28" });
    expect(previousMonth("2027-01-01")).toEqual({ from: "2026-12-01", to: "2026-12-31" });
  });
});

describe("Record expiry on the workspace", () => {
  const tiles = { asOf: "2027-03-20", month: "2027-03", activeRecords: 1, draftRecords: 0, expiringIn30Days: 0, journalsToPost: 0, liveCampaigns: 0, claimsAwaitingApproval: 0, accrualsThisMonth: [] };

  it("runs the expiry for a legal entity and day, reports the count and reloads the tiles", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/workspace")) return json(200, tiles);
      if (url.pathname.endsWith("/condition-records")) return json(200, { items: [] });
      if (init.method === "POST" && url.pathname.endsWith("/condition-records:expire")) return json(200, { legalEntityId: LE, asOf: "2027-03-20", expired: 2, recordIds: ["a", "b"] });
      return undefined;
    };
    renderAt("/pricing");
    await screen.findByText("Nothing expires in the next 30 days");
    const card = screen.getByRole("heading", { name: "Record expiry" }).closest("div")!.parentElement!;
    fireEvent.change(within(card).getByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(within(card).getByLabelText("Expire validity ended before"), { target: { value: "2026-03-20" } });
    fireEvent.click(within(card).getByRole("button", { name: "Expire ended agreements" }));
    expect(await screen.findByText("2 agreements expired.")).toBeInTheDocument();
    const run = server.calls.find((c) => c.method === "POST")!;
    expect(run.body).toEqual({ legalEntityId: LE, asOf: "2026-03-20" });
    await waitFor(() => expect(server.calls.filter((c) => c.url.pathname.endsWith("/workspace"))).toHaveLength(2));
  });

  it("says so when nothing ended and refuses a malformed entity", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/workspace")) return json(200, tiles);
      if (url.pathname.endsWith("/condition-records")) return json(200, { items: [] });
      if (init.method === "POST") return json(200, { legalEntityId: LE, asOf: "2027-03-20", expired: 0, recordIds: [] });
      return undefined;
    };
    renderAt("/pricing");
    const button = await screen.findByRole("button", { name: "Expire ended agreements" });
    fireEvent.click(button);
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getAllByLabelText("Legal entity id").at(-1)!, { target: { value: LE } });
    fireEvent.click(button);
    expect(await screen.findByText("No agreements ended before that day.")).toBeInTheDocument();
  });

  it("formats accruals per currency for one party role", () => {
    const totals = [
      { partyRole: "CUSTOMER" as const, currency: "USD", amount: "200" },
      { partyRole: "VENDOR" as const, currency: "USD", amount: "600" },
    ];
    expect(describeAccruals(totals, "CUSTOMER")).toBe("200 USD");
    expect(describeAccruals(totals, "VENDOR")).toBe("600 USD");
    expect(describeAccruals([], "VENDOR")).toBe("0");
  });
});
