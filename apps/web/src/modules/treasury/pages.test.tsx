import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BankAccount, Forecast, Statement, StatementLine } from "./api/client";
import { header, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const GL = "0190f0a0-0000-7000-8000-0000000000a1";
const ACCT = "0190f0a0-0000-7000-8000-000000000101";
const STMT = "0190f0a0-0000-7000-8000-000000000201";

const account: BankAccount = {
  id: ACCT,
  legalEntityId: LE,
  accountCode: "OPS-01",
  ibanMasked: "DE****************3000",
  ibanLast4: "3000",
  bic: "COBADEFFXXX",
  currency: "EUR",
  mainAccountId: GL,
  status: "draft",
  version: 1,
  createdAt: "2026-10-27T08:00:00Z",
  updatedAt: "2026-10-27T08:00:00Z",
};

const statement: Statement = {
  id: STMT,
  bankAccountId: ACCT,
  legalEntityId: LE,
  statementId: "STMT-20261022",
  sequence: 412,
  format: "camt.053.001.08",
  currency: "EUR",
  openingDate: "2026-10-22",
  openingBalance: "1000.00",
  closingDate: "2026-10-22",
  closingBalance: "1337.50",
  lineCount: 2,
  createdAt: "2026-10-23T06:00:00Z",
};

function line(n: number): StatementLine {
  return { lineNo: n, bookingDate: "2026-10-22", valueDate: "2026-10-22", amount: "250.00", currency: "EUR", endToEndRef: `INV-${n}`, counterpartyName: "Northwind", status: "unmatched" };
}

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("bank accounts list", () => {
  it("asks for a legal entity first, then lists its accounts and opens one", async () => {
    server.handler = (url) => (url.pathname.endsWith("/bank-accounts") ? json(200, { items: [account] }) : undefined);
    const { container } = renderAt("/treasury");
    expect(screen.getByRole("heading", { name: "Choose a legal entity" })).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);

    fill("Legal entity id", "nope");
    fireEvent.click(screen.getByRole("button", { name: "Show accounts" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();

    fill("Legal entity id", LE);
    fireEvent.click(screen.getByRole("button", { name: "Show accounts" }));
    const table = await screen.findByRole("table", { name: "Bank accounts" });
    await waitFor(() => expect(table).toHaveTextContent("OPS-01"));
    expect(table).toHaveTextContent("DE****************3000");
    expect(table).toHaveTextContent("Draft");
    expect(server.calls[0].url.searchParams.get("legalEntityId")).toBe(LE);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state, and an error with a retry", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt(`/treasury?legalEntityId=${LE}`);
    expect(await screen.findByText("No bank accounts yet")).toBeInTheDocument();
    first.unmount();

    server.handler = () => json(503, { code: "COM-0500", detail: "internal error" });
    renderAt(`/treasury?legalEntityId=${LE}`);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load the bank accounts");
    expect(alert).toHaveTextContent("COM-0500");
    server.handler = () => json(200, { items: [account] });
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("table", { name: "Bank accounts" })).toHaveTextContent("OPS-01");
  });
});

describe("new bank account", () => {
  it("validates, posts the normalized account with an Idempotency-Key and opens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname === "/api/v1/treasury/bank-accounts") return json(201, account);
      if (url.pathname.endsWith(`/bank-accounts/${ACCT}`)) return json(200, account);
      if (url.pathname.endsWith("/statements")) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt(`/treasury/bank-accounts/new?legalEntityId=${LE}`);
    expect(screen.getByLabelText("Legal entity id")).toHaveValue(LE);
    fireEvent.click(screen.getByRole("button", { name: "Create bank account" }));
    expect(await screen.findByText("Enter a valid IBAN; the check digits do not match.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fill("Account code", "OPS-01");
    fill("IBAN", "DE89 3704 0044 0532 0130 00");
    fill("Currency", "EUR");
    fill("GL main account id", GL);
    fireEvent.click(screen.getByRole("button", { name: "Create bank account" }));
    expect(await screen.findByRole("heading", { name: "Bank account OPS-01" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ legalEntityId: LE, accountCode: "OPS-01", iban: "DE89370400440532013000", currency: "EUR", mainAccountId: GL });
    expect(header(post, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("sends the optional statement account number normalized and shows only its last four characters", async () => {
    const created = { ...account, statementAccountLast4: "6789" };
    server.handler = (url, init) => {
      if (init.method === "POST") return json(201, created);
      if (url.pathname.endsWith(`/bank-accounts/${ACCT}`)) return json(200, created);
      if (url.pathname.endsWith("/statements")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/treasury/bank-accounts/new?legalEntityId=${LE}`);
    fill("Account code", "US-01");
    fill("IBAN", "DE89370400440532013000");
    fill("Currency", "USD");
    fill("GL main account id", GL);
    fill("Statement account number", "12#45");
    fireEvent.click(screen.getByRole("button", { name: "Create bank account" }));
    expect(await screen.findByText("Use up to 35 characters: A to Z, 0 to 9, slash, dot or hyphen.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);

    fill("Statement account number", " 1234 56789 ");
    fireEvent.click(screen.getByRole("button", { name: "Create bank account" }));
    expect(await screen.findByRole("heading", { name: "Bank account OPS-01" })).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "POST")!.body).toMatchObject({ statementAccount: "123456789" });
    expect(await screen.findByText("ending 6789")).toBeInTheDocument();
  });

  it("shows a duplicate refusal and puts field errors next to their fields", async () => {
    server.handler = () => json(409, { code: "TRS-0404", detail: "a bank account with this code or IBAN already exists" });
    renderAt(`/treasury/bank-accounts/new?legalEntityId=${LE}`);
    fill("Account code", "OPS-01");
    fill("IBAN", "DE89370400440532013000");
    fill("Currency", "EUR");
    fill("GL main account id", GL);
    fireEvent.click(screen.getByRole("button", { name: "Create bank account" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("This account already exists");
    const firstKey = header(server.calls[0], "Idempotency-Key");

    server.handler = () => json(400, { code: "COM-0001", detail: "request failed validation", errors: { currency: "must be an ISO 4217 code" } });
    fireEvent.click(screen.getByRole("button", { name: "Create bank account" }));
    expect(await screen.findByText("must be an ISO 4217 code")).toBeInTheDocument();
    // A refused create is final, so the next attempt uses a new key.
    expect(header(server.calls[1], "Idempotency-Key")).not.toBe(firstKey);
  });
});

describe("bank account detail", () => {
  it("activates with If-Match, then offers deactivation and statement import", async () => {
    let current = account;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":activate")) {
        current = { ...account, status: "active", version: 2 };
        return json(200, current);
      }
      if (url.pathname.endsWith(`/bank-accounts/${ACCT}`)) return json(200, current);
      if (url.pathname.endsWith(`/bank-accounts/${ACCT}/statements`)) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt(`/treasury/bank-accounts/${ACCT}`);
    expect(await screen.findByRole("heading", { name: "Bank account OPS-01" })).toBeInTheDocument();
    expect(await screen.findByText("Activate the account to import statements.")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Import statement" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Activate" }));
    expect(await screen.findByRole("button", { name: "Deactivate" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe(`/api/v1/treasury/bank-accounts/${ACCT}:activate`);
    expect(header(post, "If-Match")).toBe('"1"');
    expect(screen.getByRole("status")).toHaveTextContent("the account is now active");
    expect(screen.getByRole("link", { name: "Import statement" })).toHaveAttribute("href", `/treasury/statements/import?bankAccountId=${ACCT}`);
  });

  it("explains a stale version and reloads the account", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(412, { code: "conflict_version", detail: "the bank account changed; reload and retry" });
      if (url.pathname.endsWith(`/bank-accounts/${ACCT}`)) return json(200, account);
      return json(200, { items: [statement] });
    };
    renderAt(`/treasury/bank-accounts/${ACCT}`);
    const table = await screen.findByRole("table", { name: "Statements" });
    await waitFor(() => expect(table).toHaveTextContent("STMT-20261022"));
    expect(table).toHaveTextContent("1337.50 EUR");
    const gets = server.calls.filter((c) => c.url.pathname.endsWith(`/bank-accounts/${ACCT}`)).length;
    fireEvent.click(screen.getByRole("button", { name: "Activate" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("This account changed since you opened it");
    await waitFor(() => expect(server.calls.filter((c) => c.url.pathname.endsWith(`/bank-accounts/${ACCT}`)).length).toBe(gets + 1));
  });

  it("shows a load failure for an unknown account", async () => {
    server.handler = () => json(404, { code: "TRS-0400", detail: "bank account not found" });
    renderAt(`/treasury/bank-accounts/${ACCT}`);
    expect(await screen.findByRole("alert")).toHaveTextContent("TRS-0400");
  });
});

describe("statement import", () => {
  function choose(file: File) {
    fireEvent.change(screen.getByLabelText("Statement file"), { target: { files: [file] } });
  }

  it("uploads the file as multipart and reports the new statement with its warnings", async () => {
    server.handler = () => json(201, { ...statement, warnings: [{ code: "TRS-0407", detail: "statement sequence gap: expected 411, received 412" }] });
    const { container } = renderAt(`/treasury/statements/import?bankAccountId=${ACCT}`);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Import statement" }));
    expect(await screen.findByText("Choose the camt.053, BAI2 or MT940 file from the bank.")).toBeInTheDocument();
    expect(screen.getByLabelText("Statement file")).toHaveAttribute("accept", expect.stringContaining(".bai2"));

    choose(new File(["<Document/>"], "stmt.xml", { type: "text/xml" }));
    fireEvent.click(screen.getByRole("button", { name: "Import statement" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Statement STMT-20261022: 2 entries, closing balance 1337.50 EUR.");
    expect(screen.getByText(/expected 411, received 412 \(TRS-0407\)/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open statement" })).toHaveAttribute("href", `/treasury/statements/${STMT}`);
    const form = server.calls[0].body as FormData;
    expect(form.get("bankAccountId")).toBe(ACCT);
    expect((form.get("file") as File).name).toBe("stmt.xml");
    expect(header(server.calls[0], "Content-Type")).toBeNull();
  });

  it("tells a re-upload apart from a new import", async () => {
    server.handler = () => json(200, { ...statement, warnings: [] });
    renderAt(`/treasury/statements/import?bankAccountId=${ACCT}`);
    choose(new File(["<Document/>"], "stmt.xml"));
    fireEvent.click(screen.getByRole("button", { name: "Import statement" }));
    expect(await screen.findByRole("status")).toHaveTextContent("imported before as statement STMT-20261022; nothing was added (TRS-0405)");
  });

  it("names the failing entry of an unreadable file and the refusals a user can act on", async () => {
    server.handler = () => json(422, { code: "TRS-0493", detail: "amount must be a non-negative decimal", errors: { entry: "7" } });
    renderAt(`/treasury/statements/import?bankAccountId=${ACCT}`);
    choose(new File(["<Document/>"], "stmt.xml"));
    fireEvent.click(screen.getByRole("button", { name: "Import statement" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The file is not a readable camt.053, BAI2 or MT940 statement");
    expect(alert).toHaveTextContent("(entry 7)");

    server.handler = () => json(409, { code: "TRS-0406", detail: "statement exists with a different closing balance" });
    fireEvent.click(screen.getByRole("button", { name: "Import statement" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("already imported with another closing balance"));
  });
});

describe("statement import of BAI2 and MT940", () => {
  it("uploads a BAI2 file, shows its format, and names the failing line of a text file", async () => {
    server.handler = () => json(201, { ...statement, statementId: "123456789-2024-10-01", format: "BAI2", currency: "USD", closingBalance: "925.00", warnings: [] });
    renderAt(`/treasury/statements/import?bankAccountId=${ACCT}`);
    fireEvent.change(screen.getByLabelText("Statement file"), { target: { files: [new File(["01,BANK,CO/"], "stmt.bai2", { type: "text/plain" })] } });
    fireEvent.click(screen.getByRole("button", { name: "Import statement" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Statement 123456789-2024-10-01");
    expect((server.calls[0].body as FormData).get("file")).toHaveProperty("name", "stmt.bai2");

    server.handler = () => json(422, { code: "TRS-0493", detail: ":61: entry does not match", errors: { line: "12" } });
    fireEvent.click(screen.getByRole("button", { name: "Import statement" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(":61: entry does not match (line 12)");

    server.handler = () => json(422, { code: "TRS-0409", detail: "the statement belongs to another account" });
    fireEvent.click(screen.getByRole("button", { name: "Import statement" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("not this bank account's IBAN or statement account number"));
  });
});

describe("statement detail", () => {
  it("shows balances and pages through the entries", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/lines")) {
        const after = Number(url.searchParams.get("afterLineNo"));
        return json(200, after === 0 ? { items: [line(1)], nextAfterLineNo: 1 } : { items: [line(2)], nextAfterLineNo: null });
      }
      return json(200, statement);
    };
    const { container } = renderAt(`/treasury/statements/${STMT}`);
    expect(await screen.findByRole("heading", { name: "Statement STMT-20261022" })).toBeInTheDocument();
    expect(screen.getByText("1337.50 EUR on 2026-10-22")).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Statement entries" });
    expect(table).toHaveTextContent("INV-1");
    expect(screen.getByText("Showing 1 of 2 entries.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Load more entries" }));
    await waitFor(() => expect(table).toHaveTextContent("INV-2"));
    expect(screen.getByText("Showing 2 of 2 entries.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more entries" })).not.toBeInTheDocument();
    expect(server.calls.at(-1)!.url.searchParams.get("afterLineNo")).toBe("1");
  });

  it("shows a load failure with a retry", async () => {
    server.handler = () => json(404, { code: "TRS-0413", detail: "statement not found" });
    renderAt(`/treasury/statements/${STMT}`);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("TRS-0413");
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("cash forecast", () => {
  const forecast: Forecast = {
    layout: "weekly",
    asOf: "2026-10-20",
    outOfHorizon: 3,
    buckets: [{ key: "2026-10-20", currency: "EUR", inflow: "100.00", outflow: "-40.00", net: "60.00", cumulative: "60.00" }],
  };

  it("loads buckets for the chosen filters and adds a manual entry with a key", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(201, { id: "e1", legalEntityId: LE, sourceType: "Manual", sourceId: "x", expectedDate: "2026-10-25", amount: "-40.00", currency: "EUR", confidence: "1" });
      if (url.pathname.endsWith("/cash-forecast")) return json(200, forecast);
      return undefined;
    };
    const { container } = renderAt("/treasury/cash-forecast");
    expect(screen.getByRole("heading", { name: "Choose a legal entity" })).toBeInTheDocument();
    fill("Legal entity id", LE);
    fireEvent.change(screen.getByLabelText("Buckets"), { target: { value: "weekly" } });
    fill("As of", "2026-10-20");
    fireEvent.click(screen.getByRole("button", { name: "Show forecast" }));
    const table = await screen.findByRole("table", { name: "Forecast buckets" });
    await waitFor(() => expect(table).toHaveTextContent("60.00 EUR"));
    expect(table).toHaveTextContent("-40.00 EUR");
    expect(screen.getByText("3 entries fall after the last bucket and are not shown.")).toBeInTheDocument();
    const get = server.calls[0];
    expect(Object.fromEntries(get.url.searchParams)).toEqual({ legalEntityId: LE, layout: "weekly", asOf: "2026-10-20" });
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Add entry" }));
    expect(await screen.findByText("Enter the expected date.")).toBeInTheDocument();
    fill("Expected date", "2026-10-25");
    fill("Amount", "-40.00");
    fill("Entry currency", "EUR");
    fireEvent.click(screen.getByRole("button", { name: "Add entry" }));
    await waitFor(() => expect(screen.getAllByRole("status").some((s) => s.textContent === "Added -40.00 EUR expected on 2026-10-25.")).toBe(true));
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ legalEntityId: LE, sourceType: "Manual", expectedDate: "2026-10-25", amount: "-40.00", currency: "EUR" });
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    // The forecast reloads after the entry is stored.
    await waitFor(() => expect(server.calls.filter((c) => c.url.pathname.endsWith("/cash-forecast")).length).toBe(2));
  });

  it("shows the empty state and an error with a retry", async () => {
    server.handler = () => json(200, { ...forecast, buckets: [], outOfHorizon: 0 });
    const first = renderAt(`/treasury/cash-forecast?legalEntityId=${LE}`);
    expect(await screen.findByText("No expected cash flows")).toBeInTheDocument();
    first.unmount();

    server.handler = () => json(400, { code: "COM-0001", detail: "request failed validation" });
    renderAt(`/treasury/cash-forecast?legalEntityId=${LE}`);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load the cash forecast");
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
