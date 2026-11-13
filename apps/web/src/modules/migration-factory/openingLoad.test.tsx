import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { setLookupFetcher } from "../../shell/lookup/entities";
import migrationFactory from "./module";
import { toOpeningRequest, validateOpeningForm } from "./OpeningLoadPanel";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, method: string, body: unknown) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const RUN = "0195f0a0-0000-7000-8000-0000000000d1";
const LE = "00000000-0000-0000-0000-0000000000e1";
const PRODUCT = "0195f0a0-0000-7000-8000-0000000000f1";
const run = {
  runId: RUN, pack: "onhand", sourceSystem: "AX2012", sourceCompany: "CRP", cutoffTs: "2026-12-31T00:00:00Z", inputHash: "h", status: "RECONCILED",
  recordsIn: 2, recordsLoaded: 2, recordsQuarantined: 0, alreadyPresent: 0, version: 1, createdAt: "2026-12-31T00:00:00Z", controlTotals: [], quarantined: [],
};

let handler: Handler;
let calls: { method: string; url: URL; body: unknown }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  const respond = async (input: string, init: RequestInit = {}) => {
    const url = new URL(input, "http://localhost");
    const method = init.method ?? "GET";
    const body = init.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, url, body });
    if (url.pathname.startsWith("/api/v1/org/legal-entities")) {
      const le = { id: LE, code: "CRP-US", name: "Cedar Ridge Pumps, Inc." };
      return new Response(JSON.stringify(url.pathname.endsWith(LE) ? le : { items: [le] }), { status: 200 });
    }
    if (url.pathname.startsWith("/api/v1/product/products")) {
      const p = { id: PRODUCT, productNumber: "PMP-200", name: "Centrifugal pump 200" };
      return new Response(JSON.stringify(url.pathname.endsWith(PRODUCT) ? p : { items: [p] }), { status: 200 });
    }
    const reply = handler(url, method, body) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${method} ${url.pathname}` } };
    return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
  };
  vi.stubGlobal("fetch", vi.fn(respond));
  setLookupFetcher((url, init) => respond(url, init));
});

afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {migrationFactory.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

describe("opening load form", () => {
  it("checks the legal entity, the date and the mapping rows, and builds the request", () => {
    const form = { legalEntityId: LE, postingDate: "2026-12-31", rows: [{ legacy: " PMP-200 ", id: PRODUCT }, { legacy: "", id: "" }] };
    expect(validateOpeningForm(form)).toEqual({});
    expect(toOpeningRequest(form, "onhand")).toEqual({ legalEntityId: LE, postingDate: "2026-12-31", products: { "PMP-200": PRODUCT } });
    expect(toOpeningRequest(form, "bank_balances")).toEqual({ legalEntityId: LE, postingDate: "2026-12-31", bankAccounts: { "PMP-200": PRODUCT } });
    expect(validateOpeningForm({ legalEntityId: "", postingDate: "", rows: [{ legacy: "A", id: "" }, { legacy: "A", id: PRODUCT }] })).toEqual({
      legalEntityId: expect.any(String),
      postingDate: expect.any(String),
      "rows.0.id": "Choose what it maps to.",
      "rows.1.legacy": "This legacy code is mapped twice.",
    });
  });

  it("loads a reconciled onhand run into inventory with the products picked by name", async () => {
    let loaded = false;
    handler = (url, method) => {
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return { status: 200, body: run };
      if (url.pathname === `/api/v1/migration/runs/${RUN}/opening-load` && method === "GET")
        return loaded
          ? { status: 200, body: { runId: RUN, target: "inventory", legalEntityId: LE, status: "RECONCILED", clean: true, staged: 2, loaded: 2, rejected: 0, pending: 0, attempts: 1, version: 2, reconciliation: [{ name: "target.onhand.value", key: { currency: "USD" }, source: "100", target: "100", delta: "0", status: "MATCH" }], rejects: [] } }
          : { status: 409, body: { code: "MIG-1563", detail: "the run has not been loaded" } };
      if (url.pathname === `/api/v1/migration/runs/${RUN}/opening-load` && method === "POST") {
        loaded = true;
        return { status: 200, body: { runId: RUN, target: "inventory", legalEntityId: LE, status: "RECONCILED", clean: true, staged: 2, loaded: 2, rejected: 0, pending: 0, attempts: 1, version: 2 } };
      }
      return undefined;
    };
    const { container } = renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByRole("heading", { name: "Load into the inventory ledger" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load into inventory" }));
    expect(await screen.findByText("Enter the cutover date.")).toBeInTheDocument();
    fireEvent.change(screen.getByRole("combobox", { name: "Legal entity" }), { target: { value: "Cedar" } });
    fireEvent.click(await screen.findByRole("option", { name: /Cedar Ridge Pumps, Inc\./ }));
    fireEvent.change(screen.getByLabelText("Posting date"), { target: { value: "2026-12-31" } });
    fireEvent.change(screen.getByLabelText(/Legacy item/), { target: { value: "PMP-200" } });
    fireEvent.change(screen.getByRole("combobox", { name: /Product/ }), { target: { value: "pump" } });
    fireEvent.click(await screen.findByRole("option", { name: /Centrifugal pump 200/ }));
    fireEvent.click(screen.getByRole("button", { name: "Load into inventory" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ legalEntityId: LE, postingDate: "2026-12-31", products: { "PMP-200": PRODUCT } });
    expect(await screen.findByRole("table", { name: "Reconciliation with inventory" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Load/ })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a load the target refused", async () => {
    handler = (url, method) => {
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return { status: 200, body: { ...run, pack: "bank_balances" } };
      if (url.pathname.endsWith("/opening-load") && method === "GET") return { status: 409, body: { code: "MIG-1563", detail: "not loaded" } };
      if (method === "POST") return { status: 503, body: { code: "MIG-1560", detail: "the treasury target is not configured" } };
      return undefined;
    };
    renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByRole("heading", { name: "Load into treasury" })).toBeInTheDocument();
    fireEvent.change(screen.getByRole("combobox", { name: "Legal entity" }), { target: { value: "Cedar" } });
    fireEvent.click(await screen.findByRole("option", { name: /Cedar Ridge Pumps, Inc\./ }));
    fireEvent.change(screen.getByLabelText("Balances as of"), { target: { value: "2026-12-31" } });
    fireEvent.click(screen.getByRole("button", { name: "Load into treasury" }));
    expect(await screen.findByText("The load did not run")).toBeInTheDocument();
  });
});
