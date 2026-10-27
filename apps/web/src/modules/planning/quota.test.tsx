import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { QuotaArrangement, QuotaSource } from "./api/client";
import planning from "./module";
import { EMPTY_QUOTA, EMPTY_SOURCE, nextSource, quotaDraftOf, validateQuota, type QuotaDraft } from "./QuotaPages";

const ITEM = "00000000-0000-0000-0000-0000000000b1";
const SITE = "00000000-0000-0000-0000-0000000000a1";
const VENDOR_A = "00000000-0000-0000-0000-0000000000e1";
const VENDOR_B = "00000000-0000-0000-0000-0000000000e2";
const WH = "00000000-0000-0000-0000-0000000000d1";

const source = (over: Partial<QuotaSource>): QuotaSource => ({
  id: "00000000-0000-0000-0000-000000000101",
  type: "vendor",
  vendorId: VENDOR_A,
  productionVersionId: null,
  fromWarehouseId: null,
  quota: "60.0000",
  baseQty: "0.000000",
  allocatedQty: "300.000000",
  maxQtyPerPeriod: null,
  periodDays: null,
  priority: 1,
  blocked: false,
  rating: "5",
  ...over,
});

/** The BR-22.12 arrangement: A quota 60 allocated 300 (rating 5), B quota 40 allocated 100 (rating 2.5), and a transfer fallback. */
const arrangement: QuotaArrangement = {
  id: "00000000-0000-0000-0000-0000000000f1",
  version: 1,
  itemId: ITEM,
  siteId: SITE,
  validFrom: "2026-11-01",
  validTo: "2026-12-31",
  split: false,
  minSplitQty: "0.000000",
  sources: [
    source({}),
    source({ id: "00000000-0000-0000-0000-000000000102", vendorId: VENDOR_B, quota: "40.0000", allocatedQty: "100.000000", priority: 2, rating: "2.5" }),
    source({ id: "00000000-0000-0000-0000-000000000103", type: "transfer", vendorId: null, fromWarehouseId: WH, quota: "0.0000", allocatedQty: "0", rating: null, maxQtyPerPeriod: "500.5", periodDays: 30 }),
  ],
};

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) => json({ type: "t", title: "x", status, code, detail }, status);

type Handler = [match: string, respond: (url: string, init?: RequestInit) => Response];

function stubApi(...routes: Handler[]) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    for (const [match, respond] of routes) {
      if (url.includes(match)) return respond(url, init);
    }
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function Location() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname + location.search}</div>;
}

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
      <Location />
    </MemoryRouter>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("quota arrangement rules", () => {
  const valid: QuotaDraft = {
    ...EMPTY_QUOTA,
    itemId: ITEM,
    siteId: SITE,
    validFrom: "2026-11-01",
    validTo: "2026-12-31",
    sources: [
      { ...EMPTY_SOURCE, ref: VENDOR_A, quota: "60", allocatedQty: "300" },
      { ...EMPTY_SOURCE, type: "transfer", ref: WH, quota: "0", maxQtyPerPeriod: "500", periodDays: "30" },
    ],
  };

  it("turns a valid draft into the request body with only the reference of each type", () => {
    const { errors, input } = validateQuota(valid);
    expect(errors).toEqual({});
    expect(input?.sources).toEqual([
      { type: "vendor", vendorId: VENDOR_A, quota: "60", baseQty: "0", allocatedQty: "300", maxQtyPerPeriod: null, periodDays: null, priority: 0, blocked: false },
      { type: "transfer", fromWarehouseId: WH, quota: "0", baseQty: "0", allocatedQty: "0", maxQtyPerPeriod: "500", periodDays: 30, priority: 0, blocked: false },
    ]);
  });

  it("names every rule the service enforces", () => {
    const { errors, input } = validateQuota({
      ...valid,
      itemId: "FG",
      validFrom: "2026-12-31",
      validTo: "2026-11-01",
      minSplitQty: "-1",
      sources: [
        { ...EMPTY_SOURCE, ref: VENDOR_A, quota: "0.12345" },
        { ...EMPTY_SOURCE, ref: VENDOR_A, quota: "1", maxQtyPerPeriod: "10", priority: "1000" },
      ],
    });
    expect(input).toBeNull();
    expect(Object.keys(errors).sort()).toEqual([
      "itemId",
      "minSplitQty",
      "sources.0.quota",
      "sources.1.maxQtyPerPeriod",
      "sources.1.priority",
      "sources.1.ref",
      "validTo",
    ]);
  });

  it("needs one source with a quota above 0", () => {
    const { errors } = validateQuota({ ...valid, sources: [{ ...EMPTY_SOURCE, ref: VENDOR_A, quota: "0" }] });
    expect(errors).toEqual({ sources: "At least one source needs a quota above 0." });
  });

  it("picks the next source by lowest rating, skipping blocked and quota-0 sources (BR-22.12)", () => {
    expect(nextSource(arrangement.sources)?.vendorId).toBe(VENDOR_B);
    const blockedB = arrangement.sources.map((s) => (s.vendorId === VENDOR_B ? { ...s, blocked: true } : s));
    expect(nextSource(blockedB)?.vendorId).toBe(VENDOR_A);
    expect(nextSource([arrangement.sources[2]])).toBeNull();
  });

  it("round-trips a stored arrangement through the draft", () => {
    const { input } = validateQuota(quotaDraftOf(arrangement));
    expect(input?.sources.map((s) => [s.type, s.quota])).toEqual([
      ["vendor", "60"],
      ["vendor", "40"],
      ["transfer", "0"],
    ]);
    expect(input?.sources[2]).toMatchObject({ fromWarehouseId: WH, maxQtyPerPeriod: "500.5", periodDays: 30 });
  });
});

describe("Quota arrangement list", () => {
  it("lists arrangements with sources and the next source, filters and opens one", async () => {
    const fetchMock = stubApi([`/quota-arrangements/${arrangement.id}`, () => json(arrangement)], ["/quota-arrangements", () => json({ items: [arrangement] })]);
    renderAt("/planning/quota-arrangements");
    expect(screen.getByRole("heading", { level: 2, name: "Quota arrangements" })).toBeInTheDocument();
    const table = await findTable({ name: "Quota arrangements" });
    await waitFor(() => expect(within(table).getByText("2026-11-01 to 2026-12-31")).toBeInTheDocument());
    expect(within(table).getByText("Vendor 60 · Vendor 40 · Transfer 0")).toBeInTheDocument();
    expect(within(table).getByText(`Vendor ${VENDOR_B}`)).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Item"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Valid on"), { target: { value: "2026-11-15" } });
    fireEvent.click(screen.getByRole("button", { name: "Show arrangements" }));
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => String(url).includes(`itemId=${ITEM}`) && String(url).includes("validOn=2026-11-15"))).toBe(true),
    );
    fireEvent.click(within(await findTable({ name: "Quota arrangements" })).getAllByRole("button", { name: ITEM })[0]);
    expect(screen.getByTestId("location")).toHaveTextContent(`/planning/quota-arrangements/${arrangement.id}`);
  });

  it("shows the empty state", async () => {
    stubApi(["/quota-arrangements", () => json({ items: [] })]);
    renderAt("/planning/quota-arrangements");
    expect(await screen.findByText("No quota arrangements yet")).toBeInTheDocument();
  });

  it("shows the service error instead of the table", async () => {
    stubApi(["/quota-arrangements", () => problem(403, "AUTH-0003", "missing duty planning.read")]);
    renderAt("/planning/quota-arrangements");
    expect(await screen.findByText("Could not load quota arrangements")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("refuses an item filter that is not a UUID", async () => {
    stubApi(["/quota-arrangements", () => json({ items: [] })]);
    renderAt("/planning/quota-arrangements");
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: "FG-100" } });
    fireEvent.click(screen.getByRole("button", { name: "Show arrangements" }));
    expect(await screen.findByText(/Enter the item id as a UUID/)).toBeInTheDocument();
  });
});

describe("Quota arrangement form", () => {
  it("creates an arrangement with two sources and an Idempotency-Key; an overlap is explained", async () => {
    let calls = 0;
    const fetchMock = stubApi(
      [`/quota-arrangements/${arrangement.id}`, () => json(arrangement)],
      [
        "/quota-arrangements",
        () => (++calls === 1 ? problem(409, "PLAN-QUOTA-OVERLAP", "the validity period overlaps another quota arrangement") : json(arrangement, 201)),
      ],
    );
    renderAt("/planning/quota-arrangements/new");
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
    fireEvent.change(screen.getByLabelText("Valid from"), { target: { value: "2026-11-01" } });
    fireEvent.change(screen.getByLabelText("Valid to"), { target: { value: "2026-12-31" } });
    const first = within(screen.getByRole("group", { name: "Source 1" }));
    fireEvent.change(first.getByLabelText("Vendor"), { target: { value: VENDOR_A } });
    fireEvent.change(first.getByLabelText("Quota"), { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: "Add source" }));
    const second = within(screen.getByRole("group", { name: "Source 2" }));
    fireEvent.change(second.getByLabelText("Source type"), { target: { value: "transfer" } });
    fireEvent.change(second.getByLabelText("From warehouse"), { target: { value: WH } });
    fireEvent.change(second.getByLabelText("Quota"), { target: { value: "40" } });

    fireEvent.click(screen.getByRole("button", { name: "Create quota arrangement" }));
    expect(await screen.findByText("Another arrangement of this item and site is valid in that period")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create quota arrangement" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent(`/planning/quota-arrangements/${arrangement.id}`));

    const posts = fetchMock.mock.calls.filter(([, init]) => init?.method === "POST");
    expect(posts).toHaveLength(2);
    const key = (i: number) => (posts[i][1]?.headers as Record<string, string>)["Idempotency-Key"];
    expect(key(1)).toBe(key(0));
    const body = JSON.parse(String(posts[0][1]?.body));
    expect(body).toMatchObject({ itemId: ITEM, siteId: SITE, validFrom: "2026-11-01", validTo: "2026-12-31", split: false });
    expect(body.sources).toHaveLength(2);
    expect(body.sources[1]).toMatchObject({ type: "transfer", fromWarehouseId: WH, quota: "40" });
    expect(body.sources[1].vendorId).toBeUndefined();
  });

  it("checks the draft before sending anything", async () => {
    const fetchMock = stubApi(["/quota-arrangements", () => json(arrangement, 201)]);
    renderAt("/planning/quota-arrangements/new");
    fireEvent.click(screen.getByRole("button", { name: "Create quota arrangement" }));
    expect(await screen.findByText("Enter the item id as a UUID.")).toBeInTheDocument();
    expect(screen.getByText("Enter the vendor id as a UUID.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove source 1" })).toBeDisabled();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("shows source ratings and the next source, and saves under If-Match with a reload offer on 412", async () => {
    let puts = 0;
    const fetchMock = stubApi([
      `/quota-arrangements/${arrangement.id}`,
      (_url, init) => {
        if (init?.method !== "PUT") return json(arrangement);
        return ++puts === 1 ? problem(412, "conflict_version", "quota arrangement is at version 2") : json({ ...arrangement, version: 2, split: true });
      },
    ]);
    renderAt(`/planning/quota-arrangements/${arrangement.id}`);
    const sources = await findTable({ name: "Quota sources" });
    expect(within(sources).getByText("2.5")).toBeInTheDocument();
    expect(within(sources).getByText("Fallback")).toBeInTheDocument();
    expect(screen.getByText(`Without split, the next requirement goes to vendor ${VENDOR_B} (lowest rating 2.5).`)).toBeInTheDocument();

    fireEvent.click(screen.getByLabelText("Split one requirement across sources"));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Someone else changed this quota arrangement")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload arrangement" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Saved. Version 2.")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT");
    expect((put?.[1]?.headers as Record<string, string>)["If-Match"]).toBe('"1"');
    expect(JSON.parse(String(put?.[1]?.body))).toMatchObject({ split: true });
  });
});
