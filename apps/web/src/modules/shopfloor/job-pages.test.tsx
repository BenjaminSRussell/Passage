import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Job } from "./api/client";
import { toFeedbackInput, validateFeedback } from "./JobDetailPage";
import { qty } from "./JobsPage";
import shopfloor from "./module";

const JOB = "0195f0a0-0000-7000-8000-000000000050";
const RESOURCE = "0195f0a0-0000-7000-8000-000000000051";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
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
        {shopfloor.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const posts = () => calls.filter((c) => c.method === "POST");

const job = (extra: Partial<Job> = {}): Job => ({
  id: JOB,
  orderRef: "MO-1",
  operationRef: "OP-10",
  resourceId: RESOURCE,
  status: "started",
  plannedQuantity: "100.0000",
  goodQuantity: "40.2500",
  scrapQuantity: "0.0000",
  sequenceStart: "2026-10-21T08:00:00Z",
  checks: [
    { code: "len", name: "Length", kind: "numeric", min: "10", max: "12", mandatory: true },
    { code: "visual", name: "Visual", kind: "pass-fail", mandatory: false },
  ],
  results: [{ code: "visual", value: "pass", pass: true }],
  finishedAt: null,
  version: 3,
  updatedAt: "2026-10-21T09:00:00Z",
  ...extra,
});

describe("dispatch list", () => {
  it("lists open jobs in start order with quantities and links to each job", async () => {
    handler = (url) => (url.pathname === "/api/v1/shopfloor/jobs" ? json(200, { items: [job({ results: [] })] }) : undefined);
    const { container } = renderAt("/shopfloor/jobs");
    const table = await findTable({ name: "Jobs" });
    const row = within(table).getByRole("link", { name: "Open MO-1 OP-10" }).closest("tr") as HTMLElement;
    expect(row).toHaveTextContent("100");
    expect(row).toHaveTextContent("40.25");
    expect(row).toHaveTextContent("Started");
    expect(row).toHaveTextContent("Mandatory checks");
    expect(within(table).getByRole("link", { name: "Open MO-1 OP-10" })).toHaveAttribute("href", `/shopfloor/jobs/${JOB}`);
    expect(calls[0].url.search).toBe("");
    expect(pageViolations(container)).toEqual([]);
  });

  it("filters by resource and status, and refuses a resource that is not a UUID", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/shopfloor/jobs");
    expect(await screen.findByText("No open jobs")).toBeInTheDocument();
    fill("Resource", "press 4");
    fireEvent.click(screen.getByRole("button", { name: "Show jobs" }));
    expect(await screen.findByText(/Enter the resource id/)).toBeInTheDocument();
    fill("Resource", RESOURCE);
    fireEvent.click(screen.getByRole("button", { name: "Show jobs" }));
    fill("Status", "finished");
    await waitFor(() => expect(calls.at(-1)?.url.search).toBe(`?resourceId=${RESOURCE}&status=finished`));
    expect(await screen.findByText("No jobs match these filters")).toBeInTheDocument();
  });

  it("shows the service error with a retry", async () => {
    handler = () => json(503, { code: "COM-0503", detail: "database unavailable" });
    renderAt("/shopfloor/jobs");
    expect(await screen.findByText(/database unavailable \(COM-0503\)/)).toBeInTheDocument();
    handler = () => json(200, { items: [] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No open jobs")).toBeInTheDocument();
  });
});

describe("job detail", () => {
  it("shows the plan, totals and each check with its latest result", async () => {
    handler = (url) => (url.pathname === `/api/v1/shopfloor/jobs/${JOB}` ? json(200, job()) : undefined);
    const { container } = renderAt(`/shopfloor/jobs/${JOB}`);
    expect(await screen.findByRole("heading", { name: "Job MO-1 OP-10" })).toBeInTheDocument();
    const checks = screen.getByRole("table", { name: "Quality checks" });
    const length = within(checks).getByText("Length (len)").closest("tr") as HTMLElement;
    expect(length).toHaveTextContent("10 to 12");
    expect(length).toHaveTextContent("Not recorded");
    expect(within(checks).getByText("Visual (visual)").closest("tr")).toHaveTextContent("Pass");
    expect(pageViolations(container)).toEqual([]);
  });

  it("reports quantities and check values with one feedbackId and shows the new totals", async () => {
    handler = (_url, init) => {
      if (init.method === "POST") return json(201, { job: job({ goodQuantity: "45.2500", version: 4 }), duplicate: false });
      return json(200, job());
    };
    renderAt(`/shopfloor/jobs/${JOB}`);
    await screen.findByRole("heading", { name: "Job MO-1 OP-10" });
    fill("Good quantity", "5");
    fill("Scrap quantity", "1");
    fireEvent.click(screen.getByRole("button", { name: "Save report" }));
    expect(await screen.findByText("Scrap needs a reason code.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
    fill("Scrap reason", "BURR");
    fill("Length", " 11.5 ");
    fireEvent.click(screen.getByRole("button", { name: "Save report" }));
    expect(await screen.findByText("Quantities and check values were added to the job.")).toBeInTheDocument();
    const [post] = posts();
    expect(post.url.pathname).toBe(`/api/v1/shopfloor/jobs/${JOB}:feedback`);
    expect(post.body).toEqual({
      feedbackId: expect.stringMatching(/^[0-9a-f-]{36}$/),
      finished: false,
      goodQuantity: "5",
      scrapQuantity: "1",
      scrapReasonId: "BURR",
      checks: [{ code: "len", value: "11.5" }],
    });
    expect(screen.getByText("45.25")).toBeInTheDocument();
  });

  it("offers a supervisor override when a mandatory check blocks the finish (SF-0005), resending the same posting", async () => {
    let attempt = 0;
    handler = (_url, init) => {
      if (init.method !== "POST") return json(200, job());
      attempt += 1;
      if (attempt === 1) return json(422, { code: "SF-0005", detail: "Mandatory check len failed; operation cannot be finished." });
      return json(201, { job: job({ status: "finished", finishedAt: "2026-10-21T10:00:00Z", version: 4 }), duplicate: false });
    };
    renderAt(`/shopfloor/jobs/${JOB}`);
    await screen.findByRole("heading", { name: "Job MO-1 OP-10" });
    fill("Length", "13");
    fireEvent.click(screen.getByRole("button", { name: "Report and finish" }));
    expect(await screen.findByText(/Mandatory check len failed/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Finish with override" }));
    expect(await screen.findByText(/Say why the job may be finished/)).toBeInTheDocument();
    fill("Override reason", "Deviation D-17 approved by quality");
    fireEvent.click(screen.getByRole("button", { name: "Finish with override" }));
    expect(await screen.findByText("The job is finished and has left the dispatch list.")).toBeInTheDocument();
    const [blocked, overridden] = posts();
    expect(blocked.body).toMatchObject({ finished: true, checks: [{ code: "len", value: "13" }] });
    expect(overridden.body).toMatchObject({ finished: true, override: { reason: "Deviation D-17 approved by quality" } });
    expect((overridden.body as { feedbackId: string }).feedbackId).toBe((blocked.body as { feedbackId: string }).feedbackId);
    expect(screen.getByText("This job is finished")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save report" })).not.toBeInTheDocument();
  });

  it("shows a load failure with a retry, and a save failure without losing the form", async () => {
    handler = () => json(404, { code: "SF-0121", detail: "job not found" });
    renderAt(`/shopfloor/jobs/${JOB}`);
    expect(await screen.findByText(/job not found \(SF-0121\)/)).toBeInTheDocument();
    handler = (_url, init) => (init.method === "POST" ? json(422, { code: "SF-0122", detail: "job MO-1 OP-10 is finished" }) : json(200, job()));
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await screen.findByRole("heading", { name: "Job MO-1 OP-10" });
    fill("Good quantity", "2");
    fireEvent.click(screen.getByRole("button", { name: "Save report" }));
    expect(await screen.findByText(/is finished \(SF-0122\)/)).toBeInTheDocument();
    expect(screen.getByLabelText("Good quantity")).toHaveValue("2");
  });
});

describe("feedback rules", () => {
  const values = { good: "", scrap: "", scrapReason: "", checks: {} as Record<string, string> };

  it("needs something to report, well-formed quantities and a reason for scrap", () => {
    expect(validateFeedback(values, false)).toEqual({ good: "Report a quantity or a check value, or finish the job." });
    expect(validateFeedback(values, true)).toEqual({});
    expect(validateFeedback({ ...values, good: "1.23456" }, false)).toHaveProperty("good");
    expect(validateFeedback({ ...values, good: "-1" }, false)).toHaveProperty("good");
    expect(validateFeedback({ ...values, scrap: "0.5" }, false)).toEqual({ scrapReason: "Scrap needs a reason code." });
    expect(validateFeedback({ ...values, scrap: "0.000" }, false)).toHaveProperty("good");
    expect(validateFeedback({ ...values, checks: { len: "11" } }, false)).toEqual({});
  });

  it("sends only what was entered", () => {
    expect(toFeedbackInput("f-1", { ...values, good: " 3 ", checks: { len: "", visual: "fail" } }, false)).toEqual({
      feedbackId: "f-1",
      finished: false,
      goodQuantity: "3",
      checks: [{ code: "visual", value: "fail" }],
    });
    expect(toFeedbackInput("f-2", values, true, "ok")).toEqual({ feedbackId: "f-2", finished: true, override: { reason: "ok" } });
  });

  it("formats quantities without trailing zeros and without floats", () => {
    expect(qty("100.0000")).toBe("100");
    expect(qty("40.2500")).toBe("40.25");
    expect(qty("0.0000")).toBe("0");
    expect(qty("1000.0001")).toBe("1000.0001");
  });
});
