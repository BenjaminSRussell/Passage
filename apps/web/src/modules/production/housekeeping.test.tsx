import { findTable } from "../../test-utils";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { runHousekeeping, type HousekeepingOrder } from "./api/client";
import production from "./module";
import { json, problem } from "./testData";

afterEach(() => vi.unstubAllGlobals());

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const calls = (mock: ReturnType<typeof vi.fn>) => mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;
const header = (init: RequestInit | undefined, name: string) => (init?.headers as Record<string, string>)[name];

function renderHousekeeping() {
  return render(
    <MemoryRouter initialEntries={["/production/housekeeping"]}>
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

const stale = (n: number, ageDays: number): HousekeepingOrder => ({
  orderId: `0195d6a0-0000-7000-8000-00000000000${n}`,
  orderNumber: `PO-00000${n}`,
  siteId: "0195d6a0-0000-7000-8000-0000000000a1",
  productId: "0195d6a0-0000-7000-8000-0000000000f1",
  status: "Created",
  quantity: "100",
  qtyReportedFinished: "0",
  version: 1,
  createdAt: "2026-07-01T00:00:00Z",
  ageDays,
});

const envelope = { job: "cancel-stale-created-orders", legalEntityId: "e1", total: 2, truncated: false, succeeded: 0, failed: 0, results: [] };

describe("Order housekeeping (PRD 4.21.16)", () => {
  it("sends a dry run without an Idempotency-Key and a run with one", async () => {
    const fetchMock = stubFetch(() => json({ ...envelope, dryRun: true, candidates: [stale(1, 120)] }));
    const dry = await runHousekeeping("cancel-stale", true, "key-1");
    expect(dry.candidates).toHaveLength(1);
    await runHousekeeping("auto-end", false, "key-2");
    const [[dryUrl, dryInit], [runUrl, runInit]] = calls(fetchMock);
    expect(dryUrl).toBe("/api/v1/production/orders:cancel-stale");
    expect(JSON.parse(String(dryInit?.body))).toEqual({ dryRun: true });
    expect(header(dryInit, "Idempotency-Key")).toBeUndefined();
    expect(runUrl).toBe("/api/v1/production/orders:auto-end");
    expect(JSON.parse(String(runInit?.body))).toEqual({ dryRun: false });
    expect(header(runInit, "Idempotency-Key")).toBe("key-2");
  });

  it("finds the stale orders, confirms, runs and lists a refused order", async () => {
    const candidates = [stale(1, 120), stale(2, 95)];
    const fetchMock = stubFetch((url, init) => {
      const body = JSON.parse(String(init?.body));
      if (!url.endsWith(":cancel-stale")) {
        return json({ ...envelope, job: "auto-end-fully-reported", dryRun: body.dryRun, total: 0, candidates: [] });
      }
      return body.dryRun
        ? json({ ...envelope, dryRun: true, candidates })
        : json({
            ...envelope,
            dryRun: false,
            candidates,
            succeeded: 1,
            failed: 1,
            code: "PRODUCTION_BULK_PARTIAL",
            results: [
              { orderId: candidates[0].orderId, ok: true, status: "Cancelled", version: 2 },
              { orderId: candidates[1].orderId, ok: false, code: "PRODUCTION_ILLEGAL_TRANSITION", detail: "the order changed" },
            ],
          });
    });
    const { container } = renderHousekeeping();
    expect(screen.getByRole("heading", { name: "Order housekeeping" })).toBeInTheDocument();
    const [findStale] = screen.getAllByRole("button", { name: "Find orders" });
    await userEvent.click(findStale);
    const table = await findTable({ name: "Cancel stale orders candidates" });
    expect(within(table).getByText("PO-000001")).toBeInTheDocument();
    expect(within(table).getByText("120")).toBeInTheDocument();
    expect(checkAxeAA(container)).toEqual([]);

    await userEvent.click(screen.getByRole("button", { name: "Cancel 2 orders" }));
    expect(screen.getByText("Cancel 2 orders?")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Yes, cancel them" }));
    expect(await screen.findByText("1 succeeded, 1 failed")).toBeInTheDocument();
    const results = await findTable({ name: "Cancel stale orders results" });
    expect(within(results).getByText("Done")).toBeInTheDocument();
    expect(within(results).getByText("the order changed (PRODUCTION_ILLEGAL_TRANSITION)")).toBeInTheDocument();
    const run = calls(fetchMock).find(([, init]) => JSON.parse(String(init?.body)).dryRun === false)!;
    expect(header(run[1], "Idempotency-Key")).toBeTruthy();
    // the run names the reviewed orders, so it acts on no order the user did not see
    expect(JSON.parse(String(run[1]?.body)).orderIds).toEqual(candidates.map((o) => o.orderId));
  });

  it("says when reviewed orders stopped qualifying, and when a run was a replay", async () => {
    const candidates = [stale(1, 120), stale(2, 95)];
    let replayed = false;
    stubFetch((_url, init) => {
      const body = JSON.parse(String(init?.body));
      if (body.dryRun) {
        return json({ ...envelope, dryRun: true, candidates });
      }
      return json({ ...envelope, dryRun: false, replayed, candidates: [candidates[0]], succeeded: 1, results: [{ orderId: candidates[0].orderId, ok: true, status: "Cancelled", version: 2, replayed }] });
    });
    renderHousekeeping();
    const [findStale] = screen.getAllByRole("button", { name: "Find orders" });
    await userEvent.click(findStale);
    await userEvent.click(await screen.findByRole("button", { name: "Cancel 2 orders" }));
    await userEvent.click(screen.getByRole("button", { name: "Yes, cancel them" }));
    expect(await screen.findByText(/1 of the reviewed orders no longer qualified and were left as they are/)).toBeInTheDocument();

    replayed = true;
    await userEvent.click(findStale);
    await userEvent.click(await screen.findByRole("button", { name: "Cancel 2 orders" }));
    await userEvent.click(screen.getByRole("button", { name: "Yes, cancel them" }));
    expect(await screen.findByText(/This run had already been done; its results are shown again/)).toBeInTheDocument();
    expect(screen.queryByText(/no longer qualified/)).not.toBeInTheDocument();
    expect(within(await findTable({ name: "Cancel stale orders results" })).getByText("Already done")).toBeInTheDocument();
  });

  it("says when nothing qualifies, and notes a truncated list", async () => {
    stubFetch((url) =>
      url.endsWith(":cancel-stale")
        ? json({ ...envelope, dryRun: true, total: 900, truncated: true, candidates: [stale(1, 100)] })
        : json({ ...envelope, job: "auto-end-fully-reported", dryRun: true, total: 0, candidates: [] }),
    );
    renderHousekeeping();
    const [findStale, findEnd] = screen.getAllByRole("button", { name: "Find orders" });
    await userEvent.click(findEnd);
    expect(await screen.findByText("No orders to end")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^End \d/ })).not.toBeInTheDocument();
    await userEvent.click(findStale);
    expect(await screen.findByText(/900 orders qualify; one run takes the first 1/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel 1 order" })).toBeInTheDocument();
  });

  it("explains a missing duty and shows other failures", async () => {
    stubFetch((url) => (url.endsWith(":cancel-stale") ? problem(403, "AUTH-0003", "missing duty production.order.release") : problem(500, "COM-0500", "boom")));
    renderHousekeeping();
    const [findStale, findEnd] = screen.getAllByRole("button", { name: "Find orders" });
    await userEvent.click(findStale);
    expect(await screen.findByText("You need the duty of this command to use this job.")).toBeInTheDocument();
    await userEvent.click(findEnd);
    expect(await screen.findByText(/boom \(COM-0500\)/)).toBeInTheDocument();
  });
});
