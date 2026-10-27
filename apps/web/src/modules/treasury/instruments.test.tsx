import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Check, Facility, FacilityInstrument, Loan } from "./api/client";
import { header, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const ACCT = "0190f0a0-0000-7000-8000-000000000101";
const CHK = "0190f0a0-0000-7000-8000-000000000301";
const CHK2 = "0190f0a0-0000-7000-8000-000000000302";
const FAC = "0190f0a0-0000-7000-8000-000000000401";
const INS = "0190f0a0-0000-7000-8000-000000000501";
const LOAN = "0190f0a0-0000-7000-8000-000000000601";

const check: Check = {
  id: CHK,
  legalEntityId: LE,
  bankAccountId: ACCT,
  checkNumber: 1001,
  payee: "Acme Supplies",
  amount: "1500.00",
  currency: "USD",
  status: "printed",
  version: 2,
  createdAt: "2026-10-27T08:00:00Z",
  updatedAt: "2026-10-27T08:05:00Z",
};

const facility: Facility = {
  id: FAC,
  legalEntityId: LE,
  code: "LC-DB-01",
  bankName: "Deutsche Bank",
  currency: "EUR",
  limit: "1000000.00",
  utilized: "950000.00",
  headroom: "50000.00",
  expiryDate: "2030-12-31",
  status: "active",
  version: 1,
  createdAt: "2026-10-27T08:00:00Z",
};

const instrument: FacilityInstrument = {
  id: INS,
  facilityId: FAC,
  legalEntityId: LE,
  kind: "lc_import",
  reference: "LC-1",
  beneficiary: "Supplier GmbH",
  currency: "EUR",
  amount: "950000.00",
  issueDate: "2026-10-01",
  expiryDate: "2027-03-31",
  status: "open",
  limitOverride: false,
  version: 1,
  createdAt: "2026-10-01T08:00:00Z",
  updatedAt: "2026-10-01T08:00:00Z",
};

const loan: Loan = {
  id: LOAN,
  legalEntityId: LE,
  role: "lender",
  counterpartyName: "Passage Holdings BV",
  principal: "1000000",
  currency: "EUR",
  startDate: "2026-06-01",
  maturityDate: "2026-07-01",
  scheduleType: "Bullet",
  fixedRate: "0.03",
  dayCount: "ACT/360",
  periodMonths: 1,
  status: "active",
  version: 1,
  createdAt: "2026-10-27T08:00:00Z",
  schedule: [
    { periodNo: 1, periodStart: "2026-06-01", periodEnd: "2026-07-01", days: 30, openingPrincipal: "1000000", interest: "2500", principalRepayment: "1000000", closingPrincipal: "0" },
  ],
};

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("check register", () => {
  it("asks for a bank account, lists its checks and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/treasury/checks") return json(200, { items: [check] });
      if (url.pathname.endsWith(`/checks/${CHK}`)) return json(200, check);
      return undefined;
    };
    const { container } = renderAt("/treasury/checks");
    expect(screen.getByRole("heading", { name: "Choose a bank account" })).toBeInTheDocument();
    fill("Bank account id", "nope");
    fireEvent.click(screen.getByRole("button", { name: "Show checks" }));
    expect(await screen.findByText("Enter the bank account id (a UUID).")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);

    fill("Bank account id", ACCT);
    fireEvent.click(screen.getByRole("button", { name: "Show checks" }));
    const table = await findTable({ name: "Checks" });
    await waitFor(() => expect(table).toHaveTextContent("Acme Supplies"));
    expect(table).toHaveTextContent("1500.00 USD");
    expect(server.calls[0].url.searchParams.get("bankAccountId")).toBe(ACCT);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: "1001" }));
    expect(await screen.findByRole("heading", { name: "Check 1001" })).toBeInTheDocument();
  });

  it("shows the empty state, and an error with a retry", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt(`/treasury/checks?bankAccountId=${ACCT}`);
    expect(await screen.findByText("No checks yet")).toBeInTheDocument();
    first.unmount();

    server.handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt(`/treasury/checks?bankAccountId=${ACCT}`);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load the checks");
    server.handler = () => json(200, { items: [check] });
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Checks" })).toHaveTextContent("Acme Supplies");
  });
});

describe("issue check", () => {
  it("validates, posts with an Idempotency-Key and opens the new check", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname === "/api/v1/treasury/checks") return json(201, { ...check, status: "created", version: 1 });
      if (url.pathname.endsWith(`/checks/${CHK}`)) return json(200, { ...check, status: "created", version: 1 });
      return undefined;
    };
    const { container } = renderAt(`/treasury/checks/new?bankAccountId=${ACCT}`);
    expect(screen.getByLabelText("Bank account id")).toHaveValue(ACCT);
    fill("Payee", "Acme Supplies");
    fill("Amount", "1500.005");
    fill("Currency", "USD");
    fireEvent.click(screen.getByRole("button", { name: "Issue check" }));
    expect(await screen.findByText(/no more decimals than the currency allows/)).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fill("Amount", "1500.00");
    fireEvent.click(screen.getByRole("button", { name: "Issue check" }));
    expect(await screen.findByRole("heading", { name: "Check 1001" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ bankAccountId: ACCT, payee: "Acme Supplies", amount: "1500.00", currency: "USD" });
    expect(header(post, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("explains an inactive account and puts a currency mismatch on the field", async () => {
    server.handler = () => json(422, { code: "TRS-0412", detail: "checks can only be issued on an active bank account" });
    const view = renderAt(`/treasury/checks/new?bankAccountId=${ACCT}`);
    fill("Payee", "Acme");
    fill("Amount", "10");
    fill("Currency", "USD");
    fireEvent.click(screen.getByRole("button", { name: "Issue check" }));
    expect(await findAlert()).toHaveTextContent("The bank account is not active");
    view.unmount();

    server.handler = () => json(422, { code: "TRS-0426", detail: "check currency USD differs from the bank account currency EUR" });
    renderAt(`/treasury/checks/new?bankAccountId=${ACCT}`);
    fill("Payee", "Acme");
    fill("Amount", "10");
    fill("Currency", "USD");
    fireEvent.click(screen.getByRole("button", { name: "Issue check" }));
    expect(await screen.findByText(/differs from the bank account currency EUR/)).toBeInTheDocument();
  });
});

describe("check detail", () => {
  it("needs a reason to void, sends If-Match and reloads", async () => {
    let current: Check = check;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":void")) {
        current = { ...current, status: "voided", voidReason: "lost", version: 3 };
        return json(200, current);
      }
      if (url.pathname.endsWith(`/checks/${CHK}`)) return json(200, current);
      return undefined;
    };
    const { container } = renderAt(`/treasury/checks/${CHK}`);
    expect(await screen.findByRole("heading", { name: "Check 1001" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Mark sent" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Stop payment" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Void" }));
    expect(await screen.findByText("Enter why the check is voided, up to 200 characters.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);

    fill("Reason", "lost");
    fireEvent.click(screen.getByRole("button", { name: "Void" }));
    expect(await screen.findByText("Void: check 1001 is now voided.")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(header(post, "If-Match")).toBe('"2"');
    expect(post.body).toEqual({ reason: "lost" });
    await waitFor(() => expect(screen.getByText(/its number stays used/)).toBeInTheDocument());
  });

  it("reprints onto the next number and opens the new check", async () => {
    const fresh: Check = { ...check, id: CHK2, checkNumber: 1002, replacesId: CHK, version: 1 };
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":reprint")) return json(201, fresh);
      if (url.pathname.endsWith(`/checks/${CHK2}`)) return json(200, fresh);
      if (url.pathname.endsWith(`/checks/${CHK}`)) return json(200, check);
      return undefined;
    };
    renderAt(`/treasury/checks/${CHK}`);
    await screen.findByRole("heading", { name: "Check 1001" });
    fill("Reason", "printer jam");
    fireEvent.click(screen.getByRole("button", { name: "Reprint" }));
    expect(await screen.findByRole("heading", { name: "Check 1002" })).toBeInTheDocument();
    expect(screen.getByText(/check 1002 is printed with the same payee and amount/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: CHK })).toHaveAttribute("href", `/treasury/checks/${CHK}`);
  });

  it("explains a cleared check and a stale version", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(412, { code: "conflict_version", detail: "the resource changed" });
      if (url.pathname.endsWith(`/checks/${CHK}`)) return json(200, { ...check, status: "sent", version: 3 });
      return undefined;
    };
    const view = renderAt(`/treasury/checks/${CHK}`);
    await screen.findByRole("heading", { name: "Check 1001" });
    expect(screen.getByText(/also sends a void record to positive pay/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Mark cleared" }));
    expect(await findAlert()).toHaveTextContent("This check changed since you opened it");
    view.unmount();

    server.handler = (url) => (url.pathname.endsWith(`/checks/${CHK}`) ? json(200, { ...check, status: "cleared" }) : undefined);
    renderAt(`/treasury/checks/${CHK}`);
    expect(await screen.findByText(/it can no longer be voided/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Void" })).toBeNull();
  });

  it("shows a load error with a retry", async () => {
    server.handler = () => json(404, { code: "TRS-0423", detail: "check not found" });
    renderAt(`/treasury/checks/${CHK}`);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load the check");
    expect(alert).toHaveTextContent("TRS-0423");
  });
});

describe("facilities", () => {
  it("lists facilities with utilization and shows the empty state", async () => {
    server.handler = () => json(200, { items: [facility] });
    const { container } = renderAt(`/treasury/facilities?legalEntityId=${LE}`);
    const table = await findTable({ name: "Facilities" });
    await waitFor(() => expect(table).toHaveTextContent("LC-DB-01"));
    expect(table).toHaveTextContent("950000.00 EUR");
    expect(table).toHaveTextContent("50000.00 EUR");
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a facility and puts a duplicate code on its field", async () => {
    server.handler = (_url, init) => {
      if (init.method === "POST") return json(409, { code: "TRS-0427", detail: "a facility with this code already exists for the legal entity" });
      return undefined;
    };
    renderAt(`/treasury/facilities/new?legalEntityId=${LE}`);
    fill("Facility code", "LC-DB-01");
    fill("Bank", "Deutsche Bank");
    fill("Currency", "EUR");
    fill("Limit", "1000000.00");
    fill("Expiry date", "2030-12-31");
    fireEvent.click(screen.getByRole("button", { name: "Create facility" }));
    expect(await screen.findByText("a facility with this code already exists for the legal entity")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ legalEntityId: LE, code: "LC-DB-01", bankName: "Deutsche Bank", currency: "EUR", limit: "1000000.00", expiryDate: "2030-12-31" });
    expect(header(post, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("AC-36.6: explains a limit breach, then books with the override", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/instruments")) {
        const body = JSON.parse(String(init.body)) as { overrideLimit?: boolean };
        if (!body.overrideLimit) return json(422, { code: "TRS-0415", detail: "facility limit exceeded by 50000.00" });
        return json(201, { ...instrument, id: "0190f0a0-0000-7000-8000-000000000502", reference: "LC-2", amount: "100000.00", limitOverride: true });
      }
      if (url.pathname.endsWith("/instruments")) return json(200, { items: [instrument] });
      if (url.pathname.endsWith(`/facilities/${FAC}`)) return json(200, facility);
      return undefined;
    };
    const { container } = renderAt(`/treasury/facilities/${FAC}`);
    expect(await screen.findByRole("heading", { name: "Facility LC-DB-01" })).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Facility utilization" })).toHaveTextContent("50000.00 EUR");
    await waitFor(() => expect(screen.getByRole("table", { name: "Instruments" })).toHaveTextContent("LC-1"));
    expect(pageViolations(container)).toEqual([]);

    fill("Reference", "LC-2");
    fill("Beneficiary", "Supplier GmbH");
    fill("Amount (EUR)", "100000.00");
    fill("Issue date", "2026-10-27");
    fill("Expiry date", "2027-04-30");
    fireEvent.click(screen.getByRole("button", { name: "Book instrument" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The facility limit would be exceeded");
    expect(alert).toHaveTextContent("TRS-0415");
    const firstKey = header(server.calls.filter((c) => c.method === "POST")[0], "Idempotency-Key");

    fireEvent.change(screen.getByLabelText("Above the limit"), { target: { value: "yes" } });
    fireEvent.click(screen.getByRole("button", { name: "Book instrument" }));
    expect(await screen.findByText(/LC-2 is booked for 100000.00 EUR past the limit, under the override/)).toBeInTheDocument();
    const posts = server.calls.filter((c) => c.method === "POST");
    expect(posts[1].body).toMatchObject({ reference: "LC-2", currency: "EUR", amount: "100000.00", overrideLimit: true });
    expect(header(posts[1], "Idempotency-Key")).not.toBe(firstKey);
  });

  it("closes an open instrument under If-Match", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":close")) return json(200, { ...instrument, status: "closed", version: 2 });
      if (url.pathname.endsWith("/instruments")) return json(200, { items: [instrument] });
      if (url.pathname.endsWith(`/facilities/${FAC}`)) return json(200, facility);
      return undefined;
    };
    renderAt(`/treasury/facilities/${FAC}`);
    fireEvent.click(await screen.findByRole("button", { name: "Close LC-1" }));
    expect(await screen.findByText("LC-1 is closed and no longer counts against the limit.")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe(`/api/v1/treasury/facility-instruments/${INS}:close`);
    expect(header(post, "If-Match")).toBe('"1"');
  });

  it("shows a load error for a missing facility", async () => {
    server.handler = () => json(404, { code: "TRS-0428", detail: "facility not found" });
    renderAt(`/treasury/facilities/${FAC}`);
    expect(await findAlert()).toHaveTextContent("Could not load the facility");
  });
});

describe("loans", () => {
  it("lists loans and shows the empty and error states", async () => {
    server.handler = () => json(200, { items: [loan] });
    const first = renderAt(`/treasury/loans?legalEntityId=${LE}`);
    const table = await findTable({ name: "Loans" });
    await waitFor(() => expect(table).toHaveTextContent("Passage Holdings BV"));
    expect(table).toHaveTextContent("3%");
    expect(pageViolations(first.container)).toEqual([]);
    first.unmount();

    server.handler = () => json(200, { items: [] });
    const second = renderAt(`/treasury/loans?legalEntityId=${LE}`);
    expect(await screen.findByText("No loans yet")).toBeInTheDocument();
    second.unmount();

    server.handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt(`/treasury/loans?legalEntityId=${LE}`);
    expect(await findAlert()).toHaveTextContent("Could not load the loans");
  });

  it("creates a loan with the rate as a fraction and opens its schedule", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(201, loan);
      if (url.pathname.endsWith(`/loans/${LOAN}`)) return json(200, loan);
      return undefined;
    };
    const { container } = renderAt(`/treasury/loans/new?legalEntityId=${LE}`);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Role"), { target: { value: "lender" } });
    fill("Counterparty", "Passage Holdings BV");
    fill("Principal", "1000000");
    fill("Currency", "EUR");
    fill("Annual rate (%)", "3");
    fill("Start date", "2026-06-01");
    fill("Maturity date", "2026-07-01");
    fireEvent.change(screen.getByLabelText("Repayment"), { target: { value: "Bullet" } });
    fireEvent.click(screen.getByRole("button", { name: "Create loan and schedule" }));
    expect(await screen.findByRole("heading", { name: "Loan with Passage Holdings BV" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({
      legalEntityId: LE,
      role: "lender",
      counterpartyName: "Passage Holdings BV",
      principal: "1000000",
      currency: "EUR",
      startDate: "2026-06-01",
      maturityDate: "2026-07-01",
      scheduleType: "Bullet",
      fixedRate: "0.03",
      dayCount: "ACT/360",
      periodMonths: 1,
    });
    const schedule = await findTable({ name: "Loan schedule" });
    expect(schedule).toHaveTextContent("2500");
  });

  it("puts a schedule refusal on the maturity date", async () => {
    server.handler = () => json(422, { code: "TRS-0446", detail: "loan terms: maturity must fall on a whole number of periods after the start date" });
    renderAt(`/treasury/loans/new?legalEntityId=${LE}`);
    fill("Counterparty", "Bank");
    fill("Principal", "1000");
    fill("Currency", "EUR");
    fill("Annual rate (%)", "2.5");
    fill("Start date", "2026-01-01");
    fill("Maturity date", "2026-02-15");
    fireEvent.click(screen.getByRole("button", { name: "Create loan and schedule" }));
    expect(await screen.findByText(/whole number of periods/)).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "POST")!.body).toMatchObject({ fixedRate: "0.025" });
  });

  it("shows a load error for a missing loan", async () => {
    server.handler = () => json(404, { code: "TRS-0447", detail: "loan contract not found" });
    renderAt(`/treasury/loans/${LOAN}`);
    expect(await findAlert()).toHaveTextContent("Could not load the loan");
  });
});
