import { findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement, type ComponentType } from "react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OrgLayout } from "./pages";
import { stubLocalStorage } from "./testStorage";
import { SitesPage, WarehousesPage } from "./StructurePages";

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
const calendars = [
  { id: "c-1", code: "FY", name: "Fiscal year", status: "active", generatedYears: [2026] },
  { id: "c-2", code: "OLD", name: "Retired", status: "inactive", generatedYears: [2024] },
  { id: "c-3", code: "NEW", name: "Not generated", status: "active", generatedYears: [] },
];
const site = { id: "s-1", legalEntityId: "le-1", code: "DET", name: "Detroit", timeZone: "America/Detroit", status: "active", fiscalCalendarId: "c-1", version: 3 };
const sites = [
  site,
  { ...site, id: "s-2", code: "CHI", name: "Chicago", fiscalCalendarId: undefined },
  { ...site, id: "s-3", code: "OFF", name: "Closed", status: "inactive" },
  { ...site, id: "s-4", code: "EU", name: "Other entity", legalEntityId: "le-2" },
];
const warehouse = { id: "w-1", legalEntityId: "le-1", siteId: "s-1", code: "WH1", name: "Main", type: "standard", timeZone: "UTC", status: "active", version: 5 };
const WRITER = ["org.read", "org.site.write"];
const bodyOf = (call: [unknown, RequestInit | undefined]) => JSON.parse(String(call[1]?.body));

describe("site fiscal calendar and warehouse site", () => {
  it("offers active calendars with a generated year, and sends only the changed calendar", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /fiscal-calendars": () => json({ items: calendars }),
      "GET /sites": () => json({ items: [site] }),
      "PATCH /sites/s-1": () => json({ ...site, fiscalCalendarId: undefined, version: 4 }),
    });
    const { user } = renderPage("/org/sites", SitesPage, WRITER);
    const table = await findTable({ name: "Sites" });
    await user.click(within(table).getByRole("button", { name: "Edit site DET" }));
    const form = await screen.findByRole("form", { name: "Edit site DET" });
    const choice = within(form).getByLabelText(/Fiscal calendar/);
    await waitFor(() => expect(within(choice).getAllByRole("option").map((option) => option.textContent)).toEqual(["None", "FY - Fiscal year"]));
    expect(choice).toHaveValue("c-1");
    await user.selectOptions(choice, "");
    await user.click(within(form).getByRole("button", { name: "Save site DET" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/sites/s-1")).toHaveLength(1));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/sites/s-1")[0] as never)).toEqual({ version: 3, fiscalCalendarId: "" });
    await waitFor(() => expect(screen.queryByRole("form", { name: "Edit site DET" })).toBeNull());
  });

  it("shows ORG-SITE-CALENDAR-INVALID on the calendar field", async () => {
    serve({
      ...entities,
      "GET /fiscal-calendars": () => json({ items: calendars }),
      "GET /sites": () => json({ items: [{ ...site, fiscalCalendarId: undefined }] }),
      "PATCH /sites/s-1": () => problem("ORG-SITE-CALENDAR-INVALID", 422),
    });
    const { user } = renderPage("/org/sites", SitesPage, WRITER);
    const table = await findTable({ name: "Sites" });
    await user.click(within(table).getByRole("button", { name: "Edit site DET" }));
    const form = await screen.findByRole("form", { name: "Edit site DET" });
    await waitFor(() => expect(within(within(form).getByLabelText(/Fiscal calendar/)).getAllByRole("option")).toHaveLength(2));
    await user.selectOptions(within(form).getByLabelText(/Fiscal calendar/), "c-1");
    await user.click(within(form).getByRole("button", { name: "Save site DET" }));
    expect(await within(form).findByText("Choose an active fiscal calendar with a generated year.")).toBeInTheDocument();
  });

  it("keeps an inactive current calendar selectable and leaves it out of the body when untouched", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /fiscal-calendars": () => json({ items: calendars }),
      "GET /sites": () => json({ items: [{ ...site, fiscalCalendarId: "c-2" }] }),
      "PATCH /sites/s-1": () => json({ ...site, name: "Detroit 2", fiscalCalendarId: "c-2", version: 4 }),
    });
    const { user } = renderPage("/org/sites", SitesPage, WRITER);
    const table = await findTable({ name: "Sites" });
    await user.click(within(table).getByRole("button", { name: "Edit site DET" }));
    const form = await screen.findByRole("form", { name: "Edit site DET" });
    const choice = within(form).getByLabelText(/Fiscal calendar/);
    await waitFor(() => expect(within(choice).getAllByRole("option").map((option) => option.textContent)).toEqual(["None", "FY - Fiscal year", "OLD - Retired"]));
    expect(choice).toHaveValue("c-2");
    await user.clear(within(form).getByLabelText("Name"));
    await user.type(within(form).getByLabelText("Name"), "Detroit 2");
    await user.click(within(form).getByRole("button", { name: "Save site DET" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/sites/s-1")).toHaveLength(1));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/sites/s-1")[0] as never)).toEqual({ version: 3, name: "Detroit 2" });
  });

  it("says when the calendars could not be loaded", async () => {
    serve({
      ...entities,
      "GET /fiscal-calendars": () => problem("COM-0500", 500, "boom"),
      "GET /sites": () => json({ items: [site] }),
    });
    const { user } = renderPage("/org/sites", SitesPage, WRITER);
    const table = await findTable({ name: "Sites" });
    await user.click(within(table).getByRole("button", { name: "Edit site DET" }));
    const form = await screen.findByRole("form", { name: "Edit site DET" });
    expect(await within(form).findByText(/^Fiscal calendars could not be loaded\./)).toBeInTheDocument();
  });

  it("does not load fiscal calendars for a read-only caller", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /fiscal-calendars": () => json({ items: calendars }),
      "GET /sites": () => json({ items: [site] }),
    });
    renderPage("/org/sites", SitesPage, ["org.read"]);
    const table = await findTable({ name: "Sites" });
    expect(within(table).getByText("DET")).toBeInTheDocument();
    expect(within(table).queryByRole("button", { name: "Edit site DET" })).toBeNull();
    expect(callsTo(fetchMock, "GET", "/fiscal-calendars")).toHaveLength(0);
  });

  it("moves a warehouse to another active site of its legal entity", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /sites": () => json({ items: sites }),
      "GET /warehouses": () => json({ items: [warehouse] }),
      "PATCH /warehouses/w-1": () => json({ ...warehouse, siteId: "s-2", version: 6 }),
    });
    const { user } = renderPage("/org/warehouses", WarehousesPage, WRITER);
    const table = await findTable({ name: "Warehouses" });
    await user.click(within(table).getByRole("button", { name: "Edit warehouse WH1" }));
    const form = await screen.findByRole("form", { name: "Edit warehouse WH1" });
    const choice = within(form).getByLabelText("Site");
    expect(within(choice).getAllByRole("option").map((option) => option.textContent)).toEqual(["DET - Detroit", "CHI - Chicago"]);
    await user.selectOptions(choice, "s-2");
    await user.click(within(form).getByRole("button", { name: "Save warehouse WH1" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/warehouses/w-1")).toHaveLength(1));
    const call = callsTo(fetchMock, "PATCH", "/warehouses/w-1")[0];
    expect(bodyOf(call as never)).toEqual({ version: 5, siteId: "s-2" });
    expect(new Headers(call[1]?.headers).get("X-Passage-LegalEntity")).toBe("le-1");
    expect(await within(table).findByText("CHI")).toBeInTheDocument();
  });

  it("warns on the site field when the warehouse is its site's default", async () => {
    serve({
      ...entities,
      "GET /sites": () => json({ items: [{ ...site, defaultWarehouseId: "w-1" }, ...sites.slice(1)] }),
      "GET /warehouses": () => json({ items: [warehouse] }),
    });
    const { user } = renderPage("/org/warehouses", WarehousesPage, WRITER);
    const table = await findTable({ name: "Warehouses" });
    await user.click(within(table).getByRole("button", { name: "Edit warehouse WH1" }));
    const form = await screen.findByRole("form", { name: "Edit warehouse WH1" });
    expect(within(form).getByLabelText("Site")).toHaveAccessibleDescription(/This is its site's default warehouse/);
  });

  it.each([
    ["ORG-WH-IS-DEFAULT", "This is its site's default warehouse; change the site's default first."],
    ["ORG-WH-SITE-INVALID", "Choose an active site."],
    ["ORG-WH-LE-MISMATCH", "Choose a site of the same legal entity."],
  ])("shows %s on the site field", async (code, message) => {
    serve({
      ...entities,
      "GET /sites": () => json({ items: sites }),
      "GET /warehouses": () => json({ items: [warehouse] }),
      "PATCH /warehouses/w-1": () => problem(code, 422),
    });
    const { user } = renderPage("/org/warehouses", WarehousesPage, WRITER);
    const table = await findTable({ name: "Warehouses" });
    await user.click(within(table).getByRole("button", { name: "Edit warehouse WH1" }));
    const form = await screen.findByRole("form", { name: "Edit warehouse WH1" });
    await user.selectOptions(within(form).getByLabelText("Site"), "s-2");
    await user.click(within(form).getByRole("button", { name: "Save warehouse WH1" }));
    expect(await within(form).findByText(message)).toBeInTheDocument();
  });

  it("keeps the chosen site on a version conflict and retries with the new version", async () => {
    let patches = 0;
    const fetchMock = serve({
      ...entities,
      "GET /sites": () => json({ items: sites }),
      "GET /warehouses/w-1": () => json({ ...warehouse, name: "Renamed elsewhere", version: 6 }),
      "GET /warehouses": () => json({ items: [warehouse] }),
      "PATCH /warehouses/w-1": () => (++patches === 1 ? problem("ORG-VERSION-CONFLICT", 409, "current version is 6") : json({ ...warehouse, name: "Renamed elsewhere", siteId: "s-2", version: 7 })),
    });
    const { user } = renderPage("/org/warehouses", WarehousesPage, WRITER);
    const table = await findTable({ name: "Warehouses" });
    await user.click(within(table).getByRole("button", { name: "Edit warehouse WH1" }));
    const form = await screen.findByRole("form", { name: "Edit warehouse WH1" });
    await user.selectOptions(within(form).getByLabelText("Site"), "s-2");
    await user.click(within(form).getByRole("button", { name: "Save warehouse WH1" }));
    expect(await within(form).findByText(/your changes are kept/)).toBeInTheDocument();
    await waitFor(() => expect(within(form).getByLabelText("Name")).toHaveValue("Renamed elsewhere"));
    expect(within(form).getByLabelText("Site")).toHaveValue("s-2");
    await user.click(within(form).getByRole("button", { name: "Save warehouse WH1" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/warehouses/w-1")).toHaveLength(2));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/warehouses/w-1")[1] as never)).toEqual({ version: 6, siteId: "s-2" });
  });
});
