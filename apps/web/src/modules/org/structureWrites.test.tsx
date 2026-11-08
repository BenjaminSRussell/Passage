import { findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement, type ComponentType } from "react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OrgLayout } from "./pages";
import { stubLocalStorage } from "./testStorage";
import { OperatingUnitsPage, SitesPage, WarehousesPage, validateOperatingUnit, validateSite, validateWarehouse } from "./StructurePages";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (code: string, status: number, detail = "failed") => json({ code, detail, title: detail, status }, status);

type Reply = (init?: RequestInit) => Response;

function serve(routes: Record<string, Reply>) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    const key = `${init?.method ?? "GET"} ${url.replace("/api/v1/org", "")}`;
    const match = Object.keys(routes)
      .sort((a, b) => b.length - a.length)
      .find((prefix) => key.startsWith(prefix));
    return match ? routes[match](init) : json({ items: [] });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const callsTo = (fetchMock: ReturnType<typeof serve>, method: string, path: string) =>
  fetchMock.mock.calls.filter(([url, init]) => (init?.method ?? "GET") === method && String(url).includes(path));

function renderPage(path: string, Page: ComponentType, duties: string[]) {
  const router = createMemoryRouter([{ element: createElement(OrgLayout, { duties }), children: [{ path, element: createElement(Page) }] }], {
    initialEntries: [path],
  });
  return { user: userEvent.setup(), ...render(<RouterProvider router={router} />) };
}

beforeEach(() => void stubLocalStorage());
afterEach(() => vi.unstubAllGlobals());

const entity = { id: "le-1", code: "USMF", name: "Contoso US", country: "US", currency: "USD", timeZone: "UTC", status: "active", version: 2 };
const entities = { "GET /legal-entities": () => json({ items: [entity] }) };
const unit = { id: "ou-1", type: "cost_center", code: "CC100", name: "Assembly", defaultLegalEntityId: "le-1", status: "active", version: 4 };
const site = { id: "s-1", legalEntityId: "le-1", code: "DET", name: "Detroit", timeZone: "America/Detroit", status: "active", version: 3 };
const warehouse = { id: "w-1", legalEntityId: "le-1", siteId: "s-1", code: "WH1", name: "Main", type: "standard", timeZone: "UTC", status: "active", version: 5 };

const OU_WRITER = ["org.read", "org.operating-unit.write"];
const SITE_WRITER = ["org.read", "org.site.write"];

describe("validators", () => {
  it("checks operating unit, site and warehouse fields", () => {
    expect(Object.keys(validateOperatingUnit({ type: "", code: "x", name: " ", defaultLegalEntityId: "" }))).toEqual(["type", "code", "name"]);
    expect(validateOperatingUnit({ type: "department", code: "d1", name: "Dept", defaultLegalEntityId: "" })).toEqual({});
    expect(Object.keys(validateSite({ legalEntityId: "", code: "TOOLONGCODE1", name: "", timeZone: "" }))).toEqual(["legalEntityId", "code", "name"]);
    expect(validateSite({ legalEntityId: "le-1", code: "D", name: "N", timeZone: "" })).toEqual({});
    expect(Object.keys(validateWarehouse({ siteId: "", code: "-1", name: "x".repeat(121), type: "standard", timeZone: "" }))).toEqual(["siteId", "code", "name"]);
  });
});

describe("operating units page", () => {
  it("is read-only without the write duty", async () => {
    serve({ ...entities, "GET /operating-units": () => json({ items: [unit] }) });
    renderPage("/org/operating-units", OperatingUnitsPage, ["org.read"]);
    const table = await findTable({ name: "Operating units" });
    expect(within(table).getByText("Assembly")).toBeInTheDocument();
    expect(within(table).getByText("cost center")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add operating unit" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Import from CSV" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Deactivate/ })).toBeNull();
  });

  it("links to the CSV import for a caller who can write", async () => {
    serve({ ...entities, "GET /operating-units": () => json({ items: [unit] }) });
    renderPage("/org/operating-units", OperatingUnitsPage, ["org.read", "org.operating-unit.write"]);
    expect(await screen.findByRole("link", { name: "Import from CSV" })).toHaveAttribute("href", "/org/operating-units/import");
  });

  it("shows the empty and error states", async () => {
    serve({ ...entities, "GET /operating-units": () => json({ items: [] }) });
    const first = renderPage("/org/operating-units", OperatingUnitsPage, ["org.read"]);
    expect(await screen.findByRole("heading", { name: "No operating units" })).toBeInTheDocument();
    first.unmount();
    serve({ ...entities, "GET /operating-units": () => problem("ORG-X-BOOM", 500, "it broke") });
    renderPage("/org/operating-units", OperatingUnitsPage, ["org.read"]);
    expect((await screen.findAllByText(/ORG-X-BOOM/)).length).toBeGreaterThan(0);
  });

  it("blocks an invalid form, then creates a unit with an upper-cased code and no scope header", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /operating-units": () => json({ items: [unit] }),
      "POST /operating-units": () => json({ ...unit, id: "ou-2", code: "DEPT1", type: "department" }, 201),
    });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, OU_WRITER);
    await findTable({ name: "Operating units" });
    await user.click(screen.getByRole("button", { name: "Add operating unit" }));
    expect(await screen.findByText("Select a type.")).toBeInTheDocument();
    expect(callsTo(fetchMock, "POST", "/operating-units")).toHaveLength(0);

    await user.selectOptions(screen.getByLabelText("Type"), "department");
    await user.type(screen.getByLabelText("Code"), "dept1");
    await user.type(screen.getByLabelText("Name"), "Paint shop");
    await user.selectOptions(screen.getByLabelText("Default legal entity"), "le-1");
    await user.click(screen.getByRole("button", { name: "Add operating unit" }));
    await waitFor(() => expect(callsTo(fetchMock, "POST", "/operating-units")).toHaveLength(1));
    const [, init] = callsTo(fetchMock, "POST", "/operating-units")[0];
    expect(JSON.parse(String(init?.body))).toEqual({ type: "department", code: "DEPT1", name: "Paint shop", defaultLegalEntityId: "le-1" });
    const headers = new Headers(init?.headers);
    expect(headers.get("Idempotency-Key")).toBeTruthy();
    expect(headers.has("X-Passage-LegalEntity")).toBe(false);
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/operating-units").length).toBeGreaterThan(1));
  });

  it("shows a duplicate code on the code field", async () => {
    serve({ ...entities, "GET /operating-units": () => json({ items: [] }), "POST /operating-units": () => problem("ORG-OU-CODE-DUPLICATE", 409) });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, OU_WRITER);
    await screen.findByRole("heading", { name: "No operating units" });
    await user.selectOptions(screen.getByLabelText("Type"), "department");
    await user.type(screen.getByLabelText("Code"), "CC100");
    await user.type(screen.getByLabelText("Name"), "Dup");
    await user.click(screen.getByRole("button", { name: "Add operating unit" }));
    expect(await screen.findByText("An operating unit of this type already has this code.")).toBeInTheDocument();
  });

  it("shows other create failures with their code", async () => {
    serve({ ...entities, "GET /operating-units": () => json({ items: [] }), "POST /operating-units": () => problem("ORG-OU-LEGAL-ENTITY-INVALID", 422, "entity inactive") });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, OU_WRITER);
    await screen.findByRole("heading", { name: "No operating units" });
    await user.selectOptions(screen.getByLabelText("Type"), "department");
    await user.type(screen.getByLabelText("Code"), "D2");
    await user.type(screen.getByLabelText("Name"), "Other");
    await user.click(screen.getByRole("button", { name: "Add operating unit" }));
    expect(await screen.findByText(/entity inactive \(ORG-OU-LEGAL-ENTITY-INVALID\)/)).toBeInTheDocument();
  });

  it("deactivates after confirmation and sends the row version", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /operating-units": () => json({ items: [unit] }),
      "PATCH /operating-units/ou-1": () => json({ ...unit, status: "inactive", version: 5 }),
    });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, OU_WRITER);
    await findTable({ name: "Operating units" });
    await user.click(await screen.findByRole("button", { name: "Deactivate operating unit CC100" }));
    expect(callsTo(fetchMock, "PATCH", "/operating-units/ou-1")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "Confirm deactivate operating unit CC100" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/operating-units/ou-1")).toHaveLength(1));
    expect(JSON.parse(String(callsTo(fetchMock, "PATCH", "/operating-units/ou-1")[0][1]?.body))).toEqual({ version: 4, status: "inactive" });
    expect(await screen.findByText("Inactive")).toBeInTheDocument();
  });

  it("reloads the row on a version conflict so a retry sends the current version", async () => {
    let patches = 0;
    const fetchMock = serve({
      ...entities,
      "GET /operating-units/ou-1": () => json({ ...unit, version: 9 }),
      "GET /operating-units": () => json({ items: [unit] }),
      "PATCH /operating-units/ou-1": () => (++patches === 1 ? problem("ORG-VERSION-CONFLICT", 409, "stale") : json({ ...unit, status: "inactive", version: 10 })),
    });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, OU_WRITER);
    const table = await findTable({ name: "Operating units" });
    await user.click(await screen.findByRole("button", { name: "Deactivate operating unit CC100" }));
    await user.click(screen.getByRole("button", { name: "Confirm deactivate operating unit CC100" }));
    expect(await screen.findByText(/ORG-VERSION-CONFLICT/)).toBeInTheDocument();
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/operating-units/ou-1")).toHaveLength(1));
    await user.click(await screen.findByRole("button", { name: "Confirm deactivate operating unit CC100" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/operating-units/ou-1")).toHaveLength(2));
    expect(JSON.parse(String(callsTo(fetchMock, "PATCH", "/operating-units/ou-1")[1][1]?.body))).toEqual({ version: 9, status: "inactive" });
    expect(table).toBeInTheDocument();
  });
});

describe("sites page", () => {
  it("is read-only with only org.read, and operating-unit duty does not unlock it", async () => {
    serve({ ...entities, "GET /sites": () => json({ items: [site] }) });
    renderPage("/org/sites", SitesPage, OU_WRITER);
    const table = await findTable({ name: "Sites" });
    expect(within(table).getByText("Detroit")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add site" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Deactivate/ })).toBeNull();
  });

  it("creates a site in the chosen legal entity, scoped to that entity", async () => {
    const fetchMock = serve({ ...entities, "GET /sites": () => json({ items: [] }), "POST /sites": () => json(site, 201) });
    const { user } = renderPage("/org/sites", SitesPage, SITE_WRITER);
    await screen.findByRole("heading", { name: "No sites" });
    await user.click(screen.getByRole("button", { name: "Add site" }));
    expect(await screen.findByText("Select a legal entity.")).toBeInTheDocument();
    await within(await screen.findByLabelText("Site legal entity")).findByRole("option", { name: "USMF - Contoso US" });
    await user.selectOptions(screen.getByLabelText("Site legal entity"), "le-1");
    await user.type(screen.getByLabelText("Code"), "det");
    await user.type(screen.getByLabelText("Name"), " Detroit ");
    await user.click(screen.getByRole("button", { name: "Add site" }));
    await waitFor(() => expect(callsTo(fetchMock, "POST", "/sites")).toHaveLength(1));
    const [, init] = callsTo(fetchMock, "POST", "/sites")[0];
    expect(JSON.parse(String(init?.body))).toEqual({ legalEntityId: "le-1", code: "DET", name: "Detroit" });
    // The site table is behind the legal-entity RLS policy: without the header the service sees no scope and refuses.
    const headers = new Headers(init?.headers);
    expect(headers.get("X-Passage-LegalEntity")).toBe("le-1");
    expect(headers.get("Idempotency-Key")).toBeTruthy();
  });

  it("deactivates a site under the site's own legal entity and re-reads it there on a version conflict", async () => {
    const other = { ...site, id: "s-9", legalEntityId: "le-2", code: "TOL", name: "Toledo" };
    const fetchMock = serve({
      "GET /legal-entities": () => json({ items: [entity, { ...entity, id: "le-2", code: "USOH", name: "Contoso Ohio" }] }),
      "GET /sites/s-9": () => json({ ...other, version: 7 }),
      "GET /sites": () => json({ items: [other] }),
      "PATCH /sites/s-9": () => problem("ORG-VERSION-CONFLICT", 409, "stale"),
    });
    const { user } = renderPage("/org/sites", SitesPage, SITE_WRITER);
    await findTable({ name: "Sites" });
    await user.click(await screen.findByRole("button", { name: "Deactivate site TOL" }));
    await user.click(screen.getByRole("button", { name: "Confirm deactivate site TOL" }));
    expect(await screen.findByText(/ORG-VERSION-CONFLICT/)).toBeInTheDocument();
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/sites/s-9")).toHaveLength(1));
    const [, patch] = callsTo(fetchMock, "PATCH", "/sites/s-9")[0];
    expect(new Headers(patch?.headers).get("X-Passage-LegalEntity")).toBe("le-2");
    expect(new Headers(patch?.headers).has("Idempotency-Key")).toBe(false);
    expect(new Headers(callsTo(fetchMock, "GET", "/sites/s-9")[0][1]?.headers).get("X-Passage-LegalEntity")).toBe("le-2");
  });

  it("shows a duplicate code and deactivates a site, explaining ORG-SITE-IN-USE", async () => {
    serve({
      ...entities,
      "GET /sites": () => json({ items: [site] }),
      "POST /sites": () => problem("ORG-SITE-CODE-DUPLICATE", 409),
      "PATCH /sites/s-1": () => problem("ORG-SITE-IN-USE", 409, "an active warehouse references the site"),
    });
    const { user } = renderPage("/org/sites", SitesPage, SITE_WRITER);
    await findTable({ name: "Sites" });
    await within(await screen.findByLabelText("Site legal entity")).findByRole("option", { name: "USMF - Contoso US" });
    await user.selectOptions(screen.getByLabelText("Site legal entity"), "le-1");
    await user.type(screen.getByLabelText("Code"), "DET");
    await user.type(screen.getByLabelText("Name"), "Again");
    await user.click(screen.getByRole("button", { name: "Add site" }));
    expect(await screen.findByText("A site with this code already exists in the legal entity.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Deactivate site DET" }));
    await user.click(screen.getByRole("button", { name: "Confirm deactivate site DET" }));
    expect(await screen.findByText(/ORG-SITE-IN-USE/)).toBeInTheDocument();
  });

  it("shows the load error", async () => {
    serve({ ...entities, "GET /sites": () => problem("ORG-X-BOOM", 500, "it broke") });
    renderPage("/org/sites", SitesPage, ["org.read"]);
    expect((await screen.findAllByText(/ORG-X-BOOM/)).length).toBeGreaterThan(0);
  });
});

describe("warehouses page", () => {
  const lookups = { ...entities, "GET /sites": () => json({ items: [site, { ...site, id: "s-2", code: "OLD", name: "Closed", status: "inactive" }] }) };

  it("lists warehouses with the site code and no write controls without the duty", async () => {
    serve({ ...lookups, "GET /warehouses": () => json({ items: [warehouse] }) });
    renderPage("/org/warehouses", WarehousesPage, ["org.read"]);
    const table = await findTable({ name: "Warehouses" });
    await waitFor(() => expect(within(table).getByText("DET")).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Add warehouse" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Deactivate/ })).toBeNull();
  });

  it("offers only active sites and creates a warehouse of the chosen type", async () => {
    const fetchMock = serve({ ...lookups, "GET /warehouses": () => json({ items: [] }), "POST /warehouses": () => json(warehouse, 201) });
    const { user } = renderPage("/org/warehouses", WarehousesPage, SITE_WRITER);
    await screen.findByRole("heading", { name: "No warehouses" });
    await screen.findByRole("option", { name: "DET - Detroit" });
    expect(screen.queryByRole("option", { name: "OLD - Closed" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Add warehouse" }));
    expect(await screen.findByText("Select a site.")).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Site"), "s-1");
    await user.type(screen.getByLabelText("Code"), "q1");
    await user.type(screen.getByLabelText("Name"), "Quarantine");
    await user.selectOptions(screen.getByLabelText("Type"), "quarantine");
    await user.click(screen.getByRole("button", { name: "Add warehouse" }));
    await waitFor(() => expect(callsTo(fetchMock, "POST", "/warehouses")).toHaveLength(1));
    const [, init] = callsTo(fetchMock, "POST", "/warehouses")[0];
    expect(JSON.parse(String(init?.body))).toEqual({
      siteId: "s-1",
      code: "Q1",
      name: "Quarantine",
      type: "quarantine",
    });
    expect(new Headers(init?.headers).get("X-Passage-LegalEntity")).toBe("le-1");
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeTruthy();
    // The site choices come from the selected legal entity, not an ALL read the caller may not be allowed.
    const siteReads = callsTo(fetchMock, "GET", "/sites");
    expect(siteReads.every(([, read]) => new Headers(read?.headers).get("X-Passage-LegalEntity") === "le-1")).toBe(true);
  });

  it("shows a duplicate code and refuses to deactivate a default warehouse with its code", async () => {
    const fetchMock = serve({
      ...lookups,
      "GET /warehouses": () => json({ items: [warehouse] }),
      "POST /warehouses": () => problem("ORG-WH-CODE-DUPLICATE", 409),
      "PATCH /warehouses/w-1": () => problem("ORG-WH-IS-DEFAULT", 422, "default warehouse of its site"),
    });
    const { user } = renderPage("/org/warehouses", WarehousesPage, SITE_WRITER);
    await findTable({ name: "Warehouses" });
    await screen.findByRole("option", { name: "DET - Detroit" });
    await user.selectOptions(screen.getByLabelText("Site"), "s-1");
    await user.type(screen.getByLabelText("Code"), "WH1");
    await user.type(screen.getByLabelText("Name"), "Dup");
    await user.click(screen.getByRole("button", { name: "Add warehouse" }));
    expect(await screen.findByText("A warehouse with this code already exists in the legal entity.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Deactivate warehouse WH1" }));
    await user.click(screen.getByRole("button", { name: "Confirm deactivate warehouse WH1" }));
    expect(await screen.findByText(/ORG-WH-IS-DEFAULT/)).toBeInTheDocument();
    expect(new Headers(callsTo(fetchMock, "PATCH", "/warehouses/w-1")[0][1]?.headers).get("X-Passage-LegalEntity")).toBe("le-1");
  });

  it("reports a failed site lookup beside the form", async () => {
    serve({ ...entities, "GET /sites": () => problem("ORG-X-BOOM", 500, "sites down"), "GET /warehouses": () => json({ items: [] }) });
    renderPage("/org/warehouses", WarehousesPage, SITE_WRITER);
    expect(await screen.findByText(/sites down \(ORG-X-BOOM\)/)).toBeInTheDocument();
  });
});
