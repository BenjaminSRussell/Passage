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

const bankRun = {
  ...base,
  pack: "bank_balances",
  controlTotals: [{ name: "bank_balances.count", key: { company: "USMF" }, source: "3.000000", target: "2.000000", delta: "-1.000000", tolerance: "0.000000", status: "MISMATCH" }],
  quarantined: [{ seq: 1, legacyId: "BK-9", rule: "REFERENTIAL", reason: 'bank account "GONE" is not in the bank account master', status: "OPEN", account: "GONE", currency: "USD", amount: "5.000000" }],
} as PackRunDetail;

const onhandRun = {
  ...base,
  pack: "onhand",
  controlTotals: [{ name: "onhand.value_entity", key: { company: "USMF", currency: "USD" }, source: "9.000000", target: "4.000000", delta: "-5.000000", tolerance: "0.000000", status: "MISMATCH" }],
  quarantined: [
    { seq: 1, legacyId: "OH-9", rule: "REFERENTIAL", reason: 'item "NOPE" is not in the item master', status: "OPEN", account: "NOPE", dimensions: "S1/W1/A-01//", currency: "USD", amount: "5.000000" },
  ],
} as PackRunDetail;

describe("bank balance release", () => {
  it("sends the corrected bank account and the added master ids with the run version", async () => {
    const reconciled = {
      ...bankRun,
      status: "RECONCILED",
      recordsLoaded: 3,
      recordsQuarantined: 0,
      version: 2,
      quarantined: [{ ...bankRun.quarantined[0], status: "RELEASED", releasedBy: "ops", account: "GONE" }],
    };
    handler = (_url, init) => (init.method === "POST" ? json(200, reconciled) : json(200, bankRun));
    const { container } = renderAt(`/migration-factory/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release record BK-9" }));
    expect(screen.getByLabelText("Bank account")).toHaveValue("GONE");
    expect(screen.queryByLabelText("Party")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Bank account"), { target: { value: "SAV-USD" } });
    fireEvent.change(screen.getByLabelText("Bank accounts added to master data"), { target: { value: "SAV-USD, X-1" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Release record" }));
    expect(await screen.findByText(/Released BK-9\. Every control total now matches\./)).toBeInTheDocument();
    const [post] = posts("/quarantine/1/release");
    expect(post.body).toEqual({ bankAccount: "SAV-USD", parties: ["SAV-USD", "X-1"], version: 1 });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("shows a rule that still fails and keeps the form", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(422, { code: "MIG-0001", detail: 'invalid input: REFERENTIAL rule still fails: bank account "GONE" is not in the bank account master' }) : json(200, bankRun);
    renderAt(`/migration-factory/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release record BK-9" }));
    fireEvent.click(screen.getByRole("button", { name: "Release record" }));
    expect(await screen.findByText(/REFERENTIAL rule still fails: .* \(MIG-0001\)/)).toBeInTheDocument();
    expect(posts("/release")[0].body).toEqual({ version: 1 });
    expect(screen.getByLabelText("Bank account")).toBeInTheDocument();
  });

  it("explains a line stored before its whole line was kept", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(409, { code: "MIG-0004", detail: "quarantined record 1 was stored before its whole line was kept; re-extract and run the pack again" }) : json(200, bankRun);
    renderAt(`/migration-factory/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release record BK-9" }));
    fireEvent.click(screen.getByRole("button", { name: "Release record" }));
    expect(await screen.findByText(/re-extract and run the pack again \(MIG-0004\)/)).toBeInTheDocument();
  });
});

describe("on-hand line release", () => {
  it("sends only the changed item and the dimension fields that were filled in", async () => {
    const reconciled = { ...onhandRun, status: "RECONCILED", recordsLoaded: 3, recordsQuarantined: 0, version: 2, quarantined: [{ ...onhandRun.quarantined[0], status: "RELEASED", releasedBy: "ops" }] };
    handler = (_url, init) => (init.method === "POST" ? json(200, reconciled) : json(200, onhandRun));
    const { container } = renderAt(`/migration-factory/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release record OH-9" }));
    expect(screen.getByLabelText("Item")).toHaveValue("NOPE");
    for (const label of ["Site", "Warehouse", "Location", "Batch", "Unit"]) expect(screen.getByLabelText(new RegExp(`^${label}`))).toHaveValue("");
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: "BOLT-10" } });
    fireEvent.change(screen.getByLabelText(/^Site/), { target: { value: " S2 " } });
    fireEvent.change(screen.getByLabelText(/^Unit/), { target: { value: "EA" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Release record" }));
    expect(await screen.findByText(/Released OH-9\. Every control total now matches\./)).toBeInTheDocument();
    expect(posts("/release")[0].body).toEqual({ item: "BOLT-10", site: "S2", unit: "EA", version: 1 });
  });

  it("sends no item when it was not changed, and reloads the run on a version conflict", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(409, { code: "MIG-0006", detail: "run is at version 2" }) : json(200, onhandRun));
    renderAt(`/migration-factory/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release record OH-9" }));
    fireEvent.change(screen.getByLabelText(/^Warehouse/), { target: { value: "W2" } });
    fireEvent.click(screen.getByRole("button", { name: "Release record" }));
    expect(await screen.findByText(/Someone changed this run/)).toBeInTheDocument();
    expect(posts("/release")[0].body).toEqual({ warehouse: "W2", version: 1 });
    await waitFor(() => expect(calls.filter((c) => c.method === "GET").length).toBeGreaterThan(1));
  });

  it("offers no release once the run is reconciled, and none on a general ledger run", async () => {
    handler = () => json(200, { ...onhandRun, status: "RECONCILED" });
    const first = renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByRole("table", { name: "Quarantined records" })).toHaveTextContent("OH-9");
    expect(screen.queryByRole("button", { name: "Release record OH-9" })).not.toBeInTheDocument();
    first.unmount();
    handler = () => json(200, { ...onhandRun, pack: "gl_balances" });
    renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByRole("table", { name: "Quarantined records" })).toHaveTextContent("OH-9");
    expect(screen.queryByRole("button", { name: "Release record OH-9" })).not.toBeInTheDocument();
  });
});
