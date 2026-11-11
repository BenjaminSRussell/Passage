import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { AgingSnapshot, BucketSet, ValuationCategory, ValuationLedger } from "./api/client";
import { AgingSnapshotsPage, BucketSetsPage, CreateSnapshotPage, parseSnapshotLines, SnapshotDetailPage } from "./AgingSnapshotsPage";
import { CreateCategoryPage, CreateLedgerPage, parseTypeLines, validateLedger, ValuationSetupPage } from "./ValuationSetupPage";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE = "11111111-1111-4111-8111-111111111111";
const GROUP = "77777777-7777-4777-8777-777777777777";
const P1 = "22222222-2222-4222-8222-222222222222";
const S1 = "44444444-4444-4444-8444-444444444444";
const SNAP = "88888888-8888-4888-8888-888888888888";
const SET = "99999999-9999-4999-8999-999999999999";

const ifrs: ValuationLedger = { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", legalEntityId: LE, ledgerGroupId: GROUP, code: "IFRS", method: "WEIGHTED_AVG_PERIODIC", currency: "EUR", rateType: "AVERAGE", primary: true, createdAt: "2026-10-01T08:00:00Z" };
const gaap: ValuationLedger = { ...ifrs, id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", code: "USGAAP", method: "FIFO", currency: "USD", primary: false };
const origin: ValuationCategory = { id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", code: "ORIGIN", batch: false, types: [{ code: "DOMESTIC", account: "1300" }, { code: "IMPORT", account: "1301" }], createdAt: "2026-10-01T08:00:00Z" };
const quarter: BucketSet = { id: SET, legalEntityId: LE, name: "Quarter", bounds: [90, 180], createdAt: "2026-10-01T08:00:00Z" };

const buckets = [
  { label: "0-30", qty: "100", value: "500.00" },
  { label: "31-90", qty: "0", value: "0.00" },
  { label: "> 90", qty: "60", value: "300.00" },
];
const snapshot: AgingSnapshot = {
  id: SNAP,
  legalEntityId: LE,
  asOf: "2026-01-31",
  method: "FIFO",
  currency: "EUR",
  buckets,
  totalQty: "160",
  totalValue: "800.00",
  remainingValue: "800.00",
  tiesToRemaining: true,
  lineCount: 1,
  lines: [
    {
      productId: P1,
      siteId: S1,
      buckets,
      totalQty: "160",
      totalValue: "800.00",
      remainingValue: "800.00",
      tiesToRemaining: true,
      valuationTypes: [{ valuationType: "IMPORT", buckets, totalQty: "160", totalValue: "800.00", remainingValue: "800.00" }],
    },
  ],
  createdAt: "2026-02-01T03:00:00Z",
};

type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status });

/** Routes fetch by "METHOD path-prefix"; the longest matching key wins. */
function stubRoutes(routes: Record<string, Handler>) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    const key = Object.keys(routes)
      .filter((k) => {
        const [m, prefix] = k.split(" ");
        return m === method && url.startsWith(prefix);
      })
      .sort((a, b) => b.length - a.length)[0];
    if (!key) return json(500, { title: "unrouted", code: "TEST-0000", detail: `${method} ${url}` });
    return routes[key](url, init);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function calls(fetchMock: ReturnType<typeof vi.fn>, method: string, fragment: string) {
  return (fetchMock.mock.calls as unknown as [string, RequestInit][]).filter(([url, init]) => (init?.method ?? "GET") === method && url.includes(fragment));
}

function bodyOf(fetchMock: ReturnType<typeof vi.fn>, method: string, fragment: string) {
  const [, init] = calls(fetchMock, method, fragment)[0];
  return JSON.parse(String(init.body)) as Record<string, unknown>;
}

function at(path: string) {
  const route = (p: string, el: () => React.JSX.Element) => createElement(Route, { path: p, element: createElement(el) });
  return render(
    createElement(
      MemoryRouter,
      { initialEntries: [path] },
      createElement(
        Routes,
        null,
        route("/costing/valuation-setup", ValuationSetupPage),
        route("/costing/valuation-setup/ledgers/new", CreateLedgerPage),
        route("/costing/valuation-setup/categories/new", CreateCategoryPage),
        route("/costing/aging/snapshots", AgingSnapshotsPage),
        route("/costing/aging/snapshots/new", CreateSnapshotPage),
        route("/costing/aging/snapshots/:id", SnapshotDetailPage),
        route("/costing/aging/bucket-sets", BucketSetsPage),
      ),
    ),
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("ValuationSetupPage", () => {
  it("lists ledgers and categories and filters ledgers by legal entity", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/costing/valuation-ledgers": () => json(200, { items: [ifrs, gaap] }),
      "GET /api/v1/costing/valuation-categories": () => json(200, { items: [origin] }),
    });
    const { container } = at("/costing/valuation-setup");
    const ledgers = await findTable({ name: "Valuation ledgers" });
    expect(await within(ledgers).findByText("USGAAP")).toBeInTheDocument();
    expect(within(ledgers).getByText("Primary")).toBeInTheDocument();
    const categories = await findTable({ name: "Valuation categories" });
    expect(await within(categories).findByText("DOMESTIC → 1300, IMPORT → 1301")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: "not-a-uuid" } });
    expect(screen.getByText(/Enter a legal entity id/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    await waitFor(() => expect(calls(fetchMock, "GET", `valuation-ledgers?legalEntityId=${LE}`)).toHaveLength(1));
  });

  it("shows empty states, and an error with a retry", async () => {
    let fail = true;
    stubRoutes({
      "GET /api/v1/costing/valuation-ledgers": () => (fail ? json(503, { title: "down", code: "COM-0500", detail: "internal error" }) : json(200, { items: [] })),
      "GET /api/v1/costing/valuation-categories": () => json(200, { items: [] }),
    });
    at("/costing/valuation-setup");
    expect(await findAlert()).toHaveTextContent("Something went wrong on our side");
    expect(await screen.findByText("No valuation categories yet")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No valuation ledgers yet")).toBeInTheDocument();
  });
});

describe("CreateLedgerPage", () => {
  it("validates like the service", () => {
    const errors = validateLedger({ legalEntityId: "x", ledgerGroupId: "", code: "ifrs", method: "FIFO", currency: "eu", rateType: "SPOT", primary: false });
    expect(Object.keys(errors).sort()).toEqual(["code", "currency", "ledgerGroupId", "legalEntityId"]);
  });

  it("creates the ledger and returns to the setup filtered by its legal entity", async () => {
    const fetchMock = stubRoutes({
      "POST /api/v1/costing/valuation-ledgers": () => json(201, gaap),
      "GET /api/v1/costing/valuation-ledgers": () => json(200, { items: [ifrs, gaap] }),
      "GET /api/v1/costing/valuation-categories": () => json(200, { items: [] }),
    });
    const { container } = at("/costing/valuation-setup/ledgers/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Ledger group"), { target: { value: GROUP } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "usgaap" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.change(screen.getByLabelText("Exchange rate type"), { target: { value: "HISTORICAL" } });
    fireEvent.click(screen.getByRole("button", { name: "Create ledger" }));
    expect(await screen.findByRole("heading", { name: "Valuation setup" })).toBeInTheDocument();
    expect(bodyOf(fetchMock, "POST", "/valuation-ledgers")).toEqual({
      legalEntityId: LE,
      ledgerGroupId: GROUP,
      code: "USGAAP",
      method: "FIFO",
      currency: "USD",
      rateType: "HISTORICAL",
      primary: false,
    });
    await waitFor(() => expect(calls(fetchMock, "GET", `valuation-ledgers?legalEntityId=${LE}`).length).toBeGreaterThan(0));
  });

  it("shows the service refusal", async () => {
    stubRoutes({ "POST /api/v1/costing/valuation-ledgers": () => json(409, { title: "conflict", code: "CST-VLG-003", detail: "the legal entity already has a primary valuation ledger" }) });
    at("/costing/valuation-setup/ledgers/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Ledger group"), { target: { value: GROUP } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "TAX" } });
    fireEvent.click(screen.getByLabelText("Primary ledger"));
    fireEvent.click(screen.getByRole("button", { name: "Create ledger" }));
    expect(await findAlert()).toHaveTextContent("the legal entity already has a primary valuation ledger");
  });
});

describe("CreateCategoryPage", () => {
  it("parses type lines and refuses bad ones", () => {
    expect(parseTypeLines("DOMESTIC, 1300\n\nIMPORT,1301")).toEqual({ ok: true, value: [{ code: "DOMESTIC", account: "1300" }, { code: "IMPORT", account: "1301" }] });
    expect(parseTypeLines("DOMESTIC")).toMatchObject({ ok: false });
    expect(parseTypeLines("new, 1")).toMatchObject({ ok: false });
    expect(parseTypeLines("NEW, 1\nNEW, 2")).toMatchObject({ ok: false, error: "Line 2: NEW is already listed." });
  });

  it("creates a split category and a batch category without types", async () => {
    const fetchMock = stubRoutes({
      "POST /api/v1/costing/valuation-categories": () => json(201, origin),
      "GET /api/v1/costing/valuation-ledgers": () => json(200, { items: [] }),
      "GET /api/v1/costing/valuation-categories": () => json(200, { items: [origin] }),
    });
    const { container } = at("/costing/valuation-setup/categories/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "origin" } });
    fireEvent.click(screen.getByRole("button", { name: "Create category" }));
    expect(await screen.findByRole("heading", { name: "Valuation setup" })).toBeInTheDocument();
    expect(bodyOf(fetchMock, "POST", "/valuation-categories")).toEqual({ code: "ORIGIN", batch: false, types: origin.types });

    at("/costing/valuation-setup/categories/new");
    fireEvent.change(screen.getAllByLabelText("Code").at(-1)!, { target: { value: "BATCH" } });
    fireEvent.click(screen.getAllByLabelText("Batch valuation").at(-1)!);
    expect(screen.queryAllByLabelText("Valuation types")).toHaveLength(0);
    fireEvent.click(screen.getAllByRole("button", { name: "Create category" }).at(-1)!);
    await waitFor(() => expect(calls(fetchMock, "POST", "/valuation-categories")).toHaveLength(2));
    const [, init] = calls(fetchMock, "POST", "/valuation-categories")[1];
    expect(JSON.parse(String(init.body))).toEqual({ code: "BATCH", batch: true, types: [] });
  });
});

describe("AgingSnapshotsPage", () => {
  it("lists snapshot summaries and opens one", async () => {
    const summary = { ...snapshot, lines: undefined };
    stubRoutes({
      "GET /api/v1/costing/aging/snapshots": () => json(200, { items: [summary] }),
      [`GET /api/v1/costing/aging/snapshots/${SNAP}`]: () => json(200, snapshot),
    });
    const { container } = at("/costing/aging/snapshots");
    const table = await findTable({ name: "Aging snapshots" });
    expect(await within(table).findByText("800.00 EUR")).toBeInTheDocument();
    expect(within(table).getByText("Ties")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "2026-01-31" }));
    expect(await screen.findByRole("heading", { name: "Aging snapshot as of 2026-01-31" })).toBeInTheDocument();
  });

  it("shows the empty state and loads further pages", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/costing/aging/snapshots?cursor=": () => json(200, { items: [{ ...snapshot, id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", asOf: "2025-12-31" }] }),
      "GET /api/v1/costing/aging/snapshots": () => json(200, { items: [snapshot], nextCursor: "abc" }),
    });
    at("/costing/aging/snapshots");
    fireEvent.click(await screen.findByRole("button", { name: "Load more snapshots" }));
    expect(await screen.findByText("2025-12-31")).toBeInTheDocument();
    expect(calls(fetchMock, "GET", "cursor=abc")).toHaveLength(1);
    vi.unstubAllGlobals();

    stubRoutes({ "GET /api/v1/costing/aging/snapshots": () => json(200, { items: [] }) });
    at("/costing/aging/snapshots");
    expect(await screen.findByText("No aging snapshots yet")).toBeInTheDocument();
  });
});

describe("CreateSnapshotPage", () => {
  it("parses snapshot lines", () => {
    expect(parseSnapshotLines("[]")).toMatchObject({ ok: false });
    expect(parseSnapshotLines(`[{"productId":"${P1}","siteId":"x","transactions":[{}]}]`)).toMatchObject({ ok: false });
    expect(parseSnapshotLines(`[{"productId":"${P1}","siteId":"${S1}","transactions":[]}]`)).toMatchObject({ ok: false });
    expect(parseSnapshotLines(`[{"productId":"${P1}","siteId":"${S1}","transactions":[{"id":"r"}]}]`).ok).toBe(true);
  });

  it("stores a snapshot under a valuation ledger and a saved bucket set, with an Idempotency-Key", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/costing/valuation-ledgers": () => json(200, { items: [ifrs, gaap] }),
      "GET /api/v1/costing/aging/bucket-sets": () => json(200, { items: [quarter] }),
      "POST /api/v1/costing/aging/snapshots": () => json(201, snapshot),
      [`GET /api/v1/costing/aging/snapshots/${SNAP}`]: () => json(200, snapshot),
    });
    const { container } = at("/costing/aging/snapshots/new");
    expect(pageViolations(container)).toEqual([]);
    expect(screen.getByLabelText("Valuation ledger")).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    await screen.findByRole("option", { name: "USGAAP (FIFO, USD)" });
    await screen.findByRole("option", { name: "Quarter: 90, 180 days" });
    fireEvent.change(screen.getByLabelText("Valuation ledger"), { target: { value: gaap.id } });
    expect(screen.queryByLabelText("Currency")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Buckets"), { target: { value: SET } });
    fireEvent.click(screen.getByRole("button", { name: "Store snapshot" }));
    expect(await screen.findByRole("heading", { name: "Aging snapshot as of 2026-01-31" })).toBeInTheDocument();

    const [[, init]] = calls(fetchMock, "POST", "/aging/snapshots");
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/[0-9a-f-]{36}/);
    const body = JSON.parse(String(init.body)) as Record<string, unknown>;
    expect(body).toMatchObject({ legalEntityId: LE, valuationLedgerId: gaap.id, bucketSetId: SET, asOf: "2026-01-31" });
    expect(body.method).toBeUndefined();
    expect(body.currency).toBeUndefined();
    expect((body.lines as unknown[]).length).toBe(1);
  });

  it("keeps the form and shows field errors from the service", async () => {
    stubRoutes({
      "GET /api/v1/costing/valuation-ledgers": () => json(200, { items: [] }),
      "GET /api/v1/costing/aging/bucket-sets": () => json(200, { items: [] }),
      "POST /api/v1/costing/aging/snapshots": () =>
        json(400, { title: "invalid", code: "COM-0001", detail: "request failed validation", errors: { "lines[0].transactions[2].financialDate": "is after asOf" } }),
    });
    at("/costing/aging/snapshots/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Buckets"), { target: { value: "custom" } });
    fireEvent.change(screen.getByLabelText("Bucket bounds"), { target: { value: "90, 30" } });
    fireEvent.click(screen.getByRole("button", { name: "Store snapshot" }));
    expect(await screen.findByText("Each bound must be larger than the one before it.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Bucket bounds"), { target: { value: "30, 90" } });
    fireEvent.click(screen.getByRole("button", { name: "Store snapshot" }));
    expect(await findAlert()).toHaveTextContent("lines[0].transactions[2].financialDate is after asOf");
  });
});

describe("SnapshotDetailPage", () => {
  it("shows totals, buckets and lines with their valuation types", async () => {
    stubRoutes({ [`GET /api/v1/costing/aging/snapshots/${SNAP}`]: () => json(200, snapshot) });
    const { container } = at(`/costing/aging/snapshots/${SNAP}`);
    const bucketTable = await findTable({ name: "Snapshot buckets" });
    expect(within(bucketTable).getByText("> 90")).toBeInTheDocument();
    const lines = screen.getByRole("table", { name: "Snapshot lines" });
    expect(within(lines).getByText("IMPORT 800.00")).toBeInTheDocument();
    expect(screen.getByText(/tie to its remaining value/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains a snapshot that is not found", async () => {
    stubRoutes({ [`GET /api/v1/costing/aging/snapshots/${SNAP}`]: () => json(404, { title: "not found", code: "CST-AGE-004", detail: "aging snapshot not found" }) });
    at(`/costing/aging/snapshots/${SNAP}`);
    expect(await screen.findByRole("heading", { name: "Aging snapshot not found" })).toBeInTheDocument();
  });
});

describe("BucketSetsPage", () => {
  it("saves a bucket set and refreshes the list", async () => {
    let saved = false;
    const fetchMock = stubRoutes({
      "GET /api/v1/costing/aging/bucket-sets": () => json(200, { items: saved ? [quarter] : [] }),
      "POST /api/v1/costing/aging/bucket-sets": () => {
        saved = true;
        return json(201, quarter);
      },
    });
    const { container } = at("/costing/aging/bucket-sets");
    expect(await screen.findByText("No bucket sets yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getAllByLabelText("Legal entity")[0], { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Quarter" } });
    fireEvent.change(screen.getByLabelText("Bucket bounds"), { target: { value: "90, 180" } });
    fireEvent.click(screen.getByRole("button", { name: "Save bucket set" }));
    expect(await screen.findByText("Saved the bucket set Quarter.")).toBeInTheDocument();
    const table = await findTable({ name: "Aging bucket sets" });
    expect(await within(table).findByText("90, 180")).toBeInTheDocument();
    expect(bodyOf(fetchMock, "POST", "/aging/bucket-sets")).toEqual({ legalEntityId: LE, name: "Quarter", bounds: [90, 180] });
  });

  it("refuses an empty name before calling the service", async () => {
    const fetchMock = stubRoutes({ "GET /api/v1/costing/aging/bucket-sets": () => json(200, { items: [] }) });
    at("/costing/aging/bucket-sets");
    fireEvent.change(screen.getAllByLabelText("Legal entity")[0], { target: { value: LE } });
    fireEvent.click(screen.getByRole("button", { name: "Save bucket set" }));
    expect(await screen.findByText("Give the set a name of 1 to 64 characters.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", "/aging/bucket-sets")).toHaveLength(0);
  });
});
