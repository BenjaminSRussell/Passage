import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Book } from "./api/client";
import { validateAddition } from "./AdditionPage";
import { validateAdjustment } from "./AdjustPage";
import { validateCapex } from "./CapexPage";
import { parseYearLines } from "./format";
import { validatePolicy, validateSeries } from "./InsurancePage";
import assets from "./module";
import { validateRun } from "./RunsPage";

const LE = "11111111-0000-4000-8000-000000000001";
const ASSET = "22222222-0000-4000-8000-000000000002";
const BOOK = "33333333-0000-4000-8000-000000000003";
const TAX = "33333333-0000-4000-8000-000000000004";
const RUN = "55555555-0000-4000-8000-000000000005";
const CAPEX = "66666666-0000-4000-8000-000000000006";
const VARIANT = "77777777-0000-4000-8000-000000000007";
const PARTY = "88888888-0000-4000-8000-000000000008";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = await handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
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
        {assets.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const posts = () => calls.filter((c) => c.method !== "GET");

function book(overrides: Partial<Book> = {}): Book {
  return {
    id: BOOK,
    assetId: ASSET,
    legalEntityId: LE,
    bookCode: "IFRS",
    currency: "USD",
    method: "StraightLine",
    factor: "0",
    switchToStraightLine: false,
    serviceLifePeriods: 60,
    salvageValue: "0",
    convention: "None",
    inServiceDate: "2026-03-01",
    cost: "10000",
    accumulatedDepreciation: "0",
    nbv: "10000",
    status: "Open",
    lastDepreciatedPeriod: null,
    ...overrides,
  };
}

const detail = {
  id: ASSET,
  legalEntityId: LE,
  assetNumber: "FA-000001",
  name: "Press",
  classCode: "MACHINE",
  barcode: null,
  quantity: "1",
  status: "Open",
  acquiredDate: "2026-03-01",
  bookCount: 2,
  version: 3,
  createdAt: "2026-03-01T09:00:00Z",
  books: [book(), book({ id: TAX, bookCode: "TAX" })],
};

const run = {
  id: RUN,
  legalEntityId: LE,
  bookCode: "IFRS",
  period: "2026-03",
  currency: "USD",
  status: "Proposed",
  assetCount: 1,
  exceptionCount: 1,
  totalAmount: "166.67",
  journalEntryId: null,
  version: 2,
  createdAt: "2026-04-01T08:00:00Z",
  postedAt: null,
  lines: [
    { assetBookId: BOOK, assetId: ASSET, assetNumber: "FA-000001", status: "Due", amount: "166.67", scheduledAmount: "166.67", exceptionCode: null, detail: null },
    { assetBookId: TAX, assetId: ASSET, assetNumber: "FA-000002", status: "Exception", amount: "0", scheduledAmount: "0", exceptionCode: "AST-0503", detail: "previous period 2026-02 is not depreciated" },
  ],
};

const capex = {
  id: CAPEX,
  legalEntityId: LE,
  number: "CR-1",
  title: "Press line",
  requesterId: "plant.manager",
  status: "Draft",
  currency: "USD",
  selectedVariantId: null,
  approvalLevel: null,
  requestedAmount: null,
  approvedAmount: null,
  version: 1,
  createdAt: "2026-04-01T08:00:00Z",
  variants: [{ id: VARIANT, position: 1, description: "Full line", amountsByYear: { "2027": "120000", "2026": "200000" }, totalAmount: "320000", inServiceDate: "2027-06-01", profileAssumption: "SL-120" }],
};

describe("module registration", () => {
  it("registers the slice-2 pages with nav entries behind their duties", () => {
    const paths = assets.routes.map((r) => r.path);
    for (const p of ["/assets/depreciation-runs", "/assets/depreciation-runs/:id", "/assets/capex", "/assets/capex/new", "/assets/capex/:id", "/assets/index-series", "/assets/:id/adjust", "/assets/:id/additions/new", "/assets/:id/insurance/new"]) {
      expect(paths).toContain(p);
    }
    expect(assets.nav.find((n) => n.to === "/assets/depreciation-runs")?.duty).toBe("assets.asset.depreciate");
    expect(assets.nav.find((n) => n.to === "/assets/capex")?.duty).toBe("assets.capex-request.create");
    expect(assets.nav.find((n) => n.to === "/assets/index-series")?.duty).toBe("assets.insurance.maintain");
  });

  it("parses year lines without floats", () => {
    expect(parseYearLines("2026=200000.00\n 2027: 120000 \n")).toEqual({ values: { "2026": "200000.00", "2027": "120000" } });
    expect(parseYearLines("2026=1\n2026=2").error).toContain("twice");
    expect(parseYearLines("next=1").error).toContain("not a year");
  });
});

describe("depreciation runs", () => {
  it("validates the period format", () => {
    expect(Object.keys(validateRun({ legalEntityId: "x", bookCode: "", period: "2026-13" }).errors).sort()).toEqual(["bookCode", "legalEntityId", "period"]);
    expect(validateRun({ legalEntityId: LE, bookCode: "ifrs", period: "2026" }).input).toEqual({ legalEntityId: LE, bookCode: "IFRS", period: "2026" });
  });

  it("shows the empty list and proposes a run, then opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: run, etag: '"2"' };
      if (url.pathname === "/api/v1/assets/depreciation-runs") return { status: 200, body: { items: [] } };
      return { status: 200, body: run, etag: '"2"' };
    };
    const { container } = renderAt("/assets/depreciation-runs");
    expect(await screen.findByText("No depreciation runs yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Propose run" }));
    expect(screen.getByText("Enter the legal entity's UUID.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-03" } });
    fireEvent.click(screen.getByRole("button", { name: "Propose run" }));
    expect(await screen.findByRole("heading", { name: "Depreciation IFRS 2026-03" })).toBeInTheDocument();
    const post = posts()[0];
    expect(post.url.pathname).toBe("/api/v1/assets/depreciation-runs");
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post.body).toEqual({ legalEntityId: LE, bookCode: "IFRS", period: "2026-03" });
  });

  it("explains a period that is already posted", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 409, body: { code: "AST-0502", detail: "depreciation for book IFRS period 2026-03 is already posted" } } : { status: 200, body: { items: [] } });
    renderAt("/assets/depreciation-runs");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-03" } });
    fireEvent.click(screen.getByRole("button", { name: "Propose run" }));
    expect(await findAlert()).toHaveTextContent("That period is already posted");
  });

  it("lists runs and shows the load error with a retry", async () => {
    let fail = true;
    handler = () => (fail ? { status: 503, body: { code: "COM-0500", detail: "internal error" } } : { status: 200, body: { items: [{ ...run, lines: undefined }] } });
    renderAt("/assets/depreciation-runs");
    expect(await findAlert()).toHaveTextContent("internal error (COM-0500)");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    const table = await findTable({ name: "Depreciation runs" });
    await waitFor(() => expect(within(table).getByText("166.67 USD")).toBeInTheDocument());
  });

  it("shows lines with exceptions and posts with the run ETag", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 200, body: { ...run, status: "Posted", version: 3, postedAt: "2026-04-01T09:00:00Z", lines: [{ ...run.lines[0], status: "Posted" }, run.lines[1]] }, etag: '"3"' }
        : { status: 200, body: run, etag: '"2"' };
    const { container } = renderAt(`/assets/depreciation-runs/${RUN}`);
    const lines = await findTable({ name: "Run lines" });
    expect(within(lines).getByText("AST-0503: previous period 2026-02 is not depreciated")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Run totals" })).toHaveTextContent("Books due 1");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Post run" }));
    expect(await findStatus()).toHaveTextContent("1 books posted 166.67 USD; 1 stayed out as exceptions.");
    expect(posts()[0].headers.get("If-Match")).toBe('"2"');
    expect(posts()[0].url.pathname).toBe(`/api/v1/assets/depreciation-runs/${RUN}/post`);
    expect(screen.queryByRole("button", { name: "Post run" })).not.toBeInTheDocument();
  });

  it("explains a run that was proposed again", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 412, body: { code: "AST-0006", detail: "the run was proposed again" } } : { status: 200, body: run, etag: '"2"' });
    renderAt(`/assets/depreciation-runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Post run" }));
    expect(await findAlert()).toHaveTextContent("The run was proposed again since you opened it");
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });
});

describe("adjustments", () => {
  it("validates the amount and the in-service date", () => {
    const b = book();
    expect(Object.keys(validateAdjustment({ bookId: BOOK, type: "Impairment", transactionDate: "2026-01-01", amount: "0" }, b).errors).sort()).toEqual(["amount", "transactionDate"]);
    expect(validateAdjustment({ bookId: BOOK, type: "Impairment", transactionDate: "2026-06-30", amount: "-500.00" }, b).input).toEqual({ bookId: BOOK, type: "Impairment", transactionDate: "2026-06-30", amount: "-500.00" });
    expect(validateAdjustment({ bookId: "", type: "Revaluation", transactionDate: "2026-06-30", amount: "5" }, undefined).errors.bookId).toBeTruthy();
  });

  it("posts an impairment reversal on the chosen book and shows the cap warning", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? {
            status: 201,
            body: {
              book: book({ id: TAX, bookCode: "TAX", accumulatedDepreciation: "0", nbv: "10000" }),
              transaction: { id: "t", assetBookId: TAX, bookCode: "TAX", currency: "USD", type: "Impairment", transactionDate: "2026-06-30", costAmount: "0", accumulatedAmount: "-1500", proceedsAmount: null, gainLoss: null, sourceType: "manual", createdAt: "" },
              limited: true,
              requested: "-2000",
              warnings: ["AST-0512: impairment reversal limited to 1500.00"],
            },
          }
        : { status: 200, body: detail, etag: '"3"' };
    const { container } = renderAt(`/assets/${ASSET}/adjust`);
    await screen.findByRole("heading", { name: "Adjust FA-000001" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Book"), { target: { value: TAX } });
    fireEvent.change(screen.getByLabelText("Transaction date"), { target: { value: "2026-06-30" } });
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "-2000" } });
    fireEvent.click(screen.getByRole("button", { name: "Post adjustment" }));
    expect(await findStatus()).toHaveTextContent("Impairment posted on TAX");
    expect(screen.getByText("AST-0512: impairment reversal limited to 1500.00")).toBeInTheDocument();
    expect(posts()[0].body).toEqual({ bookId: TAX, type: "Impairment", transactionDate: "2026-06-30", amount: "-2000" });
    expect(posts()[0].url.pathname).toBe(`/api/v1/assets/${ASSET}/transactions`);
  });

  it("shows the domain error from the service", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 422, body: { code: "AST-0014", detail: "impairment would make NBV negative" } } : { status: 200, body: detail, etag: '"3"' });
    renderAt(`/assets/${ASSET}/adjust`);
    fireEvent.change(await screen.findByLabelText("Amount"), { target: { value: "99999" } });
    fireEvent.change(screen.getByLabelText("Transaction date"), { target: { value: "2026-06-30" } });
    fireEvent.click(screen.getByRole("button", { name: "Post adjustment" }));
    expect(await findAlert()).toHaveTextContent("impairment would make NBV negative (AST-0014)");
  });
});

describe("additions", () => {
  const open = [book(), book({ id: TAX, bookCode: "TAX" })];
  const base = { type: "Improvement" as const, description: "Coolant", quantity: "1", serialNumber: "", addedDate: "2026-08-01", financial: true, cost: "2000", bookIds: [BOOK, TAX] };

  it("sends no bookIds when every open book is chosen and takes the books' currency", () => {
    expect(validateAddition(base, open).input).toEqual({ type: "Improvement", description: "Coolant", quantity: "1", addedDate: "2026-08-01", financial: true, cost: "2000", currency: "USD" });
    expect(validateAddition({ ...base, bookIds: [TAX] }, open).input?.bookIds).toEqual([TAX]);
    expect(validateAddition({ ...base, bookIds: [] }, open).errors.bookIds).toBeTruthy();
    expect(validateAddition({ ...base, bookIds: [BOOK, TAX] }, [book(), book({ id: TAX, currency: "EUR" })]).errors.bookIds).toBe("Choose books in one currency.");
    expect(validateAddition({ ...base, financial: false, cost: "" }, open).input).toEqual({ type: "Improvement", description: "Coolant", quantity: "1", addedDate: "2026-08-01", financial: false });
  });

  it("records a financial addition and returns to the asset", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: { id: "add-1" } };
      if (url.pathname.endsWith("/additions")) return { status: 200, body: { items: [{ id: "add-1", assetId: ASSET, type: "Improvement", description: "Coolant system", quantity: "1", serialNumber: null, addedDate: "2026-08-01", financial: true, cost: "2000", currency: "USD", createdAt: "", transactions: [{}, {}] }] } };
      if (url.pathname.endsWith("/insurance-policies") || url.pathname.endsWith("/transactions")) return { status: 200, body: { items: [] } };
      if (url.pathname.endsWith("/schedule")) return { status: 200, body: { bookId: BOOK, bookCode: "IFRS", currency: "USD", total: "0", lines: [] } };
      return { status: 200, body: detail, etag: '"3"' };
    };
    const { container } = renderAt(`/assets/${ASSET}/additions/new`);
    await screen.findByRole("heading", { name: "New addition to FA-000001" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Coolant system" } });
    fireEvent.click(screen.getByLabelText("Post the cost to the books"));
    fireEvent.change(screen.getByLabelText("Cost"), { target: { value: "2000" } });
    fireEvent.click(screen.getByRole("button", { name: "Record addition" }));
    const table = await findTable({ name: "Additions" });
    await waitFor(() => expect(within(table).getByText("2000 USD on 2 books")).toBeInTheDocument());
    expect(posts()[0].body).toMatchObject({ type: "Improvement", description: "Coolant system", financial: true, cost: "2000", currency: "USD" });
    expect(posts()[0].body).not.toHaveProperty("bookIds");
  });
});

describe("insurance", () => {
  const values = {
    insurerPartyId: PARTY,
    policyNumber: " P-1 ",
    insuranceType: "Property" as const,
    coverageFrom: "2026-01-01",
    coverageTo: "2026-12-31",
    premium: "",
    currency: "USD",
    basis: "Replacement" as const,
    baseValue: "100000.00",
    sumInsured: "95000.00",
    indexSeriesCode: "US-MACH",
    indexBaseYear: "2024",
    valuationYear: "",
  };

  it("validates the policy like the service", () => {
    expect(validatePolicy(values).input).toEqual({
      insurerPartyId: PARTY,
      policyNumber: "P-1",
      insuranceType: "Property",
      coverageFrom: "2026-01-01",
      coverageTo: "2026-12-31",
      currency: "USD",
      basis: "Replacement",
      baseValue: "100000.00",
      sumInsured: "95000.00",
      indexSeriesCode: "US-MACH",
      indexBaseYear: 2024,
    });
    expect(Object.keys(validatePolicy({ ...values, coverageTo: "2025-12-31", indexBaseYear: "", insurerPartyId: "x" }).errors).sort()).toEqual(["coverageTo", "indexBaseYear", "insurerPartyId"]);
    expect(validatePolicy({ ...values, indexSeriesCode: "", indexBaseYear: "" }).input).not.toHaveProperty("indexBaseYear");
  });

  it("records a policy and reports under-insurance (AC-37.9)", async () => {
    handler = (url, init) => {
      if (init.method === "POST") {
        return {
          status: 201,
          body: { ...validatePolicy(values).input, id: "p", assetId: ASSET, legalEntityId: LE, premium: "0", valuationYear: 2026, indexYearUsed: 2026, indexedValue: "105803.57", underInsured: true, warnings: ["AST-0532: the asset is under-insured: 95000.00 vs 105803.57 indexed"], createdAt: "" },
        };
      }
      if (url.pathname === "/api/v1/assets/index-series") return { status: 200, body: { items: [{ code: "US-MACH", country: "US", classCode: "MACHINE", values: [], updatedAt: "" }] } };
      return { status: 200, body: detail, etag: '"3"' };
    };
    const { container } = renderAt(`/assets/${ASSET}/insurance/new`);
    await screen.findByRole("heading", { name: "New insurance policy on FA-000001" });
    await screen.findByRole("option", { name: "US-MACH (US, MACHINE)" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Insurer"), { target: { value: PARTY } });
    fireEvent.change(screen.getByLabelText("Policy number"), { target: { value: "P-1" } });
    fireEvent.change(screen.getByLabelText("Base value"), { target: { value: "100000.00" } });
    fireEvent.change(screen.getByLabelText("Sum insured"), { target: { value: "95000.00" } });
    fireEvent.change(screen.getByLabelText("Index series"), { target: { value: "US-MACH" } });
    fireEvent.change(screen.getByLabelText("Index base year"), { target: { value: "2024" } });
    fireEvent.click(screen.getByRole("button", { name: "Record policy" }));
    expect(await findStatus()).toHaveTextContent("Indexed replacement value 105803.57 USD (index year 2026)");
    expect(screen.getByText("AST-0532: the asset is under-insured: 95000.00 vs 105803.57 indexed")).toBeInTheDocument();
    expect(posts()[0].body).toMatchObject({ indexSeriesCode: "US-MACH", indexBaseYear: 2024, baseValue: "100000.00" });
  });

  it("validates and loads index series values", async () => {
    expect(validateSeries({ code: "us", country: "USA", classCode: "", values: "2026" }).errors).toMatchObject({ country: expect.any(String), classCode: expect.any(String), values: expect.any(String) });
    let saved = false;
    handler = (_url, init) =>
      init.method === "PUT"
        ? ((saved = true), { status: 200, body: { code: "US-MACH", country: "US", classCode: "MACHINE", values: [{ year: 2024, value: "112.0" }, { year: 2026, value: "118.5" }], updatedAt: "2026-01-15T00:00:00Z" } })
        : { status: 200, body: { items: saved ? [{ code: "US-MACH", country: "US", classCode: "MACHINE", values: [{ year: 2024, value: "112.0" }, { year: 2026, value: "118.5" }], updatedAt: "2026-01-15T00:00:00Z" }] : [] } };
    const { container } = renderAt("/assets/index-series");
    expect(await screen.findByText("No index series yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Series code"), { target: { value: "us-mach" } });
    fireEvent.change(screen.getByLabelText("Asset class"), { target: { value: "machine" } });
    fireEvent.change(screen.getByLabelText("Values"), { target: { value: "2024=112.0\n2026=118.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save series" }));
    expect(await findStatus()).toHaveTextContent("2 years loaded.");
    const put = posts()[0];
    expect(put.url.pathname).toBe("/api/v1/assets/index-series/US-MACH");
    expect(put.body).toEqual({ country: "US", classCode: "MACHINE", values: [{ year: 2024, value: "112.0" }, { year: 2026, value: "118.5" }] });
    expect(await screen.findByText("2024: 112.0, 2026: 118.5")).toBeInTheDocument();
  });
});

describe("capex requests", () => {
  it("validates variants and their yearly amounts", () => {
    const values = { legalEntityId: LE, number: "CR-1", title: "Press line", requesterId: "pm", currency: "USD", variants: [{ description: "Full", amounts: "2026=200000\n2027=120000", inServiceDate: "2027-06-01", profileAssumption: "SL-120" }] };
    expect(validateCapex(values).input?.variants[0].amountsByYear).toEqual({ "2026": "200000", "2027": "120000" });
    const bad = validateCapex({ ...values, variants: [{ ...values.variants[0], amounts: "2026=0", profileAssumption: "" }] }).errors;
    expect(bad.variantFields?.[0]).toMatchObject({ amounts: "Every amount must be above 0.", profileAssumption: expect.any(String) });
    expect(validateCapex({ ...values, variants: [] }).errors.variants).toBeTruthy();
  });

  it("lists requests and filters by status", async () => {
    handler = () => ({ status: 200, body: { items: [{ ...capex, status: "InApproval", approvalLevel: "CFO", requestedAmount: "320000", variants: undefined }] } });
    const { container } = renderAt("/assets/capex");
    const table = await findTable({ name: "Capex requests" });
    await waitFor(() => expect(within(table).getByText("320000 USD")).toBeInTheDocument());
    expect(within(table).getByText("CFO")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "InApproval" } });
    await waitFor(() => expect(calls.at(-1)?.url.search).toBe("?status=InApproval"));
  });

  it("creates a request with two variants and opens it", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: capex, etag: '"1"' } : { status: 200, body: capex, etag: '"1"' });
    const { container } = renderAt("/assets/capex/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Request number"), { target: { value: "CR-1" } });
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Press line" } });
    fireEvent.change(screen.getByLabelText("Requester"), { target: { value: "plant.manager" } });
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Full line" } });
    fireEvent.change(screen.getByLabelText("Depreciation profile"), { target: { value: "SL-120" } });
    fireEvent.change(screen.getByLabelText("Amounts by year"), { target: { value: "2026=200000\n2027=120000" } });
    fireEvent.click(screen.getByRole("button", { name: "Add variant" }));
    const second = screen.getByRole("group", { name: "Variant 2" });
    fireEvent.change(within(second).getByLabelText("Description"), { target: { value: "Refurbish" } });
    fireEvent.change(within(second).getByLabelText("Depreciation profile"), { target: { value: "SL-60" } });
    fireEvent.change(within(second).getByLabelText("Amounts by year"), { target: { value: "2026=40000" } });
    fireEvent.click(screen.getByRole("button", { name: "Create request" }));
    expect(await screen.findByRole("heading", { name: "CR-1 Press line" })).toBeInTheDocument();
    const post = posts()[0];
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post.body).toMatchObject({ number: "CR-1", currency: "USD", variants: [{ description: "Full line", amountsByYear: { "2026": "200000", "2027": "120000" } }, { description: "Refurbish", amountsByYear: { "2026": "40000" } }] });
  });

  it("submits a draft with If-Match and shows the CFO routing (AC-37.11)", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 200, body: { ...capex, status: "InApproval", approvalLevel: "CFO", requestedAmount: "320000", selectedVariantId: VARIANT, version: 2 }, etag: '"2"' }
        : { status: 200, body: capex, etag: '"1"' };
    const { container } = renderAt(`/assets/capex/${CAPEX}`);
    const variants = await findTable({ name: "Variants" });
    expect(within(variants).getByText("2026: 200000, 2027: 120000")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Submit request" }));
    expect(await findStatus()).toHaveTextContent("Routed to CFO for 320000 USD.");
    expect(posts()[0].headers.get("If-Match")).toBe('"1"');
    expect(posts()[0].body).toEqual({ variantId: VARIANT });
    expect(screen.queryByRole("button", { name: "Submit request" })).not.toBeInTheDocument();
  });

  it("reports an unknown request", async () => {
    handler = () => ({ status: 404, body: { code: "AST-0021", detail: "no capex request with id x" } });
    renderAt(`/assets/capex/${CAPEX}`);
    expect(await findAlert()).toHaveTextContent("no capex request with id x (AST-0021)");
  });
});

describe("asset detail, slice 2", () => {
  it("shows additions and insurance policies with the under-insured flag, and the new actions", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/additions")) return { status: 200, body: { items: [] } };
      if (url.pathname.endsWith("/insurance-policies")) {
        return { status: 200, body: { items: [{ id: "p", policyNumber: "P-1", insuranceType: "MachineryBreakdown", coverageFrom: "2026-01-01", coverageTo: "2026-12-31", sumInsured: "95000", indexedValue: "105803.57", currency: "USD", underInsured: true, warnings: [] }] } };
      }
      if (url.pathname.endsWith("/transactions")) return { status: 200, body: { items: [] } };
      if (url.pathname.endsWith("/schedule")) return { status: 200, body: { bookId: BOOK, bookCode: "IFRS", currency: "USD", total: "0", lines: [] } };
      return { status: 200, body: { ...detail, books: [book({ lastDepreciatedPeriod: "2026-03" })] }, etag: '"3"' };
    };
    const { container } = renderAt(`/assets/${ASSET}`);
    const policies = await findTable({ name: "Insurance policies" });
    await waitFor(() => expect(within(policies).getByText("Under-insured")).toBeInTheDocument());
    expect(within(policies).getByText("Machinery breakdown")).toBeInTheDocument();
    expect(await screen.findByText("No additions yet")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Books" })).getByText("2026-03")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Adjust" })).toHaveAttribute("href", `/assets/${ASSET}/adjust`);
    expect(screen.getByRole("link", { name: "Add addition" })).toHaveAttribute("href", `/assets/${ASSET}/additions/new`);
    expect(screen.getByRole("link", { name: "Add policy" })).toHaveAttribute("href", `/assets/${ASSET}/insurance/new`);
    expect(pageViolations(container)).toEqual([]);
  });
});
