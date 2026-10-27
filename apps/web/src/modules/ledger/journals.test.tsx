import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { JournalBatch, JournalName } from "./api/client";
import { formatMicros, lineTotals, parseDimensions, toMicros, validateLines, emptyLine } from "./BatchLinesEditor";
import { availableActions, currencyScale } from "./JournalBatchPage";
import { emptyJournalName, toJournalNameInput, validateJournalName } from "./JournalNameFormPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const BATCH = "0190a000-0000-7000-8000-000000000010";
const ENTRY = "0190a000-0000-7000-8000-000000000003";

function name(patch: Partial<JournalName> = {}): JournalName {
  return {
    code: "GJ",
    name: "General journal",
    requiresApproval: false,
    selfApprovalAllowed: false,
    reversalReasonRequired: false,
    allowReconAccounts: false,
    linesLimit: 5000,
    active: true,
    updatedAt: "2026-10-27T10:00:00Z",
    ...patch,
  };
}

function batch(patch: Partial<JournalBatch> = {}): JournalBatch {
  return {
    id: BATCH,
    ledgerId: LEDGER,
    legalEntityId: "0190a000-0000-7000-8000-000000000099",
    journalName: "GJ",
    description: "March accrual",
    postingDate: "2026-03-31",
    currency: "USD",
    status: "draft",
    version: 1,
    requiresApproval: false,
    selfApprovalAllowed: false,
    totalDebit: "125.00",
    totalCredit: "125.00",
    lineCount: 2,
    createdBy: "clerk",
    createdAt: "2026-03-31T09:00:00Z",
    updatedAt: "2026-03-31T09:00:00Z",
    lines: [
      { lineNumber: 1, account: "6010", dimensions: { DEPT: "D100" }, debitCredit: "D", amount: "125.00", currency: "USD", text: "Freight" },
      { lineNumber: 2, account: "2100", debitCredit: "C", amount: "125.00", currency: "USD" },
    ],
    ...patch,
  };
}

const method = (init: RequestInit) => init.method ?? "GET";
const ifMatch = (call: { init: RequestInit }) => new Headers(call.init.headers).get("If-Match");

describe("batch line arithmetic", () => {
  it("adds amounts exactly in millionths, never as floats", () => {
    expect(toMicros("0.1")).toBe(100000n);
    expect(toMicros("125")).toBe(125000000n);
    expect(toMicros("-1")).toBeNull();
    expect(toMicros("1.0000001")).toBeNull();
    expect(toMicros("1e3")).toBeNull();
    const lines = [
      { ...emptyLine("D"), amount: "0.1" },
      { ...emptyLine("D"), amount: "0.2" },
      { ...emptyLine("C"), amount: "0.3" },
    ];
    const totals = lineTotals(lines);
    expect(totals.debit).toBe(totals.credit);
    expect(formatMicros(totals.debit, 2)).toBe("0.30");
    expect(formatMicros(-1500000n, 3)).toBe("-1.500");
    expect(formatMicros(7000000n, 0)).toBe("7");
    expect(currencyScale("JPY")).toBe(0);
    expect(currencyScale("USD")).toBe(2);
  });

  it("parses dimensions and checks lines like the service", () => {
    expect(parseDimensions("DEPT=D100, CC=C200")).toEqual({ DEPT: "D100", CC: "C200" });
    expect(parseDimensions("")).toEqual({});
    expect(parseDimensions("dept=D1")).toBeNull();
    expect(parseDimensions("DEPT=D1, DEPT=D2")).toBeNull();
    expect(parseDimensions("DEPT")).toBeNull();
    const errors = validateLines([{ ...emptyLine("D"), account: "60 10", amount: "ten", dimensions: "DEPT" }]);
    expect(Object.keys(errors).sort()).toEqual(["lines.0.account", "lines.0.amount", "lines.0.dimensions"]);
    expect(validateLines([])).toEqual({ lines: "Add at least one line." });
  });

  it("offers only the BR-11.10 actions of the batch status", () => {
    const of = (patch: Partial<JournalBatch>) => availableActions(batch(patch));
    expect(of({ status: "draft" })).toEqual({ edit: true, submit: true, decide: false, post: false, cancel: true });
    expect(of({ status: "validated" })).toEqual({ edit: true, submit: false, decide: false, post: true, cancel: true });
    expect(of({ status: "validated", requiresApproval: true })).toEqual({ edit: true, submit: true, decide: false, post: false, cancel: true });
    expect(of({ status: "in_approval", requiresApproval: true })).toEqual({ edit: false, submit: false, decide: true, post: false, cancel: false });
    expect(of({ status: "approved", requiresApproval: true })).toEqual({ edit: true, submit: false, decide: false, post: true, cancel: false });
    expect(of({ status: "rejected", requiresApproval: true })).toEqual({ edit: true, submit: false, decide: false, post: false, cancel: true });
    expect(of({ status: "posted" })).toEqual({ edit: false, submit: false, decide: false, post: false, cancel: false });
    expect(of({ status: "cancelled" })).toEqual({ edit: false, submit: false, decide: false, post: false, cancel: false });
  });
});

describe("journal names", () => {
  it("lists the names with their controls and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/ledger/journal-names") return json(200, { items: [name(), name({ code: "APPR", name: "Approved journal", requiresApproval: true, active: false })] });
      if (url.pathname === "/api/v1/ledger/journal-names/APPR") return json(200, name({ code: "APPR", name: "Approved journal", requiresApproval: true, active: false }));
      return undefined;
    };
    const { container } = renderAt("/ledger/journal-names");
    const table = await findTable({ name: "Journal names" });
    const appr = (await within(table).findByRole("button", { name: "APPR" })).closest("tr") as HTMLElement;
    expect(appr.textContent).toContain("Required");
    expect(appr.textContent).toContain("Inactive");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "APPR" }));
    expect(await screen.findByLabelText("Name")).toHaveValue("Approved journal");
    expect(screen.getByLabelText("Code")).toHaveAttribute("readonly");
    expect(screen.getByLabelText("Approval required")).toBeChecked();
  });

  it("shows the empty state and the error with a retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(500, { code: "COM-0500", detail: "internal error" }) : json(200, { items: [] }));
    renderAt("/ledger/journal-names");
    expect(await screen.findByText("Could not load the journal names")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No journal names yet")).toBeInTheDocument();
  });

  it("validates, then creates with PUT", async () => {
    expect(validateJournalName({ ...emptyJournalName, code: "G J", name: "", linesLimit: "0" })).toEqual({
      code: "Use 1 to 20 letters, digits, dashes or underscores.",
      name: "Enter a name of up to 60 characters.",
      linesLimit: "Enter a whole number from 1 to 10000.",
    });
    // Self-approval only makes sense with approval.
    expect(toJournalNameInput({ ...emptyJournalName, code: "X", name: "X", selfApprovalAllowed: true }).selfApprovalAllowed).toBe(false);

    server.handler = (_url, init) => (method(init) === "PUT" ? json(201, name({ code: "APPR", requiresApproval: true })) : json(200, { items: [] }));
    const { container } = renderAt("/ledger/journal-names/new");
    fireEvent.click(screen.getByRole("button", { name: "Save journal name" }));
    expect(await screen.findByText("Enter a name of up to 60 characters.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "APPR" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: " Approved journal " } });
    expect(screen.getByLabelText("Submitter may approve")).toBeDisabled();
    fireEvent.click(screen.getByLabelText("Approval required"));
    fireEvent.click(screen.getByLabelText("Reversal reason required"));
    fireEvent.click(screen.getByRole("button", { name: "Save journal name" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/journal-names"));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toBe("/api/v1/ledger/journal-names/APPR");
    expect(put?.body).toEqual({
      name: "Approved journal",
      requiresApproval: true,
      selfApprovalAllowed: false,
      reversalReasonRequired: true,
      allowReconAccounts: false,
      linesLimit: 5000,
      active: true,
    });
  });
});

describe("journal batch list", () => {
  it("lists batches with status and totals, filters by status and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/ledger/journal-batches") {
        const status = url.searchParams.get("status");
        return json(200, { items: status === "rejected" ? [] : [batch({ status: "in_approval" })] });
      }
      if (url.pathname === `/api/v1/ledger/journal-batches/${BATCH}`) return json(200, batch({ status: "in_approval" }));
      return undefined;
    };
    const { container } = renderAt("/ledger/journal-batches");
    const table = await findTable({ name: "Journal batches" });
    const row = (await within(table).findByRole("button", { name: "GJ" })).closest("tr") as HTMLElement;
    expect(row.textContent).toContain("In approval");
    expect(row.textContent).toContain("125.00 USD");
    expect(row.textContent).toContain("March accrual");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "rejected" } });
    expect(await screen.findByText("No rejected batches")).toBeInTheDocument();
    expect(server.calls.at(-1)?.url.search).toBe("?status=rejected");

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "" } });
    fireEvent.click(await screen.findByRole("button", { name: "GJ" }));
    expect(screen.getByTestId("location").textContent).toBe(`/ledger/journal-batches/${BATCH}`);
  });

  it("shows the error with a retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(503, { code: "COM-0503", detail: "unavailable" }) : json(200, { items: [] }));
    renderAt("/ledger/journal-batches");
    expect(await screen.findByText("Could not load the journal batches")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No journal batches yet")).toBeInTheDocument();
  });
});

function newBatchServer() {
  server.handler = (url, init) => {
    if (url.pathname === `/api/v1/ledger/ledgers/${LEDGER}`)
      return json(200, { id: LEDGER, code: "GL", name: "General ledger", chartCode: "STD", currency: "USD", dimensionAttributes: ["DEPT"] });
    if (url.pathname === "/api/v1/ledger/journal-names") return json(200, { items: [name(), name({ code: "OLD", active: false })] });
    if (url.pathname === "/api/v1/ledger/journal-batches" && method(init) === "POST") return json(201, batch());
    if (url.pathname === `/api/v1/ledger/journal-batches/${BATCH}`) return json(200, batch());
    return undefined;
  };
}

describe("new journal batch", () => {
  it("checks the lines, keeps the totals and creates the draft", async () => {
    newBatchServer();
    const { container } = renderAt(`/ledger/journal-batches/new?ledger=${LEDGER}`);
    const form = await screen.findByRole("form", { name: "New journal batch" });
    const select = within(form).getByLabelText("Journal name");
    expect(within(select).queryByText(/OLD/)).toBeNull();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Create batch" }));
    expect(await screen.findByText("Pick a journal name.")).toBeInTheDocument();
    expect(screen.getByText("Line 1: enter an account of up to 20 letters, digits, dots, dashes or underscores.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);

    fireEvent.change(select, { target: { value: "GJ" } });
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "March accrual" } });
    fireEvent.change(screen.getByLabelText("Posting date"), { target: { value: "2026-03-31" } });
    fireEvent.change(screen.getByLabelText("Line 1 account"), { target: { value: "6010" } });
    fireEvent.change(screen.getByLabelText("Line 1 dimensions"), { target: { value: "dept=d100" } });
    fireEvent.change(screen.getByLabelText("Line 1 amount"), { target: { value: "125" } });
    fireEvent.change(screen.getByLabelText("Line 1 text"), { target: { value: "Freight" } });
    fireEvent.change(screen.getByLabelText("Line 2 account"), { target: { value: "2100" } });
    fireEvent.change(screen.getByLabelText("Line 2 amount"), { target: { value: "100.10" } });
    expect(screen.getByTestId("batch-debit").textContent).toBe("125.00");
    expect(screen.getByTestId("batch-credit").textContent).toBe("100.10");
    expect(screen.getByTestId("batch-difference").textContent).toBe("24.90");

    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    expect(screen.getByLabelText("Line 3 debit or credit")).toHaveValue("C");
    fireEvent.change(screen.getByLabelText("Line 3 account"), { target: { value: "2100" } });
    fireEvent.change(screen.getByLabelText("Line 3 amount"), { target: { value: "24.90" } });
    expect(screen.getByTestId("batch-difference").textContent).toBe("Balanced");

    fireEvent.click(screen.getByRole("button", { name: "Create batch" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/ledger/journal-batches/${BATCH}`));
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({
      ledgerId: LEDGER,
      journalName: "GJ",
      description: "March accrual",
      postingDate: "2026-03-31",
      lines: [
        { account: "6010", debitCredit: "D", amount: { currency: "USD", value: "125" }, dimensions: { DEPT: "D100" }, text: "Freight" },
        { account: "2100", debitCredit: "C", amount: { currency: "USD", value: "100.10" } },
        { account: "2100", debitCredit: "C", amount: { currency: "USD", value: "24.90" } },
      ],
    });
  });

  it("shows the service's field errors and asks for a journal name when there is none", async () => {
    newBatchServer();
    const base = server.handler;
    server.handler = (url, init) =>
      url.pathname === "/api/v1/ledger/journal-batches" && method(init) === "POST"
        ? json(400, { code: "COM-0001", detail: "request failed validation", errors: { "lines[1].amount.value": "has more decimals than the currency allows" } })
        : base(url, init);
    renderAt(`/ledger/journal-batches/new?ledger=${LEDGER}`);
    const form = await screen.findByRole("form", { name: "New journal batch" });
    fireEvent.change(within(form).getByLabelText("Journal name"), { target: { value: "GJ" } });
    fireEvent.change(screen.getByLabelText("Posting date"), { target: { value: "2026-03-31" } });
    for (const n of [1, 2]) {
      fireEvent.change(screen.getByLabelText(`Line ${n} account`), { target: { value: "6010" } });
      fireEvent.change(screen.getByLabelText(`Line ${n} amount`), { target: { value: "1.5" } });
    }
    fireEvent.click(screen.getByRole("button", { name: "Create batch" }));
    expect(await screen.findByText("Could not create the batch")).toBeInTheDocument();
    expect(screen.getByText("Line 2 amount.value: has more decimals than the currency allows")).toBeInTheDocument();
  });

  it("points to journal name setup when no name is active", async () => {
    server.handler = (url) => {
      if (url.pathname === `/api/v1/ledger/ledgers/${LEDGER}`)
        return json(200, { id: LEDGER, code: "GL", name: "GL", chartCode: "STD", currency: "USD", dimensionAttributes: [] });
      if (url.pathname === "/api/v1/ledger/journal-names") return json(200, { items: [name({ active: false })] });
      return undefined;
    };
    renderAt(`/ledger/journal-batches/new?ledger=${LEDGER}`);
    expect(await screen.findByText("No active journal names")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New journal name" })).toHaveAttribute("href", "/ledger/journal-names/new");
  });
});

describe("journal batch detail", () => {
  it("validates a draft, then posts it once with a stable Idempotency-Key", async () => {
    let current = batch();
    let postAttempts = 0;
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/ledger/journal-batches/${BATCH}` && method(init) === "GET") return json(200, current);
      if (url.pathname.endsWith("/submit")) {
        current = batch({ status: "validated", version: 2, submittedBy: "clerk" });
        return json(200, current);
      }
      if (url.pathname.endsWith("/post")) {
        postAttempts += 1;
        if (postAttempts === 1) {
          return json(422, { code: "LEDGER_ACCOUNT_SUSPENDED", detail: "account 6010 is suspended", violations: [{ code: "LEDGER_ACCOUNT_SUSPENDED", lineIndex: 0, message: "account 6010 is suspended from 2026-01-01" }] });
        }
        current = batch({ status: "posted", version: 3, postedEntryId: ENTRY, postedBy: "clerk" });
        return json(201, { entries: [{ id: ENTRY, voucher: "GL-000042" }], idempotency: { key: "k", replayed: false } });
      }
      return undefined;
    };
    const { container } = renderAt(`/ledger/journal-batches/${BATCH}`);
    expect(await screen.findByRole("heading", { name: "Journal batch GJ" })).toBeInTheDocument();
    expect(screen.getByText("Draft")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Batch lines" }).textContent).toContain("DEPT=D100");
    expect(screen.queryByRole("button", { name: "Post" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(await screen.findByText("Batch validated; it can be posted")).toBeInTheDocument();
    const submit = server.calls.find((c) => c.url.pathname.endsWith("/submit"));
    expect(submit?.method).toBe("POST");
    expect(ifMatch(submit!)).toBe('"1"');

    fireEvent.click(screen.getByRole("button", { name: "Post" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm post" }));
    expect(await screen.findByText("The batch was not posted")).toBeInTheDocument();
    expect(screen.getByText("Line 1: account 6010 is suspended from 2026-01-01 (LEDGER_ACCOUNT_SUSPENDED)")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Post" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm post" }));
    expect(await screen.findByText("Posted as voucher GL-000042")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open the journal entry" })).toHaveAttribute("href", `/ledger/entries/${ENTRY}`);
    const posts = server.calls.filter((c) => c.url.pathname.endsWith("/post"));
    expect(posts).toHaveLength(2);
    // The retry of the same version reuses the key, so a lost response cannot post twice.
    expect(idempotencyKey(posts[0])).toBeTruthy();
    expect(idempotencyKey(posts[1])).toBe(idempotencyKey(posts[0]));
    expect(ifMatch(posts[1])).toBe('"2"');
    expect(screen.queryByRole("button", { name: "Edit batch" })).toBeNull();
    expect(screen.getByText("Posted batches can't be changed; reverse the journal entry instead.")).toBeInTheDocument();
  });

  it("approves or rejects a batch in approval and shows a refused self-approval", async () => {
    let current = batch({ status: "in_approval", requiresApproval: true, version: 3, submittedBy: "clerk" });
    let refuse = true;
    server.handler = (url, init) => {
      if (method(init) === "GET") return json(200, current);
      if (url.pathname.endsWith("/approve")) {
        if (refuse) return json(403, { code: "LEDGER_SELF_APPROVAL", detail: "the user who submitted the batch may not approve it" });
        current = batch({ status: "approved", requiresApproval: true, version: 4, submittedBy: "clerk", decidedBy: "boss" });
        return json(200, current);
      }
      return undefined;
    };
    renderAt(`/ledger/journal-batches/${BATCH}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText("the user who submitted the batch may not approve it (LEDGER_SELF_APPROVAL)")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reject" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit batch" })).toBeNull();

    refuse = false;
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Batch approved")).toBeInTheDocument();
    expect(screen.getByText("Approved by")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Post" })).toBeInTheDocument();
  });

  it("edits the lines with If-Match and reloads after a version conflict", async () => {
    let current = batch({ status: "validated", version: 2 });
    let conflict = true;
    server.handler = (_url, init) => {
      if (method(init) === "GET") return json(200, current);
      if (method(init) === "PATCH") {
        if (conflict) {
          conflict = false;
          current = batch({ status: "draft", version: 3, description: "Changed elsewhere" });
          return json(412, { code: "LEDGER_VERSION_CONFLICT", detail: "the journal batch changed since it was read" });
        }
        current = batch({ status: "draft", version: 4, description: "Corrected", totalDebit: "130.00", totalCredit: "130.00" });
        return json(200, current);
      }
      return undefined;
    };
    renderAt(`/ledger/journal-batches/${BATCH}`);
    fireEvent.click(await screen.findByRole("button", { name: "Edit batch" }));
    const form = screen.getByRole("form", { name: "Edit journal batch" });
    expect(within(form).getByText(/Saving returns the batch to draft/)).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText("Line 1 amount"), { target: { value: "130.00" } });
    fireEvent.change(within(form).getByLabelText("Line 2 amount"), { target: { value: "130.00" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The batch changed since you opened it")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText("Changed elsewhere")).toBeInTheDocument());
    const first = server.calls.find((c) => c.method === "PATCH");
    expect(ifMatch(first!)).toBe('"2"');
    expect(first?.body).toEqual({
      description: "March accrual",
      postingDate: "2026-03-31",
      documentDate: "",
      lines: [
        { account: "6010", debitCredit: "D", amount: { currency: "USD", value: "130.00" }, dimensions: { DEPT: "D100" }, text: "Freight" },
        { account: "2100", debitCredit: "C", amount: { currency: "USD", value: "130.00" } },
      ],
    });

    fireEvent.click(within(screen.getByRole("form", { name: "Edit journal batch" })).getByRole("button", { name: "Save changes" }));
    // The reload made the batch draft at version 3; the second save carries that version.
    expect(await screen.findByText("Batch saved")).toBeInTheDocument();
    expect(screen.getByText("Corrected")).toBeInTheDocument();
    expect(ifMatch(server.calls.filter((c) => c.method === "PATCH")[1])).toBe('"3"');
  });

  it("shows the load error with a retry and refuses a malformed id", async () => {
    let fail = true;
    server.handler = () => (fail ? json(500, { code: "COM-0500", detail: "internal error" }) : json(200, batch({ status: "cancelled" })));
    renderAt(`/ledger/journal-batches/${BATCH}`);
    expect(await screen.findByText("Could not load the batch")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Cancelled batches can't be changed.")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Actions" })).toBeNull();
  });

  it("refuses a malformed id without calling the service", () => {
    renderAt("/ledger/journal-batches/not-an-id");
    expect(screen.getByText("The batch id in the link is not valid")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });
});
