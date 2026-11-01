import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const VENDOR = "22222222-2222-4222-8222-222222222222";
const CAT_ID = "99999999-9999-4999-8999-999999999999";
const IMP_ID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const BASE = "/api/v1/procurement";

afterEach(() => vi.unstubAllGlobals());

const staged = {
  id: IMP_ID,
  catalogId: CAT_ID,
  legalEntityId: LE,
  vendorId: VENDOR,
  format: "CSV",
  fileName: "gloves.csv",
  status: "STAGED",
  totalRows: 3,
  acceptableRows: 2,
  errorRows: 1,
  acceptedRows: 0,
  rejectedRows: 0,
  entriesAdded: 0,
  version: 1,
  createdAt: "2026-11-01T10:00:00Z",
  updatedAt: "2026-11-01T10:00:00Z",
};

const rows = [
  { rowNo: 1, status: "ACCEPTABLE", raw: { vendorPartNo: "G-1", name: "Gloves", category: "MRO", unit: "PR", unitPrice: "4.25", currency: "USD" } },
  { rowNo: 2, status: "ERROR", reason: "unit is required", raw: { vendorPartNo: "G-2", name: "Goggles", category: "MRO", unitPrice: "9", currency: "USD" } },
  { rowNo: 3, status: "ACCEPTABLE", raw: { vendorPartNo: "G-3", name: "Tape", category: "MRO", unit: "RL", unitPrice: "1.1", currency: "USD" } },
];

describe("vendor import list", () => {
  it("shows a loading table, then imports that open the detail page", async () => {
    let release: (r: Response) => void = () => undefined;
    stubFetch(() => new Promise<Response>((resolve) => (release = resolve)));
    const { router } = renderAt("/purchasing/catalog-imports");
    expect(await screen.findByRole("table", { name: "Vendor catalog imports" })).toHaveAttribute("aria-busy", "true");
    release(json({ items: [staged] }));
    const table = await findTable({ name: "Vendor catalog imports" });
    await waitFor(() => expect(within(table).getByText("gloves.csv")).toBeInTheDocument());
    expect(within(table).getByText("Staged", { selector: "span" })).toBeInTheDocument();
    fireEvent.click(within(table).getByRole("button", { name: "2026-11-01" }));
    expect(router.state.location.pathname).toBe(`/purchasing/catalog-imports/${IMP_ID}`);
  });

  it("explains an empty list and filters by status", async () => {
    const mock = stubFetch(() => json({ items: [] }));
    const { container } = renderAt("/purchasing/catalog-imports");
    expect(await screen.findByRole("heading", { name: "No vendor imports yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "COMMITTED" } });
    await waitFor(() => expect(String(mock.mock.calls.at(-1)?.[0])).toBe(`${BASE}/catalog-imports?status=COMMITTED`));
  });

  it("shows the problem code on error and retries", async () => {
    const mock = stubFetch(() => problem(403, "AUTH-0003", "missing duty"));
    renderAt("/purchasing/catalog-imports");
    expect(await screen.findByText(/missing duty \(AUTH-0003\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(mock.mock.calls.length).toBeGreaterThan(1));
  });
});

describe("vendor import form", () => {
  it("validates before it calls the service", async () => {
    const mock = stubFetch(() => json({}));
    renderAt(`/purchasing/catalogs/${CAT_ID}/import`);
    fireEvent.click(await screen.findByRole("button", { name: "Stage file" }));
    expect(await screen.findByText("Enter the vendor id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Choose a CSV file or paste its rows.")).toBeInTheDocument();
    expect(mock).not.toHaveBeenCalled();
  });

  it("posts the pasted rows with an Idempotency-Key and opens the staged import", async () => {
    const mock = stubFetch((url, init) => (init.method === "POST" ? json(staged, 201, { ETag: '"1"' }) : url.includes("/rows") ? json({ items: [] }) : json(staged)));
    const { router } = renderAt(`/purchasing/catalogs/${CAT_ID}/import`);
    fireEvent.change(await screen.findByLabelText("Vendor id"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Rows"), { target: { value: "name,category,unit,price,currency\nGloves,MRO,PR,4.25,USD\n" } });
    fireEvent.click(screen.getByRole("button", { name: "Stage file" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/catalog-imports/${IMP_ID}`));
    const [url, init] = mock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(`${BASE}/catalogs/${CAT_ID}/vendor-import`);
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/[0-9a-f-]{36}/);
    expect(JSON.parse(String(init.body))).toEqual({ vendorId: VENDOR, format: "CSV", csv: "name,category,unit,price,currency\nGloves,MRO,PR,4.25,USD\n" });
  });

  it("shows a file-level refusal with its code", async () => {
    stubFetch(() => problem(422, "PRC-CAT-005", "the header has no unit column"));
    renderAt(`/purchasing/catalogs/${CAT_ID}/import`);
    fireEvent.change(await screen.findByLabelText("Vendor id"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Rows"), { target: { value: "name\nx" } });
    fireEvent.click(screen.getByRole("button", { name: "Stage file" }));
    expect(await screen.findByText(/the header has no unit column \(PRC-CAT-005\)/)).toBeInTheDocument();
  });
});

describe("vendor import review", () => {
  function route(overrides: { header?: object; onPost?: (url: string, init: RequestInit) => Response } = {}) {
    return stubFetch((url, init) => {
      if (init.method === "POST") return overrides.onPost ? overrides.onPost(url, init) : json({ ...staged, version: 2 }, 200);
      if (url.includes("/rows")) return json({ items: rows });
      return json({ ...staged, ...overrides.header }, 200, { ETag: '"1"' });
    });
  }

  it("lists the staged rows with their problems and decision buttons", async () => {
    route();
    const { container } = renderAt(`/purchasing/catalog-imports/${IMP_ID}`);
    expect(await screen.findByRole("heading", { name: "Vendor catalog import" })).toBeInTheDocument();
    const table = await findTable({ name: "Staged rows" });
    await waitFor(() => expect(within(table).getByText("Goggles")).toBeInTheDocument());
    expect(within(table).getByText("unit is required")).toBeInTheDocument();
    expect(within(table).getByRole("button", { name: "Accept row 1" })).toBeInTheDocument();
    expect(within(table).queryByRole("button", { name: "Accept row 2" })).toBeNull();
    expect(screen.getByRole("button", { name: "Add 0 accepted to catalog" })).toBeDisabled();
    expect(pageViolations(container)).toEqual([]);
  });

  it("accepts one row under If-Match and filters rows by status", async () => {
    const mock = route();
    renderAt(`/purchasing/catalog-imports/${IMP_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Accept row 1" }));
    expect(await screen.findByText("Row 1 accepted.")).toBeInTheDocument();
    const post = mock.mock.calls.find(([, init]) => (init as RequestInit).method === "POST") as [string, RequestInit];
    expect(post[0]).toBe(`${BASE}/catalog-imports/${IMP_ID}/decisions`);
    expect((post[1].headers as Record<string, string>)["If-Match"]).toBe('"1"');
    expect(JSON.parse(String(post[1].body))).toEqual({ accept: [1] });
    fireEvent.change(screen.getByLabelText("Row status"), { target: { value: "ERROR" } });
    await waitFor(() => expect(String(mock.mock.calls.at(-1)?.[0])).toContain("/rows?status=ERROR"));
  });

  it("accepts all rows without errors, then commits and reports the added items", async () => {
    let posted = 0;
    const mock = route({
      onPost: (url) => {
        posted += 1;
        return url.endsWith(":commit")
          ? json({ ...staged, status: "COMMITTED", acceptedRows: 2, acceptableRows: 0, entriesAdded: 2, version: 4 })
          : json({ ...staged, acceptedRows: 2, acceptableRows: 0, version: 3 });
      },
    });
    renderAt(`/purchasing/catalog-imports/${IMP_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Accept all 2 rows without errors" }));
    expect(await screen.findByText("Every row without errors is accepted.")).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "Add 2 accepted to catalog" }));
    expect(await screen.findByText("Added 2 items to the catalog.")).toBeInTheDocument();
    expect(posted).toBe(2);
    const commit = mock.mock.calls.find(([url]) => String(url).endsWith(":commit")) as [string, RequestInit];
    expect(commit[0]).toBe(`${BASE}/catalog-imports/${IMP_ID}:commit`);
    expect((commit[1].headers as Record<string, string>)["If-Match"]).toBe('"3"');
    expect(screen.queryByRole("button", { name: /Accept row/ })).toBeNull();
    expect(screen.getByRole("link", { name: "Open catalog" })).toHaveAttribute("href", `/purchasing/catalogs/${CAT_ID}`);
  });

  it("shows the service problem when the commit is refused", async () => {
    route({
      header: { acceptedRows: 1, acceptableRows: 1 },
      onPost: () => problem(409, "PRC-VERSION-CONFLICT", "catalog import is at version 5, not 1; reload and retry"),
    });
    renderAt(`/purchasing/catalog-imports/${IMP_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Add 1 accepted to catalog" }));
    expect(await screen.findByText(/PRC-VERSION-CONFLICT/)).toBeInTheDocument();
  });

  it("cancels a staged import", async () => {
    const mock = route({ onPost: () => json({ ...staged, status: "CANCELLED", version: 2 }) });
    renderAt(`/purchasing/catalog-imports/${IMP_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel import" }));
    expect(await screen.findByText("The import was cancelled.")).toBeInTheDocument();
    const post = mock.mock.calls.find(([, init]) => (init as RequestInit).method === "POST") as [string, RequestInit];
    expect(post[0]).toBe(`${BASE}/catalog-imports/${IMP_ID}:cancel`);
  });

  it("explains an unknown import", async () => {
    stubFetch(() => problem(404, "PRC-CAT-IMPORT-NOT-FOUND", "catalog import not found"));
    renderAt(`/purchasing/catalog-imports/${IMP_ID}`);
    expect(await screen.findByRole("heading", { name: "Import not found" })).toBeInTheDocument();
  });

  it("shows the empty row list of a status with no rows", async () => {
    stubFetch((url) => (url.includes("/rows") ? json({ items: [] }) : json(staged, 200, { ETag: '"1"' })));
    renderAt(`/purchasing/catalog-imports/${IMP_ID}`);
    expect(await screen.findByRole("heading", { name: "No rows" })).toBeInTheDocument();
  });
});

describe("catalog page links to the import", () => {
  it("offers Import vendor file on a published catalog", async () => {
    stubFetch(() =>
      json({ id: CAT_ID, number: "CAT-00000001", legalEntityId: LE, name: "Plant MRO", status: "PUBLISHED", validFrom: "2027-01-01", orgUnitIds: [], version: 2, entryCount: 0, entries: [], createdAt: "", updatedAt: "" }),
    );
    renderAt(`/purchasing/catalogs/${CAT_ID}`);
    expect(await screen.findByRole("link", { name: "Import vendor file" })).toHaveAttribute("href", `/purchasing/catalogs/${CAT_ID}/import`);
  });
});

describe("catalog page pages its items", () => {
  const entry = (n: number) => ({ id: `e-${n}`, entryNo: n, vendorId: VENDOR, name: `Item ${n}`, category: "MRO", unit: "EA", unitPrice: "1", currency: "USD" });
  const catalog = { id: CAT_ID, number: "CAT-00000001", legalEntityId: LE, name: "Plant MRO", status: "PUBLISHED", validFrom: "2027-01-01", orgUnitIds: [], version: 3, createdAt: "", updatedAt: "" };

  it("reads the items after the first page from the entries endpoint", async () => {
    const mock = stubFetch((url) =>
      String(url).includes("/entries")
        ? json({ items: [entry(201)] })
        : json({ ...catalog, entryCount: 201, entries: [entry(1)], entriesNextCursor: "200" }),
    );
    renderAt(`/purchasing/catalogs/${CAT_ID}`);
    const table = await findTable({ name: "Catalog items" });
    await waitFor(() => expect(within(table).getByText("Item 1")).toBeInTheDocument());
    expect(screen.getByText("201")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show more items" }));
    await waitFor(() => expect(within(table).getByText("Item 201")).toBeInTheDocument());
    expect(String(mock.mock.calls.at(-1)?.[0])).toBe(`${BASE}/catalogs/${CAT_ID}/entries?limit=200&cursor=200`);
    expect(within(table).getByText("Item 1")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Show more items" })).not.toBeInTheDocument();
  });

  it("shows no more button when the first page holds every item", async () => {
    stubFetch(() => json({ ...catalog, entryCount: 1, entries: [entry(1)] }));
    renderAt(`/purchasing/catalogs/${CAT_ID}`);
    const table = await findTable({ name: "Catalog items" });
    await waitFor(() => expect(within(table).getByText("Item 1")).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Show more items" })).not.toBeInTheDocument();
  });
});
