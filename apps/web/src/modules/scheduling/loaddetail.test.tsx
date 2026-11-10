import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { LoadJob, Resource } from "./api/client";
import { readRange } from "./CapacityLoadJobsPage";
import scheduling from "./module";

const MIX = "3f1c0000-0000-4000-8000-000000000001";
const ORDER = "b1000000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };
type Handler = (url: URL) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

let handler: Handler;
let calls: URL[];

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

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {scheduling.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const resource: Resource = {
  id: MIX,
  code: "MIX-1",
  name: "Mixing vessel",
  finite: true,
  timeZone: "UTC",
  shifts: [],
  version: 1,
  createdAt: "2026-10-01T00:00:00Z",
};

const job: LoadJob = {
  jobId: "0a000000-0000-4000-8000-000000000001",
  orderId: ORDER,
  operNumber: 10,
  segment: 1,
  role: "primary",
  start: "2027-03-01T08:00:00Z",
  end: "2027-03-01T10:30:00Z",
  minutesInRange: 150,
  finite: true,
  firmed: true,
  lateMinutes: 45,
};

describe("Capacity load by day", () => {
  it("switches to days, asks the daily API and links each row to its jobs", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/capacity-load")) {
        return url.searchParams.get("period") === "day"
          ? { status: 200, body: { items: [{ resourceId: MIX, period: "day", periodStart: "2027-03-02", start: "2027-03-01T15:00:00Z", end: "2027-03-02T15:00:00Z", openMinutes: 480, bookedMinutes: 240, loadPct: 50, bottleneck: false }] } }
          : { status: 200, body: { items: [{ resourceId: MIX, period: "week", periodStart: "2027-03-01", weekStart: "2027-03-01", start: "2027-03-01T00:00:00Z", end: "2027-03-08T00:00:00Z", openMinutes: 2400, bookedMinutes: 2400, loadPct: 100, bottleneck: true }] } };
      }
      return { status: 200, body: { items: [resource] } };
    };
    const { container } = renderAt("/scheduling/load");
    let table = await findTable({ name: "Capacity load" });
    await waitFor(() => expect(table).toHaveTextContent("Bottleneck"));
    expect(screen.getByRole("link", { name: "Jobs of Mixing vessel (MIX-1) from 2027-03-01" })).toHaveAttribute(
      "href",
      `/scheduling/load/jobs?${new URLSearchParams({ resourceId: MIX, from: "2027-03-01T00:00:00Z", to: "2027-03-08T00:00:00Z" })}`,
    );

    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "day" } });
    await waitFor(() => expect(calls.some((c) => c.searchParams.get("period") === "day" && c.searchParams.get("days") === "14")).toBe(true));
    table = await findTable({ name: "Capacity load" });
    await waitFor(() => expect(table).toHaveTextContent("2027-03-02"));
    expect(table).toHaveTextContent("50.0");
    expect(table).not.toHaveTextContent("Bottleneck");
    expect(screen.getByLabelText("Days")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Jobs of Mixing vessel (MIX-1) from 2027-03-02" })).toHaveAttribute(
      "href",
      `/scheduling/load/jobs?${new URLSearchParams({ resourceId: MIX, from: "2027-03-01T15:00:00Z", to: "2027-03-02T15:00:00Z" })}`,
    );
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });
});

describe("Capacity load detail", () => {
  const path = `/scheduling/load/jobs?${new URLSearchParams({ resourceId: MIX, from: "2027-03-01T00:00:00Z", to: "2027-03-08T00:00:00Z" })}`;

  it("lists the booked jobs of the period for the resource", async () => {
    handler = (url) =>
      url.pathname.endsWith("/capacity-load/jobs")
        ? { status: 200, body: { items: [job, { ...job, jobId: "0a000000-0000-4000-8000-000000000002", role: "secondary", firmed: false, lateMinutes: 0, finite: false }], truncated: true } }
        : { status: 200, body: resource };
    const { container } = renderAt(path);
    const table = await findTable({ name: "Jobs booked in the period" });
    await waitFor(() => expect(table).toHaveTextContent("Firmed"));
    expect(screen.getByRole("heading", { name: "Jobs on MIX-1" })).toBeInTheDocument();
    expect(screen.getByText(/from 2027-03-01 00:00 UTC to 2027-03-08 00:00 UTC \(resource time zone UTC\)/)).toBeInTheDocument();
    expect(table).toHaveTextContent("2 h 30 min");
    expect(table).toHaveTextContent("Late 45 min");
    expect(table).toHaveTextContent("Secondary");
    expect(table).toHaveTextContent("Infinite run");
    expect(screen.getByText("Showing the first 1,000 jobs")).toBeInTheDocument();
    const asked = calls.find((c) => c.pathname.endsWith("/capacity-load/jobs"));
    expect(asked?.searchParams.get("resourceId")).toBe(MIX);
    expect(asked?.searchParams.get("from")).toBe("2027-03-01T00:00:00Z");
    expect(asked?.searchParams.get("to")).toBe("2027-03-08T00:00:00Z");
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("explains an empty period, a failure with retry, and an incomplete link", async () => {
    handler = (url) => (url.pathname.endsWith("/capacity-load/jobs") ? { status: 200, body: { items: [], truncated: false } } : { status: 200, body: resource });
    const first = renderAt(path);
    expect(await screen.findByText("Nothing is booked in this period")).toBeInTheDocument();
    first.unmount();

    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "internal error" } });
    const second = renderAt(path);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("COM-0500");
    handler = (url) => (url.pathname.endsWith("/capacity-load/jobs") ? { status: 200, body: { items: [job], truncated: false } } : { status: 200, body: resource });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Jobs booked in the period" })).toBeInTheDocument();
    second.unmount();

    renderAt("/scheduling/load/jobs?resourceId=nope");
    expect(await findAlert()).toHaveTextContent("This link is incomplete");
  });

  it("reads only usable ranges", () => {
    const range = (from: string, to: string, resourceId = MIX) => readRange(new URLSearchParams({ resourceId, from, to }));
    expect(range("2027-03-01T15:00:00Z", "2027-03-02T15:00:00Z")).toEqual({ resourceId: MIX, from: "2027-03-01T15:00:00Z", to: "2027-03-02T15:00:00Z" });
    expect(range("2027-03-01T00:00:00+09:00", "2027-03-02T00:00:00+09:00")).not.toBeNull();
    expect(range("2027-03-01T00:00:00Z", "2027-06-03T00:00:00Z")).toBeNull();
    expect(range("2027-03-02T00:00:00Z", "2027-03-01T00:00:00Z")).toBeNull();
    expect(range("2027-03-01", "2027-03-02")).toBeNull();
    expect(range("2027-13-45T00:00:00Z", "2027-13-46T00:00:00Z")).toBeNull();
    expect(range("2027-03-01T00:00:00Z", "2027-03-02T00:00:00Z", "nope")).toBeNull();
  });
});
