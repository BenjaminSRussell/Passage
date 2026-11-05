import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { CollectionCase, ConfirmationRun, DownPayment } from "./api/client";
import { toCaseInput, validateCase } from "./Cases";
import { toReplyInput, toRunInput, validateConfirmationRun, validateReply } from "./Confirmations";
import { toDownPaymentInput, validateClearing, validateDownPayment } from "./DownPayments";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "0190f0a0-0000-7000-8000-0000000000c1";
const ITEM = "0190f0a0-0000-7000-8000-000000000401";
const CASE = "0190f0a0-0000-7000-8000-000000000a01";
const RUN = "0190f0a0-0000-7000-8000-000000000b01";
const CONF = "0190f0a0-0000-7000-8000-000000000b11";
const CONF2 = "0190f0a0-0000-7000-8000-000000000b12";
const DP = "0190f0a0-0000-7000-8000-000000000c01";

const openCase: CollectionCase = {
  id: CASE,
  legalEntityId: LE,
  customerId: CUST,
  category: "COLLECTION",
  owner: "ana",
  priority: "HIGH",
  status: "OPEN",
  reason: "Overdue 60 days",
  currency: "USD",
  openItemId: ITEM,
  documentNumber: "INV-30001",
  attachedAmount: "1200.00",
  slaHours: 72,
  slaDue: "2027-03-08T12:00:00Z",
  slaBreached: false,
  createdAt: "2027-03-05T12:00:00Z",
  version: 1,
};

const run: ConfirmationRun = {
  id: RUN,
  legalEntityId: LE,
  asOfDate: "2027-03-31",
  threshold: "10000",
  includeZeroBalances: false,
  reminderDays: 14,
  createdAt: "2027-04-01T09:00:00Z",
  counts: { selected: 2, agreed: 0, disagreed: 1, noReply: 1 },
  confirmations: [
    { id: CONF, runId: RUN, legalEntityId: LE, customerId: CUST, currency: "USD", ourBalance: "12000", status: "NO_REPLY" },
    {
      id: CONF2,
      runId: RUN,
      legalEntityId: LE,
      customerId: "0190f0a0-0000-7000-8000-0000000000c2",
      currency: "USD",
      ourBalance: "15000",
      status: "DISAGREED",
      customerBalance: "14750",
      difference: "-250",
      comment: "credit note missing",
      repliedAt: "2027-04-02T10:00:00Z",
      caseId: CASE,
    },
  ],
};

const downPayment: DownPayment = {
  id: DP,
  legalEntityId: LE,
  customerId: CUST,
  salesOrderId: null,
  kind: "DOWN_PAYMENT",
  currency: "USD",
  amount: "2000.00",
  clearedAmount: "0",
  remaining: "2000.00",
  status: "REQUESTED",
  createdAt: "2027-03-01T09:00:00Z",
  version: 1,
  clearings: [],
};

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

function ifMatch(path: string): string | null {
  return new Headers(server.calls.find((c) => c.url.pathname.endsWith(path))!.init.headers).get("If-Match");
}

describe("Collection cases", () => {
  it("lists open cases, filters by category and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/cases")) return json(200, { items: [openCase, { ...openCase, id: "c2", slaBreached: true, documentNumber: null }] });
      if (url.pathname.endsWith(`/cases/${CASE}`)) return json(200, openCase);
      return undefined;
    };
    const { container } = renderAt("/receivables/cases");
    const table = await findTable({ name: "Collection cases" });
    await waitFor(() => expect(table).toHaveTextContent("1200.00 USD"));
    expect(table).toHaveTextContent("SLA breached");
    expect(table).toHaveTextContent("High");
    expect(server.calls[0].url.searchParams.get("status")).toBe("OPEN");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Category"), { target: { value: "DISPUTE" } });
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("category") === "DISPUTE")).toBe(true));
    fireEvent.click(within(await findTable({ name: "Collection cases" })).getAllByRole("button", { name: "Collection" })[0]);
    expect(await screen.findByRole("heading", { name: "Collection case" })).toBeInTheDocument();
  });

  it("shows the empty and error states", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/receivables/cases");
    expect(await screen.findByText("No cases")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty receivables.view" });
    renderAt("/receivables/cases");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load the cases");
    expect(alert).toHaveTextContent("AUTH-0003");
  });

  it("opens a case from an open item with an Idempotency-Key", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/cases") && init.method === "POST") return json(201, openCase);
      if (url.pathname.endsWith(`/cases/${CASE}`)) return json(200, openCase);
      return undefined;
    };
    const { container } = renderAt(`/receivables/cases/new?legalEntity=${LE}&customer=${CUST}&openItem=${ITEM}`);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Open case" }));
    expect(await screen.findByText("Enter who owns the case (up to 120 characters).")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Priority"), { target: { value: "HIGH" } });
    fill("Owner", "ana");
    fill("Reason", "Overdue 60 days");
    fireEvent.click(screen.getByRole("button", { name: "Open case" }));
    expect(await screen.findByRole("heading", { name: "Collection case" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(idempotencyKey(post)).toBeTruthy();
    expect(post.body).toEqual({
      legalEntityId: LE,
      customerId: CUST,
      category: "COLLECTION",
      priority: "HIGH",
      owner: "ana",
      reason: "Overdue 60 days",
      openItemId: ITEM,
      attachedAmount: null,
    });
  });

  it("assigns and closes with If-Match, and explains a stale version", async () => {
    let current = openCase;
    server.handler = (url, init) => {
      if (url.pathname.endsWith(":assign")) {
        current = { ...current, owner: "ben", version: 2 };
        return json(200, current);
      }
      if (url.pathname.endsWith(":close")) return json(412, { code: "conflict_version", detail: "the case changed since it was read" });
      if (url.pathname.endsWith(`/cases/${CASE}`) && (init.method ?? "GET") === "GET") return json(200, current);
      return undefined;
    };
    const { container } = renderAt(`/receivables/cases/${CASE}`);
    expect(await screen.findByText("Overdue 60 days")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Assign" }));
    expect(await screen.findByText("Enter the new owner (up to 120 characters).")).toBeInTheDocument();
    fill("New owner", "ben");
    fireEvent.click(screen.getByRole("button", { name: "Assign" }));
    expect(await screen.findByText("The case now belongs to ben.")).toBeInTheDocument();
    expect(ifMatch(":assign")).toBe('"1"');
    expect(server.calls.find((c) => c.url.pathname.endsWith(":assign"))!.body).toEqual({ owner: "ben" });

    await waitFor(() => expect(screen.getAllByText("ben").length).toBeGreaterThan(0));
    fill("Close note", "paid in full");
    fireEvent.click(screen.getByRole("button", { name: "Close case" }));
    expect(await screen.findByText(/The case changed since you opened it/)).toBeInTheDocument();
    expect(ifMatch(":close")).toBe('"2"');
    expect(server.calls.find((c) => c.url.pathname.endsWith(":close"))!.body).toEqual({ note: "paid in full" });
  });

  it("warns about a breached SLA and hides the actions on a closed case", async () => {
    server.handler = () => json(200, { ...openCase, slaBreached: true });
    const first = renderAt(`/receivables/cases/${CASE}`);
    expect(await screen.findByText(/passed its 72-hour SLA/)).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(200, { ...openCase, status: "CLOSED", closedAt: "2027-03-06T10:00:00Z", closeNote: "paid" });
    renderAt(`/receivables/cases/${CASE}`);
    expect(await screen.findByText("This case is closed.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close case" })).toBeNull();
  });

  it("validates a draft like the service does", () => {
    const draft = { legalEntityId: LE, customerId: CUST, category: "DISPUTE" as const, priority: "NORMAL" as const, owner: "ana", reason: "r", openItemId: "", currency: "USD", attachedAmount: "" };
    expect(validateCase(draft)).toEqual({});
    expect(Object.keys(validateCase({ ...draft, currency: "", attachedAmount: "1e3", owner: "" })).sort()).toEqual(["attachedAmount", "currency", "owner"]);
    expect(validateCase({ ...draft, currency: "", openItemId: ITEM })).toEqual({});
    expect(toCaseInput({ ...draft, currency: "", openItemId: ITEM })).not.toHaveProperty("currency");
  });
});

describe("Balance confirmations", () => {
  it("lists runs with their counts and shows the empty and error states", async () => {
    server.handler = () => json(200, { items: [{ ...run, confirmations: undefined }] });
    const first = renderAt("/receivables/balance-confirmations");
    const table = await findTable({ name: "Balance confirmation runs" });
    await waitFor(() => expect(table).toHaveTextContent("2027-03-31"));
    expect(pageViolations(first.container)).toEqual([]);
    first.unmount();
    server.handler = () => json(200, { items: [] });
    const second = renderAt("/receivables/balance-confirmations");
    expect(await screen.findByText("No confirmation runs")).toBeInTheDocument();
    second.unmount();
    server.handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/receivables/balance-confirmations");
    expect(await findAlert()).toHaveTextContent("Could not load the confirmation runs");
  });

  it("starts a run with the defaults and an Idempotency-Key", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/balance-confirmations") && init.method === "POST") return json(201, run);
      if (url.pathname.endsWith(`/balance-confirmations/${RUN}`)) return json(200, run);
      if (url.pathname.endsWith("/differences")) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt("/receivables/balance-confirmations/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Legal entity id", LE);
    fill("Key date", "2027-03-31");
    fill("Sample size", "0");
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    expect(await screen.findByText(/Enter a sample of 1 to 500 customers/)).toBeInTheDocument();
    fill("Sample size", "");
    fireEvent.change(screen.getByLabelText("Zero balances"), { target: { value: "yes" } });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    expect(await screen.findByRole("heading", { name: "Balance confirmation as of 2027-03-31" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(idempotencyKey(post)).toBeTruthy();
    expect(post.body).toEqual({ legalEntityId: LE, asOfDate: "2027-03-31", threshold: "10000.00", sampleSize: null, includeZeroBalances: true, reminderDays: 14 });
  });

  it("records a disagreement, which shows on the difference list with its case", async () => {
    let current = run;
    let diffs = [run.confirmations![1]];
    server.handler = (url, init) => {
      if (url.pathname.endsWith(":reply")) {
        const disagreed = { ...run.confirmations![0], status: "DISAGREED" as const, customerBalance: "11500", difference: "-500", caseId: CASE };
        current = { ...run, counts: { selected: 2, agreed: 0, disagreed: 2, noReply: 0 }, confirmations: [disagreed, run.confirmations![1]] };
        diffs = [disagreed, run.confirmations![1]];
        return json(200, disagreed);
      }
      if (url.pathname.endsWith("/differences")) return json(200, { items: diffs });
      if (url.pathname.endsWith(`/balance-confirmations/${RUN}`) && (init.method ?? "GET") === "GET") return json(200, current);
      return undefined;
    };
    const { container } = renderAt(`/receivables/balance-confirmations/${RUN}`);
    const differences = await findTable({ name: "Difference list" });
    await waitFor(() => expect(differences).toHaveTextContent("-250.00 USD"));
    expect(within(differences).getByRole("link", { name: "Open case" })).toHaveAttribute("href", `/receivables/cases/${CASE}`);
    expect(screen.getByRole("list", { name: "Replies" })).toHaveTextContent("No reply");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CONF } });
    fireEvent.change(screen.getByLabelText("Reply"), { target: { value: "DISAGREED" } });
    fireEvent.click(screen.getByRole("button", { name: "Record reply" }));
    expect(await screen.findByText("Enter the balance the customer states, such as 11500.00.")).toBeInTheDocument();
    fill("Customer's balance", "11500");
    fill("Comment", "payment in transit");
    fireEvent.click(screen.getByRole("button", { name: "Record reply" }));
    expect(await screen.findByText(/A dispute case was opened for the difference/)).toBeInTheDocument();
    expect(server.calls.find((c) => c.url.pathname.endsWith(":reply"))!.body).toEqual({
      confirmationId: CONF,
      reply: "DISAGREED",
      customerBalance: "11500",
      comment: "payment in transit",
    });
    await waitFor(() => expect(screen.getByRole("table", { name: "Difference list" })).toHaveTextContent("-500.00 USD"));
    expect(await screen.findByText("Every customer in this run has replied.")).toBeInTheDocument();
  });

  it("shows the service's refusal of a second reply and a load failure", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith(":reply")) return json(409, { code: "AR-CONFIRMATION-REPLIED", detail: "replies are immutable" });
      if (url.pathname.endsWith("/differences")) return json(200, { items: [] });
      return json(200, run);
    };
    const first = renderAt(`/receivables/balance-confirmations/${RUN}`);
    fireEvent.change(await screen.findByLabelText("Customer"), { target: { value: CONF } });
    fireEvent.click(screen.getByRole("button", { name: "Record reply" }));
    expect(await findAlert()).toHaveTextContent("AR-CONFIRMATION-REPLIED");
    first.unmount();
    server.handler = () => json(404, { code: "AR-CONFIRMATION-NOT-FOUND", detail: "balance confirmation run not found" });
    renderAt(`/receivables/balance-confirmations/${RUN}`);
    expect(await findAlert()).toHaveTextContent("AR-CONFIRMATION-NOT-FOUND");
  });

  it("validates runs and replies like the service does", () => {
    const draft = { legalEntityId: LE, asOfDate: "2027-03-31", threshold: "10000", sampleSize: "20", includeZeroBalances: false, reminderDays: "14" };
    expect(validateConfirmationRun(draft)).toEqual({});
    expect(toRunInput(draft).sampleSize).toBe(20);
    expect(Object.keys(validateConfirmationRun({ ...draft, threshold: "-1", reminderDays: "61", sampleSize: "501" })).sort()).toEqual([
      "reminderDays",
      "sampleSize",
      "threshold",
    ]);
    expect(validateReply({ confirmationId: CONF, reply: "DISAGREED", customerBalance: "-120.50", comment: "" })).toEqual({});
    expect(toReplyInput({ confirmationId: CONF, reply: "AGREED", customerBalance: "99", comment: " " })).toEqual({
      confirmationId: CONF,
      reply: "AGREED",
      customerBalance: null,
      comment: null,
    });
  });
});

describe("Down payments and deposits", () => {
  it("lists down payments, filters by kind and shows the empty and error states", async () => {
    server.handler = () => json(200, { items: [downPayment] });
    const first = renderAt("/receivables/down-payments");
    const table = await findTable({ name: "Down payments and deposits" });
    await waitFor(() => expect(table).toHaveTextContent("2000.00 USD"));
    expect(table).toHaveTextContent("Requested");
    expect(pageViolations(first.container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "SECURITY_DEPOSIT" } });
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("kind") === "SECURITY_DEPOSIT")).toBe(true));
    first.unmount();
    server.handler = () => json(200, { items: [] });
    const second = renderAt("/receivables/down-payments");
    expect(await screen.findByText("No down payments")).toBeInTheDocument();
    second.unmount();
    server.handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/receivables/down-payments");
    expect(await findAlert()).toHaveTextContent("Could not load the down payments");
  });

  it("records a request with an Idempotency-Key", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/down-payments") && init.method === "POST") return json(201, downPayment);
      if (url.pathname.endsWith(`/down-payments/${DP}`)) return json(200, downPayment);
      return undefined;
    };
    const { container } = renderAt(`/receivables/down-payments/new?legalEntity=${LE}&customer=${CUST}`);
    expect(pageViolations(container)).toEqual([]);
    fill("Amount", "0");
    fireEvent.click(screen.getByRole("button", { name: "Record request" }));
    expect(await screen.findByText("Enter an amount above zero, such as 2000.00.")).toBeInTheDocument();
    fill("Currency", "USD");
    fill("Amount", "2000.00");
    fireEvent.click(screen.getByRole("button", { name: "Record request" }));
    expect(await screen.findByRole("heading", { name: "Down payment 2000.00 USD" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(idempotencyKey(post)).toBeTruthy();
    expect(post.body).toEqual({ legalEntityId: LE, customerId: CUST, salesOrderId: null, kind: "DOWN_PAYMENT", currency: "USD", amount: "2000.00" });
  });

  it("receives a request, then clears it against the invoice with If-Match", async () => {
    let current: DownPayment = downPayment;
    server.handler = (url, init) => {
      if (url.pathname.endsWith(":receive")) {
        current = { ...current, status: "RECEIVED", receivedDate: "2027-03-02", version: 2 };
        return json(200, current);
      }
      if (url.pathname.endsWith(":clear")) {
        current = {
          ...current,
          status: "CLEARED",
          clearedAmount: "2000.00",
          remaining: "0",
          version: 3,
          clearings: [{ id: "cl-1", openItemId: ITEM, documentNumber: "INV-SO-4000", amount: "2000.00", clearedAt: "2027-04-01T10:00:00Z" }],
        };
        return json(200, current);
      }
      if (url.pathname.endsWith(`/down-payments/${DP}`) && (init.method ?? "GET") === "GET") return json(200, current);
      return undefined;
    };
    const { container } = renderAt(`/receivables/down-payments/${DP}`);
    expect(await screen.findByRole("heading", { name: "Down payment 2000.00 USD" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fill("Received on", "2027-03-02");
    fireEvent.click(screen.getByRole("button", { name: "Record as received" }));
    expect(await screen.findByText("Recorded as received.")).toBeInTheDocument();
    expect(ifMatch(":receive")).toBe('"1"');
    expect(server.calls.find((c) => c.url.pathname.endsWith(":receive"))!.body).toEqual({ receivedDate: "2027-03-02" });

    const item = await screen.findByLabelText("Open item id");
    fireEvent.change(item, { target: { value: ITEM } });
    fill("Amount", "2000.01");
    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(await screen.findByText("At most 2000.00 remains.")).toBeInTheDocument();
    fill("Amount", "");
    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(await screen.findByText("Cleared against the item.")).toBeInTheDocument();
    expect(ifMatch(":clear")).toBe('"2"');
    expect(server.calls.find((c) => c.url.pathname.endsWith(":clear"))!.body).toEqual({ openItemId: ITEM });
    const clearings = await findTable({ name: "Clearings" });
    await waitFor(() => expect(clearings).toHaveTextContent("INV-SO-4000"));
  });

  it("offers a deposit's release and shows the service's AR-0017", async () => {
    const deposit: DownPayment = { ...downPayment, kind: "SECURITY_DEPOSIT", status: "RECEIVED", receivedDate: "2027-03-02", remaining: "700.00", clearedAmount: "300.00", version: 4 };
    server.handler = (url) => {
      if (url.pathname.endsWith(":clear")) return json(422, { code: "AR-0017", detail: "clearing exceeds the remaining down payment" });
      if (url.pathname.endsWith(":release")) return json(200, { ...deposit, status: "RELEASED", version: 5 });
      return json(200, deposit);
    };
    renderAt(`/receivables/down-payments/${DP}`);
    fireEvent.change(await screen.findByLabelText("Open item id"), { target: { value: ITEM } });
    fireEvent.click(screen.getByRole("button", { name: "Offset" }));
    expect(await findAlert()).toHaveTextContent("AR-0017");
    fireEvent.click(screen.getByRole("button", { name: "Release 700.00 USD for refund" }));
    expect(await screen.findByText("The deposit's remainder is released for refund.")).toBeInTheDocument();
    expect(ifMatch(":release")).toBe('"4"');
  });

  it("validates requests and clearings like the service does", () => {
    const draft = { legalEntityId: LE, customerId: CUST, salesOrderId: "", kind: "DOWN_PAYMENT" as const, currency: "USD", amount: "10" };
    expect(validateDownPayment(draft)).toEqual({});
    expect(Object.keys(validateDownPayment({ ...draft, salesOrderId: "SO-4000", currency: "usd" })).sort()).toEqual(["currency", "salesOrderId"]);
    expect(toDownPaymentInput({ ...draft, salesOrderId: " " }).salesOrderId).toBeNull();
    expect(validateClearing(ITEM, "", "5")).toEqual({});
    expect(validateClearing("x", "0", "5")).toHaveProperty("openItemId");
    expect(validateClearing(ITEM, "5.000001", "5")).toEqual({ amount: "At most 5 remains." });
  });
});
