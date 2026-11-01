import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { getJobs, getOrderJobs, type ProductionJob } from "./api/client";
import { initialValues, toInput, validateOrder } from "./CreateOrderPage";
import { validateJobsFilter } from "./JobsPage";
import production from "./module";
import { validateSchedule } from "./SchedulePanel";
import { json, order, orderId, problem } from "./testData";

afterEach(() => vi.unstubAllGlobals());

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const calls = (mock: ReturnType<typeof vi.fn>) => mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;
const header = (init: RequestInit | undefined, name: string) => (init?.headers as Record<string, string> | undefined)?.[name];

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <a href="#main">Skip to content</a>
      <nav aria-label="Workspaces" />
      <main id="main">
        <h1>Production</h1>
        <Routes>
          {production.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </main>
    </MemoryRouter>,
  );
}

const uuid = (n: number) => `0195d6a0-0000-7000-8000-${String(n).padStart(12, "0")}`;

const job = (overrides: Partial<ProductionJob> = {}): ProductionJob => ({
  id: uuid(500),
  orderId,
  orderNumber: "PO-000001",
  orderStatus: "Scheduled",
  legalEntityId: uuid(1),
  siteId: uuid(2),
  productId: uuid(3),
  operNumber: 10,
  seq: 1,
  jobType: "process",
  resourceId: "WC-WELD",
  plannedStart: "2026-11-02T08:00:00Z",
  plannedEnd: "2026-11-02T18:00:00Z",
  plannedHours: "10",
  hoursReported: "0",
  qtyGood: "0",
  qtyError: "0",
  status: "planned",
  ...overrides,
});

/** Answers the detail page's reads; jobs are what GET .../jobs returns. */
function detailFetch(current: ReturnType<typeof order>, jobs: ProductionJob[] | Response, onPost?: (url: string, init: RequestInit) => Response) {
  return stubFetch((url, init) => {
    if (init?.method === "POST" && onPost) {
      return onPost(url, init);
    }
    if (url.endsWith("/jobs")) {
      return jobs instanceof Response ? jobs : json({ orderId, items: jobs, plannedHours: "0" });
    }
    if (url.endsWith("/status-log") || url.endsWith("/journals") || url.endsWith("/as-built") || url.endsWith("/trigger-points")) {
      return json({ orderId, items: [] });
    }
    if (url.includes("reworkOf=") || url.includes("/bom-edits")) {
      return json({ orderId, items: [] });
    }
    if (url.endsWith("/costs")) {
      return json({ orderId, currency: "USD", totals: {}, total: "0", rows: [] });
    }
    return json(current);
  });
}

describe("job client", () => {
  it("reads the jobs across orders with the filter and the page size", async () => {
    const fetchMock = stubFetch(() => json({ items: [job()], total: 3, plannedHours: "27", nextCursor: uuid(500) }));
    const page = await getJobs({ resourceId: uuid(9), jobType: "setup", orderStatus: "", from: "2026-11-02T08:00:00.000Z" });
    expect(calls(fetchMock)[0][0]).toBe(
      `/api/v1/production/jobs?resourceId=${uuid(9)}&jobType=setup&from=2026-11-02T08%3A00%3A00.000Z&limit=50`,
    );
    expect(page).toMatchObject({ total: 3, plannedHours: "27", nextCursor: uuid(500) });
    expect(page.items).toHaveLength(1);
  });

  it("reads the jobs of one order and tolerates a body without items", async () => {
    const fetchMock = stubFetch(() => json({}));
    expect(await getOrderJobs(orderId)).toEqual([]);
    expect(calls(fetchMock)[0][0]).toBe(`/api/v1/production/orders/${orderId}/jobs`);
  });
});

describe("Schedule panel on the order page", () => {
  it("schedules an Estimated order with the chosen options, the version and a key", async () => {
    const fetchMock = detailFetch(order({ status: "Estimated", version: 2 }), [], () => json(order({ status: "Scheduled", version: 3, startDate: "2026-11-02", endDate: "2026-11-02", schedulingMethod: "jobs" })));
    const { container } = renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "Schedule the order" });
    expect(screen.queryByRole("button", { name: "Schedule" })).not.toBeInTheDocument();
    await userEvent.selectOptions(within(form).getByLabelText("Method"), "jobs");
    fireEvent.change(within(form).getByLabelText(/Start at/), { target: { value: "2026-11-02T08:00" } });
    await userEvent.click(within(form).getByRole("button", { name: "Schedule order" }));

    expect(await screen.findByText("Schedule applied. Status: Scheduled.")).toBeInTheDocument();
    const post = calls(fetchMock).find(([, init]) => init?.method === "POST");
    expect(post?.[0]).toBe(`/api/v1/production/orders/${orderId}:schedule`);
    expect(header(post?.[1], "If-Match")).toBe('"2"');
    expect(header(post?.[1], "Idempotency-Key")).toBeTruthy();
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ method: "jobs", direction: "forward", at: new Date("2026-11-02T08:00").toISOString() });
    // The panel shows the dates the schedule gave the order.
    expect(await screen.findByText("By job (setup and run apart)")).toBeInTheDocument();
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("refuses a backward schedule with no finish and no delivery date before sending anything", async () => {
    const fetchMock = detailFetch(order({ status: "Estimated", version: 2, deliveryDate: undefined }), []);
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "Schedule the order" });
    await userEvent.selectOptions(within(form).getByLabelText("Direction"), "backward");
    await userEvent.click(within(form).getByRole("button", { name: "Schedule order" }));
    expect(within(form).getByText(/needs a finish time here or a delivery date/)).toBeInTheDocument();
    expect(calls(fetchMock).some(([, init]) => init?.method === "POST")).toBe(false);
  });

  it("shows the service's refusal of a schedule", async () => {
    detailFetch(order({ status: "Estimated", version: 2 }), [], () => problem(422, "PRODUCTION_SCHEDULE_INPUT", "the order has no operations to schedule"));
    renderAt(`/production/${orderId}`);
    await userEvent.click(await screen.findByRole("button", { name: "Schedule order" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("the order has no operations to schedule (PRODUCTION_SCHEDULE_INPUT)");
  });

  it("lists the jobs and the operation windows of a scheduled order, and warns when it ends late", async () => {
    detailFetch(
      order({
        status: "Scheduled",
        version: 3,
        deliveryDate: "2026-11-02",
        startDate: "2026-11-02",
        endDate: "2026-11-04",
        schedulingMethod: "operations",
        operations: [{ ...order().operations[0], plannedStart: "2026-11-02T08:00:00Z", plannedEnd: "2026-11-04T10:00:00Z" }],
      }),
      [job({ jobType: "setup", plannedHours: "1" }), job({ id: uuid(501), seq: 2, plannedHours: "10" })],
    );
    renderAt(`/production/${orderId}`);
    const jobs = await findTable({ name: "Order jobs" });
    await waitFor(() => expect(within(jobs).getByText("Setup")).toBeInTheDocument());
    expect(within(jobs).getByText("Process")).toBeInTheDocument();
    expect(within(jobs).getAllByText("2026-11-02 08:00Z")).toHaveLength(2);
    expect(screen.getByText("The schedule ends after the delivery date")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Order operations" })).getByText("2026-11-04 10:00Z")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Schedule the order" })).not.toBeInTheDocument();
  });

  it("explains an unscheduled order and a failed jobs read", async () => {
    detailFetch(order({ status: "Created" }), problem(500, "COM-0500", "boom"));
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText("This order has no schedule.")).toBeInTheDocument();
    expect(await screen.findByText("The jobs could not be loaded")).toBeInTheDocument();
  });

  it("shows the empty state of an Estimated order's jobs", async () => {
    detailFetch(order({ status: "Estimated" }), []);
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText("Not scheduled yet. Schedule the order to date its operations.")).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "No jobs" })).toBeInTheDocument();
  });

  it("checks the options as the service does", () => {
    expect(validateSchedule({ direction: "forward", method: "jobs", at: "" }, undefined)).toBeUndefined();
    expect(validateSchedule({ direction: "backward", method: "jobs", at: "" }, "2026-11-30")).toBeUndefined();
    expect(validateSchedule({ direction: "backward", method: "jobs", at: "2026-11-30T12:00" }, undefined)).toBeUndefined();
    expect(validateSchedule({ direction: "backward", method: "jobs", at: "" }, undefined)).toMatch(/delivery date/);
    expect(validateSchedule({ direction: "forward", method: "jobs", at: "not a time" }, undefined)).toMatch(/valid date and time/);
  });
});

describe("Scheduled jobs page", () => {
  it("lists the jobs with totals and opens the order of a row", async () => {
    const fetchMock = stubFetch(() => json({ items: [job(), job({ id: uuid(501), jobType: "setup", plannedHours: "1" })], total: 2, plannedHours: "11" }));
    const { container } = renderAt("/production/jobs");
    const table = await findTable({ name: "Jobs" });
    expect(within(table).getByText("Setup")).toBeInTheDocument();
    expect(within(table).getAllByText("PO-000001")).toHaveLength(2);
    expect(screen.getByRole("list", { name: "Job totals" })).toHaveTextContent("11");
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/jobs?limit=50");
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("filters by resource, job type and window", async () => {
    const fetchMock = stubFetch(() => json({ items: [], total: 0, plannedHours: "0" }));
    renderAt("/production/jobs");
    expect(await screen.findByRole("heading", { name: "No jobs" })).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText(/^Resource/), uuid(9));
    await userEvent.selectOptions(screen.getByLabelText("Job"), "setup");
    fireEvent.change(screen.getByLabelText(/^From/), { target: { value: "2026-11-02T08:00" } });
    await userEvent.click(screen.getByRole("button", { name: "Show" }));
    await waitFor(() => expect(calls(fetchMock).at(-1)?.[0]).toContain(`resourceId=${uuid(9)}`));
    const url = new URL(calls(fetchMock).at(-1)?.[0] ?? "", "http://x");
    expect(url.searchParams.get("jobType")).toBe("setup");
    expect(url.searchParams.get("from")).toBe(new Date("2026-11-02T08:00").toISOString());
  });

  it("checks the filter before asking", async () => {
    const fetchMock = stubFetch(() => json({ items: [], total: 0, plannedHours: "0" }));
    renderAt("/production/jobs");
    await screen.findByRole("heading", { name: "No jobs" });
    await userEvent.type(screen.getByLabelText(/^Resource/), "not-an-id");
    await userEvent.click(screen.getByRole("button", { name: "Show" }));
    expect(screen.getByText("Enter the resource's id (a UUID), or leave it empty.")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(validateJobsFilter({ resourceId: "", siteId: "x", jobType: "", orderStatus: "", from: "", to: "" }).siteId).toMatch(/UUID/);
    expect(validateJobsFilter({ resourceId: "", siteId: "", jobType: "", orderStatus: "", from: "2026-11-03T08:00", to: "2026-11-02T08:00" }).to).toMatch(/after its start/);
  });

  it("loads the next page and keeps the rows when it fails", async () => {
    let fail = true;
    stubFetch((url) => {
      if (!url.includes("cursor=")) {
        return json({ items: [job()], total: 2, plannedHours: "11", nextCursor: uuid(500) });
      }
      return fail ? problem(503, "COM-0503", "database unavailable") : json({ items: [job({ id: uuid(502), jobType: "transport" })], total: 2, plannedHours: "11" });
    });
    renderAt("/production/jobs");
    await findTable({ name: "Jobs" });
    await userEvent.click(screen.getByRole("button", { name: "Load more jobs" }));
    expect(await screen.findByText("More jobs could not be loaded")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Jobs" })).getByText("Process")).toBeInTheDocument();
    fail = false;
    await userEvent.click(screen.getByRole("button", { name: "Load more jobs" }));
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Jobs" })).getByText("Transport")).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Load more jobs" })).not.toBeInTheDocument();
  });

  it("explains a refused read", async () => {
    stubFetch(() => problem(403, "AUTH-0003", "missing duty production.order.read"));
    renderAt("/production/jobs");
    expect(await screen.findByText("You need the View production orders duty to see the jobs.")).toBeInTheDocument();
  });

  it("shows a loading skeleton while the request is pending", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    renderAt("/production/jobs");
    expect(await screen.findByRole("table", { name: "Jobs" })).toHaveAttribute("aria-busy", "true");
  });
});

describe("Queue and transit hours on the create form", () => {
  it("sends the hours given and leaves out the empty ones", () => {
    const values = initialValues();
    const input = toInput({ ...values, quantity: "48", operations: [{ ...values.operations[0], queueBeforeH: "2", transitH: "0.5" }] });
    expect(input.operations[0]).toMatchObject({ queueBeforeH: "2", transitH: "0.5" });
    expect(input.operations[0].queueAfterH).toBeUndefined();
  });

  it("checks the format of the hours", () => {
    const values = initialValues();
    const errors = validateOrder({ ...values, operations: [{ ...values.operations[0], queueAfterH: "-1" }] });
    expect(errors["operations.0.queueAfterH"]).toMatch(/up to 6 decimals/);
  });
});
