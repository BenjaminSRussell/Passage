import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { loadModules } from "../registry";
import { blankBook, validateBook } from "./AddBookPage";
import { validateDisposal } from "./DisposePage";
import { percent, sentence } from "./format";
import { validateLease } from "./LeasesPage";
import assets from "./module";
import { validateAsset } from "./NewAssetPage";

const LE = "11111111-0000-4000-8000-000000000001";
const ASSET = "22222222-0000-4000-8000-000000000002";
const BOOK = "33333333-0000-4000-8000-000000000003";
const TAX = "33333333-0000-4000-8000-000000000004";
const LEASE = "44444444-0000-4000-8000-000000000005";

// The shell supplies the h1, landmarks and skip link; a page rendered alone is
// checked for everything else.
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
        <Route path="*" element={<p>Elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

const asset = {
  id: ASSET,
  legalEntityId: LE,
  assetNumber: "FA-000001",
  name: "Delivery van",
  classCode: "VEHICLE",
  barcode: "BC-1",
  quantity: "1",
  status: "Open",
  acquiredDate: "2026-03-10",
  bookCount: 2,
  version: 3,
  createdAt: "2026-03-01T09:00:00Z",
};

function book(overrides: Record<string, unknown> = {}) {
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
    convention: "MidMonth15",
    inServiceDate: "2026-03-10",
    cost: "12000",
    accumulatedDepreciation: "0",
    nbv: "12000",
    status: "Open",
    ...overrides,
  };
}

const detail = { ...asset, books: [book(), book({ id: TAX, bookCode: "TAX", method: "DecliningBalance", factor: "2", switchToStraightLine: true, serviceLifePeriods: 5, convention: "HalfYear", cost: "25000", nbv: "25000" })] };

const schedule = {
  bookId: BOOK,
  bookCode: "IFRS",
  currency: "USD",
  total: "12000.00",
  lines: [
    { index: 1, period: "2026-03", amount: "100.00", accumulated: "100.00", nbv: "11900.00" },
    { index: 2, period: "2026-04", amount: "200.00", accumulated: "300.00", nbv: "11700.00" },
  ],
};

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
  currency: "USD",
  liability: "43294.77",
  rouAsset: "43294.77",
  status: "Active",
  version: 1,
  createdAt: "2026-01-01T00:00:00Z",
};

const leaseSchedule = [
  { periodNo: 1, opening: "43294.77", interest: "2164.74", payment: "10000", closing: "35459.51", rouDepreciation: "8658.95" },
  { periodNo: 5, opening: "9523.82", interest: "476.18", payment: "10000", closing: "0", rouDepreciation: "8658.97" },
];

describe("assets module", () => {
  it("is discovered by the shell, with nav under /assets and a route per nav entry", () => {
    expect(assets.id).toBe("assets");
    expect(loadModules().map((m) => m.id)).toContain("assets");
    expect(assets.nav.every((n) => n.to.startsWith("/assets") && n.duty)).toBe(true);
    const paths = assets.routes.map((r) => r.path);
    expect(assets.nav.every((n) => paths.includes(n.to))).toBe(true);
    expect(assets.routes.every((r) => r.element !== null)).toBe(true);
  });

  it("formats statuses and rates without floats", () => {
    expect(sentence("NotYetAcquired")).toBe("Not yet acquired");
    expect(percent("0.05")).toBe("5%");
    expect(percent("0.0525")).toBe("5.25%");
  });
});

describe("asset register", () => {
  it("lists assets and opens one", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/assets") return { status: 200, body: { items: [asset] } };
      if (url.pathname.endsWith("/schedule")) return { status: 200, body: schedule };
      if (url.pathname.endsWith("/transactions")) return { status: 200, body: { items: [] } };
      return { status: 200, body: detail, etag: '"3"' };
    };
    const { container } = renderAt("/assets");
    const table = await screen.findByRole("table", { name: "Assets" });
    await waitFor(() => expect(within(table).getByText("Delivery van")).toBeInTheDocument());
    expect(within(table).getByText("Open")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "FA-000001" }));
    expect(await screen.findByRole("heading", { name: "FA-000001 Delivery van" })).toBeInTheDocument();
  });

  it("filters by status", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/assets");
    await screen.findByText("No assets yet");
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "Sold" } });
    await waitFor(() => expect(calls.at(-1)?.url.search).toBe("?status=Sold"));
  });

  it("shows the error with a retry", async () => {
    let fail = true;
    handler = () => (fail ? { status: 503, body: { code: "COM-0500", detail: "internal error" } } : { status: 200, body: { items: [asset] } });
    renderAt("/assets");
    expect(await screen.findByRole("alert")).toHaveTextContent("internal error (COM-0500)");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Delivery van")).toBeInTheDocument();
  });

  it("loads the next page from the cursor", async () => {
    handler = (url) =>
      url.searchParams.get("after") === ASSET
        ? { status: 200, body: { items: [{ ...asset, id: BOOK, assetNumber: "FA-000002", name: "Forklift" }] } }
        : { status: 200, body: { items: [asset], nextCursor: ASSET } };
    renderAt("/assets");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("Forklift")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });
});

describe("new asset", () => {
  it("validates like the service", () => {
    const { errors } = validateAsset({ legalEntityId: "x", assetNumber: "", name: "Van", classCode: "veh", barcode: "", quantity: "0" });
    expect(Object.keys(errors).sort()).toEqual(["assetNumber", "classCode", "legalEntityId", "quantity"]);
    const ok = validateAsset({ legalEntityId: LE, assetNumber: " FA-1 ", name: "Van", classCode: "VEH", barcode: "", quantity: "" });
    expect(ok.input).toEqual({ legalEntityId: LE, assetNumber: "FA-1", name: "Van", classCode: "VEH" });
  });

  it("creates the asset with an Idempotency-Key and opens it", async () => {
    handler = (url, init) => (init.method === "POST" ? { status: 201, body: { ...asset, status: "NotYetAcquired" } } : url.pathname.endsWith("/transactions") ? { status: 200, body: { items: [] } } : { status: 200, body: { ...detail, books: [] }, etag: '"1"' });
    const { container } = renderAt("/assets/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create asset" }));
    expect(screen.getByText("Enter the legal entity's UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Asset number"), { target: { value: "FA-000001" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Delivery van" } });
    fireEvent.change(screen.getByLabelText("Asset class"), { target: { value: "vehicle" } });
    fireEvent.click(screen.getByRole("button", { name: "Create asset" }));
    await screen.findByRole("heading", { name: "FA-000001 Delivery van" });
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/assets");
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({ legalEntityId: LE, assetNumber: "FA-000001", name: "Delivery van", classCode: "VEHICLE", quantity: "1" });
  });

  it("shows a duplicate number from the service", async () => {
    handler = () => ({ status: 409, body: { code: "AST-0002", detail: "an asset with this number already exists in the legal entity" } });
    renderAt("/assets/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Asset number"), { target: { value: "FA-1" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Van" } });
    fireEvent.change(screen.getByLabelText("Asset class"), { target: { value: "VEH" } });
    fireEvent.click(screen.getByRole("button", { name: "Create asset" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("That number or barcode is taken");
  });
});

describe("asset detail", () => {
  it("shows books, the schedule of the first open book and transactions", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/schedule")) return { status: 200, body: url.searchParams.get("book") === TAX ? { ...schedule, bookId: TAX, bookCode: "TAX", total: "25000.00" } : schedule };
      if (url.pathname.endsWith("/transactions")) {
        return {
          status: 200,
          body: { items: [{ id: "t1", assetBookId: BOOK, bookCode: "IFRS", currency: "USD", type: "Acquisition", transactionDate: "2026-03-10", costAmount: "12000", accumulatedAmount: "0", proceedsAmount: null, gainLoss: null, sourceType: "manual", createdAt: "" }] },
        };
      }
      return { status: 200, body: detail, etag: '"3"' };
    };
    const { container } = renderAt(`/assets/${ASSET}`);
    const books = await screen.findByRole("table", { name: "Books" });
    expect(within(books).getAllByText("12000 USD")).toHaveLength(2); // cost and NBV
    expect(await screen.findByText("2 periods, total 12000.00 USD.")).toBeInTheDocument();
    const txs = await screen.findByRole("table", { name: "Transactions" });
    await waitFor(() => expect(within(txs).getByText("Acquisition")).toBeInTheDocument());
    expect(screen.getByRole("link", { name: "Dispose" })).toHaveAttribute("href", `/assets/${ASSET}/dispose`);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(books).getByRole("button", { name: "TAX" }));
    expect(await screen.findByText("2 periods, total 25000.00 USD.")).toBeInTheDocument();
  });

  it("offers no disposal for a scrapped asset", async () => {
    handler = (url) => (url.pathname.endsWith("/transactions") ? { status: 200, body: { items: [] } } : url.pathname.endsWith("/schedule") ? { status: 200, body: { ...schedule, lines: [], total: "0.00" } } : { status: 200, body: { ...detail, status: "Scrapped", books: [book({ status: "Scrapped", cost: "0", nbv: "0" })] }, etag: '"5"' });
    renderAt(`/assets/${ASSET}`);
    await screen.findByRole("table", { name: "Books" });
    expect(screen.queryByRole("link", { name: "Dispose" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Add book" })).not.toBeInTheDocument();
    expect(await screen.findByText("Nothing left to depreciate")).toBeInTheDocument();
  });

  it("reports an unknown asset", async () => {
    handler = () => ({ status: 404, body: { code: "AST-0001", detail: "no asset with id x" } });
    renderAt(`/assets/${ASSET}`);
    const alerts = await screen.findAllByRole("alert");
    expect(alerts[0]).toHaveTextContent("no asset with id x (AST-0001)");
  });
});

describe("add book", () => {
  it("validates life and convention per method", () => {
    const values = { ...blankBook(new Date("2026-03-10T00:00:00Z")), cost: "25000", method: "DecliningBalance" as const, serviceLifePeriods: "150", convention: "MidMonth15", factor: "4" };
    expect(Object.keys(validateBook(values).errors).sort()).toEqual(["convention", "factor", "serviceLifePeriods"]);
    const ok = validateBook({ ...values, serviceLifePeriods: "5", convention: "HalfYear", factor: "2" });
    expect(ok.input).toMatchObject({ method: "DecliningBalance", factor: "2", switchToStraightLine: true, serviceLifePeriods: 5, inServiceDate: "2026-03-10" });
  });

  it("posts the book and returns to the asset", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: book() };
      if (url.pathname.endsWith("/transactions")) return { status: 200, body: { items: [] } };
      if (url.pathname.endsWith("/schedule")) return { status: 200, body: schedule };
      return { status: 200, body: detail, etag: '"4"' };
    };
    const { container } = renderAt(`/assets/${ASSET}/books/new`);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Convention"), { target: { value: "MidMonth15" } });
    fireEvent.change(screen.getByLabelText("In-service date"), { target: { value: "2026-03-10" } });
    fireEvent.change(screen.getByLabelText("Cost"), { target: { value: "12000" } });
    fireEvent.click(screen.getByRole("button", { name: "Add book" }));
    await screen.findByRole("table", { name: "Books" });
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/assets/${ASSET}/books`);
    expect(post?.body).toMatchObject({ bookCode: "IFRS", method: "StraightLine", convention: "MidMonth15", serviceLifePeriods: 60, cost: "12000", switchToStraightLine: false });
  });

  it("switching to declining balance offers its conventions and factor", () => {
    handler = () => ({ status: 200, body: {} });
    renderAt(`/assets/${ASSET}/books/new`);
    fireEvent.change(screen.getByLabelText("Method"), { target: { value: "DecliningBalance" } });
    expect(screen.getByLabelText("Service life (years)")).toHaveValue(5);
    expect(screen.getByLabelText("Declining-balance factor")).toHaveValue("2");
    const options = within(screen.getByLabelText("Convention")).getAllByRole("option").map((o) => o.textContent);
    expect(options).toEqual(["None", "Half year"]);
  });
});

describe("dispose", () => {
  it("validates percentage and proceeds", () => {
    expect(Object.keys(validateDisposal({ kind: "sale", percentage: "0", proceeds: "-1", transactionDate: "2026-10-01" }).errors).sort()).toEqual(["percentage", "proceeds"]);
    expect(validateDisposal({ kind: "sale", percentage: "100.5", proceeds: "1", transactionDate: "2026-10-01" }).errors.percentage).toBeTruthy();
    expect(validateDisposal({ kind: "scrap", percentage: "100", proceeds: "abc", transactionDate: "2026-10-01" }).input).toEqual({ kind: "scrap", percentage: "100", transactionDate: "2026-10-01" });
  });

  it("posts with the asset ETag and shows the gain per book", async () => {
    handler = (_url, init) => {
      if (init.method === "POST") {
        return {
          status: 201,
          body: {
            asset: { ...asset, version: 4 },
            books: [book({ cost: "6000", nbv: "6000" })],
            transactions: [{ id: "t2", assetBookId: BOOK, bookCode: "IFRS", currency: "USD", type: "DisposalSale", transactionDate: "2026-10-01", costAmount: "-6000", accumulatedAmount: "0", proceedsAmount: "7000", gainLoss: "1000", sourceType: "manual", createdAt: "" }],
          },
        };
      }
      return { status: 200, body: { ...detail, books: [book()] }, etag: '"3"' };
    };
    const { container } = renderAt(`/assets/${ASSET}/dispose`);
    await screen.findByRole("table", { name: "Open books" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Percentage"), { target: { value: "50" } });
    fireEvent.change(screen.getByLabelText("Proceeds"), { target: { value: "7000" } });
    fireEvent.change(screen.getByLabelText("Disposal date"), { target: { value: "2026-10-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Post disposal" }));
    const result = await screen.findByRole("table", { name: "Disposal by book" });
    expect(within(result).getByText("1000 USD")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("The remaining share of the asset stays open.");
    const post = calls.find((c) => c.method === "POST");
    expect(post?.headers.get("If-Match")).toBe('"3"');
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({ kind: "sale", percentage: "50", proceeds: "7000", transactionDate: "2026-10-01" });
  });

  it("explains a stale version", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 412, body: { code: "AST-0006", detail: "the asset changed since you read it" } } : { status: 200, body: { ...detail, books: [book()] }, etag: '"3"' });
    renderAt(`/assets/${ASSET}/dispose`);
    await screen.findByRole("table", { name: "Open books" });
    fireEvent.change(screen.getByLabelText("Disposal type"), { target: { value: "scrap" } });
    expect(screen.queryByLabelText("Proceeds")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Post disposal" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("The asset changed since you opened it");
  });
});

describe("leases", () => {
  it("lists leases and opens the schedule", async () => {
    handler = (url) => (url.pathname === "/api/v1/leases" ? { status: 200, body: { items: [lease] } } : { status: 200, body: { ...lease, schedule: leaseSchedule } });
    const { container } = renderAt("/assets/leases");
    const table = await screen.findByRole("table", { name: "Leases" });
    await waitFor(() => expect(within(table).getByText("43294.77 USD")).toBeInTheDocument());
    expect(within(table).getByText("5%")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "L-2026-01" }));
    const sched = await screen.findByRole("table", { name: "Lease schedule" });
    expect(within(sched).getByText("476.18")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Lease balances" })).toHaveTextContent("Lease liability 43294.77 USD");
  });

  it("shows the empty state", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/assets/leases");
    expect(await screen.findByText("No leases yet")).toBeInTheDocument();
  });

  it("validates like the service", () => {
    const base = { legalEntityId: LE, leaseNumber: "L-1", commencementDate: "2026-01-01", termMonths: "61", paymentsPerYear: "1", paymentAmount: "10000", paymentTiming: "arrears" as const, ibrRate: "0.6", currency: "USD" };
    expect(Object.keys(validateLease(base).errors).sort()).toEqual(["ibrRate", "termMonths"]);
    expect(validateLease({ ...base, termMonths: "60", ibrRate: "0.05" }).input).toMatchObject({ termMonths: 60, paymentsPerYear: 1, ibrRate: "0.05" });
  });

  it("recognizes a lease and opens it", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: { ...lease, schedule: leaseSchedule } } : { status: 200, body: { ...lease, schedule: leaseSchedule } });
    const { container } = renderAt("/assets/leases/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Lease number"), { target: { value: "L-2026-01" } });
    fireEvent.change(screen.getByLabelText("Commencement date"), { target: { value: "2026-01-01" } });
    fireEvent.change(screen.getByLabelText("Payment frequency"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Payment amount"), { target: { value: "10000" } });
    fireEvent.click(screen.getByRole("button", { name: "Recognize lease" }));
    expect(await screen.findByRole("heading", { name: "Lease L-2026-01" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/leases");
    expect(post?.body).toEqual({ legalEntityId: LE, leaseNumber: "L-2026-01", commencementDate: "2026-01-01", termMonths: 60, paymentsPerYear: 1, paymentAmount: "10000", paymentTiming: "arrears", ibrRate: "0.05", currency: "USD" });
  });

  it("shows the IBR range error from the service", async () => {
    handler = () => ({ status: 422, body: { code: "AST-0515", detail: "lease IBR must be above 0 and at most 50%" } });
    renderAt("/assets/leases/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Lease number"), { target: { value: "L-1" } });
    fireEvent.change(screen.getByLabelText("Payment amount"), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "Recognize lease" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("AST-0515");
  });
});
