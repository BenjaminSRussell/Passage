import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { validateGroup, type GroupValues } from "./GroupAssetsPage";
import assets from "./module";
import { validateTransfer, type PartValues, type TransferValues } from "./TransferPage";

const LE = "11111111-0000-4000-8000-000000000001";
const LE2 = "11111111-0000-4000-8000-000000000002";
const ASSET = "22222222-0000-4000-8000-000000000002";
const BOOK = "33333333-0000-4000-8000-000000000003";
const T1 = "44444444-0000-4000-8000-000000000001";
const T2 = "44444444-0000-4000-8000-000000000002";
const GROUP = "55555555-0000-4000-8000-000000000005";
const M1 = "66666666-0000-4000-8000-000000000001";
const M2 = "66666666-0000-4000-8000-000000000002";
const M3 = "66666666-0000-4000-8000-000000000003";

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

function asset(overrides: Record<string, unknown> = {}) {
  return {
    id: ASSET,
    legalEntityId: LE,
    assetNumber: "FA-1",
    name: "Delivery van",
    classCode: "VEH",
    barcode: null,
    quantity: "1",
    status: "Open",
    acquiredDate: "2026-01-01",
    bookCount: 1,
    version: 4,
    createdAt: "2026-01-01T09:00:00Z",
    ...overrides,
  };
}

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
    serviceLifePeriods: 12,
    salvageValue: "0",
    convention: "None",
    inServiceDate: "2026-01-01",
    cost: "1200",
    accumulatedDepreciation: "100",
    nbv: "1100",
    status: "Open",
    lastDepreciatedPeriod: "2026-01",
    ...overrides,
  };
}

const part = (p: Partial<PartValues>): PartValues => ({ assetNumber: "", name: "", percentage: "", legalEntityId: "", classCode: "", ...p });

describe("module registration, slice 5", () => {
  it("registers the transfer page and the group asset screens", () => {
    const paths = assets.routes.map((r) => r.path);
    expect(paths).toEqual(expect.arrayContaining(["/assets/:id/transfer", "/assets/group-assets", "/assets/group-assets/new", "/assets/group-assets/:id"]));
    expect(assets.nav.find((n) => n.to === "/assets/group-assets")?.duty).toBe("AssetConfigure");
  });
});

describe("validateTransfer", () => {
  const split = (parts: PartValues[]): TransferValues => ({ kind: "split", transferDate: "2026-02-15", parts });

  it("accepts a 60/40 split and sends only the fields given", () => {
    const { errors, input } = validateTransfer(split([part({ assetNumber: " FA-1A ", name: "Front", percentage: "60" }), part({ assetNumber: "FA-1B", name: "Rear", percentage: "40", classCode: "VEH-P" })]));
    expect(errors).toEqual({});
    expect(input).toEqual({
      kind: "split",
      transferDate: "2026-02-15",
      parts: [
        { assetNumber: "FA-1A", name: "Front", percentage: "60" },
        { assetNumber: "FA-1B", name: "Rear", percentage: "40", classCode: "VEH-P" },
      ],
    });
  });

  it("totals the shares without floats and refuses anything but 100.00% (AST-0507)", () => {
    expect(validateTransfer(split([part({ assetNumber: "A", name: "A", percentage: "33.33" }), part({ assetNumber: "B", name: "B", percentage: "33.33" }), part({ assetNumber: "C", name: "C", percentage: "33.34" })])).errors).toEqual({});
    expect(validateTransfer(split([part({ assetNumber: "A", name: "A", percentage: "60" }), part({ assetNumber: "B", name: "B", percentage: "30" })])).errors.parts).toBe(
      "The shares total 90.00%; they must total 100.00%.",
    );
    expect(validateTransfer(split([part({ assetNumber: "A", name: "A", percentage: "0.1" }), part({ assetNumber: "B", name: "B", percentage: "99.9" })])).errors).toEqual({});
  });

  it("flags each part's fields and the shape of the request", () => {
    const { errors, input } = validateTransfer(
      split([part({ assetNumber: "X", name: "", percentage: "50.005", legalEntityId: "le", classCode: "lower" }), part({ assetNumber: "X", name: "Y", percentage: "0" })]),
    );
    expect(input).toBeUndefined();
    expect(Object.keys(errors.partFields?.[0] ?? {}).sort()).toEqual(["classCode", "legalEntityId", "name", "percentage"]);
    expect(Object.keys(errors.partFields?.[1] ?? {}).sort()).toEqual(["assetNumber", "percentage"]);
    expect(validateTransfer(split([part({ assetNumber: "A", name: "A", percentage: "100" })])).errors.parts).toBe("A split has 2 to 50 target assets.");
    const transfer: TransferValues = { kind: "transfer", transferDate: "", parts: [part({ assetNumber: "A", name: "A", percentage: "100", legalEntityId: LE2 })] };
    expect(validateTransfer(transfer).errors.transferDate).toBe("Choose the transfer date.");
    expect(validateTransfer({ ...transfer, transferDate: "2026-05-01" }).input?.parts[0]).toEqual({ assetNumber: "A", name: "A", percentage: "100", legalEntityId: LE2 });
  });
});

describe("transfer and split", () => {
  const target = (id: string, number: string, pct: string, cost: string) => ({
    lineNo: 1,
    percentage: pct,
    asset: asset({ id, assetNumber: number, version: 1 }),
    books: [book({ id: `${id}-b`, assetId: id, cost, accumulatedDepreciation: "0", nbv: cost })],
  });

  it("splits with If-Match and the Idempotency-Key, then links the new assets", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? {
            status: 201,
            etag: '"5"',
            body: {
              id: "tr",
              kind: "split",
              transferDate: "2026-02-15",
              legalEntityId: LE,
              sourceAssetId: ASSET,
              sourceAssetNumber: "FA-1",
              createdBy: null,
              createdAt: "",
              source: asset({ status: "Transferred", version: 5 }),
              targets: [target(T1, "FA-1A", "60", "720"), { ...target(T2, "FA-1B", "40", "480"), lineNo: 2 }],
            },
          }
        : { status: 200, body: { ...asset(), books: [book()] }, etag: '"4"' };
    const { container } = renderAt(`/assets/${ASSET}/transfer`);
    expect(await screen.findByRole("heading", { name: "Transfer or split FA-1" })).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Open books" })).getByText("1100 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Transfer date"), { target: { value: "2026-02-15" } });
    const numbers = screen.getAllByLabelText("Asset number");
    const names = screen.getAllByLabelText("Name");
    const shares = screen.getAllByLabelText("Share (%)");
    fireEvent.change(numbers[0], { target: { value: "FA-1A" } });
    fireEvent.change(names[0], { target: { value: "Front" } });
    fireEvent.change(shares[0], { target: { value: "60" } });
    fireEvent.change(numbers[1], { target: { value: "FA-1B" } });
    fireEvent.change(names[1], { target: { value: "Rear" } });
    fireEvent.change(shares[1], { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "Split asset" }));
    expect(await screen.findByText("The shares total 90.00%; they must total 100.00%.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);

    fireEvent.change(shares[1], { target: { value: "40" } });
    fireEvent.click(screen.getByRole("button", { name: "Split asset" }));
    expect(await screen.findByRole("status")).toHaveTextContent("FA-1 is now transferred. Its books moved to 2 new assets");
    const post = posts()[0];
    expect(post.url.pathname).toBe(`/api/v1/assets/${ASSET}/transfer`);
    expect(post.headers.get("If-Match")).toBe('"4"');
    expect(post.headers.get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(post.body).toEqual({
      kind: "split",
      transferDate: "2026-02-15",
      parts: [
        { assetNumber: "FA-1A", name: "Front", percentage: "60" },
        { assetNumber: "FA-1B", name: "Rear", percentage: "40" },
      ],
    });
    const created = screen.getByRole("table", { name: "New assets" });
    expect(within(created).getByRole("link", { name: "FA-1B" })).toHaveAttribute("href", `/assets/${T2}`);
    expect(within(created).getAllByText("IFRS 720 USD")).toHaveLength(2); // cost and NBV of the new book
  });

  it("transfers to another legal entity with a single 100% target", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? {
            status: 201,
            etag: '"5"',
            body: { id: "tr", kind: "transfer", transferDate: "2026-05-01", legalEntityId: LE, sourceAssetId: ASSET, sourceAssetNumber: "FA-1", createdBy: null, createdAt: "", targets: [target(T1, "FA-9", "100", "1200")] },
          }
        : { status: 200, body: { ...asset(), books: [book()] }, etag: '"4"' };
    renderAt(`/assets/${ASSET}/transfer`);
    fireEvent.change(await screen.findByLabelText("Kind"), { target: { value: "transfer" } });
    expect(screen.getAllByLabelText("Asset number")).toHaveLength(1);
    expect(screen.getByLabelText("Share (%)")).toHaveValue("100");
    fireEvent.change(screen.getByLabelText("Asset number"), { target: { value: "FA-9" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Van" } });
    fireEvent.change(screen.getByLabelText("Target legal entity"), { target: { value: LE2 } });
    fireEvent.click(screen.getByRole("button", { name: "Transfer asset" }));
    expect(await screen.findByRole("status")).toHaveTextContent("moved to 1 new asset,");
    expect((posts()[0].body as { parts: unknown[] }).parts).toEqual([{ assetNumber: "FA-9", name: "Van", percentage: "100", legalEntityId: LE2 }]);
  });

  it("explains a stale asset and the service's refusals", async () => {
    let reply: Reply = { status: 412, body: { code: "AST-0006", detail: "the asset changed since you read it" } };
    handler = (_url, init) => (init.method === "POST" ? reply : { status: 200, body: { ...asset(), books: [book()] }, etag: '"4"' });
    renderAt(`/assets/${ASSET}/transfer`);
    fireEvent.change(await screen.findByLabelText("Kind"), { target: { value: "transfer" } });
    fireEvent.change(screen.getByLabelText("Asset number"), { target: { value: "FA-9" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Van" } });
    fireEvent.click(screen.getByRole("button", { name: "Transfer asset" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The asset changed since you opened it");
    expect(alert).toHaveTextContent("(AST-0006)");
    reply = { status: 422, body: { code: "AST-0508", detail: "transfer date precedes the end of the last depreciated period 2026-01" } };
    fireEvent.click(screen.getByRole("button", { name: "Transfer asset" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Could not record the transfer"));
    expect(screen.getByRole("alert")).toHaveTextContent("(AST-0508)");
  });

  it("offers nothing to transfer on a transferred asset and reports a load failure", async () => {
    handler = () => ({ status: 200, body: { ...asset({ status: "Transferred" }), books: [book({ status: "Transferred", cost: "0", nbv: "0", accumulatedDepreciation: "0" })] }, etag: '"5"' });
    const { unmount } = renderAt(`/assets/${ASSET}/transfer`);
    expect(await screen.findByText(/only an open asset with an open book can be transferred or split/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Split asset" })).toBeDisabled();
    expect(screen.getByText("No open books")).toBeInTheDocument();
    unmount();

    handler = () => ({ status: 404, body: { code: "AST-0001", detail: "no asset" } });
    renderAt(`/assets/${ASSET}/transfer`);
    expect(await screen.findByRole("alert")).toHaveTextContent("no asset (AST-0001)");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("shows the transfer action, the transfers and the group of a book on the asset page", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/transfers")) {
        return {
          status: 200,
          body: {
            items: [
              { id: "tr", kind: "split", transferDate: "2026-02-15", legalEntityId: LE, sourceAssetId: "src", sourceAssetNumber: "FA-0", createdBy: null, createdAt: "", targets: [{ lineNo: 1, percentage: "60", asset: asset(), books: [] }] },
            ],
          },
        };
      }
      if (url.pathname.endsWith("/schedule")) return { status: 200, body: { bookId: BOOK, bookCode: "IFRS", currency: "USD", total: "0.00", lines: [] } };
      if (url.pathname.endsWith("/transactions") || url.pathname.endsWith("/additions") || url.pathname.endsWith("/insurance-policies")) return { status: 200, body: { items: [] } };
      return { status: 200, body: { ...asset(), books: [book({ groupAssetId: GROUP, groupCode: "SP-2026" })] }, etag: '"4"' };
    };
    const { container } = renderAt(`/assets/${ASSET}`);
    expect(await screen.findByRole("link", { name: "Transfer or split" })).toHaveAttribute("href", `/assets/${ASSET}/transfer`);
    expect(within(screen.getByRole("table", { name: "Books" })).getByRole("link", { name: "SP-2026" })).toHaveAttribute("href", `/assets/group-assets/${GROUP}`);
    const transfers = await screen.findByRole("table", { name: "Transfers" });
    await waitFor(() => expect(within(transfers).getByRole("link", { name: "FA-0" })).toHaveAttribute("href", "/assets/src"));
    expect(within(transfers).getByText("FA-1 (60%)")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });
});

const group = {
  id: GROUP,
  legalEntityId: LE,
  groupCode: "SP-2026",
  name: "Sammelposten 2026",
  classCode: "GWG",
  bookCode: "DE-TAX",
  taxYear: 2026,
  currency: "EUR",
  method: "StraightLine",
  factor: "0",
  switchToStraightLine: false,
  serviceLifePeriods: 60,
  convention: "None",
  inServiceDate: "2026-01-01",
  status: "Open",
  basis: "24000",
  memberCount: 40,
  version: 1,
  createdAt: "2026-06-01T08:00:00Z",
};

describe("validateGroup", () => {
  const values = (patch: Partial<GroupValues> = {}): GroupValues => ({
    legalEntityId: LE,
    groupCode: "SP-2026",
    name: "Pool",
    classCode: "GWG",
    bookCode: "DE-TAX",
    taxYear: "2026",
    method: "StraightLine",
    factor: "2",
    switchToStraightLine: true,
    serviceLifePeriods: "60",
    convention: "None",
    memberAssetIds: [M1],
    ...patch,
  });

  it("builds the request; the factor goes only with declining balance", () => {
    expect(validateGroup(values()).input).toEqual({
      legalEntityId: LE,
      groupCode: "SP-2026",
      name: "Pool",
      classCode: "GWG",
      bookCode: "DE-TAX",
      taxYear: 2026,
      method: "StraightLine",
      serviceLifePeriods: 60,
      convention: "None",
      memberAssetIds: [M1],
    });
    expect(validateGroup(values({ method: "DecliningBalance", serviceLifePeriods: "5", convention: "HalfYear" })).input).toMatchObject({ factor: "2", switchToStraightLine: true });
  });

  it("flags every invalid field", () => {
    const { errors, input } = validateGroup(
      values({ legalEntityId: "le", groupCode: "sp", name: "", classCode: "", bookCode: "tax", taxYear: "26", serviceLifePeriods: "0", convention: "HalfYear", memberAssetIds: [] }),
    );
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["bookCode", "classCode", "convention", "groupCode", "legalEntityId", "memberAssetIds", "name", "serviceLifePeriods", "taxYear"]);
    expect(validateGroup(values({ method: "DecliningBalance", serviceLifePeriods: "101", convention: "None", factor: "4" })).errors).toEqual({
      serviceLifePeriods: "Enter 1 to 100 years.",
      factor: "Enter a factor from 1 to 3, such as 2 for 200%.",
    });
  });
});

describe("group assets", () => {
  it("lists group assets", async () => {
    handler = () => ({ status: 200, body: { items: [group] } });
    const { container } = renderAt("/assets/group-assets");
    const table = await screen.findByRole("table", { name: "Group assets" });
    await waitFor(() => expect(within(table).getByText("24000 EUR")).toBeInTheDocument());
    expect(within(table).getByText("40")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New group asset" })).toHaveAttribute("href", "/assets/group-assets/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty and the error state of the list", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const { unmount } = renderAt("/assets/group-assets");
    expect(await screen.findByText("No group assets yet")).toBeInTheDocument();
    unmount();
    handler = () => ({ status: 403, body: { code: "AUTH-0002", detail: "missing duty AssetView or AssetConfigure" } });
    renderAt("/assets/group-assets");
    expect(await screen.findByRole("alert")).toHaveTextContent("missing duty AssetView or AssetConfigure (AUTH-0002)");
  });

  it("picks open members of the class in the legal entity and creates the group", async () => {
    const open = [
      asset({ id: M1, assetNumber: "GWG-1", name: "Laptop 1", classCode: "GWG" }),
      asset({ id: M2, assetNumber: "GWG-2", name: "Laptop 2", classCode: "GWG" }),
      asset({ id: M3, assetNumber: "IT-1", name: "Server", classCode: "IT" }),
    ];
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: { ...group, memberCount: 2 }, etag: '"1"' };
      if (url.pathname === "/api/v1/assets") return { status: 200, body: { items: open } };
      return { status: 200, body: { ...group, memberCount: 2, members: [], schedule: [] } };
    };
    const { container } = renderAt("/assets/group-assets/new");
    expect(await screen.findByText("Enter the legal entity to choose its open assets.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Asset class"), { target: { value: "GWG" } });
    expect(await screen.findByLabelText("GWG-1 Laptop 1 (GWG)")).toBeInTheDocument();
    const list = calls.find((c) => c.url.pathname === "/api/v1/assets");
    expect(list?.url.searchParams.get("status")).toBe("Open");
    expect(list?.url.searchParams.get("legalEntityId")).toBe(LE);
    expect(screen.queryByLabelText("IT-1 Server (IT)")).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Create group asset" }));
    expect(await screen.findByText("Choose at least one member asset.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);

    fireEvent.click(screen.getByLabelText("Select all 2 shown"));
    expect(screen.getByText("Members (2 chosen)")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Group code"), { target: { value: "SP-2026" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Sammelposten 2026" } });
    fireEvent.change(screen.getByLabelText("Tax book"), { target: { value: "DE-TAX" } });
    fireEvent.change(screen.getByLabelText("Tax year"), { target: { value: "2026" } });
    fireEvent.click(screen.getByRole("button", { name: "Create group asset" }));
    expect(await screen.findByRole("heading", { name: "SP-2026 Sammelposten 2026" })).toBeInTheDocument();
    const post = posts()[0];
    expect(post.url.pathname).toBe("/api/v1/assets/group-assets");
    expect(post.headers.get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(post.body).toEqual({
      legalEntityId: LE,
      groupCode: "SP-2026",
      name: "Sammelposten 2026",
      classCode: "GWG",
      bookCode: "DE-TAX",
      taxYear: 2026,
      method: "StraightLine",
      serviceLifePeriods: 60,
      convention: "None",
      memberAssetIds: [M1, M2],
    });
  });

  it("shows a membership conflict from the service (AST-0535)", async () => {
    handler = (_url, init) => {
      if (init.method === "POST") return { status: 409, body: { code: "AST-0535", detail: "asset GWG-1 already belongs to group SP-2025 for book DE-TAX" } };
      return { status: 200, body: { items: [asset({ id: M1, assetNumber: "GWG-1", name: "Laptop 1", classCode: "GWG" })] } };
    };
    renderAt("/assets/group-assets/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.click(await screen.findByLabelText("GWG-1 Laptop 1 (GWG)"));
    for (const [label, value] of [
      ["Group code", "SP-2026"],
      ["Name", "Pool"],
      ["Asset class", "GWG"],
      ["Tax book", "DE-TAX"],
    ]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value } });
    }
    fireEvent.click(screen.getByRole("button", { name: "Create group asset" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("An asset is already in a group");
    expect(alert).toHaveTextContent("(AST-0535)");
  });

  it("shows a group's basis, members and the projected yearly charge (AC-37.10)", async () => {
    const schedule = ["2026", "2027", "2028", "2029", "2030"].map((year, i) => ({ year, amount: "4800", accumulated: String(4800 * (i + 1)), nbv: String(24000 - 4800 * (i + 1)) }));
    handler = () => ({
      status: 200,
      body: { ...group, members: [{ assetId: M1, assetNumber: "GWG-101", name: "Laptop", assetBookId: "b1", bookStatus: "Open", inServiceDate: "2026-03-01", cost: "600" }], schedule },
    });
    const { container } = renderAt(`/assets/group-assets/${GROUP}`);
    expect(await screen.findByRole("heading", { name: "SP-2026 Sammelposten 2026" })).toBeInTheDocument();
    const years = screen.getByRole("table", { name: "Group schedule" });
    const rows = within(years).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(5);
    expect(rows[0]).toHaveTextContent("20264800 EUR4800 EUR19200 EUR");
    expect(rows[4]).toHaveTextContent("20304800 EUR24000 EUR0 EUR");
    expect(within(screen.getByRole("table", { name: "Members" })).getByRole("link", { name: "GWG-101" })).toHaveAttribute("href", `/assets/${M1}`);
    expect(screen.getAllByText("24000 EUR").length).toBeGreaterThan(0);
    expect(screen.getByText(/lists each member book as an exception \(AST-0035\)/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("reports an unknown group asset", async () => {
    handler = () => ({ status: 404, body: { code: "AST-0032", detail: "no group asset with id x" } });
    renderAt(`/assets/group-assets/${GROUP}`);
    expect(await screen.findByRole("alert")).toHaveTextContent("no group asset with id x (AST-0032)");
    expect(screen.getByRole("link", { name: "Back to group assets" })).toHaveAttribute("href", "/assets/group-assets");
  });
});
