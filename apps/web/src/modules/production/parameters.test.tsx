import { findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { listProductionParameters, saveProductionParameters, type ProductionParameters } from "./api/client";
import production from "./module";
import { blankParameters, validateParameters } from "./ParametersPage";
import { json, problem } from "./testData";

afterEach(() => vi.unstubAllGlobals());

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const calls = (mock: ReturnType<typeof vi.fn>) => mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;

function renderParameters() {
  return render(
    <MemoryRouter initialEntries={["/production/parameters"]}>
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

const stored: ProductionParameters = { ...blankParameters(), rafTolerancePct: "2.5", asBuiltRequired: true, version: 2 };
const defaults = { ...blankParameters(), overConsumptionPct: "15" };

describe("production parameters (PRD 4.21.13)", () => {
  it("validates like the service", () => {
    expect(validateParameters(blankParameters())).toEqual({});
    const errors = validateParameters({ ...blankParameters(), siteId: "site", rafTolerancePct: "101", overConsumptionPct: "1000.5", splitAllocation: "x" as never });
    expect(Object.keys(errors).sort()).toEqual(["overConsumptionPct", "rafTolerancePct", "siteId", "splitAllocation"]);
    expect(validateParameters({ ...blankParameters(), rafTolerancePct: "1.234" }).rafTolerancePct).toBeDefined();
  });

  it("lists the stored rows with the defaults and replaces under If-Match", async () => {
    const fetchMock = stubFetch((_url, init) => (init?.method === "PUT" ? json({ ...stored, version: 3, siteId: "All" }) : json({ items: [stored], defaults })));
    expect(await listProductionParameters()).toEqual({ items: [stored], defaults });
    await saveProductionParameters(blankParameters());
    await saveProductionParameters(blankParameters(), 2);
    const puts = calls(fetchMock).filter(([, init]) => init?.method === "PUT");
    expect((puts[0][1]?.headers as Record<string, string>)["If-Match"]).toBeUndefined();
    expect((puts[1][1]?.headers as Record<string, string>)["If-Match"]).toBe('"2"');
  });

  it("shows the rows, edits one and saves it", async () => {
    const fetchMock = stubFetch((_url, init) => (init?.method === "PUT" ? json({ ...stored, version: 3 }) : json({ items: [stored], defaults })));
    const { container } = renderParameters();
    const table = await findTable({ name: "Production parameters" });
    expect(within(table).getByText("2.5")).toBeInTheDocument();
    expect(checkAxeAA(container)).toEqual([]);

    await userEvent.click(within(table).getByText("2.5"));
    const form = screen.getByRole("form", { name: "Production parameters" });
    await userEvent.clear(within(form).getByLabelText(/RAF tolerance/));
    await userEvent.type(within(form).getByLabelText(/RAF tolerance/), "4");
    await userEvent.selectOptions(within(form).getByLabelText("Estimate on create"), "yes");
    await userEvent.selectOptions(within(form).getByLabelText(/Split allocation default/), "keep-on-original");
    await userEvent.click(within(form).getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("Production parameters for All saved (version 3).")).toBeInTheDocument();
    const [, put] = calls(fetchMock).find(([, init]) => init?.method === "PUT")!;
    expect((put?.headers as Record<string, string>)["If-Match"]).toBe('"2"');
    expect(JSON.parse(String(put?.body))).toMatchObject({ siteId: "All", rafTolerancePct: "4", estimateOnCreate: true, splitAllocation: "keep-on-original", asBuiltRequired: true });
  });

  it("starts a new row from the service defaults", async () => {
    stubFetch(() => json({ items: [], defaults }));
    renderParameters();
    expect(await screen.findByRole("heading", { name: "No parameters yet" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "New parameters" }));
    expect(within(screen.getByRole("form", { name: "Production parameters" })).getByLabelText(/Over-consumption tolerance/)).toHaveValue("15");
  });

  it("sends a site that already has parameters to its row instead of creating it again", async () => {
    const fetchMock = stubFetch(() => json({ items: [stored], defaults }));
    renderParameters();
    await findTable({ name: "Production parameters" });
    await userEvent.click(screen.getByRole("button", { name: "New parameters" }));
    const form = screen.getByRole("form", { name: "Production parameters" });
    await userEvent.click(within(form).getByRole("button", { name: "Save parameters" }));
    expect(await within(form).findByText(/Parameters for this site are already saved/)).toBeInTheDocument();
    expect(calls(fetchMock).some(([, init]) => init?.method === "PUT")).toBe(false);
  });

  it("refuses an invalid form and shows the service's refusal", async () => {
    const fetchMock = stubFetch((_url, init) =>
      init?.method === "PUT" ? problem(412, "conflict_version", "the parameters were changed by another user") : json({ items: [], defaults }),
    );
    renderParameters();
    expect(await screen.findByRole("heading", { name: "No parameters yet" })).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Production parameters" });
    await userEvent.clear(within(form).getByLabelText(/RAF tolerance/));
    await userEvent.type(within(form).getByLabelText(/RAF tolerance/), "abc");
    await userEvent.click(within(form).getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("Enter a percentage from 0 to 100, with up to 2 decimals.")).toBeInTheDocument();
    expect(calls(fetchMock).some(([, init]) => init?.method === "PUT")).toBe(false);

    await userEvent.clear(within(form).getByLabelText(/RAF tolerance/));
    await userEvent.type(within(form).getByLabelText(/RAF tolerance/), "5");
    await userEvent.click(within(form).getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("the parameters were changed by another user (conflict_version)")).toBeInTheDocument();
    await waitFor(() => expect(calls(fetchMock).filter(([, init]) => init?.method === "PUT")).toHaveLength(1));
  });

  it("shows loading and error states", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    const first = renderParameters();
    expect(await screen.findByRole("table", { name: "Production parameters" })).toHaveAttribute("aria-busy", "true");
    first.unmount();
    vi.unstubAllGlobals();
    stubFetch(() => problem(403, "AUTH-0003", "missing duty"));
    renderParameters();
    expect(await screen.findByText("You need the View production setup duty to see the parameters.")).toBeInTheDocument();
  });
});
