import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findTable } from "../../test-utils";
import type { PackRunDetail } from "./api/client";
import { parseOnhandLines } from "./domain";
import migrationFactory from "./module";
import { validateOnhandRun, type OnhandRunValues } from "./NewOnhandRunPage";

const RUN = "0195f0a0-0000-7000-8000-0000000000d1";
const GL_RUN = "0195f0a0-0000-7000-8000-0000000000d0";

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

const lines = [
  "legacyId,item,site,warehouse,location,batch,serial,unit,currency,quantity,value",
  "OH-1,BOLT-10,S1,W1,A-01,,,EA,USD,100,250.50",
  "OH-2,BOLT-10,S2,W1,,B7,,ea,usd,40.5,100.25",
  "OH-3,PUMP-1,S1,W1,A-02,,SN-1,EA,USD,1,400",
].join("\n");

const values: OnhandRunValues = {
  sourceSystem: "SAPECC",
  sourceCompany: " 1000 ",
  cutoff: "2026-12-31T00:00",
  freeze: "2026-12-31T18:00",
  quarantineMaxPct: "",
  items: "BOLT-10, PUMP-1\nNUT-2",
  glRunId: "",
  inventoryAccount: "",
  lines,
};

const onhandRun: PackRunDetail = {
  runId: RUN,
  pack: "onhand",
  sourceSystem: "AX2012",
  sourceCompany: "DEMF",
  cutoffTs: "2026-12-31T00:00:00Z",
  inputHash: "ef".repeat(32),
  status: "LOADED",
  recordsIn: 3,
  recordsLoaded: 2,
  recordsQuarantined: 1,
  alreadyPresent: 0,
  glRunId: GL_RUN,
  version: 1,
  createdAt: "2027-01-01T04:10:00Z",
  controlTotals: [
    {
      name: "onhand.value_entity",
      key: { company: "DEMF", currency: "USD" },
      source: "750.750000",
      target: "750.750000",
      delta: "0.000000",
      tolerance: "0.000000",
      status: "MATCH",
    },
    {
      name: "onhand.inventory_account",
      key: { company: "DEMF", account: "1400", currency: "USD" },
      source: "750.740000",
      target: "750.750000",
      delta: "0.010000",
      tolerance: "0.000000",
      status: "MISMATCH",
    },
  ],
  quarantined: [
    { seq: 1, legacyId: "OH-9", rule: "REFERENTIAL", reason: 'item "NOPE" is not in the item master', status: "OPEN", account: "NOPE", dimensions: "S1/W1/A-01//", currency: "USD", amount: "5.000000" },
  ],
  mismatchMessages: ["MIG-1502 Inventory account 1400 migrated balance (750.74 USD) differs from loaded on-hand value (750.75 USD)."],
};

describe("on-hand extract helpers", () => {
  it("parses pasted lines with blank dimensions, skips the header and keeps decimals as strings", () => {
    const ok = parseOnhandLines(lines);
    expect(ok.errors).toEqual([]);
    expect(ok.records).toEqual([
      { legacyId: "OH-1", item: "BOLT-10", site: "S1", warehouse: "W1", location: "A-01", unit: "EA", currency: "USD", quantity: "100", value: "250.50" },
      { legacyId: "OH-2", item: "BOLT-10", site: "S2", warehouse: "W1", batch: "B7", unit: "ea", currency: "USD", quantity: "40.5", value: "100.25" },
      { legacyId: "OH-3", item: "PUMP-1", site: "S1", warehouse: "W1", location: "A-02", serial: "SN-1", unit: "EA", currency: "USD", quantity: "1", value: "400" },
    ]);
  });

  it("reports each line the service would quarantine for its shape", () => {
    const bad = parseOnhandLines(
      [
        "OH-1,BOLT-10,S1,W1,,,,EA,USD,1,1,9",
        ",BOLT-10,S1,W1,,,,EA,USD,1,1",
        "OH-3,BOLT-10,,W1,,,,EA,USD,1,1",
        "OH-4,BOLT-10,S1,W1,,,,,USD,1,1",
        "OH-5,BOLT-10,S1,W1,,,,EA,DOLLAR,1,1",
        "OH-6,BOLT-10,S1,W1,,,,EA,USD,0,1",
        "OH-7,BOLT-10,S1,W1,,,,EA,USD,-2,1",
        "OH-8,BOLT-10,S1,W1,,,,EA,USD,1,-1",
        "OH-9,BOLT-10,S1,W1,,,SN-1,EA,USD,2,1",
        "OH-10,BOLT-10,S1,W1,,,,EA,USD,1e3,1",
      ].join("\n"),
    );
    expect(bad.records).toEqual([]);
    expect(bad.errors).toEqual([
      "Line 1: expected 11 values (legacyId, item, site, warehouse, location, batch, serial, unit, currency, quantity, value), found 12.",
      "Line 2: the legacy id and item are required.",
      "Line 3: the site and warehouse are required.",
      "Line 4: the unit of measure is required.",
      'Line 5: currency "DOLLAR" is not a three-letter code.',
      'Line 6: quantity "0" must be a decimal above zero such as 12.5.',
      'Line 7: quantity "-2" must be a decimal above zero such as 12.5.',
      'Line 8: value "-1" must be a decimal of zero or more such as 250.50.',
      'Line 9: serial "SN-1" holds quantity 2; a serial is exactly one.',
      'Line 10: quantity "1e3" must be a decimal above zero such as 12.5.',
    ]);
  });
});

describe("validateOnhandRun", () => {
  it("builds the request the service takes", () => {
    const { errors, input } = validateOnhandRun(values);
    expect(errors).toEqual({});
    expect(input).toEqual({
      sourceSystem: "SAPECC",
      sourceCompany: "1000",
      cutoffTs: "2026-12-31T00:00:00Z",
      freezeTs: "2026-12-31T18:00:00Z",
      items: ["BOLT-10", "PUMP-1", "NUT-2"],
      records: parseOnhandLines(lines).records,
    });
    expect(validateOnhandRun({ ...values, glRunId: ` ${GL_RUN} `, inventoryAccount: " 1400 ", quarantineMaxPct: "2.5" }).input).toMatchObject({
      glRunId: GL_RUN,
      inventoryAccount: "1400",
      quarantineMaxPct: "2.5",
    });
  });

  it("refuses what the service would refuse", () => {
    const { errors, input } = validateOnhandRun({
      ...values,
      sourceCompany: "",
      cutoff: "2027-01-02T00:00",
      items: " , ",
      glRunId: "GL-7",
      inventoryAccount: "",
      quarantineMaxPct: "11",
      lines: "OH-1,BOLT-10,S1,W1,,,,EA,USD,0,1",
    });
    expect(input).toBeUndefined();
    expect(errors).toEqual({
      sourceCompany: "Enter the legacy company (DataAreaId or company code).",
      cutoff: "The cutoff can't be after the legacy freeze (MIG-1501).",
      items: "List the item master the lines are checked against.",
      glRunId: "Enter the id of a gl_balances run, or leave it blank.",
      inventoryAccount: "Enter the inventory GL account to compare.",
      quarantineMaxPct: "Enter a percentage from 0 to 10.",
      lines: 'Line 1: quantity "0" must be a decimal above zero such as 12.5.',
    });
    expect(validateOnhandRun({ ...values, lines: "  " }).errors.lines).toBe("Paste at least one line.");
    expect(validateOnhandRun({ ...values, inventoryAccount: "1400" }).errors.glRunId).toBe("Name the gl_balances run that holds the inventory account.");
  });
});

describe("Load on-hand stock page", () => {
  function fill() {
    fireEvent.change(screen.getByLabelText("Legacy company"), { target: { value: "USMF" } });
    fireEvent.change(screen.getByLabelText("Extract cutoff (UTC)"), { target: { value: "2026-12-31T00:00" } });
    fireEvent.change(screen.getByLabelText("Legacy freeze (UTC)"), { target: { value: "2026-12-31T18:00" } });
    fireEvent.change(screen.getByLabelText("Item master"), { target: { value: "BOLT-10 PUMP-1" } });
    fireEvent.change(screen.getByLabelText(/^GL balances run/), { target: { value: GL_RUN } });
    fireEvent.change(screen.getByLabelText(/^Inventory GL account/), { target: { value: "1400" } });
    fireEvent.change(screen.getByLabelText("On-hand lines"), { target: { value: lines } });
  }

  it("is reached from the pack runs page and the catalog, posts the extract with an idempotency key, then opens the run", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/migration/runs") return json(200, { items: [] });
      if (init.method === "POST") return json(201, { ...onhandRun, sourceCompany: "USMF" });
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return json(200, onhandRun);
      return undefined;
    };
    const list = renderAt("/migration-factory");
    expect(await screen.findByRole("link", { name: "Load on-hand stock" })).toHaveAttribute("href", "/migration-factory/runs/new-onhand");
    list.unmount();

    const { container } = renderAt("/migration-factory/runs/new-onhand");
    expect(screen.getByRole("heading", { name: "Load on-hand stock" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Run onhand" }));
    expect(await screen.findByRole("heading", { name: "Pack run onhand" })).toBeInTheDocument();
    const [post] = posts("/migration/packs/onhand/runs");
    expect(post.body).toEqual({
      sourceSystem: "AX2012",
      sourceCompany: "USMF",
      cutoffTs: "2026-12-31T00:00:00Z",
      freezeTs: "2026-12-31T18:00:00Z",
      items: ["BOLT-10", "PUMP-1"],
      glRunId: GL_RUN,
      inventoryAccount: "1400",
      records: parseOnhandLines(lines).records,
    });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("starts onhand runs from the pack catalog", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/migration/packs"
        ? json(200, { items: [{ pack: "onhand", sourceSystems: ["AX2012"], controlTotals: "quantity per item", available: true }] })
        : undefined;
    renderAt("/migration-factory/packs");
    expect(await screen.findByRole("link", { name: "Start onhand run" })).toHaveAttribute("href", "/migration-factory/runs/new-onhand");
  });

  it("shows a bad paste on the field and sends nothing", async () => {
    renderAt("/migration-factory/runs/new-onhand");
    fill();
    fireEvent.change(screen.getByLabelText("On-hand lines"), { target: { value: "OH-1,BOLT-10,S1,W1,,,,EA,USD,-1,1" } });
    fireEvent.click(screen.getByRole("button", { name: "Run onhand" }));
    expect(await screen.findByText('Line 1: quantity "-1" must be a decimal above zero such as 12.5.')).toBeInTheDocument();
    expect(posts("/runs")).toHaveLength(0);
  });

  it("shows the service's refusal with its code and stays on the form", async () => {
    handler = () => json(422, { code: "MIG-0001", detail: "invalid input: item BOLT-10 already has opening stock" });
    renderAt("/migration-factory/runs/new-onhand");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Run onhand" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load the stock");
    expect(alert).toHaveTextContent(/already has opening stock.*\(MIG-0001\)/);
    expect(screen.getByRole("button", { name: "Run onhand" })).toBeInTheDocument();
  });
});

describe("on-hand run detail", () => {
  it("shows the inventory account message, the GL run it reconciled with, and quarantined lines with their dimension", async () => {
    handler = (url) => (url.pathname === `/api/v1/migration/runs/${RUN}` ? json(200, onhandRun) : undefined);
    const { container } = renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText(onhandRun.mismatchMessages![0])).toBeInTheDocument();
    expect(screen.getByText("Inventory account differs from the loaded stock")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: GL_RUN })).toHaveAttribute("href", `/migration-factory/runs/${GL_RUN}`);
    const totals = await findTable({ name: "Control totals" });
    expect(totals).toHaveTextContent("onhand.inventory_account");
    const quarantine = await findTable({ name: "Quarantined records" });
    expect(quarantine).toHaveTextContent("Item and dimension");
    expect(quarantine).toHaveTextContent("Quantity");
    expect(quarantine).toHaveTextContent("NOPE / S1/W1/A-01//");
    expect(within(quarantine).getByText("5.000000")).toBeInTheDocument();
    expect(quarantine).not.toHaveTextContent("5.00 USD");
    expect(screen.getByText("Staged, not posted to the inventory ledger")).toBeInTheDocument();
    expect(within(quarantine).getByRole("button", { name: /^Release record/ })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows no inventory alert when every total matches", async () => {
    handler = () => json(200, { ...onhandRun, status: "RECONCILED", glRunId: undefined, mismatchMessages: undefined, quarantined: [], controlTotals: [onhandRun.controlTotals[0]] });
    renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText("Every control total matches the legacy extract.")).toBeInTheDocument();
    expect(screen.queryByText("Inventory account differs from the loaded stock")).not.toBeInTheDocument();
    expect(screen.queryByText("Reconciled with GL balances")).not.toBeInTheDocument();
  });
});
