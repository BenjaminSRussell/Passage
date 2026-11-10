import { findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement, type ComponentType } from "react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OrgLayout } from "./pages";
import { stubLocalStorage } from "./testStorage";
import { LocationsPage, OperatingUnitsPage, SitesPage, WarehousesPage, validateEditName, validateEditZoned } from "./StructurePages";

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
const site = { id: "s-1", legalEntityId: "le-1", code: "DET", name: "Detroit", timeZone: "America/Detroit", status: "active", defaultWarehouseId: "w-1", version: 3 };
const warehouse = { id: "w-1", legalEntityId: "le-1", siteId: "s-1", code: "WH1", name: "Main", type: "standard", timeZone: "UTC", status: "active", version: 5 };
const location = { id: "l-1", legalEntityId: "le-1", warehouseId: "w-1", code: "A1", name: "Aisle 1", type: "storage", status: "active", version: 2 };

const OU_WRITER = ["org.read", "org.operating-unit.write"];
const SITE_WRITER = ["org.read", "org.site.write"];

const bodyOf = (call: [unknown, RequestInit | undefined]) => JSON.parse(String(call[1]?.body));

describe("edit forms", () => {
  it("requires a name of 1 to 120 characters", () => {
    expect(validateEditName({ name: " " })).toEqual({ name: "Enter a name of 1 to 120 characters." });
    expect(validateEditName({ name: "x".repeat(121) })).toHaveProperty("name");
    expect(validateEditName({ name: "Paint" })).toEqual({});
  });

  it("requires a real IANA time zone where the row has one, since the service cannot clear it", () => {
    const zone = "Enter an IANA time zone such as America/Detroit.";
    expect(validateEditZoned({ name: "Paint", timeZone: " " })).toEqual({ timeZone: zone });
    expect(validateEditZoned({ name: "Paint", timeZone: "Mars/Olympus" })).toEqual({ timeZone: zone });
    expect(validateEditZoned({ name: "Paint", timeZone: "Europe/Berlin" })).toEqual({});
  });

  it("shows no Edit button without the write duty", async () => {
    serve({ ...entities, "GET /operating-units": () => json({ items: [unit] }) });
    renderPage("/org/operating-units", OperatingUnitsPage, ["org.read"]);
    await findTable({ name: "Operating units" });
    expect(screen.queryByRole("button", { name: /^Edit/ })).toBeNull();
  });

  it("edits an operating unit with the row version and refreshes the row", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /operating-units": () => json({ items: [unit] }),
      "PATCH /operating-units/ou-1": () => json({ ...unit, name: "Final assembly", defaultLegalEntityId: undefined, version: 5 }),
    });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, OU_WRITER);
    const table = await findTable({ name: "Operating units" });
    await user.click(within(table).getByRole("button", { name: "Edit operating unit CC100" }));
    const form = await screen.findByRole("form", { name: "Edit operating unit CC100" });
    const name = within(form).getByLabelText("Name");
    expect(name).toHaveValue("Assembly");
    await user.clear(name);
    await user.type(name, "Final assembly");
    await user.selectOptions(within(form).getByLabelText(/Default legal entity/), "");
    await user.click(within(form).getByRole("button", { name: "Save operating unit CC100" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/operating-units/ou-1")).toHaveLength(1));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/operating-units/ou-1")[0] as never)).toEqual({ version: 4, name: "Final assembly", defaultLegalEntityId: "" });
    expect(await within(table).findByText("Final assembly")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Edit operating unit CC100" })).toBeNull();
    // The response leaves the cleared default out; the row must not keep showing the old one.
    expect(within(table).queryByText("USMF")).toBeNull();
  });

  it("blocks an empty name and cancels without a request", async () => {
    const fetchMock = serve({ ...entities, "GET /operating-units": () => json({ items: [unit] }) });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, OU_WRITER);
    const table = await findTable({ name: "Operating units" });
    await user.click(within(table).getByRole("button", { name: "Edit operating unit CC100" }));
    const form = await screen.findByRole("form", { name: "Edit operating unit CC100" });
    await user.clear(within(form).getByLabelText("Name"));
    await user.click(within(form).getByRole("button", { name: "Save operating unit CC100" }));
    expect(await within(form).findByText("Enter a name of 1 to 120 characters.")).toBeInTheDocument();
    await user.click(within(form).getByRole("button", { name: "Cancel editing operating unit CC100" }));
    expect(screen.queryByRole("form", { name: "Edit operating unit CC100" })).toBeNull();
    expect(callsTo(fetchMock, "PATCH", "/operating-units")).toHaveLength(0);
  });

  it("on a version conflict keeps the user's edit, takes the other change for untouched fields and retries with the new version", async () => {
    let patches = 0;
    const fetchMock = serve({
      ...entities,
      "GET /operating-units/ou-1": () => json({ ...unit, name: "Renamed elsewhere", defaultLegalEntityId: undefined, version: 6 }),
      "GET /operating-units": () => json({ items: [unit] }),
      "PATCH /operating-units/ou-1": () =>
        ++patches === 1 ? problem("ORG-VERSION-CONFLICT", 409, "current version is 6") : json({ ...unit, name: "Final assembly", defaultLegalEntityId: undefined, version: 7 }),
    });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, OU_WRITER);
    const table = await findTable({ name: "Operating units" });
    await user.click(within(table).getByRole("button", { name: "Edit operating unit CC100" }));
    const form = await screen.findByRole("form", { name: "Edit operating unit CC100" });
    await user.clear(within(form).getByLabelText("Name"));
    await user.type(within(form).getByLabelText("Name"), "Final assembly");
    await user.click(within(form).getByRole("button", { name: "Save operating unit CC100" }));
    expect(await within(form).findByText(/your changes are kept.*current version is 6/)).toBeInTheDocument();
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/operating-units/ou-1")).toHaveLength(1));
    expect(await within(table).findByText("Renamed elsewhere")).toBeInTheDocument();
    // The typed name survives the re-read; the default legal entity, which the user did not touch, now shows the other change.
    expect(within(form).getByLabelText("Name")).toHaveValue("Final assembly");
    await waitFor(() => expect(within(form).getByLabelText(/Default legal entity/)).toHaveValue(""));
    await user.click(within(form).getByRole("button", { name: "Save operating unit CC100" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/operating-units/ou-1")).toHaveLength(2));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/operating-units/ou-1")[1] as never)).toEqual({ version: 6, name: "Final assembly" });
    expect(await within(table).findByText("Final assembly")).toBeInTheDocument();
  });

  it("shows an invalid default legal entity on its field", async () => {
    serve({
      ...entities,
      "GET /operating-units": () => json({ items: [{ ...unit, defaultLegalEntityId: undefined }] }),
      "PATCH /operating-units/ou-1": () => problem("ORG-OU-LEGAL-ENTITY-INVALID", 422, "default legal entity is unknown or inactive"),
    });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, OU_WRITER);
    const table = await findTable({ name: "Operating units" });
    await user.click(within(table).getByRole("button", { name: "Edit operating unit CC100" }));
    const form = await screen.findByRole("form", { name: "Edit operating unit CC100" });
    const choice = within(form).getByLabelText(/Default legal entity/);
    await user.selectOptions(choice, "le-1");
    await user.click(within(form).getByRole("button", { name: "Save operating unit CC100" }));
    expect(await within(form).findByText("Choose an active legal entity you can access.")).toBeInTheDocument();
    expect(choice).toHaveAttribute("aria-invalid", "true");
  });

  it("keeps an inactive default legal entity selectable so the form shows what the row holds", async () => {
    serve({ ...entities, "GET /operating-units": () => json({ items: [{ ...unit, defaultLegalEntityId: "le-old" }] }) });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, OU_WRITER);
    const table = await findTable({ name: "Operating units" });
    await user.click(within(table).getByRole("button", { name: "Edit operating unit CC100" }));
    const form = await screen.findByRole("form", { name: "Edit operating unit CC100" });
    expect(within(form).getByLabelText(/Default legal entity/)).toHaveValue("le-old");
  });

  it("closes without a request when nothing changed, returns focus to Edit, and Escape cancels", async () => {
    const fetchMock = serve({ ...entities, "GET /operating-units": () => json({ items: [unit] }) });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, OU_WRITER);
    const table = await findTable({ name: "Operating units" });
    await user.click(within(table).getByRole("button", { name: "Edit operating unit CC100" }));
    let form = await screen.findByRole("form", { name: "Edit operating unit CC100" });
    expect(within(form).getByLabelText("Name")).toHaveFocus();
    await user.click(within(form).getByRole("button", { name: "Save operating unit CC100" }));
    expect(screen.queryByRole("form", { name: "Edit operating unit CC100" })).toBeNull();
    expect(within(table).getByRole("button", { name: "Edit operating unit CC100" })).toHaveFocus();
    await user.keyboard("{Enter}");
    form = await screen.findByRole("form", { name: "Edit operating unit CC100" });
    await user.type(within(form).getByLabelText("Name"), " line");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("form", { name: "Edit operating unit CC100" })).toBeNull();
    expect(within(table).getByRole("button", { name: "Edit operating unit CC100" })).toHaveFocus();
    expect(callsTo(fetchMock, "PATCH", "/operating-units")).toHaveLength(0);
  });

  it("edits a site under its own legal entity and clears the default warehouse", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /sites": () => json({ items: [site] }),
      "GET /warehouses": () => json({ items: [warehouse, { ...warehouse, id: "w-2", siteId: "s-2", code: "OTHER" }] }),
      "PATCH /sites/s-1": () => json({ ...site, name: "Detroit plant", defaultWarehouseId: undefined, version: 4 }),
    });
    const { user } = renderPage("/org/sites", SitesPage, SITE_WRITER);
    const table = await findTable({ name: "Sites" });
    await user.click(within(table).getByRole("button", { name: "Edit site DET" }));
    const form = await screen.findByRole("form", { name: "Edit site DET" });
    const choice = within(form).getByLabelText(/Default warehouse/);
    expect(within(choice).getAllByRole("option").map((option) => option.textContent)).toEqual(["None", "WH1 - Main"]);
    await user.selectOptions(choice, "");
    await user.clear(within(form).getByLabelText("Name"));
    await user.type(within(form).getByLabelText("Name"), "Detroit plant");
    await user.click(within(form).getByRole("button", { name: "Save site DET" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/sites/s-1")).toHaveLength(1));
    const call = callsTo(fetchMock, "PATCH", "/sites/s-1")[0];
    expect(bodyOf(call as never)).toEqual({ version: 3, name: "Detroit plant", defaultWarehouseId: "" });
    expect(new Headers(call[1]?.headers).get("X-Passage-LegalEntity")).toBe("le-1");
    expect(await within(table).findByText("Detroit plant")).toBeInTheDocument();
    expect(within(table).queryByText("w-1")).toBeNull();
  });

  it("shows an invalid default warehouse on its field", async () => {
    serve({
      ...entities,
      "GET /sites": () => json({ items: [{ ...site, defaultWarehouseId: undefined }] }),
      "GET /warehouses": () => json({ items: [warehouse] }),
      "PATCH /sites/s-1": () => problem("ORG-SITE-DEFAULT-WAREHOUSE-INVALID", 422),
    });
    const { user } = renderPage("/org/sites", SitesPage, SITE_WRITER);
    const table = await findTable({ name: "Sites" });
    await user.click(within(table).getByRole("button", { name: "Edit site DET" }));
    const form = await screen.findByRole("form", { name: "Edit site DET" });
    await user.selectOptions(within(form).getByLabelText(/Default warehouse/), "w-1");
    await user.click(within(form).getByRole("button", { name: "Save site DET" }));
    expect(await within(form).findByText("Choose an active warehouse of this site.")).toBeInTheDocument();
  });

  it("refuses a cleared time zone on the field instead of dropping it", async () => {
    const fetchMock = serve({ ...entities, "GET /sites": () => json({ items: [site] }), "GET /warehouses": () => json({ items: [warehouse] }) });
    const { user } = renderPage("/org/sites", SitesPage, SITE_WRITER);
    const table = await findTable({ name: "Sites" });
    await user.click(within(table).getByRole("button", { name: "Edit site DET" }));
    const form = await screen.findByRole("form", { name: "Edit site DET" });
    const zone = within(form).getByLabelText("Time zone");
    await user.clear(zone);
    await user.click(within(form).getByRole("button", { name: "Save site DET" }));
    expect(await within(form).findByText("Enter an IANA time zone such as America/Detroit.")).toBeInTheDocument();
    expect(zone).toHaveAttribute("aria-invalid", "true");
    expect(callsTo(fetchMock, "PATCH", "/sites")).toHaveLength(0);
  });

  it("edits a warehouse name, type and time zone", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /sites": () => json({ items: [site] }),
      "GET /warehouses": () => json({ items: [warehouse] }),
      "PATCH /warehouses/w-1": () => json({ ...warehouse, type: "quarantine", version: 6 }),
    });
    const { user } = renderPage("/org/warehouses", WarehousesPage, SITE_WRITER);
    const table = await findTable({ name: "Warehouses" });
    await user.click(within(table).getByRole("button", { name: "Edit warehouse WH1" }));
    const form = await screen.findByRole("form", { name: "Edit warehouse WH1" });
    await user.selectOptions(within(form).getByLabelText("Type"), "quarantine");
    await user.click(within(form).getByRole("button", { name: "Save warehouse WH1" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/warehouses/w-1")).toHaveLength(1));
    const call = callsTo(fetchMock, "PATCH", "/warehouses/w-1")[0];
    expect(bodyOf(call as never)).toEqual({ version: 5, type: "quarantine" });
    expect(new Headers(call[1]?.headers).get("X-Passage-LegalEntity")).toBe("le-1");
    expect(await within(table).findByText("quarantine")).toBeInTheDocument();
  });

  it("renames a location and changes its type", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /warehouses": () => json({ items: [warehouse] }),
      "GET /locations": () => json({ items: [location] }),
      "PATCH /locations/l-1": () => json({ ...location, name: "Aisle one", type: "receiving", version: 3 }),
    });
    const { user } = renderPage("/org/locations", LocationsPage, SITE_WRITER);
    const table = await findTable({ name: "Locations" });
    await user.click(within(table).getByRole("button", { name: "Edit location A1" }));
    const form = await screen.findByRole("form", { name: "Edit location A1" });
    await user.clear(within(form).getByLabelText("Name"));
    await user.type(within(form).getByLabelText("Name"), "Aisle one");
    await user.selectOptions(within(form).getByLabelText("Type"), "receiving");
    await user.click(within(form).getByRole("button", { name: "Save location A1" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/locations/l-1")).toHaveLength(1));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/locations/l-1")[0] as never)).toEqual({ version: 2, name: "Aisle one", type: "receiving" });
    expect(await within(table).findByText("Aisle one")).toBeInTheDocument();
  });
});
