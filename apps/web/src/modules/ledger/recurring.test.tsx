import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { JournalBatch, RecurringJournal } from "./api/client";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";
import { emptyRecurring, emptyRecurringLine, toRecurringInput, validateRecurring } from "./RecurringJournalPage";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const RJ = "0190a000-0000-7000-8000-0000000000b1";
const BATCH = "0190a000-0000-7000-8000-000000000010";
const base = "/api/v1/ledger/recurring-journals";

function recurring(patch: Partial<RecurringJournal> = {}): RecurringJournal {
  return {
    id: RJ,
    ledgerId: LEDGER,
    legalEntityId: "0190a000-0000-7000-8000-000000000099",
    journalName: "GJ",
    name: "Monthly freight accrual",
    description: "Freight accrual",
    frequency: "monthly",
    dayOfMonth: 0,
    startDate: "2026-04-01",
    baseAmount: "1000",
    status: "active",
    nextRunDate: "2026-04-30",
    version: 2,
    lineCount: 2,
    createdBy: "clerk",
    createdAt: "2026-03-31T09:00:00Z",
    updatedAt: "2026-03-31T09:00:00Z",
    lines: [
      { lineNumber: 1, account: "6010", dimensions: { DEPT: "D100" }, debitCredit: "D", amountKind: "percent", amount: "100", currency: "USD" },
      { lineNumber: 2, account: "2100", debitCredit: "C", amountKind: "percent", amount: "100", currency: "USD" },
    ],
    ...patch,
  };
}

const run = { id: "0190a000-0000-7000-8000-0000000000d1", recurringId: RJ, scheduledDate: "2026-04-30", batchId: BATCH, generatedBy: "clerk", generatedAt: "2026-04-30T06:00:00Z" };

describe("recurring journal checks", () => {
  it("mirrors the service rules and builds the wire form", () => {
    const empty = emptyRecurring();
    expect(Object.keys(validateRecurring(empty)).sort()).toEqual(["journalName", "lines.0.account", "lines.0.amount", "lines.1.account", "lines.1.amount", "ledgerId", "name", "startDate"].sort());
    const v = {
      ...empty,
      ledgerId: LEDGER,
      journalName: "GJ",
      name: " Rent ",
      startDate: "2026-04-01",
      endDate: "2026-12-31",
      baseAmount: "1000",
      lines: [
        { ...emptyRecurringLine("D"), account: "6100", kind: "percent" as const, amount: "60", dimensions: "DEPT=D100" },
        { ...emptyRecurringLine("C"), account: "2100", amount: "600.00" },
      ],
    };
    expect(validateRecurring(v)).toEqual({});
    expect(toRecurringInput(v)).toMatchObject({
      name: "Rent",
      dayOfMonth: 0,
      endDate: "2026-12-31",
      baseAmount: "1000",
      lines: [
        { account: "6100", dimensions: { DEPT: "D100" }, debitCredit: "D", amountKind: "percent", amount: { currency: "USD", value: "60" } },
        { account: "2100", debitCredit: "C", amountKind: "fixed", amount: { currency: "USD", value: "600.00" } },
      ],
    });
    expect(validateRecurring({ ...v, endDate: "2026-01-01", dayOfMonth: "32" })).toMatchObject({ endDate: expect.any(String), dayOfMonth: expect.any(String) });
    expect(validateRecurring({ ...v, lines: [{ ...v.lines[0], amount: "1001" }] })["lines.0.amount"]).toContain("at most 1000");
  });
});

describe("recurring journal list", () => {
  it("lists schedules, filters by status and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname === base) {
        const status = url.searchParams.get("status");
        return json(200, { items: status === "paused" ? [] : [recurring(), recurring({ id: "x", name: "Old", status: "paused", nextRunDate: undefined })] });
      }
      if (url.pathname === `${base}/${RJ}`) return json(200, recurring());
      if (url.pathname === `${base}/${RJ}/runs`) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt("/ledger/recurring-journals");
    const table = await findTable({ name: "Recurring journals" });
    const row = (await within(table).findByRole("button", { name: "Monthly freight accrual" })).closest("tr") as HTMLElement;
    expect(row.textContent).toContain("Monthly, last day");
    expect(row.textContent).toContain("2026-04-30");
    expect(row.textContent).toContain("Never");
    expect(within(table).getByText("Old").closest("tr")?.textContent).toContain("Paused");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "paused" } });
    expect(await screen.findByText("No paused recurring journals")).toBeInTheDocument();
  });

  it("shows the empty state and the error with a retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(500, { code: "COM-0500", detail: "internal error" }) : json(200, { items: [] }));
    renderAt("/ledger/recurring-journals");
    expect(await screen.findByText("Could not load the recurring journals")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No recurring journals yet")).toBeInTheDocument();
  });
});

describe("new recurring journal", () => {
  it("checks the form, then creates with PUT and opens the template", async () => {
    server.handler = (url, init) => {
      if (url.pathname.startsWith(`${base}/`) && init.method === "PUT") return json(201, recurring());
      if (url.pathname === `${base}/${RJ}`) return json(200, recurring());
      if (url.pathname === `${base}/${RJ}/runs`) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt("/ledger/recurring-journals/new");
    fireEvent.click(screen.getByRole("button", { name: "Save recurring journal" }));
    expect(await screen.findByText("Enter the ledger id as a UUID.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.change(screen.getByLabelText("Journal name"), { target: { value: "GJ" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Monthly freight accrual" } });
    fireEvent.change(screen.getByLabelText("Start date"), { target: { value: "2026-04-01" } });
    fireEvent.change(screen.getByLabelText("Base amount"), { target: { value: "1000" } });
    fireEvent.change(screen.getByLabelText("Line 1 account"), { target: { value: "6010" } });
    fireEvent.change(screen.getByLabelText("Line 1 amount kind"), { target: { value: "percent" } });
    fireEvent.change(screen.getByLabelText("Line 1 amount"), { target: { value: "100" } });
    fireEvent.change(screen.getByLabelText("Line 2 account"), { target: { value: "2100" } });
    fireEvent.change(screen.getByLabelText("Line 2 amount kind"), { target: { value: "percent" } });
    fireEvent.change(screen.getByLabelText("Line 2 amount"), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "Save recurring journal" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/ledger/recurring-journals/${RJ}`));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toMatch(new RegExp(`^${base}/[0-9a-f-]{36}$`));
    expect(new Headers(put?.init.headers).get("If-Match")).toBeNull();
    expect(put?.body).toMatchObject({ ledgerId: LEDGER, journalName: "GJ", frequency: "monthly", dayOfMonth: 0, baseAmount: "1000" });
  });
});

describe("recurring journal detail", () => {
  function detailServer(status: RecurringJournal["status"] = "active") {
    let current = recurring({ status });
    server.handler = (url, init) => {
      if (url.pathname === `${base}/${RJ}/generate`) return json(201, run);
      if (url.pathname === `${base}/${RJ}/pause`) return json(200, (current = recurring({ status: "paused", version: 3 })));
      if (url.pathname === `${base}/${RJ}/resume`) return json(200, (current = recurring({ status: "active", version: 4 })));
      if (url.pathname === `${base}/${RJ}/runs`) return json(200, { items: [run] });
      if (url.pathname === `${base}/${RJ}` && (init.method ?? "GET") === "GET") return json(200, current);
      return undefined;
    };
  }

  it("shows schedule and runs, generates a batch and pauses with If-Match", async () => {
    detailServer();
    const { container } = renderAt(`/ledger/recurring-journals/${RJ}`);
    const runs = await findTable({ name: "Generated batches" });
    expect(within(runs).getByText("2026-04-30")).toBeInTheDocument();
    expect(within(runs).getByRole("link", { name: "Open batch" })).toHaveAttribute("href", `/ledger/journal-batches/${BATCH}`);
    expect(screen.getByLabelText("Ledger id")).toHaveAttribute("readonly");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Base amount for this run"), { target: { value: "250.50" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate draft batch" }));
    expect(await screen.findByText("Draft batch generated for 2026-04-30.")).toBeInTheDocument();
    // The date defaults to the next run date and is always sent, so a retry replays rather than advancing.
    const gen = server.calls.find((c) => c.url.pathname.endsWith("/generate"));
    expect(gen?.body).toEqual({ scheduledDate: "2026-04-30", baseAmount: "250.50" });

    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    expect(await screen.findByText("Recurring journal paused.")).toBeInTheDocument();
    const pause = server.calls.find((c) => c.url.pathname.endsWith("/pause"));
    expect(new Headers(pause?.init.headers).get("If-Match")).toBe('"2"');
    expect(await screen.findByRole("button", { name: "Resume" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Generate draft batch" })).toBeDisabled();
  });

  it("asks for a date when the schedule has no next run", async () => {
    server.handler = (url) => {
      if (url.pathname === `${base}/${RJ}/runs`) return json(200, { items: [] });
      if (url.pathname === `${base}/${RJ}`) return json(200, recurring({ nextRunDate: undefined, endDate: "2026-12-31" }));
      return undefined;
    };
    renderAt(`/ledger/recurring-journals/${RJ}`);
    fireEvent.click(await screen.findByRole("button", { name: "Generate draft batch" }));
    expect(await screen.findByText("Enter the scheduled date of the run.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.url.pathname.endsWith("/generate"))).toBe(false);
  });

  it("reports a refused generation", async () => {
    detailServer();
    const handler = server.handler;
    server.handler = (url, init) =>
      url.pathname.endsWith("/generate") ? json(422, { code: "LEDGER_RECURRING_BASE_REQUIRED", detail: "the template has percentage lines" }) : handler(url, init);
    renderAt(`/ledger/recurring-journals/${RJ}`);
    fireEvent.click(await screen.findByRole("button", { name: "Generate draft batch" }));
    expect((await screen.findByRole("alert")).textContent).toContain("LEDGER_RECURRING_BASE_REQUIRED");
  });

  it("shows the load error with a retry", async () => {
    server.handler = () => json(404, { code: "LEDGER_RECURRING_NOT_FOUND", detail: "recurring journal not found" });
    renderAt(`/ledger/recurring-journals/${RJ}`);
    expect(await screen.findByText("Could not load the recurring journal")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("save a batch as a recurring journal", () => {
  const source: JournalBatch = {
    id: BATCH,
    ledgerId: LEDGER,
    legalEntityId: "0190a000-0000-7000-8000-000000000099",
    journalName: "GJ",
    description: "March rent",
    postingDate: "2026-03-31",
    currency: "EUR",
    status: "posted",
    version: 4,
    requiresApproval: false,
    selfApprovalAllowed: false,
    totalDebit: "900.00",
    totalCredit: "900.00",
    lineCount: 2,
    createdBy: "clerk",
    createdAt: "2026-03-31T09:00:00Z",
    updatedAt: "2026-03-31T09:00:00Z",
    lines: [
      { lineNumber: 1, account: "6100", dimensions: { DEPT: "D100" }, debitCredit: "D", amount: "900.00", currency: "EUR", text: "Rent" },
      { lineNumber: 2, account: "2100", debitCredit: "C", amount: "900.00", currency: "EUR" },
    ],
  };

  it("fills the form from the batch as fixed amounts and saves it as a new template", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/ledger/journal-batches/${BATCH}`) return json(200, source);
      if (url.pathname.startsWith(`${base}/`) && init.method === "PUT") return json(201, recurring());
      if (url.pathname === `${base}/${RJ}`) return json(200, recurring());
      if (url.pathname === `${base}/${RJ}/runs`) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt(`/ledger/recurring-journals/new?fromBatch=${BATCH}`);
    // The batch description becomes both the template name and the batch description.
    expect(await screen.findAllByDisplayValue("March rent")).toHaveLength(2);
    expect(screen.getByLabelText("Ledger id")).toHaveValue(LEDGER);
    expect(screen.getByLabelText("Start date")).toHaveValue("2026-03-31");
    expect(screen.getByLabelText("Currency")).toHaveValue("EUR");
    expect(screen.getByLabelText("Line 1 dimensions")).toHaveValue("DEPT=D100");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save recurring journal" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/ledger/recurring-journals/${RJ}`));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.body).toMatchObject({
      ledgerId: LEDGER,
      journalName: "GJ",
      startDate: "2026-03-31",
      lines: [
        { account: "6100", dimensions: { DEPT: "D100" }, debitCredit: "D", amountKind: "fixed", amount: { currency: "EUR", value: "900.00" }, text: "Rent" },
        { account: "2100", debitCredit: "C", amountKind: "fixed", amount: { currency: "EUR", value: "900.00" } },
      ],
    });
  });

  it("shows the error when the batch cannot be loaded", async () => {
    server.handler = () => json(404, { code: "LEDGER_BATCH_NOT_FOUND", detail: "batch not found" });
    renderAt(`/ledger/recurring-journals/new?fromBatch=${BATCH}`);
    expect(await screen.findByText("Could not load the batch to copy")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save recurring journal" })).toBeNull();
  });

  it("refuses a batch id that is not a UUID without calling the service", async () => {
    renderAt("/ledger/recurring-journals/new?fromBatch=nope");
    expect(await screen.findByText("The batch id in the link is not valid")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("is offered on the batch screen", async () => {
    server.handler = (url) => (url.pathname === `/api/v1/ledger/journal-batches/${BATCH}` ? json(200, source) : undefined);
    renderAt(`/ledger/journal-batches/${BATCH}`);
    expect(await screen.findByRole("link", { name: "Save as recurring journal" })).toHaveAttribute("href", `/ledger/recurring-journals/new?fromBatch=${BATCH}`);
  });
});
