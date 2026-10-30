import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findTable } from "../../test-utils";
import type { PackRunDetail } from "./api/client";
import { fromMicro, glImbalances, parseGLBalances, toMicro } from "./domain";
import migrationFactory from "./module";
import { validateGLRun, type GLRunValues } from "./NewGLRunPage";

const RUN = "0195f0a0-0000-7000-8000-0000000000d1";
const OPEN_ITEMS_RUN = "0195f0a0-0000-7000-8000-0000000000d0";

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

const balances = ["legacyId,account,dimensions,currency,balance", "GL-1,1100,USMF-100,USD,800.00", "GL-2,1100,,usd,450", "GL-3,3000,,USD,-1250.00"].join("\n");

const values: GLRunValues = {
  sourceSystem: "SAPECC",
  sourceCompany: " 1000 ",
  cutoff: "2026-12-31T00:00",
  freeze: "2026-12-31T18:00",
  quarantineMaxPct: "",
  accounts: "1100, 3000\n2100",
  openItemsRunId: "",
  balances,
};

const glRun: PackRunDetail = {
  runId: RUN,
  pack: "gl_balances",
  sourceSystem: "AX2012",
  sourceCompany: "DEMF",
  cutoffTs: "2026-12-31T00:00:00Z",
  inputHash: "cd".repeat(32),
  status: "LOADED",
  recordsIn: 3,
  recordsLoaded: 2,
  recordsQuarantined: 1,
  alreadyPresent: 0,
  openItemsRunId: OPEN_ITEMS_RUN,
  version: 1,
  createdAt: "2027-01-01T04:10:00Z",
  controlTotals: [
    {
      name: "gl_balances.balance",
      key: { company: "DEMF", account: "2100", dimensions: "", currency: "USD" },
      source: "40.990000",
      target: "40.990000",
      delta: "0.000000",
      tolerance: "0.000000",
      status: "MATCH",
    },
    {
      name: "gl_balances.control_account",
      key: { company: "DEMF", account: "2100", currency: "USD" },
      source: "40.990000",
      target: "41.000000",
      delta: "0.010000",
      tolerance: "0.000000",
      status: "MISMATCH",
    },
  ],
  quarantined: [
    { seq: 1, legacyId: "GL-9", rule: "REFERENTIAL", reason: 'account "9999" is not in the chart of accounts', status: "OPEN", account: "9999", dimensions: "DEMF-900", currency: "USD", amount: "-200.000000" },
  ],
  mismatchMessages: ["MIG-1506 Open items for control account 2100 (41.00 USD) differ from migrated GL balance (40.99 USD)."],
};

describe("GL extract helpers", () => {
  it("reads integer millionths exactly and refuses what is not a decimal", () => {
    expect(toMicro("0.1")).toBe(BigInt(100000));
    expect(toMicro(" -1250.000001 ")).toBe(BigInt(-1250000001));
    expect(toMicro("1e3")).toBeNull();
    expect(toMicro("1.0000001")).toBeNull();
    expect(fromMicro(BigInt(-1500000))).toBe("-1.50");
    expect(fromMicro(BigInt(42))).toBe("0.000042");
  });

  it("parses pasted balances with blank dimensions, skips the header and reports bad lines", () => {
    const ok = parseGLBalances(balances);
    expect(ok.errors).toEqual([]);
    expect(ok.records).toEqual([
      { legacyId: "GL-1", account: "1100", dimensions: "USMF-100", currency: "USD", balance: "800.00" },
      { legacyId: "GL-2", account: "1100", currency: "USD", balance: "450" },
      { legacyId: "GL-3", account: "3000", currency: "USD", balance: "-1250.00" },
    ]);
    const bad = parseGLBalances("GL-1,1100,,USD,1.000,00\n,1100,,USD,1\nGL-3,1100,,DOLLAR,1\nGL-4,1100,,USD,ten");
    expect(bad.records).toEqual([]);
    expect(bad.errors).toEqual([
      "Line 1: expected 5 values (legacyId, account, dimensions, currency, balance), found 6.",
      "Line 2: the legacy id and account are required.",
      'Line 3: currency "DOLLAR" is not a three-letter code.',
      'Line 4: balance "ten" is not a decimal such as -1250.00.',
    ]);
  });

  it("names each currency whose debits and credits differ, without float rounding", () => {
    const records = parseGLBalances("A,1100,,USD,0.10\nB,1100,,USD,0.20\nC,3000,,USD,-0.30\nD,1100,,EUR,5\nE,3000,,EUR,-4.99").records;
    expect(glImbalances(records)).toEqual([{ currency: "EUR", net: "0.01" }]);
    expect(glImbalances(records.slice(0, 3))).toEqual([]);
  });
});

describe("validateGLRun", () => {
  it("builds the request the service takes", () => {
    const { errors, input } = validateGLRun(values);
    expect(errors).toEqual({});
    expect(input).toEqual({
      sourceSystem: "SAPECC",
      sourceCompany: "1000",
      cutoffTs: "2026-12-31T00:00:00Z",
      freezeTs: "2026-12-31T18:00:00Z",
      accounts: ["1100", "3000", "2100"],
      records: parseGLBalances(balances).records,
    });
    expect(validateGLRun({ ...values, openItemsRunId: ` ${OPEN_ITEMS_RUN} `, quarantineMaxPct: "2.5" }).input).toMatchObject({
      openItemsRunId: OPEN_ITEMS_RUN,
      quarantineMaxPct: "2.5",
    });
  });

  it("refuses what the service would refuse", () => {
    const { errors, input } = validateGLRun({
      ...values,
      sourceCompany: "",
      cutoff: "2027-01-02T00:00",
      accounts: " , ",
      openItemsRunId: "OI-7",
      quarantineMaxPct: "11",
      balances: "GL-1,1100,,USD,100\nGL-2,3000,,USD,-99.99",
    });
    expect(input).toBeUndefined();
    expect(errors).toEqual({
      sourceCompany: "Enter the legacy company (DataAreaId or BUKRS).",
      cutoff: "The cutoff can't be after the legacy freeze (MIG-1501).",
      accounts: "List the chart of accounts the balances are checked against.",
      openItemsRunId: "Enter the id of an open_items run, or leave it blank.",
      quarantineMaxPct: "Enter a percentage from 0 to 10.",
      balances: "Debits and credits must agree in every currency: USD is off by 0.01.",
    });
    expect(validateGLRun({ ...values, balances: "  " }).errors.balances).toBe("Paste at least one balance.");
  });
});

describe("Load GL balances page", () => {
  function fill() {
    fireEvent.change(screen.getByLabelText("Legacy company"), { target: { value: "USMF" } });
    fireEvent.change(screen.getByLabelText("Extract cutoff (UTC)"), { target: { value: "2026-12-31T00:00" } });
    fireEvent.change(screen.getByLabelText("Legacy freeze (UTC)"), { target: { value: "2026-12-31T18:00" } });
    fireEvent.change(screen.getByLabelText("Chart of accounts"), { target: { value: "1100 3000" } });
    fireEvent.change(screen.getByLabelText(/^Open items run/), { target: { value: OPEN_ITEMS_RUN } });
    fireEvent.change(screen.getByLabelText("Balances"), { target: { value: balances } });
  }

  it("is reached from the pack runs page and posts the extract with an idempotency key, then opens the run", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/migration/runs") return json(200, { items: [] });
      if (init.method === "POST") return json(201, { ...glRun, sourceCompany: "USMF" });
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return json(200, glRun);
      return undefined;
    };
    const list = renderAt("/migration-factory");
    expect(await screen.findByRole("link", { name: "Load GL balances" })).toHaveAttribute("href", "/migration-factory/runs/new-gl");
    list.unmount();

    const { container } = renderAt("/migration-factory/runs/new-gl");
    expect(screen.getByRole("heading", { name: "Load GL balances" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Run gl_balances" }));
    expect(await screen.findByRole("heading", { name: "Pack run gl_balances" })).toBeInTheDocument();
    const [post] = posts("/migration/packs/gl_balances/runs");
    expect(post.body).toEqual({
      sourceSystem: "AX2012",
      sourceCompany: "USMF",
      cutoffTs: "2026-12-31T00:00:00Z",
      freezeTs: "2026-12-31T18:00:00Z",
      accounts: ["1100", "3000"],
      openItemsRunId: OPEN_ITEMS_RUN,
      records: parseGLBalances(balances).records,
    });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("shows an unbalanced paste on the field and sends nothing", async () => {
    renderAt("/migration-factory/runs/new-gl");
    fill();
    fireEvent.change(screen.getByLabelText("Balances"), { target: { value: "GL-1,1100,,USD,10\nGL-2,3000,,USD,-9" } });
    fireEvent.click(screen.getByRole("button", { name: "Run gl_balances" }));
    expect(await screen.findByText("Debits and credits must agree in every currency: USD is off by 1.00.")).toBeInTheDocument();
    expect(posts("/runs")).toHaveLength(0);
  });

  it("shows the service's refusal with its code and stays on the form", async () => {
    handler = () => json(422, { code: "MIG-0001", detail: "invalid input: account 1100 dimensions \"USMF-100\" currency USD already has an opening balance" });
    renderAt("/migration-factory/runs/new-gl");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Run gl_balances" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load the balances");
    expect(alert).toHaveTextContent(/already has an opening balance.*\(MIG-0001\)/);
    expect(screen.getByRole("button", { name: "Run gl_balances" })).toBeInTheDocument();
  });
});

describe("GL balances run detail", () => {
  it("shows MIG-1506, the open_items run it reconciled with, and quarantined balances with their dimensions", async () => {
    handler = (url) => (url.pathname === `/api/v1/migration/runs/${RUN}` ? json(200, glRun) : undefined);
    const { container } = renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText(glRun.mismatchMessages![0])).toBeInTheDocument();
    expect(screen.getByText("Open items differ from the GL")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: OPEN_ITEMS_RUN })).toHaveAttribute("href", `/migration-factory/runs/${OPEN_ITEMS_RUN}`);
    const totals = await findTable({ name: "Control totals" });
    expect(totals).toHaveTextContent("gl_balances.control_account");
    expect(totals).toHaveTextContent("account=2100, currency=USD");
    const quarantine = await findTable({ name: "Quarantined records" });
    expect(quarantine).toHaveTextContent("Account and dimensions");
    expect(quarantine).toHaveTextContent("9999 / DEMF-900");
    expect(within(quarantine).queryByRole("button", { name: /Release/ })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows no control-account alert when every total matches", async () => {
    handler = () => json(200, { ...glRun, status: "RECONCILED", openItemsRunId: undefined, mismatchMessages: undefined, quarantined: [], controlTotals: [glRun.controlTotals[0]] });
    renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText("Every control total matches the legacy extract.")).toBeInTheDocument();
    expect(screen.queryByText("Open items differ from the GL")).not.toBeInTheDocument();
    expect(screen.queryByText("Reconciled with open items")).not.toBeInTheDocument();
  });
});
