import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { clearResolvedOptions, setLookupFetcher } from "../../shell/lookup/entities";
import intercompany from "./module";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, method: string, body: unknown) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; body: unknown; headers: Headers }[];

beforeEach(() => {
  clearResolvedOptions();
  setLookupFetcher(async (url) => {
    if (url.includes("?")) {
      const entities = [{ id: "le-1", name: "Cedar Ridge Pumps, Inc.", code: "CRP-US" }, { id: "le-2", name: "Cedar Ridge Pumps Europe GmbH", code: "CRP-DE" }];
      return new Response(JSON.stringify({ items: entities }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    const id = decodeURIComponent(url.split("/").pop() ?? "");
    return new Response(JSON.stringify({ id, name: id === "le-1" ? "Cedar Ridge Pumps, Inc." : "Cedar Ridge Pumps Europe GmbH", code: id }), { status: 200, headers: { "Content-Type": "application/json" } });
  });
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      const method = init.method ?? "GET";
      calls.push({ method, url, body, headers: new Headers(init.headers) });
      const reply = handler(url, method, body) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => {
  vi.unstubAllGlobals();
  setLookupFetcher(null);
  clearResolvedOptions();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {intercompany.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const journal = {
  id: "j-1",
  pairId: "p-1",
  source: "journal",
  sourceRef: "p-1",
  postingDate: "2026-11-04",
  status: "compensated",
  failureCode: "IC-0845",
  failureDetail: "the ledger of CRP-DE refused its side; the entries already posted were reversed",
  createdBy: "dev",
  createdAt: "2026-11-04T10:00:00Z",
  entries: [
    { entity: "CRP-US", legalEntityId: "le-1", ledgerId: "L-US", currency: "USD", legs: [{ account: "6100", side: "debit", amount: "100.00" }, { account: "1500", side: "credit", amount: "100.00" }], ledgerEntryId: "e-1", reversalEntryId: "e-2" },
    { entity: "CRP-DE", legalEntityId: "le-2", ledgerId: "L-DE", currency: "EUR", legs: [{ account: "2500", side: "debit", amount: "92.00" }, { account: "1000", side: "credit", amount: "92.00" }] },
  ],
};

describe("posted journals", () => {
  it("lists journals with their status and filters by it", async () => {
    handler = (url) => (url.pathname === "/api/v1/interco/journals" ? { status: 200, body: { items: [journal, { ...journal, id: "j-2", status: "posted" }] } } : undefined);
    const { container } = renderAt("/intercompany/journals");
    expect(await screen.findByText("Reversed")).toBeInTheDocument();
    expect(screen.getAllByText("Posted").length).toBeGreaterThan(1);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "compensated" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("status") === "compensated")).toBe(true));
  });

  it("shows each entity's entry by the legal entity's name and why a journal was reversed", async () => {
    handler = (url) => (url.pathname === "/api/v1/interco/journals/j-1" ? { status: 200, body: journal } : undefined);
    const { container } = renderAt("/intercompany/journals/j-1");
    expect(await screen.findByText(/the ledger of CRP-DE refused its side/)).toBeInTheDocument();
    expect((await screen.findAllByText(/Cedar Ridge Pumps Europe GmbH/)).length).toBeGreaterThan(0);
    expect(screen.queryByText("le-2")).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("posting from the preview", () => {
  const legs = [
    { entity: "A", account: "6100-EXPENSE", side: "debit", amount: "1000.00", currency: "USD" },
    { entity: "A", account: "1500-DUE-FROM-B", side: "credit", amount: "1000.00", currency: "USD" },
    { entity: "B", account: "2500-DUE-TO-A", side: "debit", amount: "1000.00", currency: "USD" },
    { entity: "B", account: "1000-CASH", side: "credit", amount: "1000.00", currency: "USD" },
  ];

  it("needs a posting date and a ledger for each entity before it posts", async () => {
    handler = (url, method) => (url.pathname === "/api/v1/interco/journals:preview" && method === "POST" ? { status: 200, body: { pairId: "p-9", balanced: true, legs } } : undefined);
    renderAt("/intercompany/journal");
    fireEvent.click(screen.getByRole("button", { name: "Preview journal" }));
    await screen.findByText("Both sides post as one linked pair in A and B", { exact: false });
    fireEvent.click(await screen.findByRole("button", { name: "Post journal" }));
    expect(await screen.findByText("Choose the posting date.")).toBeInTheDocument();
    expect(screen.getAllByText("Enter the ledger code.")).toHaveLength(2);
    expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/v1/interco/journals")).toBe(false);
  });

  it("posts with an Idempotency-Key and links to the posted journal", async () => {
    handler = (url, method) => {
      if (url.pathname === "/api/v1/interco/journals:preview") return { status: 200, body: { pairId: "p-9", balanced: true, legs } };
      if (url.pathname === "/api/v1/interco/journals" && method === "POST") return { status: 201, body: { ...journal, status: "posted" } };
      return undefined;
    };
    renderAt("/intercompany/journal");
    fireEvent.click(screen.getByRole("button", { name: "Preview journal" }));
    await screen.findByRole("heading", { name: "Post to the ledgers" });
    fireEvent.change(screen.getByLabelText("Posting date"), { target: { value: "2026-11-10" } });
    const ledgers = screen.getAllByLabelText("Ledger code");
    fireEvent.change(ledgers[0], { target: { value: "L-A" } });
    fireEvent.change(ledgers[1], { target: { value: "L-B" } });
    const user = userEvent.setup();
    const lookups = screen.getAllByRole("combobox", { name: "Legal entity" });
    await user.type(lookups[0], "cedar");
    await user.click(await screen.findByRole("option", { name: /Cedar Ridge Pumps, Inc\./ }));
    await user.type(lookups[1], "europe");
    await user.click(await screen.findByRole("option", { name: /Europe GmbH/ }));
    fireEvent.click(screen.getByRole("button", { name: "Post journal" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/v1/interco/journals")).toBe(true));
    const post = calls.find((c) => c.method === "POST" && c.url.pathname === "/api/v1/interco/journals");
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect((post?.body as { postingDate: string }).postingDate).toBe("2026-11-10");
  });
});

describe("charge log", () => {
  it("shows a charge without a transfer price as on suspense", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/interco/charges"
        ? {
            status: 200,
            body: {
              items: [
                { id: "c-1", sourceId: "TS-1", version: 1, lendingEntity: "A", borrowingEntity: "B", category: "Hour", resourceRole: "Designer", transactionDate: "2026-11-03", quantity: "8", unitCost: "60.00", currency: "USD", status: "suspense", failureCode: "IC-0821", createdBy: "dev", createdAt: "2026-11-03T10:00:00Z" },
                { id: "c-2", sourceId: "TS-2", version: 1, lendingEntity: "A", borrowingEntity: "B", category: "Hour", transactionDate: "2026-11-03", quantity: "8", unitCost: "60.00", currency: "USD", status: "posted", cost: "480.00", price: "528.00", margin: "48.00", createdBy: "dev", createdAt: "2026-11-03T10:00:00Z" },
              ],
            },
          }
        : undefined;
    const { container } = renderAt("/intercompany/charge-log");
    expect(await screen.findByText("On suspense: no transfer price")).toBeInTheDocument();
    expect(screen.getByText("528.00 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("reconciliation runs", () => {
  const run = { id: "r-1", period: "2026-10", currency: "USD", fxTolerancePct: "0.5", matched: 1, timing: 0, mismatch: 1, open: 1, blocking: true, createdBy: "dev", createdAt: "2026-11-02T10:00:00Z" };
  const items = [
    { id: "i-1", runId: "r-1", ref: "IC-1", entityA: "P", entityB: "S", amountA: "100.00", amountB: "100.00", nextPeriodA: "0.00", nextPeriodB: "0.00", fxApplies: false, difference: "0.00", status: "Matched", toleranceApplied: false, resolution: "none" },
    { id: "i-2", runId: "r-1", ref: "IC-2", entityA: "P", entityB: "S", amountA: "250.00", amountB: "0.00", nextPeriodA: "0.00", nextPeriodB: "0.00", fxApplies: false, difference: "250.00", status: "Mismatch", toleranceApplied: false, resolution: "open" },
  ];

  it("lists the runs with what is still open", async () => {
    handler = (url) => (url.pathname === "/api/v1/interco/reconciliations" ? { status: 200, body: { items: [run] } } : undefined);
    const { container } = renderAt("/intercompany/reconciliation-runs");
    expect(await screen.findByText("2026-10")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("resolves an open mismatch with a note and says when a note is missing", async () => {
    handler = (url, method) => {
      if (url.pathname === "/api/v1/interco/reconciliations/r-1") return { status: 200, body: { ...run, items } };
      if (url.pathname === "/api/v1/interco/reconciliations/items/i-2/resolve" && method === "POST") return { status: 200, body: { ...items[1], resolution: "resolved", note: "credit note raised" } };
      return undefined;
    };
    const { container } = renderAt("/intercompany/reconciliation-runs/r-1");
    expect(await screen.findByText("Mismatches block consolidation")).toBeInTheDocument();
    expect(screen.getByText("Nothing to do")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Resolve" }));
    fireEvent.click(await screen.findByRole("button", { name: "Mark resolved" }));
    expect(await screen.findByText("Say how the difference was settled.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("How it was settled"), { target: { value: "credit note raised" } });
    fireEvent.click(screen.getByRole("button", { name: "Mark resolved" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname.endsWith("/resolve"))).toBe(true));
    expect(calls.find((c) => c.url.pathname.endsWith("/resolve"))?.body).toEqual({ note: "credit note raised" });
  });

  it("saves the classified pairs as a run with an Idempotency-Key", async () => {
    handler = (url, method) => (url.pathname === "/api/v1/interco/reconciliations/run" && method === "POST" ? { status: 201, body: run } : url.pathname.startsWith("/api/v1/interco/reconciliations/r-1") ? { status: 200, body: { ...run, items } } : undefined);
    renderAt("/intercompany/reconciliation");
    fireEvent.click(screen.getByRole("button", { name: "Classify and save as a run" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname.endsWith("/run"))).toBe(true));
    const post = calls.find((c) => c.url.pathname.endsWith("/run"));
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    const body = post?.body as { period: string; currency: string; items: { period?: string }[] };
    expect(body.period).toBe("2026-11");
    expect(body.currency).toBe("USD");
    expect(body.items).toHaveLength(4);
    expect(body.items[0]).not.toHaveProperty("period");
  });
});
