import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CalendarDetailPage, CalendarsPage, CapabilityDetailPage, ResourceGroupDetailPage, templateRows } from "./detail";
import { getCalendar, getCapability, getResourceGroup, listCalendars } from "./api/client";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });
const problem = (status: number, code: string, title: string) => ({ ok: false, status, json: async () => ({ code, title }) });

function stub(handler: (url: string) => unknown) {
  const fetchMock = vi.fn(async (url: string) => handler(url));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const renderInRouter = (ui: React.ReactElement) => render(<MemoryRouter>{ui}</MemoryRouter>);
const shift = { start: "06:00", end: "14:00" };
const calendar = (extra: object = {}) => ({
  id: "cal/1",
  code: "DAY",
  name: "Day shift",
  template: [[shift], [shift], [shift], [shift], [shift], [], []],
  closedDates: ["2026-12-25"],
  version: 3,
  generatedFrom: "2026-01-01",
  generatedTo: "2026-12-31",
  ...extra,
});

describe("engineering detail client calls", () => {
  it("encodes ids and cursor", async () => {
    const fetchMock = stub(() => ok({ items: [] }));
    await getResourceGroup("g/1");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resource-groups/g%2F1");
    await getCapability("c/1");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/capabilities/c%2F1");
    await getCalendar("k/1");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/calendars/k%2F1");
    await listCalendars("c9");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/calendars?cursor=c9");
    await listCalendars();
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/calendars");
  });
});

describe("ResourceGroupDetailPage", () => {
  it("shows facts and links to members and the calendar", async () => {
    stub(() => ok({ id: "g/1", code: "GRP-1", name: "Lathes", siteId: "S1", calendarId: "cal-1" }));
    renderInRouter(<ResourceGroupDetailPage id="g/1" />);
    expect(screen.getByText("Resource group")).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Resource group GRP-1" })).toBeInTheDocument();
    expect(screen.getByText("Lathes")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Resources in this group" })).toHaveAttribute("href", "/engineering/resources?groupId=g%2F1");
    expect(screen.getByRole("link", { name: "Calendar days" })).toHaveAttribute("href", "/engineering/calendars/cal-1/days");
  });

  it("shows not-found without Retry, and other errors with Retry that refetches", async () => {
    stub(() => problem(404, "ENG-RES-NOTFOUND", "resource group not found"));
    const first = renderInRouter(<ResourceGroupDetailPage id="x" />);
    expect(await findAlert()).toHaveTextContent("This resource group does not exist");
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    first.unmount();
    let calls = 0;
    stub(() => (++calls === 1 ? problem(500, "X", "Boom") : ok({ id: "g", code: "GRP-2", name: "N", siteId: "S" })));
    renderInRouter(<ResourceGroupDetailPage id="g" onBack={() => undefined} />);
    expect(await findAlert()).toHaveTextContent("Boom");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("heading", { name: "Resource group GRP-2" })).toBeInTheDocument();
  });

  it("calls onBack", async () => {
    stub(() => ok({ id: "g", code: "G", name: "N", siteId: "S" }));
    const back = vi.fn();
    renderInRouter(<ResourceGroupDetailPage id="g" onBack={back} />);
    fireEvent.click(await screen.findByRole("button", { name: "Back to resource groups" }));
    expect(back).toHaveBeenCalled();
  });
});

describe("CapabilityDetailPage", () => {
  it("shows facts and the holders link", async () => {
    stub(() => ok({ id: "c/1", code: "CNC", name: "Milling" }));
    renderInRouter(<CapabilityDetailPage id="c/1" />);
    expect(await screen.findByRole("heading", { name: "Capability CNC" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Resources holding this capability" })).toHaveAttribute("href", "/engineering/resources?capabilityId=c%2F1");
  });

  it("shows not-found", async () => {
    stub(() => problem(404, "ENG-RES-NOTFOUND", "capability not found"));
    renderInRouter(<CapabilityDetailPage id="x" />);
    expect(await findAlert()).toHaveTextContent("This capability does not exist");
  });
});

describe("CalendarDetailPage", () => {
  it("shows facts, the weekly template and the days link", async () => {
    stub(() => ok(calendar()));
    renderInRouter(<CalendarDetailPage id="cal/1" />);
    expect(await screen.findByRole("heading", { name: "Calendar DAY" })).toBeInTheDocument();
    expect(screen.getByText("2026-01-01 to 2026-12-31")).toBeInTheDocument();
    expect(screen.getByText("2026-12-25")).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Weekly template" });
    expect(table).toHaveTextContent("Monday06:00-14:008");
    expect(table).toHaveTextContent("SaturdayClosed0");
    expect(screen.getByRole("link", { name: "Calendar days" })).toHaveAttribute("href", "/engineering/calendars/cal%2F1/days");
  });

  it("says so when nothing is generated and no dates are closed", async () => {
    stub(() => ok(calendar({ generatedFrom: undefined, generatedTo: undefined, closedDates: [] })));
    renderInRouter(<CalendarDetailPage id="c" />);
    expect(await screen.findByText("Not generated yet")).toBeInTheDocument();
    expect(screen.getByText("None")).toBeInTheDocument();
  });

  it("shows the error with Retry", async () => {
    stub(() => problem(500, "X", "Calendar unavailable"));
    renderInRouter(<CalendarDetailPage id="c" />);
    expect(await findAlert()).toHaveTextContent("Calendar unavailable");
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });

  it("counts overnight shifts across midnight", () => {
    const rows = templateRows({ ...calendar(), template: [[{ start: "22:00", end: "06:00" }], [{ start: "08:00", end: "12:30" }]] });
    expect(rows[0]).toMatchObject({ shifts: "22:00-06:00", hours: "8" });
    expect(rows[1].hours).toBe("4.5");
    const efficient = templateRows({ ...calendar(), template: [[{ start: "06:00", end: "14:00", efficiencyPct: "85" }]] });
    expect(efficient[0]).toMatchObject({ shifts: "06:00-14:00 at 85%", hours: "8" });
    expect(rows[6].shifts).toBe("Closed");
  });
});

describe("CalendarsPage", () => {
  it("shows loading, rows with detail links and loads more", async () => {
    const fetchMock = stub((url) =>
      url.includes("cursor=c1")
        ? ok({ items: [calendar({ id: "k2", code: "NIGHT", generatedTo: undefined })] })
        : ok({ items: [calendar()], nextCursor: "c1" }),
    );
    renderInRouter(<CalendarsPage />);
    expect(screen.getByRole("table", { name: "Work calendars" })).toHaveAttribute("aria-busy", "true");
    expect(await screen.findByRole("link", { name: "DAY" })).toHaveAttribute("href", "/engineering/calendars/cal%2F1");
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    const table = await findTable({ name: "Work calendars" });
    await waitFor(() => expect(table).toHaveTextContent("NIGHT"));
    expect(table).toHaveTextContent("Not generated");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/calendars?cursor=c1");
  });

  it("shows the empty state and the error with Retry", async () => {
    let fail = true;
    stub(() => (fail ? problem(500, "X", "Calendars unavailable") : ok({ items: [] })));
    renderInRouter(<CalendarsPage />);
    expect(await findAlert()).toHaveTextContent("Calendars unavailable");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("No work calendars yet")).toBeInTheDocument();
  });
});
