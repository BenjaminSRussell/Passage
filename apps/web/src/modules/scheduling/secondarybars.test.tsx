import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GanttBar, Resource } from "./api/client";
import { dayStart, today } from "./format";
import { jobsOf, localOverlaps } from "./gantt";
import { GanttView } from "./GanttView";
import scheduling from "./module";

const primary: GanttBar = {
  jobId: "job-a",
  orderId: "order-1",
  operNumber: 10,
  resourceId: "m1",
  segment: 1,
  start: "2026-10-26T06:00:00Z",
  end: "2026-10-26T09:00:00Z",
  lateMinutes: 0,
  firmed: false,
  setupGroup: null,
  changeoverMinutes: 0,
  changeoverCost: null,
  changeoverCurrency: null,
  cleaningSetId: null,
  secondaryResourceIds: ["tool"],
  outsideCapacity: false,
  secondary: false,
  primaryResourceId: null,
};
const secondary: GanttBar = { ...primary, resourceId: "tool", secondaryResourceIds: [], secondary: true, primaryResourceId: "m1" };

describe("secondary booking bars (BR-23.4)", () => {
  it("are not jobs of their own", () => {
    const jobs = jobsOf([primary, secondary]);
    expect(jobs).toHaveLength(1);
    expect(jobs[0]?.resourceId).toBe("m1");
  });

  it("still occupy the secondary resource in the local overlap check", () => {
    const clash = localOverlaps([primary, secondary], { jobId: "job-b", resourceId: "tool", start: "2026-10-26T07:00:00Z", end: "2026-10-26T08:00:00Z" });
    expect(clash.map((b) => b.resourceId)).toEqual(["tool"]);
  });

  it("are drawn on the secondary row, cannot be dragged and select the job on click", () => {
    const onSelect = vi.fn();
    const onDrop = vi.fn();
    const { container } = render(
      <GanttView
        bars={[primary, secondary]}
        resources={[
          { id: "m1", code: "M1" },
          { id: "tool", code: "TOOL" },
        ]}
        from="2026-10-26T00:00:00Z"
        days={1}
        onSelect={onSelect}
        onDrop={onDrop}
      />,
    );
    const toolRow = container.querySelector('svg[data-resource="TOOL"]');
    const bar = toolRow?.querySelector("rect[data-secondary-job]");
    expect(bar).not.toBeNull();
    expect(toolRow?.querySelector("rect[data-job]")).toBeNull();
    expect(container.querySelector('svg[data-resource="M1"] rect[data-job="job-a"]')).not.toBeNull();
    expect(bar?.querySelector("title")?.textContent).toContain("Held as secondary for order order-1 operation 10 running on M1");

    fireEvent.pointerDown(bar as Element, { clientX: 10 });
    fireEvent.pointerUp(bar as Element, { clientX: 300 });
    expect(onDrop).not.toHaveBeenCalled();
    fireEvent.click(bar as Element);
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ jobId: "job-a", secondary: true }));
  });

  it("flag a secondary booking in closed time like any bar", () => {
    const { container } = render(
      <GanttView bars={[{ ...secondary, outsideCapacity: true }]} resources={[{ id: "tool", code: "TOOL" }]} from="2026-10-26T00:00:00Z" days={1} />,
    );
    expect(container.querySelector("rect[data-secondary-job]")?.getAttribute("class")).toMatch(/violation/);
    expect(container.querySelector("rect[data-secondary-job] title")?.textContent).toContain("SCHED-0002");
  });
});

describe("selecting a secondary bar in the workspace (BR-23.4)", () => {
  const M1 = "3f1c0000-0000-4000-8000-000000000001";
  const TOOL = "3f1c0000-0000-4000-8000-000000000002";
  const JOB = "0a000000-0000-4000-8000-000000000001";
  const day = dayStart(today());
  const at = (h: number) => new Date(Date.parse(day) + h * 3_600_000).toISOString().replace(".000Z", "Z");
  const shifts = [1, 2, 3, 4, 5].map((weekday) => ({ weekday, startMinute: 480, endMinute: 960 }));
  const resources: Resource[] = [
    { id: M1, code: "M1", name: "Press", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" },
    { id: TOOL, code: "TOOL", name: "Die", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" },
  ];
  const onM1: GanttBar = { ...primary, jobId: JOB, orderId: "b1000000-0000-4000-8000-000000000001", resourceId: M1, start: at(8), end: at(10), secondaryResourceIds: [TOOL] };
  const onTool: GanttBar = { ...onM1, resourceId: TOOL, secondaryResourceIds: [], secondary: true, primaryResourceId: M1 };

  afterEach(() => vi.unstubAllGlobals());

  it("proposes the move on the job's primary resource, not the secondary row", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        const url = new URL(input, "http://localhost");
        const body = url.pathname.endsWith("/resources") ? { items: resources } : url.pathname.endsWith("/gantt") ? { items: [onM1, onTool] } : undefined;
        return new Response(JSON.stringify(body ?? { code: "TEST-404", detail: "no fake" }), { status: body ? 200 : 404, headers: { "Content-Type": "application/json" } });
      }),
    );
    const { container } = render(
      <MemoryRouter initialEntries={["/scheduling"]}>
        <Routes>
          {scheduling.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>,
    );
    await waitFor(() => expect(container.querySelector(`svg[data-resource="TOOL"] rect[data-secondary-job="${JOB}"]`)).not.toBeNull());
    fireEvent.click(container.querySelector(`svg[data-resource="TOOL"] rect[data-secondary-job="${JOB}"]`) as Element);
    const select = (await screen.findByLabelText("Resource")) as HTMLSelectElement;
    expect(select.value).toBe(M1);
    expect(screen.getByLabelText("New start (UTC)")).toHaveValue(at(8).slice(0, 16));
  });
});
