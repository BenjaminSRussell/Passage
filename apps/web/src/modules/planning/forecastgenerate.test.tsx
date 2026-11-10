import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ForecastModel } from "./api/client";
import { generateOutcome, validateGenerate, type GenerateDraft } from "./ForecastGenerate";
import planning from "./module";

const model: ForecastModel = { code: "BASE", version: 3, reductionPrinciple: "transactions", reductionPercent: "0.000000", status: "draft" };
const ITEM = "00000000-0000-0000-0000-0000000000c1";
const SITE = "00000000-0000-0000-0000-0000000000d1";
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) => json({ type: "t", title: "x", status, code, detail }, status);

const result = {
  model: { ...model, version: 4 },
  cellsWritten: 11,
  cellsKept: 1,
  generated: [{ itemId: ITEM, siteId: SITE, model: "ets", seasonLength: 12, shortHistory: false, mape: 0.123, wmape: null, warnings: [] }],
};

function stubApi(onGenerate: (init: RequestInit) => Response) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST" && url.endsWith(":generate")) return onGenerate(init);
    if (url.includes("/forecasts/")) return json({ model, items: [] });
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderForecast() {
  render(
    <MemoryRouter initialEntries={["/planning/forecast"]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const draft: GenerateDraft = {
  itemId: ITEM,
  siteId: SITE,
  history: "10, 12.5\n0 8",
  firstPeriodStart: "2026-12-01",
  horizonPeriods: "6",
  model: "auto",
  autoSeason: true,
};

async function fill(values: Partial<Record<string, string>>) {
  const form = await screen.findByRole("form", { name: "Statistical forecast" });
  const labels: Record<string, string> = { item: "Item", site: "Site", history: "Monthly history", first: "First month to forecast" };
  for (const [key, value] of Object.entries(values)) {
    fireEvent.change(within(form).getByLabelText(labels[key]), { target: { value } });
  }
  return form;
}

afterEach(() => vi.unstubAllGlobals());

describe("validateGenerate", () => {
  it("builds the body of one series", () => {
    const { errors, body } = validateGenerate(draft);
    expect(errors).toEqual({});
    expect(body).toEqual({
      firstPeriodStart: "2026-12-01",
      horizonPeriods: 6,
      series: [{ itemId: ITEM, siteId: SITE, history: ["10", "12.5", "0", "8"], model: "auto", autoSeason: true }],
    });
  });

  it("names every field that is wrong", () => {
    const { errors, body } = validateGenerate({ ...draft, itemId: "x", siteId: "", history: "1, -2", firstPeriodStart: "2026-12-02", horizonPeriods: "25" });
    expect(body).toBeNull();
    expect(Object.keys(errors).sort()).toEqual(["firstPeriodStart", "history", "horizonPeriods", "itemId", "siteId"]);
    expect(validateGenerate({ ...draft, history: "  " }).errors.history).toMatch(/1 to 600/);
    expect(validateGenerate({ ...draft, history: "1.1234567" }).errors.history).toMatch(/6 decimals/);
  });

  it("words the outcome", () => {
    expect(generateOutcome("BASE", result)).toContain("11 statistical cells written, 1 left as they were");
    expect(generateOutcome("BASE", { ...result, cellsKept: 0 })).toBe("Forecast model BASE: 11 statistical cells written. The model is at version 4.");
  });
});

describe("Statistical forecast card (FR-22.9)", () => {
  it("generates under If-Match and shows the model the service used", async () => {
    const fetchMock = stubApi(() => json(result));
    renderForecast();
    const form = await fill({ item: ITEM, site: SITE, history: "10 12 14 16", first: "2026-12-01" });
    fireEvent.click(within(form).getByRole("button", { name: "Generate forecast" }));

    await waitFor(() => expect(screen.getAllByRole("status").map((e) => e.textContent).join(" ")).toContain("11 statistical cells written, 1 left as they were"));
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST")!;
    expect(post[0]).toBe("/api/v1/planning/forecasts/BASE:generate");
    expect((post[1]?.headers as Record<string, string>)["If-Match"]).toBe('"3"');
    expect(JSON.parse(post[1]?.body as string).series[0].history).toEqual(["10", "12", "14", "16"]);
    const table = await screen.findByRole("table", { name: "Generated series" });
    expect(within(table).getByText("ets")).toBeInTheDocument();
    expect(within(table).getByText("12.3%")).toBeInTheDocument();
    expect(within(table).getByText("n/a")).toBeInTheDocument();
  });

  it("does not call the service while the form is invalid", async () => {
    const fetchMock = stubApi(() => json(result));
    renderForecast();
    const form = await fill({ item: "nope" });
    fireEvent.click(within(form).getByRole("button", { name: "Generate forecast" }));
    expect(await screen.findByText("Enter the item id as a UUID.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
  });

  it.each([
    [409, "PLAN-FEATURE-OFF", "The statistical forecast is not enabled for this deployment"],
    [502, "PLAN-FORECAST-UNAVAILABLE", "The forecast service did not answer; try again later"],
    [412, "conflict_version", "Someone else changed this forecast; reload it and try again"],
    [422, "PLAN-FORECAST-RANGE", "The forecast is too large to store; check the history for a wrong quantity"],
    [500, "X", "The forecast was not generated"],
  ])("explains a %i refusal", async (status, code, title) => {
    stubApi(() => problem(status, code, "refused"));
    renderForecast();
    const form = await fill({ item: ITEM, site: SITE, history: "1 2 3", first: "2026-12-01" });
    fireEvent.click(within(form).getByRole("button", { name: "Generate forecast" }));
    expect(await screen.findByText(title)).toBeInTheDocument();
    expect(screen.getByText(new RegExp(code))).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Generated series" })).not.toBeInTheDocument();
  });
});
