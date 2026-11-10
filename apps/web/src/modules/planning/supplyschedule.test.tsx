import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { PlanProjection } from "./api/client";
import planning from "./module";
import { daysCell, periodCell, readSelection, validateDraft } from "./SupplySchedulePage";

const fg = "00000000-0000-0000-0000-0000000000b1";
const ghost = "00000000-0000-0000-0000-0000000000b2";

const period = (startDay: number, over: Partial<PlanProjection["items"][0]["periods"][0]> = {}) => ({
  startDay,
  endDay: startDay + 6,
  startDate: `2026-11-${String(2 + startDay).padStart(2, "0")}`,
  endDate: "2026-11-08",
  demand: "10",
  supply: "0",
  planned: "0",
  closingOnHand: "10",
  shortage: false,
  value: null,
  daysOfSupply: null,
  ...over,
});

const projection: PlanProjection = {
  planCode: "MAIN",
  version: 3,
  asOf: "2026-11-02",
  siteId: "00000000-0000-0000-0000-0000000000a1",
  bucket: "week",
  horizonDays: 14,
  currency: "EUR",
  items: [
    {
      itemId: fg,
      planned: true,
      openingOnHand: "20.000000",
      shortagePeriods: 1,
      periods: [
        period(0, { value: "25.00", daysOfSupply: "21.43" }),
        period(7, { demand: "120.000000", planned: "110", closingOnHand: "-5.500000", shortage: true, value: "-13.75" }),
      ],
      totals: { demand: "130", supply: "0", planned: "110", closingOnHand: "-5.5" },
    },
    { itemId: ghost, planned: false, openingOnHand: "0", shortagePeriods: 0, periods: [], totals: { demand: "0", supply: "0", planned: "0", closingOnHand: "0" } },
  ],
};

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

function Location() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname + location.search}</div>;
}

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
      <Location />
    </MemoryRouter>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("Supply schedule page", () => {
  it("shows a schedule per item with shortages, value and days of supply", async () => {
    const fetchMock = vi.fn(async (_url: string) => json(projection));
    vi.stubGlobal("fetch", fetchMock);
    renderAt(`/planning/supply-schedule?plan=MAIN&items=${fg},${ghost}&bucket=week&horizon=14&cost=2.5&currency=EUR`);
    const table = await findTable({ name: "Supply schedule" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getAllByRole("cell")[0]).toHaveTextContent("2026-11-02 to 2026-11-08");
    expect(within(rows[0]).getAllByRole("cell")[4]).toHaveTextContent("10");
    expect(within(rows[0]).getAllByRole("cell")[5]).toHaveTextContent("25.00");
    expect(within(rows[0]).getAllByRole("cell")[6]).toHaveTextContent("21.43 days");
    expect(within(rows[1]).getAllByRole("cell")[4]).toHaveTextContent("-5.5");
    expect(within(rows[1]).getAllByRole("cell")[1]).toHaveTextContent("120");
    expect(screen.getByText(/Opening on-hand 20; 1 period short\./)).toBeInTheDocument();
    expect(screen.getByText("00000000-0000-0000-0000-0000000000a1")).toBeInTheDocument();
    expect(screen.getByText(/as of 2026-11-02/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Not in this plan" })).toBeInTheDocument();
    expect(fetchMock.mock.calls[0][0]).toBe(
      `/api/v1/planning/plans/MAIN/projection?itemId=${fg}&itemId=${ghost}&bucket=week&horizonDays=14&unitCost=2.5&currency=EUR`,
    );
  });

  it("asks for items and calls nothing until some are chosen", async () => {
    const fetchMock = vi.fn(async (_url: string) => json(projection));
    vi.stubGlobal("fetch", fetchMock);
    renderAt("/planning/supply-schedule");
    expect(screen.getByRole("heading", { name: "Choose items" })).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Show schedule" }));
    expect(await screen.findByText("Enter at least one item id.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Items"), { target: { value: "RM-300" } });
    fireEvent.click(screen.getByRole("button", { name: "Show schedule" }));
    expect(await screen.findByText(/Every item id must be a UUID/)).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Items"), { target: { value: fg } });
    fireEvent.change(screen.getByLabelText("Plan"), { target: { value: "nightly" } });
    fireEvent.click(screen.getByRole("button", { name: "Show schedule" }));
    await waitFor(() =>
      expect(screen.getByTestId("location").textContent).toBe(`/planning/supply-schedule?plan=NIGHTLY&items=${fg}&bucket=week&horizon=84`),
    );
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  });

  it("shows a loading table while the request is pending", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => undefined)));
    renderAt(`/planning/supply-schedule?items=${fg}`);
    expect(await screen.findByRole("table", { name: "Supply schedule" })).toHaveAttribute("aria-busy", "true");
  });

  it("shows the problem and no table on error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json({ type: "t", title: "x", status: 404, code: "PLAN-VERSION-NOT-FOUND", detail: "plan MAIN has no current version" }, 404)),
    );
    renderAt(`/planning/supply-schedule?items=${fg}`);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("PLAN-VERSION-NOT-FOUND");
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("validates the selection and reads the URL defensively", () => {
    expect(validateDraft({ plan: "main", items: fg, bucket: "week", horizon: "84", cost: "", currency: "" })).toEqual({});
    expect(validateDraft({ plan: "m", items: fg, bucket: "week", horizon: "0", cost: "x", currency: "" })).toMatchObject({
      plan: expect.any(String),
      horizon: expect.any(String),
      cost: expect.any(String),
    });
    expect(validateDraft({ plan: "MAIN", items: fg, bucket: "week", horizon: "84", cost: "2", currency: "" }).currency).toMatch(/together/);
    const many = Array.from({ length: 21 }, (_, i) => `00000000-0000-0000-0000-${String(i).padStart(12, "0")}`).join(",");
    expect(validateDraft({ plan: "MAIN", items: many, bucket: "week", horizon: "84", cost: "", currency: "" }).items).toMatch(/at most 20/);
    const read = readSelection(new URLSearchParams(`plan=x&items=${fg},nope,${fg.toUpperCase()}&bucket=year&horizon=9999&cost=-1&currency=eur`));
    expect(read).toEqual({ plan: "MAIN", items: [fg], bucket: "week", horizon: 84, cost: "", currency: "" });
    expect(periodCell({ startDate: "2026-12-01", endDate: "2026-12-01" })).toBe("2026-12-01");
    expect(periodCell({ startDate: "2026-12-01", endDate: "2026-12-31" })).toBe("2026-12-01 to 2026-12-31");
    expect(daysCell(null)).toBe("—");
    expect(daysCell("21.430000")).toBe("21.43 days");
  });
});
