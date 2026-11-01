import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { RealignmentRun } from "./api/client";
import { parseMapping, toRealignmentInput, validateRealignment } from "./RealignmentFormPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const RUN_ID = "0190a000-0000-7000-8000-0000000000e1";
const BASE = `/api/v1/ledger/ledgers/${LEDGER}/realignments`;

const proposed: RealignmentRun = {
  id: RUN_ID,
  ledgerId: LEDGER,
  status: "proposed",
  filter: { accountFrom: "6000", accountTo: "6999", periodFrom: "2026-01", periodTo: "2026-03" },
  mapping: { DEPT: { C100: "C150" } },
  combinationsAffected: 3,
  lineCount: 4,
  amountMoved: "750.00",
  currency: "USD",
  preview: [
    { account: "6010", period: "2026-01", oldDimensions: { DEPT: "C100" }, newDimensions: { DEPT: "C150" }, oldCombination: "6010-C100", newCombination: "6010-C150", net: "150", lineCount: 2 },
    { account: "6010", period: "2026-02", oldDimensions: { DEPT: "C100" }, newDimensions: { DEPT: "C150" }, oldCombination: "6010-C100", newCombination: "6010-C150", net: "200", lineCount: 1 },
  ],
  previewTruncated: false,
  entryIds: [],
  reversalEntryIds: [],
  version: 1,
  createdAt: "2026-03-15T10:00:00Z",
};
const posted: RealignmentRun = { ...proposed, status: "posted", reasonCode: "REORG", postedBy: "controller", entryIds: ["0190a000-0000-7000-8000-0000000000f1"], version: 2 };

function fake(run: RealignmentRun = proposed) {
  server.handler = (url, init) => {
    const method = init.method ?? "GET";
    if (url.pathname === BASE && method === "GET") return json(200, { items: [{ ...run, preview: [] }] });
    if (url.pathname === BASE && method === "POST") return json(201, proposed);
    if (url.pathname === `${BASE}/${RUN_ID}` && method === "GET") return json(200, run);
    if (url.pathname === `${BASE}/${RUN_ID}:post`) return json(200, posted);
    if (url.pathname === `${BASE}/${RUN_ID}:reverse`) return json(200, { ...posted, status: "reversed" });
    if (url.pathname === `${BASE}/${RUN_ID}:cancel`) return json(200, { ...proposed, status: "cancelled" });
    return undefined;
  };
}

describe("realignment form helpers", () => {
  it("parses one old > new pair per line", () => {
    expect(parseMapping("dept: C100 > C150\nDEPT: C200>C250\nPROJECT: P1 > P2").mapping).toEqual({ DEPT: { C100: "C150", C200: "C250" }, PROJECT: { P1: "P2" } });
    expect(parseMapping("").mapping).toEqual({});
    expect(parseMapping("DEPT C100").error).toContain("DIMENSION: old > new");
    expect(parseMapping("DEPT: C100 > C100").error).toContain("same as the old");
    expect(parseMapping("DEPT: a b > c").error).toBeDefined();
    expect(parseMapping("DEPT: C100 > has/slash").error).toContain("up to 20");
  });

  it("validates the draft and builds the request", () => {
    const draft = { accountFrom: "6000", accountTo: "6999", periodFrom: "2026-01", periodTo: "2026-03", mapping: "DEPT: C100 > C150" };
    expect(validateRealignment(draft)).toEqual({});
    expect(toRealignmentInput(draft)).toEqual({ filter: { accountFrom: "6000", accountTo: "6999", periodFrom: "2026-01", periodTo: "2026-03" }, mapping: { DEPT: { C100: "C150" } } });
    expect(toRealignmentInput({ ...draft, accountFrom: " ", accountTo: "" }).filter).toEqual({ periodFrom: "2026-01", periodTo: "2026-03" });
    expect(validateRealignment({ ...draft, periodFrom: "2026-13" }).periodFrom).toBeDefined();
    expect(validateRealignment({ ...draft, periodTo: "2025-12" }).periodTo).toContain("before");
    expect(validateRealignment({ ...draft, periodFrom: "2020-01" }).periodTo).toContain("60");
    expect(validateRealignment({ ...draft, accountFrom: "7", accountTo: "6" }).accountTo).toBeDefined();
    expect(validateRealignment({ ...draft, accountFrom: "no good" }).accountFrom).toBeDefined();
    expect(validateRealignment({ ...draft, mapping: "" }).mapping).toContain("at least one");
  });
});

describe("realignment list", () => {
  it("asks for a ledger first, then lists runs and opens one", async () => {
    fake();
    const { container } = renderAt("/ledger/realignments");
    expect(screen.getByText("Enter a ledger id to see its realignment runs.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER } });
    fireEvent.click(screen.getByRole("button", { name: "Show ledger" }));
    const open = await screen.findByRole("button", { name: "2026-03-15" });
    const row = open.closest("tr") as HTMLElement;
    expect(row.textContent).toContain("DEPT: C100 to C150");
    expect(row.textContent).toContain("750.00 USD");
    expect(row.textContent).toContain("Proposed");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(open);
    expect(screen.getByTestId("location").textContent).toBe(`/ledger/realignments/${RUN_ID}`);
  });

  it("shows the empty state", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt(`/ledger/realignments?ledger=${LEDGER}`);
    expect(await screen.findByText("No realignment runs yet")).toBeInTheDocument();
  });

  it("shows the error with a retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(404, { code: "LEDGER_NOT_FOUND", detail: "ledger not found" }) : json(200, { items: [proposed] }));
    renderAt(`/ledger/realignments?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the realignment runs")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "2026-03-15" })).toBeInTheDocument();
  });
});

describe("new realignment", () => {
  it("simulates a run with an Idempotency-Key and opens it", async () => {
    fake();
    const { container } = renderAt(`/ledger/realignments/new?ledger=${LEDGER}`);
    fireEvent.change(screen.getByLabelText("First period"), { target: { value: "2026-01" } });
    fireEvent.change(screen.getByLabelText("Last period"), { target: { value: "2026-03" } });
    fireEvent.change(screen.getByLabelText("Old and new values"), { target: { value: "DEPT: C100 > C150" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Simulate realignment" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/ledger/realignments/${RUN_ID}`));
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ filter: { periodFrom: "2026-01", periodTo: "2026-03" }, mapping: { DEPT: { C100: "C150" } } });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("blocks a bad form before any request, shows service errors and needs a ledger", async () => {
    fake();
    renderAt(`/ledger/realignments/new?ledger=${LEDGER}`);
    fireEvent.click(screen.getByRole("button", { name: "Simulate realignment" }));
    expect(screen.getByText("Enter the first period as YYYY-MM.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("First period"), { target: { value: "2026-01" } });
    fireEvent.change(screen.getByLabelText("Last period"), { target: { value: "2026-03" } });
    fireEvent.change(screen.getByLabelText("Old and new values"), { target: { value: "COLOR: A > B" } });
    server.handler = () => json(422, { code: "LEDGER_REALIGNMENT_INVALID", detail: "COLOR is not a dimension attribute of the ledger" });
    fireEvent.click(screen.getByRole("button", { name: "Simulate realignment" }));
    expect(await screen.findByText(/COLOR is not a dimension attribute of the ledger \(LEDGER_REALIGNMENT_INVALID\)/)).toBeInTheDocument();
  });

  it("asks to open the page from the list when no ledger is chosen", () => {
    renderAt("/ledger/realignments/new");
    expect(screen.getByText(/Open this page from the realignment list/)).toBeInTheDocument();
  });
});

describe("realignment run", () => {
  it("shows the simulation and posts it with a reason code", async () => {
    fake();
    const { container } = renderAt(`/ledger/realignments/${RUN_ID}?ledger=${LEDGER}`);
    expect(await screen.findByRole("table", { name: "Affected balances" })).toBeInTheDocument();
    expect(screen.getByText("Proposed")).toBeInTheDocument();
    expect(screen.getAllByText("6010-C150")).toHaveLength(2);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Post realignment" }));
    expect(await screen.findByText("Enter the reason code of the realignment.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Reason code"), { target: { value: "reorg" } });
    fireEvent.click(screen.getByRole("button", { name: "Post realignment" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname.endsWith(":post"))).toBe(true));
    const call = server.calls.find((c) => c.url.pathname.endsWith(":post"))!;
    expect(call.body).toEqual({ reasonCode: "REORG" });
    expect(new Headers(call.init.headers).get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("cancels a proposed run without a reason", async () => {
    fake();
    renderAt(`/ledger/realignments/${RUN_ID}?ledger=${LEDGER}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel run" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname.endsWith(":cancel"))).toBe(true));
    expect(server.calls.find((c) => c.url.pathname.endsWith(":cancel"))!.body).toEqual({});
  });

  it("reverses a posted run and shows a refusal from the service", async () => {
    fake(posted);
    renderAt(`/ledger/realignments/${RUN_ID}?ledger=${LEDGER}`);
    expect(await screen.findByText(/Reason REORG, posted by controller/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "0190a000" })).toHaveAttribute("href", "/ledger/entries/0190a000-0000-7000-8000-0000000000f1");
    expect(screen.getByLabelText("Reason code")).toHaveValue("REORG");
    server.handler = () => json(422, { code: "LEDGER_REALIGNMENT_PERIOD_CLOSED", detail: "period 2026-01 is closed" });
    fireEvent.click(screen.getByRole("button", { name: "Reverse realignment" }));
    expect(await screen.findByText(/period 2026-01 is closed \(LEDGER_REALIGNMENT_PERIOD_CLOSED\)/)).toBeInTheDocument();
    expect(server.calls.find((c) => c.url.pathname.endsWith(":reverse"))!.body).toEqual({ reasonCode: "REORG" });
  });

  it("reverses into an open period with a reversal date", async () => {
    fake(posted);
    renderAt(`/ledger/realignments/${RUN_ID}?ledger=${LEDGER}`);
    fireEvent.change(await screen.findByLabelText(/Reversal date/), { target: { value: "2026-03-15" } });
    fireEvent.click(screen.getByRole("button", { name: "Reverse realignment" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname.endsWith(":reverse"))).toBe(true));
    expect(server.calls.find((c) => c.url.pathname.endsWith(":reverse"))!.body).toEqual({ reasonCode: "REORG", postingDate: "2026-03-15" });
  });

  it("lists the reversing entries of a reversed run and offers no action", async () => {
    fake({ ...posted, status: "reversed", reversalEntryIds: ["0190a000-0000-7000-8000-0000000000f2"] });
    renderAt(`/ledger/realignments/${RUN_ID}?ledger=${LEDGER}`);
    expect(await screen.findByText(/Reversing entries/)).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "0190a000" })[1]).toHaveAttribute("href", "/ledger/entries/0190a000-0000-7000-8000-0000000000f2");
    expect(screen.queryByRole("button", { name: "Reverse realignment" })).not.toBeInTheDocument();
  });

  it("shows the error with a retry and the empty simulation", async () => {
    let fail = true;
    server.handler = () => (fail ? json(404, { code: "LEDGER_REALIGNMENT_NOT_FOUND", detail: "realignment run not found" }) : json(200, { ...proposed, preview: [], combinationsAffected: 0 }));
    renderAt(`/ledger/realignments/${RUN_ID}?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the realignment run")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Nothing to move")).toBeInTheDocument();
  });
});

describe("realignment entries", () => {
  it("links a realignment entry to its run instead of offering a reversal", async () => {
    const ENTRY = "0190a000-0000-7000-8000-0000000000f1";
    server.handler = (url) => {
      if (url.pathname === "/api/v1/ledger/reason-codes") return json(200, { items: [] });
      if (url.pathname !== `/api/v1/ledger/journal-entries/${ENTRY}`) return undefined;
      return json(200, {
        id: ENTRY, ledgerId: LEDGER, voucher: "GL-000031", postingSequence: 31, postingDate: "2026-01-31", periodKey: "2026-01",
        description: "Dimension realignment 0190a000", traceId: "a".repeat(32), totals: { currency: "USD", debit: "150.00", credit: "150.00" },
        isRealignment: true, realignmentRunId: RUN_ID, reasonCode: "REORG",
        lines: [
          { id: "l1", lineNumber: 1, account: "6010", displayName: "6010-C100", debitCredit: "C", amount: "150.00" },
          { id: "l2", lineNumber: 2, account: "6010", displayName: "6010-C150", debitCredit: "D", amount: "150.00" },
        ],
      });
    };
    renderAt(`/ledger/entries/${ENTRY}`);
    expect(await screen.findByRole("link", { name: "Realignment run" })).toHaveAttribute("href", `/ledger/realignments/${RUN_ID}?ledger=${LEDGER}`);
    expect(screen.getByText(/reverse it from the run/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Reverse/ })).not.toBeInTheDocument();
  });
});
