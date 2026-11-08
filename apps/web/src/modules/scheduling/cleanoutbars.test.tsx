import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { GanttBar } from "./api/client";
import { jobsOf, localOverlaps } from "./gantt";
import { GanttView } from "./GanttView";

const job: GanttBar = {
  jobId: "job-c",
  orderId: "order-c",
  operNumber: 10,
  resourceId: "m1",
  segment: 1,
  start: "2026-10-26T08:00:00Z",
  end: "2026-10-26T10:00:00Z",
  lateMinutes: 0,
  firmed: false,
  setupGroup: "C",
  changeoverMinutes: 0,
  changeoverCost: null,
  changeoverCurrency: null,
  cleaningSetId: null,
  secondaryResourceIds: [],
  outsideCapacity: false,
  cleanout: false,
  campaignId: null,
};
const cleanout: GanttBar = {
  ...job,
  jobId: "campaign-1",
  operNumber: 0,
  start: "2026-10-26T10:00:00Z",
  end: "2026-10-26T14:00:00Z",
  cleaningSetId: "CLEAN-A",
  cleanout: true,
  campaignId: "campaign-1",
};

describe("campaign cleanout window bars (FR-23.14, BR-23.13)", () => {
  it("are not jobs of their own", () => {
    expect(jobsOf([job, cleanout]).map((j) => j.jobId)).toEqual(["job-c"]);
  });

  it("still count as occupied time in the local overlap check", () => {
    const clash = localOverlaps([job, cleanout], { jobId: "job-d", resourceId: "m1", start: "2026-10-26T11:00:00Z", end: "2026-10-26T12:00:00Z" });
    expect(clash.map((b) => b.jobId)).toEqual(["campaign-1"]);
  });

  it("are drawn as a read-only closed-time block that is not a job bar", () => {
    const onSelect = vi.fn();
    const onDrop = vi.fn();
    const { container } = render(
      <GanttView
        bars={[job, cleanout]}
        resources={[{ id: "m1", code: "M1" }]}
        from="2026-10-26T00:00:00Z"
        days={1}
        selectedJobId="campaign-1"
        violations={{ "campaign-1": "not a job" }}
        onSelect={onSelect}
        onDrop={onDrop}
      />,
    );
    const row = container.querySelector('svg[data-resource="M1"]');
    const bar = row?.querySelector("rect[data-cleanout-campaign]");
    expect(bar).not.toBeNull();
    expect(row?.querySelectorAll("rect[data-job]")).toHaveLength(1);
    expect(bar?.getAttribute("class")).toMatch(/cleanoutBar/);
    // The campaign id in jobId never selects or outlines the window like a job.
    expect(bar?.getAttribute("class")).not.toMatch(/selected|violation/);
    expect(bar?.querySelector("title")?.textContent).toContain("Cleanout after group C following order order-c, cleaning set CLEAN-A");
    expect(bar?.querySelector("title")?.textContent).toContain("closed time");
    fireEvent.pointerDown(bar as Element, { clientX: 10 });
    fireEvent.pointerUp(bar as Element, { clientX: 300 });
    fireEvent.click(bar as Element);
    expect(onDrop).not.toHaveBeenCalled();
    expect(onSelect).not.toHaveBeenCalled();
  });
});
