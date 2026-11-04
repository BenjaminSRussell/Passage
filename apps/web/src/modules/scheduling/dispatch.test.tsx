import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { DispatchLine, Resource } from "./api/client";
import { shiftsOn } from "./DispatchListPage";
import scheduling from "./module";

const MIX = "3f1c0000-0000-4000-8000-000000000001";
const ORDER = "b1000000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };
let handler: (url: URL) => Reply | undefined;
let calls: URL[];

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = new URL(input, "http://localhost");
      calls.push(url);
      const reply = handler(url) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/scheduling/dispatch-list"]}>
      <Routes>
        {scheduling.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

// 2027-03-01 is a Monday.
const resource: Resource = {
  id: MIX,
  code: "MIX-1",
  name: "Mixing vessel",
  finite: true,
  timeZone: "Asia/Tokyo",
  // The calendar keeps the night shift as two spans: Monday 22:00-24:00 and Tuesday 00:00-06:00.
  shifts: [
    { weekday: 1, startMinute: 1320, endMinute: 1440 },
    { weekday: 1, startMinute: 360, endMinute: 840 },
    { weekday: 2, startMinute: 0, endMinute: 360 },
  ],
  version: 1,
  createdAt: "2026-10-01T00:00:00Z",
};

const line: DispatchLine = {
  sequence: 1,
  jobId: "0a000000-0000-4000-8000-000000000001",
  orderId: ORDER,
  operNumber: 10,
  segment: 1,
  role: "primary",
  start: "2027-03-01T08:00:00Z",
  end: "2027-03-01T10:30:00Z",
  minutesInWindow: 150,
  setupGroup: "RED",
  changeoverMinutes: 20,
  finite: true,
  firmed: true,
  lateMinutes: 0,
};

function list(lines: DispatchLine[], extra: object = {}) {
  return {
    resource: { id: MIX, code: "MIX-1", name: "Mixing vessel", timeZone: "Asia/Tokyo" },
    date: "2027-03-01",
    days: 1,
    windowStart: "2027-02-28T15:00:00Z",
    windowEnd: "2027-03-01T15:00:00Z",
    totalMinutes: lines.reduce((n, l) => n + l.minutesInWindow, 0),
    lines,
    truncated: false,
    ...extra,
  };
}

describe("shift helpers", () => {
  it("picks the shifts that start on the weekday of the date and joins a night shift across midnight", () => {
    expect(shiftsOn(resource, "2027-03-01")).toEqual([
      { label: "Monday 06:00-14:00", startMinute: 360, endMinute: 840 },
      { label: "Monday 22:00 to Tuesday 06:00", startMinute: 1320, endMinute: 1800 },
    ]);
    expect(shiftsOn(resource, "2027-03-02")).toEqual([{ label: "Tuesday 00:00-06:00", startMinute: 0, endMinute: 360 }]);
    expect(shiftsOn(resource, "2027-03-03")).toEqual([]);
    expect(shiftsOn(resource, "not a date")).toEqual([]);
  });

  it("does not chain round-the-clock days into one window", () => {
    const allDay = { ...resource, shifts: [1, 2].map((weekday) => ({ weekday, startMinute: 0, endMinute: 1440 })) };
    expect(shiftsOn(allDay, "2027-03-01")).toEqual([{ label: "Monday 00:00-24:00", startMinute: 0, endMinute: 1440 }]);
    const lateAndAllDay = { ...resource, shifts: [{ weekday: 1, startMinute: 960, endMinute: 1440 }, { weekday: 2, startMinute: 0, endMinute: 1440 }] };
    expect(shiftsOn(lateAndAllDay, "2027-03-01")[0]?.endMinute).toBe(1440);
  });
});

describe("Dispatch list", () => {
  const reply = (url: URL): Reply | undefined =>
    url.pathname.endsWith("/dispatch-list") ? { status: 200, body: list([line, { ...line, sequence: 2, role: "secondary", firmed: false, setupGroup: undefined, changeoverMinutes: 0, lateMinutes: 30 }]) } : { status: 200, body: { items: [resource] } };

  it("loads the numbered jobs of the chosen resource and day, and asks for a shift window", async () => {
    handler = reply;
    const { container } = renderPage();
    fireEvent.change(await screen.findByLabelText("Date"), { target: { value: "2027-03-01" } });
    const table = await findTable({ name: "Dispatch list" });
    await waitFor(() => expect(table).toHaveTextContent("RED"));
    expect(table).toHaveTextContent("2 h 30 min");
    // 08:00 UTC is 17:00 on the resource's clock in Tokyo.
    expect(table).toHaveTextContent("Start (Asia/Tokyo)");
    expect(table).toHaveTextContent("2027-03-01 17:00");
    expect(table).toHaveTextContent("2027-03-01 19:30");
    expect(table).toHaveTextContent("Firmed");
    expect(table).toHaveTextContent("Late 30 min");
    expect(table).toHaveTextContent("Secondary");
    expect(screen.getByText(/MIX-1 - Mixing vessel, 2027-03-01: 2027-03-01 00:00 to 2027-03-02 00:00 \(Asia\/Tokyo; .*\), 2 lines, 5 h booked/)).toBeInTheDocument();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);

    fireEvent.change(screen.getByLabelText("Shift"), { target: { value: "1" } });
    await waitFor(() => {
      const asked = calls.filter((c) => c.pathname.endsWith("/dispatch-list")).at(-1);
      expect(asked?.searchParams.get("resourceId")).toBe(MIX);
      expect(asked?.searchParams.get("date")).toBe("2027-03-01");
      expect(asked?.searchParams.get("startMinute")).toBe("1320");
      expect(asked?.searchParams.get("endMinute")).toBe("1800");
    });
  });

  it("explains an empty window and a truncated list", async () => {
    handler = (url) => (url.pathname.endsWith("/dispatch-list") ? { status: 200, body: list([]) } : { status: 200, body: { items: [resource] } });
    const first = renderPage();
    expect(await screen.findByText("Nothing is booked")).toBeInTheDocument();
    first.unmount();

    handler = (url) => (url.pathname.endsWith("/dispatch-list") ? { status: 200, body: list([line], { truncated: true }) } : { status: 200, body: { items: [resource] } });
    renderPage();
    expect(await screen.findByText("Showing the first 1,000 lines")).toBeInTheDocument();
  });

  it("shows a failure with a way to retry, and a note when there are no resources", async () => {
    handler = (url) => (url.pathname.endsWith("/dispatch-list") ? { status: 500, body: { code: "COM-0500", detail: "internal error" } } : { status: 200, body: { items: [resource] } });
    const first = renderPage();
    expect(await findAlert()).toHaveTextContent("Could not load the dispatch list");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    first.unmount();

    handler = () => ({ status: 200, body: { items: [] } });
    renderPage();
    expect(await screen.findByText("No resources yet")).toBeInTheDocument();
  });
});
