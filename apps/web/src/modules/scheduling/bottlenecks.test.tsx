import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { BottleneckDetail, BottleneckEpisode } from "./api/client";
import scheduling from "./module";

const MIX = "3f1c0000-0000-4000-8000-000000000001";
const EP = "e1000000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

let handler: (url: URL, init?: RequestInit) => Reply | undefined;
let calls: { url: URL; method: string }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const url = new URL(input, "http://localhost");
      calls.push({ url, method: init?.method ?? "GET" });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
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

const open: BottleneckEpisode = {
  id: EP,
  resourceId: MIX,
  resourceCode: "MIX-1",
  firstWeek: "2027-01-04",
  flaggedWeeks: 2,
  peakLoadPct: 100,
  thresholdPct: 90,
  consecutiveWeeks: 3,
  detectedAt: "2027-01-02T09:30:00Z",
  closedAt: null,
  currentFlaggedWeeks: 3,
  currentPeakLoadPct: 104.5,
};

describe("Bottlenecks list", () => {
  it("lists open episodes with their current figures, asks for the chosen status and links to the detail", async () => {
    handler = (url) =>
      url.pathname.endsWith("/bottlenecks")
        ? { status: 200, body: { items: url.searchParams.get("status") === "closed" ? [{ ...open, closedAt: "2027-02-01T00:00:00Z", currentFlaggedWeeks: 0, currentPeakLoadPct: 0 }] : [open] } }
        : undefined;
    const { container } = renderAt("/scheduling/bottlenecks");
    const table = await findTable({ name: "Bottlenecks" });
    await waitFor(() => expect(table).toHaveTextContent("MIX-1"));
    expect(calls[0]?.url.searchParams.get("status")).toBe("open");
    expect(table).toHaveTextContent("104.5");
    expect(table).toHaveTextContent("above 90 % for 3 weeks");
    expect(screen.getByRole("link", { name: "Bottleneck of MIX-1 from 2027-01-04" })).toHaveAttribute("href", `/scheduling/bottlenecks/${EP}`);
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);

    fireEvent.change(screen.getByLabelText("Show"), { target: { value: "closed" } });
    await waitFor(() => expect(table).toHaveTextContent("2 when detected"));
    expect(table).toHaveTextContent("Closed 2027-02-01");
  });

  it("shows an empty state, then the problem when the list cannot be loaded", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/scheduling/bottlenecks");
    expect(await screen.findByText("No bottlenecks")).toBeInTheDocument();
  });

  it("reports a failed load with a retry", async () => {
    handler = () => ({ status: 500, body: { code: "SCHED-9999", detail: "database down" } });
    renderAt("/scheduling/bottlenecks");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("SCHED-9999");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("rebuilds the weekly load and reloads the list", async () => {
    handler = (url, init) =>
      init?.method === "POST" && url.pathname.endsWith("/capacity-load:refresh")
        ? { status: 200, body: { resources: 3, loadRows: 12, openEpisodes: 1 } }
        : { status: 200, body: { items: [open] } };
    renderAt("/scheduling/bottlenecks");
    await findTable({ name: "Bottlenecks" });
    fireEvent.click(screen.getByRole("button", { name: "Rebuild weekly load" }));
    expect(await screen.findByText("3 resources, 12 loaded weeks, 1 open bottlenecks.")).toBeInTheDocument();
    await waitFor(() => expect(calls.filter((c) => c.method === "GET" && c.url.pathname.endsWith("/bottlenecks")).length).toBe(2));
  });

  it("shows the problem when the rebuild is refused", async () => {
    handler = (_url, init) => (init?.method === "POST" ? { status: 403, body: { code: "COM-0403", detail: "missing duty scheduling.run" } } : { status: 200, body: { items: [] } });
    renderAt("/scheduling/bottlenecks");
    await screen.findByText("No bottlenecks");
    fireEvent.click(screen.getByRole("button", { name: "Rebuild weekly load" }));
    expect(await findAlert()).toHaveTextContent("COM-0403");
  });
});

describe("Bottleneck detail", () => {
  const detail: BottleneckDetail = {
    ...open,
    weeks: [
      { weekStart: "2027-01-04", openMinutes: 2400, bookedMinutes: 2400, loadPct: 100, bottleneck: true },
      { weekStart: "2027-01-11", openMinutes: 2400, bookedMinutes: 1200, loadPct: 50, bottleneck: false },
    ],
  };

  it("shows the episode, its weeks and a drill-down to the jobs of each week", async () => {
    handler = (url) => (url.pathname.endsWith(`/bottlenecks/${EP}`) ? { status: 200, body: detail } : undefined);
    const { container } = renderAt(`/scheduling/bottlenecks/${EP}`);
    const table = await findTable({ name: "Weekly load" });
    expect(screen.getByRole("heading", { name: "Bottleneck on MIX-1" })).toBeInTheDocument();
    expect(screen.getByText("above 90 % for 3 weeks in a row")).toBeInTheDocument();
    expect(screen.getByText("104.5 %")).toBeInTheDocument();
    expect(table).toHaveTextContent("Bottleneck");
    expect(screen.getByRole("link", { name: "Jobs of MIX-1 from 2027-01-04" })).toHaveAttribute(
      "href",
      `/scheduling/load/jobs?${new URLSearchParams({ resourceId: MIX, from: "2027-01-04T00:00:00Z", to: "2027-01-11T00:00:00Z" })}`,
    );
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("says so when the episode does not exist", async () => {
    handler = () => ({ status: 404, body: { code: "SCHED-0147", detail: "bottleneck episode not found" } });
    renderAt(`/scheduling/bottlenecks/${EP}`);
    expect(await screen.findByText("Bottleneck not found")).toBeInTheDocument();
  });

  it("reports a failed load with a retry", async () => {
    handler = () => ({ status: 500, body: { code: "SCHED-9999", detail: "database down" } });
    renderAt(`/scheduling/bottlenecks/${EP}`);
    expect(await findAlert()).toHaveTextContent("SCHED-9999");
  });
});
