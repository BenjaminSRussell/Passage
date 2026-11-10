import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  BUFFER_EXAMPLE,
  CalculatorsPage,
  CONSUMPTION_EXAMPLE,
  formatDays,
  parseBatches,
  parseFlows,
  parseForecastPeriods,
  parseSalesLines,
  parseSources,
  percent,
  QUOTA_EXAMPLE,
  SHELF_LIFE_EXAMPLE,
  SUPPLY_EXAMPLE,
  validateBufferForm,
  validateConsumptionForm,
  validateQuotaForm,
  validateShelfLifeForm,
  validateSupplyForm,
} from "./calculators";
import planning from "./module";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) => json({ type: "t", title: "x", status, code, detail }, status);

/** Stubs fetch for one calculator path and returns the mock. */
function stubCalc(path: string, respond: () => Response | Promise<Response>) {
  const fetchMock = vi.fn(async (url: string, _init?: RequestInit) => {
    if (url === `/api/v1/planning${path}`) return respond();
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const sentBody = (fetchMock: ReturnType<typeof stubCalc>) => JSON.parse(String(fetchMock.mock.calls[0][1]?.body)) as Record<string, unknown>;

const formNamed = (name: string) => screen.getByRole("form", { name });

afterEach(() => vi.unstubAllGlobals());

describe("calculator input parsing", () => {
  it("parses day:qty pairs, treats blank as none, and rejects malformed pairs", () => {
    expect(parseFlows("3:4, -2:6.5")).toEqual({ flows: [{ day: 3, qty: "4" }, { day: -2, qty: "6.5" }] });
    expect(parseFlows("  ")).toEqual({ flows: [] });
    for (const bad of ["3", "3:4:5", "x:4", "3:-4", "3.5:4", "9999:1"]) {
      expect(parseFlows(bad).error, bad).toBeTruthy();
    }
  });

  it("parses batches one per line and rejects duplicates and bad expiry days", () => {
    expect(parseBatches("B1, 100, 20\nB2, 5, -3")).toEqual({
      batches: [
        { id: "B1", qty: "100", expiryDay: 20 },
        { id: "B2", qty: "5", expiryDay: -3 },
      ],
    });
    expect(parseBatches("B1, 100, 20\nB1, 5, 30").error).toMatch(/twice/);
    expect(parseBatches("B1, 100, soon").error).toMatch(/Expiry day/);
    expect(parseBatches("B1, 100").error).toBeTruthy();
  });

  it("parses quota sources with optional priority and blocked flag", () => {
    expect(parseSources("A, vendor, 60, 300, 1\nB, transfer, 40, 100, , blocked\nC, production, 0, 0")).toEqual({
      sources: [
        { id: "A", type: "vendor", quota: "60", allocatedQty: "300", priority: 1 },
        { id: "B", type: "transfer", quota: "40", allocatedQty: "100", blocked: true },
        { id: "C", type: "production", quota: "0", allocatedQty: "0" },
      ],
    });
    expect(parseSources("A, carrier, 1, 0").error).toMatch(/vendor, production or transfer/);
    expect(parseSources("A, vendor, -1, 0").error).toMatch(/0 or more/);
    expect(parseSources("A, vendor, 1, 0, 1, maybe").error).toMatch(/blocked/);
  });

  it("parses forecast periods and sales lines and rejects reversed periods", () => {
    expect(parseForecastPeriods("NOV, 2026-11-01, 2026-11-30, 100").periods).toEqual([
      { id: "NOV", startDate: "2026-11-01", endDate: "2026-11-30", qty: "100" },
    ]);
    expect(parseForecastPeriods("NOV, 2026-11-30, 2026-11-01, 100").error).toMatch(/ends before/);
    expect(parseSalesLines("SO-1, 2026-11-20, 40").sales).toEqual([{ id: "SO-1", date: "2026-11-20", qty: "40" }]);
    expect(parseSalesLines("SO-1, 20/11/2026, 40").error).toMatch(/YYYY-MM-DD/);
  });

  it("formats priority and days of supply", () => {
    expect(percent("0.5306")).toBe("53.06 %");
    expect(formatDays("130")).toBe("130 days");
    expect(formatDays(null)).toBe("—");
  });
});

describe("calculator form validation", () => {
  it("accepts every worked example as filled in", () => {
    expect(validateBufferForm(BUFFER_EXAMPLE)).toEqual({});
    expect(validateQuotaForm(QUOTA_EXAMPLE)).toEqual({});
    expect(validateConsumptionForm(CONSUMPTION_EXAMPLE)).toEqual({});
    expect(validateSupplyForm(SUPPLY_EXAMPLE)).toEqual({});
    expect(validateShelfLifeForm(SHELF_LIFE_EXAMPLE)).toEqual({});
    expect(validateShelfLifeForm({ ...SHELF_LIFE_EXAMPLE, horizonDay: "" })).toEqual({});
  });

  it("names each invalid field", () => {
    expect(Object.keys(validateBufferForm({ ...BUFFER_EXAMPLE, adu: "-1", orderCycleDays: "1.5", onHand: "x" })).sort()).toEqual([
      "adu",
      "onHand",
      "orderCycleDays",
    ]);
    expect(Object.keys(validateQuotaForm({ ...QUOTA_EXAMPLE, qty: "0", sources: "" })).sort()).toEqual(["qty", "sources"]);
    expect(Object.keys(validateConsumptionForm({ ...CONSUMPTION_EXAMPLE, backwardDays: "9999", sales: "x" })).sort()).toEqual([
      "backwardDays",
      "sales",
    ]);
    expect(Object.keys(validateSupplyForm({ ...SUPPLY_EXAMPLE, horizonDays: "0", currency: "eur", demand: "oops" })).sort()).toEqual([
      "currency",
      "demand",
      "horizonDays",
    ]);
    expect(Object.keys(validateShelfLifeForm({ ...SHELF_LIFE_EXAMPLE, demandQty: "-3", batches: "B1" })).sort()).toEqual([
      "batches",
      "demandQty",
    ]);
  });
});

describe("Planning calculators page", () => {
  it("is registered in the planning module under planning.read", () => {
    expect(planning.routes.some((r) => r.path === "/planning/calculators")).toBe(true);
    expect(planning.nav.find((n) => n.to === "/planning/calculators")?.duty).toBe("planning.read");
  });

  it("evaluates the BR-22.11 buffer and shows zone, order and priority", async () => {
    const fetchMock = stubCalc("/ddmrp/buffers:evaluate", () =>
      json({
        adu: "10",
        adjustedAdu: "10",
        zones: { redBase: "50", redSafety: "25", yellow: "100", green: "70", topOfRed: "75", topOfYellow: "175", topOfGreen: "245" },
        qualifiedDemand: "20",
        netFlow: "130",
        plannedQty: "115",
        priority: "0.5306",
        zone: "yellow",
      }),
    );
    render(<CalculatorsPage />);
    expect(screen.getByRole("heading", { level: 2, name: "Planning calculators" })).toBeTruthy();
    fireEvent.click(within(formNamed("DDMRP buffer")).getByRole("button", { name: "Evaluate buffer" }));
    const status = await screen.findByLabelText("Buffer status");
    expect(within(status).getByText("Yellow")).toBeTruthy();
    expect(within(status).getByText("115")).toBeTruthy();
    expect(within(status).getByText("53.06 %")).toBeTruthy();
    expect(sentBody(fetchMock)).toMatchObject({
      adu: "10",
      dltDays: "10",
      onHand: "90",
      openSupply: "60",
      demand: [{ day: 0, qty: "20" }],
      profile: { leadTimeFactor: "0.5", variabilityFactor: "0.5", orderCycleDays: 7, minOrderQty: "0" },
    });
    expect(fetchMock.mock.calls[0][1]?.method).toBe("POST");
  });

  it("shows field errors and sends nothing when the buffer form is invalid", () => {
    const fetchMock = stubCalc("/ddmrp/buffers:evaluate", () => json({}));
    render(<CalculatorsPage />);
    const form = formNamed("DDMRP buffer");
    fireEvent.change(within(form).getByLabelText("Average daily usage"), { target: { value: "-5" } });
    fireEvent.click(within(form).getByRole("button", { name: "Evaluate buffer" }));
    expect(within(form).getByRole("alert").textContent).toMatch(/0 or more/);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("shows the quota assignment, and the PLAN-0009 problem when no source is usable", async () => {
    let answer = () => json({ assignments: [{ sourceId: "B", qty: "100", ratingAfter: "5" }], qty: "100" });
    const fetchMock = stubCalc("/quota-arrangements:allocate", () => answer());
    render(<CalculatorsPage />);
    const form = formNamed("Quota arrangement");
    fireEvent.click(within(form).getByRole("button", { name: "Allocate" }));
    const table = await findTable({ name: "Quota assignments" });
    expect(within(table).getByText("B")).toBeTruthy();
    expect(sentBody(fetchMock)).toMatchObject({ qty: "100", split: false, sources: [{ id: "A", quota: "60" }, { id: "B", quota: "40" }] });

    answer = () => problem(422, "PLAN-0009", "the quota arrangement has no usable source for this quantity");
    fireEvent.click(within(form).getByRole("button", { name: "Allocate" }));
    await waitFor(() => expect(screen.getByText(/no usable source for this quantity/)).toBeTruthy());
    expect(screen.queryByRole("table", { name: "Quota assignments" })).toBeNull();
  });

  it("shows the AC-22.14 gross requirement and per-line consumption", async () => {
    const fetchMock = stubCalc("/strategies:consume-forecast", () =>
      json({
        forecast: [{ id: "NOV", remainingQty: "60" }],
        sales: [{ id: "SO-1", consumedQty: "40", unconsumedQty: "0" }],
        grossQty: "100",
      }),
    );
    render(<CalculatorsPage />);
    fireEvent.click(within(formNamed("Forecast consumption")).getByRole("button", { name: "Consume forecast" }));
    expect((await screen.findByText("Gross requirement 100")).getAttribute("role")).toBe("status");
    expect(within(screen.getByRole("table", { name: "Forecast after consumption" })).getByText("60")).toBeTruthy();
    expect(sentBody(fetchMock)).toEqual({
      mode: "backward-forward",
      backwardDays: 30,
      forwardDays: 0,
      forecast: [{ id: "NOV", startDate: "2026-11-01", endDate: "2026-11-30", qty: "100" }],
      sales: [{ id: "SO-1", date: "2026-11-20", qty: "40" }],
    });
  });

  it("shows a loading table while projecting, then the schedule with value and days of supply", async () => {
    let release: (r: Response) => void = () => undefined;
    const fetchMock = stubCalc("/supply-schedule:project", () => new Promise<Response>((resolve) => (release = resolve)));
    render(<CalculatorsPage />);
    fireEvent.click(within(formNamed("Supply schedule")).getByRole("button", { name: "Project" }));
    const loading = await screen.findByRole("table", { name: "Supply schedule" });
    expect(loading.getAttribute("aria-busy")).toBe("true");
    release(
      json({
        bucket: "week",
        horizonDays: 14,
        currency: "EUR",
        periods: [
          { startDay: 0, endDay: 6, demand: "4", supply: "20", planned: "0", closingOnHand: "26", safetyStock: "5", shortage: false, value: "65.00", daysOfSupply: "130" },
          { startDay: 7, endDay: 13, demand: "30", supply: "0", planned: "0", closingOnHand: "-4", safetyStock: "5", shortage: true, value: "-10.00", daysOfSupply: null },
        ],
        totals: { demand: "34", supply: "20", planned: "0", closingOnHand: "-4" },
      }),
    );
    await waitFor(() => expect(screen.getByRole("table", { name: "Supply schedule" }).getAttribute("aria-busy")).toBeNull());
    const table = screen.getByRole("table", { name: "Supply schedule" });
    expect(within(table).getByText("65.00")).toBeTruthy();
    expect(within(table).getByText("130 days")).toBeTruthy();
    expect(within(table).getByText("—")).toBeTruthy();
    expect(within(table).getByText("Yes")).toBeTruthy();
    expect(within(table).getByRole("columnheader", { name: "Value (EUR)" })).toBeTruthy();
    expect(sentBody(fetchMock)).toMatchObject({ horizonDays: 28, bucket: "week", unitCost: "2.50", currency: "EUR", supply: [{ day: 5, qty: "20" }] });
  });

  it("shows an empty allocation, the shortfall and the expiring batch for AC-22.15", async () => {
    const fetchMock = stubCalc("/shelf-life:allocate", () =>
      json({ allocations: [], shortfallQty: "30", expiringUnpegged: [{ batchId: "B1", qty: "100", expiryDay: 20 }] }),
    );
    render(<CalculatorsPage />);
    fireEvent.click(within(formNamed("Shelf-life check")).getByRole("button", { name: "Check batches" }));
    expect((await screen.findByText(/No batch can serve this demand/)).textContent).toMatch(/Shortfall 30/);
    expect(within(screen.getByRole("table", { name: "Batch allocations" })).getByText("No usable batch")).toBeTruthy();
    const expiring = screen.getByRole("table", { name: "Batches expiring unpegged" });
    expect(within(expiring).getByText("B1")).toBeTruthy();
    expect(within(expiring).getByText("20")).toBeTruthy();
    expect(sentBody(fetchMock)).toEqual({
      demandDay: 5,
      demandQty: "30",
      requiredShelfLifeDays: 30,
      horizonDay: 25,
      batches: [{ id: "B1", qty: "100", expiryDay: 20 }],
    });
  });

  it("reports an unreachable service on the shelf-life check", async () => {
    stubCalc("/shelf-life:allocate", () => {
      throw new TypeError("network down");
    });
    render(<CalculatorsPage />);
    fireEvent.click(within(formNamed("Shelf-life check")).getByRole("button", { name: "Check batches" }));
    expect(await screen.findByText("The shelf-life check failed")).toBeTruthy();
    expect(screen.getByText(/could not be reached/)).toBeTruthy();
    expect(screen.getByText("PLAN-UNKNOWN")).toBeTruthy();
  });
});
