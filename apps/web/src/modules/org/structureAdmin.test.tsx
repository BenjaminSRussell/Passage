import { findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement, type ComponentType } from "react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FiscalCalendarsPage, validateCalendar, validateYear } from "./FiscalCalendarsPage";
import { LegalEntitiesPage, validateLegalEntity } from "./LegalEntityAdmin";
import { OrgLayout } from "./pages";
import { stubLocalStorage } from "./testStorage";
import { TeamsPage, validateTeam } from "./TeamsPage";

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

const LE_ADMIN = ["org.read", "org.legal-entity.write", "tenant.administer"];
const CAL_ADMIN = ["org.read", "org.fiscal-calendar.write", "tenant.administer"];
const TEAM_ADMIN = ["org.read", "org.operating-unit.write", "tenant.administer"];

const entity = { id: "le-1", code: "USMF", name: "Contoso US", country: "US", currency: "USD", timeZone: "UTC", status: "active", version: 2 };

describe("validators", () => {
  it("checks legal entity fields", () => {
    expect(validateLegalEntity({ code: "A", name: " ", country: "", currency: "", timeZone: "" })).toEqual({
      code: expect.any(String),
      name: expect.any(String),
      country: expect.any(String),
      currency: expect.any(String),
      timeZone: expect.any(String),
    });
    expect(validateLegalEntity({ code: "usmf", name: "N", country: "US", currency: "USD", timeZone: "UTC" })).toEqual({});
  });
  it("checks calendars, years and teams", () => {
    expect(Object.keys(validateCalendar("", ""))).toEqual(["code", "name"]);
    expect(validateCalendar("FY", "Fiscal")).toEqual({});
    expect(validateYear("1899")).toBeDefined();
    expect(validateYear("20x6")).toBeDefined();
    expect(validateYear("2026")).toBeUndefined();
    expect(Object.keys(validateTeam({ code: "", name: "", teamTypeId: "", effectiveFrom: "2026-13-45", description: "" }))).toEqual([
      "code",
      "name",
      "teamTypeId",
      "effectiveFrom",
    ]);
  });
});

describe("legal entities page", () => {
  const lookups = {
    "GET /countries": () => json({ items: [{ code: "US", name: "United States", status: "active" }] }),
    "GET /currencies": () => json({ items: [{ code: "USD", name: "US Dollar", status: "active" }] }),
  };

  it("hides every write action without the duty", async () => {
    serve({ "GET /legal-entities": () => json({ items: [entity] }) });
    renderPage("/org/legal-entities", LegalEntitiesPage, ["org.read"]);
    const table = await findTable({ name: "Legal entities" });
    expect(within(table).getByText("Contoso US")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Add legal entity/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Deactivate/ })).toBeNull();
  });

  it("keeps the status action but hides create without tenant.administer", async () => {
    serve({ ...lookups, "GET /legal-entities": () => json({ items: [entity] }) });
    renderPage("/org/legal-entities", LegalEntitiesPage, ["org.read", "org.legal-entity.write"]);
    await findTable({ name: "Legal entities" });
    expect(await screen.findByRole("button", { name: "Deactivate legal entity USMF" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add legal entity" })).toBeNull();
  });

  it("shows the empty and error states", async () => {
    serve({ "GET /legal-entities": () => json({ items: [] }) });
    const first = renderPage("/org/legal-entities", LegalEntitiesPage, ["org.read"]);
    expect(await screen.findByRole("heading", { name: "No legal entities" })).toBeInTheDocument();
    first.unmount();
    serve({ "GET /legal-entities": () => problem("ORG-X-BOOM", 500, "it broke") });
    renderPage("/org/legal-entities", LegalEntitiesPage, ["org.read"]);
    expect((await screen.findAllByText(/ORG-X-BOOM/)).length).toBeGreaterThan(0);
  });

  it("blocks an invalid form and creates a valid one with an upper-cased code", async () => {
    const fetchMock = serve({ ...lookups, "GET /legal-entities": () => json({ items: [entity] }), "POST /legal-entities": () => json({ ...entity, id: "le-2" }, 201) });
    const { user } = renderPage("/org/legal-entities", LegalEntitiesPage, LE_ADMIN);
    await findTable({ name: "Legal entities" });
    await user.click(screen.getByRole("button", { name: "Add legal entity" }));
    expect(await screen.findByText("Use 2 to 8 letters or digits.")).toBeInTheDocument();
    expect(callsTo(fetchMock, "POST", "/legal-entities")).toHaveLength(0);

    await screen.findByRole("option", { name: "US - United States" });
    await screen.findByRole("option", { name: "USD - US Dollar" });
    await user.type(screen.getByLabelText("Code"), "demf");
    await user.type(screen.getByLabelText("Name"), "Demo Germany");
    await user.selectOptions(screen.getByLabelText("Country"), "US");
    await user.selectOptions(screen.getByLabelText("Accounting currency"), "USD");
    await user.click(screen.getByRole("button", { name: "Add legal entity" }));
    await waitFor(() => expect(callsTo(fetchMock, "POST", "/legal-entities")).toHaveLength(1));
    const body = JSON.parse(String(callsTo(fetchMock, "POST", "/legal-entities")[0][1]?.body));
    expect(body).toEqual({ code: "DEMF", name: "Demo Germany", country: "US", currency: "USD", timeZone: "UTC" });
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/legal-entities").length).toBeGreaterThan(1));
  });

  it("shows a duplicate code on the code field", async () => {
    serve({ ...lookups, "GET /legal-entities": () => json({ items: [] }), "POST /legal-entities": () => problem("ORG-LE-CODE-DUPLICATE", 409) });
    const { user } = renderPage("/org/legal-entities", LegalEntitiesPage, LE_ADMIN);
    await screen.findByRole("option", { name: "USD - US Dollar" });
    await user.type(screen.getByLabelText("Code"), "USMF");
    await user.type(screen.getByLabelText("Name"), "Contoso");
    await user.selectOptions(screen.getByLabelText("Country"), "US");
    await user.selectOptions(screen.getByLabelText("Accounting currency"), "USD");
    await user.click(screen.getByRole("button", { name: "Add legal entity" }));
    expect(await screen.findByText("A legal entity with this code already exists.")).toBeInTheDocument();
  });

  it("deactivates after a confirm step and sends the row version", async () => {
    const fetchMock = serve({
      ...lookups,
      "GET /legal-entities": () => json({ items: [entity] }),
      "PATCH /legal-entities/le-1": () => json({ ...entity, status: "inactive", version: 3 }),
    });
    const { user } = renderPage("/org/legal-entities", LegalEntitiesPage, LE_ADMIN);
    await findTable({ name: "Legal entities" });
    await user.click(await screen.findByRole("button", { name: "Deactivate legal entity USMF" }));
    expect(callsTo(fetchMock, "PATCH", "/legal-entities/le-1")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "Confirm deactivate legal entity USMF" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/legal-entities/le-1")).toHaveLength(1));
    expect(JSON.parse(String(callsTo(fetchMock, "PATCH", "/legal-entities/le-1")[0][1]?.body))).toEqual({ version: 2, status: "inactive" });
    expect(await screen.findByText("Inactive")).toBeInTheDocument();
  });

  it("explains a version conflict", async () => {
    serve({ ...lookups, "GET /legal-entities/le-1": () => json(entity), "GET /legal-entities": () => json({ items: [entity] }), "PATCH /legal-entities/le-1": () => problem("ORG-VERSION-CONFLICT", 409, "stale version") });
    const { user } = renderPage("/org/legal-entities", LegalEntitiesPage, LE_ADMIN);
    await findTable({ name: "Legal entities" });
    await user.click(await screen.findByRole("button", { name: "Deactivate legal entity USMF" }));
    await user.click(screen.getByRole("button", { name: "Confirm deactivate legal entity USMF" }));
    expect(await screen.findByText(/ORG-VERSION-CONFLICT/)).toBeInTheDocument();
  });

  it("reloads the row after a version conflict so a retry sends the current version", async () => {
    let patches = 0;
    const fetchMock = serve({
      ...lookups,
      "GET /legal-entities/le-1": () => json({ ...entity, version: 7 }),
      "GET /legal-entities": () => json({ items: [entity] }),
      "PATCH /legal-entities/le-1": () => (++patches === 1 ? problem("ORG-VERSION-CONFLICT", 409, "current version is 7") : json({ ...entity, status: "inactive", version: 8 })),
    });
    const { user } = renderPage("/org/legal-entities", LegalEntitiesPage, LE_ADMIN);
    const table = await findTable({ name: "Legal entities" });
    await user.click(await screen.findByRole("button", { name: "Deactivate legal entity USMF" }));
    await user.click(screen.getByRole("button", { name: "Confirm deactivate legal entity USMF" }));
    expect(await screen.findByText(/ORG-VERSION-CONFLICT/)).toBeInTheDocument();
    expect(await within(table).findByText("7")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Confirm deactivate legal entity USMF" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/legal-entities/le-1")).toHaveLength(2));
    expect(JSON.parse(String(callsTo(fetchMock, "PATCH", "/legal-entities/le-1")[1][1]?.body))).toEqual({ version: 7, status: "inactive" });
  });
});

const calendar = { id: "cal-1", code: "FY", name: "Fiscal year", pattern: "4-4-5", startMonth: 1, status: "active", version: 1, generatedYears: [2026] };
const period = { fiscalYear: 2026, periodNo: 1, name: "P1", startDate: "2026-01-01", endDate: "2026-02-04", type: "operating", week53: false };

describe("fiscal calendars page", () => {
  it("lists calendars, empty and error states", async () => {
    serve({ "GET /fiscal-calendars": () => json({ items: [calendar] }) });
    const first = renderPage("/org/fiscal-calendars", FiscalCalendarsPage, ["org.read"]);
    const table = await findTable({ name: "Fiscal calendars" });
    expect(within(table).getByText("Fiscal year")).toBeInTheDocument();
    expect(within(table).getByText("2026")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add fiscal calendar" })).toBeNull();
    first.unmount();
    serve({ "GET /fiscal-calendars": () => json({ items: [] }) });
    const second = renderPage("/org/fiscal-calendars", FiscalCalendarsPage, ["org.read"]);
    expect(await screen.findByRole("heading", { name: "No fiscal calendars" })).toBeInTheDocument();
    second.unmount();
    serve({ "GET /fiscal-calendars": () => problem("ORG-X-BOOM", 500) });
    renderPage("/org/fiscal-calendars", FiscalCalendarsPage, ["org.read"]);
    expect((await screen.findAllByText(/ORG-X-BOOM/)).length).toBeGreaterThan(0);
  });

  it("creates a calendar", async () => {
    const fetchMock = serve({ "GET /fiscal-calendars": () => json({ items: [] }), "POST /fiscal-calendars": () => json(calendar, 201) });
    const { user } = renderPage("/org/fiscal-calendars", FiscalCalendarsPage, CAL_ADMIN);
    await screen.findByRole("heading", { name: "No fiscal calendars" });
    await user.click(screen.getByRole("button", { name: "Add fiscal calendar" }));
    expect(await screen.findByText("Use 1 to 10 letters, digits, - or _.")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Code"), "fy");
    await user.type(screen.getByLabelText("Name"), "Fiscal year");
    await user.selectOptions(screen.getByLabelText("Pattern"), "4-4-5");
    await user.click(screen.getByRole("button", { name: "Add fiscal calendar" }));
    await waitFor(() => expect(callsTo(fetchMock, "POST", "/fiscal-calendars")).toHaveLength(1));
    expect(JSON.parse(String(callsTo(fetchMock, "POST", "/fiscal-calendars")[0][1]?.body))).toEqual({ code: "FY", name: "Fiscal year", pattern: "4-4-5", startMonth: 1 });
  });

  it("shows periods and generates a year", async () => {
    const fetchMock = serve({
      "GET /fiscal-calendars/cal-1/periods": () => json({ items: [period] }),
      "GET /fiscal-calendars": () => json({ items: [calendar] }),
      "POST /fiscal-calendars/cal-1:generate": () => json({ calendarId: "cal-1", fiscalYear: 2027, generated: true, periods: [] }, 201),
    });
    const { user } = renderPage("/org/fiscal-calendars", FiscalCalendarsPage, CAL_ADMIN);
    await findTable({ name: "Fiscal calendars" });
    await user.click(screen.getByRole("button", { name: "Open periods of FY" }));
    const periods = await findTable({ name: "Periods of FY" });
    expect(within(periods).getByText("P1")).toBeInTheDocument();
    const year = screen.getByLabelText("Generate fiscal year");
    await user.clear(year);
    await user.type(year, "99");
    await user.click(screen.getByRole("button", { name: "Generate year" }));
    expect(await screen.findByText("Enter a year from 1900 to 2999.")).toBeInTheDocument();
    await user.clear(year);
    await user.type(year, "2027");
    await user.click(screen.getByRole("button", { name: "Generate year" }));
    await waitFor(() => expect(callsTo(fetchMock, "POST", ":generate")).toHaveLength(1));
    expect(JSON.parse(String(callsTo(fetchMock, "POST", ":generate")[0][1]?.body))).toEqual({ fiscalYear: 2027 });
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/periods?fiscalYear=2027")).toHaveLength(1));
    expect(screen.getByLabelText("Fiscal year")).toHaveValue("2027");
    expect(screen.getByRole("table", { name: "Periods of FY" })).toBeInTheDocument();
  });

  it("hides calendar writes without tenant.administer", async () => {
    serve({ "GET /fiscal-calendars/cal-1/periods": () => json({ items: [period] }), "GET /fiscal-calendars": () => json({ items: [calendar] }) });
    const { user } = renderPage("/org/fiscal-calendars", FiscalCalendarsPage, ["org.read", "org.fiscal-calendar.write"]);
    await findTable({ name: "Fiscal calendars" });
    await user.click(screen.getByRole("button", { name: "Open periods of FY" }));
    await findTable({ name: "Periods of FY" });
    expect(screen.queryByRole("button", { name: "Add fiscal calendar" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Generate year" })).toBeNull();
  });

  it("reports a generation refusal", async () => {
    serve({
      "GET /fiscal-calendars": () => json({ items: [{ ...calendar, generatedYears: [] }] }),
      "POST /fiscal-calendars/cal-1:generate": () => problem("ORG-CAL-INACTIVE", 422, "calendar is inactive"),
    });
    const { user } = renderPage("/org/fiscal-calendars", FiscalCalendarsPage, CAL_ADMIN);
    await findTable({ name: "Fiscal calendars" });
    await user.click(screen.getByRole("button", { name: "Open periods of FY" }));
    expect(await screen.findByRole("heading", { name: "No periods generated" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Generate year" }));
    expect(await screen.findByText(/ORG-CAL-INACTIVE/)).toBeInTheDocument();
  });
});

const teamType = { id: "tt-1", code: "PLANNERS", name: "Planners", memberKinds: ["user"] };
const team = { id: "t-1", code: "MRP", name: "MRP planners", teamTypeId: "tt-1", effectiveFrom: "2026-01-01", status: "active", version: 4 };

describe("teams page", () => {
  it("lists teams with their type and shows members", async () => {
    serve({
      "GET /team-types": () => json({ items: [teamType] }),
      "GET /teams/t-1/members": () => json({ items: [{ id: "m1", memberKind: "user", memberId: "u-1", memberRole: "lead", validFrom: "2026-01-01" }] }),
      "GET /teams": () => json({ items: [team] }),
    });
    const { user } = renderPage("/org/teams", TeamsPage, ["org.read"]);
    const table = await findTable({ name: "Teams" });
    await waitFor(() => expect(within(table).getByText("PLANNERS")).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Add team" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Show members of MRP" }));
    const members = await findTable({ name: "Members of MRP" });
    expect(within(members).getByText("u-1")).toBeInTheDocument();
  });

  it("hides team writes without tenant.administer", async () => {
    serve({ "GET /team-types": () => json({ items: [teamType] }), "GET /teams": () => json({ items: [team] }) });
    renderPage("/org/teams", TeamsPage, ["org.read", "org.operating-unit.write"]);
    await findTable({ name: "Teams" });
    expect(await screen.findByRole("button", { name: "Show members of MRP" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add team" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Deactivate team MRP" })).toBeNull();
  });

  it("shows empty and error states", async () => {
    serve({ "GET /teams": () => json({ items: [] }) });
    const first = renderPage("/org/teams", TeamsPage, ["org.read"]);
    expect(await screen.findByRole("heading", { name: "No teams" })).toBeInTheDocument();
    first.unmount();
    serve({ "GET /teams": () => problem("ORG-X-BOOM", 500) });
    renderPage("/org/teams", TeamsPage, ["org.read"]);
    expect((await screen.findAllByText(/ORG-X-BOOM/)).length).toBeGreaterThan(0);
  });

  it("creates a team and deactivates one", async () => {
    const fetchMock = serve({
      "GET /team-types": () => json({ items: [teamType] }),
      "GET /teams": () => json({ items: [team] }),
      "POST /teams": () => json({ ...team, id: "t-2" }, 201),
      "PATCH /teams/t-1": () => json({ ...team, status: "inactive", version: 5 }),
    });
    const { user } = renderPage("/org/teams", TeamsPage, TEAM_ADMIN);
    await findTable({ name: "Teams" });
    await user.click(screen.getByRole("button", { name: "Add team" }));
    expect(await screen.findByText("Select a team type.")).toBeInTheDocument();
    await screen.findByRole("option", { name: "PLANNERS - Planners" });
    await user.type(screen.getByLabelText("Code"), "buy");
    await user.type(screen.getByLabelText("Name"), "Buyers");
    await user.selectOptions(screen.getByLabelText("Team type"), "tt-1");
    await user.click(screen.getByRole("button", { name: "Add team" }));
    await waitFor(() => expect(callsTo(fetchMock, "POST", "/teams")).toHaveLength(1));
    expect(JSON.parse(String(callsTo(fetchMock, "POST", "/teams")[0][1]?.body))).toMatchObject({ code: "BUY", name: "Buyers", teamTypeId: "tt-1" });

    await user.click(await screen.findByRole("button", { name: "Deactivate team MRP" }));
    await user.click(screen.getByRole("button", { name: "Confirm deactivate team MRP" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/teams/t-1")).toHaveLength(1));
    expect(JSON.parse(String(callsTo(fetchMock, "PATCH", "/teams/t-1")[0][1]?.body))).toEqual({ version: 4, status: "inactive" });
  });
});
