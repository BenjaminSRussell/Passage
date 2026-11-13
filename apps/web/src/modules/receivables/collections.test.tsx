import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Dispute, DunningRun, InterestNote, InterestResult, LockboxBatch, OpenItem, PaymentPromise } from "./api/client";
import { validateDispute } from "./Disputes";
import { validateRun } from "./Dunning";
import { validateInterest } from "./Interest";
import { validateLockbox } from "./Lockbox";
import { promiseHolds, validatePromise } from "./Promises";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "0190f0a0-0000-7000-8000-0000000000c1";
const ITEM = "0190f0a0-0000-7000-8000-000000000401";
const DSP = "0190f0a0-0000-7000-8000-000000000501";
const RUN = "0190f0a0-0000-7000-8000-000000000601";

const dispute: Dispute = {
  id: DSP,
  legalEntityId: LE,
  customerId: CUST,
  openItemId: ITEM,
  documentNumber: "INV-30001",
  reasonCode: "PRICE",
  disputedAmount: "500.00",
  currency: "USD",
  owner: "ana",
  status: "OPEN",
  slaDue: "2027-03-10T12:00:00Z",
  createdAt: "2027-03-05T12:00:00Z",
  version: 1,
};

const promise: PaymentPromise = {
  id: "0190f0a0-0000-7000-8000-000000000701",
  legalEntityId: LE,
  customerId: CUST,
  openItemId: ITEM,
  documentNumber: "INV-30001",
  currency: "USD",
  amount: "800.00",
  promiseDate: "2099-01-15",
  createdAt: "2027-03-05T12:00:00Z",
};

const run: DunningRun = {
  id: RUN,
  legalEntityId: LE,
  runDate: "2027-03-31",
  graceDays: 5,
  status: "DRAFT",
  exclusions: [{ openItemId: ITEM, customerId: CUST, reason: "DISPUTE" }],
  letters: [
    {
      id: "0190f0a0-0000-7000-8000-000000000611",
      customerId: CUST,
      currency: "USD",
      level: 3,
      priorLevel: 2,
      amount: "1000.00",
      feeAmount: "25.00",
      status: "DRAFT",
      items: [{ openItemId: ITEM, documentNumber: "INV-30002", dueDate: "2027-02-01", openAmount: "1000.00", daysOverdue: 58, fromLevel: 2, toLevel: 3 }],
    },
  ],
  version: 1,
};

const note: InterestNote = {
  id: "0190f0a0-0000-7000-8000-000000000801",
  legalEntityId: LE,
  customerId: CUST,
  openItemId: ITEM,
  documentNumber: "INV-30001",
  interestItemId: "0190f0a0-0000-7000-8000-000000000802",
  period: "2027-03",
  calculationDate: "2027-03-31",
  days: 54,
  annualRate: "0.12",
  basis: "ACT_365",
  currency: "USD",
  principal: "10000.00",
  amount: "177.53",
};

const preview: InterestResult = {
  candidates: [{ openItemId: ITEM, customerId: CUST, documentNumber: "INV-30001", currency: "USD", principal: "10000.00", days: 54, amount: "177.53" }],
  exclusions: [{ openItemId: "0190f0a0-0000-7000-8000-000000000402", customerId: CUST, reason: "BELOW_MINIMUM" }],
  posted: [],
};

const batch: LockboxBatch = {
  id: "0190f0a0-0000-7000-8000-000000000901",
  legalEntityId: LE,
  fileSha256: "a".repeat(64),
  receivedDate: "2027-03-05",
  accountCount: 1,
  paymentCount: 1,
  importedAt: "2027-03-05T14:00:00Z",
};

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("Collections worklist", () => {
  it("asks for a legal entity, then ranks the customers by risk", async () => {
    server.handler = (url) =>
      url.pathname.endsWith("/worklist")
        ? json(200, {
            asOf: "2027-03-31",
            items: [{ customerId: CUST, currency: "USD", totalOpen: "1200.00", overdue: "1000.00", oldestDays: 58, avgDaysLate: 58, dunningLevel: 3, openDisputes: 1, brokenPromises: 0, riskScore: 74 }],
          })
        : undefined;
    const { container } = renderAt("/receivables/worklist");
    expect(screen.getByText("Choose a legal entity")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fill("Legal entity", "nope");
    expect(screen.getByText("Choose a legal entity")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    fill("Legal entity", LE);
    fill("As of", "2027-03-31");
    const table = await findTable({ name: "Collections worklist" });
    await waitFor(() => expect(table).toHaveTextContent("74"));
    expect(table).toHaveTextContent("1000.00 USD");
    const call = server.calls.at(-1)!;
    expect(call.url.searchParams.get("legalEntityId")).toBe(LE);
    expect(call.url.searchParams.get("asOf")).toBe("2027-03-31");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the error with a retry and the empty state", async () => {
    server.handler = () => json(403, { code: "AUTH-0003", detail: "legal entity is outside the caller's scope" });
    renderAt("/receivables/worklist");
    fill("Legal entity", LE);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("AUTH-0003");
    server.handler = () => json(200, { asOf: "2027-03-31", items: [] });
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Nothing to collect")).toBeInTheDocument();
  });
});

describe("Disputes", () => {
  it("lists active disputes first, filters by status and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/disputes")) return json(200, { items: [dispute] });
      if (url.pathname.endsWith(`/disputes/${DSP}`)) return json(200, dispute);
      return undefined;
    };
    const { container } = renderAt("/receivables/disputes");
    const table = await findTable({ name: "Disputes" });
    await waitFor(() => expect(table).toHaveTextContent("INV-30001"));
    expect(table).toHaveTextContent("500.00 USD");
    expect(table).toHaveTextContent("2027-03-10 12:00 UTC");
    expect(server.calls[0].url.searchParams.get("status")).toBe("active");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "REJECTED" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("status") === "REJECTED")).toBe(true));
    fireEvent.click(within(await findTable({ name: "Disputes" })).getByRole("button", { name: "INV-30001" }));
    expect(await screen.findByRole("heading", { name: "Dispute on INV-30001" })).toBeInTheDocument();
  });

  it("shows the empty and error states", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/receivables/disputes");
    expect(await screen.findByText("No disputes match these filters")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(503, { code: "COM-0500", detail: "internal error" });
    renderAt("/receivables/disputes");
    expect(await findAlert()).toHaveTextContent("Could not load disputes");
  });

  it("opens a dispute from an open item with an Idempotency-Key", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/disputes") && init.method === "POST") return json(201, dispute);
      if (url.pathname.endsWith(`/disputes/${DSP}`)) return json(200, dispute);
      return undefined;
    };
    const { container } = renderAt(`/receivables/disputes/new?legalEntity=${LE}&openItem=${ITEM}&amount=1000.00`);
    expect(screen.getByLabelText("Open item")).toHaveAttribute("data-value", ITEM);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Open dispute" }));
    expect(await screen.findByText("Use an upper-case code such as PRICE or DAMAGE.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);

    fill("Reason code", "PRICE");
    fill("Disputed amount", "500.00");
    fill("Owner", "ana");
    fireEvent.click(screen.getByRole("button", { name: "Open dispute" }));
    expect(await screen.findByRole("heading", { name: "Dispute on INV-30001" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(idempotencyKey(post)).toBeTruthy();
    expect(post.body).toEqual({ legalEntityId: LE, openItemId: ITEM, reasonCode: "PRICE", disputedAmount: "500.00", owner: "ana" });
  });

  it("shows the service's refusal of a second dispute", async () => {
    server.handler = () => json(409, { code: "AR-DISPUTE-EXISTS", detail: "open item INV-30001 already has an active dispute" });
    renderAt(`/receivables/disputes/new?legalEntity=${LE}&openItem=${ITEM}`);
    fill("Reason code", "PRICE");
    fill("Disputed amount", "5");
    fill("Owner", "ana");
    fireEvent.click(screen.getByRole("button", { name: "Open dispute" }));
    expect(await findAlert()).toHaveTextContent("AR-DISPUTE-EXISTS");
  });

  it("starts the review with If-Match, then resolves it as a credit", async () => {
    let current: Dispute = dispute;
    server.handler = (url, init) => {
      if (url.pathname.endsWith(":review")) {
        current = { ...dispute, status: "UNDER_REVIEW", version: 2 };
        return json(200, current);
      }
      if (url.pathname.endsWith(":resolve")) {
        current = { ...current, status: "RESOLVED_CREDIT", version: 3, resolutionNote: "CM-7" };
        return json(200, current);
      }
      if (url.pathname.endsWith(`/disputes/${DSP}`) && (init.method ?? "GET") === "GET") return json(200, current);
      return undefined;
    };
    const { container } = renderAt(`/receivables/disputes/${DSP}`);
    expect(await screen.findByRole("heading", { name: "Dispute on INV-30001" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    // From OPEN only rejection is offered.
    expect(within(screen.getByLabelText("Resolution")).getAllByRole("option").map((o) => o.textContent)).toEqual(["Reject the dispute"]);

    fireEvent.click(screen.getByRole("button", { name: "Start review" }));
    expect(await screen.findByText("The dispute is under review.")).toBeInTheDocument();
    expect(new Headers(server.calls.find((c) => c.url.pathname.endsWith(":review"))!.init.headers).get("If-Match")).toBe('"1"');

    await waitFor(() => expect(screen.getByLabelText("Resolution")).toHaveValue("CREDIT"));
    fill("Note", "CM-7");
    fireEvent.click(screen.getByRole("button", { name: "Resolve dispute" }));
    expect(await screen.findByText(/Resolved as a credit/)).toBeInTheDocument();
    const resolve = server.calls.find((c) => c.url.pathname.endsWith(":resolve"))!;
    expect(new Headers(resolve.init.headers).get("If-Match")).toBe('"2"');
    expect(resolve.body).toEqual({ resolution: "CREDIT", note: "CM-7" });
    expect(await screen.findByText(/This dispute is closed/)).toBeInTheDocument();
  });

  it("explains a stale version and a load failure", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith(":review")) return json(412, { code: "conflict_version", detail: "dispute changed since it was read" });
      if (url.pathname.endsWith(`/disputes/${DSP}`)) return json(200, dispute);
      return undefined;
    };
    const first = renderAt(`/receivables/disputes/${DSP}`);
    fireEvent.click(await screen.findByRole("button", { name: "Start review" }));
    expect(await findAlert()).toHaveTextContent("The dispute changed since you opened it");
    first.unmount();
    server.handler = () => json(404, { code: "AR-DISPUTE-NOT-FOUND", detail: "dispute not found" });
    renderAt(`/receivables/disputes/${DSP}`);
    expect(await findAlert()).toHaveTextContent("AR-DISPUTE-NOT-FOUND");
  });

  it("validates a draft like the service does", () => {
    expect(validateDispute({ legalEntityId: LE, openItemId: ITEM, reasonCode: "PRICE", disputedAmount: "1.00", owner: "ana" })).toEqual({});
    expect(Object.keys(validateDispute({ legalEntityId: "x", openItemId: "y", reasonCode: "price", disputedAmount: "0", owner: " " })).sort()).toEqual(
      ["disputedAmount", "legalEntityId", "openItemId", "owner", "reasonCode"],
    );
  });
});

describe("Promises to pay", () => {
  it("lists promises with their state and filters by customer", async () => {
    server.handler = () => json(200, { items: [promise, { ...promise, id: "p2", promiseDate: "2020-01-01" }] });
    const { container } = renderAt("/receivables/promises");
    const table = await findTable({ name: "Promises to pay" });
    await waitFor(() => expect(table).toHaveTextContent("800.00 USD"));
    expect(table).toHaveTextContent("Holds");
    expect(table).toHaveTextContent("Past due");
    expect(pageViolations(container)).toEqual([]);
    fill("Customer", CUST);
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("customer") === CUST)).toBe(true));
  });

  it("shows the empty and error states", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/receivables/promises");
    expect(await screen.findByText("No promises")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/receivables/promises");
    expect(await findAlert()).toHaveTextContent("Could not load the promises");
  });

  it("records a promise with an Idempotency-Key and reports refusals", async () => {
    server.handler = (url, init) => (url.pathname.endsWith("/promises") && init.method === "POST" ? json(201, promise) : undefined);
    const { container } = renderAt(`/receivables/promises/new?legalEntity=${LE}&openItem=${ITEM}&amount=800.00`);
    expect(pageViolations(container)).toEqual([]);
    fill("Promise date", "2020-01-01");
    fireEvent.click(screen.getByRole("button", { name: "Record promise" }));
    expect(await screen.findByText("The promise date cannot be in the past.")).toBeInTheDocument();
    fill("Promise date", "2099-01-15");
    fireEvent.click(screen.getByRole("button", { name: "Record promise" }));
    expect(await findStatus()).toHaveTextContent("800.00 USD on INV-30001 by 2099-01-15");
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(idempotencyKey(post)).toBeTruthy();
    expect(post.body).toEqual({ legalEntityId: LE, openItemId: ITEM, amount: "800.00", promiseDate: "2099-01-15" });

    server.handler = () => json(409, { code: "AR-PROMISE-EXISTS", detail: "open item INV-30001 already has a promise that has not yet broken" });
    fireEvent.click(screen.getByRole("button", { name: "Record promise" }));
    expect(await findAlert()).toHaveTextContent("AR-PROMISE-EXISTS");
  });

  it("holds a promise until two days after its date", () => {
    expect(promiseHolds("2027-03-30", "2027-04-01")).toBe(true);
    expect(promiseHolds("2027-03-30", "2027-04-02")).toBe(false);
    expect(validatePromise({ legalEntityId: LE, openItemId: ITEM, amount: "1", promiseDate: "2027-03-30" }, "2027-03-31")).toHaveProperty("promiseDate");
  });
});

describe("Dunning runs", () => {
  it("lists runs and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/dunning-runs")) {
        return json(200, { items: [{ id: RUN, legalEntityId: LE, runDate: "2027-03-31", graceDays: 5, status: "ISSUED", letterCount: 31, issuedAt: "2027-03-31T09:00:00Z", version: 3 }] });
      }
      if (url.pathname.endsWith(`/dunning-runs/${RUN}`)) return json(200, run);
      return undefined;
    };
    const { container } = renderAt("/receivables/dunning-runs");
    const table = await findTable({ name: "Dunning runs" });
    await waitFor(() => expect(table).toHaveTextContent("31"));
    expect(table).toHaveTextContent("Issued");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "DRAFT" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("status") === "DRAFT")).toBe(true));
    fireEvent.click(within(await findTable({ name: "Dunning runs" })).getByRole("button", { name: "2027-03-31" }));
    expect(await screen.findByRole("heading", { name: "Dunning run 2027-03-31" })).toBeInTheDocument();
  });

  it("shows the empty and error states", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/receivables/dunning-runs");
    expect(await screen.findByText("No dunning runs")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/receivables/dunning-runs");
    expect(await findAlert()).toHaveTextContent("Could not load dunning runs");
  });

  it("proposes a run with an Idempotency-Key and opens its preview", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/dunning-runs") && init.method === "POST") return json(201, run);
      if (url.pathname.endsWith(`/dunning-runs/${RUN}`)) return json(200, run);
      return undefined;
    };
    const { container } = renderAt("/receivables/dunning-runs/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Grace days", "99");
    fireEvent.click(screen.getByRole("button", { name: "Propose run" }));
    expect(await screen.findByText("Enter whole days from 0 to 60.")).toBeInTheDocument();
    fill("Legal entity", LE);
    fill("Grace days", "5");
    fill("Run date", "2027-03-31");
    fireEvent.click(screen.getByRole("button", { name: "Propose run" }));
    expect(await screen.findByRole("heading", { name: "Dunning run 2027-03-31" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(idempotencyKey(post)).toBeTruthy();
    expect(post.body).toEqual({ legalEntityId: LE, runDate: "2027-03-31", graceDays: 5 });
  });

  it("previews letters and exclusions, then reviews and issues with If-Match", async () => {
    let current: DunningRun = run;
    server.handler = (url) => {
      if (url.pathname.endsWith(":review")) {
        current = { ...run, status: "REVIEWED", version: 2 };
        return json(200, current);
      }
      if (url.pathname.endsWith(":issue")) {
        current = { ...current, status: "ISSUED", version: 3, issuedAt: "2027-03-31T09:00:00Z", letters: current.letters.map((l) => ({ ...l, status: "ISSUED" })) };
        return json(200, current);
      }
      if (url.pathname.endsWith(`/dunning-runs/${RUN}`)) return json(200, current);
      return undefined;
    };
    const { container } = renderAt(`/receivables/dunning-runs/${RUN}`);
    const letters = await findTable({ name: "Letters" });
    expect(letters).toHaveTextContent("2 → 3");
    expect(letters).toHaveTextContent("INV-30002 (58 d)");
    expect(letters).toHaveTextContent("25.00 USD");
    expect(screen.getByRole("table", { name: "Items left out" })).toHaveTextContent("Open dispute");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Mark reviewed" }));
    expect(await screen.findByText(/The run is reviewed/)).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "Issue letters" }));
    expect(await screen.findByText(/item levels are raised and fees posted/)).toBeInTheDocument();
    const issue = server.calls.find((c) => c.url.pathname.endsWith(":issue"))!;
    expect(new Headers(issue.init.headers).get("If-Match")).toBe('"2"');
    await waitFor(() => expect(screen.queryByRole("button", { name: "Cancel run" })).not.toBeInTheDocument());
  });

  it("shows a refused issue and a load failure", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith(":cancel")) return json(409, { code: "AR-DUNNING-STATE", detail: "dunning run in status ISSUED cannot move to CANCELLED" });
      if (url.pathname.endsWith(`/dunning-runs/${RUN}`)) return json(200, run);
      return undefined;
    };
    const first = renderAt(`/receivables/dunning-runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel run" }));
    expect(await findAlert()).toHaveTextContent("AR-DUNNING-STATE");
    first.unmount();
    server.handler = () => json(404, { code: "AR-DUNNING-RUN-NOT-FOUND", detail: "dunning run not found" });
    renderAt(`/receivables/dunning-runs/${RUN}`);
    expect(await findAlert()).toHaveTextContent("AR-DUNNING-RUN-NOT-FOUND");
  });

  it("validates a run draft", () => {
    expect(validateRun({ legalEntityId: LE, runDate: "2027-03-31", graceDays: "0" })).toEqual({});
    expect(validateRun({ legalEntityId: LE, runDate: "2027-03-31", graceDays: "-1" })).toHaveProperty("graceDays");
  });
});

describe("Interest", () => {
  it("lists posted notes, with empty and error states", async () => {
    server.handler = () => json(200, { items: [note] });
    const { container, unmount } = renderAt("/receivables/interest-notes");
    const table = await findTable({ name: "Interest notes" });
    await waitFor(() => expect(table).toHaveTextContent("177.53 USD"));
    expect(table).toHaveTextContent("0.12 ACT/365");
    expect(pageViolations(container)).toEqual([]);
    unmount();
    server.handler = () => json(200, { items: [] });
    const second = renderAt("/receivables/interest-notes");
    expect(await screen.findByText("No interest notes")).toBeInTheDocument();
    second.unmount();
    server.handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/receivables/interest-notes");
    expect(await findAlert()).toHaveTextContent("Could not load the interest notes");
  });

  it("previews interest, then posts it with an Idempotency-Key", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith(":calculate")) return json(200, preview);
      if (url.pathname.endsWith(":post")) return json(200, { ...preview, posted: [note] });
      return undefined;
    };
    const { container } = renderAt("/receivables/interest-notes/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Legal entity", LE);
    fill("Annual rate", "9");
    fireEvent.click(screen.getByRole("button", { name: "Preview interest" }));
    expect(await screen.findByText(/as a fraction above 0/)).toBeInTheDocument();
    fill("Annual rate", "0.12");
    fill("Calculation date", "2027-03-31");
    fireEvent.click(screen.getByRole("button", { name: "Preview interest" }));
    const table = await findTable({ name: "Interest to charge" });
    expect(table).toHaveTextContent("177.53 USD");
    expect(screen.getByRole("table", { name: "Items left out of interest" })).toHaveTextContent("Below the 5.00 minimum");
    expect(server.calls.find((c) => c.url.pathname.endsWith(":calculate"))!.body).toEqual({
      legalEntityId: LE,
      calculationDate: "2027-03-31",
      annualRate: "0.12",
      basis: "ACT_365",
      graceDays: 5,
    });
    fireEvent.click(screen.getByRole("button", { name: "Post interest notes" }));
    expect(await findStatus()).toHaveTextContent("1 interest note(s) posted");
    expect(idempotencyKey(server.calls.find((c) => c.url.pathname.endsWith(":post"))!)).toBeTruthy();
  });

  it("shows a refused post", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith(":calculate")) return json(200, preview);
      return json(403, { code: "AUTH-0003", detail: "missing duty receivables.interest.post" });
    };
    renderAt("/receivables/interest-notes/new");
    fill("Legal entity", LE);
    fill("Annual rate", "0.12");
    fireEvent.click(screen.getByRole("button", { name: "Preview interest" }));
    fireEvent.click(await screen.findByRole("button", { name: "Post interest notes" }));
    expect(await findAlert()).toHaveTextContent("AUTH-0003");
  });

  it("validates the rate and grace", () => {
    const base = { legalEntityId: LE, calculationDate: "2027-03-31", annualRate: "0.09", basis: "ACT_365" as const, graceDays: "5" };
    expect(validateInterest(base)).toEqual({});
    expect(validateInterest({ ...base, annualRate: "0" })).toHaveProperty("annualRate");
    expect(validateInterest({ ...base, annualRate: "1.5" })).toHaveProperty("annualRate");
    expect(validateInterest({ ...base, graceDays: "61" })).toHaveProperty("graceDays");
  });
});

describe("Lockbox", () => {
  it("lists imported files, with empty and error states", async () => {
    server.handler = () => json(200, { items: [batch] });
    const { container, unmount } = renderAt("/receivables/lockbox");
    const table = await findTable({ name: "Lockbox imports" });
    await waitFor(() => expect(table).toHaveTextContent("aaaaaaaaaaaa…"));
    expect(pageViolations(container)).toEqual([]);
    unmount();
    server.handler = () => json(200, { items: [] });
    const second = renderAt("/receivables/lockbox");
    expect(await screen.findByText("No lockbox files")).toBeInTheDocument();
    second.unmount();
    server.handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/receivables/lockbox");
    expect(await findAlert()).toHaveTextContent("Could not load lockbox imports");
  });

  it("imports a pasted file and lists the payments it created", async () => {
    const created = {
      ...batch,
      payments: [
        {
          id: "0190f0a0-0000-7000-8000-000000000301",
          legalEntityId: LE,
          paymentMode: "CHECK",
          source: "LOCKBOX",
          sourceRef: "123456789:REF1",
          amount: "1250.00",
          unappliedAmount: "1250.00",
          currency: "USD",
          receivedDate: "2027-03-05",
          valueDate: "2027-03-05",
          remittance: [{ reference: "INV-100", amount: "1250.00" }],
          status: "RECEIVED",
          version: 1,
        },
      ],
    };
    server.handler = (url, init) => (url.pathname.endsWith("/lockbox/imports") && init.method === "POST" ? json(201, created) : undefined);
    const { container } = renderAt("/receivables/lockbox/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Check type codes", "475");
    fireEvent.click(screen.getByRole("button", { name: "Import file" }));
    expect(await screen.findByText(/credit codes from 100 to 399/)).toBeInTheDocument();
    expect(screen.getByText("Choose the BAI2 file or paste its content.")).toBeInTheDocument();

    fill("Legal entity", LE);
    fill("Check type codes", "");
    fill("File content", "01,BANKX/");
    fireEvent.click(screen.getByRole("button", { name: "Import file" }));
    const table = await findTable({ name: "Payments created" });
    expect(table).toHaveTextContent("1250.00 USD");
    expect(table).toHaveTextContent("INV-100");
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ legalEntityId: LE, receivedDate: expect.stringMatching(/^\d{4}-\d{2}-\d{2}$/), content: "01,BANKX/" });
  });

  it("names a duplicate file and a malformed line", async () => {
    server.handler = () => json(409, { code: "AR-0003", detail: "File already imported" });
    renderAt("/receivables/lockbox/new");
    fill("Legal entity", LE);
    fill("File content", "01,BANKX/");
    fireEvent.click(screen.getByRole("button", { name: "Import file" }));
    expect(await findAlert()).toHaveTextContent("This file was already imported");
    server.handler = () => json(400, { code: "AR-0004", detail: "Line 4: malformed BAI2 record type 16" });
    fireEvent.click(screen.getByRole("button", { name: "Import file" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Line 4"));
  });

  it("validates a draft", () => {
    expect(validateLockbox({ legalEntityId: LE, receivedDate: "2027-03-05", typeCodes: "115, 116", content: "x" })).toEqual({});
    expect(validateLockbox({ legalEntityId: LE, receivedDate: "2027-03-05", typeCodes: "16", content: "x" })).toHaveProperty("typeCodes");
  });
});

describe("Open items collections actions", () => {
  it("links each open item to a dispute and a promise with the item filled in", async () => {
    const item: OpenItem = {
      id: ITEM,
      legalEntityId: LE,
      customerId: CUST,
      sourceType: "INVOICE",
      documentNumber: "INV-30001",
      documentDate: "2027-03-01",
      dueDate: "2027-03-31",
      currency: "USD",
      amount: "1000.00",
      openAmount: "1000.00",
      cashDiscountAmount: "0",
      dunningLevel: 2,
      dunningBlock: false,
      status: "OPEN",
      version: 1,
    };
    server.handler = () => json(200, { items: [item] });
    renderAt("/receivables/open-items");
    const link = await screen.findByRole("link", { name: "Dispute INV-30001" });
    expect(link.getAttribute("href")).toBe(`/receivables/disputes/new?legalEntity=${LE}&openItem=${ITEM}&amount=1000.00`);
    expect(screen.getByRole("link", { name: "Promise for INV-30001" }).getAttribute("href")).toContain("/receivables/promises/new?");
  });
});
