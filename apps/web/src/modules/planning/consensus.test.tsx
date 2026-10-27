import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ForecastLine, ForecastModel, Message } from "./api/client";
import { consensusOutcome } from "./ForecastPage";
import planning from "./module";

const draft: ForecastModel = { code: "BASE", version: 3, reductionPrinciple: "transactions", reductionPercent: "0.000000", status: "draft" };
const submitted: ForecastModel = { ...draft, version: 4, status: "submitted", submittedBy: "alice" };
const published: ForecastModel = { ...submitted, version: 5, status: "published", decidedBy: "bob", publishedAt: "2026-10-26T21:00:00Z" };
const cell: ForecastLine = {
  itemId: "00000000-0000-0000-0000-0000000000c1",
  siteId: "00000000-0000-0000-0000-0000000000d1",
  periodStart: "2026-11-02",
  periodEnd: "2026-11-08",
  source: "manual",
  qty: "100.000000",
  salesQty: "30.000000",
  reducedQty: "70.000000",
};
const publication = { items: 2, horizonPeriods: 6, periodFrom: "2026-11-02", periodTo: "2027-04-30" };

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) => json({ type: "t", title: "x", status, code, detail }, status);

/**
 * The forecast endpoints: GET lines returns the model in `state.model`, a
 * consensus POST answers with `step` (or a problem) and moves the state on.
 */
function forecastApi(initial: ForecastModel, step: (url: string) => { model: ForecastModel; publication?: unknown } | Response) {
  const state = { model: initial };
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      const out = step(url);
      if (out instanceof Response) return out;
      state.model = out.model;
      return json(out);
    }
    if (url.includes("/forecasts/")) return json({ model: state.model, items: [{ ...cell, source: state.model.status === "published" ? "consensus" : "manual" }] });
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const posts = (fetchMock: ReturnType<typeof forecastApi>) => fetchMock.mock.calls.filter(([, init]) => init?.method === "POST");

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("Forecast consensus (FR-22.9)", () => {
  it("submits a draft with its version, then locks the cells", async () => {
    const fetchMock = forecastApi(draft, () => ({ model: submitted }));
    renderAt("/planning/forecast");
    expect(await screen.findByRole("heading", { name: "Consensus" })).toBeInTheDocument();
    expect(screen.getByText("Draft", { selector: "dd *" })).toBeInTheDocument();
    expect(screen.getByRole("form", { name: "Forecast cell" })).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "Submit for approval" }));

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Forecast model BASE is submitted for approval."));
    const [url, init] = posts(fetchMock)[0];
    expect(url).toBe("/api/v1/planning/forecasts/BASE:submit");
    expect((init?.headers as Record<string, string>)["If-Match"]).toBe('"3"');
    expect(await screen.findByText("Cells are locked")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Forecast cell" })).not.toBeInTheDocument();
    expect(screen.getByText("Submitted for approval", { selector: "dd *" })).toBeInTheDocument();
    expect(screen.getByText(/Submitted by alice/)).toBeInTheDocument();
  });

  it("publishes a submitted model and reports what was published", async () => {
    const fetchMock = forecastApi(submitted, (url) => (url.endsWith(":publish") ? { model: published, publication } : problem(500, "X", "unexpected")));
    renderAt("/planning/forecast");
    expect(await screen.findByRole("button", { name: "Return to draft" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Publish consensus" }));

    await waitFor(() =>
      expect(screen.getByRole("status")).toHaveTextContent("Forecast model BASE is published: 2 item and site series over 6 periods, 2026-11-02 to 2027-04-30."),
    );
    expect((posts(fetchMock)[0][1]?.headers as Record<string, string>)["If-Match"]).toBe('"4"');
    expect(await screen.findByText(/Published on 2026-10-26 by bob/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Publish consensus" })).not.toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Forecast cells" });
    await waitFor(() => expect(within(table).getByText("Consensus")).toBeInTheDocument());
  });

  it("returns a submitted model to draft and reopens the cell form", async () => {
    forecastApi(submitted, (url) => (url.endsWith(":reject") ? { model: { ...draft, version: 5, decidedBy: "bob" } } : problem(500, "X", "unexpected")));
    renderAt("/planning/forecast");
    fireEvent.click(await screen.findByRole("button", { name: "Return to draft" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Forecast model BASE is back in draft"));
    expect(await screen.findByRole("form", { name: "Forecast cell" })).toBeInTheDocument();
  });

  it("explains a segregation-of-duties refusal and keeps the model as it was", async () => {
    forecastApi(submitted, () => problem(403, "PLAN-CONSENSUS-SOD", "the consensus must be decided by someone else"));
    renderAt("/planning/forecast");
    fireEvent.click(await screen.findByRole("button", { name: "Publish consensus" }));
    expect(await screen.findByText("Someone who neither submitted nor edited this forecast must decide it")).toBeInTheDocument();
    expect(screen.getByText(/PLAN-CONSENSUS-SOD/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Publish consensus" })).toBeEnabled();
  });

  it("offers no step on a published model", async () => {
    forecastApi(published, () => problem(500, "X", "unexpected"));
    renderAt("/planning/forecast");
    expect(await screen.findByText("Cells are locked")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Submit|Publish|Return/ })).not.toBeInTheDocument();
  });

  it("shows no consensus step before the model has cells", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => problem(404, "PLAN-FORECAST-NOT-FOUND", "forecast model not found")),
    );
    renderAt("/planning/forecast");
    expect(await screen.findByRole("heading", { name: "Forecast model BASE has no cells yet" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Submit for approval" })).not.toBeInTheDocument();
    expect(screen.getByRole("form", { name: "Forecast cell" })).toBeInTheDocument();
  });

  it("consensusOutcome words each step", () => {
    expect(consensusOutcome("reject", { model: draft })).toBe("Forecast model BASE is back in draft; its cells can be changed again.");
    expect(consensusOutcome("publish", { model: published, publication: null })).toBe("Forecast model BASE is published.");
  });
});

describe("Increase messages (FR-22.7)", () => {
  it("lists them and filters by increase", async () => {
    const increase: Message = { action: "increase", itemId: cell.itemId, supplyRef: "PO-1", demandRef: "SO-1", suggestedDate: "2026-11-03", delayDays: 0, qty: "10" };
    const shortage: Message = { ...increase, action: "shortage", supplyRef: "" };
    const fetchMock = vi.fn(async (url: string) => json({ items: url.includes("action=increase") ? [increase] : [increase, shortage] }));
    vi.stubGlobal("fetch", fetchMock);
    renderAt("/planning/messages");
    const table = await findTable({ name: "Messages" });
    await waitFor(() => expect(within(table).getByText("Increase")).toBeInTheDocument());
    fireEvent.change(screen.getByLabelText("Action"), { target: { value: "increase" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("action=increase"))).toBe(true));
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Messages" })).queryByText("Shortage")).not.toBeInTheDocument());
  });
});
