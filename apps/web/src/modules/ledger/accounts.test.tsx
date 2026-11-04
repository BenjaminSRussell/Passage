import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { exportAccountsCsv, exportJournalLinesCsv, exportTrialBalanceCsv, type Account } from "./api/client";
import { emptyAccount, toAccountInput, validateAccount } from "./AccountFormPage";
import { ExportButton } from "./ExportButton";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";

function account(patch: Partial<Account> = {}): Account {
  return {
    id: "0190a000-0000-7000-8000-0000000000f1",
    chartCode: "STD",
    accountNumber: "6010",
    name: "Freight expense",
    accountType: "expense",
    isPostable: true,
    reconciliationAccountType: "none",
    lockVersion: 3,
    ...patch,
  };
}

const WRITE = ["ledger.account.write"];

describe("account validation", () => {
  it("mirrors the service checks", () => {
    expect(validateAccount(emptyAccount("STD"))).toEqual({ number: "Use 1 to 20 letters, digits, dots, underscores or dashes.", name: "Enter a name of up to 120 characters." });
    const bad = { ...emptyAccount("std"), number: "6010", name: "x", accountType: "reporting" as const, suspendedFrom: "June" };
    expect(Object.keys(validateAccount(bad)).sort()).toEqual(["chart", "isPostable", "suspendedFrom"]);
    expect(toAccountInput({ ...emptyAccount("STD"), number: "6010", name: " Freight ", suspendedFrom: "2026-12-31" })).toEqual({
      name: "Freight",
      accountType: "expense",
      isPostable: true,
      reconciliationAccountType: "none",
      suspendedFrom: "2026-12-31",
    });
  });
});

describe("chart of accounts page", () => {
  it("asks for a chart code and refuses a malformed one without calling the API", async () => {
    renderAt("/ledger/accounts");
    expect(screen.getByText("Enter a chart code to list its accounts.")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New account" })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Chart code/), { target: { value: "bad code!" } });
    fireEvent.click(screen.getByRole("button", { name: "Show accounts" }));
    expect(await screen.findByText("Use 1 to 10 capital letters or digits, such as STD.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("lists the accounts, pages with the after cursor and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/ledger/charts/STD/accounts") {
        return url.searchParams.get("after") === "6010"
          ? json(200, { items: [account({ accountNumber: "7000", name: "Totals", accountType: "reporting", isPostable: false })], nextAfter: "" })
          : json(200, { items: [account(), account({ accountNumber: "2110", name: "Vendor control", accountType: "liability", reconciliationAccountType: "vendor", suspendedFrom: "2026-06-01" })], nextAfter: "6010" });
      }
      if (url.pathname === "/api/v1/ledger/charts/STD/accounts/6010") return json(200, account());
      return undefined;
    };
    const { container } = renderAt("/ledger/accounts?chart=STD", WRITE);
    const table = await findTable({ name: "Chart of accounts" });
    await waitFor(() => expect(within(table).getByText("Vendor control")).toBeInTheDocument());
    expect(server.calls[0].url.searchParams.get("limit")).toBe("200");
    expect(within(table).getByText("Vendor")).toBeInTheDocument();
    expect(within(table).getByText("2026-06-01")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New account" })).toHaveAttribute("href", "/ledger/accounts/new?chart=STD");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Load more accounts" }));
    await waitFor(() => expect(within(table).getByText("Totals")).toBeInTheDocument());
    expect(server.calls.at(-1)?.url.searchParams.get("after")).toBe("6010");
    expect(screen.queryByRole("button", { name: "Load more accounts" })).not.toBeInTheDocument();

    fireEvent.click(within(table).getByText("6010"));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/accounts/STD/6010"));
  });

  it("shows the empty state and the error with a retry", async () => {
    server.handler = () => json(200, { items: [], nextAfter: "" });
    const first = renderAt("/ledger/accounts?chart=EMPTY");
    expect(await screen.findByText("No accounts in this chart")).toBeInTheDocument();
    first.unmount();

    server.handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderAt("/ledger/accounts?chart=STD");
    expect((await findAlert()).textContent).toContain("boom");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("account form", () => {
  it("creates an account without If-Match", async () => {
    server.handler = (_url, init) => (init.method === "PUT" ? json(201, account({ accountNumber: "6020", name: "Packaging", lockVersion: 1 })) : json(200, { items: [], nextAfter: "" }));
    renderAt("/ledger/accounts/new?chart=STD", WRITE);
    fireEvent.change(screen.getByLabelText(/Account number/), { target: { value: "6020" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Packaging" } });
    fireEvent.click(screen.getByRole("button", { name: "Save account" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "PUT")).toBe(true));
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toBe("/api/v1/ledger/charts/STD/accounts/6020");
    expect(new Headers(put?.init.headers).get("If-Match")).toBeNull();
    expect(put?.body).toEqual({ name: "Packaging", accountType: "expense", isPostable: true, reconciliationAccountType: "none" });
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/accounts"));
  });

  it("names an existing number when a create is answered 428 (no If-Match on a create)", async () => {
    server.handler = (_url, init) =>
      init.method === "PUT" ? json(428, { code: "LEDGER_PRECONDITION_REQUIRED", detail: "send If-Match with the account entity tag" }) : undefined;
    renderAt("/ledger/accounts/new?chart=STD", WRITE);
    fireEvent.change(screen.getByLabelText(/Account number/), { target: { value: "6010" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Freight" } });
    fireEvent.click(screen.getByRole("button", { name: "Save account" }));
    const alert = await findAlert();
    expect(alert.textContent).toContain("Account 6010 already exists in chart STD");
    expect(alert.textContent).not.toContain("Someone changed");
    expect(screen.getByTestId("location").textContent).toBe("/ledger/accounts/new");
  });

  it("loads the account, shows it read-only without the duty and keeps the number fixed", async () => {
    server.handler = () => json(200, account());
    const { container } = renderAt("/ledger/accounts/STD/6010");
    await screen.findByDisplayValue("Freight expense");
    expect(screen.getByLabelText(/Account number/)).toHaveAttribute("readonly");
    expect(screen.queryByRole("button", { name: "Save account" })).not.toBeInTheDocument();
    expect(screen.getByText(/needs the maintain accounts duty/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("replaces with the lockVersion it read as If-Match and explains a stale tag", async () => {
    let status = 200;
    server.handler = (_url, init) => (init.method === "PUT" ? (status === 200 ? json(200, account({ lockVersion: 4 })) : json(412, { code: "LEDGER_VERSION_CONFLICT", detail: "stale" })) : json(200, account()));
    renderAt("/ledger/accounts/STD/6010", WRITE);
    const name = await screen.findByDisplayValue("Freight expense");
    fireEvent.change(name, { target: { value: "Freight and duty" } });
    status = 412;
    fireEvent.click(screen.getByRole("button", { name: "Save account" }));
    expect((await findAlert()).textContent).toContain("Someone changed this account since you opened it");
    const put = server.calls.find((c) => c.method === "PUT");
    expect(new Headers(put?.init.headers).get("If-Match")).toBe('"3"');
    expect(put?.body).toMatchObject({ name: "Freight and duty" });
  });

  it("shows a 404 as a load error", async () => {
    server.handler = () => json(404, { code: "LEDGER_ACCOUNT_NOT_FOUND", detail: "account not found" });
    renderAt("/ledger/accounts/STD/9999");
    expect((await findAlert()).textContent).toContain("account not found");
  });
});

describe("CSV exports", () => {
  const csvResponse = (extra: Record<string, string> = {}) =>
    new Response("a,b\n1,2\n", { status: 200, headers: { "Content-Type": "text/csv", "Content-Disposition": 'attachment; filename="ledger-lines.csv"', ...extra } });

  it("asks for format=csv with the same filter and without paging", async () => {
    const seen: URL[] = [];
    const fetchImpl = async (input: string) => {
      seen.push(new URL(input, "http://localhost"));
      return csvResponse();
    };
    const file = await exportJournalLinesCsv({ ledgerId: LEDGER, accountFrom: "6000", dimensions: { DEPT: "D100" }, cursor: "7.1", limit: 100 }, fetchImpl);
    await exportTrialBalanceCsv({ ledgerId: LEDGER, periodFrom: "2026-01", groupBy: ["DEPT"] }, fetchImpl);
    await exportAccountsCsv("STD", fetchImpl);
    expect(file.filename).toBe("ledger-lines.csv");
    expect(file.truncated).toBe(false);
    expect(Object.fromEntries(seen[0].searchParams)).toEqual({ ledger: LEDGER, account_from: "6000", "dim.DEPT": "D100", format: "csv" });
    expect(Object.fromEntries(seen[1].searchParams)).toEqual({ ledger: LEDGER, period_from: "2026-01", group_by: "DEPT", format: "csv" });
    expect(seen[2].pathname).toBe("/api/v1/ledger/charts/STD/accounts");
    expect(seen[2].searchParams.get("format")).toBe("csv");
  });

  it("reports a truncated export and turns a problem into an error", async () => {
    const file = await exportAccountsCsv("STD", async () => csvResponse({ "X-Passage-Export-Truncated": "true" }));
    expect(file.truncated).toBe(true);
    await expect(exportAccountsCsv("STD", async () => new Response(JSON.stringify({ code: "LEDGER_NOT_FOUND", detail: "ledger not found" }), { status: 404 }))).rejects.toMatchObject({ code: "LEDGER_NOT_FOUND" });
  });
});

describe("export button", () => {
  let created: Blob[];
  beforeEach(() => {
    created = [];
    URL.createObjectURL = vi.fn((b: Blob) => {
      created.push(b);
      return "blob:test";
    });
    URL.revokeObjectURL = vi.fn();
  });
  afterEach(() => vi.restoreAllMocks());

  it("downloads the file, and warns when the export was cut", async () => {
    const run = vi.fn(async () => ({ blob: new Blob(["x"]), filename: "a.csv", truncated: true }));
    render(<ExportButton run={run} />);
    fireEvent.click(screen.getByRole("button", { name: "Export CSV" }));
    await waitFor(() => expect(created).toHaveLength(1));
    expect(await screen.findByText("The export is incomplete")).toBeInTheDocument();
  });

  it("shows the failure in place", async () => {
    render(<ExportButton run={async () => Promise.reject(new Error("offline"))} />);
    fireEvent.click(screen.getByRole("button", { name: "Export CSV" }));
    expect((await findAlert()).textContent).toContain("offline");
    expect(created).toHaveLength(0);
  });
});

describe("exports on the inquiry screens", () => {
  beforeEach(() => {
    URL.createObjectURL = vi.fn(() => "blob:test");
    URL.revokeObjectURL = vi.fn();
  });

  it("exports the trial balance with the filter on screen", async () => {
    server.handler = (url) =>
      url.pathname === "/api/v1/ledger/balances"
        ? json(200, { ledgerId: LEDGER, currency: "USD", rows: [], debit: "0.00", credit: "0.00", net: "0.00", balanced: true })
        : undefined;
    renderAt("/ledger/trial-balance");
    expect(screen.queryByRole("button", { name: /Export trial balance/ })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.change(screen.getByLabelText(/Period from/), { target: { value: "2026-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Show trial balance" }));
    fireEvent.click(await screen.findByRole("button", { name: "Export trial balance (CSV)" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("format") === "csv")).toBe(true));
    const call = server.calls.find((c) => c.url.searchParams.get("format") === "csv");
    expect(call?.url.searchParams.get("ledger")).toBe(LEDGER);
    expect(call?.url.searchParams.get("period_from")).toBe("2026-01");
  });

  it("exports all lines of the filter, not the page on screen", async () => {
    const row = {
      id: "l1", entryId: "0190a000-0000-7000-8000-0000000000b1", voucher: "GL-1", postingSequence: 1, postingDate: "2026-03-01", lineNumber: 1, account: "6010", displayName: "6010",
      debitCredit: "D", amount: "10.00", amountAcct: "10.00", currency: "USD", negativePosting: false, sourceSystem: "ap", sourceType: "invoice", sourceId: "x", isRealignment: false, traceId: "t",
    };
    server.handler = (url) => (url.pathname === "/api/v1/ledger/journal-lines" ? json(200, { items: [row], nextCursor: "1.1" }) : undefined);
    renderAt(`/ledger/transactions?ledger=${LEDGER}`);
    fireEvent.click(await screen.findByRole("button", { name: "Export lines (CSV)" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("format") === "csv")).toBe(true));
    const call = server.calls.find((c) => c.url.searchParams.get("format") === "csv");
    expect(call?.url.searchParams.has("cursor")).toBe(false);
    expect(call?.url.searchParams.has("limit")).toBe(false);
  });
});
