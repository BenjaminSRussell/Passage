import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CalendarDaysPage, CapabilitiesPage, currentMonth, ResourceDetailPage, ResourceGroupsPage, ResourcesPage } from "./resources";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });
const problem = (status: number, code: string, title: string) => ({ ok: false, status, json: async () => ({ code, title }) });

function stub(handler: (url: string) => unknown) {
  const fetchMock = vi.fn(async (url: string) => handler(url));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const resource = (id: string, extra: object = {}) => ({
  id,
  code: `RES-${id}`,
  name: `Name ${id}`,
  type: "machine",
  siteId: "S1",
  exclusive: true,
  ...extra,
});

const renderInRouter = (ui: React.ReactElement) => render(<MemoryRouter>{ui}</MemoryRouter>);

describe("ResourcesPage", () => {
  it("shows loading, then rows, and opens a row", async () => {
    stub(() => ok({ items: [resource("a"), resource("b", { exclusive: false, groupId: "g1" })] }));
    const opened: string[] = [];
    renderInRouter(<ResourcesPage onOpen={(id) => opened.push(id)} />);
    expect(screen.getByRole("table", { name: "Resources" })).toHaveAttribute("aria-busy", "true");
    const table = await screen.findByRole("table", { name: "Resources" });
    await waitFor(() => expect(table).not.toHaveAttribute("aria-busy"));
    expect(table).toHaveTextContent("Name a");
    expect(table).toHaveTextContent("Yes");
    expect(table).toHaveTextContent("g1");
    fireEvent.click(screen.getByRole("button", { name: "RES-a" }));
    expect(opened).toEqual(["a"]);
  });

  it("shows the empty state", async () => {
    stub(() => ok({ items: [] }));
    renderInRouter(<ResourcesPage />);
    expect(await screen.findByText("No resources match")).toBeInTheDocument();
  });

  it("shows the problem title for a rejected filter and retries", async () => {
    let calls = 0;
    const fetchMock = stub(() => (++calls === 1 ? problem(422, "ENG-INVALID-QUERY", "groupId must be a UUID") : ok({ items: [resource("a")] })));
    renderInRouter(<ResourcesPage initial={{ groupId: "nope" }} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("groupId must be a UUID");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resources?groupId=nope");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByText("RES-a");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("refetches with type on change and with text filters on apply", async () => {
    const fetchMock = stub(() => ok({ items: [resource("a")] }));
    renderInRouter(<ResourcesPage />);
    await screen.findByText("RES-a");
    fireEvent.change(screen.getByLabelText(/Type/), { target: { value: "tool" } });
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resources?type=tool"));
    fireEvent.change(screen.getByLabelText(/Capability/), { target: { value: "cap-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resources?type=tool&capabilityId=cap-1"));
  });

  it("appends the next page and hides Load more when nextCursor is absent", async () => {
    const fetchMock = stub((url) => (url.includes("cursor=c1") ? ok({ items: [resource("b")] }) : ok({ items: [resource("a")], nextCursor: "c1" })));
    renderInRouter(<ResourcesPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await screen.findByText("RES-b");
    expect(screen.getByText("RES-a")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resources?cursor=c1");
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });
});

describe("ResourceDetailPage", () => {
  it("shows header facts, capability levels and the group and calendar links", async () => {
    stub(() =>
      ok(
        resource("a", {
          groupId: "g/1",
          effectiveCalendarId: "cal 1",
          efficiencyPct: "85",
          capacityPerDayH: "16",
          vendorPartyId: "party-9",
          capabilities: [{ capabilityId: "cap-1", level: 3, effectiveFrom: "2026-01-01", effectiveTo: "2026-12-31" }],
        }),
      ),
    );
    renderInRouter(<ResourceDetailPage id="a" />);
    expect(await screen.findByRole("heading", { name: "Resource RES-a" })).toBeInTheDocument();
    expect(screen.getByText("party-9")).toBeInTheDocument();
    expect(screen.getByText("85")).toBeInTheDocument();
    expect(screen.getByText("16")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "g/1" })).toHaveAttribute("href", "/engineering/resources?groupId=g%2F1");
    expect(screen.getByRole("link", { name: "Calendar days" })).toHaveAttribute("href", "/engineering/calendars/cal%201");
    const table = screen.getByRole("table", { name: "Capabilities" });
    expect(table).toHaveTextContent("cap-1");
    expect(table).toHaveTextContent("3");
    expect(table).toHaveTextContent("2026-12-31");
  });

  it("shows no capabilities row content and no links when the optional fields are absent", async () => {
    stub(() => ok(resource("a")));
    renderInRouter(<ResourceDetailPage id="a" />);
    expect(await screen.findByText("No capabilities")).toBeInTheDocument();
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("shows a distinct not-found message without Retry", async () => {
    stub(() => problem(404, "ENG-RESOURCE-NOTFOUND", "Resource not found"));
    renderInRouter(<ResourceDetailPage id="x" />);
    expect(await screen.findByRole("alert")).toHaveTextContent("This resource does not exist");
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });

  it("shows the error message with Retry that refetches", async () => {
    let calls = 0;
    stub(() => (++calls === 1 ? problem(500, "ENG-INTERNAL", "Something broke") : ok(resource("a"))));
    renderInRouter(<ResourceDetailPage id="a" />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Something broke");
    expect(screen.getByRole("alert")).not.toHaveTextContent("does not exist");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("heading", { name: "Resource RES-a" })).toBeInTheDocument();
  });
});

describe("ResourceGroupsPage", () => {
  it("lists groups with links that seed the resources filter and a calendar link", async () => {
    stub(() => ok({ items: [{ id: "g/1", code: "GRP-1", name: "Lathes", siteId: "S1", calendarId: "cal-1" }] }));
    renderInRouter(<ResourceGroupsPage />);
    expect(screen.getByRole("table", { name: "Resource groups" })).toHaveAttribute("aria-busy", "true");
    expect(await screen.findByRole("link", { name: "GRP-1" })).toHaveAttribute("href", "/engineering/resources?groupId=g%2F1");
    expect(screen.getByRole("link", { name: "Calendar days" })).toHaveAttribute("href", "/engineering/calendars/cal-1");
  });

  it("filters by site, shows the empty state and the error with Retry", async () => {
    let fail = true;
    const fetchMock = stub(() => (fail ? problem(500, "ENG-INTERNAL", "Groups unavailable") : ok({ items: [] })));
    renderInRouter(<ResourceGroupsPage />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Groups unavailable");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("No resource groups match")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Site/), { target: { value: "S2" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resource-groups?siteId=S2"));
  });
});

describe("CapabilitiesPage", () => {
  it("lists capabilities with links that seed the resources filter and loads more", async () => {
    const fetchMock = stub((url) =>
      url.includes("cursor=c1")
        ? ok({ items: [{ id: "c2", code: "WELD", name: "Welding" }] })
        : ok({ items: [{ id: "c/1", code: "CNC", name: "Milling" }], nextCursor: "c1" }),
    );
    renderInRouter(<CapabilitiesPage />);
    expect(await screen.findByRole("link", { name: "CNC" })).toHaveAttribute("href", "/engineering/resources?capabilityId=c%2F1");
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByRole("link", { name: "WELD" })).toBeInTheDocument();
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/capabilities?cursor=c1");
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("shows the empty state and the error", async () => {
    let empty = false;
    stub(() => (empty ? ok({ items: [] }) : problem(500, "ENG-INTERNAL", "Capabilities unavailable")));
    renderInRouter(<CapabilitiesPage />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Capabilities unavailable");
    empty = true;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("No capabilities yet")).toBeInTheDocument();
  });
});

describe("CalendarDaysPage", () => {
  const range = { from: "2026-10-01", to: "2026-10-31" };

  it("defaults to the current month", () => {
    expect(currentMonth(new Date(2026, 1, 10))).toEqual({ from: "2026-02-01", to: "2026-02-28" });
  });

  it("shows open and closed days with shifts and hours", async () => {
    const fetchMock = stub(() =>
      ok({
        items: [
          { date: "2026-10-01", open: true, shifts: [{ start: "06:00", end: "14:00" }, { start: "14:00", end: "22:00" }], totalH: "16" },
          { date: "2026-10-03", open: false, shifts: [], totalH: "0" },
        ],
      }),
    );
    renderInRouter(<CalendarDaysPage id="cal/1" initialRange={range} />);
    const table = await screen.findByRole("table", { name: "Calendar days" });
    await waitFor(() => expect(table).not.toHaveAttribute("aria-busy"));
    expect(table).toHaveTextContent("Open06:00-14:00, 14:00-22:0016");
    expect(table).toHaveTextContent("2026-10-03Closed0");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/calendars/cal%2F1/days?from=2026-10-01&to=2026-10-31");
  });

  it("refetches when the range changes", async () => {
    const fetchMock = stub(() => ok({ items: [] }));
    renderInRouter(<CalendarDaysPage id="c1" initialRange={range} />);
    expect(await screen.findByText("No generated days in this range")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2026-11-15" } });
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/calendars/c1/days?from=2026-10-01&to=2026-11-15"));
    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2026-10-10" } });
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/calendars/c1/days?from=2026-10-10&to=2026-11-15"));
  });

  it("shows the error with Retry", async () => {
    stub(() => problem(404, "ENG-CALENDAR-NOTFOUND", "Calendar not found"));
    renderInRouter(<CalendarDaysPage id="c1" initialRange={range} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Calendar not found");
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});
