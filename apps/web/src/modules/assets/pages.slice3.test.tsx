import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { parseBarcodes, validateCount } from "./CountPage";
import { validateReport } from "./InsuranceReportPage";
import { validateModification } from "./LeaseDetailPage";
import assets from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const ASSET = "22222222-0000-4000-8000-000000000002";
const LEASE = "44444444-0000-4000-8000-000000000004";
const SESSION = "99999999-0000-4000-8000-000000000009";
const MOD = "aaaaaaaa-0000-4000-8000-00000000000a";

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

const schedule = [
  { periodNo: 1, opening: "43294.77", remeasurement: "0", interest: "2164.74", payment: "10000", closing: "35459.51", rouDepreciation: "8658.95" },
  { periodNo: 2, opening: "35459.51", remeasurement: "0", interest: "1772.98", payment: "10000", closing: "27232.49", rouDepreciation: "8658.95" },
  { periodNo: 3, opening: "27232.49", remeasurement: "0", interest: "1361.62", payment: "10000", closing: "18594.11", rouDepreciation: "8658.95" },
  { periodNo: 4, opening: "18594.11", remeasurement: "0", interest: "929.71", payment: "10000", closing: "9523.82", rouDepreciation: "8658.95" },
  { periodNo: 5, opening: "9523.82", remeasurement: "0", interest: "476.18", payment: "10000", closing: "0", rouDepreciation: "8658.97" },
];

const lease = {
  id: LEASE,
  legalEntityId: LE,
  leaseNumber: "L-2026-01",
  commencementDate: "2026-01-01",
  termMonths: 60,
  paymentsPerYear: 1,
  paymentAmount: "10000",
  paymentTiming: "arrears",
  ibrRate: "0.05",
  rateMethod: "Nominal",
  currency: "USD",
  liability: "43294.77",
  rouAsset: "43294.77",
  status: "Active",
  version: 1,
  createdAt: "2026-01-01T00:00:00Z",
  schedule,
};

const modification = {
  id: MOD,
  leaseId: LEASE,
  effectivePeriod: 3,
  termMonths: 60,
  paymentAmount: "12000",
  ibrRate: "0.05",
  scopeChange: false,
  liabilityBefore: "27232.49",
  liabilityAfter: "32678.98",
  adjustment: "5446.49",
  rouBefore: "25976.87",
  rouAfter: "31423.36",
  rouAdjustment: "5446.49",
  profitLossExcess: "0",
  createdAt: "2026-03-01T00:00:00Z",
};

const modified = {
  ...lease,
  status: "Modified",
  version: 2,
  paymentAmount: "12000",
  schedule: [schedule[0], schedule[1], { periodNo: 3, opening: "27232.49", remeasurement: "5446.49", interest: "1633.95", payment: "12000", closing: "22312.93", rouDepreciation: "10474.45" }],
  modifications: [modification],
};

describe("assets module, slice 3", () => {
  it("registers the lease, count and insurance report routes with nav entries", () => {
    const paths = assets.routes.map((r) => r.path);
    for (const path of ["/assets/leases/:id/modify", "/assets/count-sessions", "/assets/count-sessions/new", "/assets/count-sessions/:id", "/assets/insurance-report"]) {
      expect(paths).toContain(path);
    }
    expect(assets.nav).toEqual(expect.arrayContaining([
      { label: "Count sessions", to: "/assets/count-sessions", duty: "AssetCount" },
      { label: "Insurance report", to: "/assets/insurance-report", duty: "AssetView" },
    ]));
  });
});

describe("lease remeasurement", () => {
  it("validates like the service", () => {
    const shape = { termMonths: 60, paymentsPerYear: 1, periods: 5 };
    const base = { effectivePeriod: "3", paymentAmount: "12000", scopeChange: false, termMonths: "", ibrRate: "" };
    expect(validateModification(base, shape).input).toEqual({ effectivePeriod: 3, paymentAmount: "12000", scopeChange: false });
    expect(Object.keys(validateModification({ ...base, effectivePeriod: "6", paymentAmount: "0" }, shape).errors).sort()).toEqual(["effectivePeriod", "paymentAmount"]);
    expect(validateModification({ ...base, scopeChange: true }, shape).errors.ibrRate).toBeDefined();
    expect(validateModification({ ...base, scopeChange: true, ibrRate: "0.06", termMonths: "13" }, { ...shape, paymentsPerYear: 1 }).errors.termMonths).toMatch(/whole number/);
    expect(validateModification({ ...base, scopeChange: true, ibrRate: "0.06", termMonths: "24" }, shape).errors.termMonths).toMatch(/at least the effective period/);
    expect(validateModification({ ...base, scopeChange: true, ibrRate: "0.06", termMonths: "84" }, shape).input).toEqual({
      effectivePeriod: 3,
      paymentAmount: "12000",
      scopeChange: true,
      ibrRate: "0.06",
      termMonths: 84,
    });
    // An unchanged term is not sent.
    expect(validateModification({ ...base, scopeChange: true, ibrRate: "0.06", termMonths: "60" }, shape).input?.termMonths).toBeUndefined();
  });

  it("offers Modify on an active lease and shows remeasurements on a modified one", async () => {
    handler = () => ({ status: 200, body: modified, etag: '"2"' });
    const { container } = renderAt(`/assets/leases/${LEASE}`);
    const history = await findTable({ name: "Remeasurements" });
    expect(within(history).getByText("32678.98 USD")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Lease schedule" })).getByText("5446.49")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Lease balances" })).toHaveTextContent("Liability after last remeasurement 32678.98 USD");
    expect(screen.getByRole("link", { name: "Modify lease" })).toHaveAttribute("href", `/assets/leases/${LEASE}/modify`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("posts the modification with If-Match and an Idempotency-Key, then shows AST-0516", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 201, body: { modification, lease: modified, messages: ["AST-0516: lease remeasured; ROU adjusted by 5446.49 USD"] }, etag: '"2"' }
        : { status: 200, body: lease, etag: '"1"' };
    const { container } = renderAt(`/assets/leases/${LEASE}/modify`);
    await screen.findByRole("form", { name: "Modify lease" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Remeasure lease" }));
    expect(await screen.findByText(/Enter the first period under the new terms, 1 to 5/)).toBeInTheDocument();
    expect(posts()).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Effective period"), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("New payment amount"), { target: { value: "12000" } });
    expect(screen.queryByLabelText("Revised borrowing rate")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remeasure lease" }));
    expect(await findStatus()).toHaveTextContent("AST-0516: lease remeasured; ROU adjusted by 5446.49 USD");
    const post = posts()[0];
    expect(post.url.pathname).toBe(`/api/v1/leases/${LEASE}/modify`);
    expect(post.headers.get("If-Match")).toBe('"1"');
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post.body).toEqual({ effectivePeriod: 3, paymentAmount: "12000", scopeChange: false });
    expect(screen.getByRole("list", { name: "Remeasurement" })).toHaveTextContent("Liability after 32678.98 USD");
    expect(screen.getByRole("link", { name: "View the new schedule" })).toHaveAttribute("href", `/assets/leases/${LEASE}`);
  });

  it("sends a scope change with the revised rate and term", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? { status: 201, body: { modification, lease: modified, messages: ["AST-0516: lease remeasured"] } } : { status: 200, body: lease, etag: '"1"' };
    renderAt(`/assets/leases/${LEASE}/modify`);
    await screen.findByRole("form", { name: "Modify lease" });
    fireEvent.change(screen.getByLabelText("Effective period"), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("New payment amount"), { target: { value: "10000" } });
    fireEvent.click(screen.getByLabelText("Scope change"));
    fireEvent.change(screen.getByLabelText("Revised term (months)"), { target: { value: "84" } });
    fireEvent.change(screen.getByLabelText("Revised borrowing rate"), { target: { value: "0.06" } });
    fireEvent.click(screen.getByRole("button", { name: "Remeasure lease" }));
    await findStatus();
    expect(posts()[0].body).toEqual({ effectivePeriod: 3, paymentAmount: "10000", scopeChange: true, ibrRate: "0.06", termMonths: 84 });
  });

  it("explains a stale version and offers a reload", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 412, body: { code: "AST-0006", detail: "the lease changed since you read it" } } : { status: 200, body: lease, etag: '"1"' });
    renderAt(`/assets/leases/${LEASE}/modify`);
    await screen.findByRole("form", { name: "Modify lease" });
    fireEvent.change(screen.getByLabelText("Effective period"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("New payment amount"), { target: { value: "9000" } });
    fireEvent.click(screen.getByRole("button", { name: "Remeasure lease" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The lease changed since you opened it");
    expect(within(alert).getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });

  it("refuses a terminated lease and shows load errors", async () => {
    handler = () => ({ status: 200, body: { ...lease, status: "Terminated" }, etag: '"4"' });
    const first = renderAt(`/assets/leases/${LEASE}/modify`);
    expect(await screen.findByText("This lease cannot be modified")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Modify lease" })).not.toBeInTheDocument();
    first.unmount();
    handler = () => ({ status: 404, body: { code: "AST-0005", detail: "no lease" } });
    renderAt(`/assets/leases/${LEASE}/modify`);
    expect(await findAlert()).toHaveTextContent("AST-0005");
  });
});

const session = {
  id: SESSION,
  legalEntityId: LE,
  location: "Warehouse 1",
  countedDate: "2026-04-10",
  expectedCount: 2,
  foundCount: 1,
  missingCount: 1,
  unknownCount: 1,
  duplicateCount: 1,
  createdAt: "2026-04-10T09:00:00Z",
};

const sessionDetail = {
  ...session,
  lines: [
    { barcode: "BC-2", outcome: "Missing", assetId: ASSET, assetNumber: "FA-2", assetName: "Printer" },
    { barcode: "XX-1", outcome: "Unknown", assetId: null, assetNumber: null, assetName: null },
    { barcode: "BC-1", outcome: "Found", assetId: ASSET, assetNumber: "FA-1", assetName: "Laptop" },
  ],
  warnings: ["AST-0525: barcode XX-1 is not in the register"],
};

describe("count sessions", () => {
  it("parses and validates scans like the service", () => {
    expect(parseBarcodes(" BC-1 \n\nBC-2, BC-1\n")).toEqual(["BC-1", "BC-2", "BC-1"]);
    const base = { legalEntityId: LE, location: " Warehouse 1 ", countedDate: "2026-04-10", barcodes: "BC-1\nBC-2" };
    expect(validateCount(base).input).toEqual({ legalEntityId: LE, location: "Warehouse 1", countedDate: "2026-04-10", barcodes: ["BC-1", "BC-2"] });
    expect(Object.keys(validateCount({ ...base, legalEntityId: "x", location: " ", countedDate: "" }).errors).sort()).toEqual(["countedDate", "legalEntityId", "location"]);
    expect(validateCount({ ...base, barcodes: "B".repeat(65) }).errors.barcodes).toMatch(/longer than 64/);
    expect(validateCount({ ...base, barcodes: Array.from({ length: 5001 }, () => "B").join("\n") }).errors.barcodes).toMatch(/at most 5000/);
  });

  it("lists sessions and opens one", async () => {
    handler = (url) => (url.pathname === "/api/v1/assets/count-sessions" ? { status: 200, body: { items: [session] } } : { status: 200, body: sessionDetail });
    const { container } = renderAt("/assets/count-sessions");
    const table = await findTable({ name: "Count sessions" });
    await waitFor(() => expect(within(table).getByText("Warehouse 1")).toBeInTheDocument());
    expect(pageViolations(container)).toEqual([]);
    expect(screen.getByRole("link", { name: "New count" })).toHaveAttribute("href", "/assets/count-sessions/new");
    fireEvent.click(within(table).getByRole("button", { name: "2026-04-10" }));
    const lines = await findTable({ name: "Count lines" });
    expect(within(lines).getByText("Not in the register")).toBeInTheDocument();
    expect(within(lines).getByText("Missing")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Count result" })).toHaveTextContent("Missing 1");
    expect(screen.getByText("1 barcodes are not in the register")).toBeInTheDocument();
    expect(screen.getByText("1 repeated scans were counted once.")).toBeInTheDocument();
  });

  it("shows the empty and error states of the list", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const first = renderAt("/assets/count-sessions");
    expect(await screen.findByText("No count sessions yet")).toBeInTheDocument();
    first.unmount();
    handler = () => ({ status: 403, body: { code: "AUTH-0003", detail: "missing duty AssetView or AssetCount" } });
    renderAt("/assets/count-sessions");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("AUTH-0003");
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("records a count with the parsed scans and opens the result", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: sessionDetail } : { status: 200, body: sessionDetail });
    const { container } = renderAt("/assets/count-sessions/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Reconcile count" }));
    expect(await screen.findByText("Enter the legal entity's UUID.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Location"), { target: { value: "Warehouse 1" } });
    fireEvent.change(screen.getByLabelText("Count date"), { target: { value: "2026-04-10" } });
    fireEvent.change(screen.getByLabelText("Scanned barcodes"), { target: { value: "BC-1\nXX-1\nBC-1" } });
    expect(screen.getByText(/3 scanned; repeats count once/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reconcile count" }));
    expect(await screen.findByRole("heading", { name: "Count at Warehouse 1" })).toBeInTheDocument();
    const post = posts()[0];
    expect(post.url.pathname).toBe("/api/v1/assets/count-sessions");
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post.body).toEqual({ legalEntityId: LE, location: "Warehouse 1", countedDate: "2026-04-10", barcodes: ["BC-1", "XX-1", "BC-1"] });
  });

  it("shows field errors from the service", async () => {
    handler = () => ({ status: 403, body: { code: "AUTH-0003", detail: "the legal entity is outside the caller's scope" } });
    renderAt("/assets/count-sessions/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Location"), { target: { value: "Site" } });
    fireEvent.click(screen.getByRole("button", { name: "Reconcile count" }));
    expect(await findAlert()).toHaveTextContent("outside the caller's scope");
  });

  it("reports an unknown session", async () => {
    handler = () => ({ status: 404, body: { code: "AST-0026", detail: "no count session" } });
    renderAt(`/assets/count-sessions/${SESSION}`);
    expect(await findAlert()).toHaveTextContent("AST-0026");
  });
});

const policyRow = {
  id: "bbbbbbbb-0000-4000-8000-00000000000b",
  assetId: ASSET,
  legalEntityId: LE,
  insurerPartyId: "88888888-0000-4000-8000-000000000008",
  policyNumber: "P-1",
  insuranceType: "MachineryBreakdown",
  coverageFrom: "2026-01-01",
  coverageTo: "2026-05-15",
  premium: "1200",
  currency: "USD",
  basis: "Replacement",
  baseValue: "100000",
  sumInsured: "95000",
  indexSeriesCode: "US-MACH",
  indexBaseYear: 2024,
  valuationYear: 2026,
  indexYearUsed: 2026,
  indexedValue: "105803.57",
  underInsured: true,
  warnings: ["AST-0532: the asset is under-insured"],
  createdAt: "2026-01-01T00:00:00Z",
  assetNumber: "FA-1",
  assetName: "Press",
  assetStatus: "Open",
  books: [
    { bookCode: "IFRS", currency: "USD", nbv: "90000" },
    { bookCode: "TAX", currency: "USD", nbv: "80000" },
  ],
  expiringSoon: true,
};

describe("insurance report", () => {
  it("validates the filters", () => {
    const base = { legalEntityId: LE, asOf: "2026-04-20", insuranceType: "" as const, expiringWithinDays: "" };
    expect(validateReport(base).filter).toEqual({ legalEntityId: LE, asOf: "2026-04-20", insuranceType: "", expiringWithinDays: undefined });
    expect(validateReport({ ...base, expiringWithinDays: "30" }).filter?.expiringWithinDays).toBe(30);
    expect(Object.keys(validateReport({ ...base, legalEntityId: "x", expiringWithinDays: "400" }).errors).sort()).toEqual(["expiringWithinDays", "legalEntityId"]);
  });

  it("asks for a legal entity, then runs the report with the filters", async () => {
    handler = () => ({ status: 200, body: { asOf: "2026-04-20", items: [policyRow] } });
    const { container } = renderAt("/assets/insurance-report");
    expect(screen.getByText("Choose a legal entity")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Report date"), { target: { value: "2026-04-20" } });
    fireEvent.change(screen.getByLabelText("Insurance type"), { target: { value: "MachineryBreakdown" } });
    fireEvent.change(screen.getByLabelText("Expiring within (days)"), { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "Run report" }));
    const table = await findTable({ name: "Insurance report" });
    await waitFor(() => expect(within(table).getByText("Under-insured")).toBeInTheDocument());
    expect(within(table).getByText("105803.57 USD")).toBeInTheDocument();
    expect(within(table).getByText("IFRS 90000 USD; TAX 80000 USD")).toBeInTheDocument();
    expect(within(table).getByText("2026-05-15 (soon)")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Report summary" })).toHaveTextContent("Under-insured 1");
    const q = calls[0].url.searchParams;
    expect(calls[0].url.pathname).toBe("/api/v1/assets/insurance-report");
    expect([q.get("legalEntityId"), q.get("asOf"), q.get("insuranceType"), q.get("expiringWithinDays")]).toEqual([LE, "2026-04-20", "MachineryBreakdown", "30"]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty and error states", async () => {
    handler = () => ({ status: 200, body: { asOf: "2026-04-20", items: [] } });
    const first = renderAt("/assets/insurance-report");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.click(screen.getByRole("button", { name: "Run report" }));
    expect(await screen.findByText("No policies in force")).toBeInTheDocument();
    first.unmount();
    handler = () => ({ status: 403, body: { code: "AUTH-0003", detail: "the legal entity is outside the caller's scope" } });
    renderAt("/assets/insurance-report");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.click(screen.getByRole("button", { name: "Run report" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("AUTH-0003");
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("does not call the service with an invalid filter", async () => {
    handler = () => ({ status: 200, body: { asOf: "2026-04-20", items: [] } });
    renderAt("/assets/insurance-report");
    fireEvent.click(screen.getByRole("button", { name: "Run report" }));
    expect(await screen.findByText("Enter the legal entity's UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});
