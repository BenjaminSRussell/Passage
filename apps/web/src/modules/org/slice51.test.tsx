import { findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement, type ComponentType } from "react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FiscalCalendarsPage } from "./FiscalCalendarsPage";
import { OrgLayout } from "./pages";
import { periodScheduleCsv, periodScheduleFilename } from "./periodSchedule";
import { OperatingUnitsPage } from "./StructurePages";
import { stubLocalStorage } from "./testStorage";
import { parentCell, loadUnitParents } from "./unitParents";
import { todayIso } from "./hierarchyTree";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (code: string, status: number) => json({ code, detail: "failed", title: "failed", status }, status);
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

function renderPage(path: string, Page: ComponentType, duties: string[]) {
  const router = createMemoryRouter([{ element: createElement(OrgLayout, { duties }), children: [{ path, element: createElement(Page) }] }], { initialEntries: [path] });
  return { user: userEvent.setup(), ...render(<RouterProvider router={router} />) };
}

beforeEach(() => void stubLocalStorage());
afterEach(() => vi.unstubAllGlobals());

const entity = { id: "le-1", code: "USMF", name: "Contoso US", country: "US", currency: "USD", timeZone: "UTC", status: "active", version: 2 };
const entities = { "GET /legal-entities": () => json({ items: [entity] }) };
const finance = { id: "ou-1", type: "department", code: "FIN", name: "Finance", defaultLegalEntityId: "le-1", status: "active", version: 1 };
const assembly = { id: "ou-2", type: "cost_center", code: "CC100", name: "Assembly", status: "active", version: 1 };
const loose = { id: "ou-3", type: "cost_center", code: "CC200", name: "Unplaced", status: "active", version: 1 };
const hierarchyType = { id: "ht-1", code: "REPORTING", name: "Reporting", purposes: ["reporting"], backdateAllowed: false, status: "active" };
const inForce = { id: "hv-2", hierarchyTypeId: "ht-1", versionNo: 2, status: "published", effectiveFrom: "2020-01-01", effectiveTo: null, nodeCount: 3 };
const nodes = [
  { orgUnitId: "le-1", kind: "legal_entity", parentOrgUnitId: null, sortOrder: 0 },
  { orgUnitId: "ou-1", kind: "operating_unit", parentOrgUnitId: "le-1", sortOrder: 1 },
  { orgUnitId: "ou-2", kind: "operating_unit", parentOrgUnitId: "ou-1", sortOrder: 2 },
];
const units = { "GET /operating-units": () => json({ items: [finance, assembly, loose] }), "GET /hierarchy-types": () => json({ items: [hierarchyType] }) };

describe("operating units Parent column", () => {
  it("shows a dash until a hierarchy is chosen, then the parent from the version in force", async () => {
    const fetchMock = serve({
      ...entities,
      ...units,
      "GET /hierarchy-types/ht-1/versions": () => json({ items: [{ ...inForce, id: "hv-1", versionNo: 1, effectiveTo: "2019-12-31" }, inForce] }),
      "GET /hierarchy-versions/hv-2": () => json({ ...inForce, version: 3, nodes }),
    });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, ["org.read"]);
    const table = await findTable({ name: "Operating units" });
    expect(within(table).getByRole("columnheader", { name: "Parent" })).toBeInTheDocument();
    const rowOf = (name: string) => within(within(table).getByText(name).closest("tr") as HTMLElement);
    expect(rowOf("Assembly").getAllByRole("cell").at(-2)).toHaveTextContent("—");
    const picker = await screen.findByLabelText("Parent from hierarchy");
    await waitFor(() => expect(within(picker).getByRole("option", { name: "REPORTING - Reporting" })).toBeInTheDocument());
    await user.selectOptions(picker, "ht-1");
    await waitFor(() => expect(rowOf("Assembly").getByText("FIN")).toBeInTheDocument());
    expect(rowOf("Finance").getAllByText("USMF")).toHaveLength(2); // default legal entity and parent
    expect(rowOf("Unplaced").queryByText("FIN")).toBeNull();
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/hierarchy-versions/hv-2"))).toBe(true);
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/hierarchy-versions/hv-1"))).toBe(false);
  });

  it("says when the hierarchy has no version in force today", async () => {
    serve({ ...entities, ...units, "GET /hierarchy-types/ht-1/versions": () => json({ items: [{ ...inForce, effectiveFrom: "2999-01-01" }] }) });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, ["org.read"]);
    await findTable({ name: "Operating units" });
    const picker = await screen.findByLabelText("Parent from hierarchy");
    await waitFor(() => expect(within(picker).getByRole("option", { name: "REPORTING - Reporting" })).toBeInTheDocument());
    await user.selectOptions(picker, "ht-1");
    expect(await screen.findByText("No version in force today")).toBeInTheDocument();
  });

  it("reports a failed hierarchy read and a failed type list without hiding the units", async () => {
    serve({ ...entities, ...units, "GET /hierarchy-types/ht-1/versions": () => problem("ORG-X-BOOM", 500) });
    const { user } = renderPage("/org/operating-units", OperatingUnitsPage, ["org.read"]);
    const table = await findTable({ name: "Operating units" });
    const picker = await screen.findByLabelText("Parent from hierarchy");
    await waitFor(() => expect(within(picker).getByRole("option", { name: "REPORTING - Reporting" })).toBeInTheDocument());
    await user.selectOptions(picker, "ht-1");
    expect((await screen.findAllByText(/ORG-X-BOOM/)).length).toBeGreaterThan(0);
    expect(within(table).getByText("Finance")).toBeInTheDocument();
  });

  it("reports a failed type list", async () => {
    serve({ ...entities, ...units, "GET /hierarchy-types": () => problem("ORG-X-TYPES", 500) });
    renderPage("/org/operating-units", OperatingUnitsPage, ["org.read"]);
    expect((await screen.findAllByText(/ORG-X-TYPES/)).length).toBeGreaterThan(0);
  });
});

describe("parentCell and loadUnitParents", () => {
  const ready = { status: "ready" as const, versionNo: 2, parentOf: new Map<string, string | null>([["a", null], ["b", "a"], ["c", "gone"]]) };
  const codeOf = (id: string) => ({ a: "ROOT" })[id as "a"];

  it("names the parent, marks a root and dashes the rest", () => {
    expect(parentCell(ready, "b", codeOf)).toBe("ROOT");
    expect(parentCell(ready, "a", codeOf)).toBe("Top level");
    expect(parentCell(ready, "z", codeOf)).toBe("—");
    expect(parentCell(ready, "c", codeOf)).toBe("—");
    expect(parentCell({ status: "idle" }, "b", codeOf)).toBe("—");
    expect(parentCell({ status: "loading" }, "b", codeOf)).toBe("—");
  });

  it("picks the version in force on the date, not a later or closed one", async () => {
    const fetchMock = serve({
      "GET /hierarchy-types/ht-1/versions": () =>
        json({ items: [{ ...inForce, id: "old", versionNo: 1, effectiveTo: "2019-12-31" }, inForce, { ...inForce, id: "future", versionNo: 3, effectiveFrom: "2999-01-01" }] }),
      "GET /hierarchy-versions/hv-2": () => json({ ...inForce, version: 1, nodes }),
    });
    const result = await loadUnitParents("ht-1", todayIso());
    expect(result.versionNo).toBe(2);
    expect(result.parentOf.get("ou-2")).toBe("ou-1");
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes("/hierarchy-versions/")).length).toBe(1);
  });
});

describe("fiscal period schedule export", () => {
  const calendar = { id: "cal-1", code: "FY", name: "Fiscal year", pattern: "4-4-5", startMonth: 1, status: "active", version: 1, generatedYears: [2026] };
  const period = { fiscalYear: 2026, periodNo: 1, name: "P1", startDate: "2026-01-01", endDate: "2026-02-04", type: "operating", week53: false };
  const CAL_READ = ["org.read"];

  it("writes one row per period with a header and quotes what needs it", () => {
    const csv = periodScheduleCsv({ code: "FY", pattern: "4-4-5" }, [
      period as never,
      { ...period, periodNo: 2, name: '=SUM(A1), "x"', startDate: "2026-02-05", endDate: "2026-03-04", week53: true } as never,
    ]);
    expect(csv.split("\r\n")).toEqual([
      "Calendar,Pattern,Fiscal year,Period,Name,Type,Start,End,53rd week",
      "FY,4-4-5,2026,1,P1,operating,2026-01-01,2026-02-04,No",
      `FY,4-4-5,2026,2,"'=SUM(A1), ""x""",operating,2026-02-05,2026-03-04,Yes`,
    ]);
    expect(periodScheduleFilename({ code: "a/b c" }, 2026)).toBe("fiscal-periods-a_b_c-2026.csv");
  });

  it("downloads the displayed year's schedule and is disabled while no period is shown", async () => {
    serve({ "GET /fiscal-calendars/cal-1/periods": () => json({ items: [period] }), "GET /fiscal-calendars": () => json({ items: [calendar] }) });
    const blobs: Blob[] = [];
    vi.stubGlobal("URL", { createObjectURL: (blob: Blob) => (blobs.push(blob), "blob:test"), revokeObjectURL: () => undefined });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    const { user } = renderPage("/org/fiscal-calendars", FiscalCalendarsPage, CAL_READ);
    await findTable({ name: "Fiscal calendars" });
    await user.click(screen.getByRole("button", { name: "Open periods of FY" }));
    await findTable({ name: "Periods of FY" });
    await user.click(screen.getByRole("button", { name: "Export schedule (CSV)" }));
    expect(click).toHaveBeenCalledTimes(1);
    expect(await blobs[0].text()).toContain("FY,4-4-5,2026,1,P1,operating,2026-01-01,2026-02-04,No");
    click.mockRestore();
  });

  it("cannot export a year with no periods", async () => {
    serve({ "GET /fiscal-calendars": () => json({ items: [{ ...calendar, generatedYears: [] }] }) });
    const { user } = renderPage("/org/fiscal-calendars", FiscalCalendarsPage, CAL_READ);
    await findTable({ name: "Fiscal calendars" });
    await user.click(screen.getByRole("button", { name: "Open periods of FY" }));
    expect(await screen.findByRole("button", { name: "Export schedule (CSV)" })).toBeDisabled();
  });
});
