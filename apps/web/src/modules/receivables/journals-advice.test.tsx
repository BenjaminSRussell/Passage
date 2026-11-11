import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { GLJournal } from "./api/client";
import { findAlert, findTable } from "../../test-utils";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";
import { standing } from "./GLJournals";
import { validateAdvice, type AdviceDraft } from "./RemittanceAdvice";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";

function journal(over: Partial<GLJournal>): GLJournal {
  return {
    id: "0190f0a0-0000-7000-8000-0000000000b1",
    legalEntityId: LE,
    sourceType: "payment_receipt",
    sourceId: "0190f0a0-0000-7000-8000-0000000000c1",
    documentId: "PAY-1",
    postingDate: "2027-03-08",
    currency: "USD",
    status: "POSTED",
    journalPostingId: "jp-1",
    attempts: 1,
    parked: false,
    createdAt: "2027-03-08T10:00:00Z",
    debit: "980.00",
    lines: [
      { account: "1000", side: "D", amount: "980.00", text: "Payment received" },
      { account: "1205", side: "C", amount: "980.00", text: "Unapplied cash" },
    ],
    ...over,
  };
}

const parked = journal({
  id: "0190f0a0-0000-7000-8000-0000000000b2",
  sourceType: "dunning_fee",
  status: "PENDING",
  journalPostingId: null,
  parked: true,
  attempts: 5,
  debit: "25.00",
  lastError: "The ledger period is closed.",
  lines: [
    { account: "1200", side: "D", amount: "25.00" },
    { account: "4300", side: "C", amount: "25.00" },
  ],
});

describe("ledger journals page", () => {
  it("lists journals in plain words with their standing, and passes the filters on", async () => {
    server.handler = (url) => (url.pathname.endsWith("/gl-journals") ? json(200, { items: [journal({}), parked] }) : undefined);
    const { container } = renderAt("/receivables/gl-journals");
    const table = await findTable({ name: "Ledger journals" });
    await waitFor(() => expect(table).toHaveTextContent("Cash received"));
    expect(table).toHaveTextContent("Dunning fee");
    expect(table).toHaveTextContent("980.00 USD");
    expect(table).toHaveTextContent("Posted");
    expect(table).toHaveTextContent("Parked");
    expect(table).not.toHaveTextContent("PAY-1");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByLabelText("Parked journals only"));
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("parked") === "true")).toBe(true));
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "PENDING" } });
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("status") === "PENDING")).toBe(true));
  });

  it("opens a parked journal and retries it under a new posting date", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":retry")) return json(200, { ...parked, parked: false, attempts: 0 });
      return url.pathname.endsWith("/gl-journals") ? json(200, { items: [parked] }) : undefined;
    };
    renderAt("/receivables/gl-journals");
    const table = await findTable({ name: "Ledger journals" });
    fireEvent.click(await within(table).findByText("Dunning fee"));
    expect(await screen.findByText("The ledger period is closed.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/New posting date/), { target: { value: "2027-04-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Retry posting" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "POST" && c.url.pathname.endsWith(`${parked.id}:retry`))).toBe(true));
    const call = server.calls.find((c) => c.url.pathname.endsWith(":retry"));
    expect(call?.body).toEqual({ postingDate: "2027-04-01" });
  });

  it("posts the due journals and says what happened", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/gl-journals:sweep")) return json(200, { posted: 3, failed: 1, parked: 0 });
      return url.pathname.endsWith("/gl-journals") ? json(200, { items: [] }) : undefined;
    };
    renderAt("/receivables/gl-journals");
    expect(await screen.findByText("No journals")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Post due journals" }));
    expect(await screen.findByText(/3 posted, 1 to try again later, 0 parked/)).toBeInTheDocument();
  });

  it("shows an error with a retry when the list fails", async () => {
    server.handler = () => json(503, { code: "AR-GL-UNAVAILABLE", title: "Unavailable", detail: "ledger down" });
    renderAt("/receivables/gl-journals");
    expect(await findAlert()).toHaveTextContent("Try again");
  });

  it("names the standing of a journal", () => {
    expect(standing(journal({}))).toBe("Posted");
    expect(standing(parked)).toBe("Parked");
    expect(standing(journal({ status: "PENDING", parked: false }))).toBe("Waiting");
  });
});

const draft: AdviceDraft = { legalEntityId: LE, format: "CSV", content: "paymentRef,reference,amount\nWIRE-1,INV-1,100.00\n", autoApply: false, createMissing: false, currency: "", valueDate: "", paymentMode: "WIRE" };

describe("remittance advice page", () => {
  it("validates before sending", () => {
    expect(validateAdvice(draft)).toEqual({});
    expect(validateAdvice({ ...draft, legalEntityId: "" }).legalEntityId).toBeTruthy();
    expect(validateAdvice({ ...draft, content: " " }).content).toBeTruthy();
    expect(validateAdvice({ ...draft, createMissing: true }).currency).toBeTruthy();
    expect(validateAdvice({ ...draft, createMissing: true, currency: "usd" }).currency).toBeTruthy();
    expect(validateAdvice({ ...draft, createMissing: true, currency: "USD", valueDate: "tomorrow" }).valueDate).toBeTruthy();
    expect(validateAdvice({ ...draft, createMissing: true, currency: "USD" })).toEqual({});
  });

  it("imports an advice and reports each line", async () => {
    server.handler = (url, init) =>
      init.method === "POST" && url.pathname.endsWith("/remittance-advices")
        ? json(200, {
            attached: 1,
            created: 1,
            skipped: 1,
            lines: [
              { paymentRef: "WIRE-1", outcome: "ATTACHED", paymentId: "p1" },
              { paymentRef: "WIRE-2", outcome: "ADVISED", paymentId: "p2", detail: "no bank credit carries this reference yet" },
            ],
          })
        : undefined;
    const { container } = renderAt("/receivables/remittance-advice");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("File content"), { target: { value: draft.content } });
    fireEvent.click(screen.getByLabelText("Record lines with no bank credit yet as advised payments"));
    fireEvent.change(await screen.findByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.click(screen.getByLabelText("Apply the payments the matcher is sure of"));
    fireEvent.click(screen.getByRole("button", { name: "Import advice" }));
    const table = await findTable({ name: "Result per payment" });
    expect(table).toHaveTextContent("Attached to the payment");
    expect(table).toHaveTextContent("Recorded as advised, no bank credit yet");
    const call = server.calls.find((c) => c.url.pathname.endsWith("/remittance-advices"));
    expect(call?.body).toMatchObject({ legalEntityId: LE, format: "CSV", autoApply: true, createMissing: true, currency: "USD", paymentMode: "WIRE" });
  });

  it("shows what the service refused", async () => {
    server.handler = () => json(400, { code: "AR-ADVICE-MALFORMED", title: "Bad file", detail: "row 3: the amount must be positive" });
    renderAt("/receivables/remittance-advice");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("File content"), { target: { value: draft.content } });
    fireEvent.click(screen.getByRole("button", { name: "Import advice" }));
    expect(await findAlert()).toHaveTextContent("remittance advice file");
  });
});
