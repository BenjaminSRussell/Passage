import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { PackRunDetail } from "./api/client";
import migrationFactory from "./module";

const RUN = "0195f0a0-0000-7000-8000-0000000000f1";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {migrationFactory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));

const base = {
  runId: RUN,
  sourceSystem: "AX2012",
  sourceCompany: "USMF",
  cutoffTs: "2026-12-31T00:00:00Z",
  inputHash: "cd".repeat(32),
  status: "LOADED",
  recordsIn: 3,
  recordsLoaded: 2,
  recordsQuarantined: 1,
  alreadyPresent: 0,
  version: 1,
  createdAt: "2027-01-01T04:10:00Z",
};

const ordersRun = {
  ...base,
  pack: "open_orders",
  controlTotals: [{ name: "open_orders.line_count", key: { company: "USMF", order_type: "SALES" }, source: "3.000000", target: "2.000000", delta: "-1.000000", tolerance: "0.000000", status: "MISMATCH" }],
  quarantined: [
    { seq: 1, legacyId: "SL-2", rule: "REFERENTIAL", reason: 'item "GONE" is not in the item master', status: "OPEN", party: "C1", account: "GONE", dimensions: "SALES/SO-2/1", currency: "USD", amount: "3.000000" },
    { seq: 2, legacyId: "PL-4", rule: "REFERENTIAL", reason: 'vendor "V9" is not in the vendor master', status: "OPEN", party: "V9", account: "BOLT", dimensions: "PURCHASE/PO-4/1", currency: "USD", amount: "2.000000" },
  ],
} as PackRunDetail;

describe("open order line release", () => {
  it("sends the corrected item, the changed fields and the added master ids with the run version", async () => {
    const reconciled = { ...ordersRun, status: "RECONCILED", recordsLoaded: 3, recordsQuarantined: 0, version: 2, quarantined: [{ ...ordersRun.quarantined[0], status: "RELEASED", releasedBy: "ops" }] };
    handler = (_url, init) => (init.method === "POST" ? json(200, reconciled) : json(200, ordersRun));
    const { container } = renderAt(`/migration-factory/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release record SL-2" }));
    expect(screen.getByLabelText("Item")).toHaveValue("GONE");
    expect(screen.getByLabelText("Customer")).toHaveValue("C1");
    expect(screen.queryByLabelText("Vendor")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: "WASHER" } });
    fireEvent.change(screen.getByLabelText(/^Delivery date/), { target: { value: " 2027-02-01 " } });
    fireEvent.change(screen.getByLabelText("Items added to master data"), { target: { value: "WASHER" } });
    fireEvent.change(screen.getByLabelText("Customers added to master data"), { target: { value: "C7, C8" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Release record" }));
    expect(await screen.findByText(/Released SL-2\. Every control total now matches\./)).toBeInTheDocument();
    const [post] = posts("/quarantine/1/release");
    expect(post.body).toEqual({ item: "WASHER", deliveryDate: "2027-02-01", customers: ["C7", "C8"], parties: ["WASHER"], version: 1 });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("asks for a vendor on a purchase order line and sends only the changed vendor", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(200, { ...ordersRun, version: 2 }) : json(200, ordersRun));
    renderAt(`/migration-factory/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release record PL-4" }));
    expect(screen.getByLabelText("Vendor")).toHaveValue("V9");
    fireEvent.change(screen.getByLabelText("Vendor"), { target: { value: "V1" } });
    fireEvent.click(screen.getByRole("button", { name: "Release record" }));
    await waitFor(() => expect(posts("/quarantine/2/release")).toHaveLength(1));
    expect(posts("/quarantine/2/release")[0].body).toEqual({ party: "V1", version: 1 });
  });

  it("shows a rule that still fails and keeps the form", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(422, { code: "MIG-0001", detail: 'invalid input: REFERENTIAL rule still fails: item "GONE" is not in the item master' }) : json(200, ordersRun);
    renderAt(`/migration-factory/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release record SL-2" }));
    fireEvent.click(screen.getByRole("button", { name: "Release record" }));
    expect(await screen.findByText(/REFERENTIAL rule still fails: .* \(MIG-0001\)/)).toBeInTheDocument();
    expect(posts("/release")[0].body).toEqual({ version: 1 });
    expect(screen.getByLabelText("Item")).toBeInTheDocument();
  });
});
