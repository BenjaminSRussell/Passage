import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { exportFileUrl, type CutoverPlan, type PackRunDetail } from "./api/client";
import migrationFactory from "./module";

const PLAN = "0195f0a0-0000-7000-8000-0000000000c1";
const RUN = "0195f0a0-0000-7000-8000-0000000000c2";

type Reply = { status: number; body: unknown };
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: (url: URL) => Reply | undefined;
let urls: string[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  urls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = new URL(input, "http://localhost");
      urls.push(url.pathname + url.search);
      const reply = handler(url) ?? json(404, { code: "TEST-404", detail: `no fake for ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

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

const plan: CutoverPlan = {
  planId: PLAN,
  t0: "2027-01-01T00:00:00Z",
  packs: ["open_items"],
  entities: ["USMF"],
  abortOffsetMin: 2160,
  rolledBack: false,
  approvers: [],
  version: 2,
  createdAt: "2026-12-01T00:00:00Z",
  stepsDone: 0,
  stepsTotal: 1,
  openGoNoGo: [],
  abortDue: false,
  steps: [{ seq: 1, offsetMin: 0, name: "Load open items", owner: "Engineer", plannedDurationMin: 60, actualDurationMin: 0, status: "PENDING", goNoGo: false }],
};

describe("cutover record exports (9.49.15)", () => {
  it("links the five CSV files of the plan", async () => {
    handler = () => json(200, plan);
    const { container } = renderAt(`/migration-factory/cutover/${PLAN}`);
    expect(await screen.findByRole("heading", { name: "Cutover record" })).toBeInTheDocument();
    for (const [label, file] of [
      ["Download runbook (CSV)", "runbook.csv"],
      ["Download rollback plan (CSV)", "rollback-plan.csv"],
      ["Download go/no-go checklist (CSV)", "go-no-go.csv"],
      ["Download decisions (CSV)", "decisions.csv"],
      ["Download reconciliation (CSV)", "reconciliation.csv"],
    ] as const) {
      expect(screen.getByRole("link", { name: label })).toHaveAttribute("href", `/api/v1/cutover/plans/${PLAN}/export/${file}`);
    }
    expect(pageViolations(container)).toEqual([]);
  });

  it("builds the export URL from the plan id", () => {
    expect(exportFileUrl("a b", "runbook.csv")).toBe("/api/v1/cutover/plans/a%20b/export/runbook.csv");
  });

  it("shows no export card while the plan has not loaded, and none when it fails", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderAt(`/migration-factory/cutover/${PLAN}`);
    await waitFor(() => expect(urls.length).toBeGreaterThan(0));
    expect(screen.queryByRole("heading", { name: "Cutover record" })).not.toBeInTheDocument();
  });
});

describe("open items by side", () => {
  const run: PackRunDetail = {
    runId: RUN,
    pack: "open_items",
    sourceSystem: "AX2012",
    sourceCompany: "USMF",
    cutoffTs: "2026-12-31T00:00:00Z",
    inputHash: "h",
    status: "LOADED",
    recordsIn: 2,
    recordsLoaded: 1,
    recordsQuarantined: 1,
    alreadyPresent: 0,
    version: 1,
    createdAt: "2027-01-01T00:00:00Z",
    controlTotals: [],
    quarantined: [{ seq: 1, legacyId: "5001", rule: "REFERENTIAL", reason: 'party "V9" is not in master data', status: "OPEN", party: "V9", account: "2100", currency: "USD", amount: "-300.000000", ledger: "AP" }],
  };

  it("marks a quarantined payable in the quarantine table", async () => {
    handler = (url) => (url.pathname === `/api/v1/migration/runs/${RUN}` ? json(200, run) : undefined);
    renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText("Payable / V9 / 2100")).toBeInTheDocument();
  });

  it("offers the receivable and payable source tables in the legacy reference lookup", async () => {
    renderAt("/migration-factory/xref");
    const select = await screen.findByLabelText(/Pack table/);
    const options = Array.from((select as HTMLSelectElement).options).map((o) => o.value);
    expect(options).toEqual(expect.arrayContaining(["open_items", "open_items.receivable", "open_items.payable"]));
    fireEvent.change(select, { target: { value: "open_items.payable" } });
    expect((select as HTMLSelectElement).value).toBe("open_items.payable");
  });
});
