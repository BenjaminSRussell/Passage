import { findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement, type ComponentType } from "react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FiscalCalendarsPage, parsePeriods } from "./FiscalCalendarsPage";
import { LegalEntitiesPage } from "./LegalEntityAdmin";
import { OrgLayout } from "./pages";
import { LocationsPage, validateLocation } from "./StructurePages";
import { TeamsPage } from "./TeamsPage";
import { validateMember, validateTeamType } from "./TeamWrites";
import { stubLocalStorage } from "./testStorage";

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
const bodyOf = (call: [unknown, RequestInit?]) => JSON.parse(String(call[1]?.body));

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
const ADMIN = ["org.read", "org.operating-unit.write", "org.fiscal-calendar.write", "org.legal-entity.write", "tenant.administer"];

const teamType = { id: "tt-1", code: "PLANNERS", name: "Planners", memberKinds: ["user"] };
const team = { id: "t-1", code: "MRP", name: "MRP planners", teamTypeId: "tt-1", effectiveFrom: "2026-01-01", status: "active", version: 4 };
const userId = "0b6f6c64-6f0e-4c61-8a53-3a7c1f0f2b11";
const member = { id: "m-1", memberKind: "user", memberId: userId, memberRole: "lead", validFrom: "2026-01-01" };

describe("validators", () => {
  it("checks team types, members, locations and periods", () => {
    expect(Object.keys(validateTeamType({ code: "x", name: " ", kinds: "" }))).toEqual(["code", "name", "kinds"]);
    expect(validateTeamType({ code: "PLAN", name: "Planners", kinds: "both" })).toEqual({});
    expect(Object.keys(validateMember({ memberKind: "", memberId: "u-1", memberRole: "member", validFrom: "soon", validTo: "" }))).toEqual(["memberKind", "memberId", "validFrom"]);
    expect(validateMember({ memberKind: "user", memberId: userId, memberRole: "member", validFrom: "2026-02-01", validTo: "2026-01-01" }).validTo).toMatch(/before/);
    expect(Object.keys(validateLocation({ warehouseId: "", code: "-", name: "", type: "storage" }))).toEqual(["warehouseId", "code", "name"]);
    expect(parsePeriods("2026-01-01 2026-01-31\n2026-02-01, 2026-02-28\n")).toEqual({
      periods: [
        { start: "2026-01-01", end: "2026-01-31" },
        { start: "2026-02-01", end: "2026-02-28" },
      ],
    });
    expect(parsePeriods("2026-02-01 2026-01-01").error).toMatch(/Line 1/);
    expect(parsePeriods("2026-01-01").error).toMatch(/Line 1/);
  });
});

describe("teams writes", () => {
  const routes = {
    ...entities,
    "GET /team-types": () => json({ items: [teamType] }),
    "GET /teams/t-1/members": () => json({ items: [member] }),
    "GET /teams": () => json({ items: [team] }),
  };

  it("creates a team type with its member kinds", async () => {
    const fetchMock = serve({ ...routes, "POST /team-types": () => json({ ...teamType, id: "tt-2" }, 201) });
    const { user } = renderPage("/org/teams", TeamsPage, ADMIN);
    await findTable({ name: "Teams" });
    await user.click(screen.getByRole("button", { name: "Add team type" }));
    expect(await screen.findByText("Select who can be a member.")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Team type code"), "ops");
    await user.type(screen.getByLabelText("Team type name"), "Operators");
    await user.selectOptions(screen.getByLabelText("Members can be"), "both");
    await user.click(screen.getByRole("button", { name: "Add team type" }));
    await waitFor(() => expect(callsTo(fetchMock, "POST", "/team-types")).toHaveLength(1));
    expect(bodyOf(callsTo(fetchMock, "POST", "/team-types")[0] as never)).toEqual({ code: "OPS", name: "Operators", memberKinds: ["user", "party"] });
    expect(new Headers(callsTo(fetchMock, "POST", "/team-types")[0][1]?.headers).get("Idempotency-Key")).toBeTruthy();
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/team-types").length).toBeGreaterThan(1));
  });

  it("shows a duplicate team type code on the field", async () => {
    serve({ ...routes, "POST /team-types": () => problem("ORG-TEAM-TYPE-CODE-DUPLICATE", 409) });
    const { user } = renderPage("/org/teams", TeamsPage, ADMIN);
    await findTable({ name: "Teams" });
    await user.type(screen.getByLabelText("Team type code"), "plan");
    await user.type(screen.getByLabelText("Team type name"), "Planners");
    await user.selectOptions(screen.getByLabelText("Members can be"), "user");
    await user.click(screen.getByRole("button", { name: "Add team type" }));
    expect(await screen.findByText("A team type with this code already exists.")).toBeInTheDocument();
  });

  it("edits a team with its row version and re-reads on a conflict", async () => {
    let patches = 0;
    const fetchMock = serve({
      ...routes,
      "GET /teams/t-1": () => json({ ...team, name: "Planners v2", version: 9 }),
      "PATCH /teams/t-1": () => (++patches === 1 ? problem("ORG-VERSION-CONFLICT", 409, "stale") : json({ ...team, name: "MRP crew", version: 10 })),
    });
    const { user } = renderPage("/org/teams", TeamsPage, ADMIN);
    await findTable({ name: "Teams" });
    await user.click(screen.getByRole("button", { name: "Show members of MRP" }));
    const name = await screen.findByLabelText("Name", { selector: "#edit-team-name" });
    await user.click(screen.getByRole("button", { name: "Save team" }));
    expect(await screen.findByText(/Nothing to save/)).toBeInTheDocument();
    await user.clear(name);
    await user.type(name, "MRP crew");
    await user.click(screen.getByRole("button", { name: "Save team" }));
    expect(await screen.findByText(/ORG-VERSION-CONFLICT/)).toBeInTheDocument();
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/teams/t-1")[0] as never)).toEqual({ version: 4, name: "MRP crew" });
    await waitFor(() => expect(callsTo(fetchMock, "GET", "/teams/t-1")).toHaveLength(1 + 0 + callsTo(fetchMock, "GET", "/teams/t-1/members").length));
    await user.click(screen.getByRole("button", { name: "Save team" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/teams/t-1")).toHaveLength(2));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/teams/t-1")[1] as never)).toMatchObject({ version: 9 });
    expect(await screen.findByText("Team saved")).toBeInTheDocument();
  });

  it("adds, ends and removes members", async () => {
    const fetchMock = serve({
      ...routes,
      "POST /teams/t-1/members/m-1:end": () => json({ ...member, validTo: "2026-06-30" }),
      "POST /teams/t-1/members": () => json({ ...member, id: "m-2" }, 201),
      "DELETE /teams/t-1/members/m-1": () => new Response(null, { status: 204 }),
    });
    const { user } = renderPage("/org/teams", TeamsPage, ADMIN);
    await findTable({ name: "Teams" });
    await user.click(screen.getByRole("button", { name: "Show members of MRP" }));
    await findTable({ name: "Members of MRP" });

    await user.click(screen.getByRole("button", { name: "Add member" }));
    expect(await screen.findByText("Enter the member's id (a UUID).")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Member id"), userId.toUpperCase());
    await user.click(screen.getByRole("button", { name: "Add member" }));
    await waitFor(() => expect(callsTo(fetchMock, "POST", "/teams/t-1/members")).toHaveLength(1));
    expect(bodyOf(callsTo(fetchMock, "POST", "/teams/t-1/members")[0] as never)).toMatchObject({ memberKind: "user", memberId: userId, memberRole: "member" });

    await user.click(screen.getByRole("button", { name: /End membership of user 0b6f6c64/ }));
    await user.clear(screen.getByLabelText(/End date for user/));
    await user.type(screen.getByLabelText(/End date for user/), "2026-06-30");
    await user.click(screen.getByRole("button", { name: /Confirm end of user/ }));
    await waitFor(() => expect(callsTo(fetchMock, "POST", "/members/m-1:end")).toHaveLength(1));
    expect(bodyOf(callsTo(fetchMock, "POST", "/members/m-1:end")[0] as never)).toEqual({ validTo: "2026-06-30" });

    await user.click(screen.getByRole("button", { name: /Remove user 0b6f6c64 from MRP/ }));
    await user.click(screen.getByRole("button", { name: /Confirm removal of user/ }));
    await waitFor(() => expect(callsTo(fetchMock, "DELETE", "/members/m-1")).toHaveLength(1));
  });

  it("shows a member refusal and no write forms to a reader", async () => {
    serve({ ...routes, "POST /teams/t-1/members": () => problem("ORG-TEAM-MEMBER-DUPLICATE", 409) });
    const first = renderPage("/org/teams", TeamsPage, ADMIN);
    await findTable({ name: "Teams" });
    await first.user.click(screen.getByRole("button", { name: "Show members of MRP" }));
    await first.user.type(await screen.findByLabelText("Member id"), userId);
    await first.user.click(screen.getByRole("button", { name: "Add member" }));
    expect(await screen.findByText(/already in the team/)).toBeInTheDocument();
    first.unmount();

    const reader = renderPage("/org/teams", TeamsPage, ["org.read"]);
    await findTable({ name: "Teams" });
    await reader.user.click(screen.getByRole("button", { name: "Show members of MRP" }));
    await findTable({ name: "Members of MRP" });
    expect(screen.queryByRole("button", { name: "Add member" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add team type" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Save team" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Remove user/ })).toBeNull();
  });
});

describe("teams review fixes", () => {
  const ended = { ...member, id: "m-3", validTo: "2026-12-31" };
  const ruled = { ...member, id: "m-4", memberId: "7c1d2e3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f", memberKind: "party", source: "rule" };
  const routes = {
    ...entities,
    "GET /team-types": () => json({ items: [teamType] }),
    "GET /teams/t-1/members": () => json({ items: [ended, ruled] }),
    "GET /teams": () => json({ items: [{ ...team, effectiveTo: "2027-12-31" }] }),
  };

  it("refuses impossible dates before calling the service", () => {
    expect(validateMember({ memberKind: "user", memberId: userId, memberRole: "member", validFrom: "2026-02-30", validTo: "" }).validFrom).toBeDefined();
    expect(parsePeriods("2026-02-01 2026-02-30").error).toMatch(/Line 1/);
  });

  it("clears the effective-to date with an empty value", async () => {
    const fetchMock = serve({ ...routes, "PATCH /teams/t-1": () => json({ ...team, version: 5 }) });
    const { user } = renderPage("/org/teams", TeamsPage, ADMIN);
    await findTable({ name: "Teams" });
    await user.click(screen.getByRole("button", { name: "Show members of MRP" }));
    await user.clear(await screen.findByLabelText("Effective to", { exact: false }));
    await user.click(screen.getByRole("button", { name: "Save team" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/teams/t-1")).toHaveLength(1));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/teams/t-1")[0] as never)).toEqual({ version: 4, effectiveTo: "" });
  });

  it("moves an end date only earlier and offers no removal of a rule-sourced member", async () => {
    const fetchMock = serve({ ...routes, "POST /teams/t-1/members/m-3:end": () => json({ ...ended, validTo: "2026-09-30" }) });
    const { user } = renderPage("/org/teams", TeamsPage, ADMIN);
    await findTable({ name: "Teams" });
    await user.click(screen.getByRole("button", { name: "Show members of MRP" }));
    await findTable({ name: "Members of MRP" });
    expect(screen.queryByRole("button", { name: /Remove party 7c1d2e3f/ })).toBeNull();
    expect(screen.getByRole("button", { name: /Remove user 0b6f6c64/ })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /End membership of user 0b6f6c64/ }));
    const date = screen.getByLabelText(/End date for user/);
    expect(date).toHaveValue("2026-12-31");
    await user.clear(date);
    await user.type(date, "2027-01-31");
    await user.click(screen.getByRole("button", { name: /Confirm end of user/ }));
    expect(await screen.findByText(/can only move earlier/)).toBeInTheDocument();
    expect(callsTo(fetchMock, "POST", ":end")).toHaveLength(0);
    await user.clear(date);
    await user.type(date, "2026-09-30");
    await user.click(screen.getByRole("button", { name: /Confirm end of user/ }));
    await waitFor(() => expect(callsTo(fetchMock, "POST", "/members/m-3:end")).toHaveLength(1));
  });

  it("shows no team or calendar writes without tenant.administer, which the service requires", async () => {
    serve({ ...routes, "GET /fiscal-calendars": () => json({ items: [] }) });
    const teams = renderPage("/org/teams", TeamsPage, ["org.read", "org.operating-unit.write", "org.fiscal-calendar.write"]);
    await findTable({ name: "Teams" });
    expect(screen.queryByRole("button", { name: "Add team type" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Deactivate/ })).toBeNull();
    teams.unmount();
    renderPage("/org/fiscal-calendars", FiscalCalendarsPage, ["org.read", "org.operating-unit.write", "org.fiscal-calendar.write"]);
    await screen.findByText("No fiscal calendars", { exact: false });
    expect(screen.queryByRole("button", { name: /Create|Add/ })).toBeNull();
  });
});

const calendar = { id: "cal-1", code: "FY", name: "Fiscal year", pattern: "calendar", startMonth: 1, weekStart: "monday", openingClosing: false, specialPeriods: 0, status: "active", version: 3, generatedYears: [2026] };
const custom = { ...calendar, id: "cal-2", code: "CUS", name: "Custom", pattern: "custom", generatedYears: [] };

describe("fiscal calendar writes", () => {
  it("renames a calendar with its version and toggles its status", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /fiscal-calendars": () => json({ items: [calendar] }),
      "PATCH /fiscal-calendars/cal-1": (init) => json({ ...calendar, ...JSON.parse(String(init?.body)), version: 4 }),
    });
    const { user } = renderPage("/org/fiscal-calendars", FiscalCalendarsPage, ADMIN);
    await findTable({ name: "Fiscal calendars" });
    await user.click(screen.getByRole("button", { name: "Open periods of FY" }));
    const name = await screen.findByLabelText("Calendar name");
    await user.clear(name);
    await user.type(name, "Fiscal 2026");
    await user.click(screen.getByRole("button", { name: "Save name" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/fiscal-calendars/cal-1")).toHaveLength(1));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/fiscal-calendars/cal-1")[0] as never)).toEqual({ version: 3, name: "Fiscal 2026" });
    expect(await screen.findByRole("heading", { name: "FY - Fiscal 2026" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Deactivate fiscal calendar FY" }));
    await user.click(screen.getByRole("button", { name: "Confirm deactivate fiscal calendar FY" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/fiscal-calendars/cal-1")).toHaveLength(2));
    expect(bodyOf(callsTo(fetchMock, "PATCH", "/fiscal-calendars/cal-1")[1] as never)).toEqual({ version: 4, status: "inactive" });
  });

  it("generates a custom-pattern year from entered periods", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /fiscal-calendars": () => json({ items: [custom] }),
      "POST /fiscal-calendars/cal-2:generate": () => json({ calendarId: "cal-2", fiscalYear: 2027, generated: true, periods: [] }, 201),
    });
    const { user } = renderPage("/org/fiscal-calendars", FiscalCalendarsPage, ADMIN);
    await findTable({ name: "Fiscal calendars" });
    await user.click(screen.getByRole("button", { name: "Open periods of CUS" }));
    await user.clear(await screen.findByLabelText("Generate fiscal year"));
    await user.type(screen.getByLabelText("Generate fiscal year"), "2027");
    await user.click(screen.getByRole("button", { name: "Generate year" }));
    expect(await screen.findByText(/needs its periods/)).toBeInTheDocument();
    expect(callsTo(fetchMock, "POST", ":generate")).toHaveLength(0);
    await user.type(screen.getByLabelText("Periods"), "2027-01-01 2027-06-30{enter}2027-07-01 2027-12-31");
    await user.click(screen.getByRole("button", { name: "Generate year" }));
    await waitFor(() => expect(callsTo(fetchMock, "POST", ":generate")).toHaveLength(1));
    expect(bodyOf(callsTo(fetchMock, "POST", ":generate")[0] as never)).toEqual({
      fiscalYear: 2027,
      periods: [
        { start: "2027-01-01", end: "2027-06-30" },
        { start: "2027-07-01", end: "2027-12-31" },
      ],
    });
  });

  it("regenerates an existing year with force and shows the lock refusal", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /fiscal-calendars": () => json({ items: [calendar] }),
      "GET /fiscal-calendars/cal-1/periods": () => json({ items: [] }),
      "POST /fiscal-calendars/cal-1:generate": () => problem("ORG-CAL-LOCKED", 409, "Fiscal year 2026 has postings"),
    });
    const { user } = renderPage("/org/fiscal-calendars", FiscalCalendarsPage, ADMIN);
    await findTable({ name: "Fiscal calendars" });
    await user.click(screen.getByRole("button", { name: "Open periods of FY" }));
    await user.click(await screen.findByLabelText(/Regenerate this year/));
    await user.click(screen.getByRole("button", { name: "Regenerate year" }));
    expect(await screen.findByText(/ORG-CAL-LOCKED/)).toBeInTheDocument();
    expect(bodyOf(callsTo(fetchMock, "POST", ":generate")[0] as never)).toEqual({ fiscalYear: 2026, force: true });
  });
});

describe("legal-entity switcher refresh", () => {
  it("lists a new entity in the switcher and drops a deactivated one without a reload", async () => {
    const second = { ...entity, id: "le-2", code: "DEMF", name: "Contoso DE", version: 1 };
    let created = false;
    let deactivated = false;
    serve({
      "GET /legal-entities": () => json({ items: [entity, ...(created ? [second] : [])].map((row) => (deactivated && row.id === "le-1" ? { ...row, status: "inactive" } : row)) }),
      "POST /legal-entities": () => {
        created = true;
        return json(second, 201);
      },
      "PATCH /legal-entities/le-1": () => {
        deactivated = true;
        return json({ ...entity, status: "inactive", version: 3 });
      },
      "GET /countries": () => json({ items: [{ code: "DE", name: "Germany", status: "active" }] }),
      "GET /currencies": () => json({ items: [{ code: "EUR", name: "Euro", status: "active" }] }),
    });
    const { user } = renderPage("/org/legal-entities", LegalEntitiesPage, ADMIN);
    const switcher = await screen.findByLabelText("Legal entity");
    await waitFor(() => expect(within(switcher).getByRole("option", { name: "USMF - Contoso US" })).toBeInTheDocument());
    expect(within(switcher).queryByRole("option", { name: "DEMF - Contoso DE" })).toBeNull();

    await user.type(screen.getByLabelText("Code"), "demf");
    await user.type(screen.getByLabelText("Name"), "Contoso DE");
    await user.selectOptions(await screen.findByLabelText("Country"), "DE");
    await user.selectOptions(screen.getByLabelText("Accounting currency"), "EUR");
    await user.click(screen.getByRole("button", { name: "Add legal entity" }));
    expect(await within(switcher).findByRole("option", { name: "DEMF - Contoso DE" })).toBeInTheDocument();
    expect(switcher).toHaveValue("le-1");

    await user.click(await screen.findByRole("button", { name: "Deactivate legal entity USMF" }));
    await user.click(screen.getByRole("button", { name: "Confirm deactivate legal entity USMF" }));
    await waitFor(() => expect(within(switcher).queryByRole("option", { name: "USMF - Contoso US" })).toBeNull());
    expect(switcher).toHaveValue("le-2");
  });
});

const warehouse = { id: "w-1", legalEntityId: "le-1", siteId: "s-1", code: "WH1", name: "Main", type: "standard", timeZone: "UTC", status: "active", version: 5 };
const location = { id: "l-1", legalEntityId: "le-1", warehouseId: "w-1", code: "A-01", name: "Aisle 1", type: "storage", status: "active", version: 2 };

describe("locations page", () => {
  it("lists locations with their warehouse and offers no writes to a reader", async () => {
    serve({ ...entities, "GET /warehouses": () => json({ items: [warehouse] }), "GET /locations": () => json({ items: [location] }) });
    renderPage("/org/locations", LocationsPage, ["org.read"]);
    const table = await findTable({ name: "Locations" });
    await waitFor(() => expect(within(table).getByText("WH1")).toBeInTheDocument());
    expect(within(table).getByText("Aisle 1")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add location" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Deactivate/ })).toBeNull();
  });

  it("shows the empty and error states", async () => {
    serve({ ...entities, "GET /locations": () => json({ items: [] }) });
    const first = renderPage("/org/locations", LocationsPage, ["org.read"]);
    expect(await screen.findByRole("heading", { name: "No locations" })).toBeInTheDocument();
    first.unmount();
    serve({ ...entities, "GET /locations": () => problem("ORG-X-BOOM", 500, "it broke") });
    renderPage("/org/locations", LocationsPage, ["org.read"]);
    expect((await screen.findAllByText(/ORG-X-BOOM/)).length).toBeGreaterThan(0);
  });

  it("creates a location under the warehouse's legal entity and deactivates under it", async () => {
    const fetchMock = serve({
      ...entities,
      "GET /warehouses": () => json({ items: [warehouse] }),
      "GET /locations": () => json({ items: [location] }),
      "POST /locations": () => json({ ...location, id: "l-2", code: "B-01" }, 201),
      "PATCH /locations/l-1": () => json({ ...location, status: "inactive", version: 3 }),
    });
    const { user } = renderPage("/org/locations", LocationsPage, ["org.read", "org.site.write"]);
    await findTable({ name: "Locations" });
    await user.click(screen.getByRole("button", { name: "Add location" }));
    expect(await screen.findByText("Select a warehouse.")).toBeInTheDocument();
    await within(await screen.findByLabelText("Warehouse")).findByRole("option", { name: "WH1 - Main" });
    await user.selectOptions(screen.getByLabelText("Warehouse"), "w-1");
    await user.type(screen.getByLabelText("Code"), "b-01");
    await user.type(screen.getByLabelText("Name"), "Aisle 2");
    await user.selectOptions(screen.getByLabelText("Type"), "receiving");
    await user.click(screen.getByRole("button", { name: "Add location" }));
    await waitFor(() => expect(callsTo(fetchMock, "POST", "/locations")).toHaveLength(1));
    const [, init] = callsTo(fetchMock, "POST", "/locations")[0];
    expect(JSON.parse(String(init?.body))).toEqual({ warehouseId: "w-1", code: "B-01", name: "Aisle 2", type: "receiving" });
    expect(new Headers(init?.headers).get("X-Passage-LegalEntity")).toBe("le-1");
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeTruthy();

    await user.click(await screen.findByRole("button", { name: "Deactivate location A-01" }));
    await user.click(screen.getByRole("button", { name: "Confirm deactivate location A-01" }));
    await waitFor(() => expect(callsTo(fetchMock, "PATCH", "/locations/l-1")).toHaveLength(1));
    const [, patch] = callsTo(fetchMock, "PATCH", "/locations/l-1")[0];
    expect(JSON.parse(String(patch?.body))).toEqual({ version: 2, status: "inactive" });
    expect(new Headers(patch?.headers).get("X-Passage-LegalEntity")).toBe("le-1");
  });

  it("shows a duplicate code on the field", async () => {
    serve({
      ...entities,
      "GET /warehouses": () => json({ items: [warehouse] }),
      "POST /locations": () => problem("ORG-LOC-CODE-DUPLICATE", 409),
    });
    const { user } = renderPage("/org/locations", LocationsPage, ["org.read", "org.site.write"]);
    await within(await screen.findByLabelText("Warehouse")).findByRole("option", { name: "WH1 - Main" });
    await user.selectOptions(screen.getByLabelText("Warehouse"), "w-1");
    await user.type(screen.getByLabelText("Code"), "a-01");
    await user.type(screen.getByLabelText("Name"), "Aisle");
    await user.click(screen.getByRole("button", { name: "Add location" }));
    expect(await screen.findByText("A location with this code already exists in the warehouse.")).toBeInTheDocument();
  });
});
