import { findTable } from "../../test-utils";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import type { Job } from "./api/client";
import { toFeedbackInput, validateFeedback } from "./JobDetailPage";
import { qty } from "./JobsPage";
import { resetOrderCache } from "../mfg-shared/orders";
import { resetResourceCache } from "../mfg-shared/resources";
import shopfloor from "./module";

const JOB = "0195f0a0-0000-7000-8000-000000000050";
const RESOURCE = "0195f0a0-0000-7000-8000-000000000051";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

const isExtra = (url: URL) => url.pathname.endsWith("/engineering/resources") || url.pathname.includes("/production/orders");
function extra(url: URL): Reply | undefined {
  if (url.pathname.endsWith("/engineering/resources")) return { status: 200, body: { items: [{ id: RESOURCE, code: "MIX-1", name: "Mixer" }] } };
  if (url.pathname.endsWith("/production/orders")) return { status: 200, body: { items: [{ id: ORDER, orderNumber: "MO-77" }] } };
  if (url.pathname.endsWith(`/production/orders/${ORDER}`)) return { status: 200, body: { id: ORDER, orderNumber: "MO-77", productId: "" } };
  return undefined;
}
const ORDER = "0195f0a0-0000-7000-8000-000000000040";

let handler: Handler;
let calls: { method: string; url: URL; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  resetResourceCache();
  resetOrderCache();
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      if (!isExtra(url)) calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? extra(url) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const OPERATOR = ["shopfloor.time.register"];
const SUPERVISOR = ["shopfloor.time.register", "shopfloor.view", "shopfloor.job.reopen"];

function renderAt(path: string, duties: readonly string[] = OPERATOR) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {shopfloor.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const pick = async (label: string, value: string) => {
  const select = screen.getByLabelText(label);
  await waitFor(() => expect(select.querySelector(`option[value="${value}"]`)).not.toBeNull());
  fireEvent.change(select, { target: { value } });
};
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

  it("filters by resource (picked by name) and status", async () => {
    handler = (url) => (url.pathname.endsWith("/engineering/resources") ? undefined : json(200, { items: [] }));
    renderAt("/shopfloor/jobs");
    expect(await screen.findByText("No open jobs")).toBeInTheDocument();
    await pick("Resource", RESOURCE);
    fireEvent.click(screen.getByRole("button", { name: "Show jobs" }));
    fill("Status", "finished");
    await waitFor(() => expect(calls.at(-1)?.url.search).toBe(`?resourceId=${RESOURCE}&status=finished`));
    expect(await screen.findByText("No jobs match these filters")).toBeInTheDocument();
  });

  it("shows the service error with a retry", async () => {
    handler = () => json(503, { code: "COM-0503", detail: "database unavailable" });
    renderAt("/shopfloor/jobs");
    expect(await screen.findByText(/database unavailable/)).toBeInTheDocument();
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

  it("tells the reader that production finished the operation, and the dispatch list marks the job", async () => {
    const finished = job({ productionFinishedAt: "2026-10-21T12:00:00Z" });
    handler = (url) => {
      if (url.pathname === `/api/v1/shopfloor/jobs/${JOB}`) return json(200, finished);
      if (url.pathname.endsWith("/jobs")) return json(200, { items: [finished] });
      return undefined;
    };
    const { container } = renderAt(`/shopfloor/jobs/${JOB}`);
    expect(await screen.findByText("Production finished this operation")).toBeInTheDocument();
    expect(screen.getByText(/SF-0157/)).toBeInTheDocument();
    expect(screen.getByText(/SF-0161/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /report/i })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    cleanup();
    renderAt("/shopfloor/jobs");
    expect(await screen.findByText("Production finished")).toBeInTheDocument();
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
    expect(await screen.findByText(/job not found/)).toBeInTheDocument();
    handler = (_url, init) => (init.method === "POST" ? json(422, { code: "SF-0122", detail: "job MO-1 OP-10 is finished" }) : json(200, job()));
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await screen.findByRole("heading", { name: "Job MO-1 OP-10" });
    fill("Good quantity", "2");
    fireEvent.click(screen.getByRole("button", { name: "Save report" }));
    expect(await screen.findByText(/is finished/)).toBeInTheDocument();
    expect(screen.getByLabelText("Good quantity")).toHaveValue("2");
  });
});

const finishedJob = (extra: Partial<Job> = {}) => job({ status: "finished", finishedAt: "2026-10-21T10:00:00Z", version: 6, ...extra });
const reopeningRow = {
  id: "0195f0a0-0000-7000-8000-000000000060",
  jobId: JOB,
  reason: "Late count of 12 pieces",
  reopenedBy: "sup-1",
  reopenedAt: "2026-10-21T11:00:00Z",
  previousFinishedAt: "2026-10-21T10:00:00Z",
  goodQuantity: "90.0000",
  scrapQuantity: "0.0000",
};

describe("reopening a finished job", () => {
  it("reopens with a reason, If-Match and one key, then offers the report form and lists the reopening", async () => {
    let reopened = false;
    handler = (url, init) => {
      if (init.method === "POST") {
        reopened = true;
        return json(200, job({ version: 7 }));
      }
      if (url.pathname.endsWith("/reopenings")) return json(200, { items: reopened ? [reopeningRow] : [] });
      return json(200, finishedJob());
    };
    const { container } = renderAt(`/shopfloor/jobs/${JOB}`, SUPERVISOR);
    expect(await screen.findByText("Never reopened")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save report" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reopen job" }));
    expect(await screen.findByText(/Say why the job is reopened/)).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
    fill("Reason", "  Late count of 12 pieces  ");
    fireEvent.click(screen.getByRole("button", { name: "Reopen job" }));
    expect(await screen.findByText("The job is reopened and is back on the dispatch list.")).toBeInTheDocument();
    const [post] = posts();
    expect(post.url.pathname).toBe(`/api/v1/shopfloor/jobs/${JOB}:reopen`);
    expect(post.body).toEqual({ reason: "Late count of 12 pieces" });
    expect(fetch).toHaveBeenCalledWith(expect.stringContaining(":reopen"), expect.objectContaining({ headers: expect.any(Headers) }));
    const headers = vi.mocked(fetch).mock.calls.find(([u]) => String(u).endsWith(":reopen"))?.[1]?.headers as Headers;
    expect(headers.get("If-Match")).toBe('"6"');
    expect(headers.get("Idempotency-Key")).toBeTruthy();
    expect(await screen.findByRole("button", { name: "Save report" })).toBeInTheDocument();
    const table = await screen.findByRole("table", { name: "Reopenings" });
    expect(within(table).getByText("Late count of 12 pieces")).toBeInTheDocument();
    expect(within(table).getByText("sup-1")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("keeps the reason and shows the service error when the reopen is refused", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(403, { code: "AUTH-0003", detail: "missing duty shopfloor.job.reopen" });
      if (url.pathname.endsWith("/reopenings")) return json(200, { items: [] });
      return json(200, finishedJob());
    };
    renderAt(`/shopfloor/jobs/${JOB}`, SUPERVISOR);
    await screen.findByText("Never reopened");
    fill("Reason", "Rework pass");
    fireEvent.click(screen.getByRole("button", { name: "Reopen job" }));
    expect(await screen.findByText(/missing duty shopfloor.job.reopen/)).toBeInTheDocument();
    expect(screen.getByLabelText("Reason")).toHaveValue("Rework pass");
  });

  it("shows a stale version as the service reports it, and a reopenings load failure with a retry", async () => {
    let listing = 0;
    handler = (url, init) => {
      if (init.method === "POST") return json(412, { code: "SF-0143", detail: "job was changed by someone else" });
      if (url.pathname.endsWith("/reopenings")) {
        listing += 1;
        return listing === 1 ? json(500, { code: "COM-0500", detail: "database down" }) : json(200, { items: [reopeningRow] });
      }
      return json(200, finishedJob());
    };
    renderAt(`/shopfloor/jobs/${JOB}`, SUPERVISOR);
    expect(await screen.findByText(/database down/)).toBeInTheDocument();
    fill("Reason", "Late count");
    fireEvent.click(screen.getByRole("button", { name: "Reopen job" }));
    expect(await screen.findByText(/changed by someone else/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Late count of 12 pieces")).toBeInTheDocument();
  });
  it("shows an operator the finished notice only: no reopen form, no reopenings list, no call for it", async () => {
    handler = () => json(200, finishedJob());
    renderAt(`/shopfloor/jobs/${JOB}`, OPERATOR);
    expect(await screen.findByText("This job is finished")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reopen job" })).not.toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Reopenings" })).not.toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname.endsWith("/reopenings"))).toBe(false);
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
