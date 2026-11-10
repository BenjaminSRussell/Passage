import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findAlert, findTable } from "../../test-utils";
import type { CatalogDetail, CatalogEntry } from "./api/hubClient";
import { earliestSunset } from "./CatalogEntryPage";
import integration from "./module";
import { EMPTY_CATALOG, validateCatalogEntry } from "./NewCatalogEntryPage";

const ENTRY_UUID = "0195f0a0-0000-7000-8000-0000000000c1";
const OTHER_UUID = "0195f0a0-0000-7000-8000-0000000000c2";
const READ = ["hub.delivery.read"];
const WRITE = ["hub.delivery.read", "hub.catalog.write"];

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: string[] = WRITE) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {integration.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
          <Route path="*" element={<p>elsewhere</p>} />
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

const entry: CatalogEntry = {
  id: ENTRY_UUID,
  kind: "api",
  name: "sales-api",
  specVersion: "1.2.0",
  title: "Sales API",
  summary: "Orders and quotes.",
  ownerService: "sales",
  specFormat: "openapi",
  specBytes: 1200,
  operationCount: 2,
  lifecycle: "active",
  deprecatedAt: null,
  sunsetAt: null,
  replacedBy: null,
  retiredAt: null,
  version: 1,
  createdAt: "2026-11-03T08:00:00Z",
  updatedAt: "2026-11-03T08:00:00Z",
};
const detail: CatalogDetail = {
  ...entry,
  operations: [
    { id: "listOrders", method: "GET", path: "/v1/orders", summary: "List orders", duty: "sales.order.read" },
    { id: null, method: "POST", path: "/v1/orders", summary: null, duty: null },
  ],
  issues: ["POST /v1/orders: no operationId", "POST /v1/orders: no summary"],
};
const events: CatalogEntry = { ...entry, id: OTHER_UUID, kind: "event", name: "sales-events", title: "Sales events", specFormat: "asyncapi", operationCount: 3 };

describe("catalog module wiring", () => {
  it("routes the catalog and gates its nav entry on the seeded read duty, without a route duty that is not seeded", () => {
    const paths = integration.routes.map((r) => r.path);
    expect(integration.nav.find((n) => n.to === "/integration/catalog")?.duty).toBe("hub.delivery.read");
    expect(paths).toEqual(expect.arrayContaining(["/integration/catalog", "/integration/catalog/new", "/integration/catalog/:id"]));
    for (const path of ["/integration/catalog", "/integration/catalog/new", "/integration/catalog/:id"]) {
      expect((integration.routes.find((r) => r.path === path)?.handle as { duty?: string }).duty).toBeUndefined();
    }
  });
});

describe("catalog list", () => {
  it("lists contracts with owner, lifecycle and sunset, and offers Publish only with the write duty", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/hub/catalog"
        ? json(200, { items: [entry, events, { ...entry, id: "x3", specVersion: "1.0.0", lifecycle: "deprecated", sunsetAt: "2027-12-01T00:00:00Z" }], nextCursor: null })
        : undefined;
    const { container, unmount } = renderAt("/integration/catalog");
    const table = await findTable({ name: "API catalog" });
    expect(within(table).getAllByText("sales-api 1.2.0")).toHaveLength(1);
    expect(within(table).getByText("3 events")).toBeInTheDocument();
    expect(within(table).getByText("Deprecated")).toBeInTheDocument();
    expect(within(table).getByText("2027-12-01 00:00 UTC")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Publish contract" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    unmount();
    renderAt("/integration/catalog", READ);
    await findTable({ name: "API catalog" });
    expect(screen.queryByRole("link", { name: "Publish contract" })).toBeNull();
  });

  it("filters by kind, lifecycle and search text through the hub", async () => {
    handler = () => json(200, { items: [entry], nextCursor: null });
    renderAt("/integration/catalog");
    await findTable({ name: "API catalog" });
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "event" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("kind")).toBe("event"));
    fireEvent.change(screen.getByLabelText("Lifecycle"), { target: { value: "deprecated" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("lifecycle")).toBe("deprecated"));
    fill("Search", " orders ");
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("q")).toBe("orders"));
    expect(calls.at(-1)?.url.searchParams.get("kind")).toBe("event");
  });

  it("loads the next page", async () => {
    handler = (url) =>
      url.searchParams.get("cursor") === "c2"
        ? json(200, { items: [events], nextCursor: null })
        : json(200, { items: [entry], nextCursor: "c2" });
    renderAt("/integration/catalog");
    await findTable({ name: "API catalog" });
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("sales-events 1.2.0")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("shows the empty states and the error state with a retry", async () => {
    handler = () => json(200, { items: [], nextCursor: null });
    const first = renderAt("/integration/catalog");
    expect(await screen.findByText("The catalog is empty")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "api" } });
    expect(await screen.findByText("No contracts match")).toBeInTheDocument();
    first.unmount();
    let fail = true;
    handler = () => (fail ? json(500, { code: "HUB-0500", detail: "unexpected error" }) : json(200, { items: [entry], nextCursor: null }));
    renderAt("/integration/catalog");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load the catalog");
    expect(alert).toHaveTextContent("unexpected error");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Sales API")).toBeInTheDocument();
  });
});

describe("publish a contract", () => {
  const spec = { openapi: "3.1.0", info: { title: "Sales", version: "1.2.0" }, paths: { "/v1/orders": { get: {} } } };
  const good = { ...EMPTY_CATALOG, name: "sales-api", specVersion: "1.2.0", title: "Sales API", ownerService: "sales", spec: JSON.stringify(spec) };

  it("validates like the hub does", () => {
    expect(validateCatalogEntry(good).input).toEqual({ kind: "api", name: "sales-api", specVersion: "1.2.0", title: "Sales API", ownerService: "sales", spec });
    expect(validateCatalogEntry({ ...good, summary: " Orders " }).input?.summary).toBe("Orders");
    const bad = validateCatalogEntry(EMPTY_CATALOG);
    expect(Object.keys(bad.errors).sort()).toEqual(["name", "ownerService", "spec", "specVersion", "title"]);
    expect(validateCatalogEntry({ ...good, specVersion: "1.2" }).errors.specVersion).toBeDefined();
    expect(validateCatalogEntry({ ...good, specVersion: "1.2.0-rc1" }).errors.specVersion).toBeDefined();
    expect(validateCatalogEntry({ ...good, specVersion: "1.3.0" }).errors.specVersion).toContain("info.version (1.2.0)");
    expect(validateCatalogEntry({ ...good, name: "Sales API" }).errors.name).toBeDefined();
    expect(validateCatalogEntry({ ...good, ownerService: "Sales!" }).errors.ownerService).toBeDefined();
    expect(validateCatalogEntry({ ...good, spec: "openapi: 3.1.0" }).errors.spec).toContain("not valid JSON");
    expect(validateCatalogEntry({ ...good, spec: "[]" }).errors.spec).toContain("JSON object");
    expect(validateCatalogEntry({ ...good, spec: "x".repeat(2 * 1024 * 1024 + 1) }).errors.spec).toContain("2 MiB");
  });

  it("publishes with one Idempotency-Key per attempt and opens the entry", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/hub/catalog" && init.method === "POST") return json(201, entry);
      if (url.pathname === `/api/v1/hub/catalog/${ENTRY_UUID}`) return json(200, detail);
      return undefined;
    };
    const { container } = renderAt("/integration/catalog/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Contract name", "sales-api");
    fill("Version", "1.2.0");
    fill("Title", "Sales API");
    fill("Owner service", "sales");
    fill("Document (JSON)", JSON.stringify(spec));
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    expect(await screen.findByRole("heading", { name: "Sales API" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST") as (typeof calls)[number];
    expect(header(post, "idempotency-key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(JSON.parse(post.init.body as string)).toEqual({ kind: "api", name: "sales-api", specVersion: "1.2.0", title: "Sales API", ownerService: "sales", spec });
  });

  it("shows field errors without calling the hub, maps a stale version to the field and other failures to an alert", async () => {
    renderAt("/integration/catalog/new");
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    expect(await screen.findByText(/Use 2 to 100 characters/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    fill("Contract name", "sales-api");
    fill("Version", "1.0.0");
    fill("Title", "Sales API");
    fill("Owner service", "sales");
    fill("Document (JSON)", JSON.stringify(spec));
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    expect(await screen.findByText(/Use the document's info.version \(1.2.0\)/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    fill("Version", "1.2.0");
    handler = () => json(409, { code: "HUB-0409", detail: "1.2.0 is already published; published versions never change" });
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    expect(await screen.findByText(/published versions never change/)).toBeInTheDocument();

    handler = () => json(400, { code: "HUB-0400", detail: "spec.paths must list at least one path" });
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    expect(await findAlert()).toHaveTextContent("spec.paths must list at least one path");
  });

  it("tells a user without the write duty why they cannot publish", () => {
    renderAt("/integration/catalog/new", READ);
    expect(screen.getByText("You cannot publish to the catalog")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Publish" })).toBeNull();
  });
});

describe("catalog entry detail", () => {
  const open = (d: CatalogDetail = detail) => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/hub/catalog/${ENTRY_UUID}/spec`) return json(200, { openapi: "3.1.0" });
      if (url.pathname === `/api/v1/hub/catalog/${ENTRY_UUID}:deprecate` && init.method === "POST") {
        const body = JSON.parse(init.body as string);
        d = { ...d, lifecycle: "deprecated", sunsetAt: body.sunsetAt, deprecatedAt: "2026-11-03T09:00:00Z", replacedBy: body.replacedBy ?? null, version: d.version + 1 };
        return json(200, d);
      }
      if (url.pathname === `/api/v1/hub/catalog/${ENTRY_UUID}:retire` && init.method === "POST") {
        d = { ...d, lifecycle: "retired", retiredAt: "2026-11-03T09:00:00Z", version: d.version + 1 };
        return json(200, d);
      }
      if (url.pathname === `/api/v1/hub/catalog/${ENTRY_UUID}`) return json(200, d);
      return undefined;
    };
  };

  it("shows the contract, its operations and the documentation gaps", async () => {
    open();
    const { container } = renderAt(`/integration/catalog/${ENTRY_UUID}`);
    expect(await screen.findByRole("heading", { name: "Sales API" })).toBeInTheDocument();
    const table = await findTable({ name: "Operations" });
    expect(within(table).getByText("listOrders")).toBeInTheDocument();
    expect(within(table).getByText("sales.order.read")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Documentation gaps" })).toHaveTextContent("POST /v1/orders: no operationId");
    expect(screen.getByText("2 documentation gaps")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("loads the published document on demand and reports a failure", async () => {
    open();
    renderAt(`/integration/catalog/${ENTRY_UUID}`);
    await screen.findByRole("heading", { name: "Sales API" });
    fireEvent.click(screen.getByRole("button", { name: "Show document" }));
    expect(await screen.findByLabelText("Published document")).toHaveTextContent('"openapi": "3.1.0"');
  });

  it("deprecates with a sunset date at least 12 months out and sends the entry version", async () => {
    open();
    renderAt(`/integration/catalog/${ENTRY_UUID}`);
    await screen.findByRole("heading", { name: "Sales API" });
    const earliest = earliestSunset(new Date());
    fill("Sunset date", "2026-12-01");
    fireEvent.click(screen.getByRole("button", { name: "Deprecate" }));
    expect(await screen.findByText(new RegExp(`on or after ${earliest}`))).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fill("Replaced by", "nope");
    fill("Sunset date", earliest);
    fireEvent.click(screen.getByRole("button", { name: "Deprecate" }));
    expect(await screen.findByText(/Enter the id of the replacing/)).toBeInTheDocument();
    fill("Replaced by", OTHER_UUID);
    fireEvent.click(screen.getByRole("button", { name: "Deprecate" }));
    expect(await screen.findByText("Deprecated", { selector: "h2, h3, [role=status] *" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST") as (typeof calls)[number];
    expect(JSON.parse(post.init.body as string)).toEqual({ version: 1, sunsetAt: `${earliest}T00:00:00Z`, replacedBy: OTHER_UUID });
  });

  it("reports a stale entry on deprecate", async () => {
    open();
    renderAt(`/integration/catalog/${ENTRY_UUID}`);
    await screen.findByRole("heading", { name: "Sales API" });
    const base = handler;
    handler = (url, init) => (init.method === "POST" ? json(409, { code: "HUB-0409", detail: "stale" }) : base(url, init));
    fill("Sunset date", earliestSunset(new Date()));
    fireEvent.click(screen.getByRole("button", { name: "Deprecate" }));
    expect(await findAlert()).toHaveTextContent("The entry changed since you opened it");
  });

  it("offers Retire only after the sunset date and asks for confirmation", async () => {
    open({ ...detail, lifecycle: "deprecated", deprecatedAt: "2019-01-01T00:00:00Z", sunsetAt: "2020-01-01T00:00:00Z", issues: [], version: 2 });
    renderAt(`/integration/catalog/${ENTRY_UUID}`);
    await screen.findByRole("heading", { name: "Sales API" });
    expect(screen.queryByRole("button", { name: "Deprecate" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Retire entry" }));
    fireEvent.click(await screen.findByRole("button", { name: "Keep entry" }));
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Retire entry" }));
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Retire entry" }));
    expect(await screen.findByText(/Retired on/)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST") as (typeof calls)[number];
    expect(JSON.parse(post.init.body as string)).toEqual({ version: 2 });
  });

  it("does not offer Retire before the sunset date or without the write duty", async () => {
    open({ ...detail, lifecycle: "deprecated", deprecatedAt: "2026-11-03T00:00:00Z", sunsetAt: "2099-01-01T00:00:00Z", issues: [] });
    const first = renderAt(`/integration/catalog/${ENTRY_UUID}`);
    await screen.findByRole("heading", { name: "Sales API" });
    expect(screen.queryByRole("button", { name: "Retire entry" })).toBeNull();
    first.unmount();
    open({ ...detail, lifecycle: "deprecated", deprecatedAt: "2019-01-01T00:00:00Z", sunsetAt: "2020-01-01T00:00:00Z", issues: [] });
    renderAt(`/integration/catalog/${ENTRY_UUID}`, READ);
    await screen.findByRole("heading", { name: "Sales API" });
    expect(screen.queryByRole("button", { name: "Retire entry" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Deprecate" })).toBeNull();
  });

  it("shows the error state with a retry", async () => {
    let fail = true;
    handler = (url) => (fail ? json(404, { code: "HUB-0404", detail: "no such catalog entry" }) : url.pathname.endsWith(ENTRY_UUID) ? json(200, detail) : undefined);
    renderAt(`/integration/catalog/${ENTRY_UUID}`);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("no such catalog entry");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { name: "Sales API" })).toBeInTheDocument();
  });

  it("computes the earliest sunset like the hub", () => {
    // The form sends the day as midnight UTC, so after midnight the same calendar day is too early.
    expect(earliestSunset(new Date("2026-11-03T00:00:00Z"))).toBe("2027-11-03");
    expect(earliestSunset(new Date("2026-11-03T10:00:00Z"))).toBe("2027-11-04");
    expect(earliestSunset(new Date("2024-02-29T00:00:00Z"))).toBe("2025-02-28");
    expect(earliestSunset(new Date("2024-02-29T10:00:00Z"))).toBe("2025-03-01");
    expect(earliestSunset(new Date("2026-01-31T10:00:00Z"), 1)).toBe("2026-03-01");
  });
});
