import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const VENDOR = "22222222-2222-4222-8222-222222222222";
const UNIT = "44444444-4444-4444-8444-444444444444";
const CAT_ID = "99999999-9999-4999-8999-999999999999";
const ENTRY_ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const BASE = "/api/v1/procurement";

afterEach(() => vi.unstubAllGlobals());

const entry = {
  id: ENTRY_ID,
  entryNo: 1,
  vendorId: VENDOR,
  name: "Safety gloves",
  category: "MRO",
  unit: "PR",
  unitPrice: "4.25",
  currency: "USD",
  leadTimeDays: 3,
  preferred: true,
};

const catalog = {
  id: CAT_ID,
  number: "CAT-00000001",
  legalEntityId: LE,
  name: "Plant MRO",
  status: "DRAFT",
  validFrom: "2027-01-01",
  validTo: "2027-12-31",
  orgUnitIds: [],
  version: 1,
  entryCount: 1,
  entries: [entry],
  createdAt: "2026-10-26T10:00:00Z",
  updatedAt: "2026-10-26T10:00:00Z",
};

describe("catalog list", () => {
  it("shows a loading table, then rows that open the detail page", async () => {
    let release: (r: Response) => void = () => undefined;
    stubFetch(() => new Promise<Response>((resolve) => (release = resolve)));
    const { router } = renderAt("/purchasing/catalogs");
    expect(await screen.findByRole("table", { name: "Procurement catalogs" })).toHaveAttribute("aria-busy", "true");
    release(json({ items: [{ ...catalog, status: "PUBLISHED", entryCount: 12, entries: undefined }] }));
    const table = await findTable({ name: "Procurement catalogs" });
    await waitFor(() => expect(within(table).getByText("CAT-00000001")).toBeInTheDocument());
    expect(within(table).getByText("Published")).toBeInTheDocument();
    expect(within(table).getByText("12")).toBeInTheDocument();
    fireEvent.click(within(table).getByRole("button", { name: "CAT-00000001" }));
    expect(router.state.location.pathname).toBe(`/purchasing/catalogs/${CAT_ID}`);
  });

  it("explains an empty list and filters by status", async () => {
    const mock = stubFetch(() => json({ items: [] }));
    const { container } = renderAt("/purchasing/catalogs");
    expect(await screen.findByRole("heading", { name: "No catalogs yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "RETIRED" } });
    await waitFor(() => expect(String(mock.mock.calls.at(-1)?.[0])).toBe(`${BASE}/catalogs?status=RETIRED`));
  });

  it("shows the problem code on error and retries", async () => {
    const mock = stubFetch(() => problem(403, "AUTH-0003", "missing duty"));
    renderAt("/purchasing/catalogs");
    expect(await screen.findByText(/missing duty/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(mock).toHaveBeenCalledTimes(2));
  });
});

describe("new catalog", () => {
  it("validates before calling the service", async () => {
    const mock = stubFetch(() => json({}));
    renderAt("/purchasing/catalogs/new");
    fireEvent.click(await screen.findByRole("button", { name: "Add item" }));
    fireEvent.click(screen.getByRole("button", { name: "Save catalog" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.getAllByText("Enter a name.")).toHaveLength(2);
    expect(screen.getByText("Enter the vendor id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter a price of zero or more.")).toBeInTheDocument();
    fireEvent.change(within(screen.getByRole("group", { name: "Item 1" })).getByLabelText("Image link"), { target: { value: "javascript:alert(1)" } });
    fireEvent.click(screen.getByRole("button", { name: "Save catalog" }));
    expect(await screen.findByText("Enter an http(s) image link.")).toBeInTheDocument();
    expect(mock).not.toHaveBeenCalled();
  });

  it("posts the catalog with its first item and an Idempotency-Key, then opens it", async () => {
    const mock = stubFetch((_url, init) => (init.method === "POST" ? json(catalog, 201, { ETag: '"1"' }) : json(catalog)));
    const { router } = renderAt("/purchasing/catalogs/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: " Plant MRO " } });
    fireEvent.change(screen.getByLabelText("Valid to"), { target: { value: "2027-12-31" } });
    fireEvent.change(screen.getByLabelText("Operating unit ids"), { target: { value: UNIT } });
    fireEvent.click(screen.getByRole("button", { name: "Add item" }));
    const item = screen.getByRole("group", { name: "Item 1" });
    fireEvent.change(within(item).getByLabelText("Item name"), { target: { value: "Safety gloves" } });
    fireEvent.change(within(item).getByLabelText("Vendor"), { target: { value: VENDOR } });
    fireEvent.change(within(item).getByLabelText("Category"), { target: { value: "MRO" } });
    fireEvent.change(within(item).getByLabelText("Unit"), { target: { value: "PR" } });
    fireEvent.change(within(item).getByLabelText("Unit price"), { target: { value: "4.25" } });
    fireEvent.change(within(item).getByLabelText("Lead time (days)"), { target: { value: "3" } });
    fireEvent.change(within(item).getByLabelText("Image link"), { target: { value: "https://img.example.com/gloves.png" } });
    fireEvent.click(within(item).getByLabelText("Preferred item"));
    fireEvent.click(screen.getByRole("button", { name: "Save catalog" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/catalogs/${CAT_ID}`));
    const [url, init] = mock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(`${BASE}/catalogs`);
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/[0-9a-f-]{36}/);
    const body = JSON.parse(String(init.body));
    expect(body).toMatchObject({ legalEntityId: LE, name: "Plant MRO", validTo: "2027-12-31", orgUnitIds: [UNIT] });
    expect(body.entries[0]).toEqual({
      vendorId: VENDOR,
      name: "Safety gloves",
      category: "MRO",
      unit: "PR",
      unitPrice: "4.25",
      currency: "USD",
      leadTimeDays: 3,
      preferred: true,
      imageUrl: "https://img.example.com/gloves.png",
    });
  });

  it("maps a service field error back onto the form", async () => {
    stubFetch(() => problem(422, "COM-0001", "validTo must not be before validFrom", { validTo: "must not be before validFrom" }));
    renderAt("/purchasing/catalogs/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Save catalog" }));
    expect(await screen.findByText("must not be before validFrom")).toBeInTheDocument();
    expect(screen.getAllByRole("alert").some((a) => a.textContent?.includes("COM-0001"))).toBe(true);
  });
});

describe("catalog detail", () => {
  it("shows the summary and items of a draft with publish, add and remove", async () => {
    stubFetch(() => json(catalog, 200, { ETag: '"1"' }));
    const { container } = renderAt(`/purchasing/catalogs/${CAT_ID}`);
    expect(await screen.findByRole("heading", { name: "Catalog CAT-00000001" })).toBeInTheDocument();
    expect(screen.getByText("Every operating unit")).toBeInTheDocument();
    const items = screen.getByRole("table", { name: "Catalog items" });
    expect(within(items).getByText("Safety gloves (preferred)")).toBeInTheDocument();
    expect(within(items).getByText("4.25 USD")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Publish" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove Safety gloves" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("publishes with If-Match and an Idempotency-Key, then offers retire and hides the editing", async () => {
    const mock = stubFetch((_url, init) =>
      init.method === "POST" ? json({ ...catalog, status: "PUBLISHED", version: 2 }) : json(catalog, 200, { ETag: '"1"' }),
    );
    renderAt(`/purchasing/catalogs/${CAT_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Publish" }));
    expect(await screen.findByText("The catalog is now published.")).toBeInTheDocument();
    const [url, init] = mock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe(`${BASE}/catalogs/${CAT_ID}:publish`);
    const headers = init.headers as Record<string, string>;
    expect(headers["If-Match"]).toBe('"1"');
    expect(headers["Idempotency-Key"]).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retire" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove Safety gloves" })).toBeNull();
    expect(screen.queryByText("Add an item")).toBeNull();
  });

  it("shows the problem when publishing an empty catalog", async () => {
    stubFetch((_url, init) =>
      init.method === "POST" ? problem(422, "PRC-CAT-001", "a catalog needs at least one entry before it is published") : json({ ...catalog, entries: [] }),
    );
    renderAt(`/purchasing/catalogs/${CAT_ID}`);
    expect(await screen.findByText("No items yet")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    expect(await screen.findByText(/PRC-CAT-001/)).toBeInTheDocument();
  });

  it("adds an item under the catalog version and shows it", async () => {
    const added = { ...entry, id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", entryNo: 2, name: "Tape", preferred: false };
    const mock = stubFetch((_url, init) =>
      init.method === "POST" ? json({ ...catalog, version: 2, entries: [entry, added] }) : json(catalog, 200, { ETag: '"1"' }),
    );
    renderAt(`/purchasing/catalogs/${CAT_ID}`);
    const form = await screen.findByText("Add an item");
    const card = form.closest("form") as HTMLElement;
    fireEvent.click(within(card).getByRole("button", { name: "Add item" }));
    expect(await within(card).findByText("Enter a name.")).toBeInTheDocument();
    fireEvent.change(within(card).getByLabelText("Item name"), { target: { value: "Tape" } });
    fireEvent.change(within(card).getByLabelText("Vendor"), { target: { value: VENDOR } });
    fireEvent.change(within(card).getByLabelText("Category"), { target: { value: "MRO" } });
    fireEvent.change(within(card).getByLabelText("Unit price"), { target: { value: "1.10" } });
    fireEvent.click(within(card).getByRole("button", { name: "Add item" }));
    expect(await screen.findByText("Added Tape.")).toBeInTheDocument();
    const [url, init] = mock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe(`${BASE}/catalogs/${CAT_ID}/entries`);
    expect((init.headers as Record<string, string>)["If-Match"]).toBe('"1"');
    expect(JSON.parse(String(init.body)).entries[0]).toMatchObject({ name: "Tape", unitPrice: "1.10", vendorId: VENDOR });
    expect(within(screen.getByRole("table", { name: "Catalog items" })).getByText("Tape")).toBeInTheDocument();
  });

  it("reports a version conflict on remove, then removes the item with If-Match", async () => {
    let call = 0;
    const mock = stubFetch((_url, init) => {
      if (init.method === "DELETE") return call++ === 0 ? problem(409, "PRC-VERSION-CONFLICT", "catalog is at version 3") : json({ ...catalog, version: 2, entries: [] });
      return json(catalog, 200, { ETag: '"1"' });
    });
    renderAt(`/purchasing/catalogs/${CAT_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Remove Safety gloves" }));
    expect(await screen.findByText(/catalog is at version 3/)).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Catalog items" })).getByText("Safety gloves (preferred)")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove Safety gloves" }));
    expect(await screen.findByText("Removed Safety gloves.")).toBeInTheDocument();
    const [url, init] = mock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe(`${BASE}/catalogs/${CAT_ID}/entries/${ENTRY_ID}`);
    expect((init.headers as Record<string, string>)["If-Match"]).toBe('"1"');
    expect(await screen.findByText("No items yet")).toBeInTheDocument();
  });

  it("explains a missing catalog and a load failure", async () => {
    stubFetch(() => problem(404, "PRC-CAT-NOT-FOUND", "procurement catalog not found"));
    const first = renderAt(`/purchasing/catalogs/${CAT_ID}`);
    expect(await screen.findByRole("heading", { name: "Catalog not found" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to catalogs" })).toHaveAttribute("href", "/purchasing/catalogs");
    first.unmount();
    stubFetch(() => problem(500, "COM-0500", "boom"));
    renderAt(`/purchasing/catalogs/${CAT_ID}`);
    expect(await screen.findByText(/boom/)).toBeInTheDocument();
  });
});

describe("requisition catalog picker", () => {
  const hit = {
    ...entry,
    productId: "33333333-3333-4333-8333-333333333333",
    imageUrl: "https://img.example.com/gloves.png",
    catalogId: CAT_ID,
    catalogNumber: "CAT-00000001",
    catalogName: "Plant MRO",
  };

  it("asks for the legal entity first, then searches and adds a catalog line", async () => {
    const saved = { id: "r1", number: "PR-1", legalEntityId: LE, requesterId: VENDOR, status: "DRAFT", currency: "USD", total: "42.5", approverChain: [], version: 1, lines: [], createdAt: "2026-10-26T10:00:00Z", updatedAt: "2026-10-26T10:00:00Z" };
    const mock = stubFetch((url, init) => {
      if (init.method === "POST") return json(saved, 201);
      return url.includes("/requisitions/") ? json(saved) : json({ items: [hit] });
    });
    renderAt("/purchasing/requisitions/new");
    const search = await screen.findByRole("button", { name: "Search catalogs" });
    expect(search).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Operating unit"), { target: { value: UNIT } });
    fireEvent.change(screen.getByLabelText("Search items"), { target: { value: "glove" } });
    fireEvent.click(screen.getByRole("button", { name: "Search catalogs" }));
    const results = await findTable({ name: "Catalog search results" });
    expect(String(mock.mock.calls[0][0])).toBe(`${BASE}/catalog-entries?legalEntityId=${LE}&operatingUnitId=${UNIT}&q=glove`);
    expect(within(results).getByText("4.25 USD per PR")).toBeInTheDocument();
    expect(within(results).getByRole("img", { name: "Safety gloves" })).toHaveAttribute("src", hit.imageUrl);
    fireEvent.click(within(results).getByRole("button", { name: "Add Safety gloves" }));
    const line = screen.getByRole("group", { name: "Line 1" });
    expect(within(line).getByText(/Catalog item: Safety gloves at 4.25 USD per PR/)).toBeInTheDocument();
    expect(within(line).queryByLabelText("Description")).toBeNull();
    fireEvent.change(within(line).getByLabelText("Quantity"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save requisition" }));
    await waitFor(() => expect(mock.mock.calls.some(([, i]) => (i as RequestInit).method === "POST")).toBe(true));
    const post = mock.mock.calls.find(([, i]) => (i as RequestInit).method === "POST")!;
    const body = JSON.parse(String((post[1] as RequestInit).body));
    expect(body.operatingUnitId).toBe(UNIT);
    expect(body.lines).toHaveLength(1);
    expect(body.lines[0]).toMatchObject({ catalogEntryId: ENTRY_ID, qty: "10", unit: "PR" });
    expect(body.lines[0].description).toBeUndefined();
  });

  it("explains no matches and a failed search", async () => {
    stubFetch(() => json({ items: [] }));
    const first = renderAt("/purchasing/requisitions/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.click(screen.getByRole("button", { name: "Search catalogs" }));
    expect(await screen.findByRole("heading", { name: "No catalog items match" })).toBeInTheDocument();
    first.unmount();
    stubFetch(() => problem(403, "AUTH-0003", "legal entity is outside the caller's scope"));
    renderAt("/purchasing/requisitions/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.click(screen.getByRole("button", { name: "Search catalogs" }));
    expect(await screen.findByText(/AUTH-0003/)).toBeInTheDocument();
  });

  it("shows the service refusal of a catalog line that is no longer available", async () => {
    stubFetch((_url, init) =>
      init.method === "POST" ? problem(422, "PRC-CAT-004", "catalog CAT-00000001 is not published to this legal entity and unit") : json({ items: [hit] }),
    );
    renderAt("/purchasing/requisitions/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.click(screen.getByRole("button", { name: "Search catalogs" }));
    fireEvent.click(await screen.findByRole("button", { name: "Add Safety gloves" }));
    fireEvent.click(screen.getByRole("button", { name: "Save requisition" }));
    expect(await screen.findByText(/PRC-CAT-004/)).toBeInTheDocument();
  });
});

describe("requisition detail", () => {
  it("marks catalog and non-catalog lines", async () => {
    const line = { lineNo: 1, description: "Safety gloves", category: "MRO", qty: "10", unit: "PR", estimatedUnitPrice: "4.25", amount: "42.5", requestedDate: "2026-11-02", status: "OPEN" };
    const requisition = {
      id: "r1",
      number: "PR-1",
      legalEntityId: LE,
      requesterId: VENDOR,
      status: "DRAFT",
      currency: "USD",
      total: "52.5",
      approverChain: [],
      version: 1,
      lines: [
        { ...line, id: "l1", catalogEntryId: ENTRY_ID },
        { ...line, id: "l2", lineNo: 2, description: "Ad hoc repair", qty: "1", unit: "EA", estimatedUnitPrice: "10", amount: "10" },
      ],
      createdAt: "2026-10-26T10:00:00Z",
      updatedAt: "2026-10-26T10:00:00Z",
    };
    stubFetch(() => json(requisition, 200, { ETag: '"1"' }));
    renderAt("/purchasing/requisitions/r1");
    const rows = within(await findTable({ name: "Requisition lines" })).getAllByRole("row");
    expect(within(rows[1]).getByText("Catalog")).toBeInTheDocument();
    expect(within(rows[2]).getByText("Non-catalog")).toBeInTheDocument();
  });

  it("says which line a split line was cut from", async () => {
    const line = { description: "Bolts", category: "MRO", qty: "10", unit: "EA", estimatedUnitPrice: "1", amount: "10", requestedDate: "2026-11-02", status: "OPEN" };
    const requisition = {
      id: "r1",
      number: "PR-1",
      legalEntityId: LE,
      requesterId: VENDOR,
      status: "APPROVED",
      currency: "USD",
      total: "20",
      approverChain: [],
      version: 2,
      lines: [
        { ...line, id: "l1", lineNo: 1 },
        { ...line, id: "l2", lineNo: 2, splitFromLineId: "l1" },
      ],
      createdAt: "2026-10-26T10:00:00Z",
      updatedAt: "2026-10-26T10:00:00Z",
    };
    stubFetch(() => json(requisition, 200, { ETag: '"2"' }));
    renderAt("/purchasing/requisitions/r1");
    const rows = within(await findTable({ name: "Requisition lines" })).getAllByRole("row");
    expect(within(rows[1]).queryByText(/split from/)).not.toBeInTheDocument();
    expect(within(rows[2]).getByText("(split from line 1)")).toBeInTheDocument();
  });
});
