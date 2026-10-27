import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RouteVersionPage, RouteVersionsPage } from "./screens";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });
const problem = (status: number, code: string, title: string) => ({ ok: false, status, json: async () => ({ code, title }) });

function header(id: string, extra: object = {}) {
  return {
    id,
    productId: `PROD-${id}`,
    siteId: "S1",
    routeCode: `RT-${id}`,
    versionNo: 1,
    effectiveFrom: "2026-01-01",
    status: "active",
    operationCount: 2,
    ...extra,
  };
}

function stubRoutes(handler: (url: string, init?: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("RouteVersionsPage", () => {
  it("shows a skeleton while loading, then rows with status pills", async () => {
    stubRoutes(() => ok({ items: [header("a"), header("b", { status: "draft" })] }));
    const opened: string[] = [];
    render(<RouteVersionsPage onOpen={(id) => opened.push(id)} />);
    expect(screen.getByRole("table", { name: "Route versions" })).toHaveAttribute("aria-busy", "true");
    const table = await screen.findByRole("table", { name: "Route versions" });
    await waitFor(() => expect(table).not.toHaveAttribute("aria-busy"));
    expect(table).toHaveTextContent("RT-a");
    expect(table).toHaveTextContent("Active");
    expect(table).toHaveTextContent("Draft");
    fireEvent.click(screen.getByRole("button", { name: "RT-a" }));
    expect(opened).toEqual(["a"]);
  });

  it("loads the next page with the returned cursor and appends", async () => {
    const fetchMock = stubRoutes((url) =>
      url.includes("cursor=c1") ? ok({ items: [header("b")] }) : ok({ items: [header("a")], nextCursor: "c1" }),
    );
    render(<RouteVersionsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await screen.findByText("RT-b");
    expect(screen.getByText("RT-a")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/route-versions?cursor=c1");
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("refetches on status change and on applying text filters", async () => {
    const fetchMock = stubRoutes(() => ok({ items: [header("a")] }));
    render(<RouteVersionsPage />);
    await screen.findByText("RT-a");
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "active" } });
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/route-versions?status=active"));
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "P1" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/route-versions?productId=P1&status=active"));
  });

  it("discards a late Load more page after the filter changes", async () => {
    let release: (value: unknown) => void = () => undefined;
    stubRoutes((url) => {
      if (url.includes("cursor=c1")) return new Promise((resolve) => (release = resolve));
      return url.includes("status=draft") ? ok({ items: [header("d")] }) : ok({ items: [header("a")], nextCursor: "c1" });
    });
    render(<RouteVersionsPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "draft" } });
    await screen.findByText("RT-d");
    release(ok({ items: [header("late")], nextCursor: "c2" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Load more" })).toBeNull());
    expect(screen.queryByText("RT-late")).toBeNull();
  });

  it("shows the empty state when no version matches", async () => {
    stubRoutes(() => ok({ items: [] }));
    render(<RouteVersionsPage />);
    expect(await screen.findByText("No route versions match")).toBeInTheDocument();
  });

  it("alerts on error and refetches on Retry", async () => {
    let calls = 0;
    stubRoutes(() => (++calls === 1 ? problem(400, "COM-0001", "Bad filter") : ok({ items: [header("a")] })));
    render(<RouteVersionsPage />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Bad filter");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByText("RT-a");
    expect(screen.getByRole("alert")).toBeEmptyDOMElement();
  });
});

const op = (no: number, operationId: string, nextNo: number, extra: object = {}) => ({
  no,
  operationId,
  nextNo,
  linkType: "hard",
  priority: "primary",
  setupH: "1",
  runH: "2",
  processQty: "1",
  queueBeforeH: "0.5",
  queueAfterH: "0.25",
  transitH: "0.1",
  efficiencyPct: "90",
  ...extra,
});

const detail = {
  ...header("a", { effectiveTo: "2026-12-31" }),
  operations: [op(20, "PACK", 0), op(10, "WELD", 20, { requirements: [{ kind: "capability", refId: "welding", level: 2, quantity: 1 }] })],
};

describe("RouteVersionPage", () => {
  it("renders operations in sequence with their requirements and no Activate control", async () => {
    stubRoutes(() => ok(detail));
    const back = vi.fn();
    render(<RouteVersionPage id="a" onBack={back} />);
    const table = await screen.findByRole("table", { name: "Operations" });
    fireEvent.click(screen.getByRole("button", { name: "Back to routings" }));
    expect(back).toHaveBeenCalled();
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows[0]).toHaveTextContent("WELD");
    expect(rows[0]).toHaveTextContent("capability welding, level 2, quantity 1");
    expect(rows[1]).toHaveTextContent("PACK");
    expect(screen.getByText("2026-01-01 to 2026-12-31")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /activate/i })).toBeNull();
  });

  it("posts the mapped operations and shows hours and calendar days", async () => {
    const fetchMock = stubRoutes((url) => (url.endsWith("lead-time") || url.endsWith(":lead-time") ? ok({ hours: "12.5", calendarDays: 2 }) : ok(detail)));
    render(<RouteVersionPage id="a" />);
    await screen.findByRole("table", { name: "Operations" });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Hours per day"), { target: { value: "8" } });
    fireEvent.click(screen.getByRole("button", { name: "Calculate" }));
    expect(await screen.findByRole("status")).toHaveTextContent("12.5 hours, 2 calendar days");
    const [, init] = fetchMock.mock.calls.find(([url]) => url.endsWith(":lead-time"))!;
    const body = JSON.parse((init as RequestInit).body as string);
    expect(body.quantity).toBe("5");
    expect(body.hoursPerDay).toBe("8");
    expect(body.operations[0]).toEqual({ no: 10, next: 20, priority: "primary", setup: "1", run: "2", processQty: "1", queueBefore: "0.5", queueAfter: "0.25", transit: "0.1", efficiencyPct: "90" });
  });

  it("keeps the inputs and shows the problem title when lead time fails", async () => {
    stubRoutes((url) => (url.endsWith(":lead-time") ? problem(422, "COM-0002", "Hours per day must be positive") : ok(detail)));
    render(<RouteVersionPage id="a" />);
    await screen.findByRole("table", { name: "Operations" });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Hours per day"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Calculate" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Hours per day must be positive");
    expect(screen.getByLabelText("Quantity")).toHaveValue(5);
  });

  it("shows not-found copy for ENG-ROUTE-NOTFOUND", async () => {
    stubRoutes(() => problem(404, "ENG-ROUTE-NOTFOUND", "Route version not found"));
    render(<RouteVersionPage id="missing" />);
    expect(await screen.findByRole("alert")).toHaveTextContent("This route version does not exist");
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });

  it("shows a generic error with Retry and recovers", async () => {
    let calls = 0;
    stubRoutes(() => (++calls === 1 ? { ok: false, status: 500, json: async () => { throw new Error("x"); } } : ok(detail)));
    render(<RouteVersionPage id="a" onBack={() => undefined} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Request failed");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("table", { name: "Operations" })).toBeInTheDocument();
  });
});
