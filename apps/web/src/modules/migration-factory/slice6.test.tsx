import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findTable } from "../../test-utils";
import type { PackRunDetail, TargetLoad, TargetLoadDetail } from "./api/client";
import { loadNeedsAttention, parseCustomerMap, parseRecords } from "./domain";
import migrationFactory from "./module";
import { validatePackRun, type PackRunValues } from "./NewPackRunPage";

const RUN = "0195f0a0-0000-7000-8000-0000000000e1";
const LE = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const CUSTOMER = "11111111-1111-4111-8111-111111111111";

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

const staged: TargetLoad = {
  runId: RUN,
  target: "receivables",
  legalEntityId: LE,
  importSource: "ax2012.usmf",
  status: "STAGED",
  clean: false,
  staged: 4,
  created: 0,
  updated: 0,
  unchanged: 0,
  skipped: 0,
  rejected: 1,
  notAvailable: 1,
  pending: 2,
  attempts: 0,
  version: 1,
};

const run = (targetLoad?: TargetLoad): PackRunDetail => ({
  runId: RUN,
  pack: "open_items",
  sourceSystem: "AX2012",
  sourceCompany: "USMF",
  cutoffTs: "2026-12-31T00:00:00Z",
  inputHash: "ab".repeat(32),
  status: "RECONCILED",
  recordsIn: 4,
  recordsLoaded: 4,
  recordsQuarantined: 0,
  alreadyPresent: 0,
  version: 1,
  createdAt: "2027-01-01T04:10:00Z",
  controlTotals: [],
  quarantined: [],
  ...(targetLoad ? { targetLoad } : {}),
});

const line = (name: string, source: string, target: string, delta: string, status: "MATCH" | "MISMATCH") => ({
  name: `target.receivables.${name}`,
  key: { importSource: "ax2012.usmf", legalEntityId: LE, currency: "USD" },
  source,
  target,
  delta,
  status,
});

const rejects: TargetLoadDetail["rejects"] = [
  { seq: 2, legacyId: "BAD-AMT", ledger: "AR", status: "REJECTED", code: "AR-OPENING-INVALID", detail: "openAmount must not exceed originalAmount", side: "DEBIT", amount: "10.000000", currency: "USD" },
  { seq: 4, legacyId: "AP-1", ledger: "AP", status: "NOT_AVAILABLE", code: "MIG-TARGET-NOT-AVAILABLE", detail: "payables has no opening-item batch API yet", side: "CREDIT", amount: "300.000000", currency: "USD" },
];

const loaded: TargetLoadDetail = {
  ...staged,
  status: "RECONCILED",
  created: 2,
  pending: 0,
  attempts: 1,
  version: 3,
  finishedAt: "2027-01-01T04:20:00Z",
  reconciliation: [line("item_count", "2.000000", "2.000000", "0.000000", "MATCH"), line("debit_open", "102.500000", "102.500000", "0.000000", "MATCH")],
  rejects,
};

describe("receivables extract parsing", () => {
  it("reads the document dates, optional ledger, kind, customer and original amount", () => {
    const ok = parseRecords(
      ["legacyId,party,account,currency,amount,documentDate,dueDate,ledger,kind,documentNumber,customerId,originalAmount", `INV-1,C001,1100,usd,125.50,2026-01-05,2026-02-04`, `CN-1,C001,1100,USD,-20,2026-01-06,2026-01-06,ar,credit_note,CN/1,${CUSTOMER.toUpperCase()},25.00`, "AP-1,V001,2100,USD,-300,2026-01-01,2026-02-01,AP"].join("\n"),
      { forTarget: true },
    );
    expect(ok.errors).toEqual([]);
    expect(ok.records).toEqual([
      { legacyId: "INV-1", party: "C001", account: "1100", currency: "USD", amount: "125.50", documentDate: "2026-01-05", dueDate: "2026-02-04" },
      { legacyId: "CN-1", party: "C001", account: "1100", currency: "USD", amount: "-20", documentDate: "2026-01-06", dueDate: "2026-01-06", ledger: "AR", kind: "CREDIT_NOTE", documentNumber: "CN/1", customerId: CUSTOMER, originalAmount: "25.00" },
      { legacyId: "AP-1", party: "V001", account: "2100", currency: "USD", amount: "-300", documentDate: "2026-01-01", dueDate: "2026-02-01", ledger: "AP" },
    ]);
  });

  it("names each bad line", () => {
    const bad = parseRecords(
      ["A,C,1100,USD,1", "B,C,1100,USD,x,2026-01-01,2026-01-02", "C,C,1100,USD,1,01/02/2026,2026-01-02", "D,C,1100,USD,1,2026-01-01,2026-01-02,GL", "E,C,1100,USD,1,2026-01-01,2026-01-02,AR,SALE", "F,C,1100,USD,1,2026-01-01,2026-01-02,AR,,,not-a-uuid", "G,C,1100,USD,1,2026-01-01,2026-01-02,AR,,,,-5"].join("\n"),
      { forTarget: true },
    );
    expect(bad.records).toEqual([]);
    expect(bad.errors.map((e) => e.replace(/\(.*\)/, "()"))).toEqual([
      "Line 1: expected 7 to 12 values (), found 5.",
      'Line 2: amount "x" is not a decimal such as 1250.00.',
      'Line 3: documentDate "01/02/2026" is not a YYYY-MM-DD date.',
      'Line 4: ledger "GL" must be AR or AP.',
      "Line 5: kind \"SALE\" must be INVOICE, CREDIT_NOTE, PAYMENT, ADJUSTMENT.",
      'Line 6: customerId "not-a-uuid" is not a UUID.',
      'Line 7: originalAmount "-5" is not a positive decimal.',
    ]);
  });

  it("parses the customer map and flags a malformed entry", () => {
    expect(parseCustomerMap(`C001=${CUSTOMER}\nC002 = ${CUSTOMER.toUpperCase()}`)).toEqual({ map: { C001: CUSTOMER, C002: CUSTOMER }, errors: [] });
    expect(parseCustomerMap("C001=nope, =x").errors).toHaveLength(2);
  });

  it("flags the load statuses that need a person", () => {
    expect(["MISMATCH", "PARTIAL", "UNRECONCILED", "FAILED"].every(loadNeedsAttention)).toBe(true);
    expect(["STAGED", "LOADING", "RECONCILED"].some(loadNeedsAttention)).toBe(false);
  });
});

describe("validatePackRun with a load", () => {
  const values: PackRunValues = {
    sourceSystem: "AX2012",
    sourceCompany: "USMF",
    cutoff: "2026-12-31T00:00",
    freeze: "2026-12-31T06:00",
    quarantineMaxPct: "",
    parties: "C001",
    records: "INV-1,C001,1100,USD,125.50,2026-01-05,2026-02-04",
    loadIntoReceivables: true,
    legalEntityId: ` ${LE.toUpperCase()} `,
    importSource: "",
    customers: `C001=${CUSTOMER}`,
  };

  it("adds the load object and keeps the old request when the box is off", () => {
    const { errors, input } = validatePackRun(values);
    expect(errors).toEqual({});
    expect(input?.load).toEqual({ target: "receivables", legalEntityId: LE, customerIds: { C001: CUSTOMER } });
    expect(input?.records[0]).toMatchObject({ documentDate: "2026-01-05", dueDate: "2026-02-04" });
    const plain = validatePackRun({ ...values, loadIntoReceivables: false, records: "INV-1,C001,1100,USD,125.50" });
    expect(plain.input?.load).toBeUndefined();
    expect(plain.input?.records[0]).toEqual({ legacyId: "INV-1", party: "C001", account: "1100", currency: "USD", amount: "125.50" });
  });

  it("refuses a bad legal entity, import source and customer map", () => {
    const { errors, input } = validatePackRun({ ...values, legalEntityId: "USMF", importSource: "has space", customers: "C001" });
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["customers", "importSource", "legalEntityId"]);
  });
});

describe("Start pack run with a load", () => {
  it("posts the load object and opens the run", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(201, run(staged));
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return json(200, run(staged));
      if (url.pathname.endsWith("/target-load")) return json(200, { ...staged, reconciliation: [], rejects });
      return undefined;
    };
    const { container } = renderAt("/migration-factory/runs/new");
    expect(screen.queryByLabelText("Legal entity")).not.toBeInTheDocument();
    fireEvent.click(screen.getByLabelText(/Load the accepted open items into receivables/));
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legacy company"), { target: { value: "USMF" } });
    fireEvent.change(screen.getByLabelText("Extract cutoff (UTC)"), { target: { value: "2026-12-31T00:00" } });
    fireEvent.change(screen.getByLabelText("Legacy freeze (UTC)"), { target: { value: "2026-12-31T06:00" } });
    fireEvent.change(screen.getByLabelText("Parties in master data"), { target: { value: "C001" } });
    fireEvent.change(screen.getByLabelText("Open items"), { target: { value: "INV-1,C001,1100,USD,125.50,2026-01-05,2026-02-04" } });
    fireEvent.click(screen.getByRole("button", { name: "Run open_items" }));
    expect(await screen.findByText("Choose the legal entity the open items belong to.")).toBeInTheDocument();
    expect(posts("/runs")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText(/^Customers/), { target: { value: `C001=${CUSTOMER}` } });
    fireEvent.click(screen.getByRole("button", { name: "Run open_items" }));
    expect(await screen.findByRole("heading", { name: "Pack run open_items" })).toBeInTheDocument();
    expect(posts("/runs")[0].body).toMatchObject({ load: { target: "receivables", legalEntityId: LE, customerIds: { C001: CUSTOMER } } });
  });
});

describe("Pack run: load into receivables", () => {
  it("shows no load card for a run without a load object", async () => {
    handler = (url) => (url.pathname === `/api/v1/migration/runs/${RUN}` ? json(200, run()) : undefined);
    renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText("Every control total matches the legacy extract.")).toBeInTheDocument();
    expect(screen.queryByText("Load into receivables")).not.toBeInTheDocument();
  });

  it("loads on request, then shows the results, the reconciliation and the rejects with the target's code", async () => {
    let done = false;
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname === `/api/v1/migration/runs/${RUN}/load`) {
        done = true;
        return json(200, { ...loaded, reconciliation: undefined, rejects: undefined });
      }
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return json(200, run(done ? loaded : staged));
      if (url.pathname === `/api/v1/migration/runs/${RUN}/target-load`) return json(200, done ? loaded : { ...staged, reconciliation: [], rejects: [] });
      return undefined;
    };
    const { container } = renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText("Ready to load")).toBeInTheDocument();
    expect(screen.getByText("2 of 4 rows are ready to send to receivables.")).toBeInTheDocument();
    expect(await findTable({ name: "Reconciliation with receivables" })).toHaveTextContent("Not reconciled yet");
    fireEvent.click(screen.getByRole("button", { name: "Load into receivables" }));
    expect(await screen.findByText("Reconciled")).toBeInTheDocument();
    expect(posts("/load")).toHaveLength(1);
    expect(screen.getByText(/Receivables' totals match what was accepted/)).toBeInTheDocument();
    const rec = await findTable({ name: "Reconciliation with receivables" });
    expect(rec).toHaveTextContent("debit_open");
    expect(rec).toHaveTextContent("102.50");
    const rej = await findTable({ name: "Rejected and unavailable rows" });
    expect(rej).toHaveTextContent("AR-OPENING-INVALID");
    expect(rej).toHaveTextContent("openAmount must not exceed originalAmount");
    expect(rej).toHaveTextContent("MIG-TARGET-NOT-AVAILABLE");
    expect(rej).toHaveTextContent("-300.00 USD");
    // Reconciled with a reject outstanding: the button stays off until something is pending.
    expect(screen.getByRole("button", { name: "Load again" })).toBeDisabled();
    expect(pageViolations(container)).toEqual([]);
  });

  it("flags a mismatch instead of passing it", async () => {
    const mismatch: TargetLoadDetail = {
      ...loaded,
      status: "MISMATCH",
      reconciliation: [line("debit_open", "102.500000", "102.510000", "0.010000", "MISMATCH")],
      rejects: [],
      rejected: 0,
      notAvailable: 0,
    };
    handler = (url) => {
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return json(200, run(mismatch));
      if (url.pathname.endsWith("/target-load")) return json(200, mismatch);
      return undefined;
    };
    const { container } = renderAt(`/migration-factory/runs/${RUN}`);
    const alert = await screen.findByText(/receivables holds different totals than the factory sent/);
    expect(alert).toBeInTheDocument();
    expect(screen.getAllByText("Totals differ").length).toBeGreaterThan(0);
    const rec = await findTable({ name: "Reconciliation with receivables" });
    expect(rec).toHaveTextContent("0.01");
    expect(rec).toHaveTextContent("Mismatch");
    expect(screen.getByRole("button", { name: "Load again" })).toBeEnabled();
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains a partial load and resumes it", async () => {
    const partial = { ...staged, status: "PARTIAL" as const, pending: 3, attempts: 1, lastError: "receivables did not answer: gave up after 5 attempts", version: 2 };
    handler = (url, init) => {
      if (init.method === "POST") return json(200, { ...loaded, reconciliation: undefined, rejects: undefined });
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return json(200, run(partial));
      if (url.pathname.endsWith("/target-load")) return json(200, { ...partial, reconciliation: [], rejects: [] });
      return undefined;
    };
    renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText(/3 row\(s\) are still pending because receivables did not answer/)).toBeInTheDocument();
    expect(screen.getByText(/gave up after 5 attempts/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load again" }));
    await waitFor(() => expect(posts("/load")).toHaveLength(1));
  });

  it("shows the service's not-configured answer with its code", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(503, { code: "MIG-1520", detail: "the receivables target is not configured: set RECEIVABLES_URL" });
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return json(200, run(staged));
      if (url.pathname.endsWith("/target-load")) return json(200, { ...staged, reconciliation: [], rejects: [] });
      return undefined;
    };
    renderAt(`/migration-factory/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Load into receivables" }));
    const alert = await screen.findByText(/set RECEIVABLES_URL/);
    expect(alert).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Load into receivables" })).toBeEnabled();
  });

  it("keeps the counts and shows an error when the load detail can't be read", async () => {
    handler = (url) => {
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return json(200, run(staged));
      if (url.pathname.endsWith("/target-load")) return json(500, { code: "COM-0500", detail: "internal error" });
      return undefined;
    };
    renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText(/internal error/)).toBeInTheDocument();
    expect(screen.getByText("Created")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
